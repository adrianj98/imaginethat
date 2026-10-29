package ai.alertd.imagethat

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.keys.AppState
import ai.alertd.imagethat.ui.AppNav
import ai.alertd.imagethat.ui.OFFLINE_ID
import ai.alertd.imagethat.ui.Routes
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.navigation.NavHostController
import androidx.navigation.compose.rememberNavController
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.File
import java.security.SecureRandom

/**
 * WARNING: this test works on the debug app's REAL data (noBackupFilesDir/offline-vault and the
 * encrypted state file), because it must prove what the real app writes. It wipes both. To avoid
 * destroying a developer's data it refuses to run when an offline vault or saved state exists that
 * this test did not create (it marks its own vault with [SENTINEL] in noBackupFilesDir).
 *
 * The app's central guarantee: decrypted vault content never reaches disk. Drives the real app
 * (real KeyVault, real offline vault in noBackupFilesDir, the app's Coil loader) through a remote
 * view, Save offline, an offline view and an upload, then searches every file under the app's
 * data directory for the plaintext markers and the server address.
 */
class NoPlaintextOnDiskTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val app = InstrumentationRegistry.getInstrumentation().targetContext
    private val container = (app.applicationContext as ImageThatApp).container
    private val session = container.session
    private val offlineDir = File(app.noBackupFilesDir, "offline-vault")
    private val marker1 = ByteArray(64).also { SecureRandom().nextBytes(it) }
    /** Trailing bytes of the JPEG served by /thumb: the only marker the Coil path ever sees. */
    private val marker1b = ByteArray(64).also { SecureRandom().nextBytes(it) }
    private val sentinel = File(app.noBackupFilesDir, SENTINEL)
    private val marker2 = ByteArray(64).also { SecureRandom().nextBytes(it) }
    private val password = "correct-horse-battery"
    private var server: MockWebServer? = null
    private lateinit var nav: NavHostController

    private val cert = HeldCertificate.Builder().commonName("localhost").addSubjectAlternativeName("localhost").build()

    /** A real PNG; [marker] rides after IEND, which every decoder ignores, so it stays plain bytes in the file. */
    private fun png(marker: ByteArray): ByteArray = ByteArrayOutputStream().also {
        Bitmap.createBitmap(64, 48, Bitmap.Config.ARGB_8888).apply { eraseColor(0xFF3366CC.toInt()) }.compress(Bitmap.CompressFormat.PNG, 100, it)
        it.write(marker)
    }.toByteArray()

    /** A real JPEG; [marker] follows EOI, which decoders ignore. */
    private fun jpeg(marker: ByteArray): ByteArray = ByteArrayOutputStream().also {
        Bitmap.createBitmap(32, 24, Bitmap.Config.ARGB_8888).apply { eraseColor(0xFFCC6633.toInt()) }.compress(Bitmap.CompressFormat.JPEG, 80, it)
        it.write(marker)
    }.toByteArray()

    @Before fun setUp() {
        rule.runOnUiThread { session.lockAll() }
        val foreign = (offlineDir.exists() || container.keys.state() != AppState()) && !sentinel.exists()
        check(!foreign) {
            "Refusing to run: the app has an offline vault or saved state that this test did not create " +
                "(no $SENTINEL). This test wipes them; clear the app's data (adb shell pm clear ${app.packageName}) first."
        }
        wipe()
        sentinel.writeText("created by NoPlaintextOnDiskTest; safe to wipe")
    }

    @After fun tearDown() {
        server?.shutdown()
        rule.runOnUiThread { session.lockAll() }
        wipe()
    }

    private fun wipe() {
        offlineDir.deleteRecursively()
        container.keys.store().update { AppState() }
        sentinel.delete()
    }

    private fun startServer(image: ByteArray, thumb: ByteArray): MockWebServer {
        val hc = HandshakeCertificates.Builder().heldCertificate(cert).build()
        var unlocked = false
        fun json(body: String) = MockResponse().setHeader("Content-Type", "application/json").setBody(body)
        return MockWebServer().also {
            it.useHttps(hc.sslSocketFactory(), false)
            it.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    val url = request.requestUrl!!
                    val path = url.encodedPath
                    return when {
                        path == "/api/vaults" ->
                            json("""{"vaults":[{"id":"v1","name":"Holiday","unlocked":$unlocked}]}""")
                        path == "/api/vaults/v1/unlock" -> { unlocked = true; json("{}") }
                        path == "/api/vaults/v1/list" ->
                            json("""{"path":"/","entries":[{"name":"pic.png","isDir":false,"size":${image.size},"mediaType":"image"}]}""")
                        path == "/api/vaults/v1/thumb" -> thumbResponse(thumb)
                        path == "/api/vaults/v1/file" -> fileResponse(request, image)
                        path == "/api/logout" -> json("{}")
                        else -> MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
                    }
                }
            }
            it.start()
            server = it
        }
    }

    private fun thumbResponse(thumb: ByteArray): MockResponse {
        thumbRequests.incrementAndGet()
        return MockResponse().setHeader("Content-Type", "image/jpeg").setBody(Buffer().write(thumb))
    }

    private fun fileResponse(request: RecordedRequest, image: ByteArray): MockResponse {
        val r = MockResponse().setHeader("Content-Type", "image/png").setHeader("Accept-Ranges", "bytes")
        val range = request.getHeader("Range")?.removePrefix("bytes=")?.split('-')
        if (range == null) return r.setBody(Buffer().write(image))
        val from = range[0].toInt()
        val to = minOf(range[1].toInt(), image.size - 1)
        return r.setResponseCode(206).setHeader("Content-Range", "bytes $from-$to/${image.size}")
            .setBody(Buffer().write(image, from, to - from + 1))
    }

    private fun waitFor(what: String, matcher: androidx.compose.ui.test.SemanticsMatcher) {
        try {
            rule.waitUntil(30_000) { rule.onAllNodes(matcher).fetchSemanticsNodes().isNotEmpty() }
        } catch (e: androidx.compose.ui.test.ComposeTimeoutException) {
            throw AssertionError("timed out waiting for $what", e)
        }
    }

    private fun waitForTag(tag: String) = waitFor("tag $tag", hasTestTag(tag))
    private fun waitForText(text: String) = waitFor("text '$text'", hasText(text))

    private fun List<String>.leaks(): String = joinToString("\n")

    @Test fun nothingDecryptedOrIdentifyingReachesDisk() {
        val s = startServer(png(marker1), jpeg(marker1b))
        val serverStrings = listOf("localhost:${s.port}", "127.0.0.1:${s.port}")

        rule.setContent {
            nav = rememberNavController()
            MaterialTheme { AppNav(session, null, nav) }
        }

        // Create the offline vault (first run), then unlock it so Save offline has a destination.
        rule.onNodeWithTag("new-password").performTextInput(password)
        rule.onNodeWithTag("new-password-confirm").performTextInput(password)
        rule.onNodeWithText("Create").performClick()
        waitForText("On this phone")
        rule.onNodeWithText("Offline vault").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput(password)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        rule.runOnUiThread { nav.popBackStack(Routes.HOME, false) }
        waitForText("Connect…")

        // Connect to the remote server without ticking "Remember this server".
        rule.onNodeWithText("Connect…").performClick()
        rule.onNodeWithTag("connect-url").performTextInput("https://localhost:${s.port}")
        rule.onNodeWithTag("connect-submit").performClick()
        waitForText("Trust this certificate")
        rule.onNodeWithText("Trust this certificate").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput("remote-password")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")

        // View it (full file through the viewer), then save it offline.
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer-close").performClick()
        waitForTag("browser-grid")
        // Thumbnails go through Coil; load one for real, bypassing the memory cache so it is fetched.
        val remoteId = session.openSourceIds.value.single { it != OFFLINE_ID }
        loadThumbnail(remoteId, "/pic.png")
        rule.onNodeWithTag("item-pic.png").performTouchInput { longClick() }
        waitForTag("action-save-offline")
        rule.onNodeWithTag("action-save-offline").performClick()
        waitForTag("result-saved")
        rule.onNodeWithTag("result-ok").performClick()

        // View the offline copy through the grid thumbnails and the viewer.
        rule.runOnUiThread { nav.navigate(Routes.browser(OFFLINE_ID)) }
        val offline = session.source(OFFLINE_ID) as LocalSource
        val folders = runBlocking { offline.list("/") }
        assertEquals("the offline vault should hold exactly the saved server folder", 1, folders.size)
        waitForTag("item-${folders[0].name}")
        var dir = "/" + folders[0].name
        rule.onNodeWithTag("item-${folders[0].name}").performClick()
        val vaults = runBlocking { offline.list(dir) }
        assertEquals(1, vaults.size)
        waitForTag("item-${vaults[0].name}")
        dir += "/" + vaults[0].name
        rule.onNodeWithTag("item-${vaults[0].name}").performClick()
        waitForTag("item-pic.png")
        loadThumbnail(OFFLINE_ID, "$dir/pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer-close").performClick()

        // Upload a second marker straight into the offline vault.
        val up = png(marker2)
        runBlocking { offline.upload("/", "uploaded.png", ByteArrayInputStream(up), up.size.toLong()) }

        // The offline vault really holds both files, decrypted in memory only.
        val savedBytes = runBlocking { offline.open("$dir/pic.png").use { it.readBytes() } }
        assertTrue("saved copy must contain marker1 when decrypted", savedBytes.indexOf(marker1) >= 0)
        val uploadedBytes = runBlocking { offline.open("/uploaded.png").use { it.readBytes() } }
        assertTrue("uploaded file must contain marker2 when decrypted", uploadedBytes.indexOf(marker2) >= 0)
        assertTrue("offline vault must have gocryptfs.conf", File(offlineDir, "gocryptfs.conf").exists())
        assertTrue("server must not be remembered", container.keys.state().servers.isEmpty())

        assertTrue("grid never requested the remote thumbnail", thumbRequests.get() > 0)

        // Let async writers (Coil, ExoPlayer, engine) finish before looking.
        rule.waitForIdle()
        Thread.sleep(2_000)
        val leaks = scan(listOf("marker1" to marker1, "marker1b(thumb)" to marker1b, "marker2" to marker2), serverStrings)
        assertTrue("plaintext found on disk:\n${leaks.leaks()}", leaks.isEmpty())
    }

    private val thumbRequests = java.util.concurrent.atomic.AtomicInteger()

    private fun loadThumbnail(sourceId: String, path: String) {
        val loader = coil3.SingletonImageLoader.get(app)
        val req = coil3.request.ImageRequest.Builder(app)
            .data(ai.alertd.imagethat.media.VaultUri.build(sourceId, path, 128))
            .memoryCachePolicy(coil3.request.CachePolicy.DISABLED)
            .build()
        val result = runBlocking { loader.execute(req) }
        assertTrue("thumbnail did not load: $result", result is coil3.request.SuccessResult)
    }

    private fun scan(markers: List<Pair<String, ByteArray>>, urls: List<String>): List<String> {
        val needles = markers +
            urls.flatMap { listOf("server-address '$it'" to it.toByteArray(), "server-url 'https://$it'" to "https://$it".toByteArray()) }
        val roots = listOfNotNull(
            app.filesDir, app.cacheDir, app.codeCacheDir, app.noBackupFilesDir, app.externalCacheDir, app.getExternalFilesDir(null), app.dataDir,
        ).map { it.canonicalFile }.distinct()
        val seen = HashSet<String>()
        val found = ArrayList<String>()
        val unreadable = ArrayList<String>()
        for (root in roots) {
            root.walkTopDown()
                .onEnter { !java.nio.file.Files.isSymbolicLink(it.toPath()) }
                .onFail { f, e -> unreadable += "${f.absolutePath} (${e.message})" }
                .filter { it.isFile && !java.nio.file.Files.isSymbolicLink(it.toPath()) && seen.add(it.canonicalPath) }
                .forEach { f ->
                    try {
                        for ((label, n) in needles) if (containsStreaming(f, n)) found += "${f.absolutePath} (${f.length()} bytes) contains $label"
                    } catch (e: Exception) {
                        unreadable += "${f.absolutePath} (${e.message})"
                    }
                }
        }
        assertTrue("scan visited no files", seen.isNotEmpty())
        assertTrue("could not read files under the app's data dirs:\n${unreadable.joinToString("\n")}", unreadable.isEmpty())
        return found
    }
}

private fun ByteArray.indexOf(needle: ByteArray): Int {
    if (needle.isEmpty() || size < needle.size) return -1
    outer@ for (i in 0..size - needle.size) {
        for (j in needle.indices) if (this[i + j] != needle[j]) continue@outer
        return i
    }
    return -1
}

/** Streams [f] in chunks with a (needle - 1) byte overlap so a match across a chunk boundary is found. */
private fun containsStreaming(f: File, needle: ByteArray): Boolean {
    val keep = needle.size - 1
    val buf = ByteArray(64 * 1024 + keep)
    var have = 0
    f.inputStream().use { input ->
        while (true) {
            val n = input.read(buf, have, buf.size - have)
            if (n < 0) return false
            have += n
            if (buf.copyOf(have).indexOf(needle) >= 0) return true
            val tail = minOf(keep, have)
            System.arraycopy(buf, have - tail, buf, 0, tail)
            have = tail
        }
    }
}

private const val SENTINEL = ".imagethat-test-owned"
