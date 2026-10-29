package ai.alertd.imagethat.keys

import java.io.File
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class AppUnlockerTest {
    @get:Rule val tmp = TemporaryFolder()

    private object XorCipher : SecretCipher {
        override fun encrypt(plain: ByteArray) = ByteArray(plain.size) { (plain[it].toInt() xor 0x5A).toByte() }
        override fun decrypt(blob: ByteArray) = encrypt(blob)
    }

    /** A software stand-in for the Keystore master key; [invalidate] mimics a fingerprint change. */
    private class FakeMaster : MasterKey {
        var key: SecretKeySpec? = null
        var invalidated = false
        var created = 0
        override fun decryptCipher(iv: ByteArray): Cipher {
            val k = key ?: throw MasterKeyInvalidated()
            if (invalidated) throw MasterKeyInvalidated()
            return Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.DECRYPT_MODE, k, GCMParameterSpec(128, iv)) }
        }
        override fun encryptCipher(): Cipher {
            created++
            invalidated = false
            key = SecretKeySpec(SavedPasswords.newDek(), "AES")
            return Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key) }
        }
        override fun delete() { key = null }
    }

    private val master = FakeMaster()
    private val store by lazy { StateStore(File(tmp.root, "state.bin"), XorCipher) }
    private val prompted = ArrayList<Int>()
    private var cancel = false
    private val unlocker by lazy {
        AppUnlocker(master, store) { c -> prompted += c.blockSize; if (cancel) null else c }
    }

    private fun unlock() = runBlocking { unlocker.unlock() }
    private fun ok(): AppUnlock.Ok = unlock() as AppUnlock.Ok

    @Test fun firstRunCreatesAWrappedKeyWithOnePrompt() {
        val r = ok()
        assertFalse(r.cleared)
        assertEquals(1, prompted.size)
        assertEquals(1, master.created)
        assertNotNull(store.load().wrappedDek)
        assertTrue(r.passwords.ids().isEmpty())
    }

    @Test fun savedPasswordsSurviveTheNextUnlockWithOnePrompt() {
        ok().passwords.put("offline", "secret-1".toCharArray())
        prompted.clear()
        val r = ok()
        assertEquals(1, prompted.size)
        assertEquals(1, master.created) // the existing key was reused
        assertFalse(r.cleared)
        assertArrayEquals("secret-1".toCharArray(), r.passwords.get("offline"))
    }

    @Test fun changesAfterUnlockNeedNoFurtherPrompt() {
        val p = ok().passwords
        p.put("a", "1".toCharArray())
        p.put("b", "2".toCharArray())
        p.remove("a")
        assertEquals(1, prompted.size)
        assertEquals(setOf("b"), ok().passwords.ids())
    }

    @Test fun cancelReturnsCancelledAndChangesNothing() {
        ok().passwords.put("offline", "pw".toCharArray())
        val before = store.load()
        cancel = true
        assertSame(AppUnlock.Cancelled, unlock())
        assertEquals(before, store.load())
    }

    @Test fun cancelOnFirstRunStoresNothing() {
        cancel = true
        assertSame(AppUnlock.Cancelled, unlock())
        assertNull(store.load().wrappedDek)
    }

    @Test fun invalidatedKeyClearsPasswordsAndMakesAFreshKeyInOnePrompt() {
        ok().passwords.put("offline", "pw".toCharArray())
        val oldWrapped = store.load().wrappedDek
        master.invalidated = true
        prompted.clear()
        val r = ok()
        assertTrue(r.cleared)
        assertEquals(1, prompted.size)
        assertTrue(r.passwords.ids().isEmpty())
        assertFalse(oldWrapped == store.load().wrappedDek)
        // The fresh key works and the notice is not repeated.
        r.passwords.put("offline", "new".toCharArray())
        val again = ok()
        assertFalse(again.cleared)
        assertArrayEquals("new".toCharArray(), again.passwords.get("offline"))
    }

    @Test fun missingKeyIsTreatedAsInvalidated() {
        ok().passwords.put("offline", "pw".toCharArray())
        master.key = null
        val r = ok()
        assertTrue(r.cleared)
        assertTrue(r.passwords.ids().isEmpty())
    }

    @Test fun cancelAfterInvalidationStillShowsTheNoticeNextTime() {
        ok()
        master.invalidated = true
        cancel = true
        assertSame(AppUnlock.Cancelled, unlock())
        cancel = false
        prompted.clear()
        assertTrue(ok().cleared)
        // The cancelled attempt replaced the master key; the next open must not try the old wrapped key first.
        assertEquals(1, prompted.size)
    }

    @Test fun unreadableWrappedKeyStartsOver() {
        ok().passwords.put("offline", "pw".toCharArray())
        store.update { it.copy(wrappedDek = "AAAA" + it.wrappedDek!!.drop(4)) }
        val r = ok()
        assertTrue(r.cleared)
        assertTrue(r.passwords.ids().isEmpty())
        assertFalse(ok().cleared)
    }

    @Test fun unreadableMapUnderAGoodKeyIsDroppedNotFatal() {
        ok().passwords.put("offline", "pw".toCharArray())
        store.update { it.copy(savedPasswords = "garbage") }
        val r = ok()
        assertTrue(r.cleared)
        assertTrue(r.passwords.ids().isEmpty())
        r.passwords.put("x", "y".toCharArray())
        assertEquals(setOf("x"), ok().passwords.ids())
    }

    @Test fun stateHoldsNoPlaintextPassword() {
        ok().passwords.put("server:s1:v1", "plaintext-marker-123".toCharArray())
        val raw = String(XorCipher.decrypt(File(tmp.root, "state.bin").readBytes()), Charsets.UTF_8)
        assertFalse(raw.contains("plaintext-marker-123"))
        assertFalse(raw.contains("server:s1:v1"))
    }
}
