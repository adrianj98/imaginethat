package ai.alertd.imagethat.ui.components

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.unit.dp

/**
 * Stroke icons traced from the design mockups (24-unit grid, round caps and joins).
 * Tint them with Icon's tint; the colour baked in here is replaced.
 */
object AppIcons {
    private fun icon(name: String, vararg strokes: String, fills: List<String> = emptyList(), width: Float = 2.2f): ImageVector {
        val b = ImageVector.Builder(name, 24.dp, 24.dp, 24f, 24f)
        strokes.forEach {
            b.addPath(
                addPathNodes(it), stroke = SolidColor(Color.Black), strokeLineWidth = width,
                strokeLineCap = StrokeCap.Round, strokeLineJoin = StrokeJoin.Round,
            )
        }
        fills.forEach { b.addPath(addPathNodes(it), fill = SolidColor(Color.Black)) }
        return b.build()
    }

    private fun circle(cx: Float, cy: Float, r: Float) = "M${cx - r} ${cy}a$r $r 0 1 0 ${2 * r} 0a$r $r 0 1 0 ${-2 * r} 0z"

    /** Rounded rect as a path (SVG <rect x y w h rx>). */
    private fun rect(x: Float, y: Float, w: Float, h: Float, r: Float) =
        "M${x + r} ${y}h${w - 2 * r}a$r $r 0 0 1 $r ${r}v${h - 2 * r}a$r $r 0 0 1 ${-r} ${r}h${-(w - 2 * r)}a$r $r 0 0 1 ${-r} ${-r}v${-(h - 2 * r)}a$r $r 0 0 1 $r ${-r}z"

    private const val FOLDER = "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"
    private const val EYE = "M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"

    val Back = icon("Back", "M15 6l-6 6 6 6")
    val Chevron = icon("Chevron", "M9 6l6 6-6 6")
    val Close = icon("Close", "M6 6l12 12M18 6L6 18")
    val Plus = icon("Plus", "M12 5v14M5 12h14")
    val Check = icon("Check", "M5 12l5 5 9-10", width = 3.2f)
    val Lock = icon("Lock", rect(4f, 11f, 16f, 10f, 2f), "M8 11V7a4 4 0 0 1 8 0v4")
    /** The "lock this vault" action: shackle half open. */
    val LockAction = icon("LockAction", rect(4f, 11f, 16f, 10f, 2f), "M8 11V7a4 4 0 0 1 7.5-2")
    /** Large lock for the unlock screen, with a keyhole. */
    val LockKeyhole = icon("LockKeyhole", rect(4f, 11f, 16f, 10f, 2.5f), "M8 11V7a4 4 0 0 1 8 0v4", fills = listOf(circle(12f, 16f, 1.2f)))
    val Folder = icon("Folder", FOLDER)
    val FolderPlus = icon("FolderPlus", FOLDER, "M12 10.5v6M9 13.5h6")
    val MoveTo = icon("MoveTo", FOLDER, "M10 14h5M13 12l2 2-2 2")
    val Upload = icon("Upload", "M12 16V4M6 10l6-6 6 6", "M4 20h16", width = 2.4f)
    /** Save offline (into the offline vault on this phone). */
    val SaveOffline = icon("SaveOffline", "M12 4v12M7 11l5 5 5-5", "M4 20h16")
    val Delete = icon("Delete", "M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3")
    val Info = icon("Info", circle(12f, 12f, 9f), "M12 11v5M12 8h.01")
    val More = icon("More", fills = listOf(circle(5f, 12f, 2f), circle(12f, 12f, 2f), circle(19f, 12f, 2f)))
    val Play = icon("Play", fills = listOf("M8 5.5v13a1 1 0 0 0 1.5.85l10.5-6.5a1 1 0 0 0 0-1.7L9.5 4.65A1 1 0 0 0 8 5.5z"))
    val Eye = icon("Eye", EYE, circle(12f, 12f, 3f))
    val EyeOff = icon("EyeOff", EYE, circle(12f, 12f, 3f), "M3 3l18 18")
    val Search = icon("Search", circle(11f, 11f, 7f), "M20 20l-3.5-3.5")
    /** Circular arrow: ask the server again instead of the cache. */
    val Refresh = icon("Refresh", "M4 12a8 8 0 0 1 13.7-5.7L20 8.5", "M20 4v4.5h-4.5", "M20 12a8 8 0 0 1-13.7 5.7L4 15.5", "M4 20v-4.5h4.5")
    val Server = icon("Server", rect(3f, 4f, 18f, 7f, 2f), rect(3f, 13f, 18f, 7f, 2f), "M7 7.5h.01M7 16.5h.01")
    val Phone = icon("Phone", rect(5f, 2f, 14f, 20f, 2f), "M11 18h2")
    val Fingerprint = icon(
        "Fingerprint",
        "M7 4.5A9 9 0 0 1 17 4.5M4.5 9a9 9 0 0 0-.5 3M19.5 9a9 9 0 0 1 .5 3M6 19.5a9 9 0 0 0 12 0M9.5 12a2.5 2.5 0 0 1 5 0c0 3-1 5-2.5 6.5",
        "M12 8.5a3.5 3.5 0 0 0-3.5 3.5",
    )
    val Image = icon("Image", rect(3f, 4f, 18f, 16f, 2f), circle(9f, 10f, 1.5f), "M21 16l-5-5-9 9")
    val File = icon("File", "M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z", "M14 3v5h5")
    val Clock = icon("Clock", circle(12f, 12f, 9f), "M12 7v5l3 2")
    val Shield = icon("Shield", "M12 3l8 3v6c0 4.5-3.4 8-8 9-4.6-1-8-4.5-8-9V6z")
}
