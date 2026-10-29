package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.CachingSource
import ai.alertd.imagethat.data.RemoteSource
import ai.alertd.imagethat.keys.PasswordIds
import ai.alertd.imagethat.remote.RemoteError
import ai.alertd.imagethat.remote.RemoteVault
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.Chevron
import ai.alertd.imagethat.ui.components.ErrorText
import ai.alertd.imagethat.ui.components.Gap
import ai.alertd.imagethat.ui.components.GroupCard
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.InsetDivider
import ai.alertd.imagethat.ui.components.OutlinePill
import ai.alertd.imagethat.ui.components.RoundIconButton
import ai.alertd.imagethat.ui.components.ScreenTitle
import ai.alertd.imagethat.ui.components.SearchField
import ai.alertd.imagethat.ui.components.StatusDot
import ai.alertd.imagethat.ui.components.TopBar
import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.PopupProperties
import androidx.compose.ui.window.SecureFlagPolicy
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

internal fun remoteSourceId(serverKey: String, vaultId: String) = "$serverKey:$vaultId"

private sealed interface Load {
    object Loading : Load
    class Loaded(val vaults: List<RemoteVault>) : Load
    /** Temporary: unreachable, retry may help. */
    class Unreachable(val message: String) : Load
    /** Hard failure: the pinned certificate changed. No retry, no bypass. */
    class CertChanged(val sha256: String) : Load
}

/**
 * The server's vaults with their lock state. Tap unlocks with the saved password when there is
 * one (no prompt), otherwise asks once and saves it; long-press offers to forget it.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun RemoteVaultsScreen(session: Session, serverKey: String, onBack: () -> Unit, onOpen: (sourceId: String) -> Unit) {
    val server = session.server(serverKey)
    val scope = rememberCoroutineScope()
    var load by remember { mutableStateOf<Load>(Load.Loading) }
    var unlocking by remember { mutableStateOf<RemoteVault?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var notice by remember { mutableStateOf<String?>(null) }
    /** A vault being unlocked with its saved password (no dialog). */
    var autoUnlocking by remember { mutableStateOf<String?>(null) }
    var forgetting by remember { mutableStateOf<RemoteVault?>(null) }

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
        // CachingSource: listings, thumbnails, images and video pieces are kept in memory and encrypted
        // on disk under a key from the vault password (derived in unlock below; without it the disk
        // layer stays locked), so browsing does not hit the server for the same thing twice.
        if (session.source(id) == null) {
            session.registerSource(id, vault.name, CachingSource(RemoteSource(server!!.api, vault.id), disk = session.diskCache(id)))
        }
        onOpen(id)
    }

    /** [fromSaved]: a wrong password means the saved one is stale: forget it and ask once. */
    fun unlock(v: RemoteVault, pw: CharArray, fromSaved: Boolean) {
        val passwordId = PasswordIds.server(serverKey, v.id)
        busy = true
        error = null
        if (fromSaved) autoUnlocking = v.id
        scope.launch(Dispatchers.Main) {
            try {
                server!!.api.unlock(v.id, pw)
                if (!fromSaved) withContext(Dispatchers.IO) { runCatching { session.savePassword(passwordId, pw) } }
                // The disk cache is keyed from the same password (PBKDF2: a few hundred ms, off the main thread).
                session.diskCache(remoteSourceId(serverKey, v.id))?.let { d -> withContext(Dispatchers.Default) { d.unlock(pw) } }
                server.markUnlocked(v.id)
                (load as? Load.Loaded)?.let { l -> load = Load.Loaded(l.vaults.map { if (it.id == v.id) it.copy(unlocked = true) else it }) }
                unlocking = null
                open(v)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                val message = when (e) {
                    is RemoteError.WrongPassword -> if (fromSaved) {
                        session.forgetPassword(passwordId)
                        notice = "The saved password no longer opens this vault. Enter the password again."
                        null
                    } else "Wrong password"
                    is RemoteError.RateLimited -> "Too many attempts, wait a moment and try again"
                    is RemoteError.CertMismatch -> {
                        unlocking = null
                        load = Load.CertChanged(e.sha256)
                        null
                    }
                    is RemoteError.Network -> "Server unreachable"
                    else -> e.message ?: "Could not unlock"
                }
                error = message
                // A saved password that failed for another reason (network, 429) is kept; the dialog shows why.
                if (fromSaved && e !is RemoteError.CertMismatch) unlocking = v
            } finally {
                pw.fill('\u0000')
                busy = false
                autoUnlocking = null
            }
        }
    }

    fun tap(v: RemoteVault) {
        if (v.unlocked) { open(v); return }
        if (busy) return
        error = null
        notice = null
        val saved = session.savedPassword(PasswordIds.server(serverKey, v.id))
        if (saved != null) unlock(v, saved, fromSaved = true) else unlocking = v
    }

    val allVaults = (load as? Load.Loaded)?.vaults
    var query by remember { mutableStateOf("") }
    var menu by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf(false) }
    val port = server?.url?.toHttpUrlOrNull()?.port

    Column(
        Modifier.fillMaxSize().background(Palette.Background).statusBarsPadding().navigationBarsPadding()
            .padding(start = 16.dp, end = 16.dp, top = 8.dp),
    ) {
        TopBar(onBack, backTag = "servers-back") {
            Box {
                RoundIconButton(AppIcons.More, "More options", { menu = true }, Modifier.testTag("server-menu"))
                DropdownMenu(
                    expanded = menu,
                    onDismissRequest = { menu = false },
                    containerColor = Palette.Surface,
                    properties = PopupProperties(focusable = true, securePolicy = SecureFlagPolicy.SecureOn),
                ) {
                    // The vault list is remembered for a few minutes; this asks the server again.
                    DropdownMenuItem(
                        text = { Text("Refresh") },
                        onClick = { menu = false; load = Load.Loading; refresh(force = true) },
                        modifier = Modifier.testTag("vaults-refresh"),
                    )
                    DropdownMenuItem(
                        text = { Text("Remove server", color = Palette.Destructive) },
                        onClick = { menu = false; removing = true },
                        modifier = Modifier.testTag("server-remove"),
                    )
                }
            }
        }
        val (dot, status) = when (val l = load) {
            Load.Loading -> Palette.TextTertiary to if (server == null) "Not connected" else "Connecting…"
            is Load.Loaded -> Palette.Online to listOfNotNull(
                "Online", port?.let { "port $it" }, if (l.vaults.size == 1) "1 vault" else "${l.vaults.size} vaults",
            ).joinToString(" · ")
            is Load.Unreachable -> Palette.Destructive to "Unreachable"
            is Load.CertChanged -> Palette.Destructive to "Certificate changed"
        }
        ScreenTitle(
            server?.label ?: "Server", Modifier.padding(start = 8.dp, end = 8.dp, top = 8.dp),
            subtitle = status, subtitleLeading = { StatusDot(dot) },
        )
        Gap(16.dp)
        if (!allVaults.isNullOrEmpty()) {
            SearchField(query, { query = it }, placeholder = "Search vaults", tag = "vault-search")
            Gap(12.dp)
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(bottom = 24.dp)) {
            when (val l = load) {
                Load.Loading -> if (server != null) {
                    Box(Modifier.fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) {
                        CircularProgressIndicator(color = Palette.Accent)
                    }
                }
                is Load.CertChanged -> GroupCard {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("The server's certificate changed.", color = Palette.Destructive, style = MaterialTheme.typography.titleMedium)
                        Text("It no longer matches the one you trusted, so the connection was refused. New SHA-256:", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                        Text(fingerprintDisplay(l.sha256), color = Palette.Text, style = MaterialTheme.typography.bodySmall.copy(fontFamily = FontFamily.Monospace))
                        Text("Remove this server and connect again only if you expect the change.", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                    }
                }
                is Load.Unreachable -> GroupCard {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        ErrorText(l.message)
                        OutlinePill("Retry", { load = Load.Loading; refresh(force = true) })
                    }
                }
                is Load.Loaded -> {
                    val shown = l.vaults.filter { query.isBlank() || it.name.contains(query.trim(), ignoreCase = true) }
                    when {
                        l.vaults.isEmpty() -> EmptyNote("This server has no vaults.")
                        shown.isEmpty() -> EmptyNote("No vaults match “${query.trim()}”.")
                        else -> GroupCard {
                            shown.forEachIndexed { i, v ->
                                if (i > 0) InsetDivider()
                                VaultRow(
                                    title = v.name,
                                    unlocked = v.unlocked,
                                    saved = session.hasSavedPassword(PasswordIds.server(serverKey, v.id)),
                                    busy = autoUnlocking == v.id,
                                    modifier = Modifier.combinedClickable(
                                        onLongClick = {
                                            if (session.hasSavedPassword(PasswordIds.server(serverKey, v.id))) forgetting = v
                                        },
                                    ) { tap(v) }.testTag("vault-${v.name}"),
                                )
                            }
                        }
                    }
                }
            }
            if (server == null) EmptyNote("Not connected. Go back and connect again.")
        }
    }

    unlocking?.let { v ->
        UnlockDialog(
            vaultName = v.name,
            place = server?.label ?: "Server",
            idleMinutes = session.idleTimeoutMinutes,
            notice = notice,
            error = error,
            busy = busy,
            onDismiss = { unlocking = null },
            onSubmit = { pw -> unlock(v, pw, fromSaved = false) },
        )
    }

    if (removing) {
        RemoveServerDialog(
            server?.label ?: "this server",
            onRemove = { removing = false; removeServer(session, serverKey); onBack() },
            onDismiss = { removing = false },
        )
    }

    forgetting?.let { v ->
        ForgetPasswordDialog(
            v.name,
            onForget = { session.forgetPassword(PasswordIds.server(serverKey, v.id)); forgetting = null },
            onDismiss = { forgetting = null },
        )
    }
}

@Composable
private fun EmptyNote(text: String) {
    Text(text, Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 16.dp), color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium)
}

/**
 * A vault row: lock tile, name, "Locked"/"Unlocked" and, when a password is saved, "Password saved"
 * (separate texts so each can be found on its own). [busy] shows a spinner in place of the chevron.
 */
@Composable
internal fun VaultRow(
    title: String,
    unlocked: Boolean,
    saved: Boolean,
    modifier: Modifier = Modifier,
    busy: Boolean = false,
    icon: ImageVector = if (unlocked) AppIcons.LockAction else AppIcons.Lock,
    status: String = if (unlocked) "Unlocked" else "Locked",
    trailing: (@Composable () -> Unit)? = null,
) {
    Row(
        modifier.fillMaxWidth().heightIn(min = 64.dp).padding(horizontal = 16.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        IconTile(icon, tint = if (unlocked) Palette.Accent else Palette.Text)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(title, color = Palette.Text, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(status, color = if (unlocked) Palette.Online else Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                if (saved) {
                    Text("·", color = Palette.TextTertiary, style = MaterialTheme.typography.bodySmall)
                    Text("Password saved", color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
                }
            }
        }
        when {
            busy -> CircularProgressIndicator(Modifier.size(20.dp), color = Palette.Accent, strokeWidth = 2.dp)
            trailing != null -> trailing()
            else -> Chevron()
        }
    }
}
