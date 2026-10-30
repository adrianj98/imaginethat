package ai.alertd.imagethat.remote

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.local.LocalError
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.runBlocking
import mobile.Mobile
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith
import java.security.MessageDigest

/**
 * The Go engine on a device against a real imagethat-web server in client-decrypt mode: it logs
 * in with the access password, fetches ciphertext and decrypts on the phone.
 *
 * Opt-in, since it needs a server on the host (the emulator reaches the host's loopback as
 * 10.0.2.2). Serve the fixture vault and pass its details, e.g.:
 *
 *     imagethat-web -root <dir with core/vault/testdata/vault> -mode client-decrypt \
 *         -access-password-file <hash file>
 *     ./gradlew :app:connectedDebugAndroidTest \
 *         -Pandroid.testInstrumentationRunnerArguments.class=ai.alertd.imagethat.remote.ClientDecryptServerTest \
 *         -Pandroid.testInstrumentationRunnerArguments.server=http://10.0.2.2:8347 \
 *         -Pandroid.testInstrumentationRunnerArguments.accessPassword=... \
 *         -Pandroid.testInstrumentationRunnerArguments.vaultId=...
 */
@RunWith(AndroidJUnit4::class)
class ClientDecryptServerTest {
    private val args = InstrumentationRegistry.getArguments()

    private fun sha256(b: ByteArray) = MessageDigest.getInstance("SHA-256").digest(b).joinToString("") { "%02x".format(it) }

    private inline fun expectLocalError(block: () -> Unit): LocalError {
        try {
            block()
        } catch (e: Exception) {
            return LocalError.from(e)
        }
        fail("expected an error")
        throw AssertionError()
    }

    @Test fun opensAndReadsAVaultDecryptedOnTheDevice() {
        val server = args.getString("server")
        assumeTrue("no server argument: skipped", server != null)
        val access = args.getString("accessPassword")!!
        val vaultId = args.getString("vaultId")!!
        // The fixture vault (core/vault/testdata/vault) and one of its files.
        val password = args.getString("vaultPassword") ?: "test-password-123"
        val file = args.getString("file") ?: "photo.jpg"
        val sha = args.getString("sha256") ?: "c52295b8e7d984490fce94c63e984e3c8c6f7818ee44a10aa288032ea13ca240"

        assertEquals("client-decrypt", Mobile.serverMode(server, ""))
        assertTrue(expectLocalError { Mobile.openRemote(server, vaultId, password, "", "wrong") } is LocalError.WrongAccessPassword)
        assertTrue(expectLocalError { Mobile.openRemote(server, vaultId, "wrong", "", access) } is LocalError.WrongPassword)

        val vault = Mobile.openRemote(server, vaultId, password, "", access)
        try {
            val source = LocalSource(MobileVaultApi(vault))
            runBlocking {
                val items = source.list("/")
                assertTrue("listing has $file: ${items.map { it.name }}", items.any { it.name == file })
                val bytes = source.open("/$file").use { it.readBytes() }
                assertEquals(sha, sha256(bytes))
                val tail = source.open("/$file", 100L..199L).use { it.readBytes() }
                assertEquals(bytes.copyOfRange(100, 200).toList(), tail.toList())
                assertTrue("thumbnail", source.thumbnail("/$file", 64).isNotEmpty())
                // Read-only: a client-decrypt vault cannot be written from the phone yet.
                try {
                    source.mkdir("/new-folder")
                    fail("mkdir succeeded on a client-decrypt vault")
                } catch (_: Exception) {
                }
            }
        } finally {
            vault.close()
        }
    }

    /** The app's path: [ClientDecryptServer] logs in, LocalVaults opens over the same Go client with a file cache. */
    @Test fun appPathLogsInOpensAndKeepsAFileOnThePhone() {
        val server = args.getString("server")
        assumeTrue("no server argument: skipped", server != null)
        val access = args.getString("accessPassword")!!
        val vaultId = args.getString("vaultId")!!
        val password = args.getString("vaultPassword") ?: "test-password-123"
        val file = args.getString("file") ?: "photo.jpg"
        val ctx = InstrumentationRegistry.getInstrumentation().targetContext
        val dir = java.io.File(ctx.cacheDir, "cd-test-${System.nanoTime()}")
        val local = ai.alertd.imagethat.local.LocalVaults(
            ctx, ai.alertd.imagethat.keys.StateStore(java.io.File(dir, "state.bin"), NoCipher),
            offlineDir = java.io.File(dir, "offline"), thumbDir = java.io.File(dir, "thumbs"),
            fileCacheDir = java.io.File(dir, "files"), driveCacheDir = java.io.File(dir, "drive"),
        )
        val client = ClientDecryptServer(server!!, null)
        try {
            runBlocking {
                try {
                    client.vaults()
                    fail("listed vaults before logging in")
                } catch (e: RemoteError.Locked) {
                }
                try {
                    client.login("wrong".toCharArray())
                    fail("wrong access password accepted")
                } catch (e: RemoteError.WrongAccessPassword) {
                }
                client.login(access.toCharArray())
                assertTrue(client.vaults().any { it.id == vaultId })
            }
            val vault = local.openRemote(client.server, vaultId, password.toCharArray(), "$server#$vaultId")
            val source = LocalSource(MobileVaultApi(vault), cachesFiles = true)
            runBlocking {
                assertTrue(!source.isCached("/$file"))
                source.cacheFile("/$file")
                assertTrue(source.isCached("/$file"))
                assertTrue(mobile.Mobile.fileCacheSize(local.fileCacheDir.absolutePath) > 0)
                source.uncache("/$file")
                assertTrue(!source.isCached("/$file"))
            }
            runBlocking { client.logout() }
        } finally {
            local.closeAll()
            dir.deleteRecursively()
        }
    }

    private object NoCipher : ai.alertd.imagethat.keys.SecretCipher {
        override fun encrypt(plain: ByteArray) = plain
        override fun decrypt(blob: ByteArray) = blob
    }
}
