package ai.alertd.imagethat.keys

import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.nio.ByteBuffer
import java.nio.CharBuffer
import java.nio.charset.CodingErrorAction
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/** Ids under which [SavedPasswords] keeps each password. */
object PasswordIds {
    const val OFFLINE = "offline"
    fun folder(folderId: String) = "folder:$folderId"
    fun server(serverKey: String, vaultId: String) = "server:$serverKey:$vaultId"
    fun serverPrefix(serverKey: String) = "server:$serverKey:"
    /** A client-decrypt server's access password; under [serverPrefix] so removing the server forgets it. */
    fun serverAccess(serverKey: String) = "server:$serverKey:#access"
}

/**
 * The saved vault passwords, decrypted in memory for one app-unlocked session.
 *
 * On disk (AppState.savedPasswords) the map is one AES-256-GCM blob under a random data key
 * ([dek]); the DEK itself is stored wrapped by the auth-per-use Keystore master key. The DEK
 * stays in memory until [wipe], so adding, changing or forgetting a password re-encrypts the map
 * without another biometric prompt. [persist] receives the new base64 blob.
 */
class SavedPasswords(
    dek: ByteArray,
    initial: Map<String, CharArray>,
    private val persist: (String) -> Unit,
) {
    private var dek: ByteArray? = dek.copyOf()
    private val map = HashMap<String, CharArray>()

    init {
        for ((k, v) in initial) map[k] = v.copyOf()
    }

    /** A copy of the saved password the caller must zero, or null. */
    @Synchronized fun get(id: String): CharArray? = map[id]?.copyOf()

    @Synchronized fun contains(id: String): Boolean = map.containsKey(id)

    @Synchronized fun ids(): Set<String> = map.keys.toSet()

    /** Saves a copy of [password] under [id] and re-encrypts the map. */
    @Synchronized fun put(id: String, password: CharArray) {
        val key = checkNotNull(dek) { "saved passwords are locked" }
        if (map[id]?.contentEquals(password) == true) return
        map.put(id, password.copyOf())?.fill('\u0000')
        persist(sealMap(key, map))
    }

    @Synchronized fun remove(id: String) = removeIf { it == id }

    /** Removes every entry whose id matches; re-encrypts once if anything changed. */
    @Synchronized fun removeIf(match: (String) -> Boolean) {
        val key = dek ?: return
        val gone = map.keys.filter(match)
        if (gone.isEmpty()) return
        for (id in gone) map.remove(id)?.fill('\u0000')
        persist(sealMap(key, map))
    }

    /** Zeroes every password and the data key; the object is useless afterwards. */
    @Synchronized fun wipe() {
        for (v in map.values) v.fill('\u0000')
        map.clear()
        dek?.fill(0)
        dek = null
    }

    @Synchronized internal fun heldForTest(): List<Any> = map.values.toList<Any>() + listOfNotNull(dek)

    companion object {
        const val DEK_LEN = 32
        private const val TRANSFORM = "AES/GCM/NoPadding"
        private const val IV_LEN = 12
        private val random = SecureRandom()

        fun newDek(): ByteArray = ByteArray(DEK_LEN).also { random.nextBytes(it) }

        /** base64(iv + AES-GCM(dek, encoded map)); the plaintext encoding is zeroed. */
        fun sealMap(dek: ByteArray, map: Map<String, CharArray>): String {
            val plain = encode(map)
            try {
                val c = Cipher.getInstance(TRANSFORM)
                c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(dek, "AES"))
                return Base64.getEncoder().encodeToString(c.iv + c.doFinal(plain))
            } finally {
                plain.fill(0)
            }
        }

        /** Inverse of [sealMap]. Throws [AEADBadTagException] for a wrong key or a tampered blob. */
        fun openMap(dek: ByteArray, sealed: String): MutableMap<String, CharArray> {
            val blob = try {
                Base64.getDecoder().decode(sealed)
            } catch (e: IllegalArgumentException) {
                throw AEADBadTagException("not base64")
            }
            if (blob.size <= IV_LEN) throw AEADBadTagException("blob too short")
            val c = Cipher.getInstance(TRANSFORM)
            c.init(Cipher.DECRYPT_MODE, SecretKeySpec(dek, "AES"), GCMParameterSpec(128, blob, 0, IV_LEN))
            val plain = c.doFinal(blob, IV_LEN, blob.size - IV_LEN)
            try {
                return decode(plain)
            } finally {
                plain.fill(0)
            }
        }

        /**
         * count, then per entry: id (UTF), password length in bytes, password UTF-8 bytes.
         * The password never becomes a String.
         */
        internal fun encode(map: Map<String, CharArray>): ByteArray {
            val bytes = ByteArrayOutputStream()
            DataOutputStream(bytes).use { out ->
                out.writeInt(map.size)
                for ((id, pw) in map) {
                    out.writeUTF(id)
                    val buf = Charsets.UTF_8.newEncoder().encode(CharBuffer.wrap(pw))
                    val raw = ByteArray(buf.remaining()).also { buf.get(it) }
                    if (buf.hasArray()) buf.array().fill(0)
                    out.writeInt(raw.size)
                    out.write(raw)
                    raw.fill(0)
                }
            }
            // ByteArrayOutputStream's internal buffer cannot be zeroed; it holds the same bytes we
            // zero after encryption and is unreachable once this returns.
            return bytes.toByteArray()
        }

        internal fun decode(plain: ByteArray): MutableMap<String, CharArray> {
            val out = HashMap<String, CharArray>()
            DataInputStream(plain.inputStream()).use { input ->
                repeat(input.readInt()) {
                    val id = input.readUTF()
                    val raw = ByteArray(input.readInt()).also { input.readFully(it) }
                    val chars = Charsets.UTF_8.newDecoder()
                        .onMalformedInput(CodingErrorAction.REPORT)
                        .decode(ByteBuffer.wrap(raw))
                    out[id] = CharArray(chars.remaining()).also { chars.get(it) }
                    if (chars.hasArray()) chars.array().fill('\u0000')
                    raw.fill(0)
                }
            }
            return out
        }
    }
}
