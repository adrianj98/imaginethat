package ai.alertd.imagethat.ui

import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MobileVaultApi
import ai.alertd.imagethat.AutoLockReceiver
import ai.alertd.imagethat.ImageThatApp
import android.content.Intent
import androidx.test.platform.app.InstrumentationRegistry
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.flow.first
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
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

    @Test fun aVaultTrackedByLocalVaultsButNotRegisteredStillLocksOnIdle() {
        fx.local.openOffline(TestFixture.PASSWORD.toCharArray())
        assertTrue(fx.local.hasOpen())
        fx.now += 5 * 60_000
        session.tick()
        assertTrue(!fx.local.hasOpen())
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

    // ---- background alarm (frozen process) ----------------------------------

    @Test fun backgroundArmsTheAlarmAndForegroundCancelsIt() {
        openOffline()
        session.onBackground()
        assertEquals(60_000L, fx.alarm.pendingDelayMs)
        fx.now += 10_000
        session.onForeground()
        assertNull(fx.alarm.pendingDelayMs)
        assertEquals(setOf("offline"), open)
    }

    @Test fun alarmWhileBackgroundedLocksEverything() {
        openOffline()
        session.onBackground()
        fx.now += 60_000 // the frozen process never ticked
        session.onAlarm()
        assertTrue(open.isEmpty())
        assertTrue(!fx.local.hasOpen())
        assertEquals(1, fx.imageCacheClears)
    }

    @Test fun staleAlarmAfterReturningDoesNotLock() {
        openOffline()
        session.onBackground()
        session.onForeground()
        session.onAlarm()
        assertEquals(setOf("offline"), open)
    }

    /** The real receiver against the app's own Session, as AlarmManager would deliver it. */
    @Test fun autoLockReceiverLocksTheAppSession() {
        val app = fx.context.applicationContext as ImageThatApp
        val appSession = app.container.session
        val v = fx.local.openOffline(TestFixture.PASSWORD.toCharArray())
        InstrumentationRegistry.getInstrumentation().runOnMainSync {
            appSession.registerSource("receiver-test", "Test", LocalSource(MobileVaultApi(v)))
            appSession.onBackground()
            AutoLockReceiver().onReceive(app, Intent(app, AutoLockReceiver::class.java))
            assertTrue(appSession.openSourceIds.value.isEmpty())
            appSession.onForeground() // cancels the real alarm it armed
        }
    }

    // ---- picker pauses the background lock ----------------------------------

    @Test fun pickerOpenPausesTheBackgroundLockButNotTheIdleTimer() {
        openOffline()
        session.pauseBackgroundLock()
        session.onBackground()
        // No 1 minute alarm: only one at the idle deadline (5 minutes after the last touch).
        assertEquals(5 * 60_000L, fx.alarm.pendingDelayMs)
        fx.now += 2 * 60_000
        session.tick()
        session.onAlarm()
        assertEquals(setOf("offline"), open)
        fx.now += 3 * 60_000
        session.onAlarm()
        assertTrue(open.isEmpty())
    }

    @Test fun pickerResultAfterTwoMinutesKeepsTheBrowserOpen() {
        openOffline()
        session.pauseBackgroundLock()
        session.onBackground()
        fx.now += 2 * 60_000
        session.onForeground()
        session.resumeBackgroundLock()
        assertEquals(setOf("offline"), open)
        assertNull(fx.alarm.pendingDelayMs)
    }

    @Test fun resumingWhileStillBackgroundedStartsTheBackgroundDeadline() {
        openOffline()
        session.pauseBackgroundLock()
        session.onBackground()
        fx.now += 2 * 60_000
        session.resumeBackgroundLock()
        assertEquals(60_000L, fx.alarm.pendingDelayMs)
        fx.now += 59_000
        session.tick()
        assertEquals(setOf("offline"), open)
        fx.now += 1_000
        session.tick()
        assertTrue(open.isEmpty())
    }

    // ---- configurable idle timeout ------------------------------------------

    @Test fun idleTimeoutIsConfigurableAndSaved() {
        assertEquals(5, fx.keys.state().idleTimeoutMinutes)
        session.setIdleTimeoutMinutes(1)
        assertEquals(1, fx.keys.state().idleTimeoutMinutes)
        openOffline()
        fx.now += 60_000
        session.tick()
        assertTrue(open.isEmpty())

        session.setIdleTimeoutMinutes(30)
        val fresh = TestFixture().also { it.keys.store().update { st -> st.copy(idleTimeoutMinutes = 15) } }
        try {
            fresh.session.loadIdleTimeout()
            assertEquals(15, fresh.session.idleTimeoutMinutes)
        } finally {
            fresh.cleanup()
        }
    }

    @Test fun reportedLockEvictsThumbnailsFromTheMemoryCache() {
        openOffline()
        session.reportLocked("offline")
        assertEquals(1, fx.imageCacheClears)
    }
}
