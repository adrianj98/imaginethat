package ai.alertd.imagethat.keys

import java.util.Base64
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher

/** The Keystore master key that wraps the saved-password data key. Auth per use. */
interface MasterKey {
    /** A DECRYPT cipher for a blob with [iv]; throws [MasterKeyInvalidated] when the key is gone or invalidated. */
    fun decryptCipher(iv: ByteArray): Cipher

    /** An ENCRYPT cipher under a newly generated key (any old key is replaced). */
    fun encryptCipher(): Cipher

    fun delete()
}

/** The master key no longer exists or was invalidated (fingerprints or screen lock changed). */
class MasterKeyInvalidated(cause: Throwable? = null) : Exception("master key invalidated", cause)

sealed interface AppUnlock {
    /** [cleared]: the saved passwords were lost (key invalidated or unreadable) and start empty. */
    class Ok(val passwords: SavedPasswords, val cleared: Boolean) : AppUnlock
    object Cancelled : AppUnlock
}

/**
 * The app-open unlock: one authentication of a master-key cipher yields the data key (DEK) of the
 * saved-password map. With a stored wrapped DEK the prompt authorises a DECRYPT cipher; on first
 * run (or after invalidation) it authorises an ENCRYPT cipher that wraps a fresh DEK. Either way
 * the user authenticates once and the DEK then stays in memory for the session.
 *
 * [prompt] shows the biometric/device-credential prompt for a cipher; null means cancelled.
 */
class AppUnlocker(
    private val master: MasterKey,
    private val store: StateStore,
    private val prompt: suspend (Cipher) -> Cipher?,
) {
    suspend fun unlock(): AppUnlock {
        val st = store.load()
        var cleared = false
        val wrapped = st.wrappedDek?.let { decodeOrNull(it) }
        if (st.wrappedDek != null) {
            val cipher = if (wrapped == null || wrapped.size <= IV_LEN) null else try {
                master.decryptCipher(wrapped.copyOfRange(0, IV_LEN))
            } catch (e: MasterKeyInvalidated) {
                null
            }
            if (cipher != null) {
                val ready = prompt(cipher) ?: return AppUnlock.Cancelled
                val dek = try {
                    ready.doFinal(wrapped!!, IV_LEN, wrapped.size - IV_LEN)
                } catch (e: AEADBadTagException) {
                    null
                }
                if (dek != null) return opened(dek, st.savedPasswords)
                // Authenticated but the wrapped key is unreadable: start over below (a second prompt,
                // only on this corrupted-state path).
            }
            cleared = true
        }
        master.delete()
        val enc = master.encryptCipher()
        val ready = prompt(enc) ?: return AppUnlock.Cancelled
        val dek = SavedPasswords.newDek()
        try {
            val blob = Base64.getEncoder().encodeToString(ready.iv + ready.doFinal(dek))
            val emptyMap = SavedPasswords.sealMap(dek, emptyMap())
            store.update { it.copy(wrappedDek = blob, savedPasswords = emptyMap) }
            return AppUnlock.Ok(SavedPasswords(dek, emptyMap(), ::persist), cleared)
        } finally {
            dek.fill(0)
        }
    }

    private fun opened(dek: ByteArray, sealed: String?): AppUnlock {
        try {
            var cleared = false
            val map = if (sealed == null) mutableMapOf() else try {
                SavedPasswords.openMap(dek, sealed)
            } catch (e: Exception) {
                // An unreadable map under a good key: drop it rather than lock the user out.
                cleared = true
                mutableMapOf()
            }
            try {
                val saved = SavedPasswords(dek, map, ::persist)
                if (cleared) persist(SavedPasswords.sealMap(dek, emptyMap()))
                return AppUnlock.Ok(saved, cleared)
            } finally {
                map.values.forEach { it.fill('\u0000') }
            }
        } finally {
            dek.fill(0)
        }
    }

    private fun persist(blob: String) = store.update { it.copy(savedPasswords = blob) }

    private fun decodeOrNull(s: String): ByteArray? = try {
        Base64.getDecoder().decode(s)
    } catch (e: IllegalArgumentException) {
        null
    }

    private companion object {
        const val IV_LEN = 12
    }
}
