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
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.job
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json
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
}

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

    override suspend fun thumbnail(path: String, px: Int): ByteArray = io {
        val name = path.substringAfterLast('/')
        if (mediaKindOf(name, false, "") != MediaKind.IMAGE) return@io ByteArray(0)
        val total = vault.size(path)
        if (total > MAX_THUMB_SOURCE) return@io ByteArray(0)
        thumbnailer.thumbnail(vault.readRange(path, 0, total), px)
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

    private companion object {
        const val MAX_THUMB_SOURCE = 64L * 1024 * 1024
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
