package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.data.offlinePrefix
import ai.alertd.imagethat.media.VaultUri
import ai.alertd.imagethat.ui.components.ActionBar
import ai.alertd.imagethat.ui.components.ActionTile
import ai.alertd.imagethat.ui.components.ActionTone
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.AppTextField
import ai.alertd.imagethat.ui.components.CardRow
import ai.alertd.imagethat.ui.components.Chevron
import ai.alertd.imagethat.ui.components.DialogButton
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.GroupCard
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.InsetDivider
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.components.RoundIconButton
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.components.TopBar
import ai.alertd.imagethat.ui.theme.Palette
import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.BackHandler
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import coil3.compose.AsyncImage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

private const val THUMB_PX = 256

/** Where saved-offline copies go: "<server>/<vault>" (a phone folder files under "This phone"). */
private fun savePrefix(session: Session, sourceId: String): String {
    val vault = session.label(sourceId) ?: sourceId
    val server = if (sourceId.startsWith("folder-")) "This phone"
    else session.server(sourceId.substringBefore(':'))?.label ?: "Server"
    return offlinePrefix(server, vault)
}

private class SessionOffline(private val session: Session) : OfflineAccess {
    override fun exists() = session.local.offlineExists()
    override fun source() = session.source(OFFLINE_ID) as? LocalSource
}

/** Grid of a vault folder with breadcrumb, multi-select actions, upload and the full-screen viewer. */
@Composable
fun BrowserScreen(
    session: Session,
    sourceId: String,
    activity: FragmentActivity?,
    onBack: () -> Unit,
    onSetOfflinePassword: () -> Unit,
) {
    val source = session.source(sourceId)
    if (source == null) {
        // Locked or never opened: nothing to browse; the vault list shows it as locked.
        LaunchedEffect(sourceId) { onBack() }
        Box(Modifier.fillMaxSize().testTag("browser-screen"))
        return
    }
    BrowserContent(session, sourceId, source, activity, onBack, onSetOfflinePassword)
}

/** What the viewer is showing: the folder's media from [path] on, or just that one file when [single]. */
private data class ViewerTarget(val path: String, val single: Boolean)

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun BrowserContent(
    session: Session,
    sourceId: String,
    source: VaultSource,
    activity: FragmentActivity?,
    onBack: () -> Unit,
    onSetOfflinePassword: () -> Unit,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val vm = viewModel<BrowserViewModel>(key = sourceId, factory = viewModelFactory {
        initializer {
            BrowserViewModel(sourceId, source, session.label(sourceId) ?: "Vault", savePrefix(session, sourceId), SessionOffline(session))
        }
    })
    val state by vm.state.collectAsState()
    var viewer by remember { mutableStateOf<ViewerTarget?>(null) }
    var unlockOffline by remember { mutableStateOf(false) }
    var newFolder by remember { mutableStateOf<String?>(null) }
    // "Select" entered with nothing picked yet; a long press or any selection also means selecting.
    var selectMode by remember { mutableStateOf(false) }
    val selecting = selectMode || state.selection.isNotEmpty()
    val endSelection = { selectMode = false; vm.clearSelection() }

    LaunchedEffect(vm) {
        vm.events.collect { e ->
            when (e) {
                BrowserEvent.NavigateToUnlock -> session.reportLocked(sourceId)
                is BrowserEvent.NeedOfflineVault -> if (e.exists) unlockOffline = true else onSetOfflinePassword()
            }
        }
    }

    val pickMedia = rememberPickerLauncher(session, ActivityResultContracts.PickMultipleVisualMedia()) { uris ->
        if (uris.isEmpty()) return@rememberPickerLauncher
        scope.launch(Dispatchers.IO) { vm.upload(uris.map { uploadSpec(context, it) }) }
    }

    BackHandler(enabled = viewer == null && (selecting || state.path != "/")) {
        if (selecting) endSelection() else vm.up()
    }

    val target = viewer
    if (target != null) {
        // Live list: after a delete or move from the viewer the file drops out and the pager shows the next one.
        val media = state.items.filter { !it.isDir && it.media != MediaKind.OTHER }
        val list = if (target.single) state.items.filter { it.path == target.path } else media
        val closeViewer = { viewer = null; vm.clearSelection() }
        if (list.isEmpty()) {
            LaunchedEffect(Unit) { closeViewer() }
        } else {
            val start = remember(target) { list.indexOfFirst { it.path == target.path }.coerceAtLeast(0) }
            ViewerScreen(
                session, sourceId, source, list, start,
                onClose = closeViewer,
                actions = ViewerActions(
                    canSaveOffline = state.canSaveOffline,
                    onSaveOffline = { vm.selectOnly(it); vm.saveSelectedOffline() },
                    onMove = { vm.selectOnly(it); vm.startMove() },
                    onDelete = { vm.selectOnly(it); vm.requestDelete() },
                ),
            )
        }
    } else {
        GalleryLayout(
            session = session,
            sourceId = sourceId,
            state = state,
            vm = vm,
            selecting = selecting,
            onBack = onBack,
            onStartSelect = { selectMode = true },
            onEndSelect = endSelection,
            onUpload = { pickMedia(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageAndVideo)) },
            onNewFolder = { newFolder = "" },
            onAction = { selectMode = false },
            onOpenFile = { item ->
                val isMedia = item.media != MediaKind.OTHER
                viewer = ViewerTarget(item.path, single = !isMedia)
            },
        )
    }

    state.confirmDelete?.let { items ->
        SecureAlertDialog(
            onDismissRequest = vm::cancelDelete,
            title = if (items.size == 1) "Delete ${items[0].name}?" else "Delete ${items.size} items?",
            confirmButton = { DialogButton("Delete", vm::confirmDelete, Modifier.testTag("delete-confirm"), destructive = true) },
            dismissButton = { DialogButton("Cancel", vm::cancelDelete, emphasized = false) },
        ) {
            Text("This cannot be undone. Folders are deleted with everything in them.", style = MaterialTheme.typography.bodyMedium)
        }
    }

    state.result?.let { r ->
        SecureAlertDialog(
            onDismissRequest = vm::dismissResult,
            title = r.title,
            confirmButton = { DialogButton("OK", vm::dismissResult, Modifier.testTag("result-ok")) },
        ) { ResultBody(r) }
    }

    newFolder?.let { name ->
        SecureAlertDialog(
            onDismissRequest = { newFolder = null },
            title = "New folder",
            confirmButton = {
                DialogButton("Create", { vm.newFolder(name); newFolder = null }, Modifier.testTag("folder-create"), enabled = name.isNotBlank())
            },
            dismissButton = { DialogButton("Cancel", { newFolder = null }, emphasized = false) },
        ) {
            AppTextField(name, { newFolder = it }, placeholder = "Folder name", tag = "folder-name")
        }
    }

    state.move?.let { m -> MoveDialog(vm, m) }

    if (unlockOffline) {
        LocalUnlock(
            session,
            LocalTarget(OFFLINE_ID, "Offline vault") { pw -> session.local.openOffline(pw) },
            activity,
            onUnlocked = { unlockOffline = false; vm.saveSelectedOffline() },
            onDismiss = { unlockOffline = false },
        )
    }
}

@Composable
private fun GalleryLayout(
    session: Session,
    sourceId: String,
    state: BrowserState,
    vm: BrowserViewModel,
    selecting: Boolean,
    onBack: () -> Unit,
    onStartSelect: () -> Unit,
    onEndSelect: () -> Unit,
    onUpload: () -> Unit,
    onNewFolder: () -> Unit,
    onAction: () -> Unit,
    onOpenFile: (Item) -> Unit,
) {
    Box(Modifier.fillMaxSize().background(Palette.Background).testTag("browser-screen")) {
        Column(Modifier.fillMaxSize().statusBarsPadding()) {
            if (selecting) {
                SelectionHeader(state.selection.size, onCancel = onEndSelect, onSelectAll = vm::selectAll)
            } else {
                GalleryHeader(session, vm.rootLabel, state, onBack, onStartSelect, onNavigate = vm::navigate)
            }
            state.busy?.let { label ->
                Row(Modifier.fillMaxWidth().padding(horizontal = 18.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    CircularProgressIndicator(Modifier.size(14.dp), color = Palette.Accent, strokeWidth = 2.dp)
                    Text("$label…", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                }
                LinearProgressIndicator(Modifier.fillMaxWidth().padding(horizontal = 16.dp).clip(CircleShape), color = Palette.Accent, trackColor = Palette.Raised)
            }
            state.error?.let {
                Row(Modifier.fillMaxWidth().padding(start = 18.dp, end = 8.dp, top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    ErrorText(it, Modifier.weight(1f).testTag("browser-error"))
                    DialogButton("Retry", { vm.dismissError(); vm.refresh() })
                }
            }
            when {
                state.loading && state.items.isEmpty() -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(color = Palette.Accent)
                }
                state.items.isEmpty() && state.error == null && !state.locked -> EmptyFolder()
                else -> ItemGrid(sourceId, state, vm, selecting, onOpenFile)
            }
        }

        val bottom = Modifier.align(Alignment.BottomCenter).fillMaxWidth().navigationBarsPadding()
        if (selecting) {
            val any = state.selection.isNotEmpty()
            ActionBar(bottom.padding(start = 16.dp, end = 16.dp, bottom = 16.dp)) {
                if (state.canSaveOffline) {
                    ActionTile("Save offline", AppIcons.SaveOffline, { onAction(); vm.saveSelectedOffline() }, Modifier.testTag("action-save-offline"), tone = ActionTone.Accent, enabled = any)
                }
                ActionTile(
                    "Move to…", AppIcons.MoveTo, { onAction(); vm.startMove() }, Modifier.testTag("action-move"),
                    tone = if (state.canSaveOffline) ActionTone.Default else ActionTone.Accent, enabled = any,
                )
                ActionTile("Delete", AppIcons.Delete, { onAction(); vm.requestDelete() }, Modifier.testTag("action-delete"), tone = ActionTone.Destructive, enabled = any)
            }
        } else {
            Row(
                bottom.padding(end = 24.dp, bottom = 24.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp, Alignment.End),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                RoundIconButton(
                    AppIcons.FolderPlus, "New folder", onNewFolder, Modifier.testTag("menu-new-folder"),
                    background = Palette.Surface, bordered = true, size = 56.dp, iconSize = 22.dp,
                )
                PillButton("Upload", onUpload, Modifier.testTag("fab"), icon = AppIcons.Upload)
            }
        }
    }
}

@Composable
private fun GalleryHeader(
    session: Session,
    rootLabel: String,
    state: BrowserState,
    onBack: () -> Unit,
    onStartSelect: () -> Unit,
    onNavigate: (String) -> Unit,
) {
    Column(Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        TopBar(onBack, backDescription = "Back", backTag = "browser-back") {
            OutlinePill("Select", onStartSelect, Modifier.testTag("browser-select"), enabled = state.items.isNotEmpty())
            RoundIconButton(
                AppIcons.LockAction, "Lock all vaults", { session.lockAll() }, Modifier.testTag("browser-lock"),
                tint = Palette.Accent, background = Palette.Surface, bordered = true, size = 40.dp, iconSize = 16.dp,
            )
        }
        val folder = state.path.trimEnd('/').substringAfterLast('/').ifEmpty { rootLabel }
        ScreenTitle(folder, Modifier.padding(horizontal = 8.dp), subtitle = gallerySubtitle(session, state))
        if (state.path != "/") Breadcrumb(rootLabel, state.path, onNavigate)
    }
}

/** "N items · unlocked · locks in m:ss", the countdown following the idle auto-lock deadline. */
@Composable
private fun gallerySubtitle(session: Session, state: BrowserState): String {
    val remaining by produceState(session.idleRemainingMs(), session) {
        while (true) {
            value = session.idleRemainingMs()
            delay(1_000)
        }
    }
    val secs = (remaining + 999) / 1000
    val count = when (val n = state.items.size) { 1 -> "1 item"; else -> "$n items" }
    return "$count · unlocked · locks in ${secs / 60}:${"%02d".format(secs % 60)}"
}

@Composable
private fun SelectionHeader(count: Int, onCancel: () -> Unit, onSelectAll: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().padding(start = 4.dp, end = 4.dp, top = 8.dp, bottom = 12.dp).heightIn(min = 44.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        DialogButton("Cancel", onCancel, Modifier.testTag("selection-cancel"), emphasized = false, color = Palette.Text)
        Text(
            if (count == 0) "Select items" else "$count selected",
            Modifier.weight(1f), color = Palette.Text, textAlign = TextAlign.Center,
            style = MaterialTheme.typography.titleMedium.copy(fontWeight = FontWeight.Bold),
        )
        DialogButton("Select all", onSelectAll, Modifier.testTag("selection-all"))
    }
}

@Composable
private fun EmptyFolder() {
    Column(
        Modifier.fillMaxSize().padding(32.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp, Alignment.CenterVertically),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        IconTile(AppIcons.Folder, tint = Palette.TextTertiary, background = Palette.Surface, border = Palette.Divider, size = 64.dp, radius = 20.dp, iconSize = 26.dp)
        Text("This folder is empty", Modifier.testTag("browser-empty"), color = Palette.Text, style = MaterialTheme.typography.titleMedium)
        Text("Upload photos or videos, or make a folder.", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
    }
}

@Composable
private fun MoveDialog(vm: BrowserViewModel, m: MoveState) {
    SecureAlertDialog(
        onDismissRequest = vm::cancelMove,
        title = "Move to",
        confirmButton = { DialogButton("Move here", vm::confirmMove, Modifier.testTag("move-here"), enabled = !m.loading) },
        dismissButton = { DialogButton("Cancel", vm::cancelMove, emphasized = false) },
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Icon(AppIcons.Folder, null, Modifier.size(16.dp), tint = Palette.Accent)
            Text(if (m.path == "/") vm.rootLabel else m.path, color = Palette.Text, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(16.dp)).background(Palette.Background).border(1.dp, Palette.Divider, RoundedCornerShape(16.dp))) {
            if (m.path != "/") {
                CardRow(
                    "Up", onClick = { vm.moveNavigate(m.path.trimEnd('/').substringBeforeLast('/').ifEmpty { "/" }) },
                    leading = { Icon(AppIcons.Back, null, Modifier.size(18.dp), tint = Palette.TextSecondary) },
                    trailing = null,
                    contentPadding = PaddingValues(horizontal = 14.dp, vertical = 10.dp),
                )
                if (m.folders.isNotEmpty() || m.loading) InsetDivider(inset = 14.dp)
            }
            if (m.loading) {
                Box(Modifier.fillMaxWidth().padding(16.dp), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(Modifier.size(22.dp), color = Palette.Accent, strokeWidth = 2.dp)
                }
            } else if (m.folders.isEmpty()) {
                Text("No folders here", Modifier.padding(14.dp), color = Palette.TextTertiary, style = MaterialTheme.typography.bodySmall)
            }
            LazyColumn(Modifier.heightIn(max = 240.dp)) {
                itemsIndexed(m.folders, key = { _, f -> f.path }) { i, f ->
                    if (i > 0) InsetDivider(inset = 14.dp)
                    CardRow(
                        f.name, onClick = { vm.moveNavigate(f.path) },
                        leading = { Icon(AppIcons.Folder, null, Modifier.size(18.dp), tint = Palette.TextSecondary) },
                        contentPadding = PaddingValues(horizontal = 14.dp, vertical = 10.dp),
                    )
                }
            }
        }
    }
}

@Composable
private fun ResultBody(r: OpResult) {
    Column(Modifier.heightIn(max = 320.dp).verticalScrollable(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        r.note?.let { ErrorText(it) }
        if (r.ok.isEmpty() && r.failed.isEmpty() && r.skipped.isEmpty()) Text("Nothing was done.", style = MaterialTheme.typography.bodyMedium)
        if (r.ok.isNotEmpty()) {
            SectionLabel("${r.ok.size} succeeded", Modifier.padding(top = 4.dp).testTag("result-saved"), color = Palette.Online)
            r.ok.forEach { Text(it, color = Palette.Text, style = MaterialTheme.typography.bodySmall) }
        }
        if (r.skipped.isNotEmpty()) {
            SectionLabel("${r.skipped.size} already saved", Modifier.padding(top = 8.dp).testTag("result-skipped"))
            r.skipped.forEach { Text(it, style = MaterialTheme.typography.bodySmall) }
        }
        if (r.failed.isNotEmpty()) {
            SectionLabel("${r.failed.size} failed", Modifier.padding(top = 8.dp).testTag("result-failed"), color = Palette.Destructive)
            r.failed.forEach { (name, why) -> Text("$name: $why", style = MaterialTheme.typography.bodySmall, color = Palette.Destructive) }
        }
    }
}

@Composable
private fun Modifier.verticalScrollable(): Modifier = this.verticalScroll(rememberScrollState())

@Composable
private fun Breadcrumb(root: String, path: String, onNavigate: (String) -> Unit) {
    val segs = path.split('/').filter { it.isNotEmpty() }
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 4.dp).testTag("breadcrumb"),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(2.dp),
    ) {
        Crumb(root, current = false) { onNavigate("/") }
        var acc = ""
        segs.forEachIndexed { i, s ->
            acc += "/$s"
            val target = acc
            Chevron(Modifier.size(14.dp))
            Crumb(s, current = i == segs.lastIndex) { onNavigate(target) }
        }
    }
}

@Composable
private fun Crumb(text: String, current: Boolean, onClick: () -> Unit) {
    Text(
        text,
        Modifier.clip(CircleShape).clickable(onClick = onClick).heightIn(min = 32.dp).padding(horizontal = 8.dp, vertical = 6.dp),
        color = if (current) Palette.Text else Palette.TextSecondary,
        style = MaterialTheme.typography.labelLarge.copy(fontSize = 13.sp),
        maxLines = 1,
    )
}

private val TileShape = RoundedCornerShape(6.dp)

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ItemGrid(sourceId: String, state: BrowserState, vm: BrowserViewModel, selecting: Boolean, onOpenFile: (Item) -> Unit) {
    val haptic = LocalHapticFeedback.current
    val folders = state.items.filter { it.isDir }
    val files = state.items.filter { !it.isDir }
    fun click(item: Item) = when {
        selecting -> vm.toggle(item)
        item.isDir -> vm.navigate(item.path)
        else -> onOpenFile(item)
    }
    fun longClick(item: Item) {
        haptic.performHapticFeedback(HapticFeedbackType.LongPress)
        vm.toggle(item)
    }
    LazyVerticalGrid(
        columns = GridCells.Fixed(3),
        modifier = Modifier.fillMaxSize().testTag("browser-grid"),
        contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 128.dp),
        horizontalArrangement = Arrangement.spacedBy(3.dp),
        // Row gaps come from each tile's bottom padding, so the folder card needs no gap trickery.
    ) {
        // Folders first, as one grouped card of rows spanning the grid.
        if (folders.isNotEmpty()) {
            item(key = "folders", span = { GridItemSpan(maxLineSpan) }) {
                GroupCard(Modifier.padding(bottom = 12.dp)) {
                    folders.forEachIndexed { i, item ->
                        if (i > 0) InsetDivider()
                        FolderRow(item, selected = item.path in state.selection, selecting = selecting, onClick = { click(item) }, onLongClick = { longClick(item) })
                    }
                }
            }
        }
        items(files, key = { it.path }) { item ->
            FileTile(sourceId, item, selected = item.path in state.selection, selecting = selecting, onClick = { click(item) }, onLongClick = { longClick(item) })
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun FolderRow(item: Item, selected: Boolean, selecting: Boolean, onClick: () -> Unit, onLongClick: () -> Unit) {
    Row(
        Modifier
            .fillMaxWidth()
            .testTag("item-${item.name}")
            .combinedClickable(onClick = onClick, onLongClick = onLongClick)
            .padding(horizontal = 16.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        IconTile(AppIcons.Folder, tint = Palette.Accent)
        Text(item.name, Modifier.weight(1f), color = Palette.Text, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (selecting) SelectCheck(selected) else Chevron()
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun FileTile(sourceId: String, item: Item, selected: Boolean, selecting: Boolean, onClick: () -> Unit, onLongClick: () -> Unit) {
    Box(
        Modifier
            .padding(bottom = 3.dp)
            .aspectRatio(1f)
            .clip(TileShape)
            .background(Palette.Surface)
            .testTag("item-${item.name}")
            .combinedClickable(onClick = onClick, onLongClick = onLongClick),
    ) {
        Box(Modifier.fillMaxSize().graphicsLayer { alpha = if (selected) 0.6f else 1f }) {
            // Behind the thumbnail: stays visible when there is none (non-images, no preview, failures).
            Icon(
                when (item.media) { MediaKind.VIDEO -> AppIcons.Play; MediaKind.IMAGE -> AppIcons.Image; MediaKind.OTHER -> AppIcons.File },
                contentDescription = null,
                modifier = Modifier.align(Alignment.Center).size(22.dp),
                tint = Palette.TextTertiary,
            )
            if (item.media != MediaKind.OTHER) {
                AsyncImage(
                    model = VaultUri.build(sourceId, item.path, THUMB_PX),
                    contentDescription = item.name,
                    contentScale = ContentScale.Crop,
                    modifier = Modifier.fillMaxSize(),
                )
            } else {
                Text(
                    item.name,
                    Modifier.align(Alignment.BottomStart).fillMaxWidth().padding(6.dp),
                    color = Palette.TextSecondary,
                    style = MaterialTheme.typography.labelSmall,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (item.media == MediaKind.VIDEO) {
                Box(
                    Modifier.align(Alignment.BottomStart).padding(6.dp).size(22.dp).clip(CircleShape).background(Palette.Scrim),
                    contentAlignment = Alignment.Center,
                ) { Icon(AppIcons.Play, "Video", Modifier.size(12.dp), tint = Palette.Text) }
            }
        }
        if (selecting) SelectCheck(selected, Modifier.align(Alignment.TopEnd).padding(6.dp))
    }
}

/** Selection circle: white ring, accent fill and check when selected. */
@Composable
private fun SelectCheck(selected: Boolean, modifier: Modifier = Modifier) {
    Box(
        modifier
            .size(22.dp)
            .clip(CircleShape)
            .background(if (selected) Palette.Accent else Color(0x590E0F12))
            .border(2.dp, Color.White, CircleShape)
            .semantics { stateDescription = if (selected) "Selected" else "Not selected" },
        contentAlignment = Alignment.Center,
    ) {
        if (selected) Icon(AppIcons.Check, null, Modifier.size(12.dp), tint = Palette.OnAccent)
    }
}

/** Name and exact size of a picked item. The size comes from the provider's metadata, never from copying the file. */
private fun uploadSpec(context: android.content.Context, uri: Uri): UploadSpec {
    val resolver = context.contentResolver
    var name: String? = null
    var size = -1L
    resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
        if (c.moveToFirst()) {
            c.getColumnIndex(OpenableColumns.DISPLAY_NAME).takeIf { it >= 0 && !c.isNull(it) }?.let { name = c.getString(it) }
            c.getColumnIndex(OpenableColumns.SIZE).takeIf { it >= 0 && !c.isNull(it) }?.let { size = c.getLong(it) }
        }
    }
    if (size < 0) size = runCatching { resolver.openAssetFileDescriptor(uri, "r")?.use { it.length } ?: -1L }.getOrDefault(-1L)
    val fileName = name ?: uri.lastPathSegment?.substringAfterLast('/') ?: "upload"
    return UploadSpec(fileName, size) { if (size < 0) null else runCatching { resolver.openInputStream(uri) }.getOrNull() }
}
