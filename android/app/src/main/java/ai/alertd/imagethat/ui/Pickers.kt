package ai.alertd.imagethat.ui

import android.content.ActivityNotFoundException
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContract
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember

/**
 * A launcher for a system picker the app waits on. The background auto-lock is paused while the
 * picker is open (leaving for it is not "backgrounded", and locking would drop its result); the
 * idle timeout still applies. See [Session.pauseBackgroundLock].
 */
@Composable
internal fun <I, O> rememberPickerLauncher(
    session: Session,
    contract: ActivityResultContract<I, O>,
    onResult: (O) -> Unit,
): (I) -> Unit {
    val launcher = rememberLauncherForActivityResult(contract) { result ->
        session.resumeBackgroundLock()
        onResult(result)
    }
    return remember(launcher, session) {
        { input ->
            session.pauseBackgroundLock()
            try {
                launcher.launch(input)
            } catch (e: ActivityNotFoundException) {
                session.resumeBackgroundLock()
                throw e
            }
        }
    }
}
