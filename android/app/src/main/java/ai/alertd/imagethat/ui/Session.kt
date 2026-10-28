package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.keys.KeyVault
import ai.alertd.imagethat.local.LocalVaults
import ai.alertd.imagethat.remote.ApiClient
import android.os.SystemClock
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

sealed interface SessionEvent {
    /** A source reported it is locked (session expired); the UI goes back to its unlock prompt. */
    data class SourceLocked(val id: String) : SessionEvent
    object AllLocked : SessionEvent
}

/** A connected remote server; [key] is the saved server's id, or a random one for an unsaved server. */
class RemoteServer(val key: String, val label: String, val url: String, val api: ApiClient)

/**
 * App-scoped holder of everything that is unlocked: open [VaultSource]s and
 * connected [ApiClient]s. Nothing here is persisted. [lockAll] drops it all.
 *
 * Auto-lock is deadline based on an injectable [clock] (milliseconds): the app
 * calls [tick] periodically and [onForeground] on return, so a backgrounded
 * process that was frozen still locks the moment it is used again.
 */
class Session(
    val keys: KeyVault,
    val local: LocalVaults,
    private val clock: () -> Long = { SystemClock.elapsedRealtime() },
    private val clearImageCache: () -> Unit = {},
    /** True when a strong biometric is enrolled, so the "unlock with biometrics" shortcut can be offered. */
    val biometricsAvailable: () -> Boolean = { false },
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Default),
    private val idleTimeoutMs: Long = IDLE_TIMEOUT_MS,
    private val backgroundTimeoutMs: Long = BACKGROUND_TIMEOUT_MS,
) {
    private class Open(val label: String, val source: VaultSource)

    private val sources = LinkedHashMap<String, Open>()
    private val servers = LinkedHashMap<String, RemoteServer>()
    private val _openIds = MutableStateFlow<Set<String>>(emptySet())
    private val _events = MutableSharedFlow<SessionEvent>(extraBufferCapacity = 16)
    private var lastTouch = clock()
    private var backgroundSince: Long? = null

    /** Ids of the unlocked sources. */
    val openSourceIds: StateFlow<Set<String>> = _openIds
    val events: SharedFlow<SessionEvent> = _events.asSharedFlow()

    @Synchronized
    fun registerSource(id: String, label: String, source: VaultSource) {
        sources[id] = Open(label, source)
        _openIds.value = sources.keys.toSet()
    }

    @Synchronized fun source(id: String): VaultSource? = sources[id]?.source

    @Synchronized fun label(id: String): String? = sources[id]?.label

    @Synchronized
    fun registerServer(server: RemoteServer) {
        servers.put(server.key, server)?.takeIf { it.api !== server.api }?.api?.close()
    }

    @Synchronized fun server(key: String): RemoteServer? = servers[key]

    /** A source found its vault locked (401). Drops it and tells the UI to go back to unlock. */
    fun reportLocked(id: String) {
        synchronized(this) {
            sources.remove(id)
            _openIds.value = sources.keys.toSet()
        }
        _events.tryEmit(SessionEvent.SourceLocked(id))
    }

    fun touch() = synchronized(this) { lastTouch = clock() }

    fun onBackground() = synchronized(this) { backgroundSince = clock() }

    fun onForeground() {
        tick()
        synchronized(this) {
            backgroundSince = null
            lastTouch = clock()
        }
    }

    /** Locks if the idle or background deadline has passed and anything is open. */
    fun tick() {
        val now = clock()
        val due = synchronized(this) {
            val bg = backgroundSince
            val expired = if (bg != null) now - bg >= backgroundTimeoutMs else now - lastTouch >= idleTimeoutMs
            expired && (sources.isNotEmpty() || servers.isNotEmpty() || local.hasOpen())
        }
        if (due) lockAll()
    }

    /** Closes Go vaults and HTTP sessions, clears the thumbnail memory cache, drops every source. */
    fun lockAll() {
        val closing = synchronized(this) {
            sources.clear()
            _openIds.value = emptySet()
            servers.values.toList().also { servers.clear() }
        }
        local.closeAll()
        for (s in closing) {
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
        clearImageCache()
        _events.tryEmit(SessionEvent.AllLocked)
    }

    companion object {
        const val IDLE_TIMEOUT_MS = 5 * 60_000L
        const val BACKGROUND_TIMEOUT_MS = 60_000L
        private const val LOGOUT_TIMEOUT_MS = 3_000L
    }
}
