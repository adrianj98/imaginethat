package ai.alertd.imagethat.data

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.ByteArrayInputStream
import java.io.InputStream

/**
 * Read cache in front of a [VaultSource], so browsing does not ask the server for the same folder,
 * thumbnail or image again and again. Two layers: memory, and optionally an encrypted [disk] layer
 * ([VaultDiskCache]) that survives restarts. Decrypted content lives only in memory; the disk layer
 * holds ciphertext under a key derived from the vault password, and is readable only while that
 * key is held ([VaultDiskCache.unlock]). [clear] drops the memory layer and locks the disk one.
 *
 *  - Folder listings are good for [listTtlMs] from the moment they were fetched, in memory and on
 *    disk alike. Every write through this source (upload, mkdir, move, delete) forgets the listings
 *    it touches, so the browser never sees a stale folder after its own changes; changes made by
 *    other clients show up after the TTL, on [invalidateListing] (the Refresh action), or on the
 *    next [list] after a stale listing was shown through [peekListing].
 *  - [size] answers from the cached parent listing when it can.
 *  - Thumbnail bytes are an LRU of [thumbBudgetBytes] in memory, and on disk keyed by the file's
 *    size from the listing, so a replaced file gets a new thumbnail; "no preview" is cached too.
 *  - Whole image files up to [maxCachedFileBytes] are kept (memory LRU of [fileBudgetBytes], and
 *    disk) once the caller has read them to the end, so swiping back to an image or reopening it
 *    does not download it again. Ranged reads and non-images stream through untouched.
 *
 * A [SourceError.Locked] from the wrapped source clears the cache before it is rethrown.
 */
class CachingSource(
    internal val inner: VaultSource,
    private val listTtlMs: Long = LIST_TTL_MS,
    thumbBudgetBytes: Long = DEFAULT_THUMB_BUDGET,
    fileBudgetBytes: Long = DEFAULT_FILE_BUDGET,
    private val maxCachedFileBytes: Long = maxCachedFileFor(fileBudgetBytes),
    private val maxListings: Int = MAX_LISTINGS,
    /** Monotonic milliseconds, for the in-memory TTL. */
    private val clock: () -> Long = { System.nanoTime() / 1_000_000 },
    /** Wall-clock milliseconds, stored with listings on disk so the TTL carries across restarts. */
    private val wallClock: () -> Long = { System.currentTimeMillis() },
    private val disk: VaultDiskCache? = null,
) : VaultSource {
    private class Listing(val items: List<Item>, val at: Long)

    @Serializable
    private class DiskListing(val at: Long, val items: List<Item>)

    private val lock = Any()
    private val listings = LinkedHashMap<String, Listing>(64, 0.75f, true)
    private val thumbs = ByteLru(thumbBudgetBytes)
    private val files = ByteLru(fileBudgetBytes)
    private val json = Json { ignoreUnknownKeys = true }

    // ---- reads -----------------------------------------------------------------

    override suspend fun list(path: String): List<Item> {
        val key = norm(path)
        memListing(key, fresh = true)?.let { return it }
        diskListing(key)?.let { (items, age) -> if (age < listTtlMs) return items }
        val items = guard { inner.list(path) }
        remember(key, items, clock())
        diskPut(CacheKind.LISTING, key, "", json.encodeToString(DiskListing.serializer(), DiskListing(wallClock(), items)).toByteArray())
        return items
    }

    /**
     * The last known listing of [path], however old, without asking the source: what the browser
     * shows while [list] revalidates. Null when nothing is known.
     */
    suspend fun peekListing(path: String): List<Item>? {
        val key = norm(path)
        return memListing(key, fresh = false) ?: diskListing(key)?.first
    }

    override suspend fun size(path: String): Long {
        val (dir, name) = parentOf(path)
        memListing(norm(dir), fresh = true)?.firstOrNull { it.name == name }?.let { return it.size }
        // Not in a fresh listing (or none cached): the source is authoritative.
        return guard { inner.size(path) }
    }

    override suspend fun thumbnail(path: String, px: Int): ByteArray {
        val key = norm(path)
        val memKey = "$key\u0000$px"
        thumbs.get(memKey)?.let { return it }
        // On disk the file's size is part of the key: a replaced file gets a new thumbnail.
        val variant = knownItem(key)?.let { "$px-${it.size}" }
        if (variant != null) diskGet(CacheKind.THUMB, key, variant)?.let { thumbs.put(memKey, it); return it }
        val bytes = guard { inner.thumbnail(path, px) }
        thumbs.put(memKey, bytes)
        if (variant != null) diskPut(CacheKind.THUMB, key, variant, bytes)
        return bytes
    }

    override suspend fun open(path: String, range: LongRange?): InputStream {
        val key = norm(path)
        if (range != null) return guard { inner.open(path, range) }
        files.get(key)?.let { return ByteArrayInputStream(it) }
        val expected = cacheableImageSize(key)
        if (expected != null) {
            diskGet(CacheKind.FILE, key, expected.toString())?.let { files.put(key, it); return ByteArrayInputStream(it) }
        }
        val stream = guard { inner.open(path, null) }
        if (expected == null) return stream
        val d = disk
        return RecordingInput(stream, expected) { bytes ->
            files.put(key, bytes)
            // Called from the reader's (blocking I/O) thread once the last byte was read.
            d?.put(CacheKind.FILE, key, expected.toString(), bytes)
        }
    }

    /** The file's size when the last known parent listing says it is an image small enough to keep. */
    private suspend fun cacheableImageSize(key: String): Int? {
        val item = knownItem(key) ?: return null
        if (item.isDir || item.media != MediaKind.IMAGE) return null
        if (item.size <= 0 || item.size > maxCachedFileBytes) return null
        return item.size.toInt()
    }

    /** The entry for [key] in the last known listing of its parent (any age; memory, then disk). */
    private suspend fun knownItem(key: String): Item? {
        val (dir, name) = parentOf(key)
        return peekListing(dir)?.firstOrNull { it.name == name }
    }

    // ---- writes: do the work, then forget what it changed ----------------------

    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        try {
            guard { inner.upload(dir, name, input, length) }
        } finally {
            invalidateListing(dir)
            forgetSubtree(childPath(norm(dir), name))
        }
    }

    override suspend fun mkdir(path: String) {
        try {
            guard { inner.mkdir(path) }
        } finally {
            invalidateListing(parentOf(path).first)
        }
    }

    override suspend fun move(from: String, to: String) {
        try {
            guard { inner.move(from, to) }
        } finally {
            invalidateListing(parentOf(from).first)
            invalidateListing(parentOf(to).first)
            forgetSubtree(from)
            forgetSubtree(to)
        }
    }

    override suspend fun delete(path: String) {
        try {
            guard { inner.delete(path) }
        } finally {
            invalidateListing(parentOf(path).first)
            forgetSubtree(path)
        }
    }

    // ---- cache control ---------------------------------------------------------

    /** Forgets the listing of [path] so the next [list] asks the source (the browser's Refresh). */
    suspend fun invalidateListing(path: String) {
        val key = norm(path)
        synchronized(lock) { listings.remove(key) }
        diskIo { it.removeListing(key) }
    }

    /** Drops every cached listing, thumbnail and file from memory and locks the disk layer. */
    fun clear() {
        synchronized(lock) { listings.clear() }
        thumbs.clear()
        files.clear()
        disk?.lock()
    }

    /** What the memory layer holds right now; for tests and diagnostics. */
    internal fun stats() = synchronized(lock) { Stats(listings.size, thumbs.size, thumbs.bytes, files.size, files.bytes) }

    internal data class Stats(val listings: Int, val thumbs: Int, val thumbBytes: Long, val files: Int, val fileBytes: Long)

    // ---- memory layer ----------------------------------------------------------

    private fun memListing(key: String, fresh: Boolean): List<Item>? = synchronized(lock) {
        val l = listings[key] ?: return null
        if (fresh && clock() - l.at >= listTtlMs) return null
        l.items
    }

    private fun remember(key: String, items: List<Item>, at: Long) = synchronized(lock) {
        listings[key] = Listing(items, at)
        while (listings.size > maxListings) listings.remove(listings.keys.first())
    }

    /** Forgets [path] and everything below it: listings, thumbnails and file bytes, on both layers. */
    private suspend fun forgetSubtree(path: String) {
        val root = norm(path)
        val prefix = if (root == "/") "/" else "$root/"
        fun hit(key: String) = key == root || key.startsWith(prefix)
        synchronized(lock) { listings.keys.removeAll(::hit) }
        thumbs.removeIf { hit(it.substringBefore('\u0000')) }
        files.removeIf(::hit)
        diskIo { it.removeSubtree(root) }
    }

    // ---- disk layer ------------------------------------------------------------

    /** The listing of [key] from disk with its age; also remembered in memory (as old as it is). */
    private suspend fun diskListing(key: String): Pair<List<Item>, Long>? {
        val bytes = diskGet(CacheKind.LISTING, key, "") ?: return null
        val l = try {
            json.decodeFromString(DiskListing.serializer(), String(bytes))
        } catch (_: Exception) {
            return null
        }
        val age = (wallClock() - l.at).coerceAtLeast(0)
        remember(key, l.items, clock() - age)
        return l.items to age
    }

    private suspend fun diskGet(kind: CacheKind, key: String, variant: String): ByteArray? =
        diskIo { it.get(kind, key, variant) }

    private suspend fun diskPut(kind: CacheKind, key: String, variant: String, bytes: ByteArray) {
        diskIo { it.put(kind, key, variant, bytes) }
    }

    private suspend fun <T> diskIo(block: (VaultDiskCache) -> T): T? {
        val d = disk?.takeIf { it.isOpen } ?: return null
        return withContext(Dispatchers.IO) { block(d) }
    }

    private suspend fun <T> guard(block: suspend () -> T): T = try {
        block()
    } catch (e: CancellationException) {
        throw e
    } catch (e: SourceError.Locked) {
        clear()
        throw e
    }

    companion object {
        const val LIST_TTL_MS = 5 * 60_000L
        const val MAX_LISTINGS = 512
        private const val MB = 1024L * 1024

        /** Budgets scale with the heap: 1/16 of it for thumbnails, 1/8 for whole images, within fixed bounds. */
        val DEFAULT_THUMB_BUDGET: Long = (Runtime.getRuntime().maxMemory() / 16).coerceIn(8 * MB, 48 * MB)
        val DEFAULT_FILE_BUDGET: Long = (Runtime.getRuntime().maxMemory() / 8).coerceIn(16 * MB, 96 * MB)

        /** A single cached image may take at most a third of the file budget, and never more than 16 MB. */
        fun maxCachedFileFor(fileBudgetBytes: Long): Long = minOf(fileBudgetBytes / 3, 16 * MB)

        /** "/" for the root; otherwise a leading slash and no trailing one. */
        internal fun norm(path: String): String {
            val t = path.trim('/')
            return if (t.isEmpty()) "/" else "/$t"
        }
    }
}

/** The [RemoteSource] behind [this], through any [CachingSource]; null for local vaults. */
fun VaultSource.remoteSource(): RemoteSource? = when (this) {
    is RemoteSource -> this
    is CachingSource -> inner.remoteSource()
    else -> null
}

/** Byte-budgeted LRU of immutable byte arrays. Safe to use from any thread. */
internal class ByteLru(private val maxBytes: Long) {
    private val map = LinkedHashMap<String, ByteArray>(64, 0.75f, true)
    var bytes = 0L
        private set
    val size: Int get() = synchronized(this) { map.size }

    @Synchronized fun get(key: String): ByteArray? = map[key]

    @Synchronized fun put(key: String, value: ByteArray) {
        val cost = cost(value)
        if (cost > maxBytes) { remove(key); return }
        remove(key)
        map[key] = value
        bytes += cost
        val it = map.entries.iterator()
        while (bytes > maxBytes && it.hasNext()) {
            val e = it.next()
            if (e.key == key) continue
            bytes -= cost(e.value)
            it.remove()
        }
    }

    @Synchronized fun remove(key: String) {
        map.remove(key)?.let { bytes -= cost(it) }
    }

    @Synchronized fun removeIf(pred: (String) -> Boolean) {
        val it = map.entries.iterator()
        while (it.hasNext()) {
            val e = it.next()
            if (pred(e.key)) { bytes -= cost(e.value); it.remove() }
        }
    }

    @Synchronized fun clear() {
        map.clear()
        bytes = 0
    }

    /** An empty answer still costs an entry, so a "no preview" result cannot grow the map without bound. */
    private fun cost(v: ByteArray) = maxOf(v.size.toLong(), MIN_COST)

    private companion object {
        const val MIN_COST = 64L
    }
}

/**
 * Passes [inner] through while copying it into a buffer of exactly [expected] bytes; [onComplete]
 * gets the buffer once every byte has been read. A stream closed early, or one that turns out
 * longer or shorter than [expected], is simply not cached.
 */
private class RecordingInput(
    private val inner: InputStream,
    private val expected: Int,
    private val onComplete: (ByteArray) -> Unit,
) : InputStream() {
    private var buf: ByteArray? = ByteArray(expected)
    private var n = 0

    override fun read(): Int {
        val b = inner.read()
        if (b < 0) finish() else record(1) { it[n] = b.toByte() }
        return b
    }

    override fun read(b: ByteArray, off: Int, len: Int): Int {
        val r = inner.read(b, off, len)
        if (r < 0) finish() else if (r > 0) record(r) { System.arraycopy(b, off, it, n, r) }
        return r
    }

    override fun available(): Int = inner.available()

    override fun close() {
        buf = null
        inner.close()
    }

    /** Appends [count] bytes to the buffer, or gives up on caching when the stream outgrows [expected]. */
    private inline fun record(count: Int, write: (ByteArray) -> Unit) {
        val buf = buf ?: return
        if (count > expected - n) {
            this.buf = null
            return
        }
        write(buf)
        n += count
    }

    private fun finish() {
        val done = buf ?: return
        buf = null
        if (n == expected) onComplete(done)
    }
}
