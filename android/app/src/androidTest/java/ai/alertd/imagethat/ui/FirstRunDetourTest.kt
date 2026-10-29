package ai.alertd.imagethat.ui

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
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

/** First run, "Not now" on the offline password, then Save offline from a browser: the detour must return to that browser. */
class FirstRunDetourTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val fx = TestFixture()

    @After fun tearDown() { rule.runOnUiThread { fx.cleanup() } }

    private fun route(r: String) = rule.waitUntil(30_000) { rule.runOnUiThread { } ; nav.currentDestination?.route == r }
    private lateinit var nav: NavHostController

    @Test fun saveOfflineAfterSkippingFirstRunSetupReturnsToTheSameBrowser() {
        val mem = MemVault().apply { files["/pic.png"] = ByteArray(10) }
        fx.session.registerSource("srv:v1", "Trips", mem)
        rule.setContent {
            nav = rememberNavController()
            MaterialTheme { AppNav(fx.session, null, nav) }
        }
        route(Routes.SET_OFFLINE)
        rule.onNodeWithText("Not now").performClick()
        route(Routes.HOME)
        rule.runOnUiThread { nav.navigate(Routes.browser("srv:v1")) }
        rule.waitUntil(20_000) { rule.onAllNodes(hasTestTag("item-pic.png")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("item-pic.png").performTouchInput { longClick() }
        rule.onNodeWithTag("action-save-offline").performClick()
        route(Routes.SET_OFFLINE)

        rule.onNodeWithTag("new-password").performTextInput("long-enough-1")
        rule.onNodeWithTag("new-password-confirm").performTextInput("long-enough-1")
        rule.onNodeWithText("Create").performClick()
        route(Routes.BROWSER)
        assertTrue(fx.local.offlineExists())
        // Same browser, selection kept.
        rule.waitUntil(20_000) { rule.onAllNodes(hasTestTag("action-save-offline")).fetchSemanticsNodes().isNotEmpty() }

        rule.runOnUiThread { nav.popBackStack() }
        route(Routes.HOME)
        rule.runOnUiThread { assertTrue(!nav.popBackStack()) }
    }
}
