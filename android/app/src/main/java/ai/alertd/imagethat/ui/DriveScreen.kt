package ai.alertd.imagethat.ui

import ai.alertd.imagethat.drive.DriveFile
import ai.alertd.imagethat.drive.isVaultFolder
import ai.alertd.imagethat.drive.parseDriveFiles
import ai.alertd.imagethat.drive.parseOAuthClientJson
import ai.alertd.imagethat.keys.PasswordIds
import ai.alertd.imagethat.keys.SavedDriveVault
import ai.alertd.imagethat.local.LocalError
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.AppTextField
import ai.alertd.imagethat.ui.components.CardRow
import ai.alertd.imagethat.ui.components.Chevron
import ai.alertd.imagethat.ui.components.DialogButton
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.FootnoteText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.GroupCard
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.InsetDivider
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.PillButton
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.components.TopBar
import ai.alertd.imagethat.ui.theme.Palette
import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import androidx.activity.compose.BackHandler
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Drive
import mobile.Mobile
import mobile.SignIn
import java.util.UUID

/** Session id of an opened Drive vault. */
internal fun driveSourceId(v: SavedDriveVault) = Session.DRIVE_SOURCE_PREFIX + v.id

/** The unlock target of a Drive vault: a writable vault the Go engine reads from Drive and caches. */
internal fun driveTarget(session: Session, drive: Drive, v: SavedDriveVault) = LocalTarget(
    driveSourceId(v), PasswordIds.drive(v.rootId), v.label, place = DRIVE_PLACE, cachesFiles = true,
) { pw -> session.local.openDrive(drive, v.rootId, pw) }

internal const val DRIVE_PLACE = "Google Drive"

private data class Folder(val id: String, val name: String)

private sealed interface DriveLoad {
    object Loading : DriveLoad
    /** Not set up or not signed in. */
    object Setup : DriveLoad
    class Ready(val drive: Drive) : DriveLoad
}

/**
 * Google Drive: first the user's own OAuth client and sign-in (as on the Mac), then a folder
 * browser in which a folder holding gocryptfs.conf opens as a vault. Opened vaults are remembered
 * on Home. Also the Drive cache on this phone and signing out.
 */
@Composable
fun DriveScreen(session: Session, activity: FragmentActivity?, onBack: () -> Unit, onOpenSource: (sourceId: String) -> Unit) {
    var load by remember { mutableStateOf<DriveLoad>(DriveLoad.Loading) }
    LaunchedEffect(Unit) {
        val d = withContext(Dispatchers.IO) { session.drive() }
        load = if (d == null) DriveLoad.Setup else DriveLoad.Ready(d)
    }
    when (val l = load) {
        DriveLoad.Loading -> Box(Modifier.fillMaxSize().background(Palette.Background), contentAlignment = Alignment.Center) {
            CircularProgressIndicator(color = Palette.Accent)
        }
        DriveLoad.Setup -> DriveSetup(session, onBack, onConnected = { load = DriveLoad.Ready(it) })
        is DriveLoad.Ready -> DriveBrowser(session, l.drive, activity, onBack, onOpenSource, onSignedOut = { load = DriveLoad.Setup })
    }
}

@Composable
private fun DriveSetup(session: Session, onBack: () -> Unit, onConnected: (Drive) -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var clientId by remember { mutableStateOf("") }
    // Plain (non-saveable) state: saved state can be written out in a Bundle.
    var clientSecret by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var pending by remember { mutableStateOf<SignIn?>(null) }

    // Leaving the screen (or the app locking) ends a sign-in still waiting for Google.
    DisposableEffect(Unit) { onDispose { pending?.cancel() } }

    val importJson = rememberPickerLauncher(session, ActivityResultContracts.OpenDocument()) { uri ->
        if (uri == null) return@rememberPickerLauncher
        scope.launch {
            val text = withContext(Dispatchers.IO) {
                runCatching { context.contentResolver.openInputStream(uri)?.use { it.readBytes().decodeToString() } }.getOrNull()
            }
            val creds = text?.let(::parseOAuthClientJson)
            if (creds == null) {
                error = "That file has no client_id and client_secret."
            } else {
                clientId = creds.first
                clientSecret = creds.second
                error = null
            }
        }
    }

    fun connect() {
        val id = clientId.trim()
        val secret = clientSecret.trim().toCharArray()
        error = null
        scope.launch {
            try {
                val drive = Mobile.newDrive(id, String(secret), "")
                val signIn = withContext(Dispatchers.IO) { drive.startSignIn() }
                pending = signIn
                // The browser in front is not "backgrounded": a background lock would end the sign-in.
                session.pauseBackgroundLock()
                try {
                    context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(signIn.url())))
                    withContext(Dispatchers.IO) { signIn.await() }
                } finally {
                    session.resumeBackgroundLock()
                }
                withContext(Dispatchers.IO) { session.saveDrive(drive, id, secret) }
                clientSecret = ""
                onConnected(drive)
            } catch (e: CancellationException) {
                throw e
            } catch (e: ActivityNotFoundException) {
                pending?.cancel()
                error = "No browser is installed to sign in with."
            } catch (e: Exception) {
                error = when (LocalError.from(e)) {
                    is LocalError.Cancelled -> null
                    is LocalError.Auth -> "Google refused the sign-in. Check the client ID and secret."
                    else -> e.message ?: "Could not sign in."
                }
            } finally {
                pending = null
                secret.fill('\u0000')
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
            .padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 32.dp),
    ) {
        TopBar(onBack, backTag = "drive-back")
        Column(Modifier.padding(horizontal = 8.dp), verticalArrangement = Arrangement.spacedBy(16.dp)) {
            Gap(4.dp)
            ScreenTitle("Connect Google Drive", subtitle = "Open vaults stored in your Google Drive")
            Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Text("Create OAuth 2.0 credentials at console.cloud.google.com:", color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium)
                Text("1. APIs & Services → Credentials → Create Credentials", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                Text("2. Choose OAuth 2.0 Client ID → Application type: Desktop app", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                Text("3. No redirect URI registration required", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
            }
            AppTextField(
                clientId, { clientId = it; error = null },
                label = "Client ID", placeholder = "Paste your Client ID", tag = "drive-client-id",
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, autoCorrectEnabled = false),
                enabled = pending == null,
            )
            AppTextField(
                clientSecret, { clientSecret = it; error = null },
                label = "Client Secret", placeholder = "Paste your Client Secret", tag = "drive-client-secret",
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false),
                visualTransformation = PasswordVisualTransformation(),
                enabled = pending == null,
            )
            OutlinePill("Upload JSON…", { importJson(arrayOf("application/json")) }, Modifier.testTag("drive-import-json"), icon = AppIcons.Upload)
            error?.let { ErrorText(it, Modifier.testTag("drive-error")) }
            if (pending != null) {
                Text(
                    "Finish signing in in your browser, then come back here.",
                    color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium,
                )
                OutlinePill("Cancel connection", { pending?.cancel() }, Modifier.fillMaxWidth().testTag("drive-cancel"), height = 48.dp)
            }
            PillButton(
                "Connect", { connect() }, Modifier.fillMaxWidth().testTag("drive-connect"),
                enabled = clientId.isNotBlank() && clientSecret.isNotBlank(), loading = pending != null,
            )
            FootnoteText("The client secret and the sign-in token are saved encrypted on this phone. Vault passwords never leave it.")
        }
    }
}

@Composable
private fun DriveBrowser(
    session: Session,
    drive: Drive,
    activity: FragmentActivity?,
    onBack: () -> Unit,
    onOpenSource: (sourceId: String) -> Unit,
    onSignedOut: () -> Unit,
) {
    val scope = rememberCoroutineScope()
    var stack by remember { mutableStateOf(listOf(Folder("root", "My Drive"))) }
    val folder = stack.last()
    var files by remember { mutableStateOf<List<DriveFile>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var reload by remember { mutableStateOf(0) }
    var unlocking by remember { mutableStateOf<LocalTarget?>(null) }
    // A Drive vault is only remembered once it unlocks.
    var pendingVault by remember { mutableStateOf<SavedDriveVault?>(null) }
    var cacheBytes by remember { mutableLongStateOf(-1L) }
    var signingOut by remember { mutableStateOf(false) }
    val cacheDir = session.local.networkCacheDir

    fun up() { if (stack.size > 1) stack = stack.dropLast(1) else onBack() }
    BackHandler(enabled = stack.size > 1) { up() }

    LaunchedEffect(folder.id, reload) {
        files = null
        error = null
        try {
            files = withContext(Dispatchers.IO) { parseDriveFiles(drive.list(folder.id)) }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            error = when (LocalError.from(e)) {
                is LocalError.Auth, is LocalError.NotConnected -> "Google Drive refused access. Sign out and connect again."
                else -> e.message ?: "Could not list the folder."
            }
        }
    }
    LaunchedEffect(reload) { cacheBytes = withContext(Dispatchers.IO) { Mobile.networkCacheSize(cacheDir.absolutePath) } }

    fun openVault() {
        val known = session.keys.state().driveVaults.firstOrNull { it.rootId == folder.id }
        val v = known ?: SavedDriveVault(UUID.randomUUID().toString(), folder.name, folder.id)
        if (session.source(driveSourceId(v)) != null) { onOpenSource(driveSourceId(v)); return }
        if (known == null) pendingVault = v
        unlocking = driveTarget(session, drive, v)
    }

    Column(
        Modifier
            .fillMaxSize()
            .background(Palette.Background)
            .verticalScroll(rememberScrollState())
            .statusBarsPadding()
            .navigationBarsPadding()
            .padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 32.dp),
    ) {
        TopBar({ up() }, backTag = "drive-back")
        ScreenTitle(folder.name, Modifier.padding(start = 8.dp, end = 8.dp, top = 8.dp), subtitle = DRIVE_PLACE)
        Gap(16.dp)
        val list = files
        when {
            error != null -> GroupCard {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    ErrorText(error!!, Modifier.testTag("drive-error"))
                    OutlinePill("Retry", { reload++ })
                }
            }
            list == null -> Box(Modifier.fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(color = Palette.Accent)
            }
            else -> {
                if (isVaultFolder(list)) {
                    GroupCard {
                        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                            Text("This folder is a vault.", color = Palette.Text, style = MaterialTheme.typography.titleMedium)
                            Text(
                                "It is decrypted on this phone; files are fetched from Drive as you open them.",
                                color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall,
                            )
                            PillButton("Open vault", { openVault() }, Modifier.fillMaxWidth().testTag("drive-open-vault"), icon = AppIcons.LockKeyhole, height = 48.dp)
                        }
                    }
                    Gap(16.dp)
                }
                val folders = list.filter { it.isFolder }
                if (folders.isEmpty()) {
                    Text("No folders here.", Modifier.padding(horizontal = 8.dp, vertical = 8.dp), color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium)
                } else {
                    GroupCard {
                        folders.forEachIndexed { i, f ->
                            if (i > 0) InsetDivider()
                            CardRow(
                                f.name, Modifier.testTag("drive-folder-${f.name}"),
                                onClick = { stack = stack + Folder(f.id, f.name) },
                                leading = { IconTile(AppIcons.Folder, tint = Palette.Accent) },
                                trailing = { Chevron() },
                            )
                        }
                    }
                }
            }
        }

        Gap(28.dp)
        SectionLabel("Google Drive", Modifier.padding(horizontal = 8.dp))
        Gap(10.dp)
        GroupCard {
            CardRow(
                "Cache on this phone",
                Modifier.testTag("drive-cache"),
                subtitle = when {
                    cacheBytes < 0 -> "…"
                    cacheBytes == 0L -> "No files cached"
                    else -> "${formatSize(cacheBytes)} of encrypted Drive files"
                },
                leading = { IconTile(AppIcons.Cloud) },
                trailing = {
                    OutlinePill("Clear", {
                        scope.launch {
                            withContext(Dispatchers.IO) { runCatching { Mobile.clearNetworkCache(cacheDir.absolutePath) } }
                            reload++
                        }
                    }, Modifier.testTag("drive-cache-clear"), enabled = cacheBytes > 0)
                },
            )
            InsetDivider()
            CardRow(
                "Sign out of Google Drive", Modifier.testTag("drive-sign-out"),
                subtitle = "Locks open Drive vaults and forgets their saved passwords.",
                onClick = { signingOut = true }, titleColor = Palette.Destructive, trailing = null,
            )
        }
    }

    unlocking?.let { target ->
        LocalUnlock(
            session, target, activity,
            onUnlocked = { id ->
                pendingVault?.let { v -> session.keys.store().update { st -> st.copy(driveVaults = st.driveVaults.filterNot { it.id == v.id } + v) } }
                pendingVault = null
                unlocking = null
                onOpenSource(id)
            },
            onDismiss = { unlocking = null; pendingVault = null },
        )
    }

    if (signingOut) {
        SecureAlertDialog(
            onDismissRequest = { signingOut = false },
            title = "Sign out of Google Drive?",
            confirmButton = {
                DialogButton("Sign out", {
                    signingOut = false
                    scope.launch {
                        withContext(Dispatchers.IO) { session.forgetDrive() }
                        onSignedOut()
                    }
                }, Modifier.testTag("drive-sign-out-confirm"), destructive = true)
            },
            dismissButton = { DialogButton("Cancel", { signingOut = false }, emphasized = false) },
        ) {
            Text("Nothing in your Drive changes. Connect again to open its vaults.", style = MaterialTheme.typography.bodyMedium)
        }
    }
}
