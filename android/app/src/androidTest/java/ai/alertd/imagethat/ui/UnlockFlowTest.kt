package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.remote.sha256Hex
import androidx.activity.ComponentActivity
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.printToLog
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class UnlockFlowTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val fx = TestFixture()
    private var server: MockWebServer? = null

    @After fun tearDown() {
        server?.shutdown()
        rule.runOnUiThread { fx.cleanup() } // lockAll emits to the UI's collector, which must run on main
    }

    private fun launch() = rule.setContent { MaterialTheme { AppNav(fx.session) } }

    private fun waitFor(what: String, matcher: androidx.compose.ui.test.SemanticsMatcher) {
        try {
            rule.waitUntil(30_000) { rule.onAllNodes(matcher).fetchSemanticsNodes().isNotEmpty() }
        } catch (e: androidx.compose.ui.test.ComposeTimeoutException) {
            rule.onRoot().printToLog("UITREE")
            throw AssertionError("timed out waiting for $what", e)
        }
    }

    private fun waitForText(text: String, substring: Boolean = false) =
        waitFor("text '$text'", hasText(text, substring = substring))

    private fun waitForTag(tag: String) = waitFor("tag $tag", androidx.compose.ui.test.hasTestTag(tag))

    private fun createOffline() = fx.local.createOffline(TestFixture.PASSWORD.toCharArray())

    // ---- set offline password ------------------------------------------------

    @Test fun firstLaunchAsksForOfflinePasswordAndValidates() {
        launch()
        rule.onNodeWithText("Set offline password").assertIsDisplayed()
        rule.onNodeWithText("cannot be recovered", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Create").assertIsNotEnabled()

        rule.onNodeWithTag("new-password").performTextInput("abc")
        rule.onNodeWithTag("new-password-confirm").performTextInput("abc")
        rule.onNodeWithText("Create").assertIsNotEnabled() // too short

        rule.onNodeWithTag("new-password").performTextInput("defgh")
        rule.onNodeWithText("Create").assertIsNotEnabled() // mismatch (abcdefgh vs abc)

        rule.onNodeWithTag("new-password-confirm").performTextInput("defgh")
        rule.onNodeWithText("Create").assertIsEnabled()
    }

    @Test fun creatingTheOfflinePasswordCreatesTheVaultAndShowsHome() {
        launch()
        rule.onNodeWithTag("new-password").performTextInput("long-enough-1")
        rule.onNodeWithTag("new-password-confirm").performTextInput("long-enough-1")
        rule.onNodeWithText("Create").performClick()
        waitForText("On this phone")
        assertTrue(fx.local.offlineExists())
        assertTrue(fx.keys.state().offlineVaultCreated)
    }

    // ---- local unlock --------------------------------------------------------

    @Test fun wrongPasswordShowsInlineErrorAndKeepsTheDialog() {
        createOffline()
        launch()
        rule.onNodeWithText("Offline vault").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput("not-the-password")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("unlock-error")
        rule.onNodeWithText("Wrong password").assertIsDisplayed()
        rule.onNodeWithTag("unlock-password").assertIsDisplayed()

        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-stub")
        assertEquals(setOf("offline"), fx.session.openSourceIds.value)
    }

    @Test fun invalidatedShortcutExplainsAsksForPasswordAndOffersToReEnable() {
        createOffline()
        // A wrapped secret whose Keystore key does not exist is what an enrolment change leaves behind.
        fx.keys.store().update { it.copy(wrapped = mapOf("offline" to "imagethat.bio.offline:AAAAAAAAAAAAAAAAAAAAAAAA")) }
        launch()
        rule.onNodeWithText("Offline vault").performClick()
        waitForText("fingerprints changed", substring = true)
        rule.onNodeWithTag("unlock-password").assertIsDisplayed()
        rule.onNodeWithText("Unlock with biometrics next time").assertIsDisplayed()
        assertTrue(fx.keys.state().wrapped.isEmpty())

        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-stub")
        assertTrue(fx.keys.state().wrapped.containsKey("offline"))
    }

    @Test fun enabledShortcutUnlocksWithoutTypingThePassword() {
        createOffline()
        kotlinx.coroutines.runBlocking { fx.keys.wrap(null, "offline", TestFixture.PASSWORD.toByteArray()) }
        launch()
        rule.onNodeWithText("Offline vault").performClick()
        waitForTag("browser-stub")
        assertEquals(setOf("offline"), fx.session.openSourceIds.value)
    }

    // ---- remote --------------------------------------------------------------

    private val cert = HeldCertificate.Builder().commonName("localhost").addSubjectAlternativeName("localhost").build()

    private fun startServer(): MockWebServer {
        val hc = HandshakeCertificates.Builder().heldCertificate(cert).build()
        return MockWebServer().also {
            it.useHttps(hc.sslSocketFactory(), false)
            it.start()
            server = it
        }
    }

    private fun json(body: String, code: Int = 200) = MockResponse().setResponseCode(code)
        .setHeader("Content-Type", "application/json").setBody(body)

    private val vaultsLocked = """{"vaults":[{"id":"v1","name":"Holiday","unlocked":false}]}"""

    private fun connect(remember: Boolean) {
        val s = startServer()
        s.enqueue(json(vaultsLocked)) // pinned connectivity check
        s.enqueue(json(vaultsLocked)) // vault list
        launch()
        // First launch asks for the offline password; skip it to reach Home.
        rule.onNodeWithText("Not now").performClick()
        rule.onNodeWithText("Connect…").performClick()
        rule.onNodeWithTag("connect-url").performTextInput("https://localhost:${s.port}")
        rule.onNodeWithTag("connect-submit").performClick()
        waitForText("Trust this certificate")
        rule.onNodeWithText(fingerprintDisplay(sha256Hex(cert.certificate)), substring = true).assertIsDisplayed()
        if (!remember) rule.onNodeWithText("Remember this server").performClick()
        rule.onNodeWithText("Trust this certificate").performClick()
        waitForText("Holiday")
    }

    @Test fun rememberUntickedLeavesTheStateStoreUnchanged() {
        val before = fx.keys.state()
        connect(remember = false)
        assertEquals(before, fx.keys.state())
    }

    @Test fun rememberTickedSavesTheServerWithItsPin() {
        connect(remember = true)
        val saved = fx.keys.state().servers.single()
        assertEquals(sha256Hex(cert.certificate), saved.pinnedSha256)
    }

    private fun savedServerHome(): MockWebServer {
        val s = startServer()
        fx.keys.store().update {
            it.copy(
                offlineVaultCreated = true,
                servers = listOf(SavedServer("s1", "My server", "https://localhost:${s.port}/", sha256Hex(cert.certificate))),
            )
        }
        createOffline()
        return s
    }

    @Test fun remoteWrongPasswordIsInlineAndRateLimitSaysWait() {
        val s = savedServerHome()
        s.enqueue(json(vaultsLocked))
        s.enqueue(json("""{"error":"wrong password"}""", 401))
        s.enqueue(json("""{"error":"slow down"}""", 429))
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput("nope")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForText("Wrong password")
        rule.onNodeWithTag("unlock-password").assertIsDisplayed()

        rule.onNodeWithTag("unlock-password").performTextInput("nope")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForText("Too many attempts, wait", substring = true)
    }

    @Test fun remoteUnlockSuccessOpensTheBrowserRoute() {
        val s = savedServerHome()
        s.enqueue(json(vaultsLocked))
        s.enqueue(json("""{"vaults":[{"id":"v1","name":"Holiday","unlocked":true}]}"""))
        s.enqueue(json(vaultsLocked))
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput("right")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-stub")
        assertEquals(1, fx.session.openSourceIds.value.size)
    }

    @Test fun pinnedCertificateMismatchIsAHardFailureShowingTheNewFingerprint() {
        val s = startServer()
        fx.keys.store().update {
            it.copy(
                offlineVaultCreated = true,
                servers = listOf(SavedServer("s1", "My server", "https://localhost:${s.port}/", "00".repeat(32))),
            )
        }
        createOffline()
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("certificate changed", substring = true)
        rule.onNodeWithText(fingerprintDisplay(sha256Hex(cert.certificate)), substring = true).assertIsDisplayed()
    }
}
