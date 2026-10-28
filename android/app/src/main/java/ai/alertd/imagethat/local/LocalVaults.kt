package ai.alertd.imagethat.local

import ai.alertd.imagethat.keys.SavedFolder
import ai.alertd.imagethat.keys.StateStore
import android.content.Context
import android.net.Uri
import java.io.File
import mobile.Mobile
import mobile.Vault

/** Failures opening or creating a local vault, mapped from the Go engine's message prefixes. */
sealed class LocalError(message: String, cause: Throwable? = null) : Exception(message, cause) {
    class WrongPassword(m: String, c: Throwable? = null) : LocalError(m, c)
    class NoConfig(m: String, c: Throwable? = null) : LocalError(m, c)
    class NotFound(m: String, c: Throwable? = null) : LocalError(m, c)
    class Exists(m: String, c: Throwable? = null) : LocalError(m, c)
    class InvalidPath(m: String, c: Throwable? = null) : LocalError(m, c)
    /** The picked folder is no longer accessible (SAF permission revoked, provider gone). UI: "Pick again". */
    class Unavailable(m: String, c: Throwable? = null) : LocalError(m, c)
    class Other(m: String, c: Throwable? = null) : LocalError(m, c)

    companion object {
        fun from(e: Throwable): LocalError {
            if (e is LocalError) return e
            val m = e.message ?: e.toString()
            return when {
                m.startsWith("wrong_password") -> WrongPassword(m, e)
                m.startsWith("no_config") -> NoConfig(m, e)
                m.startsWith("not_found") -> NotFound(m, e)
                m.startsWith("exists") -> Exists(m, e)
                m.startsWith("invalid_path") -> InvalidPath(m, e)
                e is SecurityException -> Unavailable(m, e)
                else -> Other(m, e)
            }
        }
    }
}

/**
 * The built-in offline vault (app-private, no-backup) and vaults in user-picked
 * SAF folders. Opened [Vault]s are tracked so [closeAll] can lock everything.
 * Passwords arrive as CharArray and are zeroed after the engine call (the
 * String copy that gomobile needs is unavoidable).
 */
class LocalVaults(
    private val context: Context,
    private val state: StateStore,
    private val offlineDir: File = File(context.noBackupFilesDir, "offline-vault"),
) {
    private val open = ArrayList<Vault>()

    fun offlineExists(): Boolean = File(offlineDir, "gocryptfs.conf").exists()

    fun createOffline(password: CharArray) {
        try {
            require(password.size >= MIN_OFFLINE_PASSWORD) { "offline password must be at least $MIN_OFFLINE_PASSWORD characters" }
            check(!offlineExists()) { "offline vault already exists" }
            offlineDir.mkdirs()
            withPassword(password) { Mobile.initLocal(offlineDir.absolutePath, it) }
            state.update { it.copy(offlineVaultCreated = true) }
        } finally {
            password.fill('\u0000')
        }
    }

    fun openOffline(password: CharArray): Vault =
        track(withPassword(password) { Mobile.openLocal(offlineDir.absolutePath, it) })

    fun openFolder(folder: SavedFolder, password: CharArray): Vault {
        try {
            val storage = storageFor(Uri.parse(folder.treeUri))
            return track(withPassword(password) { Mobile.openStorage(storage, it) })
        } finally {
            password.fill('\u0000')
        }
    }

    fun createInFolder(treeUri: Uri, password: CharArray) {
        try {
            val storage = storageFor(treeUri)
            withPassword(password) { Mobile.initStorage(storage, it) }
        } finally {
            password.fill('\u0000')
        }
    }

    @Synchronized
    fun closeAll() {
        open.forEach { runCatching { it.close() } }
        open.clear()
    }

    /** Checks the persisted grant and probes the tree so a revoked folder fails as [LocalError.Unavailable]. */
    private fun storageFor(treeUri: Uri): SafStorage {
        val granted = context.contentResolver.persistedUriPermissions.any {
            it.uri == treeUri && it.isReadPermission && it.isWritePermission
        }
        if (!granted) throw LocalError.Unavailable("no persisted access to $treeUri")
        val storage = SafStorage(context.contentResolver, treeUri)
        try {
            storage.probe()
        } catch (e: Exception) {
            throw LocalError.Unavailable("folder is not accessible: ${e.message}", e)
        }
        return storage
    }

    @Synchronized
    private fun track(v: Vault): Vault = v.also { open.add(it) }

    private fun <T> withPassword(password: CharArray, block: (String) -> T): T {
        try {
            return block(String(password))
        } catch (e: LocalError) {
            throw e
        } catch (e: Exception) {
            throw LocalError.from(e)
        } finally {
            password.fill('\u0000')
        }
    }

    companion object {
        const val MIN_OFFLINE_PASSWORD = 8
    }
}
