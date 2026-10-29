package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.FakeLocalVault
import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.media.VaultUri
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.InputStream

private fun file(dir: String, name: String, size: Long = 10) =
    Item(if (dir == "/") "/$name" else "$dir/$name", name, false, size, if (name.endsWith(".jpg")) MediaKind.IMAGE else MediaKind.OTHER)

private fun folder(dir: String, name: String) = Item(if (dir == "/") "/$name" else "$dir/$name", name, true, 0, MediaKind.OTHER)

/** Mutable in-memory vault that records what the view model asks of it. */
private class MemSource : VaultSource {
    val tree = HashMap<String, MutableList<Item>>()
    val data = HashMap<String, ByteArray>()
    var listError: SourceError? = null
    var listCalls = 0
    val deleted = ArrayList<String>()
    val moved = ArrayList<Pair<String, String>>()
    val made = ArrayList<String>()
    val uploaded = ArrayList<Triple<String, String, Long>>()
    var failUploads = emptyMap<String, SourceError>()
    var openError: Map<String, SourceError> = emptyMap()

    override suspend fun list(path: String): List<Item> {
        listCalls++
        listError?.let { throw it }
        return tree[path]?.toList() ?: emptyList()
    }
    override suspend fun open(path: String, range: LongRange?): InputStream {
        openError[path]?.let { throw it }
        return ByteArrayInputStream(data[path] ?: throw SourceError.NotFound)
    }
    override suspend fun size(path: String) = data[path]?.size?.toLong() ?: throw SourceError.NotFound
    override suspend fun thumbnail(path: String, px: Int) = ByteArray(0)
    override suspend fun upload(dir: String, name: String, input: InputStream, length: Long) {
        input.use { it.readBytes() }
        failUploads[name]?.let { throw it }
        uploaded += Triple(dir, name, length)
    }
    override suspend fun mkdir(path: String) { made += path }
    override suspend fun move(from: String, to: String) { moved += from to to }
    override suspend fun delete(path: String) {
        deleted += path
        tree.values.forEach { l -> l.removeAll { it.path == path } }
    }
}

private class Offline(var lsrc: LocalSource?, val exists: Boolean = true) : OfflineAccess {
    override fun exists() = exists
    override fun source() = lsrc
}

@OptIn(ExperimentalCoroutinesApi::class)
class BrowserViewModelTest {
    private fun TestScope.vm(
        src: VaultSource,
        offline: OfflineAccess = Offline(LocalSource(FakeLocalVault())),
        sourceId: String = "srv:v1",
    ) = BrowserViewModel(
        sourceId = sourceId,
        source = src,
        rootLabel = "Trips",
        savePrefix = "My server/Trips",
        offline = offline,
        scope = CoroutineScope(SupervisorJob() + UnconfinedTestDispatcher(testScheduler)),
    )

    private fun memTrip() = MemSource().apply {
        tree["/"] = mutableListOf(file("/", "b.jpg"), folder("/", "Zed"), file("/", "a.jpg"), folder("/", "Alpha"))
        tree["/Alpha"] = mutableListOf(file("/Alpha", "c.jpg"))
        data["/a.jpg"] = ByteArray(10) { 1 }
        data["/b.jpg"] = ByteArray(10) { 2 }
        data["/Alpha/c.jpg"] = ByteArray(10) { 3 }
    }

    @Test fun listsFoldersFirstThenNames() = runTest {
        val v = vm(memTrip())
        val s = v.state.first { !it.loading }
        assertEquals(listOf("Alpha", "Zed", "a.jpg", "b.jpg"), s.items.map { it.name })
        assertEquals("/", s.path)
    }

    @Test fun openingAFolderChangesPathAndClearsSelection() = runTest {
        val v = vm(memTrip())
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg"))
        assertEquals(setOf("/a.jpg"), v.state.value.selection)
        v.navigate("/Alpha")
        val s = v.state.first { !it.loading && it.path == "/Alpha" }
        assertEquals(listOf("c.jpg"), s.items.map { it.name })
        assertTrue(s.selection.isEmpty())
        assertTrue(v.up())
        assertEquals("/", v.state.first { !it.loading && it.path == "/" }.path)
        assertFalse(v.up())
    }

    @Test fun selectionToggleAndClear() = runTest {
        val v = vm(memTrip())
        v.state.first { !it.loading }
        val a = file("/", "a.jpg"); val b = file("/", "b.jpg")
        v.toggle(a); v.toggle(b)
        assertEquals(setOf("/a.jpg", "/b.jpg"), v.state.value.selection)
        v.toggle(a)
        assertEquals(setOf("/b.jpg"), v.state.value.selection)
        v.clearSelection()
        assertTrue(v.state.value.selection.isEmpty())
    }

    @Test fun selectAllAndSelectOnly() = runTest {
        val v = vm(memTrip())
        v.state.first { !it.loading }
        v.selectAll()
        assertEquals(setOf("/Alpha", "/Zed", "/a.jpg", "/b.jpg"), v.state.value.selection)
        v.selectOnly(file("/", "b.jpg"))
        assertEquals(setOf("/b.jpg"), v.state.value.selection)
    }

    @Test fun deleteFromTheViewerRemovesOnlyThatFile() = runTest {
        val src = memTrip()
        val v = vm(src)
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg"))
        v.selectOnly(file("/", "b.jpg"))
        v.requestDelete()
        assertEquals(listOf("b.jpg"), v.state.value.confirmDelete?.map { it.name })
        v.confirmDelete()
        assertEquals(listOf("/b.jpg"), src.deleted)
        val s = v.state.first { !it.loading && it.items.none { i -> i.name == "b.jpg" } }
        assertTrue(s.selection.isEmpty())
    }

    @Test fun saveOfflineProducesPerFileReport() = runTest {
        val src = memTrip()
        src.openError = mapOf("/b.jpg" to SourceError.Other("disk gremlins"))
        val v = vm(src)
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg")); v.toggle(file("/", "b.jpg")); v.toggle(folder("/", "Alpha"))
        assertTrue(v.state.value.canSaveOffline)
        v.saveSelectedOffline()
        val r = v.state.first { it.result != null }.result!!
        assertEquals(setOf("My server/Trips/a.jpg", "My server/Trips/Alpha/c.jpg"), r.ok.toSet())
        assertEquals(mapOf("My server/Trips/b.jpg" to "disk gremlins"), r.failed)
        assertFalse(r.unlockAfter)
        assertTrue(v.state.value.selection.isEmpty())
    }

    @Test fun saveOfflineIsHiddenAndInertInTheOfflineVault() = runTest {
        val v = vm(memTrip(), sourceId = OFFLINE_ID)
        v.state.first { !it.loading }
        assertFalse(v.state.value.canSaveOffline)
        v.toggle(file("/", "a.jpg"))
        v.saveSelectedOffline()
        assertNull(v.state.value.result)
    }

    @Test fun saveOfflineWithoutAnOpenOfflineVaultAsksForUnlockAndKeepsSelection() = runTest {
        val v = vm(memTrip(), offline = Offline(null, exists = false))
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg"))
        v.saveSelectedOffline()
        assertEquals(BrowserEvent.NeedOfflineVault(exists = false), v.events.first())
        assertEquals(setOf("/a.jpg"), v.state.value.selection)
        assertNull(v.state.value.result)
    }

    @Test fun lockedMidSaveShowsPartialReportThenNavigatesToUnlock() = runTest {
        val src = memTrip()
        src.openError = mapOf("/b.jpg" to SourceError.Locked)
        val v = vm(src)
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg")); v.toggle(file("/", "b.jpg"))
        v.saveSelectedOffline()
        val r = v.state.first { it.result != null }.result!!
        assertEquals(listOf("My server/Trips/a.jpg"), r.ok)
        assertTrue(r.unlockAfter)
        assertNotNull(r.note)
        v.dismissResult()
        assertNull(v.state.value.result)
        assertEquals(BrowserEvent.NavigateToUnlock, v.events.first())
    }

    @Test fun deleteAsksForConfirmationThenRefreshes() = runTest {
        val src = memTrip()
        val v = vm(src)
        v.state.first { !it.loading }
        val a = file("/", "a.jpg")
        v.toggle(a)
        v.requestDelete()
        assertEquals(listOf(a), v.state.value.confirmDelete)
        assertTrue(src.deleted.isEmpty())
        val before = src.listCalls
        v.confirmDelete()
        val s = v.state.first { it.confirmDelete == null && !it.loading && it.items.none { i -> i.name == "a.jpg" } }
        assertEquals(listOf("/a.jpg"), src.deleted)
        assertTrue(src.listCalls > before)
        assertTrue(s.selection.isEmpty())
    }

    @Test fun cancellingDeleteDeletesNothing() = runTest {
        val src = memTrip()
        val v = vm(src)
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg"))
        v.requestDelete()
        v.cancelDelete()
        assertNull(v.state.value.confirmDelete)
        assertTrue(src.deleted.isEmpty())
        assertEquals(setOf("/a.jpg"), v.state.value.selection)
    }

    @Test fun lockedWhileListingNavigatesToUnlockInsteadOfShowingAnEmptyFolder() = runTest {
        val src = memTrip()
        src.listError = SourceError.Locked
        val v = vm(src)
        assertEquals(BrowserEvent.NavigateToUnlock, v.events.first())
        val s = v.state.value
        assertNull(s.error)
        assertTrue(s.locked)
    }

    @Test fun otherListErrorsAreShownAndRetryable() = runTest {
        val src = memTrip()
        src.listError = SourceError.Other("server down")
        val v = vm(src)
        assertEquals("server down", v.state.first { !it.loading }.error)
        src.listError = null
        v.refresh()
        val s = v.state.first { !it.loading && it.error == null }
        assertEquals(4, s.items.size)
    }

    @Test fun uploadListsFailuresPerFile() = runTest {
        val src = memTrip()
        src.failUploads = mapOf("bad.jpg" to SourceError.Exists)
        val v = vm(src)
        v.state.first { !it.loading }
        v.upload(listOf(
            UploadSpec("ok.jpg", 3) { ByteArrayInputStream(byteArrayOf(1, 2, 3)) },
            UploadSpec("bad.jpg", 2) { ByteArrayInputStream(byteArrayOf(1, 2)) },
            UploadSpec("gone.jpg", 2) { null },
        ))
        val r = v.state.first { it.result != null }.result!!
        assertEquals(listOf("ok.jpg"), r.ok)
        assertEquals(setOf("bad.jpg", "gone.jpg"), r.failed.keys)
        assertEquals(listOf(Triple("/", "ok.jpg", 3L)), src.uploaded)
    }

    @Test fun lockedUploadStopsAndOffersUnlock() = runTest {
        val src = memTrip()
        src.failUploads = mapOf("one.jpg" to SourceError.Locked)
        val v = vm(src)
        v.state.first { !it.loading }
        v.upload(listOf(UploadSpec("one.jpg", 1) { ByteArrayInputStream(byteArrayOf(1)) }, UploadSpec("two.jpg", 1) { ByteArrayInputStream(byteArrayOf(1)) }))
        val r = v.state.first { it.result != null }.result!!
        assertTrue(r.unlockAfter)
        assertTrue(src.uploaded.isEmpty())
    }

    @Test fun newFolderCreatesAndRefreshes() = runTest {
        val src = memTrip()
        val v = vm(src)
        v.state.first { !it.loading }
        v.newFolder("Fresh")
        v.state.first { !it.loading && src.made.isNotEmpty() }
        assertEquals(listOf("/Fresh"), src.made)
    }

    @Test fun moveGoesToAFolderPickedWithinTheSameVault() = runTest {
        val src = memTrip()
        val v = vm(src)
        v.state.first { !it.loading }
        v.toggle(file("/", "a.jpg"))
        v.toggle(folder("/", "Zed"))
        v.startMove()
        val m0 = v.state.first { it.move != null && !it.move!!.loading }.move!!
        assertEquals("/", m0.path)
        // The folder being moved is not offered as a destination.
        assertEquals(listOf("Alpha"), m0.folders.map { it.name })
        v.moveNavigate("/Alpha")
        assertEquals("/Alpha", v.state.first { it.move?.path == "/Alpha" && !it.move!!.loading }.move!!.path)
        v.confirmMove()
        val s = v.state.first { it.move == null && it.result != null }
        assertEquals(setOf("/a.jpg" to "/Alpha/a.jpg", "/Zed" to "/Alpha/Zed"), src.moved.toSet())
        assertTrue(s.selection.isEmpty())
    }

    @Test fun vaultUriRoundTripsAwkwardIdsAndPaths() {
        val u = VaultUri.build("srv key:v/1", "/Trip 2024/a b+c#d?.jpg", 256)
        assertTrue(u.startsWith("vault://"))
        val p = VaultUri.parse(u)!!
        assertEquals("srv key:v/1", p.sourceId)
        assertEquals("/Trip 2024/a b+c#d?.jpg", p.path)
        assertEquals(256, p.px)
        assertNull(VaultUri.parse("https://example.com/x"))
    }
}
