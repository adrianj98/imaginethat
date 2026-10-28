package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.keys.Unwrap
import ai.alertd.imagethat.local.LocalError
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.SecureFlagPolicy
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Vault

/** Dialogs are separate windows the activity never sees touches for; they report activity through this. */
internal val LocalTouch = staticCompositionLocalOf<() -> Unit> { {} }

/** Every dialog is its own window, so FLAG_SECURE has to be set on each one. */
internal val SecureDialogProperties = DialogProperties(securePolicy = SecureFlagPolicy.SecureOn)

@Composable
internal fun SecureAlertDialog(
    onDismissRequest: () -> Unit,
    title: String,
    confirmButton: @Composable () -> Unit,
    dismissButton: @Composable (() -> Unit)? = null,
    content: @Composable () -> Unit,
) = AlertDialog(
    modifier = Modifier.touchReporting(LocalTouch.current),
    onDismissRequest = onDismissRequest,
    title = { Text(title) },
    text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) { content() } },
    confirmButton = confirmButton,
    dismissButton = dismissButton,
    properties = SecureDialogProperties,
)

private fun Modifier.touchReporting(touch: () -> Unit): Modifier = pointerInput(touch) {
    awaitPointerEventScope {
        while (true) {
            awaitPointerEvent(PointerEventPass.Initial)
            touch()
        }
    }
}

/** Password text lives in plain (non-saveable) state: saved state can be written out in a Bundle. */
@Composable
internal fun PasswordField(
    value: String,
    onValueChange: (String) -> Unit,
    label: String,
    tag: String,
    modifier: Modifier = Modifier,
) = OutlinedTextField(
    value = value,
    onValueChange = onValueChange,
    label = { Text(label) },
    singleLine = true,
    visualTransformation = PasswordVisualTransformation(),
    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false),
    modifier = modifier.fillMaxWidth().testTag(tag),
)

@Composable
internal fun CheckRow(label: String, checked: Boolean, onChange: (Boolean) -> Unit, modifier: Modifier = Modifier) {
    Row(
        modifier.fillMaxWidth().toggleable(value = checked, role = Role.Checkbox, onValueChange = onChange),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Checkbox(checked = checked, onCheckedChange = null)
        Text(label, Modifier.padding(start = 8.dp))
    }
}

/**
 * Password prompt shared by every vault. [offerBiometrics] shows the
 * "Unlock with biometrics next time" checkbox, initially [biometricsDefault].
 */
@Composable
fun UnlockDialog(
    title: String,
    notice: String? = null,
    error: String? = null,
    busy: Boolean = false,
    offerBiometrics: Boolean = false,
    onSubmit: (password: CharArray, enableBiometrics: Boolean) -> Unit,
    onDismiss: () -> Unit,
    extraButton: @Composable (() -> Unit)? = null,
) {
    var password by remember { mutableStateOf("") }
    var enable by remember { mutableStateOf(false) }
    // A rejected password is cleared so the next attempt starts empty.
    LaunchedEffect(busy) { if (!busy && error != null) password = "" }
    SecureAlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = title,
        confirmButton = {
            TextButton(
                onClick = { onSubmit(password.toCharArray(), offerBiometrics && enable) },
                enabled = password.isNotEmpty() && !busy,
                modifier = Modifier.testTag("unlock-submit"),
            ) { Text("Unlock") }
        },
        dismissButton = {
            Row {
                extraButton?.invoke()
                TextButton(onClick = onDismiss, enabled = !busy) { Text("Cancel") }
            }
        },
    ) {
        if (notice != null) Text(notice, style = MaterialTheme.typography.bodyMedium)
        PasswordField(password, { password = it }, "Password", "unlock-password")
        if (error != null) {
            Text(error, color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("unlock-error"))
        }
        if (offerBiometrics) CheckRow("Unlock with biometrics next time", enable, { enable = it })
        if (busy) CircularProgressIndicator(Modifier.size(24.dp))
    }
}

/** A vault the local unlock flow can open: the built-in one or a picked folder. */
class LocalTarget(
    /** Session id, also the key of the wrapped biometric secret. */
    val sourceId: String,
    val label: String,
    val open: (CharArray) -> Vault,
)

private enum class Stage { Biometric, Password }

/**
 * One unlock flow for every local vault: biometric prompt when a wrapped
 * secret exists, otherwise (or on "Use password") the password dialog.
 */
@Composable
fun LocalUnlock(
    session: Session,
    target: LocalTarget,
    activity: FragmentActivity?,
    onUnlocked: (sourceId: String) -> Unit,
    onDismiss: () -> Unit,
    /** Offered when the folder itself can no longer be read (SAF grant revoked). */
    onPickAgain: (() -> Unit)? = null,
) {
    val scope = rememberCoroutineScope()
    var stage by remember { mutableStateOf(if (session.keys.state().wrapped.containsKey(target.sourceId)) Stage.Biometric else Stage.Password) }
    var notice by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var unavailable by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }

    /** [fromShortcut]: the password came from the biometric-wrapped secret, so a failure means it is stale. */
    fun attempt(password: CharArray, enableBiometrics: Boolean, fromShortcut: Boolean) {
        val secret = if (enableBiometrics) String(password).toByteArray(Charsets.UTF_8) else null
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            try {
                // Open and register together: a cancel in between would leave a decrypted vault the Session
                // does not know about.
                withContext(NonCancellable) {
                    val vault = withContext(Dispatchers.Default) { target.open(password) }
                    session.registerSource(target.sourceId, target.label, LocalSource(MobileVaultApi(vault)))
                }
                if (secret != null) runCatching { session.keys.wrap(activity, target.sourceId, secret) }
                if (isActive) onUnlocked(target.sourceId)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                when (val le = LocalError.from(e)) {
                    is LocalError.WrongPassword -> if (fromShortcut) {
                        session.keys.forget(target.sourceId)
                        notice = "The saved password no longer opens this vault. Enter the password again."
                    } else {
                        error = "Wrong password"
                    }
                    is LocalError.Unavailable -> {
                        unavailable = true
                        error = "This folder is not accessible any more. Pick it again to restore access."
                    }
                    is LocalError.NoConfig -> error = "No vault was found in this folder."
                    else -> error = le.message ?: "Could not open the vault"
                }
                stage = Stage.Password
            } finally {
                secret?.fill(0)
                password.fill('\u0000')
                busy = false
            }
        }
    }

    if (stage == Stage.Biometric) {
        LaunchedEffect(Unit) {
            val r = try {
                session.keys.unwrap(activity, target.sourceId)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                notice = "Biometric unlock is not available right now. Enter the password."
                stage = Stage.Password
                return@LaunchedEffect
            }
            when (r) {
                is Unwrap.Ok -> {
                    val chars = String(r.secret, Charsets.UTF_8).toCharArray()
                    r.secret.fill(0)
                    attempt(chars, enableBiometrics = false, fromShortcut = true)
                }
                Unwrap.Invalidated -> {
                    notice = "Your fingerprints changed, so the biometric shortcut was turned off. " +
                        "Enter the vault password; you can tick the box below to turn the shortcut back on."
                    stage = Stage.Password
                }
                Unwrap.Cancelled, Unwrap.None -> stage = Stage.Password
            }
        }
        SecureAlertDialog(
            onDismissRequest = { if (!busy) onDismiss() },
            title = "Unlock ${target.label}",
            confirmButton = { TextButton(onClick = { stage = Stage.Password }, enabled = !busy) { Text("Use password") } },
            dismissButton = { TextButton(onClick = onDismiss, enabled = !busy) { Text("Cancel") } },
        ) {
            Text("Use your biometrics to unlock.")
            if (busy) CircularProgressIndicator(Modifier.size(24.dp))
        }
        return
    }

    UnlockDialog(
        title = "Unlock ${target.label}",
        notice = notice,
        error = error,
        busy = busy,
        offerBiometrics = session.biometricsAvailable(),
        onSubmit = { pw, enable -> attempt(pw, enable, fromShortcut = false) },
        onDismiss = onDismiss,
        extraButton = if (unavailable && onPickAgain != null) {
            { TextButton(onClick = onPickAgain) { Text("Pick again") } }
        } else null,
    )
}
