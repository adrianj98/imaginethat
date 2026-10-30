package ai.alertd.imagethat.remote

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import okhttp3.Call
import okhttp3.Callback
import okhttp3.Cookie
import okhttp3.CookieJar
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okio.BufferedSink
import java.io.IOException
import java.nio.CharBuffer
import java.util.concurrent.TimeUnit
import javax.net.ssl.SSLException
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/**
 * Client for the ImageThat webserver JSON API (webserver/README.md "HTTP API").
 *
 * Privacy: no OkHttp [okhttp3.Cache] (the server also sends no-store), cookies
 * live only in memory, and nothing is written to disk. [openFile] hands back
 * the live [Response] for streaming.
 *
 * Origin: the server's CSRF check (auth.go/handlers_fs.go checkOrigin) accepts
 * requests with no Origin header (non-browser clients), and OkHttp sends none,
 * so none is set. A mismatching Origin would be rejected as 403, which maps to
 * [RemoteError.OriginRejected].
 *
 * [pinnedSha256]: lowercase hex SHA-256 of the server's leaf certificate (see
 * [probeCertificate]); when null the platform trust store is used.
 */
class ApiClient(private val baseUrl: HttpUrl, private val pinnedSha256: String?) {
    /** The pinned certificate, for the Go engine's own connection (core/remote). */
    val pin: String? get() = pinnedSha256

    private val jar = MemoryCookieJar()

    internal val httpClient: OkHttpClient = OkHttpClient.Builder()
        .cache(null)
        .cookieJar(jar)
        .followRedirects(false)
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .apply {
            if (pinnedSha256 != null) {
                val tm = PinnedTrustManager(pinnedSha256)
                sslSocketFactory(sslContextFor(tm).socketFactory, tm)
                hostnameVerifier(pinnedHostnameVerifier)
            }
        }
        .build()

    private val json = Json { ignoreUnknownKeys = true }

    private fun url(vararg segments: String, query: Map<String, String> = emptyMap()): HttpUrl =
        baseUrl.newBuilder().apply {
            addPathSegment("api")
            segments.forEach { addPathSegment(it) }
            query.forEach { (k, v) -> addQueryParameter(k, v) }
        }.build()

    private fun vaultUrl(id: String, op: String, query: Map<String, String> = emptyMap()) =
        url("vaults", id, op, query = query)

    private fun jsonBody(s: String) = s.toRequestBody(JSON)

    /** The server's mode. Servers without /api/info predate the switch and decrypt themselves. */
    suspend fun mode(): ServerMode = try {
        fetch(Request.Builder().url(url("info")).get().build()) {
            val mode = json.decodeFromString<InfoResponse>(it.body!!.string()).mode
            if (mode == "client-decrypt") ServerMode.CLIENT_DECRYPT else ServerMode.SERVER_DECRYPT
        }
    } catch (e: RemoteError.NotFound) {
        ServerMode.SERVER_DECRYPT
    }

    /**
     * Logs in to a client-decrypt server with its access password, which lets this session list
     * vaults; vault passwords then never go to the server. [accessPassword] is left untouched
     * (caller zeroes it). A refused password is [RemoteError.WrongAccessPassword].
     */
    suspend fun login(accessPassword: CharArray) {
        val body = PasswordBody(accessPassword)
        try {
            fetch(Request.Builder().url(url("login")).post(body).build(), unlock = true) {}
        } catch (e: RemoteError.WrongPassword) {
            throw RemoteError.WrongAccessPassword
        } finally {
            body.wipe()
        }
    }

    suspend fun vaults(): List<RemoteVault> =
        fetch(Request.Builder().url(url("vaults")).get().build()) {
            json.decodeFromString<VaultsResponse>(it.body!!.string()).vaults
        }

    /**
     * Unlock [id]. [password] is left untouched (caller zeroes it); the
     * request body is built in a private char/byte copy that is zeroed once
     * the call ends. On 401 the result is [RemoteError.WrongPassword].
     */
    suspend fun unlock(id: String, password: CharArray) {
        val body = PasswordBody(password)
        try {
            fetch(Request.Builder().url(vaultUrl(id, "unlock")).post(body).build(), unlock = true) {}
        } finally {
            body.wipe()
        }
    }

    suspend fun lock(id: String) {
        fetch(Request.Builder().url(vaultUrl(id, "lock")).post(jsonBody("{}")).build()) {}
    }

    suspend fun list(id: String, path: String): Listing =
        fetch(Request.Builder().url(vaultUrl(id, "list", mapOf("path" to path))).get().build()) {
            json.decodeFromString<Listing>(it.body!!.string())
        }

    /** The streaming URL of a file; ExoPlayer requests it through [httpClient] so the session cookie is sent. */
    internal fun fileUrl(id: String, path: String): HttpUrl = vaultUrl(id, "file", mapOf("path" to path))

    /** Caller must close the returned [Response]. Error statuses throw (body already closed). */
    suspend fun openFile(id: String, path: String, range: LongRange?): Response {
        val b = Request.Builder().url(vaultUrl(id, "file", mapOf("path" to path))).get()
        if (range != null) b.header("Range", "bytes=${range.first}-${range.last}")
        return call(b.build())
    }

    /** JPEG bytes; empty array when the server answers 204 (file cannot be previewed). */
    suspend fun thumb(id: String, path: String, size: Int): ByteArray =
        fetch(Request.Builder().url(vaultUrl(id, "thumb", mapOf("path" to path, "size" to size.toString()))).get().build()) {
            if (it.code == 204) ByteArray(0) else it.body!!.bytes()
        }

    suspend fun upload(id: String, dir: String, name: String, body: RequestBody) {
        fetch(Request.Builder().url(vaultUrl(id, "upload", mapOf("path" to dir, "name" to name))).post(body).build()) {}
    }

    suspend fun mkdir(id: String, path: String) {
        fetch(Request.Builder().url(vaultUrl(id, "mkdir")).post(jsonBody(obj("path" to path))).build()) {}
    }

    suspend fun move(id: String, from: String, to: String) {
        fetch(Request.Builder().url(vaultUrl(id, "move")).post(jsonBody(obj("from" to from, "to" to to))).build()) {}
    }

    suspend fun delete(id: String, path: String) {
        fetch(Request.Builder().url(vaultUrl(id, "entry", mapOf("path" to path))).delete().build()) {}
    }

    suspend fun logout() {
        fetch(Request.Builder().url(url("logout")).post(jsonBody("{}")).build()) {}
    }

    /** Forget the session cookie and drop pooled connections. */
    fun close() {
        jar.clear()
        httpClient.connectionPool.evictAll()
    }

    private fun obj(vararg pairs: Pair<String, String>) =
        pairs.joinToString(",", "{", "}") { (k, v) -> "\"$k\":${kotlinx.serialization.json.JsonPrimitive(v)}" }

    // Reading or closing a response body is blocking socket I/O; callers are often on the main
    // thread (Compose), so it runs on IO. (Over https StrictMode does not flag it; over http it
    // throws NetworkOnMainThreadException.)
    private suspend fun <T> fetch(request: Request, unlock: Boolean = false, read: (Response) -> T): T =
        withContext(Dispatchers.IO) { call(request, unlock).use(read) }

    private suspend fun call(request: Request, unlock: Boolean = false): Response {
        val response = try {
            httpClient.newCall(request).await()
        } catch (e: IOException) {
            throw mapIo(e)
        }
        if (response.isSuccessful) return response
        response.use { throw mapStatus(it, unlock) }
    }

    private fun mapIo(e: IOException): RemoteError {
        var t: Throwable? = e
        while (t != null) {
            if (t is PinMismatchException) return RemoteError.CertMismatch(t.actual)
            t = t.cause
        }
        return RemoteError.Network(e)
    }

    private fun mapStatus(r: Response, unlock: Boolean): RemoteError {
        val detail = try {
            json.decodeFromString<ApiErrorBody>(r.body?.string().orEmpty()).error
        } catch (_: Exception) {
            ""
        }
        return when (r.code) {
            401 -> if (unlock) RemoteError.WrongPassword else RemoteError.Locked
            403 -> if (detail == "cross-origin request rejected" || detail == "bad origin") {
                RemoteError.OriginRejected
            } else {
                RemoteError.Forbidden(detail)
            }
            404 -> RemoteError.NotFound
            409 -> RemoteError.Exists
            429 -> RemoteError.RateLimited
            else -> RemoteError.Http(r.code, detail)
        }
    }

    private companion object {
        val JSON = "application/json; charset=utf-8".toMediaType()
    }
}

private suspend fun Call.await(): Response = suspendCancellableCoroutine { cont ->
    cont.invokeOnCancellation { cancel() }
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            if (!cont.isCancelled) cont.resumeWithException(e)
        }

        override fun onResponse(call: Call, response: Response) {
            cont.resume(response) { _, _, _ -> response.close() }
        }
    })
}

/**
 * `{"password":"..."}` held as UTF-8 bytes only; no String is made from the
 * password. Both the escaped char buffer and the byte array are zeroed by [wipe].
 */
private class PasswordBody(password: CharArray) : RequestBody() {
    private val bytes: ByteArray

    init {
        val head = "{\"password\":\""
        val tail = "\"}"
        val chars = CharArray(head.length + password.size * 6 + tail.length)
        var n = 0
        for (c in head) chars[n++] = c
        for (c in password) {
            when {
                c == '"' -> { chars[n++] = '\\'; chars[n++] = '"' }
                c == '\\' -> { chars[n++] = '\\'; chars[n++] = '\\' }
                c < ' ' -> {
                    chars[n++] = '\\'; chars[n++] = 'u'
                    val hex = "0123456789abcdef"
                    chars[n++] = '0'; chars[n++] = '0'
                    chars[n++] = hex[(c.code shr 4) and 0xf]; chars[n++] = hex[c.code and 0xf]
                }
                else -> chars[n++] = c
            }
        }
        for (c in tail) chars[n++] = c
        val encoded = Charsets.UTF_8.encode(CharBuffer.wrap(chars, 0, n))
        bytes = ByteArray(encoded.remaining())
        encoded.get(bytes)
        if (encoded.hasArray()) encoded.array().fill(0)
        chars.fill('\u0000')
    }

    override fun contentType() = "application/json; charset=utf-8".toMediaType()
    override fun contentLength() = bytes.size.toLong()
    override fun writeTo(sink: BufferedSink) {
        sink.write(bytes)
    }

    fun wipe() = bytes.fill(0)
}

/** Session cookie store: memory only, per-host, cleared by [clear]. */
private class MemoryCookieJar : CookieJar {
    private val cookies = mutableListOf<Cookie>()

    @Synchronized override fun saveFromResponse(url: HttpUrl, cookies: List<Cookie>) {
        for (c in cookies) {
            this.cookies.removeAll { it.name == c.name && it.domain == c.domain && it.path == c.path }
            if (c.expiresAt > System.currentTimeMillis()) this.cookies.add(c)
        }
    }

    @Synchronized override fun loadForRequest(url: HttpUrl): List<Cookie> {
        val now = System.currentTimeMillis()
        cookies.removeAll { it.expiresAt <= now }
        return cookies.filter { it.matches(url) }
    }

    @Synchronized fun clear() = cookies.clear()
}
