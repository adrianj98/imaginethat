package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.SavedFolder
import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.remote.ApiClient
import android.content.Intent
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import java.util.UUID

internal const val OFFLINE_ID = "offline"
internal fun folderSourceId(f: SavedFolder) = "folder-${f.id}"

private fun savedFolderLabel(uri: Uri): String =
    uri.lastPathSegment?.substringAfterLast(':')?.substringAfterLast('/')?.takeIf { it.isNotBlank() } ?: "Folder"

/** Servers, vaults on this phone (offline vault and picked folders), and lock-all. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HomeScreen(
    session: Session,
    activity: FragmentActivity?,
    onConnect: () -> Unit,
    onOpenServer: (serverKey: String) -> Unit,
    onSetOfflinePassword: () -> Unit,
    onOpenSource: (sourceId: String) -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val open by session.openSourceIds.collectAsState()
    var state by remember { mutableStateOf(session.keys.state()) }
    var unlocking by remember { mutableStateOf<LocalTarget?>(null) }
    // A folder that was just picked is only remembered once it unlocks.
    var pendingFolder by remember { mutableStateOf<SavedFolder?>(null) }
    var newVaultUri by remember { mutableStateOf<Uri?>(null) }
    var message by remember { mutableStateOf<String?>(null) }

    fun refresh() { state = session.keys.state() }

    fun persist(uri: Uri) {
        val flags = Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION
        runCatching { context.contentResolver.takePersistableUriPermission(uri, flags) }
    }

    fun folderTarget(f: SavedFolder) = LocalTarget(folderSourceId(f), f.label) { pw -> session.local.openFolder(f, pw) }

    val openFolder = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        persist(uri)
        val known = state.folders.firstOrNull { it.treeUri == uri.toString() }
        val folder = known ?: SavedFolder(UUID.randomUUID().toString(), savedFolderLabel(uri), uri.toString())
        if (known == null) pendingFolder = folder
        unlocking = folderTarget(folder)
    }
    val newVaultPicker = rememberLauncherForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri != null) { persist(uri); newVaultUri = uri }
    }

    fun saveFolder(f: SavedFolder) {
        session.keys.store().update { st -> st.copy(folders = st.folders.filterNot { it.id == f.id } + f) }
        refresh()
    }

    fun openServer(s: SavedServer) {
        val url = s.url.toHttpUrlOrNull()
        if (url == null) { message = "The saved address for ${s.label} is not valid."; return }
        if (session.server(s.id) == null) session.registerServer(RemoteServer(s.id, s.label, s.url, ApiClient(url, s.pinnedSha256)))
        onOpenServer(s.id)
    }

    fun openLocal(target: LocalTarget) {
        if (session.source(target.sourceId) != null) onOpenSource(target.sourceId) else unlocking = target
    }

    Scaffold(topBar = {
        TopAppBar(title = { Text("ImageThat") }, actions = {
            TextButton(onClick = { session.lockAll() }) { Text("Lock all") }
        })
    }) { pad ->
        LazyColumn(Modifier.fillMaxSize().padding(pad)) {
            item { SectionHeader("Servers") }
            state.servers.forEach { s ->
                item(key = "server-${s.id}") {
                    ListItem(
                        headlineContent = { Text(s.label) },
                        supportingContent = { Text(s.url) },
                        trailingContent = {
                            TextButton(onClick = {
                                session.keys.store().update { st -> st.copy(servers = st.servers.filterNot { it.id == s.id }) }
                                refresh()
                            }) { Text("Remove") }
                        },
                        modifier = Modifier.clickable { openServer(s) },
                    )
                }
            }
            item { ListItem(headlineContent = { Text("Connect…") }, modifier = Modifier.clickable(onClick = onConnect)) }
            item { HorizontalDivider() }

            item { SectionHeader("On this phone") }
            item {
                ListItem(
                    headlineContent = { Text("Offline vault") },
                    supportingContent = {
                        Text(
                            when {
                                !session.local.offlineExists() -> "Not set up yet"
                                OFFLINE_ID in open -> "Unlocked"
                                else -> "Locked"
                            },
                        )
                    },
                    modifier = Modifier.clickable {
                        if (!session.local.offlineExists()) onSetOfflinePassword()
                        else openLocal(LocalTarget(OFFLINE_ID, "Offline vault") { pw -> session.local.openOffline(pw) })
                    },
                )
            }
            state.folders.forEach { f ->
                item(key = "folder-${f.id}") {
                    ListItem(
                        headlineContent = { Text(f.label) },
                        supportingContent = { Text(if (folderSourceId(f) in open) "Unlocked" else "Locked") },
                        trailingContent = {
                            TextButton(onClick = {
                                session.keys.forget(folderSourceId(f))
                                session.keys.store().update { st -> st.copy(folders = st.folders.filterNot { it.id == f.id }) }
                                refresh()
                            }) { Text("Remove") }
                        },
                        modifier = Modifier.clickable { openLocal(folderTarget(f)) },
                    )
                }
            }
            item { ListItem(headlineContent = { Text("Open folder…") }, modifier = Modifier.clickable { openFolder.launch(null) }) }
            item { ListItem(headlineContent = { Text("New vault…") }, modifier = Modifier.clickable { newVaultPicker.launch(null) }) }
            message?.let { item { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(16.dp)) } }
        }
    }

    unlocking?.let { target ->
        LocalUnlock(
            session, target, activity,
            onUnlocked = { id ->
                pendingFolder?.let { saveFolder(it) }
                pendingFolder = null
                unlocking = null
                refresh()
                onOpenSource(id)
            },
            onDismiss = { unlocking = null; pendingFolder = null },
            onPickAgain = if (pendingFolder == null && target.sourceId != OFFLINE_ID) {
                { unlocking = null; openFolder.launch(null) }
            } else null,
        )
    }

    newVaultUri?.let { uri ->
        NewFolderVaultDialog(
            onDismiss = { newVaultUri = null },
            create = { pw -> withContext(Dispatchers.Default) { session.local.createInFolder(uri, pw) } },
            onCreated = {
                saveFolder(SavedFolder(UUID.randomUUID().toString(), savedFolderLabel(uri), uri.toString()))
                newVaultUri = null
            },
        )
    }
}

@Composable
private fun SectionHeader(text: String) =
    Text(text, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary, modifier = Modifier.padding(16.dp, 16.dp, 16.dp, 4.dp))

@Composable
private fun NewFolderVaultDialog(onDismiss: () -> Unit, create: suspend (CharArray) -> Unit, onCreated: () -> Unit) {
    val scope = rememberCoroutineScope()
    val state = remember { NewPasswordState() }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    SecureAlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        title = "New vault in this folder",
        confirmButton = {
            TextButton(
                enabled = state.valid && !busy,
                onClick = {
                    val pw = state.password.toCharArray()
                    busy = true
                    error = null
                    scope.launch(Dispatchers.Main) {
                        try {
                            create(pw)
                            state.clear()
                            onCreated()
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
        },
        dismissButton = { TextButton(onClick = onDismiss, enabled = !busy) { Text("Cancel") } },
    ) {
        Text(NO_RECOVERY_WARNING)
        NewPasswordFields(state)
        error?.let { Text(it, color = MaterialTheme.colorScheme.error) }
    }
}
