package ai.alertd.imagethat.ui

import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.local.LocalVaults
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
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
    PasswordField(state.password, { state.password = it }, "Password (at least ${LocalVaults.MIN_OFFLINE_PASSWORD} characters)", "new-password")
    PasswordField(state.confirm, { state.confirm = it }, "Repeat password", "new-password-confirm")
    if (state.password.isNotEmpty() && state.password.length < LocalVaults.MIN_OFFLINE_PASSWORD) {
        Text("Too short", color = MaterialTheme.colorScheme.error)
    } else if (state.confirm.isNotEmpty() && state.password != state.confirm) {
        Text("The passwords do not match", color = MaterialTheme.colorScheme.error)
    }
}

/** Sets the password of the built-in offline vault and creates it. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SetOfflinePasswordScreen(local: LocalVaults, onDone: () -> Unit, onSkip: () -> Unit) {
    val scope = rememberCoroutineScope()
    val state = remember { NewPasswordState() }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    Scaffold(topBar = { TopAppBar(title = { Text("Set offline password") }) }) { pad ->
        Column(
            Modifier.fillMaxSize().padding(pad).padding(16.dp).verticalScroll(rememberScrollState()),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text("The offline vault keeps files you save from a server on this phone, encrypted.")
            Text(NO_RECOVERY_WARNING, color = MaterialTheme.colorScheme.error)
            NewPasswordFields(state)
            error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    enabled = state.valid && !busy,
                    onClick = {
                        val pw = state.password.toCharArray()
                        busy = true
                        error = null
                        scope.launch(Dispatchers.Main) {
                            try {
                                withContext(Dispatchers.Default) { local.createOffline(pw) }
                                state.clear()
                                onDone()
                            } catch (e: CancellationException) {
                                throw e
                            } catch (e: Exception) {
                                error = LocalError.from(e).message ?: "Could not create the vault"
                            } finally {
                                pw.fill('\u0000')
                                busy = false
                            }
                        }
                    },
                ) { Text("Create") }
                TextButton(onClick = onSkip, enabled = !busy) { Text("Not now") }
                if (busy) CircularProgressIndicator(Modifier.size(24.dp))
            }
        }
    }
}
