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
    /**
     * Legacy (before the app lock): per-vault secrets wrapped by imagethat.bio.* Keystore keys.
     * Only read so the migration can clear it; nothing writes it any more.
     */
    val wrapped: Map<String, String> = emptyMap(),
    /** base64(iv + ciphertext): the saved-password data key, wrapped by the auth-per-use master key. */
    val wrappedDek: String? = null,
    /** base64(iv + ciphertext): the saved-password map (see SavedPasswords), encrypted under the data key. */
    val savedPasswords: String? = null,
    val offlineVaultCreated: Boolean = false,
    /** Auto-lock after this many minutes without a touch (not secret; spec default 5). */
    val idleTimeoutMinutes: Int = 5,
)
