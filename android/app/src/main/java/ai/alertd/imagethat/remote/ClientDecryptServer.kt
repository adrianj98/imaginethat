package ai.alertd.imagethat.remote

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import mobile.Mobile
import mobile.Server
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

/**
 * A client-decrypt server through the Go engine's `mobile.Server` (core/remote), the same code the
 * Mac app uses: the access-password login, the vault list and logout, and the client that
 * [ai.alertd.imagethat.local.LocalVaults.openRemote] opens vaults over (ciphertext only, decrypted
 * on the phone). It holds the session cookie, the pinned certificate and the access password it
 * logs in again with after the server's idle timeout, all in Go memory.
 *
 * Go calls block, so they run on [Dispatchers.IO]. [pinnedSha256]: as for [ApiClient].
 */
class ClientDecryptServer(private val baseUrl: String, private val pinnedSha256: String?) {
    private val lazyServer = lazy { Mobile.newServer(baseUrl, pinnedSha256.orEmpty()) }

    /** The Go client; created on first use. */
    val server: Server by lazyServer

    private val json = Json { ignoreUnknownKeys = true }

    /**
     * Logs in with the server's access password, after which vaults are listed and opened (and
     * vault passwords never sent). [accessPassword] is left untouched (caller zeroes it; the String
     * gomobile needs is unavoidable). A refused password is [RemoteError.WrongAccessPassword].
     */
    suspend fun login(accessPassword: CharArray) = call { it.login(String(accessPassword)) }

    /** The vaults; [RemoteError.Locked] until logged in. */
    suspend fun vaults(): List<RemoteVault> = call { json.decodeFromString<List<RemoteVault>>(it.vaults()) }

    /** Ends the session if one was started; the Go client forgets the access password. */
    suspend fun logout() {
        if (lazyServer.isInitialized()) call { it.logout() }
    }

    private suspend fun <T> call(block: (Server) -> T): T = withContext(Dispatchers.IO) {
        try {
            block(server)
        } catch (e: Exception) {
            val err = goRemoteError(e)
            // Go reports only that the pin failed; show the user the certificate actually presented.
            throw if (err is RemoteError.CertMismatch) {
                RemoteError.CertMismatch(baseUrl.toHttpUrlOrNull()?.let { probeCertificate(it) }.orEmpty())
            } else err
        }
    }
}

/** "remote: login: HTTP 429" and the like: an answer the engine has no code for. */
private val HTTP_STATUS = Regex("""HTTP (\d{3})(?:: (.*))?$""")

/**
 * Maps an exception from a `mobile.Server` call to [RemoteError] by the code the Go binding puts in
 * front of its message (core/errcode). [RemoteError.CertMismatch] comes back without the presented
 * certificate. An uncoded HTTP status is [RemoteError.RateLimited] (429) or [RemoteError.Http];
 * anything else (transport, TLS) is [RemoteError.Network].
 */
fun goRemoteError(e: Throwable): RemoteError {
    if (e is RemoteError) return e
    val m = e.message ?: e.toString()
    return when {
        m.startsWith("wrong_access_password:") -> RemoteError.WrongAccessPassword
        m.startsWith("wrong_password:") -> RemoteError.WrongPassword
        m.startsWith("cert_mismatch:") -> RemoteError.CertMismatch("")
        m.startsWith("locked:") -> RemoteError.Locked
        m.startsWith("not_found:") -> RemoteError.NotFound
        m.startsWith("exists:") -> RemoteError.Exists
        m.startsWith("forbidden:") -> RemoteError.Forbidden(m.substringAfter(": "))
        else -> {
            val status = HTTP_STATUS.find(m) ?: return RemoteError.Network(e)
            when (val code = status.groupValues[1].toInt()) {
                429 -> RemoteError.RateLimited
                else -> RemoteError.Http(code, status.groupValues[2])
            }
        }
    }
}
