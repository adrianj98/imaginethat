package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
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
import androidx.compose.ui.Modifier
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
    title = { Text(title, style = MaterialTheme.typography.headlineSmall) },
    text = { Column(verticalArrangement = Arrangement.spacedBy(12.dp)) { content() } },
    confirmButton = confirmButton,
    dismissButton = dismissButton,
    properties = SecureDialogProperties,
    shape = RoundedCornerShape(24.dp),
    containerColor = Palette.Surface,
    titleContentColor = Palette.Text,
    textContentColor = Palette.TextSecondary,
    tonalElevation = 0.dp,
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

/**
 * Password prompt shared by every vault. A password that unlocks is saved automatically (by the
 * caller), so this is only shown the first time, or after a saved password stopped working.
 */
@Composable
fun UnlockDialog(
    title: String,
    notice: String? = null,
    error: String? = null,
    busy: Boolean = false,
    onSubmit: (password: CharArray) -> Unit,
    onDismiss: () -> Unit,
    extraButton: @Composable (() -> Unit)? = null,
) {
    var password by remember { mutableStateOf("") }
    // A rejected password is cleared so the next attempt starts empty.
    LaunchedEffect(busy) { if (!busy && error != null) password = "" }
    SecureAlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = title,
        confirmButton = {
            TextButton(
                onClick = { onSubmit(password.toCharArray()) },
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
        if (notice != null) Text(notice, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.testTag("unlock-notice"))
        PasswordField(password, { password = it }, "Password", "unlock-password")
        if (error != null) {
            Text(error, color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("unlock-error"))
        }
        if (busy) CircularProgressIndicator(Modifier.size(24.dp))
    }
}

/** Confirms forgetting one saved password; the vault itself is untouched. */
@Composable
internal fun ForgetPasswordDialog(label: String, onForget: () -> Unit, onDismiss: () -> Unit) = SecureAlertDialog(
    onDismissRequest = onDismiss,
    title = "Forget the saved password for $label?",
    confirmButton = { TextButton(onClick = onForget, modifier = Modifier.testTag("forget-confirm")) { Text("Forget") } },
    dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
) {
    Text("You will be asked for the password the next time you open it.")
}

/** A vault the local unlock flow can open: the built-in one or a picked folder. */
class LocalTarget(
    /** Session id of the opened source. */
    val sourceId: String,
    /** Key of its saved password (see PasswordIds). */
    val passwordId: String,
    val label: String,
    val open: (CharArray) -> Vault,
)

/** The saved password is tried first (no prompt); the dialog only when there is none or it failed. */
private enum class Stage { Saved, Password }

/**
 * One unlock flow for every local vault: with a saved password it opens without asking;
 * otherwise the password dialog, and a password that opens the vault is saved.
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
    var stage by remember { mutableStateOf(if (session.hasSavedPassword(target.passwordId)) Stage.Saved else Stage.Password) }
    var notice by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var unavailable by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }

    /** [fromSaved]: the password came from the saved passwords, so a wrong-password failure means it is stale. */
    fun attempt(password: CharArray, fromSaved: Boolean) {
        // Opening zeroes the password it is given, so a typed one is kept aside to save on success.
        val toSave = if (fromSaved) null else password.copyOf()
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            try {
                // Open and register together: a cancel in between would leave a decrypted vault the Session
                // does not know about.
                withContext(NonCancellable) {
                    val vault = withContext(Dispatchers.Default) { target.open(password) }
                    session.registerSource(target.sourceId, target.label, LocalSource(MobileVaultApi(vault)))
                    // A failed save only means the password is asked for again next time.
                    if (toSave != null) withContext(Dispatchers.IO) { runCatching { session.savePassword(target.passwordId, toSave) } }
                }
                if (isActive) onUnlocked(target.sourceId)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                when (val le = LocalError.from(e)) {
                    is LocalError.WrongPassword -> if (fromSaved) {
                        session.forgetPassword(target.passwordId)
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
                password.fill('\u0000')
                toSave?.fill('\u0000')
                busy = false
            }
        }
    }

    if (stage == Stage.Saved) {
        LaunchedEffect(Unit) {
            val saved = session.savedPassword(target.passwordId)
            if (saved == null) stage = Stage.Password else attempt(saved, fromSaved = true)
        }
        SecureAlertDialog(
            onDismissRequest = {},
            title = "Unlock ${target.label}",
            confirmButton = {},
        ) {
            Text("Opening with the saved password…")
            CircularProgressIndicator(Modifier.size(24.dp))
        }
        return
    }

    UnlockDialog(
        title = "Unlock ${target.label}",
        notice = notice,
        error = error,
        busy = busy,
        onSubmit = { pw -> attempt(pw, fromSaved = false) },
        onDismiss = onDismiss,
        extraButton = if (unavailable && onPickAgain != null) {
            { TextButton(onClick = onPickAgain) { Text("Pick again") } }
        } else null,
    )
}
