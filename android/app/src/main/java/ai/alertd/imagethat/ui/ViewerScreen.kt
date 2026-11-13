package ai.alertd.imagethat.ui

import ai.alertd.imagethat.R
import ai.alertd.imagethat.data.Item
import ai.alertd.imagethat.data.LocalSource
import ai.alertd.imagethat.data.MediaKind
import ai.alertd.imagethat.data.RemoteSource
import ai.alertd.imagethat.data.SourceError
import ai.alertd.imagethat.data.VaultSource
import ai.alertd.imagethat.media.VaultDataSource
import ai.alertd.imagethat.ui.components.AccentChip
import ai.alertd.imagethat.ui.components.ActionTile
import ai.alertd.imagethat.ui.components.ActionTone
import ai.alertd.imagethat.ui.components.AppIcons
import ai.alertd.imagethat.ui.components.DialogButton
import ai.alertd.imagethat.ui.components.IconTile
import ai.alertd.imagethat.ui.components.RoundIconButton
import ai.alertd.imagethat.ui.components.SectionLabel
import ai.alertd.imagethat.ui.theme.Palette
import android.app.Activity
import android.content.Context
import android.content.ContextWrapper
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Matrix
import android.net.Uri
import android.view.LayoutInflater
import android.view.View
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
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
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
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.PopupProperties
import androidx.compose.ui.window.SecureFlagPolicy
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.VideoSize
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.HttpDataSource
import androidx.media3.datasource.okhttp.OkHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.ui.PlayerView
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.ByteArrayInputStream
import java.io.IOException

/** Images above this many bytes are not previewed: they are read whole into memory to be decoded. */
private const val MAX_IMAGE_BYTES = 48L * 1024 * 1024
/** Decoded images are downsampled until the long edge is at most about this many pixels. */
private const val MAX_DECODE_EDGE = 2048
private const val MAX_ZOOM = 6f
/** Where a double-tap zooms to. */
private const val TOGGLE_ZOOM = 2.5f
private const val DOUBLE_TAP_MS = 300L
/** Height, from the bottom of a video, left to the player's own control bar while it shows. */
private val PLAYER_CONTROLS_BAND = 120.dp

/** Zoom and pan of the photo or video on a page. Scale 1 is "fit"; [offset] is in pixels. */
internal data class Zoom(val scale: Float = 1f, val offset: Offset = Offset.Zero) {
    val isZoomed: Boolean get() = scale > 1f

    /** Applies a pinch and a drag, keeping the media from being dragged off a page of [size]. */
    fun transformed(zoomBy: Float, pan: Offset, size: IntSize): Zoom {
        val s = (scale * zoomBy).coerceIn(1f, MAX_ZOOM)
        return if (s == 1f) Zoom() else Zoom(s, clamp(offset + pan, s, size))
    }

    /** Double-tap: zoom in on [tap] (measured from the centre of the page), or back to fit. */
    fun toggled(tap: Offset, size: IntSize): Zoom =
        if (isZoomed) Zoom() else Zoom(TOGGLE_ZOOM, clamp(tap - tap * TOGGLE_ZOOM, TOGGLE_ZOOM, size))

    /** Spoken by screen readers, and read by the UI tests. */
    val description: String get() = if (isZoomed) "Zoomed ${(scale * 100).toInt()}%" else "Fit"

    private fun clamp(o: Offset, s: Float, size: IntSize): Offset {
        val mx = size.width * (s - 1f) / 2f
        val my = size.height * (s - 1f) / 2f
        return Offset(o.x.coerceIn(-mx, mx), o.y.coerceIn(-my, my))
    }
}

private tailrec fun Context.findActivity(): Activity? = when (this) {
    is Activity -> this
    is ContextWrapper -> baseContext.findActivity()
    else -> null
}
private const val KEEP_ALIVE_MS = 30_000L

internal fun formatSize(bytes: Long): String = when {
    bytes < 1024 -> "$bytes B"
    bytes < 1024 * 1024 -> "%.1f KB".format(bytes / 1024.0)
    bytes < 1024L * 1024 * 1024 -> "%.1f MB".format(bytes / 1048576.0)
    else -> "%.2f GB".format(bytes / 1073741824.0)
}

/** What the viewer can do to the current file, run through the browser's selection actions. */
class ViewerActions(
    val canSaveOffline: Boolean,
    val onSaveOffline: (Item) -> Unit,
    val onMove: (Item) -> Unit,
    val onDelete: (Item) -> Unit,
)

/**
 * Full-screen pager over [items] (the folder's media). Images are decoded in memory and
 * pinch-zoomable; videos stream through Media3. Nothing is exported, shared or cached.
 * Tapping an image hides the top bar and the bottom panel. Fullscreen hides those and the system
 * bars; Back, or a tap on an image, leaves it.
 */
@Composable
fun ViewerScreen(
    session: Session,
    sourceId: String,
    source: VaultSource,
    items: List<Item>,
    startIndex: Int,
    onClose: () -> Unit,
    actions: ViewerActions? = null,
) {
    var fullscreen by remember { mutableStateOf(false) }
    BackHandler { if (fullscreen) fullscreen = false else onClose() }
    val view = LocalView.current
    DisposableEffect(fullscreen) {
        val window = view.context.findActivity()?.window
        val bars = window?.let { WindowCompat.getInsetsController(it, view) }
        if (fullscreen) {
            bars?.systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            bars?.hide(WindowInsetsCompat.Type.systemBars())
        }
        // Also runs when the viewer closes while fullscreen.
        onDispose { bars?.show(WindowInsetsCompat.Type.systemBars()) }
    }
    val pager = rememberPagerState(initialPage = startIndex.coerceIn(0, (items.size - 1).coerceAtLeast(0))) { items.size }
    var chromeWanted by remember { mutableStateOf(true) }
    val chrome = chromeWanted && !fullscreen
    // Whether the video player's own control bar is showing; in fullscreen the exit button rides with it.
    var playerControls by remember { mutableStateOf(false) }
    var info by remember { mutableStateOf<Item?>(null) }
    var menu by remember { mutableStateOf(false) }
    // Full-size pixel dimensions, learned when a page decodes its image or the player reports the video size.
    val dims = remember { mutableStateMapOf<String, IntSize>() }
    val cur = items.getOrNull(pager.currentPage)

    Column(Modifier.fillMaxSize().background(Color.Black).testTag("viewer")) {
        Box(Modifier.weight(1f).fillMaxWidth()) {
            HorizontalPager(pager, Modifier.fillMaxSize(), beyondViewportPageCount = 0, key = { items[it].path }) { page ->
                val item = items[page]
                when (item.media) {
                    MediaKind.IMAGE -> ImagePage(session, sourceId, source, item, onSize = { dims[item.path] = it }, onTap = { if (fullscreen) fullscreen = false else chromeWanted = !chromeWanted })
                    MediaKind.VIDEO -> if (pager.settledPage == page) {
                        VideoPage(session, sourceId, source, item, onSize = { dims[item.path] = it }, onControls = { playerControls = it })
                    } else Box(Modifier.fillMaxSize())
                    MediaKind.OTHER -> UnsupportedPage(item)
                }
            }
            if (chrome) {
                Row(
                    Modifier.fillMaxWidth().statusBarsPadding().padding(start = 16.dp, end = 16.dp, top = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RoundIconButton(AppIcons.Back, "Close", onClose, Modifier.testTag("viewer-close"), background = Palette.Scrim)
                    Column(Modifier.weight(1f).padding(horizontal = 12.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                        Text(cur?.name.orEmpty(), color = Palette.Text, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text("${pager.currentPage + 1} of ${items.size}", color = Palette.TextSecondary, style = MaterialTheme.typography.labelMedium.copy(fontWeight = FontWeight.Normal))
                    }
                    RoundIconButton(AppIcons.Fullscreen, "Full screen", { fullscreen = true }, Modifier.padding(end = 8.dp).testTag("viewer-fullscreen"), background = Palette.Scrim)
                    Box {
                        RoundIconButton(AppIcons.More, "More options", { menu = true }, Modifier.testTag("viewer-more"), background = Palette.Scrim)
                        DropdownMenu(
                            expanded = menu,
                            onDismissRequest = { menu = false },
                            containerColor = Palette.Surface,
                            properties = PopupProperties(focusable = true, securePolicy = SecureFlagPolicy.SecureOn),
                        ) {
                            DropdownMenuItem(text = { Text("Info") }, onClick = { menu = false; info = cur })
                            DropdownMenuItem(text = { Text("Hide controls") }, onClick = { menu = false; chromeWanted = false })
                        }
                    }
                }
            }
            // A fullscreen video has no tap of its own to leave with, so the way out shows with its controls.
            if (fullscreen && cur?.media == MediaKind.VIDEO && playerControls) {
                RoundIconButton(
                    AppIcons.FullscreenExit, "Exit full screen", { fullscreen = false },
                    Modifier.align(Alignment.TopEnd).statusBarsPadding().padding(16.dp).testTag("viewer-fullscreen-exit"),
                    background = Palette.Scrim,
                )
            }
        }
        if (chrome && cur != null) {
            ViewerPanel(cur, dims[cur.path], actions, onInfo = { info = cur }, keep = (source as? LocalSource)?.takeIf { it.cachesFiles })
        }
    }

    info?.let { item -> InfoDialog(item, dims[item.path], onDismiss = { info = null }) }
}

@Composable
private fun ViewerPanel(item: Item, size: IntSize?, actions: ViewerActions?, onInfo: () -> Unit, keep: LocalSource?) {
    Column(
        Modifier.fillMaxWidth().background(Palette.Background).navigationBarsPadding().padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Row(Modifier.padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            AccentChip("Encrypted at rest", filled = false, icon = AppIcons.Lock)
            Text(
                details(item, size), Modifier.testTag("viewer-details"),
                color = Palette.TextSecondary, style = MaterialTheme.typography.labelMedium.copy(fontWeight = FontWeight.Normal),
                maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
        }
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            val tile = Palette.Surface
            if (actions?.canSaveOffline == true) {
                ActionTile("Save offline", AppIcons.SaveOffline, { actions.onSaveOffline(item) }, Modifier.testTag("viewer-save-offline"), height = 60.dp, background = tile)
            }
            if (keep != null) KeepOnPhoneTile(keep, item, tile)
            if (actions != null) ActionTile("Move", AppIcons.MoveTo, { actions.onMove(item) }, Modifier.testTag("viewer-move"), height = 60.dp, background = tile)
            ActionTile("Info", AppIcons.Info, onInfo, Modifier.testTag("viewer-info"), height = 60.dp, background = tile)
            if (actions != null) {
                ActionTile("Delete", AppIcons.Delete, { actions.onDelete(item) }, Modifier.testTag("viewer-delete"), tone = ActionTone.Destructive, height = 60.dp, background = tile)
            }
        }
    }
}

private sealed interface Keep {
    object Checking : Keep
    object Remote : Keep
    /** Being fetched; [fraction] is null until the size is known. */
    class Fetching(val fraction: Float?) : Keep
    object OnPhone : Keep
}

/**
 * Keeps the file of a vault that lives elsewhere (client-decrypt server, Google Drive) on the
 * phone, as ciphertext the engine reads without the network. Tapping again cancels a fetch in
 * progress, or drops the phone's copy. A fetch stops when the viewer closes.
 */
@Composable
private fun RowScope.KeepOnPhoneTile(source: LocalSource, item: Item, background: Color) {
    val scope = rememberCoroutineScope()
    var state by remember(item.path) { mutableStateOf<Keep>(Keep.Checking) }
    var fetch by remember(item.path) { mutableStateOf<Job?>(null) }
    var error by remember(item.path) { mutableStateOf<String?>(null) }
    LaunchedEffect(item.path) {
        state = if (runCatching { source.isCached(item.path) }.getOrDefault(false)) Keep.OnPhone else Keep.Remote
    }
    fun onClick() {
        error = null
        when (state) {
            Keep.Checking -> Unit
            is Keep.Fetching -> fetch?.cancel()
            Keep.Remote -> {
                state = Keep.Fetching(null)
                fetch = scope.launch {
                    state = try {
                        source.cacheFile(item.path) { done, total ->
                            state = Keep.Fetching(if (total > 0) (done.toFloat() / total).coerceIn(0f, 1f) else null)
                        }
                        Keep.OnPhone
                    } catch (e: CancellationException) {
                        state = Keep.Remote
                        throw e
                    } catch (e: Exception) {
                        error = e.message ?: "Could not keep the file"
                        Keep.Remote
                    }
                }
            }
            Keep.OnPhone -> scope.launch {
                state = try {
                    source.uncache(item.path)
                    Keep.Remote
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    error = e.message ?: "Could not remove the copy"
                    Keep.OnPhone
                }
            }
        }
    }
    val label = when (val st = state) {
        Keep.Checking, Keep.Remote -> if (error != null) "Retry keep" else "Keep on phone"
        is Keep.Fetching -> st.fraction?.let { "Cancel ${(it * 100).toInt()}%" } ?: "Cancel"
        Keep.OnPhone -> "On phone"
    }
    ActionTile(
        label, if (state == Keep.OnPhone) AppIcons.Check else AppIcons.SaveOffline, { onClick() },
        Modifier.testTag("viewer-keep").semantics { error?.let { stateDescription = it } },
        tone = if (state == Keep.OnPhone) ActionTone.Accent else ActionTone.Default,
        enabled = state != Keep.Checking, height = 60.dp, background = background,
    )
}

/** "4032 × 3024 · 3.8 MB", leaving out what is not known yet. */
internal fun details(item: Item, size: IntSize?): String = listOfNotNull(
    size?.let { "${it.width} × ${it.height}" },
    item.size.takeIf { it > 0 }?.let { formatSize(it) },
).joinToString(" · ")

/** Human file type from the name and media kind, e.g. "JPG image". */
internal fun fileType(item: Item): String {
    val ext = item.name.substringAfterLast('.', "").uppercase().takeIf { it.isNotEmpty() && it.length <= 5 }
    val kind = when (item.media) { MediaKind.IMAGE -> "image"; MediaKind.VIDEO -> "video"; MediaKind.OTHER -> "file" }
    return if (ext != null) "$ext $kind" else kind.replaceFirstChar { it.uppercase() }
}

@Composable
private fun InfoDialog(item: Item, size: IntSize?, onDismiss: () -> Unit) {
    SecureAlertDialog(
        onDismissRequest = onDismiss,
        title = "Info",
        confirmButton = { DialogButton("Done", onDismiss, Modifier.testTag("viewer-info-done")) },
    ) {
        Column(Modifier.testTag("viewer-info-dialog"), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            InfoLine("Name", item.name)
            InfoLine("Type", fileType(item))
            if (item.size > 0) InfoLine("Size", formatSize(item.size))
            InfoLine("Dimensions", size?.let { "${it.width} × ${it.height}" } ?: "Not known")
            InfoLine("Storage", "Encrypted in the vault; decrypted only in memory")
        }
    }
}

@Composable
private fun InfoLine(label: String, value: String) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        SectionLabel(label)
        Text(value, color = Palette.Text, style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun UnsupportedPage(item: Item) {
    Column(
        Modifier.fillMaxSize().padding(32.dp).testTag("viewer-unsupported"),
        verticalArrangement = Arrangement.spacedBy(10.dp, Alignment.CenterVertically),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        IconTile(AppIcons.File, tint = Palette.TextSecondary, background = Palette.Surface, border = Palette.Divider, size = 72.dp, radius = 22.dp, iconSize = 28.dp)
        Text(item.name, color = Palette.Text, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
        Text(formatSize(item.size), color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall)
        Text("This file type cannot be previewed.", color = Palette.TextTertiary, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 8.dp))
    }
}

private sealed interface ImageLoad {
    object Loading : ImageLoad
    class Ready(val bitmap: Bitmap, val fullSize: IntSize) : ImageLoad
    class Failed(val message: String) : ImageLoad
}

@Composable
private fun ImagePage(session: Session, sourceId: String, source: VaultSource, item: Item, onSize: (IntSize) -> Unit, onTap: () -> Unit) {
    var load by remember(item.path) { mutableStateOf<ImageLoad>(ImageLoad.Loading) }
    LaunchedEffect(item.path) {
        load = try {
            loadBitmap(source, item).also { onSize(it.fullSize) }
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
        ImageLoad.Loading -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) { CircularProgressIndicator(color = Palette.Accent) }
        is ImageLoad.Failed -> Box(Modifier.fillMaxSize().padding(32.dp), contentAlignment = Alignment.Center) {
            Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(6.dp)) {
                Text(item.name, color = Palette.Text, style = MaterialTheme.typography.titleMedium, textAlign = TextAlign.Center)
                Text(l.message, color = Palette.TextSecondary, style = MaterialTheme.typography.bodySmall, textAlign = TextAlign.Center, modifier = Modifier.testTag("viewer-error"))
            }
        }
        is ImageLoad.Ready -> ZoomableImage(l.bitmap, item.name, onTap)
    }
}

@Composable
private fun ZoomableImage(bitmap: Bitmap, description: String, onTap: () -> Unit) {
    var zoom by remember(bitmap) { mutableStateOf(Zoom()) }
    var size by remember { mutableStateOf(IntSize.Zero) }

    Image(
        bitmap = bitmap.asImageBitmap(),
        contentDescription = description,
        contentScale = ContentScale.Fit,
        modifier = Modifier
            .fillMaxSize()
            .testTag("viewer-image")
            .semantics { stateDescription = zoom.description }
            .onSizeChanged { size = it }
            .pointerInput(bitmap) {
                detectTapGestures(
                    onTap = { onTap() },
                    onDoubleTap = { tap ->
                        zoom = zoom.toggled(tap - Offset(size.width / 2f, size.height / 2f), size)
                    },
                )
            }
            .pointerInput(bitmap) {
                // Single-finger drags are only taken while zoomed in, so the pager can still swipe at 1x.
                awaitEachGesture {
                    awaitFirstDown(requireUnconsumed = false)
                    do {
                        val ev = awaitPointerEvent(PointerEventPass.Main)
                        if (ev.changes.size > 1 || zoom.isZoomed) {
                            zoom = zoom.transformed(ev.calculateZoom(), ev.calculatePan(), size)
                            ev.changes.forEach { if (it.positionChanged()) it.consume() }
                        }
                    } while (ev.changes.any { it.pressed })
                }
            }
            .graphicsLayer(scaleX = zoom.scale, scaleY = zoom.scale, translationX = zoom.offset.x, translationY = zoom.offset.y),
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

/** Reads the whole (bounded) file into memory and decodes a downsampled, upright bitmap, with the upright full size. */
private suspend fun loadBitmap(source: VaultSource, item: Item): ImageLoad.Ready = withContext(Dispatchers.IO) {
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
    val fullSize = if (degrees == 90f || degrees == 270f) IntSize(bounds.outHeight, bounds.outWidth) else IntSize(bounds.outWidth, bounds.outHeight)
    val upright = if (degrees == 0f) decoded else {
        val rotated = Bitmap.createBitmap(decoded, 0, 0, decoded.width, decoded.height, Matrix().apply { postRotate(degrees) }, true)
        if (rotated !== decoded) decoded.recycle()
        rotated
    }
    ImageLoad.Ready(upright, fullSize)
}

@Composable
private fun VideoPage(session: Session, sourceId: String, source: VaultSource, item: Item, onSize: (IntSize) -> Unit, onControls: (Boolean) -> Unit) {
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
            override fun onVideoSizeChanged(videoSize: VideoSize) {
                if (videoSize.width > 0 && videoSize.height > 0) onSize(IntSize(videoSize.width, videoSize.height))
            }
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

    var zoom by remember(item.path) { mutableStateOf(Zoom()) }
    var controlsShown by remember(item.path) { mutableStateOf(false) }
    DisposableEffect(item.path) { onDispose { onControls(false) } }

    Box(
        Modifier.fillMaxSize().testTag("viewer-video")
            .semantics { stateDescription = zoom.description }
            // Before the player sees the touches (Initial pass): pinches, drags of a zoomed picture
            // and double-taps are taken here; everything else, single taps included, goes on to the
            // player and its controls.
            .pointerInput(item.path) {
                var lastTap = 0L
                var lastTapAt = Offset.Zero
                awaitEachGesture {
                    val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
                    // Leave the seek bar alone: a drag there scrubs, it does not pan.
                    val onPlayerControls = controlsShown && down.position.y > size.height - PLAYER_CONTROLS_BAND.toPx()
                    var travel = 0f
                    var taken = false
                    var upTime = down.uptimeMillis
                    do {
                        val ev = awaitPointerEvent(PointerEventPass.Initial)
                        upTime = ev.changes.first().uptimeMillis
                        val pan = ev.calculatePan()
                        travel += pan.getDistance()
                        if (!onPlayerControls && (ev.changes.size > 1 || (zoom.isZoomed && travel > viewConfiguration.touchSlop))) {
                            taken = true
                            zoom = zoom.transformed(ev.calculateZoom(), pan, size)
                            ev.changes.forEach { if (it.positionChanged()) it.consume() }
                        }
                    } while (ev.changes.any { it.pressed })
                    if (!taken && !onPlayerControls && travel <= viewConfiguration.touchSlop) {
                        val near = (down.position - lastTapAt).getDistance() < viewConfiguration.touchSlop * 4
                        if (upTime - lastTap < DOUBLE_TAP_MS && near) {
                            zoom = zoom.toggled(down.position - Offset(size.width / 2f, size.height / 2f), size)
                            lastTap = 0L
                        } else {
                            lastTap = upTime
                            lastTapAt = down.position
                        }
                    }
                }
            },
    ) {
        AndroidView(
            factory = { ctx ->
                (LayoutInflater.from(ctx).inflate(R.layout.vault_player, null) as PlayerView).also {
                    it.player = player
                    it.setControllerVisibilityListener(PlayerView.ControllerVisibilityListener { visibility ->
                        controlsShown = visibility == View.VISIBLE
                        onControls(controlsShown)
                    })
                }
            },
            // Only the picture is zoomed: the player's controls sit beside this frame, not inside it.
            update = { pv ->
                pv.findViewById<View>(androidx.media3.ui.R.id.exo_content_frame)?.apply {
                    scaleX = zoom.scale
                    scaleY = zoom.scale
                    translationX = zoom.offset.x
                    translationY = zoom.offset.y
                }
            },
            modifier = Modifier.fillMaxSize(),
        )
        error?.let {
            Text(
                it, color = Palette.Text, style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.align(Alignment.Center).padding(24.dp).clip(RoundedCornerShape(16.dp)).background(Palette.Surface).padding(16.dp).testTag("viewer-error"),
            )
        }
    }
}
