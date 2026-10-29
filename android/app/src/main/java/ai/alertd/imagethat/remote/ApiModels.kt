package ai.alertd.imagethat.remote

import kotlinx.serialization.Serializable

/** Mirrors `server.Vault` (webserver/server/server.go). */
@Serializable
data class RemoteVault(val id: String, val name: String, val unlocked: Boolean)

/** Mirrors `vault.Entry` (core/vault/read.go). [mediaType] is "image", "video" or "". */
@Serializable
data class Entry(val name: String, val isDir: Boolean, val size: Long, val mediaType: String = "")

/** Mirrors `listResponse` (handlers_fs.go). [parent] is null at the vault root. */
@Serializable
data class Listing(val path: String, val parent: String? = null, val entries: List<Entry> = emptyList())

@Serializable
internal data class VaultsResponse(val vaults: List<RemoteVault>)

@Serializable
internal data class ApiErrorBody(val error: String = "")

/**
 * Failures of the remote API. Mapping from the Go server's status codes:
 * 401 -> [Locked] (or [WrongPassword] on unlock), 403 -> [OriginRejected] for
 * the CSRF/Origin check else [Forbidden], 404 -> [NotFound], 409 -> [Exists],
 * 429 -> [RateLimited], anything else -> [Http]; transport failures ->
 * [Network]; pin failure -> [CertMismatch].
 */
sealed class RemoteError(message: String, cause: Throwable? = null) : Exception(message, cause) {
    /** No session / vault locked / session expired (401). */
    object Locked : RemoteError("vault is locked")
    object WrongPassword : RemoteError("wrong password")
    object RateLimited : RemoteError("too many attempts")
    object NotFound : RemoteError("not found")
    object Exists : RemoteError("already exists")

    /** Server rejected the Origin header; a client bug, not a locked vault. */
    object OriginRejected : RemoteError("origin rejected by server")

    /** 403 that is not an Origin failure: read-only vault or backend permission denied. */
    data class Forbidden(val detail: String) : RemoteError(detail)

    /** The pinned certificate did not match; [sha256] is the certificate actually presented. */
    data class CertMismatch(val sha256: String) : RemoteError("certificate mismatch: $sha256")
    data class Http(val code: Int, val detail: String) : RemoteError("HTTP $code: $detail")
    class Network(cause: Throwable) : RemoteError(cause.message ?: "network error", cause)
}
