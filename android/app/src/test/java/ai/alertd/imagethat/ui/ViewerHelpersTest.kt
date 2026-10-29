package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.MediaKind
import androidx.compose.ui.unit.IntSize
import androidx.lifecycle.Lifecycle
import androidx.media3.common.Player
import java.lang.reflect.Proxy
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ViewerHelpersTest {
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
