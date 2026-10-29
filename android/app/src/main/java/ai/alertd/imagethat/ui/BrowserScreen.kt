package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.data.offlinePrefix
import ai.alertd.imagethat.media.VaultUri
import android.net.Uri
import android.provider.OpenableColumns
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
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
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.PopupProperties
import androidx.compose.ui.window.SecureFlagPolicy
import androidx.fragment.app.FragmentActivity
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import coil3.compose.AsyncImage
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

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
@OptIn(ExperimentalMaterial3Api::class)
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

@OptIn(ExperimentalMaterial3Api::class, ExperimentalFoundationApi::class)
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
    var viewer by remember { mutableStateOf<Pair<List<Item>, Int>?>(null) }
    var unlockOffline by remember { mutableStateOf(false) }
    var newFolder by remember { mutableStateOf<String?>(null) }
    var menuOpen by remember { mutableStateOf(false) }

    LaunchedEffect(vm) {
        vm.events.collect { e ->
            when (e) {
                BrowserEvent.NavigateToUnlock -> session.reportLocked(sourceId)
                is BrowserEvent.NeedOfflineVault -> if (e.exists) unlockOffline = true else onSetOfflinePassword()
            }
        }
    }

    val picker = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia()) { uris ->
        if (uris.isEmpty()) return@rememberLauncherForActivityResult
        scope.launch(Dispatchers.IO) { vm.upload(uris.map { uploadSpec(context, it) }) }
    }

    BackHandler(enabled = viewer == null && (state.selection.isNotEmpty() || state.path != "/")) {
        if (state.selection.isNotEmpty()) vm.clearSelection() else vm.up()
    }

    viewer?.let { (list, index) ->
        ViewerScreen(session, sourceId, source, list, index, onClose = { viewer = null })
        return
    }

    Scaffold(
        modifier = Modifier.testTag("browser-screen"),
        topBar = {
            if (state.selection.isEmpty()) {
                TopAppBar(
                    title = { Text(vm.rootLabel, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    navigationIcon = { TextButton(onClick = onBack) { Text("Back") } },
                )
            } else {
                TopAppBar(
                    title = { Text("${state.selection.size} selected") },
                    navigationIcon = { TextButton(onClick = vm::clearSelection) { Text("Cancel") } },
                    actions = {
                        if (state.canSaveOffline) {
                            TextButton(onClick = vm::saveSelectedOffline, modifier = Modifier.testTag("action-save-offline")) { Text("Save offline") }
                        }
                        TextButton(onClick = vm::startMove, modifier = Modifier.testTag("action-move")) { Text("Move") }
                        TextButton(onClick = vm::requestDelete, modifier = Modifier.testTag("action-delete")) { Text("Delete") }
                    },
                )
            }
        },
        floatingActionButton = {
            if (state.selection.isEmpty()) {
                Box {
                    FloatingActionButton(onClick = { menuOpen = true }, modifier = Modifier.testTag("fab")) { Text("+") }
                    DropdownMenu(
                        expanded = menuOpen,
                        onDismissRequest = { menuOpen = false },
                        properties = PopupProperties(focusable = true, securePolicy = SecureFlagPolicy.SecureOn),
                    ) {
                        DropdownMenuItem(text = { Text("Upload photos or videos") }, onClick = {
                            menuOpen = false
                            picker.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageAndVideo))
                        })
                        DropdownMenuItem(text = { Text("New folder") }, modifier = Modifier.testTag("menu-new-folder"), onClick = {
                            menuOpen = false
                            newFolder = ""
                        })
                    }
                }
            }
        },
    ) { pad ->
        Column(Modifier.fillMaxSize().padding(pad)) {
            Breadcrumb(vm.rootLabel, state.path, vm::navigate)
            if (state.busy != null) {
                Text(state.busy!! + "…", Modifier.padding(horizontal = 16.dp), style = MaterialTheme.typography.bodySmall)
                LinearProgressIndicator(Modifier.fillMaxWidth())
            }
            state.error?.let {
                Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.weight(1f).testTag("browser-error"))
                    TextButton(onClick = { vm.dismissError(); vm.refresh() }) { Text("Retry") }
                }
            }
            when {
                state.loading && state.items.isEmpty() -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
                state.items.isEmpty() && state.error == null && !state.locked -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Text("This folder is empty", modifier = Modifier.testTag("browser-empty"))
                }
                else -> ItemGrid(sourceId, state, vm, onOpenFile = { item ->
                    val media = state.items.filter { !it.isDir && it.media != MediaKind.OTHER }
                    val idx = media.indexOfFirst { it.path == item.path }
                    viewer = if (idx >= 0) media to idx else listOf(item) to 0
                })
            }
        }
    }

    state.confirmDelete?.let { items ->
        SecureAlertDialog(
            onDismissRequest = vm::cancelDelete,
            title = if (items.size == 1) "Delete ${items[0].name}?" else "Delete ${items.size} items?",
            confirmButton = { TextButton(onClick = vm::confirmDelete, modifier = Modifier.testTag("delete-confirm")) { Text("Delete") } },
            dismissButton = { TextButton(onClick = vm::cancelDelete) { Text("Cancel") } },
        ) {
            Text("This cannot be undone. Folders are deleted with everything in them.")
        }
    }

    state.result?.let { r ->
        SecureAlertDialog(
            onDismissRequest = vm::dismissResult,
            title = r.title,
            confirmButton = { TextButton(onClick = vm::dismissResult, modifier = Modifier.testTag("result-ok")) { Text("OK") } },
        ) { ResultBody(r) }
    }

    newFolder?.let { name ->
        SecureAlertDialog(
            onDismissRequest = { newFolder = null },
            title = "New folder",
            confirmButton = {
                TextButton(enabled = name.isNotBlank(), onClick = { vm.newFolder(name); newFolder = null }, modifier = Modifier.testTag("folder-create")) { Text("Create") }
            },
            dismissButton = { TextButton(onClick = { newFolder = null }) { Text("Cancel") } },
        ) {
            OutlinedTextField(name, { newFolder = it }, label = { Text("Name") }, singleLine = true, modifier = Modifier.fillMaxWidth().testTag("folder-name"))
        }
    }

    state.move?.let { m ->
        SecureAlertDialog(
            onDismissRequest = vm::cancelMove,
            title = "Move to",
            confirmButton = { TextButton(onClick = vm::confirmMove, enabled = !m.loading, modifier = Modifier.testTag("move-here")) { Text("Move here") } },
            dismissButton = { TextButton(onClick = vm::cancelMove) { Text("Cancel") } },
        ) {
            Text(if (m.path == "/") vm.rootLabel else m.path, style = MaterialTheme.typography.bodySmall)
            if (m.path != "/") {
                TextButton(onClick = { vm.moveNavigate(m.path.trimEnd('/').substringBeforeLast('/').ifEmpty { "/" }) }) { Text("Up") }
            }
            if (m.loading) CircularProgressIndicator()
            LazyColumn(Modifier.heightIn(max = 240.dp)) {
                items(m.folders, key = { it.path }) { f ->
                    Text(f.name, Modifier.fillMaxWidth().clickable { vm.moveNavigate(f.path) }.padding(vertical = 12.dp))
                }
            }
        }
    }

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
private fun ResultBody(r: OpResult) {
    Column(Modifier.heightIn(max = 320.dp).verticalScrollable(), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        r.note?.let { Text(it, color = MaterialTheme.colorScheme.error) }
        if (r.ok.isEmpty() && r.failed.isEmpty() && r.skipped.isEmpty()) Text("Nothing was done.")
        if (r.ok.isNotEmpty()) {
            Text("${r.ok.size} succeeded", style = MaterialTheme.typography.titleSmall, modifier = Modifier.testTag("result-saved"))
            r.ok.forEach { Text(it, style = MaterialTheme.typography.bodySmall) }
        }
        if (r.skipped.isNotEmpty()) {
            Text("${r.skipped.size} already saved", style = MaterialTheme.typography.titleSmall, modifier = Modifier.testTag("result-skipped"))
            r.skipped.forEach { Text(it, style = MaterialTheme.typography.bodySmall) }
        }
        if (r.failed.isNotEmpty()) {
            Text("${r.failed.size} failed", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("result-failed"))
            r.failed.forEach { (name, why) -> Text("$name: $why", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error) }
        }
    }
}

@Composable
private fun Modifier.verticalScrollable(): Modifier = this.verticalScroll(rememberScrollState())

@Composable
private fun Breadcrumb(root: String, path: String, onNavigate: (String) -> Unit) {
    val segs = path.split('/').filter { it.isNotEmpty() }
    Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp).testTag("breadcrumb"), verticalAlignment = Alignment.CenterVertically) {
        TextButton(onClick = { onNavigate("/") }) { Text(root) }
        var acc = ""
        segs.forEach { s ->
            acc += "/$s"
            val target = acc
            Text("/")
            TextButton(onClick = { onNavigate(target) }) { Text(s) }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ItemGrid(sourceId: String, state: BrowserState, vm: BrowserViewModel, onOpenFile: (Item) -> Unit) {
    val haptic = LocalHapticFeedback.current
    LazyVerticalGrid(
        columns = GridCells.Adaptive(110.dp),
        modifier = Modifier.fillMaxSize().testTag("browser-grid"),
        contentPadding = androidx.compose.foundation.layout.PaddingValues(8.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(state.items, key = { it.path }) { item ->
            val selected = item.path in state.selection
            Box(
                Modifier
                    .aspectRatio(1f)
                    .clip(MaterialTheme.shapes.small)
                    .background(MaterialTheme.colorScheme.surfaceVariant)
                    .then(if (selected) Modifier.border(3.dp, MaterialTheme.colorScheme.primary, MaterialTheme.shapes.small) else Modifier)
                    .testTag("item-${item.name}")
                    .combinedClickable(
                        onClick = {
                            when {
                                state.selection.isNotEmpty() -> vm.toggle(item)
                                item.isDir -> vm.navigate(item.path)
                                else -> onOpenFile(item)
                            }
                        },
                        onLongClick = {
                            haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                            vm.toggle(item)
                        },
                    ),
            ) {
                // Behind the thumbnail: stays visible when there is none (non-images, no preview, failures).
                Text(
                    when {
                        item.isDir -> "Folder"
                        item.media == MediaKind.VIDEO -> "Video"
                        item.media == MediaKind.IMAGE -> "Image"
                        else -> "File"
                    },
                    Modifier.align(Alignment.Center),
                    style = MaterialTheme.typography.labelMedium,
                )
                if (!item.isDir && item.media != MediaKind.OTHER) {
                    AsyncImage(
                        model = VaultUri.build(sourceId, item.path, THUMB_PX),
                        contentDescription = item.name,
                        contentScale = ContentScale.Crop,
                        modifier = Modifier.fillMaxSize(),
                    )
                }
                Text(
                    item.name,
                    Modifier.align(Alignment.BottomStart).fillMaxWidth().background(Color(0x99000000)).padding(4.dp),
                    color = Color.White,
                    style = MaterialTheme.typography.labelSmall,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (selected) {
                    Text(
                        "Selected",
                        Modifier.align(Alignment.TopEnd).padding(4.dp).background(MaterialTheme.colorScheme.primary).padding(horizontal = 4.dp),
                        color = MaterialTheme.colorScheme.onPrimary,
                        style = MaterialTheme.typography.labelSmall,
                    )
                }
            }
        }
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
