package ai.alertd.imagethat.keys

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class StateStoreTest {
    @get:Rule val tmp = TemporaryFolder()

    private object XorCipher : SecretCipher {
        override fun encrypt(plain: ByteArray) = ByteArray(plain.size) { (plain[it].toInt() xor 0x5A).toByte() }
        override fun decrypt(blob: ByteArray) = encrypt(blob)
    }

    private val sample = AppState(
        servers = listOf(SavedServer("s1", "Home", "https://nas.example.com:8443", "ab12")),
        folders = listOf(SavedFolder("f1", "Pics", "content://tree/1")),
        wrapped = mapOf("s1" to "c2VjcmV0"),
        offlineVaultCreated = true,
    )

    @Test fun roundTrip() {
        val file = File(tmp.root, "state.bin")
        StateStore(file, XorCipher).update { sample }
        assertEquals(sample, StateStore(file, XorCipher).load())
    }

    @Test fun missingFileGivesDefault() {
        assertEquals(AppState(), StateStore(File(tmp.root, "state.bin"), XorCipher).load())
    }

    @Test fun fileDoesNotContainPlaintext() {
        val file = File(tmp.root, "state.bin")
        StateStore(file, XorCipher).update { sample }
        val text = String(file.readBytes(), Charsets.ISO_8859_1)
        assertFalse(text.contains("nas.example.com"))
    }

    @Test fun corruptFileIsRenamedAndDefaultReturned() {
        val file = File(tmp.root, "state.bin")
        file.writeBytes(byteArrayOf(1, 2, 3, 4, 5))
        assertEquals(AppState(), StateStore(file, XorCipher).load())
        assertFalse(file.exists())
        assertTrue(tmp.root.list()!!.any { it.startsWith("state.bin.corrupt") })
    }

    @Test fun updateLeavesNoTempFiles() {
        val file = File(tmp.root, "state.bin")
        val store = StateStore(file, XorCipher)
        store.update { sample }
        store.update { it.copy(offlineVaultCreated = false) }
        assertEquals(listOf("state.bin"), tmp.root.list()!!.toList())
        assertFalse(store.load().offlineVaultCreated)
    }

    @Test fun keystoreFailurePropagatesAndLeavesFileUntouched() {
        val file = File(tmp.root, "state.bin")
        StateStore(file, XorCipher).update { sample }
        val before = file.readBytes()
        val flaky = object : SecretCipher {
            override fun encrypt(plain: ByteArray) = plain
            override fun decrypt(blob: ByteArray): ByteArray = throw java.security.KeyStoreException("transient")
        }
        val store = StateStore(file, flaky)
        assertThrows(java.security.KeyStoreException::class.java) { store.load() }
        assertThrows(java.security.KeyStoreException::class.java) { store.update { it.copy(offlineVaultCreated = false) } }
        assertTrue(before.contentEquals(file.readBytes()))
        assertEquals(listOf("state.bin"), tmp.root.list()!!.toList())
    }

    @Test fun repeatedCorruptionKeepsEarlierQuarantine() {
        val file = File(tmp.root, "state.bin")
        file.writeBytes(byteArrayOf(1, 2, 3)); StateStore(file, XorCipher).load()
        Thread.sleep(5)
        file.writeBytes(byteArrayOf(4, 5, 6)); StateStore(file, XorCipher).load()
        assertEquals(2, tmp.root.list()!!.count { it.startsWith("state.bin.corrupt") })
    }
}
