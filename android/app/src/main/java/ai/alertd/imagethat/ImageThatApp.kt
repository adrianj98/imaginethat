package ai.alertd.imagethat

import ai.alertd.imagethat.data.EncryptedDiskCache
import ai.alertd.imagethat.keys.KeyVault
import ai.alertd.imagethat.local.LocalVaults
import ai.alertd.imagethat.media.ThumbnailFetcher
import ai.alertd.imagethat.ui.Session
import android.app.Activity
import android.app.Application
import android.content.Context
import android.os.Bundle
import androidx.biometric.BiometricManager
import coil3.ImageLoader
import coil3.PlatformContext
import coil3.SingletonImageLoader
import coil3.memory.MemoryCache
import coil3.request.CachePolicy
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import mobile.Mobile
import java.io.File

/** The one KeyVault/StateStore and Session shared app-wide. */
class AppContainer(private val context: Context) {
    val keys = KeyVault(context)
    val local = LocalVaults(context, keys.store())
    // Ciphertext only, keyed per vault from its password (DiskCache.kt); the OS may clear cacheDir.
    val diskCache = EncryptedDiskCache(File(context.cacheDir, "vault-cache"))
    val session = Session(
        keys, local,
        clearImageCache = { SingletonImageLoader.get(context).memoryCache?.clear() },
        diskCache = diskCache,
        appLockAvailable = {
            BiometricManager.from(context).canAuthenticate(KeyVault.appLockAuthenticators()) == BiometricManager.BIOMETRIC_SUCCESS
        },
        alarm = AndroidLockAlarm(context),
    )
}

class ImageThatApp : Application(), SingletonImageLoader.Factory {
    val container by lazy { AppContainer(this) }

    override fun onCreate() {
        super.onCreate()
        // Go's default temp dir (/data/local/tmp) is not writable by apps.
        Mobile.setTempDir(cacheDir.absolutePath)
        startAutoLock()
        // The saved idle timeout; until it loads the 5 minute default applies (nothing is unlocked yet).
        CoroutineScope(SupervisorJob() + Dispatchers.IO).launch {
            runCatching { container.session.loadIdleTimeout() }
        }
    }

    // androidx.startup's provider is removed from the manifest (so ProcessLifecycleOwner cannot
    // initialise itself); the app is "in the background" when no activity is started.
    private fun startAutoLock() {
        val session = container.session
        registerActivityLifecycleCallbacks(object : ActivityLifecycleCallbacks {
            private var started = 0

            override fun onActivityStarted(activity: Activity) {
                if (started++ == 0) session.onForeground()
            }

            override fun onActivityStopped(activity: Activity) {
                // A rotation stops and restarts the activity within milliseconds; that is harmless
                // because the 1 minute deadline is measured from here.
                if (--started == 0) session.onBackground()
            }

            override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit
            override fun onActivityResumed(activity: Activity) = Unit
            override fun onActivityPaused(activity: Activity) = Unit
            override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit
            override fun onActivityDestroyed(activity: Activity) = Unit
        })
        // Deadlines are checked here while the process runs. A frozen background process is woken by
        // the Session's alarm (AutoLockReceiver), and onForeground is the backstop.
        CoroutineScope(SupervisorJob() + Dispatchers.Main).launch {
            while (true) {
                delay(TICK_MS)
                session.tick()
            }
        }
    }

    // Decrypted thumbnails must never reach disk, so Coil gets a memory cache only.
    override fun newImageLoader(context: PlatformContext): ImageLoader =
        ImageLoader.Builder(context)
            .memoryCache { MemoryCache.Builder().maxSizePercent(context, 0.25).build() }
            .diskCache(null)
            .diskCachePolicy(CachePolicy.DISABLED)
            .components { add(ThumbnailFetcher.Factory { container.session }) }
            .build()

    private companion object {
        const val TICK_MS = 5_000L
    }
}
