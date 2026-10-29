package ai.alertd.imagethat.data

import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.InputStream

private fun img(dir: String, name: String, size: Long) = Item(childPath(dir, name), name, false, size, MediaKind.IMAGE)
private fun bin(dir: String, name: String, size: Long) = Item(childPath(dir, name), name, false, size, MediaKind.OTHER)
private fun dir(dir: String, name: String) = Item(childPath(dir, name), name, true, 0, MediaKind.OTHER)

/** Counts every call that reaches it, so a test can tell a cache hit from a fetch. */
class CountingSource : VaultSource {
    val tree = HashMap<String, MutableList<Item>>()
    val data = HashMap<String, ByteArray>()
    val thumbs = HashMap<String, ByteArray>()
    var lists = 0
    var sizes = 0
    var opens = 0
    var thumbCalls = 0
    var writes = 0
    var error: SourceError? = null

    override suspend fun list(path: String): List<Item> {
        lists++
        error?.let { throw it }
        return tree[CachingSource.norm(path)]?.toList() ?: emptyList()
    }

    override suspend fun open(path: String, range: LongRange?): InputStream {
        opens++
        error?.let { throw it }
        val b = data[path] ?: throw SourceError.NotFound
        return ByteArrayInputStream(if (range == null) b else b.copyOfRange(range.first.toInt(), range.last.toInt() + 1))
    }

    override suspend fun size(path: String): Long {
        sizes++
        error?.let { throw it }
        return data[path]?.size?.toLong() ?: throw SourceError.NotFound
    }

    override suspend fun thumbnail(path: String, px: Int): ByteArray {
        thumbCalls++
        error?.let { throw it }
        return thumbs[path] ?: ByteArray(0)
    }

    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        writes++
        error?.let { throw it }
        input.use { data[childPath(dir, name)] = it.readBytes() }
    }

    override suspend fun mkdir(path: String) {
        writes++
        error?.let { throw it }
        val (parent, name) = parentOf(path)
        tree.getOrPut(parent) { mutableListOf() }.add(dir(parent, name))
    }

    override suspend fun move(from: String, to: String) { writes++; error?.let { throw it } }

    override suspend fun delete(path: String) {
        writes++
        error?.let { throw it }
        tree.values.forEach { l -> l.removeAll { it.path == path } }
    }
}

class CachingSourceTest {
    private var now = 0L

    private fun inner() = CountingSource().apply {
        tree["/"] = mutableListOf(img("/", "a.jpg", 10), bin("/", "b.bin", 10), img("/", "big.jpg", 1000), dir("/", "d"))
        tree["/d"] = mutableListOf(img("/d", "c.jpg", 10), dir("/d", "sub"))
        tree["/d/sub"] = mutableListOf(img("/d/sub", "e.jpg", 10))
        data["/a.jpg"] = ByteArray(10) { 1 }
        data["/b.bin"] = ByteArray(10) { 2 }
        data["/big.jpg"] = ByteArray(1000) { 3 }
        data["/d/c.jpg"] = ByteArray(10) { 4 }
        thumbs["/a.jpg"] = ByteArray(40) { 5 }
        thumbs["/d/c.jpg"] = ByteArray(40) { 6 }
        thumbs["/d/sub/e.jpg"] = ByteArray(40) { 7 }
    }

    private fun cached(
        src: CountingSource,
        ttl: Long = 1_000,
        thumbBudget: Long = 1_000,
        fileBudget: Long = 1_000,
        maxFile: Long = 100,
        maxListings: Int = 512,
    ) = CachingSource(src, ttl, thumbBudget, fileBudget, maxFile, maxListings) { now }

    @Test fun listingsAreServedFromMemoryUntilTheTtl() = runTest {
        val src = inner()
        val c = cached(src, ttl = 1_000)
        assertEquals(listOf("a.jpg", "b.bin", "big.jpg", "d"), c.list("/").map { it.name })
        c.list("/"); c.list("")
        assertEquals(1, src.lists)
        now = 999
        c.list("/")
        assertEquals(1, src.lists)
        now = 1_000
        c.list("/")
        assertEquals(2, src.lists)
    }

    @Test fun sizeComesFromTheCachedListingWhenItCan() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/d")
        assertEquals(10L, c.size("/d/c.jpg"))
        assertEquals(0, src.sizes)
        // Not in the listing: the source decides (a file another client added, or a real NotFound).
        try { c.size("/d/nope.jpg"); fail() } catch (e: SourceError.NotFound) {}
        assertEquals(1, src.sizes)
        // Parent not listed yet: still the source, nothing is listed on the side.
        assertEquals(10L, c.size("/a.jpg"))
        assertEquals(2, src.sizes)
        assertEquals(1, src.lists)
    }

    @Test fun thumbnailsAreCachedIncludingNoPreview() = runTest {
        val src = inner()
        val c = cached(src)
        assertArrayEquals(ByteArray(40) { 5 }, c.thumbnail("/a.jpg", 256))
        assertArrayEquals(ByteArray(40) { 5 }, c.thumbnail("/a.jpg", 256))
        assertEquals(1, src.thumbCalls)
        c.thumbnail("/a.jpg", 128)
        assertEquals(2, src.thumbCalls)
        assertEquals(0, c.thumbnail("/b.bin", 256).size)
        assertEquals(0, c.thumbnail("/b.bin", 256).size)
        assertEquals(3, src.thumbCalls)
    }

    @Test fun thumbnailCacheEvictsTheLeastRecentlyUsedWithinItsByteBudget() = runTest {
        val src = inner()
        val c = cached(src, thumbBudget = 100)
        c.thumbnail("/a.jpg", 256)      // 40 bytes
        c.thumbnail("/d/c.jpg", 256)    // 80
        c.thumbnail("/a.jpg", 256)      // hit; a.jpg is now the most recent
        c.thumbnail("/d/sub/e.jpg", 256) // 120 > 100: c.jpg goes
        assertEquals(3, src.thumbCalls)
        c.thumbnail("/a.jpg", 256)
        c.thumbnail("/d/sub/e.jpg", 256)
        assertEquals(3, src.thumbCalls)
        c.thumbnail("/d/c.jpg", 256)
        assertEquals(4, src.thumbCalls)
        assertEquals(2, c.stats().thumbs)
    }

    @Test fun aWholeImageIsKeptOnceReadToTheEnd() = runTest {
        val src = inner()
        val c = cached(src, maxFile = 100)
        c.list("/")
        assertArrayEquals(ByteArray(10) { 1 }, c.open("/a.jpg").use { it.readBytes() })
        assertEquals(1, src.opens)
        assertArrayEquals(ByteArray(10) { 1 }, c.open("/a.jpg").use { it.readBytes() })
        assertEquals(1, src.opens)
        assertEquals(1, c.stats().files)
        // Single-byte reads reach the end too.
        c.open("/d/c.jpg").use { while (it.read() >= 0) Unit }
        c.list("/d")
        c.open("/d/c.jpg").use { it.readBytes() }
        assertEquals(3, src.opens) // "/d" was not listed before the first open: not cacheable then
        c.open("/d/c.jpg").use { it.readBytes() }
        assertEquals(3, src.opens)
    }

    @Test fun partialRangedLargeAndNonImageReadsAreNotKept() = runTest {
        val src = inner()
        val c = cached(src, maxFile = 100)
        c.list("/")
        c.open("/a.jpg").use { it.read(ByteArray(4)) } // closed early
        c.open("/a.jpg", 0L..3L).use { it.readBytes() }  // a range
        c.open("/b.bin").use { it.readBytes() }           // not an image
        c.open("/big.jpg").use { it.readBytes() }         // over the per-file limit
        assertEquals(0, c.stats().files)
        assertEquals(4, src.opens)
        // The range read is answered correctly and never from the file cache.
        assertArrayEquals(ByteArray(4) { 1 }, c.open("/a.jpg", 0L..3L).use { it.readBytes() })
        assertEquals(5, src.opens)
    }

    @Test fun aStreamLongerThanTheListingSaidIsNotKept() = runTest {
        val src = inner()
        src.data["/a.jpg"] = ByteArray(12) { 9 } // the listing says 10
        val c = cached(src)
        c.list("/")
        assertEquals(12, c.open("/a.jpg").use { it.readBytes() }.size)
        assertEquals(0, c.stats().files)
        src.data["/a.jpg"] = ByteArray(8) { 9 } // shorter than the listing says
        c.open("/a.jpg").use { it.readBytes() }
        assertEquals(0, c.stats().files)
    }

    @Test fun uploadAndMkdirForgetTheFolderTheyChange() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/"); c.list("/d")
        c.open("/a.jpg").use { it.readBytes() }
        c.thumbnail("/a.jpg", 256)
        assertEquals(2, src.lists)
        c.upload("/", "a.jpg", ByteArrayInputStream(ByteArray(10) { 8 }), 10) // overwrite
        assertEquals(0, c.stats().files)
        assertEquals(0, c.stats().thumbs)
        c.list("/")
        assertEquals(3, src.lists)
        c.list("/d")
        assertEquals(3, src.lists) // untouched folder still cached
        c.mkdir("/d/new")
        c.list("/d")
        assertEquals(4, src.lists)
    }

    @Test fun deleteForgetsTheParentAndTheWholeSubtree() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/"); c.list("/d"); c.list("/d/sub")
        c.thumbnail("/a.jpg", 256); c.thumbnail("/d/c.jpg", 256); c.thumbnail("/d/sub/e.jpg", 256)
        c.open("/a.jpg").use { it.readBytes() }
        c.open("/d/c.jpg").use { it.readBytes() }
        c.delete("/d")
        val s = c.stats()
        assertEquals(0, s.listings) // "/" (parent), "/d" and "/d/sub"
        assertEquals(1, s.thumbs)   // only a.jpg
        assertEquals(1, s.files)
        c.thumbnail("/a.jpg", 256)
        assertEquals(3, src.thumbCalls)
    }

    @Test fun moveForgetsBothEndsAndTheirSubtrees() = runTest {
        val src = inner()
        src.tree["/x"] = mutableListOf()
        val c = cached(src)
        c.list("/"); c.list("/d"); c.list("/d/sub"); c.list("/x")
        c.thumbnail("/d/sub/e.jpg", 256)
        c.move("/d/sub", "/x/sub")
        assertEquals(1, c.stats().listings) // only "/" remains
        assertEquals(0, c.stats().thumbs)
        c.list("/")
        assertEquals(4, src.lists)
    }

    @Test fun aWriteThatFailsStillForgetsTheFolder() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/")
        src.error = SourceError.Other("boom")
        try { c.mkdir("/n"); fail() } catch (e: SourceError.Other) {}
        src.error = null
        c.list("/")
        assertEquals(2, src.lists)
    }

    @Test fun lockedEmptiesTheCacheBeforeItIsRethrown() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/"); c.thumbnail("/a.jpg", 256); c.open("/a.jpg").use { it.readBytes() }
        assertTrue(c.stats().listings > 0 && c.stats().thumbs > 0 && c.stats().files > 0)
        src.error = SourceError.Locked
        try { c.list("/d"); fail() } catch (e: SourceError.Locked) {}
        assertEquals(CachingSource.Stats(0, 0, 0, 0, 0), c.stats())
    }

    @Test fun invalidateListingAndClear() = runTest {
        val src = inner()
        val c = cached(src)
        c.list("/"); c.list("/d")
        c.invalidateListing("/")
        c.list("/"); c.list("/d")
        assertEquals(3, src.lists)
        c.thumbnail("/a.jpg", 256)
        c.clear()
        assertEquals(CachingSource.Stats(0, 0, 0, 0, 0), c.stats())
        c.list("/d")
        assertEquals(4, src.lists)
    }

    @Test fun listingCountIsBounded() = runTest {
        val src = inner()
        for (i in 0 until 10) src.tree["/f$i"] = mutableListOf()
        val c = cached(src, maxListings = 3)
        for (i in 0 until 10) c.list("/f$i")
        assertEquals(3, c.stats().listings)
        c.list("/f9")
        assertEquals(10, src.lists)
        c.list("/f0")
        assertEquals(11, src.lists)
    }

    @Test fun remoteSourceIsFoundThroughTheWrapper() {
        val plain = CountingSource()
        assertNull(plain.remoteSource())
        assertNull(CachingSource(plain).remoteSource())
    }

    @Test fun byteLruKeepsToItsBudget() {
        val lru = ByteLru(200)
        lru.put("a", ByteArray(100))
        lru.put("b", ByteArray(100))
        assertEquals(200, lru.bytes)
        lru.put("a", ByteArray(50)) // replace: no double counting
        assertEquals(150, lru.bytes)
        lru.put("c", ByteArray(100)) // 250 > 200: "b" is the least recently used
        assertNull(lru.get("b"))
        assertEquals(150, lru.bytes)
        lru.put("huge", ByteArray(201)) // never stored
        assertNull(lru.get("huge"))
        assertEquals(2, lru.size)
        lru.put("empty", ByteArray(0)) // an empty answer still has a cost (64), which pushes "a" out
        assertNull(lru.get("a"))
        assertEquals(164, lru.bytes)
        assertEquals(2, lru.size)
        lru.removeIf { it == "a" || it == "empty" }
        assertEquals(1, lru.size)
        assertEquals(100, lru.bytes)
        lru.clear()
        assertEquals(0, lru.bytes)
        assertEquals(0, lru.size)
    }

    @Test fun defaultBudgetsAreSane() {
        assertTrue(CachingSource.DEFAULT_THUMB_BUDGET >= 8L * 1024 * 1024)
        assertTrue(CachingSource.DEFAULT_FILE_BUDGET >= 16L * 1024 * 1024)
        assertTrue(CachingSource.DEFAULT_FILE_BUDGET > CachingSource.DEFAULT_THUMB_BUDGET)
        assertEquals(16L * 1024 * 1024, CachingSource.maxCachedFileFor(96L * 1024 * 1024))
        assertEquals(6L * 1024 * 1024, CachingSource.maxCachedFileFor(18L * 1024 * 1024))
    }
}
