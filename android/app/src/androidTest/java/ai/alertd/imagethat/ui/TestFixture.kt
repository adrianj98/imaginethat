package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.KeyVault
import ai.alertd.imagethat.local.LocalVaults
import android.content.Context
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/** A Session over private temp state so tests never touch the app's real state file or offline vault. */
class TestFixture(
    var now: Long = 0L,
    biometrics: Boolean = true,
    idleMs: Long = 5 * 60_000L,
    backgroundMs: Long = 60_000L,
) {
    val context: Context = InstrumentationRegistry.getInstrumentation().targetContext
    val dir = File(context.cacheDir, "ui-fixture-${System.nanoTime()}").also { it.mkdirs() }
    // requireAuth = false: emulators have no enrolled biometrics, so wrap/unwrap skip the prompt.
    val keys = KeyVault(context, requireAuth = false, stateFile = File(dir, "state.bin"))
    val local = LocalVaults(context, keys.store(), File(dir, "offline"))
    var imageCacheClears = 0
    val alarm = FakeAlarm()
    val session = Session(
        keys, local,
        clock = { now },
        clearImageCache = { imageCacheClears++ },
        biometricsAvailable = { biometrics },
        idleTimeoutMs = idleMs,
        backgroundTimeoutMs = backgroundMs,
        alarm = alarm,
    )

    fun cleanup() {
        session.lockAll()
        for (id in listOf("offline")) keys.forget(id)
        dir.deleteRecursively()
    }

    /** Records the alarm the Session arms; null when none is pending. */
    class FakeAlarm : LockAlarm {
        var pendingDelayMs: Long? = null
        override fun schedule(delayMs: Long) { pendingDelayMs = delayMs }
        override fun cancel() { pendingDelayMs = null }
    }

    companion object {
        const val PASSWORD = "correct-horse"
    }
}
