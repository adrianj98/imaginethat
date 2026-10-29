package ai.alertd.imagethat.local

import android.content.Intent
import android.provider.DocumentsContract
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import ai.alertd.imagethat.keys.AppState
import ai.alertd.imagethat.keys.SavedFolder
import ai.alertd.imagethat.keys.SecretCipher
import ai.alertd.imagethat.keys.StateStore
import java.io.File
import mobile.Mobile
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class LocalVaultsTest {
    private val inst = InstrumentationRegistry.getInstrumentation()
    private lateinit var dir: File
    private lateinit var state: StateStore
    private lateinit var vaults: LocalVaults

    private object Plain : SecretCipher {
        override fun encrypt(plain: ByteArray) = plain
        override fun decrypt(blob: ByteArray) = blob
    }

    @Before
    fun setUp() {
        Mobile.setTempDir(inst.targetContext.cacheDir.absolutePath)
        dir = File(inst.targetContext.cacheDir, "lv-${System.nanoTime()}").apply { mkdirs() }
        state = StateStore(File(dir, "state.bin"), Plain)
        vaults = LocalVaults(inst.targetContext, state, offlineDir = File(dir, "offline-vault"))
    }

    @After
    fun tearDown() {
        vaults.closeAll()
        dir.deleteRecursively()
    }

    @Test
    fun offlineCreateOpenWriteReopen() {
        assertFalse(vaults.offlineExists())
        vaults.createOffline("correct horse".toCharArray())
        assertTrue(vaults.offlineExists())
        assertTrue(state.load().offlineVaultCreated)

        val data = ByteArray(7000) { (it % 200).toByte() }
        val v = vaults.openOffline("correct horse".toCharArray())
        v.create("/x.bin").apply { write(data); close() }
        vaults.closeAll()

        val again = vaults.openOffline("correct horse".toCharArray())
        assertArrayEquals(data, again.readRange("/x.bin", 0, data.size.toLong()))
    }

    @Test
    fun passwordArrayIsZeroedAfterUse() {
        val pw = "correct horse".toCharArray()
        vaults.createOffline(pw)
        assertTrue(pw.all { it == '\u0000' })
        val pw2 = "correct horse".toCharArray()
        vaults.openOffline(pw2)
        assertTrue(pw2.all { it == '\u0000' })
    }

    @Test
    fun wrongPasswordIsTyped() {
        vaults.createOffline("correct horse".toCharArray())
        try {
            vaults.openOffline("wrong password".toCharArray())
            fail()
        } catch (e: LocalError.WrongPassword) {
        }
    }

    @Test
    fun openOfflineWithoutVaultIsNoConfig() {
        try {
            vaults.openOffline("whatever1".toCharArray())
            fail()
        } catch (e: LocalError) {
            assertTrue(e is LocalError.NoConfig || e is LocalError.NotFound)
        }
    }

    @Test
    fun shortOfflinePasswordRejected() {
        try {
            vaults.createOffline("short".toCharArray())
            fail()
        } catch (e: IllegalArgumentException) {
        }
        assertFalse(vaults.offlineExists())
    }

    @Test
    fun createOfflineTwiceFails() {
        vaults.createOffline("correct horse".toCharArray())
        try {
            vaults.createOffline("another one!".toCharArray())
            fail()
        } catch (e: IllegalStateException) {
        }
    }

    @Test
    fun folderVaultCreateOpenAndPermissionRevoked() {
        val tree = testTree()
        val ctx = inst.targetContext
        grantAndResetTree(tree)
        val saf = LocalVaults(ctx, state, offlineDir = File(dir, "o2"))
        val folder = SavedFolder("f1", "Test folder", tree.toString())
        try {
            saf.createInFolder(tree, "folder password".toCharArray())
            val v = saf.openFolder(folder, "folder password".toCharArray())
            v.create("/a.bin").apply { write(byteArrayOf(1, 2, 3)); close() }
            saf.closeAll()
            val again = saf.openFolder(folder, "folder password".toCharArray())
            assertEquals(3L, again.size("/a.bin"))
            saf.closeAll()

            try {
                saf.openFolder(folder, "nope nope nope".toCharArray())
                fail()
            } catch (e: LocalError.WrongPassword) {
            }

            ctx.contentResolver.releasePersistableUriPermission(tree, Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
            try {
                saf.openFolder(folder, "folder password".toCharArray())
                fail("expected Unavailable")
            } catch (e: LocalError.Unavailable) {
            }
        } finally {
            saf.closeAll()
            grantAndResetTree(tree)
            releaseTree(tree)
        }
    }

    @Test
    fun revocationAfterUnlockSurfacesAsUnavailable() {
        val tree = testTree()
        val ctx = inst.targetContext
        grantAndResetTree(tree)
        val saf = LocalVaults(ctx, state, offlineDir = File(dir, "o3"))
        val folder = SavedFolder("f2", "Test folder", tree.toString())
        try {
            saf.createInFolder(tree, "folder password".toCharArray())
            val v = saf.openFolder(folder, "folder password".toCharArray())
            v.create("/a.bin").apply { write(byteArrayOf(1, 2, 3)); close() }
            assertEquals(3L, v.size("/a.bin"))

            // Revoke everything the app holds on the tree while the vault is open.
            ctx.contentResolver.releasePersistableUriPermission(tree, RW)
            ctx.revokeUriPermission(ctx.packageName, tree, RW)

            for (op in listOf<() -> Unit>({ v.list("/") }, { v.readRange("/a.bin", 0, 3) }, { v.size("/a.bin") })) {
                try {
                    op()
                    fail("expected failure after revocation")
                } catch (e: Exception) {
                    assertTrue("got ${LocalError.from(e)}: ${e.message}", LocalError.from(e) is LocalError.Unavailable)
                }
            }
        } finally {
            saf.closeAll()
            grantAndResetTree(tree)
            releaseTree(tree)
        }
    }

    @Test
    fun writeAfterRevocationIsUnavailableNotReadOnly() {
        val tree = testTree()
        val ctx = inst.targetContext
        grantAndResetTree(tree)
        val saf = LocalVaults(ctx, state, offlineDir = File(dir, "o4"))
        val folder = SavedFolder("f3", "Test folder", tree.toString())
        try {
            saf.createInFolder(tree, "folder password".toCharArray())
            val v = saf.openFolder(folder, "folder password".toCharArray())
            ctx.contentResolver.releasePersistableUriPermission(tree, RW)
            ctx.revokeUriPermission(ctx.packageName, tree, RW)

            for (op in listOf<() -> Unit>({ v.mkdir("/newdir") }, { v.create("/b.bin").close() }, { v.list("/") })) {
                try {
                    op()
                    fail("expected failure after revocation")
                } catch (e: Exception) {
                    assertTrue("got ${LocalError.from(e)}: ${e.message}", LocalError.from(e) is LocalError.Unavailable)
                }
            }
        } finally {
            saf.closeAll()
            grantAndResetTree(tree)
            releaseTree(tree)
        }
    }
}
