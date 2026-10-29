package ai.alertd.imagethat.keys

import kotlinx.serialization.Serializable

@Serializable
data class SavedServer(val id: String, val label: String, val url: String, val pinnedSha256: String?)

@Serializable
data class SavedFolder(val id: String, val label: String, val treeUri: String)

/** Everything the app remembers. Persisted only through [StateStore] (encrypted). */
@Serializable
data class AppState(
    val servers: List<SavedServer> = emptyList(),
    val folders: List<SavedFolder> = emptyList(),
    /** vault/server id -> base64(iv + ciphertext) of a secret wrapped by a biometric Keystore key. */
    val wrapped: Map<String, String> = emptyMap(),
    val offlineVaultCreated: Boolean = false,
    /** Auto-lock after this many minutes without a touch (not secret; spec default 5). */
    val idleTimeoutMinutes: Int = 5,
)
