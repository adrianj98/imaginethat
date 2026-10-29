package ai.alertd.imagethat.data

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

class DiskCacheTest {
    @get:Rule val tmp = TemporaryFolder()

    private lateinit var root: File

    private fun store(budget: Long = 10_000_000, maxEntry: Long = 1_000_000): EncryptedDiskCache {
        if (!::root.isInitialized) root = tmp.newFolder("cache")
        return EncryptedDiskCache(root, budget, maxEntry, kdfIterations = 1_000)
    }

    private fun open(store: EncryptedDiskCache, id: String = "srv:v1", password: String = "correct horse") =
        store.forVault(id).apply { unlock(password.toCharArray()) }

    private fun allFiles() = root.walkTopDown().filter { it.isFile }.toList()
    private fun allNames() = root.walkTopDown().map { it.name }.toList()

    @Test fun entriesRoundTripAndNothingOnDiskIsReadable() {
        val v = open(store())
        val listing = "MARKER-listing-{\"items\":[\"holiday.jpg\"]}".toByteArray()
        val thumb = ByteArray(5000) { (it % 251).toByte() }
        assertTrue(v.put(CacheKind.LISTING, "/Trips/Summer 2024", "", listing))
        assertTrue(v.put(CacheKind.THUMB, "/Trips/Summer 2024/holiday.jpg", "256-5000", thumb))
        assertTrue(v.put(CacheKind.FILE, "/Trips/Summer 2024/holiday.jpg", "5000", thumb))
        assertArrayEquals(listing, v.get(CacheKind.LISTING, "/Trips/Summer 2024"))
        assertArrayEquals(thumb, v.get(CacheKind.THUMB, "/Trips/Summer 2024/holiday.jpg", "256-5000"))
        assertArrayEquals(thumb, v.get(CacheKind.FILE, "/Trips/Summer 2024/holiday.jpg", "5000"))
        assertArrayEquals(listing, v.get(CacheKind.LISTING, "Trips/Summer 2024/")) // same folder, any spelling

        // Names: no vault path segment or server id appears anywhere under the cache root.
        for (n in allNames()) {
            assertFalse(n, n.contains("Trips") || n.contains("Summer") || n.contains("holiday") || n.contains("srv") || n.contains("v1"))
        }
        // Contents: ciphertext only.
        for (f in allFiles()) {
            val bytes = f.readBytes()
            assertFalse(f.path, String(bytes, Charsets.ISO_8859_1).contains("MARKER"))
            assertFalse(f.path, bytes.indexOfSubArray(thumb.copyOf(64)) >= 0)
        }
        // Each kind and variant of one file is its own entry.
        assertNull(v.get(CacheKind.THUMB, "/Trips/Summer 2024/holiday.jpg", "128-5000"))
        assertNull(v.get(CacheKind.THUMB, "/Trips/Summer 2024/holiday.jpg", "256-5001"))
    }

    @Test fun lockedCacheReadsAndWritesNothing() {
        val s = store()
        val v = s.forVault("srv:v1")
        assertFalse(v.isOpen)
        assertFalse(v.put(CacheKind.LISTING, "/", "", ByteArray(3)))
        assertNull(v.get(CacheKind.LISTING, "/"))
        v.unlock("pw".toCharArray())
        assertTrue(v.isOpen)
        assertTrue(v.put(CacheKind.LISTING, "/", "", ByteArray(3)))
        v.lock()
        assertFalse(v.isOpen)
        assertNull(v.get(CacheKind.LISTING, "/"))
        assertEquals(1, allFiles().count { it.name != EncryptedDiskCache.SALT_FILE }) // the entry stayed
        v.unlock("pw".toCharArray())
        assertNotNull(v.get(CacheKind.LISTING, "/"))
        v.unlock(CharArray(0))
        assertTrue(v.isOpen) // an empty password changes nothing
    }

    @Test fun entriesSurviveANewStoreWithTheSamePassword() {
        open(store()).put(CacheKind.THUMB, "/a.jpg", "256-10", ByteArray(10) { 7 })
        val again = open(store())
        assertArrayEquals(ByteArray(10) { 7 }, again.get(CacheKind.THUMB, "/a.jpg", "256-10"))
        assertEquals(1, allFiles().count { it.name == EncryptedDiskCache.SALT_FILE })
    }

    @Test fun anotherPasswordSeesNothingAndWritesApart() {
        open(store()).put(CacheKind.THUMB, "/a.jpg", "256-10", ByteArray(10) { 7 })
        // A different password means different names as well as a different key: the old entry is
        // invisible to it (and left for eviction), and what it writes is invisible the other way.
        val other = open(store(), password = "wrong")
        assertNull(other.get(CacheKind.THUMB, "/a.jpg", "256-10"))
        assertTrue(other.put(CacheKind.THUMB, "/a.jpg", "256-10", ByteArray(10) { 8 }))
        assertArrayEquals(ByteArray(10) { 7 }, open(store()).get(CacheKind.THUMB, "/a.jpg", "256-10"))
        assertEquals(2, allFiles().count { it.name != EncryptedDiskCache.SALT_FILE })
    }

    @Test fun anEntryThatDoesNotDecryptIsDropped() {
        val v = open(store())
        v.put(CacheKind.THUMB, "/a.jpg", "256-10", ByteArray(10) { 7 })
        val f = allFiles().single { it.name != EncryptedDiskCache.SALT_FILE }
        val bytes = f.readBytes()
        bytes[bytes.size - 1] = (bytes.last().toInt() xor 1).toByte() // break the tag
        f.writeBytes(bytes)
        assertNull(v.get(CacheKind.THUMB, "/a.jpg", "256-10"))
        assertFalse(f.exists())
        // A truncated or foreign file under the entry's name is treated the same way.
        f.writeBytes("not a cache entry".toByteArray())
        assertNull(v.get(CacheKind.THUMB, "/a.jpg", "256-10"))
        assertFalse(f.exists())
    }

    @Test fun vaultsAreKeptApart() {
        val s = store()
        val a = open(s, id = "srv:a")
        val b = open(s, id = "srv:b")
        a.put(CacheKind.LISTING, "/", "", "A".toByteArray())
        assertNull(b.get(CacheKind.LISTING, "/"))
        assertEquals(2, root.listFiles()!!.size)
    }

    @Test fun removeListingAndRemoveSubtree() {
        val v = open(store())
        v.put(CacheKind.LISTING, "/", "", "root".toByteArray())
        v.put(CacheKind.LISTING, "/d", "", "d".toByteArray())
        v.put(CacheKind.LISTING, "/d/sub", "", "sub".toByteArray())
        v.put(CacheKind.THUMB, "/d/c.jpg", "256-1", ByteArray(1))
        v.put(CacheKind.FILE, "/d/c.jpg", "1", ByteArray(1))
        v.put(CacheKind.THUMB, "/d/sub/e.jpg", "256-1", ByteArray(1))
        v.put(CacheKind.THUMB, "/a.jpg", "256-1", ByteArray(1))
        v.put(CacheKind.THUMB, "/d", "256-1", ByteArray(1)) // a file called "d" at the root would live beside the folder

        v.removeListing("/d")
        assertNull(v.get(CacheKind.LISTING, "/d"))
        assertNotNull(v.get(CacheKind.LISTING, "/d/sub"))
        assertNotNull(v.get(CacheKind.THUMB, "/d/c.jpg", "256-1"))

        v.removeSubtree("/d")
        assertNull(v.get(CacheKind.LISTING, "/d/sub"))
        assertNull(v.get(CacheKind.THUMB, "/d/c.jpg", "256-1"))
        assertNull(v.get(CacheKind.FILE, "/d/c.jpg", "1"))
        assertNull(v.get(CacheKind.THUMB, "/d/sub/e.jpg", "256-1"))
        assertNull(v.get(CacheKind.THUMB, "/d", "256-1"))
        assertNotNull(v.get(CacheKind.LISTING, "/"))
        assertNotNull(v.get(CacheKind.THUMB, "/a.jpg", "256-1"))

        v.removeSubtree("/")
        assertNull(v.get(CacheKind.LISTING, "/"))
        assertNull(v.get(CacheKind.THUMB, "/a.jpg", "256-1"))
        assertTrue(v.isOpen)
        assertTrue(v.put(CacheKind.LISTING, "/", "", "again".toByteArray()))
    }

    @Test fun wipeKeepsTheKeyUsable() {
        val v = open(store())
        v.put(CacheKind.LISTING, "/", "", "x".toByteArray())
        v.wipe()
        assertNull(v.get(CacheKind.LISTING, "/"))
        assertTrue(v.put(CacheKind.LISTING, "/", "", "y".toByteArray()))
        assertArrayEquals("y".toByteArray(), open(store()).get(CacheKind.LISTING, "/"))
    }

    @Test fun storeStaysUnderItsBudgetEvictingTheOldestFirst() {
        val s = store(budget = 1_000, maxEntry = 500)
        val v = open(s)
        fun entries() = allFiles().filter { it.name != EncryptedDiskCache.SALT_FILE }
        var t = 1_000_000_000_000L
        for (i in 1..3) {
            v.put(CacheKind.THUMB, "/$i.jpg", "256-300", ByteArray(300))
            entries().filter { it.lastModified() > t }.forEach { it.setLastModified(t) } // oldest first: 1, 2, 3
            t += 10_000
        }
        assertEquals(3, entries().size)
        v.put(CacheKind.THUMB, "/4.jpg", "256-300", ByteArray(300)) // 4 x ~330 > 1000: evict down to 900
        assertTrue(s.totalBytes() <= 900)
        assertNull(v.get(CacheKind.THUMB, "/1.jpg", "256-300"))
        assertNotNull(v.get(CacheKind.THUMB, "/3.jpg", "256-300"))
        assertNotNull(v.get(CacheKind.THUMB, "/4.jpg", "256-300"))
        assertEquals(entries().sumOf { it.length() }, s.totalBytes())
        // A new store on the same directory counts what is there.
        assertEquals(s.totalBytes(), store(budget = 1_000, maxEntry = 500).totalBytes())
        assertFalse(v.put(CacheKind.FILE, "/big.jpg", "501", ByteArray(501)))
    }

    @Test fun keyDerivationIsDeterministicPerSaltAndNamesAreOpaque() {
        val salt = ByteArray(16) { it.toByte() }
        val a = CacheKey.derive("pw".toCharArray(), salt, 1_000)
        val b = CacheKey.derive("pw".toCharArray(), salt, 1_000)
        val c = CacheKey.derive("pw".toCharArray(), ByteArray(16) { (it + 1).toByte() }, 1_000)
        assertEquals(a.name("Trips"), b.name("Trips"))
        assertTrue(a.name("Trips") != c.name("Trips"))
        assertTrue(a.name("Trips") != a.name("trips"))
        assertEquals(32, a.name("Trips").length)
        assertTrue(a.name("Trips").all { it in "0123456789abcdef" })
    }
}

private fun ByteArray.indexOfSubArray(needle: ByteArray): Int {
    outer@ for (i in 0..size - needle.size) {
        for (j in needle.indices) if (this[i + j] != needle[j]) continue@outer
        return i
    }
    return -1
}
