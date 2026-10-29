package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.CachingSource
import ai.alertd.imagethat.data.CountingSource
import ai.alertd.imagethat.data.FakeLocalVault
import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MediaKind
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test

private class OpenOffline : OfflineAccess {
    override fun exists() = true
    override fun source() = LocalSource(FakeLocalVault())
}

/** The browser over a [CachingSource]: moving around a vault does not list the same folder twice. */
@OptIn(ExperimentalCoroutinesApi::class)
class BrowserCachingTest {
    private fun tree() = CountingSource().apply {
        tree["/"] = mutableListOf(
            Item("/a.jpg", "a.jpg", false, 10, MediaKind.IMAGE),
            Item("/Alpha", "Alpha", true, 0, MediaKind.OTHER),
        )
        tree["/Alpha"] = mutableListOf(Item("/Alpha/c.jpg", "c.jpg", false, 10, MediaKind.IMAGE))
    }

    private fun TestScope.vm(src: CachingSource) = BrowserViewModel(
        sourceId = "srv:v1",
        source = src,
        rootLabel = "Trips",
        savePrefix = "My server/Trips",
        offline = OpenOffline(),
        scope = CoroutineScope(SupervisorJob() + UnconfinedTestDispatcher(testScheduler)),
    )

    @Test fun goingBackAndForthListsEachFolderOnce() = runTest {
        val src = tree()
        val v = vm(CachingSource(src))
        v.state.first { !it.loading }
        v.navigate("/Alpha")
        v.state.first { !it.loading && it.path == "/Alpha" }
        v.up()
        v.state.first { !it.loading && it.path == "/" }
        v.navigate("/Alpha")
        v.state.first { !it.loading && it.path == "/Alpha" }
        assertEquals(2, src.lists)
        assertEquals(listOf("c.jpg"), v.state.value.items.map { it.name })
    }

    @Test fun refreshAsksTheVaultAgainOnlyWhenForced() = runTest {
        val src = tree()
        val v = vm(CachingSource(src))
        v.state.first { !it.loading }
        v.refresh()
        v.state.first { !it.loading }
        assertEquals(1, src.lists)
        src.tree["/"]!!.add(Item("/new.jpg", "new.jpg", false, 10, MediaKind.IMAGE))
        v.refresh(force = true)
        val s = v.state.first { !it.loading }
        assertEquals(2, src.lists)
        assertEquals(listOf("Alpha", "a.jpg", "new.jpg"), s.items.map { it.name })
    }

    @Test fun aChangeMadeHereShowsUpWithoutAForcedRefresh() = runTest {
        val src = tree()
        val v = vm(CachingSource(src))
        v.state.first { !it.loading }
        v.newFolder("Beta") // the fake's mkdir adds the folder; the wrapper forgets "/" so the reload sees it
        val s = v.state.first { !it.loading && it.busy == null && it.items.any { i -> i.name == "Beta" } }
        assertEquals(2, src.lists)
        assertEquals(listOf("Alpha", "Beta", "a.jpg"), s.items.map { it.name })
    }
}
