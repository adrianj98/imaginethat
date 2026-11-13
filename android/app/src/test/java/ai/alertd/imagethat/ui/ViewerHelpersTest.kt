package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.MediaKind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.unit.IntSize
import androidx.lifecycle.Lifecycle
import androidx.media3.common.Player
import java.lang.reflect.Proxy
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ViewerHelpersTest {
    private val page = IntSize(800, 600)

    @Test fun zoomStaysBetweenFitAndTheMaximum() {
        assertEquals(6f, Zoom().transformed(100f, Offset.Zero, page).scale)
        assertEquals(Zoom(), Zoom(2f, Offset(100f, 50f)).transformed(0.001f, Offset.Zero, page))
    }

    @Test fun panOnlyWhileZoomedAndNeverOffThePage() {
        assertEquals(Zoom(), Zoom().transformed(1f, Offset(50f, 50f), page))
        assertEquals(Offset(100f, -50f), Zoom(2f).transformed(1f, Offset(100f, -50f), page).offset)
        assertEquals(Offset(400f, -300f), Zoom(2f).transformed(1f, Offset(5000f, -5000f), page).offset)
        assertEquals(Zoom(1.5f, Offset(200f, -150f)), Zoom(2f, Offset(400f, -300f)).transformed(0.75f, Offset.Zero, page))
    }

    @Test fun doubleTapZoomsInOnThePointAndBack() {
        val z = Zoom().toggled(Offset(100f, -60f), page)
        assertEquals(Zoom(2.5f, Offset(-150f, 90f)), z)
        assertEquals(Zoom(2.5f, Offset(-600f, -450f)), Zoom().toggled(Offset(400f, 300f), page))
        assertEquals(Zoom(), z.toggled(Offset.Zero, page))
    }

    @Test fun sampleSizeKeepsLongEdgeAtOrBelow2048() {
        assertEquals(1, decodeSampleSize(2048, 1000))
        assertEquals(2, decodeSampleSize(2049, 100))
        assertEquals(2, decodeSampleSize(4000, 3000))
        assertEquals(2, decodeSampleSize(4095, 10))
        assertEquals(2, decodeSampleSize(4096, 10))
        assertEquals(4, decodeSampleSize(4100, 10))
        assertEquals(4, decodeSampleSize(8000, 6000))
        for (edge in listOf(1, 500, 2047, 4095, 4096, 4097, 12000, 20000)) {
            assertTrue(edge / decodeSampleSize(edge, 1) <= 2048)
        }
    }

    private fun fakePlayer(calls: MutableList<String>): Player =
        Proxy.newProxyInstance(Player::class.java.classLoader, arrayOf(Player::class.java)) { _, m, _ ->
            calls += m.name
            null
        } as Player

    @Test fun playbackPausesOnStopOnly() {
        val calls = ArrayList<String>()
        val p = fakePlayer(calls)
        for (e in listOf(Lifecycle.Event.ON_CREATE, Lifecycle.Event.ON_START, Lifecycle.Event.ON_RESUME, Lifecycle.Event.ON_PAUSE)) pauseOnStop(p, e)
        assertTrue(calls.isEmpty())
        pauseOnStop(p, Lifecycle.Event.ON_STOP)
        assertEquals(listOf("pause"), calls)
        pauseOnStop(p, Lifecycle.Event.ON_START)
        assertEquals(listOf("pause"), calls) // no auto-resume
    }

    @Test fun detailsLineLeavesOutWhatIsUnknown() {
        val img = Item("/a.jpg", "a.jpg", false, 3_984_588, MediaKind.IMAGE)
        assertEquals("4032 × 3024 · 3.8 MB", details(img, IntSize(4032, 3024)))
        assertEquals("3.8 MB", details(img, null))
        assertEquals("", details(img.copy(size = 0), null))
    }

    @Test fun fileTypeNamesExtensionAndKind() {
        assertEquals("JPG image", fileType(Item("/a.jpg", "a.jpg", false, 1, MediaKind.IMAGE)))
        assertEquals("MP4 video", fileType(Item("/c.mp4", "c.mp4", false, 1, MediaKind.VIDEO)))
        assertEquals("File", fileType(Item("/README", "README", false, 1, MediaKind.OTHER)))
    }
}
