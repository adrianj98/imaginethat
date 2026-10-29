package ai.alertd.imagethat.keys

import java.util.Base64
import javax.crypto.AEADBadTagException
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class SavedPasswordsTest {
    private val dek = SavedPasswords.newDek()
    private val persisted = ArrayList<String>()
    private fun saved(initial: Map<String, CharArray> = emptyMap()) = SavedPasswords(dek, initial) { persisted += it }

    @Test fun sealOpenRoundTripKeepsUnicodeAndEmptyMaps() {
        val map = mapOf("offline" to "pässwörd ✓".toCharArray(), "server:s1:v1" to "x".toCharArray())
        val back = SavedPasswords.openMap(dek, SavedPasswords.sealMap(dek, map))
        assertEquals(map.keys, back.keys)
        for ((k, v) in map) assertArrayEquals(v, back[k])
        assertTrue(SavedPasswords.openMap(dek, SavedPasswords.sealMap(dek, emptyMap())).isEmpty())
    }

    @Test fun wrongKeyOrTamperedBlobFails() {
        val sealed = SavedPasswords.sealMap(dek, mapOf("a" to "secret".toCharArray()))
        assertThrows(AEADBadTagException::class.java) { SavedPasswords.openMap(SavedPasswords.newDek(), sealed) }
        val raw = Base64.getDecoder().decode(sealed)
        raw[raw.size - 1] = (raw[raw.size - 1].toInt() xor 1).toByte()
        assertThrows(AEADBadTagException::class.java) { SavedPasswords.openMap(dek, Base64.getEncoder().encodeToString(raw)) }
        assertThrows(AEADBadTagException::class.java) { SavedPasswords.openMap(dek, "not base64!") }
    }

    @Test fun sealedBlobHasNoPlaintextAndUsesAFreshIv() {
        val map = mapOf("folder:f1" to "hunter2-hunter2".toCharArray())
        val a = SavedPasswords.sealMap(dek, map)
        val b = SavedPasswords.sealMap(dek, map)
        assertFalse(a == b)
        val bytes = String(Base64.getDecoder().decode(a), Charsets.ISO_8859_1)
        assertFalse(bytes.contains("hunter2"))
        assertFalse(bytes.contains("folder:f1"))
    }

    @Test fun putPersistsTheWholeReEncryptedMap() {
        val s = saved()
        val pw = "first".toCharArray()
        s.put("offline", pw)
        pw.fill('\u0000') // the caller zeroes its copy; the saved one is unaffected
        s.put("server:s1:v1", "second".toCharArray())
        assertEquals(2, persisted.size)
        val back = SavedPasswords.openMap(dek, persisted.last())
        assertArrayEquals("first".toCharArray(), back["offline"])
        assertArrayEquals("second".toCharArray(), back["server:s1:v1"])
        assertArrayEquals("first".toCharArray(), s.get("offline"))
    }

    @Test fun puttingTheSamePasswordAgainDoesNotRewrite() {
        val s = saved()
        s.put("offline", "same".toCharArray())
        s.put("offline", "same".toCharArray())
        assertEquals(1, persisted.size)
    }

    @Test fun getReturnsACopy() {
        val s = saved(mapOf("offline" to "pw".toCharArray()))
        s.get("offline")!!.fill('\u0000')
        assertArrayEquals("pw".toCharArray(), s.get("offline"))
    }

    @Test fun removeAndRemoveIfPersistOnlyWhenSomethingChanged() {
        val s = saved(mapOf("server:s1:a" to "1".toCharArray(), "server:s1:b" to "2".toCharArray(), "offline" to "3".toCharArray()))
        s.remove("missing")
        assertTrue(persisted.isEmpty())
        s.removeIf { it.startsWith(PasswordIds.serverPrefix("s1")) }
        assertEquals(1, persisted.size)
        assertEquals(setOf("offline"), SavedPasswords.openMap(dek, persisted.last()).keys)
        s.remove("offline")
        assertTrue(SavedPasswords.openMap(dek, persisted.last()).isEmpty())
        assertNull(s.get("offline"))
    }

    @Test fun wipeZeroesPasswordsAndTheKey() {
        val s = saved(mapOf("offline" to "secret".toCharArray()))
        val held = s.heldForTest()
        s.wipe()
        for (h in held) when (h) {
            is CharArray -> assertTrue(h.all { it == '\u0000' })
            is ByteArray -> assertTrue(h.all { it == 0.toByte() })
        }
        assertNull(s.get("offline"))
        assertThrows(IllegalStateException::class.java) { s.put("offline", "x".toCharArray()) }
    }

    @Test fun constructorCopiesTheKeyAndInitialMap() {
        val key = SavedPasswords.newDek()
        val initial = mapOf("offline" to "pw".toCharArray())
        val s = SavedPasswords(key, initial) { persisted += it }
        val keyCopy = key.copyOf()
        key.fill(0)
        initial.values.forEach { it.fill('\u0000') }
        assertArrayEquals("pw".toCharArray(), s.get("offline"))
        s.put("x", "y".toCharArray())
        assertEquals(2, SavedPasswords.openMap(keyCopy, persisted.last()).size)
    }

    @Test fun ids() {
        assertEquals("folder:abc", PasswordIds.folder("abc"))
        assertEquals("server:s1:v1", PasswordIds.server("s1", "v1"))
        assertTrue(PasswordIds.server("s1", "v1").startsWith(PasswordIds.serverPrefix("s1")))
        assertFalse(PasswordIds.server("s10", "v1").startsWith(PasswordIds.serverPrefix("s1")))
    }
}
