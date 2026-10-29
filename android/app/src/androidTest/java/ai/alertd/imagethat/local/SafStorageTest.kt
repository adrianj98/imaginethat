package ai.alertd.imagethat.local

import android.net.Uri
import android.provider.DocumentsContract
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import mobile.Mobile
import org.json.JSONArray
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class SafStorageTest {
    private val inst = InstrumentationRegistry.getInstrumentation()
    private val ctx = inst.targetContext
    private lateinit var storage: SafStorage
    private val tree: Uri = testTree()
    private val noMoveTree: Uri = testTree(TestDocsNoMoveProvider.AUTHORITY_NO_MOVE)

    @Before
    fun setUp() {
        Mobile.setTempDir(inst.targetContext.cacheDir.absolutePath)
        grantAndResetTree(tree)
        grantAndResetTree(noMoveTree)
        storage = SafStorage(ctx.contentResolver, tree)
    }

    @After
    fun tearDown() {
        grantAndResetTree(tree)
        releaseTree(tree)
        releaseTree(noMoveTree)
    }

    private fun names(path: String): List<String> {
        val a = JSONArray(storage.listDir(path))
        return (0 until a.length()).map { a.getJSONObject(it).getString("name") }.sorted()
    }

    private fun write(path: String, data: ByteArray) {
        storage.openWriter(path).apply { write(data); close() }
    }

    private fun assertThrowsPrefix(prefix: String, block: () -> Unit) {
        try {
            block()
            fail("expected exception starting with $prefix")
        } catch (e: Exception) {
            assertTrue("message '${e.message}' should start with $prefix", e.message!!.startsWith(prefix))
        }
    }

    @Test
    fun writeListReadSize() {
        storage.mkdir("sub")
        write("sub/a.bin", ByteArray(5000) { (it % 251).toByte() })
        storage.writeFile("top.txt", "hello".toByteArray())
        assertEquals(listOf("sub", "top.txt"), names(""))
        assertEquals(listOf("a.bin"), names("sub"))
        assertEquals(5000L, storage.fileSize("sub/a.bin"))
        assertArrayEquals(ByteArray(64) { ((it + 100) % 251).toByte() }, storage.readAt("sub/a.bin", 100, 64))
        // Short read at EOF is not an error; past EOF is empty.
        assertEquals(10, storage.readAt("sub/a.bin", 4990, 100).size)
        assertEquals(0, storage.readAt("sub/a.bin", 6000, 10).size)
        val entry = JSONArray(storage.listDir("")).let { a -> (0 until a.length()).map { a.getJSONObject(it) }.first { it.getString("name") == "sub" } }
        assertTrue(entry.getBoolean("isDir"))
        assertTrue(storage.exists("sub/a.bin"))
        assertFalse(storage.exists("sub/none"))
    }

    @Test
    fun overwriteWriteFileTruncates() {
        storage.writeFile("f", ByteArray(100) { 1 })
        storage.writeFile("f", byteArrayOf(9, 9))
        assertEquals(2L, storage.fileSize("f"))
        assertEquals(listOf("f"), names(""))
    }

    @Test
    fun missingAndExistingErrors() {
        assertThrowsPrefix("not_found") { storage.fileSize("nope") }
        assertThrowsPrefix("not_found") { storage.readAt("nope", 0, 1) }
        assertThrowsPrefix("not_found") { storage.listDir("nodir") }
        assertThrowsPrefix("not_found") { storage.openWriter("nodir/x") }
        assertThrowsPrefix("not_found") { storage.rename("nope", "x") }
        storage.mkdir("d")
        assertThrowsPrefix("exists") { storage.mkdir("d") }
        write("f", byteArrayOf(1))
        assertThrowsPrefix("exists") { storage.openWriter("f") }
        assertEquals(1L, storage.fileSize("f"))
        storage.writeFile("g", byteArrayOf(2))
        assertThrowsPrefix("exists") { storage.rename("g", "f") }
        assertEquals(listOf("d", "f", "g"), names(""))
    }

    @Test
    fun renameWithinAndAcrossDirectories() {
        storage.mkdir("d1"); storage.mkdir("d2")
        storage.writeFile("d1/a", byteArrayOf(1, 2, 3))
        storage.rename("d1/a", "d1/b")
        assertEquals(listOf("b"), names("d1"))
        storage.rename("d1/b", "d2/c")
        assertEquals(emptyList<String>(), names("d1"))
        assertEquals(listOf("c"), names("d2"))
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.readAt("d2/c", 0, 10))
    }

    @Test
    fun renameAcrossDirectoriesWithoutProviderMove() {
        val storage = SafStorage(ctx.contentResolver, noMoveTree)
        fun names(path: String): List<String> {
            val a = JSONArray(storage.listDir(path))
            return (0 until a.length()).map { a.getJSONObject(it).getString("name") }.sorted()
        }
        storage.mkdir("d1"); storage.mkdir("d2"); storage.mkdir("d1/sub")
        storage.writeFile("d1/a", byteArrayOf(1, 2, 3))
        storage.writeFile("d1/sub/x", byteArrayOf(7))
        storage.rename("d1/a", "d2/c")
        storage.rename("d1/sub", "d2/moved")
        assertEquals(emptyList<String>(), names("d1"))
        assertEquals(listOf("c", "moved"), names("d2"))
        assertArrayEquals(byteArrayOf(1, 2, 3), storage.readAt("d2/c", 0, 10))
        assertArrayEquals(byteArrayOf(7), storage.readAt("d2/moved/x", 0, 10))
        assertFalse(storage.exists("d1/sub"))
    }

    @Test
    fun removeAndRemoveAll() {
        storage.mkdir("d"); storage.writeFile("d/a", byteArrayOf(1))
        storage.writeFile("f", byteArrayOf(1))
        storage.remove("f")
        assertFalse(storage.exists("f"))
        assertThrowsPrefix("not_found") { storage.remove("f") }
        try { storage.remove("d"); fail("non-empty dir must not be removed") } catch (_: Exception) {}
        assertTrue(storage.exists("d/a"))
        storage.removeAll("d")
        assertFalse(storage.exists("d"))
        storage.removeAll("d") // idempotent
    }

    @Test
    fun failedWriteLeavesNothingBehind() {
        val w = storage.openWriter("partial")
        // An aborted writer must remove its half-created document.
        (w as SafStorage.Writer).abort()
        assertFalse(storage.exists("partial"))
        assertEquals(emptyList<String>(), names(""))
    }

    @Test
    fun cacheSeesExternalChangesOnlyAfterOwnWrites() {
        storage.writeFile("a", byteArrayOf(1))
        assertTrue(storage.exists("a"))
        storage.remove("a")
        assertFalse(storage.exists("a"))
        storage.writeFile("a", byteArrayOf(1, 2))
        assertEquals(2L, storage.fileSize("a"))
    }

    @Test
    fun opensAesSivFixtureAndMatchesLocalOpen() {
        val fixtureLocal = File(inst.targetContext.cacheDir, "fixture-${System.nanoTime()}").apply { mkdirs() }
        try {
            copyAssetToStorage("vault-aessiv", "")
            copyAsset("vault-aessiv", fixtureLocal)
            val viaSaf = Mobile.openStorage(storage, "test-password-123")
            val viaLocal = Mobile.openLocal(fixtureLocal.absolutePath, "test-password-123")
            try {
                val expected = mapOf(
                    "/onebyte.bin" to 1L, "/block-4096.bin" to 4096L, "/block-4097.bin" to 4097L,
                    "/medium.bin" to 20000L, "/subdir/nested.bin" to 3000L,
                )
                for ((p, size) in expected) {
                    assertEquals(p, size, viaSaf.size(p))
                    assertArrayEquals(p, viaLocal.readRange(p, 0, size), viaSaf.readRange(p, 0, size))
                }
                assertEquals(viaLocal.list("/"), viaSaf.list("/"))
                assertTrue(viaSaf.list("/").contains("photo.jpg"))
                assertNotNull(viaSaf.size("/photo.jpg"))
                // Writes through the SAF-backed vault are readable after reopen.
                val data = ByteArray(9000) { (it * 7).toByte() }
                viaSaf.create("/new.bin").apply { write(data); close() }
                assertArrayEquals(data, viaSaf.readRange("/new.bin", 0, 9000))
            } finally {
                viaSaf.close(); viaLocal.close()
            }
            val again = Mobile.openStorage(SafStorage(ctx.contentResolver, tree), "test-password-123")
            try { assertEquals(9000L, again.size("/new.bin")) } finally { again.close() }
        } finally {
            fixtureLocal.deleteRecursively()
        }
    }

    @Test
    fun deniedWriteIsReadOnlyAndDoesNotBreakReads() {
        val roTree = testTree(TestDocsReadOnlyProvider.AUTHORITY_RO)
        val ctx = inst.targetContext
        ctx.grantUriPermission(ctx.packageName, roTree, RW or android.content.Intent.FLAG_GRANT_PREFIX_URI_PERMISSION)
        try {
            val ro = SafStorage(ctx.contentResolver, roTree)
            for (op in listOf<() -> Unit>(
                { ro.mkdir("d") }, { ro.writeFile("x", byteArrayOf(1)) }, { ro.openWriter("y") },
                { ro.remove("seed.bin") }, { ro.rename("seed.bin", "z") },
            )) {
                try {
                    op()
                    fail("expected write to be denied")
                } catch (e: Exception) {
                    assertTrue(e.message, LocalError.from(e) is LocalError.ReadOnly)
                }
            }
            // Reads keep working: the denial was not treated as revocation.
            assertEquals(listOf("seed.bin"), JSONArray(ro.listDir("")).let { a -> (0 until a.length()).map { a.getJSONObject(it).getString("name") } })
            assertEquals(10L, ro.fileSize("seed.bin"))
            assertArrayEquals(byteArrayOf(1, 2, 3), ro.readAt("seed.bin", 0, 3))
            assertTrue(ro.exists("seed.bin"))
        } finally {
            runCatching { ctx.revokeUriPermission(ctx.packageName, roTree, RW) }
        }
    }

    private fun copyAssetToStorage(assetDir: String, vaultPath: String) {
        val am = inst.context.assets
        for (name in am.list(assetDir)!!) {
            val child = "$assetDir/$name"
            val p = if (vaultPath.isEmpty()) name else "$vaultPath/$name"
            if (am.list(child)!!.isNotEmpty()) {
                storage.mkdir(p)
                copyAssetToStorage(child, p)
            } else {
                storage.writeFile(p, am.open(child).use { it.readBytes() })
            }
        }
    }

    private fun copyAsset(assetDir: String, dst: File) {
        val am = inst.context.assets
        for (name in am.list(assetDir)!!) {
            val child = "$assetDir/$name"
            if (am.list(child)!!.isNotEmpty()) {
                copyAsset(child, File(dst, name).apply { mkdirs() })
            } else {
                File(dst, name).outputStream().use { o -> am.open(child).use { it.copyTo(o) } }
            }
        }
    }
}
