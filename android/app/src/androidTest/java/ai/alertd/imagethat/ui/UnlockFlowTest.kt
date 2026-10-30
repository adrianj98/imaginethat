package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.PasswordIds
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
import androidx.compose.ui.test.performTextClearance
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.printToLog
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.performTouchInput
import org.junit.Assert.assertFalse
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

    @Test fun autoLockTimeoutIsPickedOnHomeAndSaved() {
        createOffline()
        launch()
        rule.onNodeWithText("5 minutes without a touch").assertIsDisplayed()
        rule.onNodeWithTag("idle-timeout-setting").performClick()
        rule.onNodeWithTag("idle-timeout-10").performClick()
        waitForText("10 minutes without a touch")
        assertEquals(10, fx.keys.state().idleTimeoutMinutes)
        assertEquals(10, fx.session.idleTimeoutMinutes)
    }

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
        waitForTag("browser-screen")
        assertEquals(setOf("offline"), fx.session.openSourceIds.value)
    }

    // ---- app lock --------------------------------------------------------------

    private fun appLock() = rule.onNodeWithTag("app-lock")

    @Test fun appOpensLockedAndUnlocksToHome() {
        createOffline()
        fx.cancelPrompt = true
        launch()
        waitForTag("app-unlock")
        assertEquals(1, fx.prompts) // the prompt showed by itself
        rule.onAllNodesWithText("Offline vault").assertCountEquals(0)
        fx.cancelPrompt = false
        rule.onNodeWithTag("app-unlock").performClick()
        waitForText("On this phone")
        assertFalse(fx.session.appLocked.value)
    }

    @Test fun lockScreenNamesNoServerOrVault() {
        createOffline()
        fx.keys.store().update { it.copy(servers = listOf(SavedServer("s1", "My server", "https://nas.example:1/", null))) }
        fx.cancelPrompt = true
        launch()
        waitForTag("app-unlock")
        rule.onAllNodesWithText("My server", substring = true).assertCountEquals(0)
        rule.onAllNodesWithText("nas.example", substring = true).assertCountEquals(0)
        rule.onAllNodesWithText("Offline vault").assertCountEquals(0)
    }

    @Test fun noScreenLockExplainsAndRetries() {
        createOffline()
        fx.appLockAvailable = false
        launch()
        waitForText(SCREEN_LOCK_REQUIRED)
        assertEquals(0, fx.prompts)
        rule.onAllNodesWithText("On this phone").assertCountEquals(0)
        rule.onNodeWithTag("app-lock-retry").performClick()
        waitForText(SCREEN_LOCK_REQUIRED) // still none set up
        fx.appLockAvailable = true
        rule.onNodeWithTag("app-lock-retry").performClick()
        waitForText("On this phone")
    }

    @Test fun lockAllWipesSavedPasswordsAndReturnsToTheLockScreen() {
        createOffline()
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.OFFLINE, TestFixture.PASSWORD.toCharArray())
        launch()
        waitForText("On this phone")
        fx.cancelPrompt = true
        rule.onNodeWithText("Lock all").performClick()
        waitForTag("app-unlock")
        assertTrue(fx.session.appLocked.value)
        assertNull(fx.session.savedPassword(PasswordIds.OFFLINE))
        rule.onAllNodesWithText("On this phone").assertCountEquals(0)
        // Still saved on disk (encrypted): the next unlock brings it back.
        fx.cancelPrompt = false
        rule.onNodeWithTag("app-unlock").performClick()
        waitForText("On this phone")
        assertNotNull(fx.session.savedPassword(PasswordIds.OFFLINE))
    }

    @Test fun idleTimeoutWithNothingOpenStillLocksTheApp() {
        createOffline()
        launch()
        waitForText("On this phone")
        fx.cancelPrompt = true
        fx.now += 5 * 60_000
        rule.runOnUiThread { fx.session.tick() }
        waitForTag("app-unlock")
    }

    @Test fun changedFingerprintsClearSavedPasswordsAndSaySoOnce() {
        createOffline()
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.OFFLINE, TestFixture.PASSWORD.toCharArray())
        fx.session.lockAll()
        // An invalidated master key is unusable, exactly like a missing one.
        java.security.KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry(fx.masterAlias)
        launch()
        waitForText("fingerprints or screen lock changed", substring = true)
        rule.onNodeWithTag("notice-ok").performClick()
        waitForText("On this phone")
        assertFalse(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        rule.onNodeWithText("Offline vault").performClick()
        waitForTag("unlock-password") // asked once again
        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        assertTrue(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        // The next unlock (the lock screen prompts by itself) does not repeat the notice.
        rule.runOnUiThread { fx.session.lockAll() }
        waitForText("On this phone")
        assertNull(fx.session.notice.value)
        rule.onAllNodesWithTag("notice-ok").assertCountEquals(0)
    }

    // ---- saved passwords ----------------------------------------------------

    @Test fun firstLocalUnlockSavesThePasswordAndTheNextOpenAsksNothing() {
        createOffline()
        launch()
        rule.onNodeWithText("Offline vault").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        assertTrue(fx.session.hasSavedPassword(PasswordIds.OFFLINE))

        rule.runOnUiThread { fx.local.closeAll(); fx.session.reportLocked(OFFLINE_ID) }
        waitForText("On this phone")
        rule.onNodeWithText("Offline vault").performClick()
        waitForTag("browser-screen")
        rule.onAllNodesWithTag("unlock-password").assertCountEquals(0)
        assertEquals(setOf(OFFLINE_ID), fx.session.openSourceIds.value)
    }

    @Test fun staleSavedPasswordAsksOnceAndIsReplaced() {
        createOffline()
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.OFFLINE, "an-old-password".toCharArray())
        launch()
        rule.onNodeWithText("Offline vault").performClick()
        waitForText("saved password no longer opens", substring = true)
        assertFalse(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        assertArrayEquals(TestFixture.PASSWORD.toCharArray(), fx.session.savedPassword(PasswordIds.OFFLINE))
    }

    @Test fun settingTheOfflinePasswordSavesIt() {
        launch()
        rule.onNodeWithTag("new-password").performTextInput("long-enough-1")
        rule.onNodeWithTag("new-password-confirm").performTextInput("long-enough-1")
        rule.onNodeWithText("Create").performClick()
        waitForText("On this phone")
        assertArrayEquals("long-enough-1".toCharArray(), fx.session.savedPassword(PasswordIds.OFFLINE))
        rule.onNodeWithText("Offline vault").performClick()
        waitForTag("browser-screen")
    }

    @Test fun longPressForgetsTheSavedOfflinePassword() {
        createOffline()
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.OFFLINE, TestFixture.PASSWORD.toCharArray())
        launch()
        rule.onNodeWithTag("offline-row").performTouchInput { longClick() }
        waitForText("Forget the saved password for Offline vault?")
        rule.onNodeWithTag("forget-confirm").performClick()
        assertFalse(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        // Gone from disk too: a fresh unlock does not bring it back.
        rule.runOnUiThread { fx.session.lockAll() }
        fx.unlockApp()
        assertFalse(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        rule.onNodeWithText("Offline vault").performClick()
        waitForTag("unlock-password")
    }

    @Test fun plainHttpWarnsAndConnects() {
        val s = MockWebServer().also { it.dispatcher = ServerDecryptQueue(); it.start(); server = it }
        s.enqueue(json(vaultsLocked)) // connectivity check
        s.enqueue(json(vaultsLocked)) // vault list
        createOffline()
        launch()
        rule.onNodeWithText("Connect to a server").performClick()
        rule.onNodeWithTag("connect-url").performTextInput("https://localhost:${s.port}")
        rule.onAllNodesWithTag("connect-http-warning").assertCountEquals(0)
        rule.onNodeWithTag("connect-url").performTextClearance()
        rule.onNodeWithTag("connect-url").performTextInput("http://localhost:${s.port}")
        rule.onNodeWithTag("connect-http-warning").assertIsDisplayed()
        rule.onNodeWithTag("connect-submit").performClick()
        waitForText("Holiday")
        assertEquals("/api/info", s.takeRequest().path)
        assertEquals("/api/vaults", s.takeRequest().path)
    }

    @Test fun connectHasNoRememberCheckbox() {
        createOffline()
        launch()
        rule.onNodeWithText("Connect to a server").performClick()
        rule.onAllNodes(androidx.compose.ui.test.isToggleable()).assertCountEquals(0)
    }

    @Test fun cancelledOpenNeverLeavesAnUntrackedVault() {
        createOffline()
        val show = androidx.compose.runtime.mutableStateOf(true)
        rule.setContent {
            MaterialTheme {
                if (show.value) {
                    LocalUnlock(
                        fx.session,
                        LocalTarget("offline", "offline", "Offline vault") { pw -> Thread.sleep(1500); fx.local.openOffline(pw) },
                        null, onUnlocked = {}, onDismiss = {},
                    )
                }
            }
        }
        rule.onNodeWithTag("unlock-password").performTextInput(TestFixture.PASSWORD)
        rule.onNodeWithTag("unlock-submit").performClick()
        rule.runOnUiThread { show.value = false } // the screen goes away while the vault is opening
        rule.waitUntil(15_000) { fx.local.hasOpen() }
        rule.waitUntil(15_000) { fx.session.openSourceIds.value.isNotEmpty() }
        rule.runOnUiThread { fx.session.lockAll() }
        assertFalse(fx.local.hasOpen())
    }

    // ---- remote --------------------------------------------------------------

    private val cert = HeldCertificate.Builder().commonName("localhost").addSubjectAlternativeName("localhost").build()

    private fun startServer(): MockWebServer {
        val hc = HandshakeCertificates.Builder().heldCertificate(cert).build()
        return MockWebServer().also {
            it.dispatcher = ServerDecryptQueue()
            it.useHttps(hc.sslSocketFactory(), false)
            it.start()
            server = it
        }
    }

    private fun json(body: String, code: Int = 200) = MockResponse().setResponseCode(code)
        .setHeader("Content-Type", "application/json").setBody(body)

    /**
     * The enqueued responses, in order, with `/api/info` answered 404 like a server from before
     * client-decrypt mode, so the app's mode probe doesn't eat a queued response.
     */
    private class ServerDecryptQueue : okhttp3.mockwebserver.QueueDispatcher() {
        override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest): MockResponse =
            if (request.requestUrl!!.encodedPath == "/api/info") MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
            else super.dispatch(request)
    }

    private val vaultsLocked = """{"vaults":[{"id":"v1","name":"Holiday","unlocked":false}]}"""

    private fun connect() {
        val s = startServer()
        s.enqueue(json(vaultsLocked)) // pinned connectivity check
        s.enqueue(json(vaultsLocked)) // vault list
        launch()
        // First launch asks for the offline password; skip it to reach Home.
        rule.onNodeWithText("Not now").performClick()
        rule.onNodeWithText("Connect to a server").performClick()
        rule.onNodeWithTag("connect-url").performTextInput("https://localhost:${s.port}")
        rule.onNodeWithTag("connect-submit").performClick()
        waitForText("Trust this certificate")
        rule.onNodeWithText(fingerprintDisplay(sha256Hex(cert.certificate)), substring = true).assertIsDisplayed()
        rule.onNodeWithText("Trust this certificate").performClick()
        waitForText("Holiday")
    }

    @Test fun everyConnectedServerIsSavedWithItsPin() {
        connect()
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
        waitForTag("browser-screen")
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

    /** A server whose vault v1 opens with [password]; tracks unlock attempts. */
    private class FakeVaultServer(var password: String) : okhttp3.mockwebserver.Dispatcher() {
        @Volatile var unlocked = false
        val attempts = java.util.concurrent.atomic.AtomicInteger()
        private fun json(body: String, code: Int = 200) = MockResponse().setResponseCode(code)
            .setHeader("Content-Type", "application/json").setBody(body)
        override fun dispatch(request: okhttp3.mockwebserver.RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
            "/api/vaults" -> json("""{"vaults":[{"id":"v1","name":"Holiday","unlocked":$unlocked}]}""")
            "/api/vaults/v1/unlock" -> {
                attempts.incrementAndGet()
                if (request.body.readUtf8().contains("\"$password\"")) { unlocked = true; json("{}") }
                else json("""{"error":"wrong password"}""", 401)
            }
            "/api/vaults/v1/list" -> json("""{"path":"/","entries":[]}""")
            "/api/logout" -> json("{}")
            else -> MockResponse().setResponseCode(404).setBody("""{"error":"not found"}""")
        }
    }

    private fun savedServerWith(fake: FakeVaultServer) {
        val s = startServer()
        s.dispatcher = fake
        fx.keys.store().update {
            it.copy(servers = listOf(SavedServer("s1", "My server", "https://localhost:${s.port}/", sha256Hex(cert.certificate))))
        }
        createOffline()
    }

    /** The server forgot its session (restart): back to the vault list, which shows it locked. */
    private fun serverForgets(fake: FakeVaultServer) {
        fake.unlocked = false
        rule.runOnUiThread { fx.session.reportLocked(remoteSourceId("s1", "v1")) }
        waitForText("Locked")
    }

    @Test fun remoteVaultPasswordIsSavedAndUsedWithoutAPrompt() {
        val fake = FakeVaultServer("right")
        savedServerWith(fake)
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performClick()
        rule.onNodeWithTag("unlock-password").performTextInput("right")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        assertArrayEquals("right".toCharArray(), fx.session.savedPassword(PasswordIds.server("s1", "v1")))

        serverForgets(fake)
        rule.onNodeWithText("Holiday").performClick()
        waitForTag("browser-screen")
        rule.onAllNodesWithTag("unlock-password").assertCountEquals(0)
        assertEquals(2, fake.attempts.get())
    }

    @Test fun changedRemotePasswordAsksOnceAndReplacesTheSavedOne() {
        val fake = FakeVaultServer("new-pass")
        savedServerWith(fake)
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.server("s1", "v1"), "old-pass".toCharArray())
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performClick()
        waitForText("saved password no longer opens", substring = true)
        assertFalse(fx.session.hasSavedPassword(PasswordIds.server("s1", "v1")))
        rule.onNodeWithTag("unlock-password").performTextInput("new-pass")
        rule.onNodeWithTag("unlock-submit").performClick()
        waitForTag("browser-screen")
        assertArrayEquals("new-pass".toCharArray(), fx.session.savedPassword(PasswordIds.server("s1", "v1")))
    }

    @Test fun longPressForgetsASavedRemoteVaultPassword() {
        val fake = FakeVaultServer("right")
        savedServerWith(fake)
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.server("s1", "v1"), "right".toCharArray())
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithText("Holiday").performTouchInput { longClick() }
        waitForText("Forget the saved password for Holiday?")
        rule.onNodeWithTag("forget-confirm").performClick()
        assertFalse(fx.session.hasSavedPassword(PasswordIds.server("s1", "v1")))
        rule.onNodeWithText("Holiday").performClick()
        waitForTag("unlock-password")
        assertEquals(0, fake.attempts.get())
    }

    @Test fun removingAServerForgetsItsSavedPasswords() {
        val fake = FakeVaultServer("right")
        savedServerWith(fake)
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.server("s1", "v1"), "right".toCharArray())
        fx.session.savePassword(PasswordIds.OFFLINE, TestFixture.PASSWORD.toCharArray())
        launch()
        rule.onNodeWithText("My server").performTouchInput { longClick() }
        rule.onNodeWithTag("remove-confirm").performClick()
        waitForText("Connect to a server")
        assertTrue(fx.keys.state().servers.isEmpty())
        assertTrue(fx.session.hasSavedPassword(PasswordIds.OFFLINE))
        assertFalse(fx.session.hasSavedPassword(PasswordIds.server("s1", "v1")))
    }

    @Test fun removingAServerLocksItsOpenVaultsOnly() {
        val fake = FakeVaultServer("right")
        savedServerWith(fake)
        fx.unlockApp()
        var closedRemote = 0
        var closedFolder = 0
        fx.session.registerSource(remoteSourceId("s1", "v1"), "Holiday", MemVault()) { closedRemote++ }
        fx.session.registerSource("folder-f1", "Photos", MemVault()) { closedFolder++ }
        launch()
        rule.onNodeWithText("My server").performTouchInput { longClick() }
        rule.onNodeWithTag("remove-confirm").performClick()
        waitForText("Connect to a server")
        assertNull(fx.session.source(remoteSourceId("s1", "v1")))
        assertNull(fx.session.server("s1"))
        assertEquals(1, closedRemote)
        assertNotNull(fx.session.source("folder-f1"))
        assertEquals(0, closedFolder)
    }

    @Test fun searchFiltersTheServersVaults() {
        val s = savedServerHome()
        s.enqueue(json("""{"vaults":[{"id":"v1","name":"Holiday","unlocked":false},{"id":"v2","name":"Work","unlocked":false}]}"""))
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Work")
        rule.onNodeWithTag("vault-search").performTextInput("hol")
        waitForText("Holiday")
        rule.onAllNodesWithText("Work").assertCountEquals(0)
        rule.onNodeWithTag("vault-search").performTextClearance()
        waitForText("Work")
    }

    @Test fun serverScreenMenuRemovesTheServer() {
        val fake = FakeVaultServer("right")
        savedServerWith(fake)
        fx.unlockApp()
        fx.session.savePassword(PasswordIds.server("s1", "v1"), "right".toCharArray())
        launch()
        rule.onNodeWithText("My server").performClick()
        waitForText("Holiday")
        rule.onNodeWithTag("server-menu").performClick()
        waitForTag("server-remove")
        rule.onNodeWithTag("server-remove").performClick()
        rule.onNodeWithTag("remove-confirm").performClick()
        waitForText("Connect to a server")
        assertTrue(fx.keys.state().servers.isEmpty())
        assertFalse(fx.session.hasSavedPassword(PasswordIds.server("s1", "v1")))
    }
}
