package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.doubleClick
import androidx.compose.ui.test.pinch
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.InputStream

/** A remote-like vault held in memory. */
internal class MemVault : VaultSource {
    val files = linkedMapOf<String, ByteArray>()
    var lockedList = false
    override suspend fun list(path: String): List<Item> {
        if (lockedList) throw SourceError.Locked
        return files.map { (p, b) ->
            val name = p.substringAfterLast('/')
            val kind = when (name.substringAfterLast('.')) { "png" -> MediaKind.IMAGE; "mp4" -> MediaKind.VIDEO; else -> MediaKind.OTHER }
            Item(p, name, false, b.size.toLong(), kind)
        }
    }
    override suspend fun open(path: String, range: LongRange?): InputStream = ByteArrayInputStream(files[path] ?: throw SourceError.NotFound)
    override suspend fun size(path: String) = files[path]?.size?.toLong() ?: throw SourceError.NotFound
    override suspend fun thumbnail(path: String, px: Int) = ByteArray(0)
    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) { files["/$name"] = input.use { it.readBytes() } }
    override suspend fun mkdir(path: String) = Unit
    override suspend fun move(from: String, to: String) { files[to] = files.remove(from)!! }
    override suspend fun delete(path: String) { files.remove(path) }
}

class BrowserFlowTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val fx = TestFixture()
    private val mem = MemVault()
    private lateinit var offline: LocalSource

    private fun png(): ByteArray = ByteArrayOutputStream().also {
        Bitmap.createBitmap(64, 48, Bitmap.Config.ARGB_8888).apply { eraseColor(0xFF3366CC.toInt()) }.compress(Bitmap.CompressFormat.PNG, 100, it)
    }.toByteArray()

    @Before fun setUp() {
        fx.local.createOffline(TestFixture.PASSWORD.toCharArray())
        offline = LocalSource(MobileVaultApi(fx.local.openOffline(TestFixture.PASSWORD.toCharArray())))
        fx.session.registerSource(OFFLINE_ID, "Offline vault", offline)
        mem.files["/pic.png"] = png()
        mem.files["/clip.mp4"] = ByteArray(2048) { 7 }
        mem.files["/notes.txt"] = "hello".toByteArray()
        fx.session.registerSource("srv:v1", "Trips", mem)
    }

    @After fun tearDown() {
        rule.runOnUiThread { fx.cleanup() }
    }

    private fun show(id: String) = rule.setContent {
        MaterialTheme { BrowserScreen(fx.session, id, null, onBack = {}, onSetOfflinePassword = {}) }
    }

    private fun waitForTag(tag: String) = rule.waitUntil(20_000) { rule.onAllNodes(hasTestTag(tag)).fetchSemanticsNodes().isNotEmpty() }
    private fun gone(tag: String) = rule.waitUntil(20_000) { rule.onAllNodes(hasTestTag(tag)).fetchSemanticsNodes().isEmpty() }

    @Test fun imageOpensInViewerAndClosesBack() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer").assertIsDisplayed()
        rule.onNodeWithTag("viewer-close").performClick()
        waitForTag("browser-grid")
    }

    private fun statusBarShown(): Boolean {
        var shown = true
        rule.runOnUiThread {
            val insets = ViewCompat.getRootWindowInsets(rule.activity.window.decorView)
            shown = insets?.isVisible(WindowInsetsCompat.Type.statusBars()) ?: true
        }
        return shown
    }

    @Test fun fullscreenHidesTheBarsAndATapOnTheImageBringsThemBack() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer-fullscreen").performClick()
        gone("viewer-close")
        gone("viewer-info")
        rule.waitUntil(10_000) { !statusBarShown() }
        rule.onNodeWithTag("viewer-image").performClick()
        waitForTag("viewer-close")
        rule.waitUntil(10_000) { statusBarShown() }
    }

    @Test fun backLeavesFullscreenBeforeItClosesTheViewer() {
        show("srv:v1")
        waitForTag("item-clip.mp4")
        rule.onNodeWithTag("item-clip.mp4").performClick()
        waitForTag("viewer-video")
        rule.onNodeWithTag("viewer-fullscreen").performClick()
        gone("viewer-close")
        rule.runOnUiThread { rule.activity.onBackPressedDispatcher.onBackPressed() }
        waitForTag("viewer-close")
        rule.onNodeWithTag("viewer").assertIsDisplayed()
        rule.waitUntil(10_000) { statusBarShown() }
    }

    @Test fun doubleTapZoomsAnImageInAndBackOut() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        val zoomed = SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Zoomed 250%")
        val fit = SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Fit")
        rule.onNodeWithTag("viewer-image").assert(fit)
        rule.onNodeWithTag("viewer-image").performTouchInput { doubleClick() }
        rule.waitUntil(5_000) { rule.onAllNodes(hasTestTag("viewer-image") and zoomed).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("viewer-image").performTouchInput { doubleClick() }
        rule.waitUntil(5_000) { rule.onAllNodes(hasTestTag("viewer-image") and fit).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun pinchAndDoubleTapZoomAVideo() {
        show("srv:v1")
        waitForTag("item-clip.mp4")
        rule.onNodeWithTag("item-clip.mp4").performClick()
        waitForTag("viewer-video")
        val fit = SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Fit")
        fun isFit() = rule.onAllNodes(hasTestTag("viewer-video") and fit).fetchSemanticsNodes().isNotEmpty()
        assertTrue(isFit())
        rule.onNodeWithTag("viewer-video").performTouchInput {
            pinch(center - Offset(40f, 0f), center - Offset(200f, 0f), center + Offset(40f, 0f), center + Offset(200f, 0f))
        }
        rule.waitUntil(5_000) { !isFit() }
        // Zoomed: a double-tap goes back to fit, and another zooms in again.
        rule.onNodeWithTag("viewer-video").performTouchInput { doubleClick(center - Offset(0f, 200f)) }
        rule.waitUntil(5_000) { isFit() }
        rule.onNodeWithTag("viewer-video").performTouchInput { doubleClick(center - Offset(0f, 200f)) }
        rule.waitUntil(5_000) { !isFit() }
    }

    @Test fun unsupportedTypeShowsNameAndSizeOnly() {
        show("srv:v1")
        waitForTag("item-notes.txt")
        rule.onNodeWithTag("item-notes.txt").performClick()
        waitForTag("viewer-unsupported")
        rule.onAllNodes(hasText("notes.txt")).fetchSemanticsNodes().isNotEmpty().let { assertTrue(it) }
        rule.onAllNodes(hasText("5 B")).fetchSemanticsNodes().isNotEmpty().let { assertTrue(it) }
    }

    @Test fun undecodableVideoShowsAnErrorInsteadOfCrashing() {
        show("srv:v1")
        waitForTag("item-clip.mp4")
        rule.onNodeWithTag("item-clip.mp4").performClick()
        waitForTag("viewer-video")
        waitForTag("viewer-error")
        rule.onNodeWithTag("viewer-close").performClick()
        waitForTag("browser-grid")
    }

    @Test fun saveOfflineShowsPerFileResultAndWritesToTheOfflineVault() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performTouchInput { longClick() }
        waitForTag("action-save-offline")
        rule.onNodeWithTag("action-save-offline").performClick()
        waitForTag("result-saved")
        rule.onNodeWithTag("result-ok").performClick()
        val saved = runBlocking { offline.list("/Server/Trips") }
        assertEquals(listOf("pic.png"), saved.map { it.name })
    }

    @Test fun saveOfflineIsNotOfferedInsideTheOfflineVault() {
        runBlocking { offline.upload("/", "x.png", ByteArrayInputStream(png()), png().size.toLong()) }
        show(OFFLINE_ID)
        waitForTag("item-x.png")
        rule.onNodeWithTag("item-x.png").performTouchInput { longClick() }
        waitForTag("action-delete")
        assertTrue(rule.onAllNodes(hasTestTag("action-save-offline")).fetchSemanticsNodes().isEmpty())
    }

    @Test fun deleteAsksThenRemovesTheFile() {
        show("srv:v1")
        waitForTag("item-notes.txt")
        rule.onNodeWithTag("item-notes.txt").performTouchInput { longClick() }
        rule.onNodeWithTag("action-delete").performClick()
        waitForTag("delete-confirm")
        assertTrue("/notes.txt" in mem.files)
        rule.onNodeWithTag("delete-confirm").performClick()
        gone("item-notes.txt")
        assertTrue("/notes.txt" !in mem.files)
    }

    @Test fun deleteFromTheViewerAsksThenShowsTheNextImage() {
        mem.files["/pic2.png"] = png()
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer-delete").performClick()
        waitForTag("delete-confirm")
        rule.onNodeWithTag("delete-confirm").performClick()
        rule.waitUntil(20_000) { "/pic.png" !in mem.files && rule.onAllNodes(hasText("pic2.png")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("viewer").assertIsDisplayed()
        rule.onNodeWithTag("viewer-close").performClick()
        waitForTag("browser-grid")
        // The viewer's one-file selection does not leak into the grid.
        assertTrue(rule.onAllNodes(hasTestTag("action-delete")).fetchSemanticsNodes().isEmpty())
    }

    @Test fun viewerInfoShowsTheFullImageSize() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performClick()
        waitForTag("viewer-image")
        rule.onNodeWithTag("viewer-info").performClick()
        waitForTag("viewer-info-dialog")
        rule.waitUntil(20_000) { rule.onAllNodes(hasText("64 × 48")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("viewer-info-done").performClick()
        gone("viewer-info-dialog")
    }

    @Test fun selectThenSelectAllPicksEveryItem() {
        show("srv:v1")
        waitForTag("item-pic.png")
        rule.onNodeWithTag("browser-select").performClick()
        waitForTag("selection-all")
        rule.onNodeWithTag("selection-all").performClick()
        rule.waitUntil(20_000) { rule.onAllNodes(hasText("3 selected")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("selection-cancel").performClick()
        waitForTag("browser-select")
    }

    @Test fun lockedListingReportsTheSourceLockedInsteadOfShowingAnEmptyFolder() {
        mem.lockedList = true
        val seen = java.util.concurrent.atomic.AtomicReference<SessionEvent?>()
        val collector = kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.Default).launch {
            fx.session.events.collect { seen.set(it) }
        }
        try {
            rule.setContent { MaterialTheme { BrowserScreen(fx.session, "srv:v1", null, onBack = {}, onSetOfflinePassword = {}) } }
            rule.waitUntil(20_000) { seen.get() != null }
            assertEquals(SessionEvent.SourceLocked("srv:v1"), seen.get())
            assertTrue(fx.session.source("srv:v1") == null)
            assertTrue(rule.onAllNodes(hasTestTag("browser-empty")).fetchSemanticsNodes().isEmpty())
        } finally {
            collector.cancel()
        }
    }

    @Test fun offlineSetupDetourReturnsToTheSameBrowser() {
        lateinit var nav: androidx.navigation.NavHostController
        rule.setContent {
            nav = androidx.navigation.compose.rememberNavController()
            MaterialTheme { AppNav(fx.session, null, nav) }
        }
        rule.waitUntil(20_000) { nav.currentDestination?.route == Routes.HOME }
        rule.runOnUiThread { nav.navigate(Routes.browser("srv:v1")) }
        waitForTag("item-pic.png")
        rule.onNodeWithTag("item-pic.png").performTouchInput { longClick() }
        rule.runOnUiThread { nav.navigate(Routes.SET_OFFLINE) }
        rule.waitUntil(20_000) { nav.currentDestination?.route == Routes.SET_OFFLINE }
        rule.onNode(hasText("Not now")).performClick()
        rule.waitUntil(20_000) { nav.currentDestination?.route == Routes.BROWSER }
        // Same browser with its selection; Back leads to Home, not to a second browser.
        waitForTag("action-save-offline")
        rule.runOnUiThread { nav.popBackStack() }
        rule.waitUntil(20_000) { nav.currentDestination?.route == Routes.HOME }
        rule.runOnUiThread { assertTrue(!nav.popBackStack()) }
    }
}
