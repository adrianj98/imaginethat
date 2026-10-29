package ai.alertd.imagethat.keys

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import java.io.File
import java.security.KeyStore
import java.security.KeyStoreException
import java.security.UnrecoverableKeyException
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import kotlinx.coroutines.suspendCancellableCoroutine

/**
 * Android Keystore-backed secrets. [stateCipher] uses a key that needs no authentication (the
 * state file holds what exists, and saved passwords only as ciphertext). The master key
 * ([masterAlias]) needs authentication for every use (biometric strong, or the device PIN /
 * pattern / password on API 30+) and is invalidated by biometric enrolment; it wraps the data key
 * of the saved-password map (see [AppUnlocker]). [requireAuth] = false exists only for tests on
 * emulators with no enrolled biometrics or screen lock.
 */
class KeyVault(
    private val context: Context,
    private val requireAuth: Boolean = true,
    val stateFile: File = File(context.noBackupFilesDir, "state.bin"),
    /** Test hook replacing the biometric prompt; return null to simulate cancel. */
    private val authenticator: (suspend (FragmentActivity?, Cipher) -> Cipher?)? = null,
    /** Tests use their own alias so they never touch the app's real master key. */
    private val masterAlias: String = MASTER_ALIAS,
) {
    private val ks: KeyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    private val stateCipher = GcmCipher { stateKey() }
    private val store = StateStore(stateFile, stateCipher)

    fun stateCipher(): SecretCipher = stateCipher

    fun state(): AppState = store.load()

    fun store(): StateStore = store

    /**
     * The app-open authentication: one prompt yields the saved passwords for this session.
     * Throws [IllegalStateException] for a biometric error other than cancel (lockout, none enrolled).
     */
    suspend fun unlockApp(activity: FragmentActivity?): AppUnlock =
        AppUnlocker(master, store) { cipher -> prompt(activity, cipher) }.unlock()

    /** Deletes the master key and the saved passwords (they are unreadable without it). */
    fun clearSavedPasswords() {
        ks.deleteEntry(masterAlias)
        store.update { it.copy(wrappedDek = null, savedPasswords = null) }
    }

    /**
     * Removes the per-vault biometric shortcut of earlier versions: its imagethat.bio.* Keystore keys
     * and the wrapped secrets in the state file. Safe to call every time.
     */
    fun migrateLegacy() {
        for (alias in ks.aliases().toList()) if (alias.startsWith(BIO_PREFIX)) ks.deleteEntry(alias)
        if (store.load().wrapped.isNotEmpty()) store.update { it.copy(wrapped = emptyMap()) }
    }

    private val master = object : MasterKey {
        override fun decryptCipher(iv: ByteArray): Cipher {
            val c = Cipher.getInstance(TRANSFORM)
            try {
                val key = ks.getKey(masterAlias, null) as SecretKey? ?: throw MasterKeyInvalidated()
                c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, iv))
            } catch (e: KeyPermanentlyInvalidatedException) {
                throw MasterKeyInvalidated(e)
            } catch (e: UnrecoverableKeyException) {
                throw MasterKeyInvalidated(e)
            } catch (e: KeyStoreException) {
                throw MasterKeyInvalidated(e)
            }
            return c
        }

        override fun encryptCipher(): Cipher {
            ks.deleteEntry(masterAlias)
            return Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, masterKey()) }
        }

        override fun delete() = ks.deleteEntry(masterAlias)
    }

    private suspend fun prompt(activity: FragmentActivity?, cipher: Cipher): Cipher? = when {
        authenticator != null -> authenticator.invoke(activity, cipher)
        requireAuth -> authenticate(activity, cipher)
        else -> cipher
    }

    private fun masterKey(): SecretKey {
        val b = KeyGenParameterSpec.Builder(masterAlias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
        if (requireAuth) {
            b.setUserAuthenticationRequired(true)
            b.setInvalidatedByBiometricEnrollment(true)
            // Auth per use (timeout 0). API 29 can only express biometric-per-use (the default).
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                b.setUserAuthenticationParameters(0, KeyProperties.AUTH_BIOMETRIC_STRONG or KeyProperties.AUTH_DEVICE_CREDENTIAL)
            }
        }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            .apply { init(b.build()) }.generateKey()
    }

    /** The state-file key: no authentication, so the app can read what exists before unlocking. */
    private fun stateKey(): SecretKey {
        (ks.getKey(STATE_ALIAS, null) as SecretKey?)?.let { return it }
        val spec = KeyGenParameterSpec.Builder(STATE_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .build()
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply { init(spec) }.generateKey()
    }

    /** Shows the biometric prompt bound to [cipher]; null if the user cancelled. */
    private suspend fun authenticate(activity: FragmentActivity?, cipher: Cipher): Cipher? {
        requireNotNull(activity) { "an activity is required for biometric authentication" }
        return suspendCancellableCoroutine { cont ->
            val prompt = BiometricPrompt(activity, ContextCompat.getMainExecutor(activity),
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                        if (cont.isActive) cont.resume(result.cryptoObject!!.cipher)
                    }

                    override fun onAuthenticationError(code: Int, msg: CharSequence) {
                        if (!cont.isActive) return
                        when (code) {
                            BiometricPrompt.ERROR_USER_CANCELED,
                            BiometricPrompt.ERROR_NEGATIVE_BUTTON,
                            BiometricPrompt.ERROR_CANCELED -> cont.resume(null)
                            else -> cont.resumeWithException(AuthError(code, msg.toString()))
                        }
                    }
                })
            val info = BiometricPrompt.PromptInfo.Builder()
                .setTitle("Unlock ImageThat")
                .setAllowedAuthenticators(appLockAuthenticators())
                .apply { if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) setNegativeButtonText("Cancel") }
                .build()
            cont.invokeOnCancellation { prompt.cancelAuthentication() }
            prompt.authenticate(info, BiometricPrompt.CryptoObject(cipher))
        }
    }

    private class GcmCipher(private val key: () -> SecretKey) : SecretCipher {
        override fun encrypt(plain: ByteArray): ByteArray {
            val c = Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key()) }
            return c.iv + c.doFinal(plain)
        }

        override fun decrypt(blob: ByteArray): ByteArray {
            if (blob.size <= IV_LEN) throw AEADBadTagException("blob too short")
            val c = Cipher.getInstance(TRANSFORM)
                .apply { init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, blob.copyOfRange(0, IV_LEN))) }
            return c.doFinal(blob, IV_LEN, blob.size - IV_LEN)
        }
    }

    /** A BiometricPrompt error other than a cancel; [message] is the system's text for it. */
    class AuthError(val code: Int, message: String) : IllegalStateException(message)

    companion object {
        /** API 30+: a strong biometric or the device credential. API 29: strong biometric only. */
        fun appLockAuthenticators(): Int =
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) BIOMETRIC_STRONG or DEVICE_CREDENTIAL else BIOMETRIC_STRONG

        const val MASTER_ALIAS = "imagethat.vault-master"
        private const val STATE_ALIAS = "imagethat.state"
        private const val BIO_PREFIX = "imagethat.bio."
        private const val TRANSFORM = "AES/GCM/NoPadding"
        private const val IV_LEN = 12
    }
}
