package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.CachingSource
import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.SaveInterrupted
import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.data.saveOffline
import androidx.lifecycle.ViewModel
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.receiveAsFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import java.io.InputStream

/** The offline vault as the browser sees it: it may not exist yet, or exist but be locked. */
interface OfflineAccess {
    fun exists(): Boolean
    /** The unlocked offline vault, or null when it is not open. */
    fun source(): LocalSource?
}

/** One file to upload. [open] returns null when the file can no longer be read. [size] must be exact. */
class UploadSpec(val name: String, val size: Long, val open: () -> InputStream?)

/** Per-file outcome of a bulk operation. [ok] and [failed] (name or path to reason) are listed as they are. */
data class OpResult(
    val title: String,
    val ok: List<String>,
    val failed: Map<String, String>,
    val note: String? = null,
    /** The vault was locked part-way: dismissing the result goes to the unlock prompt. */
    val unlockAfter: Boolean = false,
    /** Already there from an earlier save (same size); not copied again, not a failure. */
    val skipped: List<String> = emptyList(),
)

data class MoveState(val path: String, val folders: List<Item> = emptyList(), val loading: Boolean = true)

data class BrowserState(
    val path: String = "/",
    val items: List<Item> = emptyList(),
    val loading: Boolean = true,
    val error: String? = null,
    /** The vault reported itself locked; the UI is going back to the unlock prompt. */
    val locked: Boolean = false,
    val selection: Set<String> = emptySet(),
    val confirmDelete: List<Item>? = null,
    val result: OpResult? = null,
    /** Label of a long-running operation, null when idle. */
    val busy: String? = null,
    val move: MoveState? = null,
    val canSaveOffline: Boolean = true,
)

sealed interface BrowserEvent {
    /** The source is locked: tell the Session so the UI returns to the unlock prompt. */
    object NavigateToUnlock : BrowserEvent
    /** Save offline needs the offline vault: [exists] false means it must be created first. */
    data class NeedOfflineVault(val exists: Boolean) : BrowserEvent
}

/** Folders first, then names, case-insensitive. */
private val ORDER = compareByDescending<Item> { it.isDir }.thenBy { it.name.lowercase() }.thenBy { it.name }

/**
 * State and operations of one vault browser. Everything runs on [scope]; a
 * [SourceError.Locked] from any call becomes [BrowserEvent.NavigateToUnlock]
 * instead of an empty folder or an error banner.
 */
class BrowserViewModel(
    private val sourceId: String,
    private val source: VaultSource,
    val rootLabel: String,
    /** Destination folder inside the offline vault: "<server>/<vault>". */
    private val savePrefix: String,
    private val offline: OfflineAccess,
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate),
) : ViewModel() {
    private val _state = MutableStateFlow(BrowserState(canSaveOffline = sourceId != OFFLINE_ID))
    val state: StateFlow<BrowserState> = _state

    private val _events = Channel<BrowserEvent>(Channel.UNLIMITED)
    val events: Flow<BrowserEvent> = _events.receiveAsFlow()

    private var loadJob: Job? = null

    init {
        navigate("/")
    }

    override fun onCleared() {
        scope.cancel()
    }

    // ---- navigation ------------------------------------------------------------

    fun navigate(path: String) {
        _state.update { it.copy(path = path, selection = emptySet()) }
        refresh()
    }

    /** Goes to the parent folder; false when already at the root. */
    fun up(): Boolean {
        val p = _state.value.path.trimEnd('/')
        if (p.isEmpty()) return false
        navigate(p.substringBeforeLast('/').ifEmpty { "/" })
        return true
    }

    /**
     * Reloads the current folder. A cached source answers from memory or its encrypted disk layer;
     * a listing it only knows as stale is shown at once while the fresh one is fetched. [force] (the
     * Refresh menu item) makes it ask the vault again, for changes made by other clients.
     */
    fun refresh(force: Boolean = false) {
        val path = _state.value.path
        val cached = source as? CachingSource
        loadJob?.cancel()
        _state.update { it.copy(loading = true, error = null, locked = false) }
        loadJob = scope.launch {
            try {
                if (force) {
                    cached?.invalidateListing(path)
                } else {
                    cached?.peekListing(path)?.let { known ->
                        val stale = known.sortedWith(ORDER)
                        _state.update { if (it.path == path) it.copy(items = stale) else it }
                    }
                }
                val items = source.list(path).sortedWith(ORDER)
                _state.update { if (it.path == path) it.copy(items = items, loading = false) else it }
            } catch (e: CancellationException) {
                throw e
            } catch (e: SourceError.Locked) {
                _state.update { it.copy(loading = false, locked = true) }
                _events.trySend(BrowserEvent.NavigateToUnlock)
            } catch (e: Exception) {
                _state.update { it.copy(loading = false, error = e.message ?: "Could not open the folder") }
            }
        }
    }

    // ---- selection -------------------------------------------------------------

    fun toggle(item: Item) = _state.update {
        it.copy(selection = if (item.path in it.selection) it.selection - item.path else it.selection + item.path)
    }

    fun clearSelection() = _state.update { it.copy(selection = emptySet()) }

    fun selectAll() = _state.update { s -> s.copy(selection = s.items.mapTo(HashSet()) { it.path }) }

    /** Makes [item] the whole selection, so the viewer can run the selection actions on one file. */
    fun selectOnly(item: Item) = _state.update { it.copy(selection = setOf(item.path)) }

    private fun selected(): List<Item> = _state.value.let { s -> s.items.filter { it.path in s.selection } }

    // ---- save offline ----------------------------------------------------------

    fun saveSelectedOffline() {
        if (!_state.value.canSaveOffline) return
        val items = selected()
        if (items.isEmpty()) return
        val into = offline.source()
        if (into == null) {
            _events.trySend(BrowserEvent.NeedOfflineVault(offline.exists()))
            return
        }
        guarded("Saving offline") {
            try {
                val r = saveOffline(source, items, into, savePrefix)
                _state.update { it.copy(selection = emptySet(), result = OpResult("Saved offline", r.saved, r.failed, skipped = r.skipped)) }
            } catch (e: SaveInterrupted) {
                val locked = e.reason is SourceError.Locked
                val note = if (locked) "The vault was locked part-way. Unlock it and save the rest again."
                else "Stopped: ${e.reason.message}."
                _state.update {
                    it.copy(selection = emptySet(), result = OpResult("Saved offline", e.partial.saved, e.partial.failed, note, unlockAfter = locked, skipped = e.partial.skipped))
                }
            }
        }
    }

    // ---- delete ----------------------------------------------------------------

    fun requestDelete() {
        val items = selected()
        if (items.isNotEmpty()) _state.update { it.copy(confirmDelete = items) }
    }

    fun cancelDelete() = _state.update { it.copy(confirmDelete = null) }

    fun confirmDelete() {
        val items = _state.value.confirmDelete ?: return
        _state.update { it.copy(confirmDelete = null) }
        guarded("Deleting") {
            val failed = LinkedHashMap<String, String>()
            val ok = ArrayList<String>()
            for (i in items) {
                try {
                    source.delete(i.path)
                    ok += i.name
                } catch (e: CancellationException) {
                    throw e
                } catch (e: SourceError.Locked) {
                    throw e
                } catch (e: Exception) {
                    failed[i.name] = e.message ?: e.toString()
                }
            }
            _state.update { it.copy(selection = emptySet(), result = if (failed.isEmpty()) null else OpResult("Delete", ok, failed)) }
            refresh()
        }
    }

    // ---- upload / new folder ---------------------------------------------------

    fun upload(specs: List<UploadSpec>) {
        if (specs.isEmpty()) return
        val dir = _state.value.path
        guarded("Uploading") {
            val ok = ArrayList<String>()
            val failed = LinkedHashMap<String, String>()
            var locked = false
            try {
                for (s in specs) {
                    try {
                        val input = s.open() ?: throw SourceError.Other("could not read the file")
                        source.upload(dir, s.name, input, s.size)
                        ok += s.name
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: SourceError.Locked) {
                        locked = true
                        throw e
                    } catch (e: Exception) {
                        failed[s.name] = e.message ?: e.toString()
                    }
                }
            } finally {
                val note = if (locked) "The vault was locked. Unlock it and upload the rest again." else null
                _state.update { it.copy(result = OpResult("Upload", ok, failed, note, unlockAfter = locked)) }
                if (!locked) refresh()
            }
        }
    }

    fun newFolder(name: String) {
        val n = name.trim()
        if (n.isEmpty() || '/' in n) {
            _state.update { it.copy(error = "Folder names cannot be empty or contain /") }
            return
        }
        val path = _state.value.path.trimEnd('/') + "/" + n
        guarded("Creating folder") {
            source.mkdir(path)
            refresh()
        }
    }

    // ---- move ------------------------------------------------------------------

    fun startMove() {
        if (selected().isEmpty()) return
        moveNavigate(_state.value.path)
    }

    fun moveNavigate(path: String) {
        _state.update { it.copy(move = MoveState(path)) }
        val skip = _state.value.selection
        guarded(null) {
            val folders = source.list(path).filter { it.isDir && it.path !in skip }.sortedWith(ORDER)
            _state.update { s -> if (s.move?.path == path) s.copy(move = MoveState(path, folders, loading = false)) else s }
        }
    }

    fun cancelMove() = _state.update { it.copy(move = null) }

    fun confirmMove() {
        val dest = _state.value.move?.path ?: return
        val items = selected()
        _state.update { it.copy(move = null) }
        guarded("Moving") {
            val ok = ArrayList<String>()
            val failed = LinkedHashMap<String, String>()
            for (i in items) {
                try {
                    source.move(i.path, dest.trimEnd('/') + "/" + i.name)
                    ok += i.name
                } catch (e: CancellationException) {
                    throw e
                } catch (e: SourceError.Locked) {
                    throw e
                } catch (e: Exception) {
                    failed[i.name] = e.message ?: e.toString()
                }
            }
            _state.update { it.copy(selection = emptySet(), result = OpResult("Move", ok, failed)) }
            refresh()
        }
    }

    // ---- results ---------------------------------------------------------------

    fun dismissResult() {
        val r = _state.value.result ?: return
        _state.update { it.copy(result = null) }
        if (r.unlockAfter) _events.trySend(BrowserEvent.NavigateToUnlock)
    }

    fun dismissError() = _state.update { it.copy(error = null) }

    /** Runs [block] with a busy label; Locked becomes the unlock event, anything else an error banner. */
    private fun guarded(label: String?, block: suspend () -> Unit) {
        scope.launch {
            if (label != null) _state.update { it.copy(busy = label) }
            try {
                block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: SourceError.Locked) {
                _state.update { it.copy(locked = true) }
                // A result that carries its own unlock step is shown first.
                if (_state.value.result?.unlockAfter != true) _events.trySend(BrowserEvent.NavigateToUnlock)
            } catch (e: Exception) {
                _state.update { it.copy(error = e.message ?: e.toString()) }
            } finally {
                if (label != null) _state.update { it.copy(busy = null) }
            }
        }
    }
}
