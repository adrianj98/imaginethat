package ai.alertd.imagethat.data

import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.remote.ApiClient
import ai.alertd.imagethat.remote.Entry
import ai.alertd.imagethat.remote.RemoteError
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.job
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
import mobile.Mobile
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okio.BufferedSink
import okio.source
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import kotlin.coroutines.coroutineContext

enum class MediaKind { IMAGE, VIDEO, OTHER }

data class Item(val path: String, val name: String, val isDir: Boolean, val size: Long, val media: MediaKind)

/** Remote and local failures in one vocabulary. Anything unrecognised is [Other]. */
sealed class SourceError(message: String, cause: Throwable? = null) : Exception(message, cause) {
    object Locked : SourceError("vault is locked")
    object WrongPassword : SourceError("wrong password")
    object NotFound : SourceError("not found")
    object Exists : SourceError("already exists")
    class Other(val msg: String, cause: Throwable? = null) : SourceError(msg, cause)
}

interface VaultSource {
    suspend fun list(path: String): List<Item>
    /** Caller closes the stream. Never reads the whole file into memory. */
    suspend fun open(path: String, range: LongRange? = null): InputStream
    suspend fun size(path: String): Long
    /** JPEG bytes; empty when there is no preview. */
    suspend fun thumbnail(path: String, px: Int): ByteArray
    /** Streams [input] (closed by this call) of exactly [length] bytes into [dir]/[name]. */
    suspend fun upload(dir: String, name: String, input: InputStream, length: Long)
    suspend fun mkdir(path: String)
    suspend fun move(from: String, to: String)
    suspend fun delete(path: String)

    /**
     * Where the Go engine reads this source's files for a fault-tolerant copy (Save offline); null
     * for a server-decrypt vault, whose files are streamed through [open] instead.
     */
    val copyOrigin: CopyOrigin? get() = null
}

/**
 * A Go engine copy or cache fetch in progress (`mobile.Job`, see [GoJob]): poll [done] and
 * [total] and [cancel] from any thread while the blocking call runs on another.
 */
interface CopyJob {
    /** Bytes of the current file copied so far. */
    fun done(): Long
    /** The current file's size, 0 until known. */
    fun total(): Long
    /** Makes the running call fail with "cancelled:" and leave nothing behind. */
    fun cancel()
}

class GoJob(val job: mobile.Job = Mobile.newJob()) : CopyJob {
    override fun done(): Long = job.done()
    override fun total(): Long = job.total()
    override fun cancel() = job.cancel()
}

/**
 * Where a Go copy reads from ([GoVaultOrigin] in the app). The engine retries a read that fails
 * part-way (dropped connection, Drive or server 5xx), resumes where it stopped, and commits the
 * copy only when it arrived whole.
 */
interface CopyOrigin

/** Files of a vault the engine decrypts on the phone: local, SAF folder, client-decrypt server or Drive. */
class GoVaultOrigin(val vault: mobile.Vault) : CopyOrigin

internal const val CHUNK = 256 * 1024

private val IMAGE_EXT = setOf("jpg", "jpeg", "png", "gif", "webp", "heic", "heif", "bmp")
private val VIDEO_EXT = setOf("mp4", "m4v", "mov", "webm", "mkv", "avi", "3gp")

/** [serverType] is the webserver's "image"/"video"/"" and wins when present. */
fun mediaKindOf(name: String, isDir: Boolean, serverType: String): MediaKind {
    if (isDir) return MediaKind.OTHER
    when (serverType) {
        "image" -> return MediaKind.IMAGE
        "video" -> return MediaKind.VIDEO
    }
    val ext = name.substringAfterLast('.', "").lowercase()
    return when (ext) {
        in IMAGE_EXT -> MediaKind.IMAGE
        in VIDEO_EXT -> MediaKind.VIDEO
        else -> MediaKind.OTHER
    }
}

private fun entryItems(dir: String, entries: List<Entry>) = entries.map {
    Item(childPath(dir, it.name), it.name, it.isDir, it.size, mediaKindOf(it.name, it.isDir, it.mediaType))
}

internal fun childPath(dir: String, name: String): String =
    if (dir.isEmpty() || dir == "/") "/$name" else "${dir.trimEnd('/')}/$name"

internal fun parentOf(path: String): Pair<String, String> {
    val t = path.trimEnd('/')
    val i = t.lastIndexOf('/')
    return (if (i <= 0) "/" else t.substring(0, i)) to t.substring(i + 1)
}

/** Maps anything a remote call can throw to [SourceError]; cancellation passes through. */
private suspend fun <T> remote(block: suspend () -> T): T = try {
    block()
} catch (e: CancellationException) {
    throw e
} catch (e: SourceError) {
    throw e
} catch (e: RemoteError) {
    throw when (e) {
        RemoteError.Locked -> SourceError.Locked
        RemoteError.WrongPassword -> SourceError.WrongPassword
        RemoteError.NotFound -> SourceError.NotFound
        RemoteError.Exists -> SourceError.Exists
        else -> SourceError.Other(e.message ?: "remote error", e)
    }
} catch (e: IOException) {
    throw SourceError.Other(e.message ?: "network error", e)
} catch (e: SerializationException) {
    throw SourceError.Other("unexpected server response", e)
}

class RemoteSource(private val api: ApiClient, private val vaultId: String) : VaultSource {
    /** For Media3: the file's URL and the session's OkHttpClient (cookie jar, pinning, no cache). */
    internal fun fileUrl(path: String): String = api.fileUrl(vaultId, path).toString()
    internal val httpClient get() = api.httpClient

    override suspend fun list(path: String): List<Item> = remote {
        entryItems(path, api.list(vaultId, path).entries)
    }

    override suspend fun open(path: String, range: LongRange?): InputStream = remote {
        val resp = api.openFile(vaultId, path, range)
        if (range != null && resp.code != 206) {
            resp.close()
            throw SourceError.Other("server ignored the range request")
        }
        resp.body?.byteStream() ?: run { resp.close(); InputStream.nullInputStream() }
    }

    override suspend fun size(path: String): Long = remote {
        val (dir, name) = parentOf(path)
        api.list(vaultId, dir).entries.firstOrNull { it.name == name }?.size ?: throw SourceError.NotFound
    }

    override suspend fun thumbnail(path: String, px: Int): ByteArray = remote { api.thumb(vaultId, path, px) }

    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        input.use {
            remote { api.upload(vaultId, dir, name, StreamBody(it, length)) }
        }
    }

    override suspend fun mkdir(path: String) = remote { api.mkdir(vaultId, path) }
    override suspend fun move(from: String, to: String) = remote { api.move(vaultId, from, to) }
    override suspend fun delete(path: String) = remote { api.delete(vaultId, path) }
}

/** One-shot streaming body: bytes flow from [input] to the socket without being buffered whole. */
private class StreamBody(private val input: InputStream, private val length: Long) : RequestBody() {
    override fun contentType() = "application/octet-stream".toMediaType()
    override fun contentLength() = length
    override fun isOneShot() = true
    override fun writeTo(sink: BufferedSink) {
        sink.writeAll(input.source())
    }
}

/** The gocryptfs engine surface this layer needs; [MobileVaultApi] adapts the gomobile `mobile.Vault`. */
interface LocalVaultApi {
    fun list(path: String): String
    fun size(path: String): Long
    fun readRange(path: String, off: Long, n: Long): ByteArray
    fun create(path: String): LocalWriter
    fun mkdir(path: String)
    fun move(from: String, to: String)
    fun remove(path: String)

    /** Where copies out of this vault read from. */
    val origin: CopyOrigin
    fun newJob(): CopyJob
    /** Copies [srcPath] of [from] to [dest]. Blocks; the file appears at [dest] only when whole. */
    fun copyIn(job: CopyJob, dest: String, from: CopyOrigin, srcPath: String)

    /** The engine's cached or decoded JPEG preview; empty when it cannot decode the file (HEIC, video). */
    fun thumbnail(path: String, px: Int): ByteArray = ByteArray(0)
    /** Hands the engine's caches a preview rendered on the phone. */
    fun storeThumbnail(path: String, px: Int, jpeg: ByteArray) = Unit

    /** Keeps the file readable without the network (a server vault opened with a cache, or Drive). Blocks. */
    fun cacheFile(job: CopyJob, path: String)
    fun isCached(path: String): Boolean
    fun uncache(path: String)
}

interface LocalWriter {
    fun write(data: ByteArray)
    fun close()
    /** Discards the partial file. */
    fun abort()
}

class MobileVaultApi(private val v: mobile.Vault) : LocalVaultApi {
    override fun list(path: String): String = v.list(path)
    override fun size(path: String): Long = v.size(path)
    override fun readRange(path: String, off: Long, n: Long): ByteArray = v.readRange(path, off, n)
    override fun create(path: String): LocalWriter {
        val w = v.create(path)
        return object : LocalWriter {
            override fun write(data: ByteArray) = w.write(data)
            override fun close() = w.close()
            override fun abort() = w.abort()
        }
    }
    override fun mkdir(path: String) = v.mkdir(path)
    override fun move(from: String, to: String) = v.move(from, to)
    override fun remove(path: String) = v.remove(path)

    override val origin: CopyOrigin = GoVaultOrigin(v)
    override fun newJob(): CopyJob = GoJob()
    override fun copyIn(job: CopyJob, dest: String, from: CopyOrigin, srcPath: String) {
        val j = (job as GoJob).job
        val src = (from as? GoVaultOrigin)?.vault ?: throw IllegalArgumentException("cannot copy from $from")
        v.copyFrom(j, dest, src, srcPath)
    }

    override fun thumbnail(path: String, px: Int): ByteArray = v.thumbnail(path, px.toLong())
    override fun storeThumbnail(path: String, px: Int, jpeg: ByteArray) = v.storeThumbnail(path, px.toLong(), jpeg)

    override fun cacheFile(job: CopyJob, path: String) = v.cacheFile((job as GoJob).job, path)
    override fun isCached(path: String): Boolean = v.isCached(path)
    override fun uncache(path: String) = v.uncache(path)
}

/** How often a running copy or cache fetch reports its progress. */
private const val PROGRESS_INTERVAL_MS = 250L

/**
 * Runs the blocking engine [call] for [job] on IO, reporting its progress to [onProgress] (bytes
 * done, total) while it runs. A blocking Go call cannot be interrupted, so cancelling the calling
 * coroutine cancels [job], which makes the engine stop and leave nothing behind.
 */
internal suspend fun runJob(job: CopyJob, onProgress: ((Long, Long) -> Unit)?, call: () -> Unit) = coroutineScope {
    val poll = onProgress?.let { report ->
        launch {
            while (true) {
                report(job.done(), job.total())
                delay(PROGRESS_INTERVAL_MS)
            }
        }
    }
    // The result is caught inside so a failure after cancellation cannot replace the cancellation.
    val running = async(Dispatchers.IO) { runCatching { local(call) } }
    try {
        running.await().getOrThrow()
    } catch (e: CancellationException) {
        job.cancel()
        throw e
    } finally {
        poll?.cancel()
    }
}

/** Decodes and downsizes in memory only. Injectable so JVM tests need no Android runtime. */
fun interface Thumbnailer {
    fun thumbnail(source: ByteArray, px: Int): ByteArray
}

/** Two-pass BitmapFactory decode (bounds, then inSampleSize) and in-memory JPEG compress. */
object BitmapThumbnailer : Thumbnailer {
    override fun thumbnail(source: ByteArray, px: Int): ByteArray {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(source, 0, source.size, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return ByteArray(0)
        var sample = 1
        while (bounds.outWidth / (sample * 2) >= px && bounds.outHeight / (sample * 2) >= px) sample *= 2
        val opts = BitmapFactory.Options().apply { inSampleSize = sample }
        val bmp = BitmapFactory.decodeByteArray(source, 0, source.size, opts) ?: return ByteArray(0)
        try {
            val out = ByteArrayOutputStream()
            bmp.compress(Bitmap.CompressFormat.JPEG, 80, out)
            return out.toByteArray()
        } finally {
            bmp.recycle()
        }
    }
}

class LocalSource(
    private val vault: LocalVaultApi,
    private val thumbnailer: Thumbnailer = BitmapThumbnailer,
    /** Bounds concurrent thumbnail work on the phone (whole-file read + decode); shared app-wide by default. */
    private val thumbLimit: Semaphore = THUMB_LIMIT,
    /**
     * The vault lives elsewhere (a client-decrypt server or Google Drive) and the engine can keep
     * chosen files on the phone ([cacheFile]) as ciphertext, readable without the network.
     */
    val cachesFiles: Boolean = false,
) : VaultSource {
    private val json = Json { ignoreUnknownKeys = true }

    private suspend fun <T> io(block: () -> T): T = withContext(Dispatchers.IO) { local(block) }

    override suspend fun list(path: String): List<Item> = io {
        val entries = json.decodeFromString<List<Entry>?>(vault.list(path)).orEmpty()
        entryItems(path, entries)
    }

    override suspend fun open(path: String, range: LongRange?): InputStream = io {
        val total = vault.size(path)
        val start = range?.first ?: 0L
        val end = minOf(range?.last ?: Long.MAX_VALUE, total - 1)
        ChunkedInput(vault, path, start, end)
    }

    override suspend fun size(path: String): Long = io { vault.size(path) }

    /**
     * The engine's preview (its encrypted memory and sealed disk caches, or decoded by Go). For
     * images Go cannot decode (HEIC, ...) the phone decodes it with [thumbnailer] and hands the
     * result back to the engine's caches. That fallback reads the whole image into memory, so at
     * most [thumbLimit] permits run at once (a grid fling queues dozens; the engine calls block and
     * cannot be cancelled once started). A request cancelled while queued or before the read never
     * starts it.
     */
    override suspend fun thumbnail(path: String, px: Int): ByteArray {
        val name = path.substringAfterLast('/')
        val kind = mediaKindOf(name, false, "")
        if (kind == MediaKind.OTHER) return ByteArray(0)
        val cached = io { vault.thumbnail(path, px) }
        if (cached.isNotEmpty() || kind != MediaKind.IMAGE) return cached
        return thumbLimit.withPermit {
            withContext(Dispatchers.IO) {
                ensureActive()
                val total = local { vault.size(path) }
                if (total > MAX_THUMB_SOURCE) return@withContext ByteArray(0)
                ensureActive()
                val jpeg = local { thumbnailer.thumbnail(vault.readRange(path, 0, total), px) }
                // A cache that cannot store it only costs a decode next time.
                if (jpeg.isNotEmpty()) runCatching { vault.storeThumbnail(path, px, jpeg) }
                jpeg
            }
        }
    }

    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        withContext(Dispatchers.IO) {
            val job = coroutineContext.job
            input.use { copyInto(it, dir, name, job) }
        }
    }

    private fun copyInto(input: InputStream, dir: String, name: String, job: Job) {
        val w = local { vault.create(childPath(dir, name)) }
        try {
            val buf = ByteArray(CHUNK)
            while (true) {
                job.ensureActive()
                val n = input.read(buf)
                if (n < 0) break
                if (n == 0) continue
                local { w.write(if (n == buf.size) buf else buf.copyOf(n)) }
            }
            local { w.close() }
        } catch (t: Throwable) {
            runCatching { w.abort() }
            throw if (t is IOException) SourceError.Other(t.message ?: "read failed", t) else t
        }
    }

    override suspend fun mkdir(path: String) = io { vault.mkdir(path) }
    override suspend fun move(from: String, to: String) = io { vault.move(from, to) }
    override suspend fun delete(path: String) = io { vault.remove(path) }

    override val copyOrigin: CopyOrigin get() = vault.origin

    /**
     * Copies [srcPath] from [from] into this vault at [dest] with the engine's fault-tolerant copy:
     * a read that fails part-way is retried and resumed, and [dest] appears only when the file
     * arrived whole. [onProgress] gets (bytes done, total) while it runs. Cancelling stops the copy.
     */
    suspend fun copyFrom(from: CopyOrigin, srcPath: String, dest: String, onProgress: ((Long, Long) -> Unit)? = null) {
        val job = local { vault.newJob() }
        runJob(job, onProgress) { vault.copyIn(job, dest, from, srcPath) }
    }

    /** Keeps [path] readable without the network (see [cachesFiles]); cancelling stops the fetch. */
    suspend fun cacheFile(path: String, onProgress: ((Long, Long) -> Unit)? = null) {
        val job = local { vault.newJob() }
        runJob(job, onProgress) { vault.cacheFile(job, path) }
    }

    suspend fun isCached(path: String): Boolean = io { vault.isCached(path) }

    /** Drops the phone's copy of [path]; the file itself stays where it is. */
    suspend fun uncache(path: String) = io { vault.uncache(path) }

    internal companion object {
        /** Larger images get no thumbnail: a SAF read holds several copies of the file at once. */
        const val MAX_THUMB_SOURCE = 24L * 1024 * 1024
        const val THUMB_CONCURRENCY = 2
        private val THUMB_LIMIT = Semaphore(THUMB_CONCURRENCY)
    }
}

/** Maps engine exceptions to [SourceError]; unknown [LocalError]s fall into Other. */
private fun <T> local(block: () -> T): T = try {
    block()
} catch (e: SourceError) {
    throw e
} catch (e: CancellationException) {
    throw e
} catch (e: SerializationException) {
    throw SourceError.Other("unexpected engine response", e)
} catch (e: IOException) {
    throw SourceError.Other(e.message ?: "io error", e)
} catch (e: Exception) {
    val le = LocalError.from(e)
    throw when (le) {
        is LocalError.WrongPassword -> SourceError.WrongPassword
        is LocalError.NotFound -> SourceError.NotFound
        is LocalError.Exists -> SourceError.Exists
        // The Go vault was closed under us (lockAll), or its server forgot the session and could not
        // be logged in again: to the UI that is "locked", not a failure.
        is LocalError.Closed, is LocalError.Locked -> SourceError.Locked
        else -> SourceError.Other(le.message ?: "local error", le)
    }
}

/** Lazily reads [start]..[end] inclusive in [CHUNK]-sized engine calls. */
private class ChunkedInput(
    private val vault: LocalVaultApi,
    private val path: String,
    private var pos: Long,
    private val end: Long,
) : InputStream() {
    private var buf = ByteArray(0)
    private var i = 0

    private fun fill(): Boolean {
        if (i < buf.size) return true
        if (pos > end) return false
        val want = minOf(CHUNK.toLong(), end - pos + 1)
        buf = local { vault.readRange(path, pos, want) }
        i = 0
        pos += buf.size
        if (buf.isEmpty()) { pos = end + 1; return false }
        return true
    }

    override fun read(): Int = if (fill()) buf[i++].toInt() and 0xff else -1

    override fun read(b: ByteArray, off: Int, len: Int): Int {
        if (len == 0) return 0
        if (!fill()) return -1
        val n = minOf(len, buf.size - i)
        System.arraycopy(buf, i, b, off, n)
        i += n
        return n
    }
}
