package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.PasswordIds
import ai.alertd.imagethat.keys.SavedFolder
import ai.alertd.imagethat.keys.SavedServer
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.remote.ApiClient
import ai.alertd.imagethat.ui.components.AccentChip
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.CardRow
import ai.alertd.imagethat.ui.components.Chevron
import ai.alertd.imagethat.ui.components.DialogButton
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.GroupCard
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.InsetDivider
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.RoundIconButton
import ai.alertd.imagethat.ui.components.RowValue
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.components.SplitButton
import ai.alertd.imagethat.ui.components.SplitRow
import ai.alertd.imagethat.ui.theme.Palette
import android.content.Intent
import android.net.Uri
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
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
@OptIn(ExperimentalFoundationApi::class)
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
    var removingServer by remember { mutableStateOf<SavedServer?>(null) }
    var folderOptions by remember { mutableStateOf<SavedFolder?>(null) }

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

    val offlineExists = session.local.offlineExists()
    Column(
        Modifier
            .fillMaxSize()
            .background(Palette.Background)
            .verticalScroll(rememberScrollState())
            .statusBarsPadding()
            .navigationBarsPadding()
            .padding(start = 24.dp, end = 24.dp, top = 20.dp, bottom = 32.dp),
    ) {
        Row(verticalAlignment = Alignment.Top) {
            ScreenTitle("ImageThat", Modifier.weight(1f), subtitle = homeSummary(state.servers.size, open.size), large = true)
            OutlinePill("Lock all", { session.lockAll() }, Modifier.padding(top = 2.dp).testTag("lock-all"), icon = AppIcons.LockAction)
        }

        Section("Servers") {
            state.servers.forEach { s ->
                CardRow(
                    s.label,
                    Modifier.combinedClickable(onLongClick = { removingServer = s }) { openServer(s) }.testTag("server-${s.id}"),
                    subtitle = serverSubtitle(s.url),
                    leading = { IconTile(AppIcons.Server) },
                    trailing = { Chevron() },
                )
                InsetDivider()
            }
            CardRow(
                "Connect to a server", onClick = onConnect, trailing = null,
                leading = { IconTile(AppIcons.Plus, tint = Palette.TextSecondary, dashed = true) },
                modifier = Modifier.testTag("connect-row"),
            )
        }

        Section("On this phone") {
            VaultRow(
                "Offline vault",
                unlocked = OFFLINE_ID in open,
                saved = session.hasSavedPassword(PasswordIds.OFFLINE),
                icon = AppIcons.Phone,
                status = when {
                    !offlineExists -> "Not set up yet"
                    OFFLINE_ID in open -> "Unlocked"
                    else -> "Locked"
                },
                trailing = if (!offlineExists) ({ AccentChip("Set up") }) else null,
                modifier = Modifier.combinedClickable(
                    onLongClick = { offerForget(PasswordIds.OFFLINE, "Offline vault") },
                ) {
                    if (!session.local.offlineExists()) onSetOfflinePassword()
                    else openLocal(LocalTarget(OFFLINE_ID, PasswordIds.OFFLINE, "Offline vault") { pw -> session.local.openOffline(pw) })
                }.testTag("offline-row"),
            )
            state.folders.forEach { f ->
                InsetDivider()
                VaultRow(
                    f.label,
                    unlocked = folderSourceId(f) in open,
                    saved = session.hasSavedPassword(PasswordIds.folder(f.id)),
                    icon = AppIcons.Folder,
                    modifier = Modifier.combinedClickable(onLongClick = { folderOptions = f }) { openLocal(folderTarget(f)) }
                        .testTag("folder-${f.id}"),
                    trailing = {
                        RoundIconButton(
                            AppIcons.More, "Options for ${f.label}", { folderOptions = f },
                            Modifier.testTag("folder-options-${f.id}"), tint = Palette.TextTertiary, size = 40.dp, iconSize = 18.dp,
                        )
                    },
                )
            }
            InsetDivider(inset = 0.dp)
            SplitRow {
                SplitButton("Open folder", AppIcons.Folder, { openFolder(null) }, Modifier.testTag("open-folder"), divider = true)
                SplitButton("New vault", AppIcons.Plus, { newVaultPicker(null) }, Modifier.testTag("new-vault"))
            }
        }

        Section("Settings") {
            CardRow(
                "Auto-lock after",
                Modifier.testTag("idle-timeout-setting"),
                subtitle = minutesLabel(state.idleTimeoutMinutes) + " without a touch",
                onClick = { pickingTimeout = true },
                leading = { IconTile(AppIcons.Clock) },
                trailing = {
                    RowValue("${state.idleTimeoutMinutes} min")
                    Chevron()
                },
            )
            InsetDivider()
            CardRow(
                "App lock",
                subtitle = "Fingerprint, face or screen lock, each time the app opens",
                leading = { IconTile(AppIcons.Fingerprint, tint = Palette.Accent) },
                trailing = { AccentChip("On", filled = false) },
            )
        }
        message?.let {
            Gap(16.dp)
            ErrorText(it, Modifier.padding(horizontal = 4.dp))
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

    removingServer?.let { s ->
        RemoveServerDialog(
            s.label,
            onRemove = { removeServer(session, s.id); removingServer = null; refresh() },
            onDismiss = { removingServer = null },
        )
    }

    folderOptions?.let { f ->
        FolderOptionsDialog(
            f.label,
            hasSavedPassword = session.hasSavedPassword(PasswordIds.folder(f.id)),
            onForget = { folderOptions = null; forgetting = PasswordIds.folder(f.id) to f.label },
            onRemove = {
                session.forgetPassword(PasswordIds.folder(f.id))
                session.keys.store().update { st -> st.copy(folders = st.folders.filterNot { it.id == f.id }) }
                folderOptions = null
                refresh()
            },
            onDismiss = { folderOptions = null },
        )
    }
}

/** "1 server · 2 vaults unlocked", or "· all locked" when nothing is open. */
internal fun homeSummary(servers: Int, unlocked: Int): String {
    val s = if (servers == 1) "1 server" else "$servers servers"
    val v = when (unlocked) { 0 -> "all locked"; 1 -> "1 vault unlocked"; else -> "$unlocked vaults unlocked" }
    return "$s · $v"
}

/** "Port 8347 · https" from a saved server URL (its online state is only known on its own screen). */
internal fun serverSubtitle(url: String): String {
    val u = url.toHttpUrlOrNull() ?: return url
    return "Port ${u.port} · ${if (u.isHttps) "https" else "http, not encrypted"}"
}

/** Forgets a remembered server and every saved password of its vaults. The vaults on the server are untouched. */
internal fun removeServer(session: Session, id: String) {
    session.keys.store().update { st -> st.copy(servers = st.servers.filterNot { it.id == id }) }
    session.forgetPasswords { it.startsWith(PasswordIds.serverPrefix(id)) }
}

@Composable
internal fun RemoveServerDialog(label: String, onRemove: () -> Unit, onDismiss: () -> Unit) = SecureAlertDialog(
    onDismissRequest = onDismiss,
    title = "Remove $label?",
    confirmButton = { DialogButton("Remove", onRemove, Modifier.testTag("remove-confirm"), destructive = true) },
    dismissButton = { DialogButton("Cancel", onDismiss, emphasized = false) },
) {
    Text("Its saved vault passwords are forgotten too. Nothing on the server changes.", style = MaterialTheme.typography.bodyMedium)
}

@Composable
private fun FolderOptionsDialog(label: String, hasSavedPassword: Boolean, onForget: () -> Unit, onRemove: () -> Unit, onDismiss: () -> Unit) =
    SecureAlertDialog(
        onDismissRequest = onDismiss,
        title = label,
        confirmButton = { DialogButton("Cancel", onDismiss, emphasized = false) },
    ) {
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Palette.Background).border(1.dp, Palette.Divider, RoundedCornerShape(16.dp))) {
            if (hasSavedPassword) {
                CardRow("Forget saved password", Modifier.testTag("folder-forget"), onClick = onForget, trailing = null)
                InsetDivider()
            }
            CardRow(
                "Remove from this list", Modifier.testTag("folder-remove"),
                subtitle = "The folder and its vault stay on the phone.",
                onClick = onRemove, titleColor = Palette.Destructive, trailing = null,
            )
        }
    }

@Composable
private fun Section(label: String, content: @Composable ColumnScope.() -> Unit) {
    Gap(28.dp)
    SectionLabel(label)
    Gap(10.dp)
    GroupCard(content = content)
}

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
            DialogButton(
                "Create",
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
            )
        },
        dismissButton = { DialogButton("Cancel", onDismiss, enabled = !busy, emphasized = false) },
    ) {
        Text(NO_RECOVERY_WARNING, style = MaterialTheme.typography.bodySmall)
        NewPasswordFields(state)
        if (busy) CircularProgressIndicator(Modifier.size(20.dp), color = Palette.Accent, strokeWidth = 2.dp)
        error?.let { ErrorText(it) }
    }
}

@Composable
private fun IdleTimeoutDialog(current: Int, onPick: (Int) -> Unit, onDismiss: () -> Unit) {
    SecureAlertDialog(
        onDismissRequest = onDismiss,
        title = "Auto-lock after",
        confirmButton = { DialogButton("Cancel", onDismiss, emphasized = false) },
    ) {
        Text("Everything locks after this long without a touch.", style = MaterialTheme.typography.bodySmall)
        Column(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Palette.Background)
                .border(1.dp, Palette.Divider, RoundedCornerShape(16.dp)).selectableGroup(),
        ) {
            Session.IDLE_TIMEOUT_CHOICES.forEachIndexed { i, m ->
                if (i > 0) InsetDivider()
                Row(
                    Modifier.fillMaxWidth()
                        .heightIn(min = 48.dp)
                        .selectable(selected = m == current, role = Role.RadioButton, onClick = { onPick(m) })
                        .padding(horizontal = 16.dp, vertical = 12.dp)
                        .testTag("idle-timeout-$m"),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        minutesLabel(m), Modifier.weight(1f),
                        color = if (m == current) Palette.Text else Palette.TextSecondary,
                        style = MaterialTheme.typography.titleSmall,
                    )
                    if (m == current) Icon(AppIcons.Check, null, Modifier.size(18.dp), tint = Palette.Accent)
                }
            }
        }
    }
}
