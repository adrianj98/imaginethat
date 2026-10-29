package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.CachingSource
import ai.alertd.imagethat.data.RemoteSource
import ai.alertd.imagethat.remote.RemoteError
import ai.alertd.imagethat.remote.RemoteVault
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
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
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

internal fun remoteSourceId(serverKey: String, vaultId: String) = "$serverKey:$vaultId"

private sealed interface Load {
    object Loading : Load
    class Loaded(val vaults: List<RemoteVault>) : Load
    /** Temporary: unreachable, retry may help. */
    class Unreachable(val message: String) : Load
    /** Hard failure: the pinned certificate changed. No retry, no bypass. */
    class CertChanged(val sha256: String) : Load
}

/** The server's vaults with their lock state; tap to unlock with the vault password. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RemoteVaultsScreen(session: Session, serverKey: String, onBack: () -> Unit, onOpen: (sourceId: String) -> Unit) {
    val server = session.server(serverKey)
    val scope = rememberCoroutineScope()
    var load by remember { mutableStateOf<Load>(Load.Loading) }
    var unlocking by remember { mutableStateOf<RemoteVault?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    /** Shows the remembered list when it is recent; [force] (Refresh, Retry) always asks the server. */
    fun refresh(force: Boolean = false) {
        if (server == null) return
        if (!force) {
            server.cachedVaults()?.let { load = Load.Loaded(it); return }
        }
        scope.launch(Dispatchers.Main) {
            load = try {
                // The server forgets sessions when it restarts and then reports unlocked = false: that is just "locked" here.
                Load.Loaded(server.api.vaults().also { server.rememberVaults(it) })
            } catch (e: CancellationException) {
                throw e
            } catch (e: RemoteError.CertMismatch) {
                Load.CertChanged(e.sha256)
            } catch (e: Exception) {
                Load.Unreachable((e as? RemoteError.Network)?.let { "Server unreachable." } ?: (e.message ?: "Server error."))
            }
        }
    }
    LaunchedEffect(serverKey) { refresh() }

    fun open(vault: RemoteVault) {
        val id = remoteSourceId(serverKey, vault.id)
        // CachingSource: listings, thumbnails and viewed images are kept in memory, and encrypted on
        // disk under a key from the vault password (derived at unlock below; without it the disk layer
        // stays locked), so browsing does not hit the server for the same thing twice.
        if (session.source(id) == null) {
            session.registerSource(id, vault.name, CachingSource(RemoteSource(server!!.api, vault.id), disk = session.diskCache(id)))
        }
        onOpen(id)
    }

    Scaffold(topBar = {
        TopAppBar(
            title = { Text(server?.label ?: "Server") },
            navigationIcon = { TextButton(onClick = onBack) { Text("Back") } },
            actions = {
                if (server != null) {
                    TextButton(onClick = { load = Load.Loading; refresh(force = true) }, modifier = Modifier.testTag("vaults-refresh")) { Text("Refresh") }
                }
            },
        )
    }) { pad ->
        Column(Modifier.fillMaxSize().padding(pad)) {
            when (val l = load) {
                Load.Loading -> if (server != null) CircularProgressIndicator(Modifier.padding(16.dp).align(Alignment.CenterHorizontally))
                is Load.CertChanged -> Card(Modifier.fillMaxWidth().padding(16.dp)) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("The server's certificate changed.", color = MaterialTheme.colorScheme.error)
                        Text("It no longer matches the one you trusted, so the connection was refused. New SHA-256:")
                        Text(fingerprintDisplay(l.sha256), style = MaterialTheme.typography.bodySmall)
                        Text("Remove this server and connect again only if you expect the change.")
                    }
                }
                is Load.Unreachable -> Card(Modifier.fillMaxWidth().padding(16.dp)) {
                    Column(Modifier.padding(16.dp)) {
                        Text(l.message, color = MaterialTheme.colorScheme.error)
                        TextButton(onClick = { load = Load.Loading; refresh(force = true) }) { Text("Retry") }
                    }
                }
                is Load.Loaded -> if (l.vaults.isEmpty()) {
                    Text("This server has no vaults.", Modifier.padding(16.dp))
                } else LazyColumn {
                    items(l.vaults, key = { it.id }) { v ->
                        ListItem(
                            headlineContent = { Text(v.name) },
                            supportingContent = { Text(if (v.unlocked) "Unlocked" else "Locked") },
                            modifier = Modifier.clickable {
                                if (v.unlocked) open(v) else { error = null; unlocking = v }
                            },
                        )
                    }
                }
            }
            if (server == null) Text("Not connected. Go back and connect again.", Modifier.padding(16.dp))
        }
    }

    unlocking?.let { v ->
        UnlockDialog(
            title = "Unlock ${v.name}",
            error = error,
            busy = busy,
            onDismiss = { unlocking = null },
            onSubmit = { pw, _ ->
                busy = true
                error = null
                scope.launch(Dispatchers.Main) {
                    try {
                        server!!.api.unlock(v.id, pw)
                        // The disk cache is keyed from the same password (PBKDF2: a few hundred ms, off the main thread).
                        session.diskCache(remoteSourceId(serverKey, v.id))?.let { d -> withContext(Dispatchers.Default) { d.unlock(pw) } }
                        server.markUnlocked(v.id)
                        (load as? Load.Loaded)?.let { l -> load = Load.Loaded(l.vaults.map { if (it.id == v.id) it.copy(unlocked = true) else it }) }
                        unlocking = null
                        open(v)
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        error = when (e) {
                            is RemoteError.WrongPassword -> "Wrong password"
                            is RemoteError.RateLimited -> "Too many attempts, wait a moment and try again"
                            is RemoteError.CertMismatch -> {
                                unlocking = null
                                load = Load.CertChanged(e.sha256)
                                null
                            }
                            is RemoteError.Network -> "Server unreachable"
                            else -> e.message ?: "Could not unlock"
                        }
                    } finally {
                        pw.fill('\u0000')
                        busy = false
                    }
                }
            },
        )
    }
}
