package ai.alertd.imagethat.ui

import ai.alertd.imagethat.R
import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.RemoteSource
import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.media.VaultDataSource
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Matrix
import android.net.Uri
import android.view.LayoutInflater
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.calculatePan
import androidx.compose.foundation.gestures.calculateZoom
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChanged
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.HttpDataSource
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.ui.PlayerView
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import java.io.ByteArrayInputStream
import java.io.IOException

/** Images above this many bytes are not previewed: they are read whole into memory to be decoded. */
private const val MAX_IMAGE_BYTES = 48L * 1024 * 1024
/** Decoded images are downsampled until the long edge is at most about this many pixels. */
private const val MAX_DECODE_EDGE = 2048
private const val MAX_ZOOM = 6f
private const val KEEP_ALIVE_MS = 30_000L

internal fun formatSize(bytes: Long): String = when {
    bytes < 1024 -> "$bytes B"
    bytes < 1024 * 1024 -> "%.1f KB".format(bytes / 1024.0)
    bytes < 1024L * 1024 * 1024 -> "%.1f MB".format(bytes / 1048576.0)
    else -> "%.2f GB".format(bytes / 1073741824.0)
}

/**
 * Full-screen pager over [items] (the folder's media). Images are decoded in memory and
 * pinch-zoomable; videos stream through Media3. Nothing is exported, shared or cached.
 */
@Composable
fun ViewerScreen(
    session: Session,
    sourceId: String,
    source: VaultSource,
    items: List<Item>,
    startIndex: Int,
    onClose: () -> Unit,
) {
    BackHandler(onBack = onClose)
    val pager = rememberPagerState(initialPage = startIndex.coerceIn(0, (items.size - 1).coerceAtLeast(0))) { items.size }
    Box(Modifier.fillMaxSize().background(Color.Black).testTag("viewer")) {
        HorizontalPager(pager, Modifier.fillMaxSize(), beyondViewportPageCount = 0, key = { items[it].path }) { page ->
            val item = items[page]
            when (item.media) {
                MediaKind.IMAGE -> ImagePage(session, sourceId, source, item)
                MediaKind.VIDEO -> if (pager.settledPage == page) VideoPage(session, sourceId, source, item) else Box(Modifier.fillMaxSize())
                MediaKind.OTHER -> UnsupportedPage(item)
            }
        }
        Row(
            Modifier.fillMaxWidth().background(Color(0x66000000)).padding(horizontal = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            TextButton(onClick = onClose, modifier = Modifier.testTag("viewer-close")) { Text("Close", color = Color.White) }
            val cur = items.getOrNull(pager.currentPage)
            Text(
                cur?.name.orEmpty(), color = Color.White, maxLines = 1,
                modifier = Modifier.weight(1f).padding(horizontal = 8.dp),
            )
            Text("${pager.currentPage + 1} / ${items.size}", color = Color.White, modifier = Modifier.padding(end = 8.dp))
        }
    }
}

@Composable
private fun UnsupportedPage(item: Item) {
    Column(
        Modifier.fillMaxSize().padding(32.dp).testTag("viewer-unsupported"),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(item.name, color = Color.White, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        Text(formatSize(item.size), color = Color.LightGray)
        Text("This file type cannot be previewed.", color = Color.LightGray, modifier = Modifier.padding(top = 16.dp))
    }
}

private sealed interface ImageLoad {
    object Loading : ImageLoad
    class Ready(val bitmap: Bitmap) : ImageLoad
    class Failed(val message: String) : ImageLoad
}

@Composable
private fun ImagePage(session: Session, sourceId: String, source: VaultSource, item: Item) {
    var load by remember(item.path) { mutableStateOf<ImageLoad>(ImageLoad.Loading) }
    LaunchedEffect(item.path) {
        load = try {
            ImageLoad.Ready(loadBitmap(source, item))
        } catch (e: CancellationException) {
            throw e
        } catch (e: SourceError.Locked) {
            session.reportLocked(sourceId)
            ImageLoad.Failed("The vault is locked.")
        } catch (e: Exception) {
            ImageLoad.Failed(e.message ?: "Could not load the image")
        }
    }
    // Free the decoded pixels as soon as the page leaves composition.
    DisposableEffect(item.path) {
        onDispose { (load as? ImageLoad.Ready)?.bitmap?.recycle() }
    }
    when (val l = load) {
        ImageLoad.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator() }
        is ImageLoad.Failed -> Box(Modifier.fillMaxSize().padding(32.dp), contentAlignment = Alignment.Center) {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text(item.name, color = Color.White, textAlign = TextAlign.Center)
                Text(l.message, color = Color.LightGray, textAlign = TextAlign.Center, modifier = Modifier.testTag("viewer-error"))
            }
        }
        is ImageLoad.Ready -> ZoomableImage(l.bitmap, item.name)
    }
}

@Composable
private fun ZoomableImage(bitmap: Bitmap, description: String) {
    var scale by remember(bitmap) { mutableFloatStateOf(1f) }
    var offset by remember(bitmap) { mutableStateOf(Offset.Zero) }
    var size by remember { mutableStateOf(androidx.compose.ui.unit.IntSize.Zero) }

    fun clamp(o: Offset, s: Float): Offset {
        val mx = size.width * (s - 1f) / 2f
        val my = size.height * (s - 1f) / 2f
        return Offset(o.x.coerceIn(-mx, mx), o.y.coerceIn(-my, my))
    }

    Image(
        bitmap = bitmap.asImageBitmap(),
        contentDescription = description,
        contentScale = ContentScale.Fit,
        modifier = Modifier
            .fillMaxSize()
            .testTag("viewer-image")
            .onSizeChanged { size = it }
            .pointerInput(bitmap) {
                detectTapGestures(onDoubleTap = {
                    if (scale > 1f) { scale = 1f; offset = Offset.Zero } else scale = 2.5f
                })
            }
            .pointerInput(bitmap) {
                // Single-finger drags are only taken while zoomed in, so the pager can still swipe at 1x.
                awaitEachGesture {
                    awaitFirstDown(requireUnconsumed = false)
                    do {
                        val ev = awaitPointerEvent(PointerEventPass.Main)
                        if (ev.changes.size > 1 || scale > 1f) {
                            val z = ev.calculateZoom()
                            val pan = ev.calculatePan()
                            scale = (scale * z).coerceIn(1f, MAX_ZOOM)
                            offset = if (scale == 1f) Offset.Zero else clamp(offset + pan, scale)
                            ev.changes.forEach { if (it.positionChanged()) it.consume() }
                        }
                    } while (ev.changes.any { it.pressed })
                }
            }
            .graphicsLayer(scaleX = scale, scaleY = scale, translationX = offset.x, translationY = offset.y),
    )
}

/** Smallest power-of-two inSampleSize that brings the long edge to at most [MAX_DECODE_EDGE]. */
internal fun decodeSampleSize(width: Int, height: Int): Int {
    var sample = 1
    while (maxOf(width, height) / sample > MAX_DECODE_EDGE) sample *= 2
    return sample
}

/** Private audio must not play on after the app leaves the foreground; playback is not resumed automatically. */
internal fun pauseOnStop(player: Player, event: Lifecycle.Event) {
    if (event == Lifecycle.Event.ON_STOP) player.pause()
}

/** Reads the whole (bounded) file into memory and decodes a downsampled, upright bitmap. */
private suspend fun loadBitmap(source: VaultSource, item: Item): Bitmap = withContext(Dispatchers.IO) {
    val total = if (item.size > 0) item.size else source.size(item.path)
    if (total > MAX_IMAGE_BYTES) throw IOException("Too large to preview (${formatSize(total)})")
    val bytes = ByteArray(total.toInt())
    source.open(item.path).use { input ->
        var n = 0
        while (n < bytes.size) {
            val r = input.read(bytes, n, bytes.size - n)
            if (r < 0) break
            n += r
        }
        if (n < bytes.size) throw IOException("The file ended early")
    }
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    if (bounds.outWidth <= 0) throw IOException("Not an image this device can display")
    val sample = decodeSampleSize(bounds.outWidth, bounds.outHeight)
    val decoded = BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = sample })
        ?: throw IOException("Not an image this device can display")
    val degrees = runCatching {
        when (android.media.ExifInterface(ByteArrayInputStream(bytes))
            .getAttributeInt(android.media.ExifInterface.TAG_ORIENTATION, android.media.ExifInterface.ORIENTATION_NORMAL)) {
            android.media.ExifInterface.ORIENTATION_ROTATE_90 -> 90f
            android.media.ExifInterface.ORIENTATION_ROTATE_180 -> 180f
            android.media.ExifInterface.ORIENTATION_ROTATE_270 -> 270f
            else -> 0f
        }
    }.getOrDefault(0f)
    if (degrees == 0f) decoded else {
        val rotated = Bitmap.createBitmap(decoded, 0, 0, decoded.width, decoded.height, Matrix().apply { postRotate(degrees) }, true)
        if (rotated !== decoded) decoded.recycle()
        rotated
    }
}

@Composable
private fun VideoPage(session: Session, sourceId: String, source: VaultSource, item: Item) {
    val context = LocalContext.current
    var error by remember(item.path) { mutableStateOf<String?>(null) }
    var playing by remember(item.path) { mutableStateOf(false) }

    val player = remember(item.path) {
        // No CacheDataSource anywhere: decrypted bytes only ever live in player memory.
        val (factory: DataSource.Factory, uri) = if (source is RemoteSource) {
            OkHttpDataSource.Factory(source.httpClient) to Uri.parse(source.fileUrl(item.path))
        } else {
            DataSource.Factory { VaultDataSource(source) } to Uri.parse("vault://local").buildUpon().path(item.path).build()
        }
        ExoPlayer.Builder(context)
            .setMediaSourceFactory(ProgressiveMediaSource.Factory(factory))
            .build()
            .apply {
                setMediaItem(MediaItem.fromUri(uri))
                playWhenReady = true
                prepare()
            }
    }
    DisposableEffect(player) {
        val l = object : Player.Listener {
            override fun onIsPlayingChanged(isPlaying: Boolean) { playing = isPlaying }
            override fun onPlayerError(e: PlaybackException) {
                val http = e.cause as? HttpDataSource.InvalidResponseCodeException
                if (http?.responseCode == 401) {
                    session.reportLocked(sourceId)
                    error = "The vault is locked."
                } else {
                    error = "Could not play this video (${e.errorCodeName})."
                }
            }
        }
        player.addListener(l)
        onDispose {
            player.removeListener(l)
            player.release()
        }
    }
    val lifecycleOwner = LocalLifecycleOwner.current
    DisposableEffect(player, lifecycleOwner) {
        val obs = LifecycleEventObserver { _, event -> pauseOnStop(player, event) }
        lifecycleOwner.lifecycle.addObserver(obs)
        onDispose { lifecycleOwner.lifecycle.removeObserver(obs) }
    }
    // Watching a long video is activity: keep the idle auto-lock from firing mid-playback.
    LaunchedEffect(playing) {
        while (playing) {
            session.touch()
            delay(KEEP_ALIVE_MS)
        }
    }

    Box(Modifier.fillMaxSize().testTag("viewer-video")) {
        AndroidView(
            factory = { ctx ->
                (LayoutInflater.from(ctx).inflate(R.layout.vault_player, null) as PlayerView).also { it.player = player }
            },
            modifier = Modifier.fillMaxSize(),
        )
        error?.let {
            Text(
                it, color = Color.White,
                modifier = Modifier.align(Alignment.Center).background(Color(0xAA000000)).padding(16.dp).testTag("viewer-error"),
            )
        }
    }
}
