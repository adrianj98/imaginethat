package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.remote.ApiClient
import ai.alertd.imagethat.remote.RemoteError
import ai.alertd.imagethat.remote.probeCertificate
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import java.util.UUID

/** "192.168.1.5:8080" -> https://192.168.1.5:8080/ ; an explicit http:// or https:// is kept. */
internal fun parseServerUrl(input: String): HttpUrl? {
    val t = input.trim()
    if (t.isEmpty()) return null
    return (if ("://" in t) t else "https://$t").toHttpUrlOrNull()
}

internal const val PLAIN_HTTP_ERROR =
    "Plain http:// connections are not allowed. Use https:// (the server can use a self-signed certificate)."

internal fun fingerprintDisplay(sha256: String) = sha256.chunked(2).joinToString(":").uppercase()

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ConnectScreen(session: Session, onBack: () -> Unit, onConnected: (serverKey: String) -> Unit) {
    val scope = rememberCoroutineScope()
    var url by remember { mutableStateOf("") }
    var remember by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var untrusted by remember { mutableStateOf<Pair<HttpUrl, String>?>(null) }

    fun connect(target: HttpUrl, pin: String?) {
        if (!target.isHttps) {
            error = PLAIN_HTTP_ERROR
            return
        }
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            val api = ApiClient(target, pin)
            try {
                api.vaults() // proves the server is reachable and speaks the API
                val key = finishConnect(session, target, pin, api, remember)
                untrusted = null
                onConnected(key)
            } catch (e: CancellationException) {
                api.close()
                throw e
            } catch (e: Exception) {
                api.close()
                when (e) {
                    is RemoteError.CertMismatch ->
                        error = "The server's certificate changed. New SHA-256: ${fingerprintDisplay(e.sha256)}"
                    is RemoteError.Network -> {
                        // An https server the platform does not trust: show its certificate so the user can decide.
                        val fp = if (target.isHttps && pin == null) probeCertificate(target) else null
                        if (fp != null) untrusted = target to fp else error = "Could not reach the server."
                    }
                    is RemoteError -> error = e.message ?: "The server refused the connection."
                    else -> error = "Unexpected response from the server."
                }
            } finally {
                busy = false
            }
        }
    }

    Scaffold(topBar = {
        TopAppBar(title = { Text("Connect") }, navigationIcon = { TextButton(onClick = onBack) { Text("Back") } })
    }) { pad ->
        Column(
            Modifier.fillMaxSize().padding(pad).padding(16.dp).verticalScroll(rememberScrollState()),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            OutlinedTextField(
                value = url,
                onValueChange = { url = it; untrusted = null; error = null },
                label = { Text("Server address") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                modifier = Modifier.fillMaxWidth().testTag("connect-url"),
            )
            CheckRow("Remember this server", remember, { remember = it })
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("connect-error")) }
            untrusted?.let { (target, fp) ->
                Card(Modifier.fillMaxWidth()) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("This server's certificate is not signed by a trusted authority.")
                        Text("SHA-256 fingerprint", style = MaterialTheme.typography.labelMedium)
                        Text(fingerprintDisplay(fp), style = MaterialTheme.typography.bodySmall)
                        Text("Only trust it if this matches the fingerprint your server shows.")
                        Button(onClick = { connect(target, fp) }, enabled = !busy) { Text("Trust this certificate") }
                    }
                }
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    onClick = {
                        val target = parseServerUrl(url)
                        if (target == null) error = "Enter a valid server address." else connect(target, null)
                    },
                    enabled = url.isNotBlank() && !busy,
                    modifier = Modifier.testTag("connect-submit"),
                ) { Text("Connect") }
                if (busy) CircularProgressIndicator(Modifier.size(24.dp))
            }
        }
    }
}

private fun finishConnect(session: Session, target: HttpUrl, pin: String?, api: ApiClient, remember: Boolean): String {
    val label = if (target.port == HttpUrl.defaultPort(target.scheme)) target.host else "${target.host}:${target.port}"
    var key = UUID.randomUUID().toString()
    if (remember) {
        val urlText = target.toString()
        session.keys.store().update { st ->
            val existing = st.servers.firstOrNull { it.url == urlText }
            if (existing != null) key = existing.id
            val saved = SavedServer(key, existing?.label ?: label, urlText, pin)
            st.copy(servers = st.servers.filterNot { it.id == key } + saved)
        }
    }
    session.registerServer(RemoteServer(key, label, target.toString(), api))
    return key
}
