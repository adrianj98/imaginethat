package ai.alertd.imagethat.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.fragment.app.FragmentActivity
import android.net.Uri
import androidx.navigation.NavHostController
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument

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

    NavHost(nav, startDestination = start) {
        composable(Routes.SET_OFFLINE) {
            val toHome: () -> Unit = {
                nav.navigate(Routes.HOME) { popUpTo(Routes.SET_OFFLINE) { inclusive = true } }
            }
            SetOfflinePasswordScreen(session.local, onDone = toHome, onSkip = toHome)
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
            BrowserStub(entry.arguments!!.getString("sourceId")!!)
        }
    }
}

/** Placeholder for the vault browser (Task 10 replaces this composable and keeps the route). */
@Composable
private fun BrowserStub(sourceId: String) {
    Box(Modifier.fillMaxSize().testTag("browser-stub"), contentAlignment = Alignment.Center) {
        Text("Browser: $sourceId")
    }
}
