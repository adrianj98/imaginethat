package ai.alertd.imagethat.ui

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
}
