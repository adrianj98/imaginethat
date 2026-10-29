package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.CachingSource
import ai.alertd.imagethat.data.EncryptedDiskCache
import ai.alertd.imagethat.data.VaultDiskCache
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.keys.AppUnlock
import ai.alertd.imagethat.keys.KeyVault
import ai.alertd.imagethat.keys.SavedPasswords
import ai.alertd.imagethat.local.LocalVaults
import ai.alertd.imagethat.remote.ApiClient
import ai.alertd.imagethat.remote.RemoteVault
import android.os.SystemClock
import androidx.fragment.app.FragmentActivity
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout

/** Wakes the app after a delay even when its process is frozen (AlarmManager in the app). */
interface LockAlarm {
    fun schedule(delayMs: Long)
    fun cancel()

    object None : LockAlarm {
        override fun schedule(delayMs: Long) = Unit
        override fun cancel() = Unit
    }
}

sealed interface SessionEvent {
    /** A source reported it is locked (session expired); the UI goes back to its unlock prompt. */
    data class SourceLocked(val id: String) : SessionEvent
    object AllLocked : SessionEvent
}

/**
 * A connected remote server; [key] is the saved server's id, or a random one for an unsaved server.
 *
 * The server's vault list is remembered in memory for [VAULTS_TTL_MS] so going back to the list does
 * not ask the server again. The lock state of a vault this app unlocks is patched in with
 * [markUnlocked]; a vault found locked (session expired) drops the list via [forgetVaults].
 */
class RemoteServer(val key: String, val label: String, val url: String, val api: ApiClient) {
    private var vaults: List<RemoteVault>? = null
    private var vaultsAt = 0L

    /** The vault list fetched less than [VAULTS_TTL_MS] ago, or null. */
    @Synchronized
    fun cachedVaults(now: Long = monotonicMs()): List<RemoteVault>? = vaults?.takeIf { now - vaultsAt < VAULTS_TTL_MS }

    @Synchronized
    fun rememberVaults(list: List<RemoteVault>, now: Long = monotonicMs()) {
        vaults = list
        vaultsAt = now
    }

    @Synchronized
    fun markUnlocked(vaultId: String) {
        vaults = vaults?.map { if (it.id == vaultId) it.copy(unlocked = true) else it }
    }

    @Synchronized
    fun forgetVaults() {
        vaults = null
    }

    companion object {
        const val VAULTS_TTL_MS = 5 * 60_000L
        private fun monotonicMs() = System.nanoTime() / 1_000_000
    }
}

/**
 * App-scoped holder of everything that is unlocked: the app lock itself (with the saved passwords
 * decrypted for this session), open [VaultSource]s and connected [ApiClient]s. Nothing here is
 * persisted. [lockAll] drops it all and puts the app back behind its lock screen.
 *
 * Auto-lock is deadline based on an injectable [clock] (milliseconds): the app
 * calls [tick] periodically and [onForeground] on return. A backgrounded
 * process can be frozen (cached-app freezer), which stops [tick], so
 * [onBackground] also arms [alarm]; its broadcast thaws the process and calls
 * [onAlarm], which locks. [onForeground] stays the backstop.
 */
class Session(
    val keys: KeyVault,
    val local: LocalVaults,
    private val clock: () -> Long = { SystemClock.elapsedRealtime() },
    private val clearImageCache: () -> Unit = {},
    /** True when the app lock can authenticate (strong biometric, or a device credential on API 30+). */
    val appLockAvailable: () -> Boolean = { true },
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Default),
    idleTimeoutMs: Long = IDLE_TIMEOUT_MS,
    private val backgroundTimeoutMs: Long = BACKGROUND_TIMEOUT_MS,
    private val alarm: LockAlarm = LockAlarm.None,
    /** Encrypted on-disk cache of remote vault reads; null (tests) means memory-only caching. */
    private val diskCache: EncryptedDiskCache? = null,
) {
    /** The disk cache of one remote vault (same instance each time); null when there is no disk cache. */
    fun diskCache(sourceId: String): VaultDiskCache? = diskCache?.forVault(sourceId)

    /** The configurable idle timeout (AppState.idleTimeoutMinutes); takes effect on the next [tick]. */
    @Volatile var idleTimeoutMs: Long = idleTimeoutMs
        private set

    val idleTimeoutMinutes: Int get() = (idleTimeoutMs / 60_000L).toInt()

    /** Applies the saved idle timeout. Reads the encrypted state file, so call it off the main thread. */
    fun loadIdleTimeout() {
        idleTimeoutMs = keys.state().idleTimeoutMinutes.coerceAtLeast(1) * 60_000L
    }

    /** Saves the idle timeout and applies it to this session. */
    fun setIdleTimeoutMinutes(minutes: Int) {
        require(minutes >= 1)
        keys.store().update { it.copy(idleTimeoutMinutes = minutes) }
        idleTimeoutMs = minutes * 60_000L
    }

    private class Open(val label: String, val source: VaultSource, val close: () -> Unit)

    private val sources = LinkedHashMap<String, Open>()
    private val servers = LinkedHashMap<String, RemoteServer>()
    private val _openIds = MutableStateFlow<Set<String>>(emptySet())
    private val _events = MutableSharedFlow<SessionEvent>(extraBufferCapacity = 16)
    private var lastTouch = clock()
    private var inBackground = false
    private var backgroundSince: Long? = null
    private var backgroundLockPaused = false

    /** Ids of the unlocked sources. */
    val openSourceIds: StateFlow<Set<String>> = _openIds
    val events: SharedFlow<SessionEvent> = _events.asSharedFlow()

    private var passwords: SavedPasswords? = null
    private val _appLocked = MutableStateFlow(true)
    private val _notice = MutableStateFlow<String?>(null)

    /** True until the app-open authentication succeeds, and again after every [lockAll]. */
    val appLocked: StateFlow<Boolean> = _appLocked

    /** A one-time message for the UI after unlocking (saved passwords cleared). */
    val notice: StateFlow<String?> = _notice

    fun dismissNotice() { _notice.value = null }

    /**
     * The app-open authentication (one prompt). On success the saved passwords are held in memory
     * until [lockAll]. Returns false if the user cancelled. [vault] is a test hook.
     */
    suspend fun unlockApp(activity: FragmentActivity?, vault: KeyVault = keys): Boolean {
        vault.migrateLegacy()
        return when (val r = vault.unlockApp(activity)) {
            AppUnlock.Cancelled -> false
            is AppUnlock.Ok -> {
                synchronized(this) {
                    passwords?.wipe()
                    passwords = r.passwords
                    lastTouch = clock()
                }
                if (r.cleared) _notice.value = PASSWORDS_CLEARED
                _appLocked.value = false
                true
            }
        }
    }

    /** A copy of the saved password for [id] (see PasswordIds) that the caller must zero; null if none or locked. */
    @Synchronized fun savedPassword(id: String): CharArray? = passwords?.get(id)

    @Synchronized fun hasSavedPassword(id: String): Boolean = passwords?.contains(id) == true

    /** Saves (or replaces) the password for [id]. Writes the encrypted state file. No-op while locked. */
    @Synchronized fun savePassword(id: String, password: CharArray) { passwords?.put(id, password) }

    @Synchronized fun forgetPassword(id: String) { passwords?.remove(id) }

    @Synchronized fun forgetPasswords(match: (String) -> Boolean) { passwords?.removeIf(match) }

    @Synchronized
    fun registerSource(id: String, label: String, source: VaultSource, close: () -> Unit = {}) {
        sources[id] = Open(label, source, close)
        _openIds.value = sources.keys.toSet()
    }

    @Synchronized fun source(id: String): VaultSource? = sources[id]?.source

    @Synchronized fun label(id: String): String? = sources[id]?.label

    @Synchronized
    fun registerServer(server: RemoteServer) {
        servers.put(server.key, server)?.takeIf { it.api !== server.api }?.api?.close()
    }

    @Synchronized fun server(key: String): RemoteServer? = servers[key]

    /**
     * Locks what a removed server or folder had open: the sources matching [sourceIds] (closing
     * their Go vaults) and, with [serverKey], that server's HTTP session.
     */
    fun closeRemoved(sourceIds: (String) -> Boolean, serverKey: String? = null) {
        val (closing, server) = synchronized(this) {
            val ids = sources.keys.filter(sourceIds)
            val closing = ids.map { id -> id to sources.remove(id)!! }
            _openIds.value = sources.keys.toSet()
            closing to serverKey?.let { servers.remove(it) }
        }
        for ((id, open) in closing) {
            runCatching { open.close() }
            (open.source as? CachingSource)?.let { cache ->
                cache.clear()
                // A removed server's vaults leave nothing behind on disk either.
                if (serverKey != null) scope.launch { runCatching { diskCache(id)?.wipe() } }
            }
        }
        server?.let { it.forgetVaults(); logoutAndClose(it) }
        if (closing.isNotEmpty()) clearImageCache()
        for ((id, _) in closing) _events.tryEmit(SessionEvent.SourceLocked(id))
    }

    /**
     * A source found its vault locked (401, or its Go vault was closed). Drops it, evicts decrypted
     * thumbnails from the memory cache, and tells the UI to go back to unlock.
     */
    fun reportLocked(id: String) {
        val dropped = synchronized(this) {
            val d = sources.remove(id)
            _openIds.value = sources.keys.toSet()
            // The server's vault list said "unlocked" for this one; it will be fetched again.
            servers.values.filter { id.startsWith("${it.key}:") }.forEach { it.forgetVaults() }
            d
        }
        (dropped?.source as? CachingSource)?.clear()
        clearImageCache()
        _events.tryEmit(SessionEvent.SourceLocked(id))
    }

    fun touch() = synchronized(this) { lastTouch = clock() }

    /** Time left before the idle auto-lock fires, for the "locks in m:ss" countdown. */
    fun idleRemainingMs(): Long = synchronized(this) { maxOf(0L, lastTouch + idleTimeoutMs - clock()) }

    fun onBackground() = synchronized(this) {
        inBackground = true
        if (backgroundLockPaused) {
            // An app-launched picker is in front: no 1 minute background lock, but the idle
            // timer still applies, and a frozen process could not tick, so wake at its deadline.
            alarm.schedule(maxOf(0L, lastTouch + idleTimeoutMs - clock()))
        } else {
            backgroundSince = clock()
            alarm.schedule(backgroundTimeoutMs)
        }
    }

    fun onForeground() {
        tick()
        synchronized(this) {
            inBackground = false
            backgroundSince = null
            lastTouch = clock()
        }
        alarm.cancel()
    }

    /**
     * Call right before launching a system picker (photo picker, folder picker) whose result the
     * app needs: leaving for it is not "backgrounded", so the 1 minute background lock is paused
     * until [resumeBackgroundLock]. The idle timeout still applies.
     */
    fun pauseBackgroundLock() = synchronized(this) { backgroundLockPaused = true }

    /** Call when the picker result (or cancellation) arrives. */
    fun resumeBackgroundLock() = synchronized(this) {
        backgroundLockPaused = false
        if (inBackground && backgroundSince == null) {
            backgroundSince = clock()
            alarm.schedule(backgroundTimeoutMs)
        }
    }

    /** The [LockAlarm] fired: the process was backgrounded past a deadline (and may have been frozen). */
    fun onAlarm() {
        val lockNow = synchronized(this) { inBackground && !backgroundLockPaused && backgroundSince != null }
        if (lockNow) lockAll() else tick()
    }

    /** Locks if the idle or background deadline has passed and the app or anything in it is unlocked. */
    fun tick() {
        val now = clock()
        val due = synchronized(this) {
            val bg = backgroundSince.takeUnless { backgroundLockPaused }
            val expired = now - lastTouch >= idleTimeoutMs || (bg != null && now - bg >= backgroundTimeoutMs)
            expired && (passwords != null || sources.isNotEmpty() || servers.isNotEmpty() || local.hasOpen())
        }
        if (due) lockAll()
    }

    /**
     * Closes Go vaults and HTTP sessions, clears every in-memory cache (and locks the disk ones),
     * drops every source, zeroes the saved passwords held in memory and shows the app lock again.
     */
    fun lockAll() {
        val (dropped, closing) = synchronized(this) {
            passwords?.wipe()
            passwords = null
            _appLocked.value = true
            val d = sources.values.map { it.source }
            sources.clear()
            _openIds.value = emptySet()
            d to servers.values.toList().also { servers.clear() }
        }
        local.closeAll()
        for (d in dropped) (d as? CachingSource)?.clear()
        for (s in closing) {
            s.forgetVaults()
            logoutAndClose(s)
        }
        clearImageCache()
        _events.tryEmit(SessionEvent.AllLocked)
    }

    private fun logoutAndClose(s: RemoteServer) {
        scope.launch {
            try {
                withTimeout(LOGOUT_TIMEOUT_MS) { s.api.logout() }
            } catch (_: Exception) {
                // best effort: the cookie is dropped by close() either way
            } finally {
                s.api.close()
            }
        }
    }

    companion object {
        const val IDLE_TIMEOUT_MS = 5 * 60_000L
        /** Choices offered for the idle timeout, in minutes. */
        val IDLE_TIMEOUT_CHOICES = listOf(1, 2, 5, 10, 15, 30)
        const val BACKGROUND_TIMEOUT_MS = 60_000L
        private const val LOGOUT_TIMEOUT_MS = 3_000L
        const val PASSWORDS_CLEARED =
            "Your fingerprints or screen lock changed, so saved passwords were cleared. Enter each password once again."
    }
}
