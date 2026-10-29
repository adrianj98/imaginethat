package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.DialogButton
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.FootnoteText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.PasswordInput
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.components.TopBar
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.systemBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
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

/** Full-screen dialog window (the unlock screen): edge to edge, FLAG_SECURE like every other dialog. */
internal val SecureFullScreenDialogProperties = DialogProperties(
    usePlatformDefaultWidth = false,
    decorFitsSystemWindows = false,
    securePolicy = SecureFlagPolicy.SecureOn,
)

internal fun minutesLabel(m: Int) = if (m == 1) "1 minute" else "$m minutes"

/** The unlock screen's footer, with the real idle timeout. */
internal fun unlockFootnote(idleMinutes: Int) =
    "Decrypted only in memory. Locks again after ${minutesLabel(idleMinutes)} without a touch."

/**
 * Password screen shared by every vault, shown as a full-screen secure dialog: lock tile, vault
 * name, where it lives ([place]), password, Unlock. A password that unlocks is saved automatically
 * (by the caller), so this is only shown the first time, or after a saved password stopped working.
 * [opening]: the saved password is being tried, so there is nothing to type yet.
 */
@Composable
fun UnlockDialog(
    vaultName: String,
    place: String,
    idleMinutes: Int,
    notice: String? = null,
    error: String? = null,
    busy: Boolean = false,
    onSubmit: (password: CharArray) -> Unit,
    onDismiss: () -> Unit,
    extraButton: @Composable (() -> Unit)? = null,
    opening: Boolean = false,
) {
    // Plain (non-saveable) state: saved state can be written out in a Bundle.
    var password by remember { mutableStateOf("") }
    // A rejected password is cleared so the next attempt starts empty.
    LaunchedEffect(busy) { if (!busy && error != null) password = "" }
    val submit = { if (password.isNotEmpty() && !busy) onSubmit(password.toCharArray()) }
    Dialog(onDismissRequest = { if (!busy && !opening) onDismiss() }, properties = SecureFullScreenDialogProperties) {
        Column(
            Modifier
                .fillMaxSize()
                .background(Palette.Background)
                .touchReporting(LocalTouch.current)
                .systemBarsPadding()
                .imePadding()
                .padding(start = 16.dp, end = 24.dp, top = 8.dp, bottom = 24.dp)
                .testTag("unlock-screen"),
        ) {
            TopBar(
                onBack = if (busy || opening) null else onDismiss,
                backDescription = "Cancel",
                backTag = "unlock-cancel",
                center = { SectionLabel(place) },
            )
            Column(
                Modifier.weight(1f).fillMaxWidth().padding(start = 8.dp).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(28.dp, Alignment.CenterVertically),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Gap(8.dp)
                IconTile(
                    AppIcons.LockKeyhole, tint = Palette.Accent, background = Palette.Surface, border = Palette.Divider,
                    size = 88.dp, radius = 28.dp, iconSize = 38.dp,
                )
                Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        vaultName, color = Palette.Text, style = MaterialTheme.typography.headlineMedium,
                        textAlign = TextAlign.Center, maxLines = 2, overflow = TextOverflow.Ellipsis,
                    )
                    Text(
                        if (opening) "Opening with the saved password…" else "Enter the password once. It is saved for next time.",
                        color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium, textAlign = TextAlign.Center,
                    )
                }
                Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    if (notice != null) {
                        Text(notice, Modifier.testTag("unlock-notice"), color = Palette.Accent, style = MaterialTheme.typography.bodySmall)
                    }
                    if (opening) {
                        PillButton("Unlock", {}, Modifier.fillMaxWidth(), loading = true)
                    } else {
                        PasswordInput(
                            password, { password = it },
                            tag = "unlock-password", isError = error != null, enabled = !busy,
                            onImeAction = submit,
                        )
                        if (error != null) ErrorText(error, Modifier.testTag("unlock-error"))
                        PillButton(
                            "Unlock", submit, Modifier.fillMaxWidth().testTag("unlock-submit"),
                            enabled = password.isNotEmpty(), loading = busy,
                        )
                        extraButton?.invoke()
                    }
                }
            }
            FootnoteText(unlockFootnote(idleMinutes), Modifier.fillMaxWidth().padding(start = 8.dp, top = 16.dp), textAlign = TextAlign.Center)
        }
    }
}

/** Confirms forgetting one saved password; the vault itself is untouched. */
@Composable
internal fun ForgetPasswordDialog(label: String, onForget: () -> Unit, onDismiss: () -> Unit) = SecureAlertDialog(
    onDismissRequest = onDismiss,
    title = "Forget the saved password for $label?",
    confirmButton = { DialogButton("Forget", onForget, Modifier.testTag("forget-confirm"), destructive = true) },
    dismissButton = { DialogButton("Cancel", onDismiss, emphasized = false) },
) {
    Text("You will be asked for the password the next time you open it.", style = MaterialTheme.typography.bodyMedium)
}

/** Where local vaults live, shown above their name on the unlock screen. */
internal const val LOCAL_PLACE = "On this phone"

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
                    session.registerSource(target.sourceId, target.label, LocalSource(MobileVaultApi(vault))) { session.local.close(vault) }
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
        UnlockDialog(
            vaultName = target.label, place = LOCAL_PLACE, idleMinutes = session.idleTimeoutMinutes,
            onSubmit = {}, onDismiss = {}, opening = true,
        )
        return
    }

    UnlockDialog(
        vaultName = target.label,
        place = LOCAL_PLACE,
        idleMinutes = session.idleTimeoutMinutes,
        notice = notice,
        error = error,
        busy = busy,
        onSubmit = { pw -> attempt(pw, fromSaved = false) },
        onDismiss = onDismiss,
        extraButton = if (unavailable && onPickAgain != null) {
            { OutlinePill("Pick again", onPickAgain, Modifier.fillMaxWidth().testTag("unlock-pick-again"), height = 52.dp) }
        } else null,
    )
}
