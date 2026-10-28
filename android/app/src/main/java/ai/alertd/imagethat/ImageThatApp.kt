package ai.alertd.imagethat

import android.app.Application
import coil3.ImageLoader
import coil3.PlatformContext
import coil3.SingletonImageLoader
import coil3.memory.MemoryCache
import coil3.request.CachePolicy
import mobile.Mobile

class ImageThatApp : Application(), SingletonImageLoader.Factory {
    override fun onCreate() {
        super.onCreate()
        // Go's default temp dir (/data/local/tmp) is not writable by apps.
        Mobile.setTempDir(cacheDir.absolutePath)
    }

    // Decrypted thumbnails must never reach disk, so Coil gets a memory cache only.
    override fun newImageLoader(context: PlatformContext): ImageLoader =
        ImageLoader.Builder(context)
            .memoryCache { MemoryCache.Builder().maxSizePercent(context, 0.25).build() }
            .diskCache(null)
            .diskCachePolicy(CachePolicy.DISABLED)
            .build()
}
