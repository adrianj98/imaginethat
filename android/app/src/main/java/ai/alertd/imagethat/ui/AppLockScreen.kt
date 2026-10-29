package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.KeyVault
import androidx.biometric.BiometricPrompt
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.FootnoteText
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.material3.MaterialTheme
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

    Column(
        Modifier
            .fillMaxSize()
            .background(Palette.Background)
            .systemBarsPadding()
            .padding(horizontal = 24.dp, vertical = 24.dp)
            .testTag("app-lock"),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Column(
            Modifier.weight(1f).fillMaxWidth(),
            verticalArrangement = Arrangement.spacedBy(28.dp, Alignment.CenterVertically),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            IconTile(
                if (available) AppIcons.LockKeyhole else AppIcons.Fingerprint,
                tint = if (available) Palette.Accent else Palette.Destructive,
                background = Palette.Surface, border = Palette.Divider, size = 88.dp, radius = 28.dp, iconSize = 38.dp,
            )
            Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp)) {
                Text("ImageThat", color = Palette.Text, style = MaterialTheme.typography.headlineMedium)
                if (!available) {
                    Text("A screen lock is required", color = Palette.Text, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                    Text(SCREEN_LOCK_REQUIRED, color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center)
                } else {
                    Text("Locked", color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium)
                }
            }
            Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                if (!available) {
                    PillButton(
                        "Retry",
                        {
                            available = session.appLockAvailable()
                            if (available) unlock()
                        },
                        Modifier.fillMaxWidth().testTag("app-lock-retry"),
                    )
                } else {
                    PillButton("Unlock", ::unlock, Modifier.fillMaxWidth().testTag("app-unlock"), icon = AppIcons.Fingerprint, loading = busy)
                    error?.let { ErrorText(it, Modifier.fillMaxWidth()) }
                }
            }
        }
        FootnoteText(
            if (available) "Fingerprint, face or screen lock. Your vaults stay encrypted until you unlock."
            else "ImageThat asks for it each time it opens, before showing anything.",
            Modifier.fillMaxWidth(),
            textAlign = TextAlign.Center,
        )
    }
}
