package ai.alertd.imagethat.ui

import ai.alertd.imagethat.keys.KeyVault
import ai.alertd.imagethat.local.LocalVaults
import android.content.Context
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File

/** A Session over private temp state so tests never touch the app's real state file or offline vault. */
class TestFixture(
    var now: Long = 0L,
    /** What the app lock reports for "a biometric or screen lock is set up". */
    var appLockAvailable: Boolean = true,
    idleMs: Long = 5 * 60_000L,
    backgroundMs: Long = 60_000L,
) {
    val context: Context = InstrumentationRegistry.getInstrumentation().targetContext
    val dir = File(context.cacheDir, "ui-fixture-${System.nanoTime()}").also { it.mkdirs() }
    /** Its own Keystore alias, so tests never touch the app's real master key. */
    val masterAlias = "imagethat.vault-master.test-${System.nanoTime()}"
    /** Replaces the biometric prompt; set [cancelPrompt] to simulate the user cancelling. */
    var cancelPrompt = false
    var prompts = 0
    // requireAuth = false: emulators have no enrolled biometrics, so the master key needs no prompt.
    val keys = KeyVault(
        context, requireAuth = false, stateFile = File(dir, "state.bin"), masterAlias = masterAlias,
        authenticator = { _, c -> prompts++; if (cancelPrompt) null else c },
    )
    val local = LocalVaults(context, keys.store(), File(dir, "offline"))
    var imageCacheClears = 0
    val alarm = FakeAlarm()
    val session = Session(
        keys, local,
        clock = { now },
        clearImageCache = { imageCacheClears++ },
        appLockAvailable = { appLockAvailable },
        idleTimeoutMs = idleMs,
        backgroundTimeoutMs = backgroundMs,
        alarm = alarm,
    )

    /** Passes the app lock as the lock screen would (no prompt on an emulator). */
    fun unlockApp() = check(kotlinx.coroutines.runBlocking { session.unlockApp(null) })

    fun cleanup() {
        session.lockAll()
        keys.clearSavedPasswords()
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
