package ai.alertd.imagethat.data

import kotlinx.coroutines.CancellationException
import java.io.ByteArrayInputStream
import java.io.InputStream

/**
 * In-memory read cache in front of a [VaultSource], so browsing does not ask the server for the
 * same folder, thumbnail or image again and again.
 *
 * Nothing here is ever written to disk: every cache is a plain heap structure, bounded by a byte
 * budget or entry count, and [clear] drops it all (the Session calls it when the vault locks).
 *
 *  - Folder listings are kept for [listTtlMs] and re-fetched after that. Every write through this
 *    source (upload, mkdir, move, delete) invalidates the listings it touches, so the browser never
 *    sees a stale folder after its own changes; changes made by other clients show up after the TTL
 *    or on an explicit [invalidateListing].
 *  - [size] answers from the cached parent listing when it can.
 *  - Thumbnail bytes are an LRU of [thumbBudgetBytes]; a "no preview" answer is cached as well.
 *  - Whole image files up to [maxCachedFileBytes] are kept in an LRU of [fileBudgetBytes] once the
 *    caller has read them to the end (the viewer reads a whole image to decode it), so swiping back
 *    to an image or reopening it does not download it again. Ranged reads and non-images stream
 *    through untouched.
 *
 * A [SourceError.Locked] from the wrapped source empties the cache before it is rethrown: a locked
 * vault's decrypted content is not kept around.
 */
class CachingSource(
    internal val inner: VaultSource,
    private val listTtlMs: Long = LIST_TTL_MS,
    thumbBudgetBytes: Long = DEFAULT_THUMB_BUDGET,
    fileBudgetBytes: Long = DEFAULT_FILE_BUDGET,
    private val maxCachedFileBytes: Long = maxCachedFileFor(fileBudgetBytes),
    private val maxListings: Int = MAX_LISTINGS,
    private val clock: () -> Long = { System.nanoTime() / 1_000_000 },
) : VaultSource {
    private class Listing(val items: List<Item>, val at: Long)

    private val lock = Any()
    private val listings = LinkedHashMap<String, Listing>(64, 0.75f, true)
    private val thumbs = ByteLru(thumbBudgetBytes)
    private val files = ByteLru(fileBudgetBytes)

    // ---- reads -----------------------------------------------------------------

    override suspend fun list(path: String): List<Item> {
        val key = norm(path)
        cachedListing(key)?.let { return it }
        val items = guard { inner.list(path) }
        synchronized(lock) {
            listings[key] = Listing(items, clock())
            while (listings.size > maxListings) listings.remove(listings.keys.first())
        }
        return items
    }

    override suspend fun size(path: String): Long {
        val (dir, name) = parentOf(path)
        cachedListing(norm(dir))?.firstOrNull { it.name == name }?.let { return it.size }
        // Not in a cached listing (or none cached): the source is authoritative.
        return guard { inner.size(path) }
    }

    override suspend fun thumbnail(path: String, px: Int): ByteArray {
        val key = "${norm(path)}\u0000$px"
        thumbs.get(key)?.let { return it }
        val bytes = guard { inner.thumbnail(path, px) }
        thumbs.put(key, bytes)
        return bytes
    }

    override suspend fun open(path: String, range: LongRange?): InputStream {
        val key = norm(path)
        if (range == null) files.get(key)?.let { return ByteArrayInputStream(it) }
        val stream = guard { inner.open(path, range) }
        if (range != null) return stream
        val expected = cacheableImageSize(key) ?: return stream
        return RecordingInput(stream, expected) { bytes -> files.put(key, bytes) }
    }

    /** The file's size when the cached parent listing says it is an image small enough to keep. */
    private fun cacheableImageSize(key: String): Int? {
        val (dir, name) = parentOf(key)
        val item = cachedListing(norm(dir))?.firstOrNull { it.name == name } ?: return null
        if (item.isDir || item.media != MediaKind.IMAGE) return null
        if (item.size <= 0 || item.size > maxCachedFileBytes) return null
        return item.size.toInt()
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
    fun invalidateListing(path: String) {
        synchronized(lock) { listings.remove(norm(path)) }
    }

    /** Drops every cached listing, thumbnail and file. */
    fun clear() {
        synchronized(lock) { listings.clear() }
        thumbs.clear()
        files.clear()
    }

    /** What the cache holds right now; for tests and diagnostics. */
    internal fun stats() = synchronized(lock) { Stats(listings.size, thumbs.size, thumbs.bytes, files.size, files.bytes) }

    internal data class Stats(val listings: Int, val thumbs: Int, val thumbBytes: Long, val files: Int, val fileBytes: Long)

    private fun cachedListing(key: String): List<Item>? = synchronized(lock) {
        val l = listings[key] ?: return null
        if (clock() - l.at >= listTtlMs) {
            listings.remove(key)
            return null
        }
        l.items
    }

    /** Forgets [path] and everything below it: listings, thumbnails and file bytes. */
    private fun forgetSubtree(path: String) {
        val root = norm(path)
        val prefix = if (root == "/") "/" else "$root/"
        fun hit(key: String) = key == root || key.startsWith(prefix)
        synchronized(lock) { listings.keys.removeAll(::hit) }
        thumbs.removeIf { hit(it.substringBefore('\u0000')) }
        files.removeIf(::hit)
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
