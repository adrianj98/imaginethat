package ai.alertd.imagethat.keys

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.security.KeyStore
import javax.crypto.KeyGenerator
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/** The real Android Keystore with requireAuth = false (an emulator has no enrolled biometrics). */
@RunWith(AndroidJUnit4::class)
class KeyVaultTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    private val alias = "imagethat.vault-master.kvtest-${System.nanoTime()}"
    private lateinit var file: File
    private lateinit var vault: KeyVault
    private var cancel = false

    @Before fun setUp() {
        file = File(context.cacheDir, "kv-state-${System.nanoTime()}.bin")
        vault = KeyVault(context, requireAuth = false, stateFile = file, masterAlias = alias, authenticator = { _, c -> if (cancel) null else c })
    }

    @After fun tearDown() {
        ks.deleteEntry(alias)
        listOf("imagethat.bio.t1", "imagethat.bio.t2.new").forEach(ks::deleteEntry)
        file.delete()
    }

    private fun unlock() = runBlocking { vault.unlockApp(null) }

    @Test fun stateCipherRoundTrip() {
        val c = vault.stateCipher()
        val plain = "hello state".toByteArray()
        val blob = c.encrypt(plain)
        assertFalse(blob.contentEquals(plain))
        assertArrayEquals(plain, c.decrypt(blob))
    }

    @Test fun defaultStateFileIsInNoBackupDir() {
        val v = KeyVault(context, requireAuth = false)
        assertEquals(context.noBackupFilesDir, v.stateFile.parentFile)
    }

    @Test fun firstUnlockCreatesTheMasterKeyAndSavedPasswordsRoundTrip() {
        val r = unlock() as AppUnlock.Ok
        assertFalse(r.cleared)
        assertTrue(ks.containsAlias(alias))
        assertNotNull(vault.state().wrappedDek)
        r.passwords.put(PasswordIds.OFFLINE, "secret-pw".toCharArray())
        r.passwords.wipe()

        val again = unlock() as AppUnlock.Ok
        assertFalse(again.cleared)
        assertArrayEquals("secret-pw".toCharArray(), again.passwords.get(PasswordIds.OFFLINE))
    }

    @Test fun deletedMasterKeyClearsSavedPasswordsAndStartsFresh() {
        (unlock() as AppUnlock.Ok).passwords.put(PasswordIds.OFFLINE, "pw".toCharArray())
        ks.deleteEntry(alias) // what a fingerprint change leaves behind (the key is unusable)
        val r = unlock() as AppUnlock.Ok
        assertTrue(r.cleared)
        assertTrue(r.passwords.ids().isEmpty())
        assertTrue(ks.containsAlias(alias))
        assertFalse((unlock() as AppUnlock.Ok).cleared)
    }

    @Test fun cancelledPromptIsCancelledAndKeepsSavedPasswords() {
        (unlock() as AppUnlock.Ok).passwords.put(PasswordIds.OFFLINE, "pw".toCharArray())
        cancel = true
        assertSame(AppUnlock.Cancelled, unlock())
        cancel = false
        assertArrayEquals("pw".toCharArray(), (unlock() as AppUnlock.Ok).passwords.get(PasswordIds.OFFLINE))
    }

    @Test fun clearSavedPasswordsDeletesKeyAndBlobs() {
        (unlock() as AppUnlock.Ok).passwords.put(PasswordIds.OFFLINE, "pw".toCharArray())
        vault.clearSavedPasswords()
        assertFalse(ks.containsAlias(alias))
        assertNull(vault.state().wrappedDek)
        assertNull(vault.state().savedPasswords)
    }

    @Test fun migrationRemovesLegacyBiometricKeysAndWrappedSecrets() {
        for (a in listOf("imagethat.bio.t1", "imagethat.bio.t2.new")) {
            val spec = KeyGenParameterSpec.Builder(a, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build()
            KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply { init(spec) }.generateKey()
        }
        vault.store().update { it.copy(wrapped = mapOf("t1" to "imagethat.bio.t1:AAAA"), offlineVaultCreated = true) }
        vault.migrateLegacy()
        assertFalse(ks.containsAlias("imagethat.bio.t1"))
        assertFalse(ks.containsAlias("imagethat.bio.t2.new"))
        assertTrue(vault.state().wrapped.isEmpty())
        assertTrue(vault.state().offlineVaultCreated) // nothing else is touched
        assertTrue(ks.containsAlias("imagethat.state"))
    }
}
