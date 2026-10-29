package ai.alertd.imagethat.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.runtime.remember
import androidx.fragment.app.FragmentActivity
import android.net.Uri
import androidx.navigation.NavHostController
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import ai.alertd.imagethat.keys.PasswordIds

internal object Routes {
    const val HOME = "home"
    const val CONNECT = "connect"
    const val SET_OFFLINE = "setOffline"
    const val SERVERS = "servers/{serverKey}"
    const val BROWSER = "browser/{sourceId}"
    fun servers(key: String) = "servers/${Uri.encode(key)}"
    fun browser(id: String) = "browser/${Uri.encode(id)}"
}

@Composable
fun ImageThatTheme(content: @Composable () -> Unit) =
    MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme(), content = content)

@Composable
fun AppNav(session: Session, activity: FragmentActivity? = null, nav: NavHostController = rememberNavController()) {
    val start = remember { if (session.local.offlineExists()) Routes.HOME else Routes.SET_OFFLINE }
    val appLocked by session.appLocked.collectAsState()
    val notice by session.notice.collectAsState()

    LaunchedEffect(session) {
        session.events.collect { e ->
            val route = nav.currentDestination?.route
            when (e) {
                SessionEvent.AllLocked ->
                    if (route == Routes.BROWSER || route == Routes.SERVERS) {
                        if (!nav.popBackStack(Routes.HOME, inclusive = false)) nav.navigate(Routes.HOME)
                    }
                is SessionEvent.SourceLocked ->
                    if (route == Routes.BROWSER && nav.currentBackStackEntry?.arguments?.getString("sourceId") == e.id) {
                        // Back to the vault list (or Home), where the vault shows as locked and can be unlocked again.
                        nav.popBackStack()
                    }
            }
        }
    }

    // Nothing of the app (Home included) is composed while it is locked.
    if (appLocked) {
        AppLockScreen(session, activity)
        return
    }

    CompositionLocalProvider(LocalTouch provides { session.touch() }) {
    notice?.let {
        SecureAlertDialog(
            onDismissRequest = session::dismissNotice,
            title = "Saved passwords cleared",
            confirmButton = { TextButton(onClick = session::dismissNotice, modifier = Modifier.testTag("notice-ok")) { Text("OK") } },
        ) { Text(it) }
    }
    NavHost(nav, startDestination = start) {
        composable(Routes.SET_OFFLINE) {
            // SET_OFFLINE as the back stack root (first run) is replaced by Home. Reached from a browser
            // (Save offline), go back to it so its state and source stay current. Decided by the live back
            // stack: "Not now" on first run leaves SET_OFFLINE as the start even when it is visited again.
            val toHome: () -> Unit = {
                if (nav.previousBackStackEntry == null) nav.navigate(Routes.HOME) { popUpTo(Routes.SET_OFFLINE) { inclusive = true } }
                else nav.popBackStack()
            }
            SetOfflinePasswordScreen(
                session.local, onDone = toHome, onSkip = toHome,
                savePassword = { session.savePassword(PasswordIds.OFFLINE, it) },
            )
        }
        composable(Routes.HOME) {
            HomeScreen(
                session, activity,
                onConnect = { nav.navigate(Routes.CONNECT) },
                onOpenServer = { nav.navigate(Routes.servers(it)) },
                onSetOfflinePassword = { nav.navigate(Routes.SET_OFFLINE) },
                onOpenSource = { nav.navigate(Routes.browser(it)) },
            )
        }
        composable(Routes.CONNECT) {
            ConnectScreen(
                session,
                onBack = { nav.popBackStack() },
                onConnected = { key -> nav.navigate(Routes.servers(key)) { popUpTo(Routes.CONNECT) { inclusive = true } } },
            )
        }
        composable(Routes.SERVERS, arguments = listOf(navArgument("serverKey") { type = NavType.StringType })) { entry ->
            RemoteVaultsScreen(
                session, entry.arguments!!.getString("serverKey")!!,
                onBack = { nav.popBackStack() },
                onOpen = { nav.navigate(Routes.browser(it)) },
            )
        }
        composable(Routes.BROWSER, arguments = listOf(navArgument("sourceId") { type = NavType.StringType })) { entry ->
            BrowserScreen(
                session, entry.arguments!!.getString("sourceId")!!, activity,
                onBack = { nav.popBackStack() },
                onSetOfflinePassword = { nav.navigate(Routes.SET_OFFLINE) },
            )
        }
    }
    }
}
