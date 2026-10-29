package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.PasswordIds
import ai.alertd.imagethat.keys.SavedFolder
import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.remote.ApiClient
import android.content.Intent
import android.net.Uri
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.material3.RadioButton
import androidx.compose.ui.Alignment
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
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
@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
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
    var pickingTimeout by remember { mutableStateOf(false) }
    /** (password id, label) whose saved password the user asked to forget. */
    var forgetting by remember { mutableStateOf<Pair<String, String>?>(null) }

    fun refresh() { state = session.keys.state() }

    fun persist(uri: Uri) {
        val flags = Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION
        runCatching { context.contentResolver.takePersistableUriPermission(uri, flags) }
    }

    fun folderTarget(f: SavedFolder) =
        LocalTarget(folderSourceId(f), PasswordIds.folder(f.id), f.label) { pw -> session.local.openFolder(f, pw) }

    /** Long-press: offer to forget a saved password; nothing to do when none is saved. */
    fun offerForget(id: String, label: String) {
        if (session.hasSavedPassword(id)) forgetting = id to label
    }

    val openFolder = rememberPickerLauncher(session, ActivityResultContracts.OpenDocumentTree()) { uri ->
        if (uri == null) return@rememberPickerLauncher
        persist(uri)
        val known = state.folders.firstOrNull { it.treeUri == uri.toString() }
        val folder = known ?: SavedFolder(UUID.randomUUID().toString(), savedFolderLabel(uri), uri.toString())
        if (known == null) pendingFolder = folder
        unlocking = folderTarget(folder)
    }
    val newVaultPicker = rememberPickerLauncher(session, ActivityResultContracts.OpenDocumentTree()) { uri ->
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
                                session.forgetPasswords { it.startsWith(PasswordIds.serverPrefix(s.id)) }
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
                    modifier = Modifier.combinedClickable(
                        onLongClick = { offerForget(PasswordIds.OFFLINE, "Offline vault") },
                    ) {
                        if (!session.local.offlineExists()) onSetOfflinePassword()
                        else openLocal(LocalTarget(OFFLINE_ID, PasswordIds.OFFLINE, "Offline vault") { pw -> session.local.openOffline(pw) })
                    }.testTag("offline-row"),
                )
            }
            state.folders.forEach { f ->
                item(key = "folder-${f.id}") {
                    ListItem(
                        headlineContent = { Text(f.label) },
                        supportingContent = { Text(if (folderSourceId(f) in open) "Unlocked" else "Locked") },
                        trailingContent = {
                            TextButton(onClick = {
                                session.forgetPassword(PasswordIds.folder(f.id))
                                session.keys.store().update { st -> st.copy(folders = st.folders.filterNot { it.id == f.id }) }
                                refresh()
                            }) { Text("Remove") }
                        },
                        modifier = Modifier.combinedClickable(onLongClick = { offerForget(PasswordIds.folder(f.id), f.label) }) {
                            openLocal(folderTarget(f))
                        },
                    )
                }
            }
            item { ListItem(headlineContent = { Text("Open folder…") }, modifier = Modifier.clickable { openFolder(null) }) }
            item { ListItem(headlineContent = { Text("New vault…") }, modifier = Modifier.clickable { newVaultPicker(null) }) }
            item { HorizontalDivider() }

            item { SectionHeader("Settings") }
            item {
                ListItem(
                    headlineContent = { Text("Auto-lock after") },
                    supportingContent = { Text(minutesLabel(state.idleTimeoutMinutes) + " without a touch") },
                    modifier = Modifier.clickable { pickingTimeout = true }.testTag("idle-timeout-setting"),
                )
            }
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
                { unlocking = null; openFolder(null) }
            } else null,
        )
    }

    if (pickingTimeout) {
        IdleTimeoutDialog(
            current = state.idleTimeoutMinutes,
            onPick = { m ->
                session.setIdleTimeoutMinutes(m)
                refresh()
                pickingTimeout = false
            },
            onDismiss = { pickingTimeout = false },
        )
    }

    newVaultUri?.let { uri ->
        val folder = remember(uri) { SavedFolder(UUID.randomUUID().toString(), savedFolderLabel(uri), uri.toString()) }
        NewFolderVaultDialog(
            onDismiss = { newVaultUri = null },
            create = { pw ->
                val toSave = pw.copyOf() // createInFolder zeroes pw
                try {
                    withContext(Dispatchers.Default) { session.local.createInFolder(uri, pw) }
                    withContext(Dispatchers.IO) { runCatching { session.savePassword(PasswordIds.folder(folder.id), toSave) } }
                } finally {
                    toSave.fill('\u0000')
                }
            },
            onCreated = {
                saveFolder(folder)
                newVaultUri = null
            },
        )
    }

    forgetting?.let { (id, label) ->
        ForgetPasswordDialog(label, onForget = { session.forgetPassword(id); forgetting = null }, onDismiss = { forgetting = null })
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

private fun minutesLabel(m: Int) = if (m == 1) "1 minute" else "$m minutes"

@Composable
private fun IdleTimeoutDialog(current: Int, onPick: (Int) -> Unit, onDismiss: () -> Unit) {
    SecureAlertDialog(
        onDismissRequest = onDismiss,
        title = "Auto-lock after",
        confirmButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    ) {
        Column(Modifier.selectableGroup()) {
            Session.IDLE_TIMEOUT_CHOICES.forEach { m ->
                Row(
                    Modifier.fillMaxWidth()
                        .selectable(selected = m == current, role = Role.RadioButton, onClick = { onPick(m) })
                        .padding(vertical = 8.dp)
                        .testTag("idle-timeout-$m"),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RadioButton(selected = m == current, onClick = null)
                    Text(minutesLabel(m), Modifier.padding(start = 12.dp))
                }
            }
        }
    }
}
