package ai.alertd.imagethat.keys

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
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
        assertTrue(File(tmp.root, "state.bin.corrupt").exists())
    }

    @Test fun updateLeavesNoTempFiles() {
        val file = File(tmp.root, "state.bin")
        val store = StateStore(file, XorCipher)
        store.update { sample }
        store.update { it.copy(offlineVaultCreated = false) }
        assertEquals(listOf("state.bin"), tmp.root.list()!!.toList())
        assertFalse(store.load().offlineVaultCreated)
    }
}
