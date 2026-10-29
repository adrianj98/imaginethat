package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.KeyVault
import androidx.biometric.BiometricPrompt
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.withStateAtLeast
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

internal const val SCREEN_LOCK_REQUIRED = "Set up a fingerprint, face or screen lock in Android settings"

/** BiometricPrompt errors that mean there is nothing to authenticate with. */
private val NOT_SET_UP = setOf(
    BiometricPrompt.ERROR_NO_BIOMETRICS,
    BiometricPrompt.ERROR_HW_NOT_PRESENT,
    BiometricPrompt.ERROR_HW_UNAVAILABLE,
    BiometricPrompt.ERROR_NO_DEVICE_CREDENTIAL,
)

/**
 * Shown on cold start and after every lockAll, in place of everything else. It names no vault or
 * server. The prompt shows by itself once the activity is resumed; "Unlock" asks again.
 */
@Composable
fun AppLockScreen(session: Session, activity: FragmentActivity?) {
    val scope = rememberCoroutineScope()
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    var available by remember { mutableStateOf(session.appLockAvailable()) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    fun unlock() {
        if (busy) return
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            try {
                session.unlockApp(activity) // false: cancelled, stay here
            } catch (e: CancellationException) {
                throw e
            } catch (e: KeyVault.AuthError) {
                if (e.code in NOT_SET_UP) available = false else error = e.message
            } catch (e: Exception) {
                // Generating an auth-bound key fails when there is no secure lock screen.
                if (!session.appLockAvailable()) available = false else error = "Could not unlock: ${e.message}"
            } finally {
                busy = false
            }
        }
    }

    LaunchedEffect(Unit) {
        // A prompt cannot be shown over a stopped activity (lockAll often runs in the background).
        lifecycle.withStateAtLeast(Lifecycle.State.RESUMED) {}
        if (available) unlock()
    }

    Surface(Modifier.fillMaxSize().testTag("app-lock")) {
        Column(
            Modifier.fillMaxSize().padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp, Alignment.CenterVertically),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text("ImageThat", style = MaterialTheme.typography.headlineMedium)
            if (!available) {
                Text("A screen lock is required", style = MaterialTheme.typography.titleMedium)
                Text(SCREEN_LOCK_REQUIRED, textAlign = TextAlign.Center)
                Button(
                    onClick = {
                        available = session.appLockAvailable()
                        if (available) unlock()
                    },
                    modifier = Modifier.testTag("app-lock-retry"),
                ) { Text("Retry") }
            } else {
                Button(onClick = ::unlock, enabled = !busy, modifier = Modifier.testTag("app-unlock")) { Text("Unlock") }
                if (busy) CircularProgressIndicator(Modifier.size(24.dp))
                error?.let { Text(it, color = MaterialTheme.colorScheme.error, textAlign = TextAlign.Center) }
            }
        }
    }
}
