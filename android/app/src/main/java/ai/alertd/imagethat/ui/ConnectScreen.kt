package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.remote.ApiClient
import ai.alertd.imagethat.remote.RemoteError
import ai.alertd.imagethat.remote.probeCertificate
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.AppTextField
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.FootnoteText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.GroupCard
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.components.TopBar
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.ImeAction
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

/** Shown, not enforced: http is allowed (e.g. a home LAN), but the user should know what it means. */
internal const val PLAIN_HTTP_WARNING =
    "Not encrypted: with http:// your photos and video travel over the network unencrypted. " +
        "Use https:// unless this is a network you trust."

private fun isPlainHttp(input: String) = parseServerUrl(input)?.isHttps == false

internal fun fingerprintDisplay(sha256: String) = sha256.chunked(2).joinToString(":").uppercase()

@Composable
fun ConnectScreen(session: Session, onBack: () -> Unit, onConnected: (serverKey: String) -> Unit) {
    val scope = rememberCoroutineScope()
    var url by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var untrusted by remember { mutableStateOf<Pair<HttpUrl, String>?>(null) }

    fun connect(target: HttpUrl, pin: String?) {
        busy = true
        error = null
        scope.launch(Dispatchers.Main) {
            val api = ApiClient(target, pin)
            try {
                api.vaults() // proves the server is reachable and speaks the API
                val key = finishConnect(session, target, pin, api)
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

    fun submit() {
        val target = parseServerUrl(url)
        if (target == null) error = "Enter a valid server address." else connect(target, null)
    }

    Column(
        Modifier
            .fillMaxSize()
            .background(Palette.Background)
            .verticalScroll(rememberScrollState())
            .statusBarsPadding()
            .navigationBarsPadding()
            .imePadding()
            .padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 32.dp),
    ) {
        TopBar(onBack, backTag = "connect-back")
        Column(Modifier.padding(horizontal = 8.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            Gap(4.dp)
            ScreenTitle("Connect to a server", subtitle = "The address of your ImageThat server")
            AppTextField(
                url,
                { url = it; untrusted = null; error = null },
                label = "Server address",
                placeholder = "192.168.1.5:8347",
                tag = "connect-url",
                leadingIcon = AppIcons.Server,
                isError = error != null,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false, imeAction = ImeAction.Go),
                keyboardActions = KeyboardActions(onGo = { if (url.isNotBlank() && !busy) submit() }),
            )
            if (isPlainHttp(url)) ErrorText(PLAIN_HTTP_WARNING, Modifier.testTag("connect-http-warning"))
            error?.let { ErrorText(it, Modifier.testTag("connect-error")) }
            untrusted?.let { (target, fp) ->
                GroupCard {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                            IconTile(AppIcons.Shield, tint = Palette.Accent)
                            Text(
                                "This server's certificate is not signed by a trusted authority.",
                                Modifier.weight(1f), color = Palette.Text, style = MaterialTheme.typography.titleSmall,
                            )
                        }
                        SectionLabel("SHA-256 fingerprint")
                        Text(fingerprintDisplay(fp), color = Palette.Text, style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace))
                        Text(
                            "Only trust it if this matches the fingerprint your server shows.",
                            color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall,
                        )
                        OutlinePill("Trust this certificate", { connect(target, fp) }, Modifier.fillMaxWidth(), enabled = !busy, height = 48.dp, contentColor = Palette.Accent)
                    }
                }
            }
            PillButton(
                "Connect", ::submit, Modifier.fillMaxWidth().testTag("connect-submit"),
                enabled = url.isNotBlank(), loading = busy,
            )
            FootnoteText("Servers you connect to are remembered, with their certificate. Vault passwords are asked for once and saved encrypted.")
        }
    }
}

/** Every server that connects is remembered (URL, label, pinned cert) in the encrypted state file. */
private fun finishConnect(session: Session, target: HttpUrl, pin: String?, api: ApiClient): String {
    val label = if (target.port == HttpUrl.defaultPort(target.scheme)) target.host else "${target.host}:${target.port}"
    var key = UUID.randomUUID().toString()
    val urlText = target.toString()
    session.keys.store().update { st ->
        val existing = st.servers.firstOrNull { it.url == urlText }
        if (existing != null) key = existing.id
        val saved = SavedServer(key, existing?.label ?: label, urlText, pin)
        st.copy(servers = st.servers.filterNot { it.id == key } + saved)
    }
    session.registerServer(RemoteServer(key, label, target.toString(), api))
    return key
}
