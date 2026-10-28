package ai.alertd.imagethat.keys

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.security.KeyStore
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class KeyVaultTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private lateinit var file: File
    private lateinit var vault: KeyVault

    @Before fun setUp() {
        file = File(context.cacheDir, "kv-state-${System.nanoTime()}.bin")
        vault = KeyVault(context, requireAuth = false, stateFile = file)
    }

    @After fun tearDown() {
        vault.forget("t1")
        file.delete()
    }

    // wrap/unwrap take an activity only for BiometricPrompt, which the
    // requireAuth=false hook never shows.
    @Test fun stateCipherRoundTrip() {
        val c = vault.stateCipher()
        val plain = "hello state".toByteArray()
        val blob = c.encrypt(plain)
        assertFalse(blob.contentEquals(plain))
        assertArrayEquals(plain, c.decrypt(blob))
    }

    @Test fun wrapUnwrapRoundTrip() = runBlocking {
        val secret = ByteArray(32) { it.toByte() }
        vault.wrap(null, "t1", secret)
        assertTrue(vault.state().wrapped.containsKey("t1"))
        val r = vault.unwrap(null, "t1")
        assertTrue(r is Unwrap.Ok)
        assertArrayEquals(secret, (r as Unwrap.Ok).secret)
    }

    @Test fun unwrapUnknownIsNone() = runBlocking {
        assertEquals(Unwrap.None, vault.unwrap(null, "t1"))
    }

    @Test fun deletedKeyIsInvalidatedAndStateCleaned() = runBlocking {
        vault.wrap(null, "t1", byteArrayOf(1, 2, 3))
        KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry("imagethat.bio.t1")
        assertEquals(Unwrap.Invalidated, vault.unwrap(null, "t1"))
        assertFalse(vault.state().wrapped.containsKey("t1"))
    }

    @Test fun defaultStateFileIsInNoBackupDir() {
        val v = KeyVault(context, requireAuth = false)
        assertEquals(context.noBackupFilesDir, v.stateFile.parentFile)
    }

    @Test fun cancelledRewrapKeepsOldSecret() = runBlocking {
        vault.wrap(null, "t1", byteArrayOf(9, 9))
        val cancelling = KeyVault(context, requireAuth = false, stateFile = file, authenticator = { _, _ -> null })
        cancelling.wrap(null, "t1", byteArrayOf(1))
        assertArrayEquals(byteArrayOf(9, 9), (vault.unwrap(null, "t1") as Unwrap.Ok).secret)
    }

    @Test fun rewrapReplacesSecretAndOldKey() = runBlocking {
        vault.wrap(null, "t1", byteArrayOf(1))
        vault.wrap(null, "t1", byteArrayOf(2))
        assertArrayEquals(byteArrayOf(2), (vault.unwrap(null, "t1") as Unwrap.Ok).secret)
        vault.wrap(null, "t1", byteArrayOf(3))
        assertArrayEquals(byteArrayOf(3), (vault.unwrap(null, "t1") as Unwrap.Ok).secret)
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertEquals(1, listOf("imagethat.bio.t1", "imagethat.bio.t1.new").count { ks.containsAlias(it) })
    }

    @Test fun tamperedBlobIsInvalidated() = runBlocking {
        vault.wrap(null, "t1", byteArrayOf(1, 2, 3))
        vault.store().update { s ->
            val (alias, b64) = s.wrapped["t1"]!!.split(":")
            val raw = android.util.Base64.decode(b64, android.util.Base64.NO_WRAP)
            raw[raw.size - 1] = (raw[raw.size - 1].toInt() xor 1).toByte()
            s.copy(wrapped = mapOf("t1" to alias + ":" + android.util.Base64.encodeToString(raw, android.util.Base64.NO_WRAP)))
        }
        assertEquals(Unwrap.Invalidated, vault.unwrap(null, "t1"))
        assertFalse(vault.state().wrapped.containsKey("t1"))
    }
}
