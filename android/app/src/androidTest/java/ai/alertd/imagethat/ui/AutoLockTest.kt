package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.flow.first
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class AutoLockTest {
    private val fx = TestFixture()
    private val session get() = fx.session

    @Before fun setUp() {
        fx.local.createOffline(TestFixture.PASSWORD.toCharArray())
        fx.now = 1_000
        session.touch()
    }

    @After fun tearDown() = fx.cleanup()

    private fun openOffline() {
        val v = fx.local.openOffline(TestFixture.PASSWORD.toCharArray())
        session.registerSource("offline", "Offline vault", LocalSource(MobileVaultApi(v)))
    }

    private val open get() = session.openSourceIds.value

    @Test fun idleForFiveMinutesLocksEverything() {
        openOffline()
        assertEquals(setOf("offline"), open)
        fx.now += 5 * 60_000 - 1
        session.tick()
        assertEquals(setOf("offline"), open)
        fx.now += 1
        session.tick()
        assertTrue(open.isEmpty())
        assertEquals(1, fx.imageCacheClears)
    }

    @Test fun touchResetsTheIdleTimer() {
        openOffline()
        fx.now += 4 * 60_000
        session.touch()
        fx.now += 4 * 60_000
        session.tick()
        assertEquals(setOf("offline"), open)
        fx.now += 60_000
        session.tick()
        assertTrue(open.isEmpty())
    }

    @Test fun backgroundForOneMinuteLocks() {
        openOffline()
        session.onBackground()
        fx.now += 59_000
        session.onForeground()
        assertEquals(setOf("offline"), open)

        session.onBackground()
        fx.now += 60_000
        session.tick()
        assertTrue(open.isEmpty())
    }

    @Test fun returningAfterLongBackgroundLocksEvenWithoutATick() {
        openOffline()
        session.onBackground()
        fx.now += 10 * 60_000
        session.onForeground()
        assertTrue(open.isEmpty())
    }

    @Test fun lockAllAnnouncesItself() = runBlocking {
        openOffline()
        val seen = async(start = CoroutineStart.UNDISPATCHED) { withTimeout(5_000) { session.events.first() } }
        session.lockAll()
        assertEquals(SessionEvent.AllLocked, seen.await())
        assertTrue(open.isEmpty())
    }

    @Test fun reportedLockDropsOnlyThatSource() = runBlocking {
        openOffline()
        val seen = async(start = CoroutineStart.UNDISPATCHED) { withTimeout(5_000) { session.events.first() } }
        session.reportLocked("offline")
        assertEquals(SessionEvent.SourceLocked("offline"), seen.await())
        assertTrue(open.isEmpty())
    }
}
