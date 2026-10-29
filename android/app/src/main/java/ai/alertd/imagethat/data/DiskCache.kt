package ai.alertd.imagethat.data

import java.io.File
import java.security.MessageDigest
import java.security.SecureRandom
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

/**
 * Encrypted-at-rest cache of remote vault reads (listings, thumbnails, viewed images) that
 * survives app restarts. Only ciphertext ever touches disk:
 *
 *  - Each vault has its own directory (named by a hash of the source id) and its own key, derived
 *    from the vault password with PBKDF2 and a random salt kept next to the entries. The key lives
 *    in memory only while the vault is unlocked here; [VaultDiskCache.lock] wipes it, the ciphertext
 *    stays and becomes readable again at the next unlock. A changed password just makes the old
 *    entries fail to decrypt, and they are dropped as misses.
 *  - Entries are AES-256-GCM sealed with a random nonce, with their kind, vault path and variant
 *    bound in as associated data: an entry copied or renamed on disk does not decrypt.
 *  - The vault's folder tree is mirrored with HMAC-named segments, so a folder's whole subtree can be
 *    forgotten by deleting a directory. The tree shape is the same metadata the gocryptfs vault
 *    itself exposes on disk.
 *  - The whole store stays under [budgetBytes]: when a write takes it over, the least recently used
 *    entries (by file time) are deleted first. The OS may clear the cache directory at any time.
 */
class EncryptedDiskCache(
    private val root: File,
    private val budgetBytes: Long = DEFAULT_BUDGET,
    val maxEntryBytes: Long = DEFAULT_MAX_ENTRY,
    private val kdfIterations: Int = KDF_ITERATIONS,
) {
    private val vaults = HashMap<String, VaultDiskCache>()
    private var total = -1L

    /** The cache of one vault; the same instance for the same id. It starts locked. */
    @Synchronized
    fun forVault(sourceId: String): VaultDiskCache =
        vaults.getOrPut(sourceId) { VaultDiskCache(this, File(root, sha256Hex(sourceId).take(32)), kdfIterations) }

    /** Bytes of entries on disk right now (walks the tree the first time). */
    @Synchronized
    fun totalBytes(): Long {
        if (total < 0) total = entryFiles().sumOf { it.length() }
        return total
    }

    /** Called after a change of [delta] bytes on disk; a first walk already sees that change. */
    @Synchronized
    internal fun adjust(delta: Long) {
        total = if (total < 0) entryFiles().sumOf { it.length() } else maxOf(0L, total + delta)
        if (total > budgetBytes) evict()
    }

    /** Deletes the oldest entries until the store is back under 90% of its budget. */
    private fun evict() {
        val target = budgetBytes * 9 / 10
        for (f in entryFiles().sortedBy { it.lastModified() }) {
            if (total <= target) break
            val n = f.length()
            if (f.delete()) total -= n
        }
    }

    private fun entryFiles(): List<File> =
        if (!root.isDirectory) emptyList() else root.walkTopDown().filter { it.isFile && it.name != SALT_FILE }.toList()

    companion object {
        const val DEFAULT_BUDGET = 256L * 1024 * 1024
        const val DEFAULT_MAX_ENTRY = 16L * 1024 * 1024
        const val KDF_ITERATIONS = 100_000
        internal const val SALT_FILE = "salt"
        internal const val LISTING_FILE = "_l"

        private fun sha256Hex(s: String) = MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).toHex()
    }
}

/** What an entry holds; the suffix keeps the kinds of one file apart on disk and in the AAD. */
enum class CacheKind(internal val suffix: String) { LISTING("l"), THUMB("t"), FILE("f") }

/** One vault's part of an [EncryptedDiskCache]. Every method is blocking I/O; call it off the main thread. */
class VaultDiskCache internal constructor(
    private val store: EncryptedDiskCache,
    internal val dir: File,
    private val kdfIterations: Int,
) {
    @Volatile private var key: CacheKey? = null

    val isOpen: Boolean get() = key != null

    /**
     * Derives this vault's key from [password] (PBKDF2, [EncryptedDiskCache.KDF_ITERATIONS] rounds:
     * a few hundred milliseconds on a phone). [password] is left for the caller to zero. The salt is
     * created on first use. An empty password opens nothing.
     */
    fun unlock(password: CharArray) {
        if (password.isEmpty()) return
        val salt = synchronized(this) { readOrCreateSalt() }
        val k = CacheKey.derive(password, salt, kdfIterations)
        synchronized(this) {
            key?.wipe()
            key = k
        }
    }

    /** Forgets the key; the ciphertext stays for the next [unlock]. */
    fun lock() = synchronized(this) {
        key?.wipe()
        key = null
    }

    /** The entry's bytes, or null when locked, absent, or not decryptable (then it is deleted). */
    fun get(kind: CacheKind, path: String, variant: String = ""): ByteArray? {
        val k = key ?: return null
        val f = fileFor(k, kind, path, variant)
        val blob = try {
            if (!f.isFile) return null
            f.readBytes()
        } catch (_: Exception) {
            return null
        }
        val plain = try {
            Sealed.open(k, blob, aad(kind, path, variant))
        } catch (_: Exception) {
            dropFile(f)
            return null
        }
        f.setLastModified(System.currentTimeMillis()) // LRU order for eviction
        return plain
    }

    /** Stores [bytes]; false when locked, too large, or the write failed. */
    fun put(kind: CacheKind, path: String, variant: String, bytes: ByteArray): Boolean {
        val k = key ?: return false
        if (bytes.size > store.maxEntryBytes) return false
        val f = fileFor(k, kind, path, variant)
        val tmp = File(f.parentFile, f.name + ".tmp")
        return try {
            f.parentFile?.mkdirs()
            val blob = Sealed.seal(k, bytes, aad(kind, path, variant))
            tmp.writeBytes(blob)
            val before = f.length()
            if (!tmp.renameTo(f)) {
                tmp.delete()
                return false
            }
            store.adjust(blob.size.toLong() - before)
            true
        } catch (_: Exception) {
            tmp.delete()
            false
        }
    }

    /** Forgets the listing of [path]. */
    fun removeListing(path: String) {
        val k = key ?: return
        dropFile(File(dirFor(k, path), EncryptedDiskCache.LISTING_FILE))
    }

    /** Forgets [path] and everything below it: its listing, its subtree, its thumbnails and bytes. */
    fun removeSubtree(path: String) {
        val k = key ?: return
        val p = CachingSource.norm(path)
        if (p == "/") return wipe()
        dropTree(dirFor(k, p))
        val (parent, name) = parentOf(p)
        val prefix = k.name(name) + "."
        dirFor(k, parent).listFiles { f -> f.isFile && f.name.startsWith(prefix) }?.forEach { dropFile(it) }
    }

    /** Deletes every entry this vault has on disk; the salt stays, so the current key remains valid. */
    fun wipe() {
        synchronized(this) {
            dir.listFiles()?.filter { it.name != EncryptedDiskCache.SALT_FILE }?.forEach { dropTree(it) }
        }
    }

    private fun readOrCreateSalt(): ByteArray {
        val f = File(dir, EncryptedDiskCache.SALT_FILE)
        if (f.isFile) {
            val s = f.readBytes()
            if (s.size == SALT_BYTES) return s
        }
        val s = ByteArray(SALT_BYTES).also { SecureRandom().nextBytes(it) }
        dir.mkdirs()
        f.writeBytes(s)
        return s
    }

    /** The directory mirroring vault folder [path]: one HMAC-named segment per folder. */
    private fun dirFor(k: CacheKey, path: String): File {
        var d = dir
        for (seg in CachingSource.norm(path).split('/').filter { it.isNotEmpty() }) d = File(d, k.name(seg))
        return d
    }

    private fun fileFor(k: CacheKey, kind: CacheKind, path: String, variant: String): File = when (kind) {
        CacheKind.LISTING -> File(dirFor(k, path), EncryptedDiskCache.LISTING_FILE)
        else -> {
            val (parent, name) = parentOf(CachingSource.norm(path))
            File(dirFor(k, parent), "${k.name(name)}.${kind.suffix}${k.name(variant).take(16)}")
        }
    }

    private fun aad(kind: CacheKind, path: String, variant: String) =
        "${kind.suffix}\u0000${CachingSource.norm(path)}\u0000$variant".toByteArray()

    private fun dropFile(f: File) {
        val n = f.length()
        if (f.delete()) store.adjust(-n)
    }

    private fun dropTree(d: File) {
        if (!d.exists()) return
        val n = d.walkTopDown().filter { it.isFile }.sumOf { it.length() }
        if (d.deleteRecursively()) store.adjust(-n)
    }

    private companion object {
        const val SALT_BYTES = 16
    }
}

/**
 * A vault cache's key material: an AES key for entries and a MAC key for names, both derived from
 * one PBKDF2 output. [wipe] zeroes what this class holds; JCE copies made during a cipher call are
 * beyond its reach, as with every key the app handles.
 */
class CacheKey private constructor(private val enc: ByteArray, private val mac: ByteArray) {
    internal fun aesKey() = SecretKeySpec(enc, "AES")

    /** Opaque, stable name for a path segment or variant. */
    internal fun name(segment: String): String = hmac(mac, segment.toByteArray()).copyOf(NAME_BYTES).toHex()

    fun wipe() {
        enc.fill(0)
        mac.fill(0)
    }

    companion object {
        private const val NAME_BYTES = 16

        fun derive(password: CharArray, salt: ByteArray, iterations: Int): CacheKey {
            val spec = PBEKeySpec(password, salt, iterations, 256)
            val master = try {
                SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
            } finally {
                spec.clearPassword()
            }
            try {
                return CacheKey(hmac(master, "enc".toByteArray()), hmac(master, "name".toByteArray()))
            } finally {
                master.fill(0)
            }
        }
    }
}

/** `magic | nonce(12) | AES-256-GCM(plaintext) + tag`, with the caller's AAD. */
private object Sealed {
    private val MAGIC = "ITC1".toByteArray()
    private const val NONCE = 12
    private const val TAG_BITS = 128

    fun seal(k: CacheKey, plain: ByteArray, aad: ByteArray): ByteArray {
        val nonce = ByteArray(NONCE).also { SecureRandom().nextBytes(it) }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, k.aesKey(), GCMParameterSpec(TAG_BITS, nonce))
        c.updateAAD(aad)
        val ct = c.doFinal(plain)
        return MAGIC + nonce + ct
    }

    /** Throws (including [AEADBadTagException]) on anything that is not an intact entry under [k]. */
    fun open(k: CacheKey, blob: ByteArray, aad: ByteArray): ByteArray {
        require(blob.size >= MAGIC.size + NONCE + TAG_BITS / 8 && blob.copyOf(MAGIC.size).contentEquals(MAGIC)) { "not a cache entry" }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, k.aesKey(), GCMParameterSpec(TAG_BITS, blob, MAGIC.size, NONCE))
        c.updateAAD(aad)
        return c.doFinal(blob, MAGIC.size + NONCE, blob.size - MAGIC.size - NONCE)
    }
}

private fun hmac(key: ByteArray, data: ByteArray): ByteArray =
    Mac.getInstance("HmacSHA256").apply { init(SecretKeySpec(key, "HmacSHA256")) }.doFinal(data)

private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
