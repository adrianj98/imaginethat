package ai.alertd.imagethat.keys

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.util.Base64
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
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

sealed interface Unwrap {
    class Ok(val secret: ByteArray) : Unwrap
    /** Biometrics changed (or the key is gone); the wrapped secret was deleted. */
    object Invalidated : Unwrap
    object Cancelled : Unwrap
    object None : Unwrap
}

/**
 * Android Keystore-backed secrets. [stateCipher] uses a key that needs no
 * authentication (the app lists remembered servers before auth). [wrap] and
 * [unwrap] use per-secret keys gated by BIOMETRIC_STRONG and invalidated by
 * biometric enrolment. [requireAuth] = false exists only for tests on
 * emulators with no enrolled biometrics.
 */
class KeyVault(
    private val context: Context,
    private val requireAuth: Boolean = true,
    val stateFile: File = File(context.noBackupFilesDir, "state.bin"),
    /** Test hook replacing the biometric prompt; return null to simulate cancel. */
    private val authenticator: (suspend (FragmentActivity?, Cipher) -> Cipher?)? = null,
) {
    private val ks: KeyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    private val stateCipher = GcmCipher { key(STATE_ALIAS, authRequired = false) }
    private val store = StateStore(stateFile, stateCipher)

    fun stateCipher(): SecretCipher = stateCipher

    fun state(): AppState = store.load()

    fun store(): StateStore = store

    suspend fun wrap(activity: FragmentActivity?, id: String, secret: ByteArray) {
        // The new key goes under whichever alias the current blob is not using, so a
        // cancelled or failed re-wrap leaves the old key and blob untouched.
        val current = store.load().wrapped[id]?.let { split(id, it).first }
        val alias = if (current == bioAlias(id)) bioAlias(id) + ".new" else bioAlias(id)
        ks.deleteEntry(alias)
        try {
            val cipher = Cipher.getInstance(TRANSFORM).apply { init(Cipher.ENCRYPT_MODE, key(alias, requireAuth)) }
            val ready = prompt(activity, cipher)
            if (ready == null) {
                ks.deleteEntry(alias)
                return
            }
            val blob = Base64.encodeToString(ready.iv + ready.doFinal(secret), Base64.NO_WRAP)
            store.update { it.copy(wrapped = it.wrapped + (id to "$alias:$blob")) }
        } catch (e: Exception) {
            ks.deleteEntry(alias)
            throw e
        }
        if (current != null && current != alias) ks.deleteEntry(current)
    }

    suspend fun unwrap(activity: FragmentActivity?, id: String): Unwrap {
        val stored = store.load().wrapped[id] ?: return Unwrap.None
        val (alias, encoded) = split(id, stored)
        val blob = Base64.decode(encoded, Base64.NO_WRAP)
        val cipher = Cipher.getInstance(TRANSFORM)
        try {
            val key = ks.getKey(alias, null) as SecretKey? ?: return invalidated(id)
            cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, blob.copyOfRange(0, IV_LEN)))
        } catch (e: KeyPermanentlyInvalidatedException) {
            return invalidated(id)
        } catch (e: UnrecoverableKeyException) {
            return invalidated(id)
        } catch (e: KeyStoreException) {
            return invalidated(id)
        }
        val ready = prompt(activity, cipher) ?: return Unwrap.Cancelled
        return try {
            Unwrap.Ok(ready.doFinal(blob, IV_LEN, blob.size - IV_LEN))
        } catch (e: AEADBadTagException) {
            invalidated(id)
        }
    }

    fun forget(id: String) {
        ks.deleteEntry(bioAlias(id))
        ks.deleteEntry(bioAlias(id) + ".new")
        if (store.load().wrapped.containsKey(id)) store.update { it.copy(wrapped = it.wrapped - id) }
    }

    private fun bioAlias(id: String) = BIO_PREFIX + id

    /** Blobs are "alias:base64"; a blob without a prefix uses the canonical alias. */
    private fun split(id: String, stored: String): Pair<String, String> =
        if (':' in stored) stored.substringBefore(':') to stored.substringAfter(':') else bioAlias(id) to stored

    private suspend fun prompt(activity: FragmentActivity?, cipher: Cipher): Cipher? = when {
        authenticator != null -> authenticator.invoke(activity, cipher)
        requireAuth -> authenticate(activity, cipher)
        else -> cipher
    }

    private fun invalidated(id: String): Unwrap {
        forget(id)
        return Unwrap.Invalidated
    }

    private fun key(alias: String, authRequired: Boolean): SecretKey {
        (ks.getKey(alias, null) as SecretKey?)?.let { return it }
        val b = KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
        if (authRequired) {
            b.setUserAuthenticationRequired(true)
            b.setInvalidatedByBiometricEnrollment(true)
        }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            .apply { init(b.build()) }.generateKey()
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
                            else -> cont.resumeWithException(IllegalStateException("biometric error $code: $msg"))
                        }
                    }
                })
            val info = BiometricPrompt.PromptInfo.Builder()
                .setTitle("Unlock vault")
                .setAllowedAuthenticators(BIOMETRIC_STRONG)
                .setNegativeButtonText("Cancel")
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

    private companion object {
        const val STATE_ALIAS = "imagethat.state"
        const val BIO_PREFIX = "imagethat.bio."
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
    }
}
