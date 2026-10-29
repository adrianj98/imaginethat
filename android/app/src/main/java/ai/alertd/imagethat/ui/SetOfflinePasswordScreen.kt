package ai.alertd.imagethat.ui

import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.local.LocalVaults
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.FootnoteText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.PasswordInput
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

internal const val NO_RECOVERY_WARNING =
    "If you forget this password it cannot be recovered. Encryption is the point: " +
        "without the password nobody, including this app, can open the vault."

/** Two password fields (entered twice, at least [LocalVaults.MIN_OFFLINE_PASSWORD] characters). */
internal class NewPasswordState {
    var password by mutableStateOf("")
    var confirm by mutableStateOf("")
    val valid get() = password.length >= LocalVaults.MIN_OFFLINE_PASSWORD && password == confirm

    fun clear() {
        password = ""
        confirm = ""
    }
}

@Composable
internal fun NewPasswordFields(state: NewPasswordState) {
    PasswordInput(
        state.password, { state.password = it }, tag = "new-password",
        placeholder = "At least ${LocalVaults.MIN_OFFLINE_PASSWORD} characters", imeAction = ImeAction.Next,
    )
    PasswordInput(state.confirm, { state.confirm = it }, label = "Repeat password", tag = "new-password-confirm")
    if (state.password.isNotEmpty() && state.password.length < LocalVaults.MIN_OFFLINE_PASSWORD) {
        ErrorText("Too short")
    } else if (state.confirm.isNotEmpty() && state.password != state.confirm) {
        ErrorText("The passwords do not match")
    }
}

/** Sets the password of the built-in offline vault, creates it, and saves the password ([savePassword]). */
@Composable
fun SetOfflinePasswordScreen(
    local: LocalVaults,
    onDone: () -> Unit,
    onSkip: () -> Unit,
    savePassword: (CharArray) -> Unit = {},
) {
    val scope = rememberCoroutineScope()
    val state = remember { NewPasswordState() }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    fun create() {
        val pw = state.password.toCharArray()
        val toSave = pw.copyOf() // createOffline zeroes pw
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            try {
                withContext(Dispatchers.Default) { local.createOffline(pw) }
                withContext(Dispatchers.IO) { runCatching { savePassword(toSave) } }
                state.clear()
                onDone()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = LocalError.from(e).message ?: "Could not create the vault"
            } finally {
                pw.fill('\u0000')
                toSave.fill('\u0000')
                busy = false
            }
        }
    }

    Column(
        Modifier
            .fillMaxSize()
            .background(Palette.Background)
            .verticalScroll(rememberScrollState())
            .statusBarsPadding()
            .navigationBarsPadding()
            .imePadding()
            .padding(start = 24.dp, end = 24.dp, top = 32.dp, bottom = 32.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        IconTile(AppIcons.Phone, tint = Palette.Accent, background = Palette.Surface, border = Palette.Divider, size = 64.dp, radius = 20.dp, iconSize = 28.dp)
        ScreenTitle("Set offline password")
        Text(
            "The offline vault keeps files you save from a server on this phone, encrypted.",
            color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium,
        )
        Row(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Palette.Surface)
                .border(1.dp, Palette.Divider, RoundedCornerShape(16.dp)).padding(14.dp),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Icon(AppIcons.Info, null, Modifier.size(18.dp), tint = Palette.Destructive)
            Text(NO_RECOVERY_WARNING, Modifier.weight(1f), color = Palette.Text, style = MaterialTheme.typography.bodySmall)
        }
        NewPasswordFields(state)
        error?.let { ErrorText(it) }
        Gap(4.dp)
        PillButton("Create", ::create, Modifier.fillMaxWidth(), enabled = state.valid, loading = busy)
        OutlinePill("Not now", onSkip, Modifier.fillMaxWidth(), enabled = !busy, height = 52.dp, background = Palette.Background)
        FootnoteText("The password is saved encrypted on this phone, so you only type it once.")
    }
}
