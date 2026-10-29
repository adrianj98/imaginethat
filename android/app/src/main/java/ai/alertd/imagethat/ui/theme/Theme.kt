package ai.alertd.imagethat.ui.theme

import ai.alertd.imagethat.R
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.ExperimentalTextApi
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.Font
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontVariation
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp

/** The app's only palette: dark, with one warm accent. */
object Palette {
    val Background = Color(0xFF0E0F12)
    /** Cards, fields, the floating action bar. */
    val Surface = Color(0xFF181A1F)
    /** Card borders, inset dividers, secondary tiles and buttons. */
    val Raised = Color(0xFF23262E)
    val Divider = Raised
    /** Border of outline pills. */
    val OutlineBorder = Color(0xFF2A2D35)
    /** Border of text fields and chips. */
    val InputBorder = Color(0xFF353943)
    /** Border of dashed "add" tiles. */
    val DashedBorder = Color(0xFF3A3E48)
    val Text = Color(0xFFF2F1EC)
    val TextSecondary = Color(0xFFA3A5AD)
    /** Chevrons, footers, placeholder icons. */
    val TextTertiary = Color(0xFF6E717A)
    val Accent = Color(0xFFE8B04B)
    val OnAccent = Color(0xFF0E0F12)
    val Destructive = Color(0xFFF6A08C)
    val Online = Color(0xFF7FD1B9)
    /** Translucent round buttons over media in the viewer. */
    val Scrim = Color(0x990E0F12)
}

/** Instrument Sans, bundled as one variable font (res/font/instrument_sans.ttf, OFL in assets/licenses). */
@OptIn(ExperimentalTextApi::class)
val InstrumentSans = FontFamily(
    listOf(400, 500, 600, 700).map { w ->
        Font(R.font.instrument_sans, FontWeight(w), variationSettings = FontVariation.Settings(FontVariation.weight(w)))
    },
)

private fun style(size: Int, weight: FontWeight, line: Int, tracking: Double = 0.0) = TextStyle(
    fontFamily = InstrumentSans,
    fontWeight = weight,
    fontSize = size.sp,
    lineHeight = line.sp,
    letterSpacing = tracking.em,
)

val ImageThatTypography = Typography(
    // Screen titles: Home uses headlineLarge, list/gallery/unlock screens headlineMedium.
    displaySmall = style(36, FontWeight.Bold, 40, -0.02),
    headlineLarge = style(32, FontWeight.Bold, 36, -0.02),
    headlineMedium = style(28, FontWeight.Bold, 32, -0.02),
    headlineSmall = style(22, FontWeight.Bold, 28, -0.01),
    titleLarge = style(20, FontWeight.SemiBold, 26, -0.01),
    // Row titles.
    titleMedium = style(16, FontWeight.SemiBold, 22, -0.01),
    titleSmall = style(15, FontWeight.SemiBold, 20),
    bodyLarge = style(16, FontWeight.Normal, 22),
    bodyMedium = style(14, FontWeight.Normal, 20),
    // Secondary lines under row titles.
    bodySmall = style(13, FontWeight.Normal, 18),
    labelLarge = style(14, FontWeight.SemiBold, 20),
    labelMedium = style(12, FontWeight.SemiBold, 16),
    labelSmall = style(11, FontWeight.Medium, 14),
)

private val ImageThatColors = darkColorScheme(
    primary = Palette.Accent,
    onPrimary = Palette.OnAccent,
    primaryContainer = Palette.Raised,
    onPrimaryContainer = Palette.Accent,
    inversePrimary = Palette.Accent,
    secondary = Palette.TextSecondary,
    onSecondary = Palette.Background,
    secondaryContainer = Palette.Raised,
    onSecondaryContainer = Palette.Text,
    tertiary = Palette.Online,
    onTertiary = Palette.Background,
    tertiaryContainer = Palette.Raised,
    onTertiaryContainer = Palette.Online,
    background = Palette.Background,
    onBackground = Palette.Text,
    surface = Palette.Background,
    onSurface = Palette.Text,
    surfaceVariant = Palette.Raised,
    onSurfaceVariant = Palette.TextSecondary,
    surfaceTint = Palette.Background,
    inverseSurface = Palette.Text,
    inverseOnSurface = Palette.Background,
    error = Palette.Destructive,
    onError = Palette.OnAccent,
    errorContainer = Palette.Raised,
    onErrorContainer = Palette.Destructive,
    outline = Palette.InputBorder,
    outlineVariant = Palette.Divider,
    scrim = Color.Black,
    surfaceBright = Palette.Raised,
    surfaceDim = Palette.Background,
    surfaceContainerLowest = Palette.Background,
    surfaceContainerLow = Palette.Surface,
    surfaceContainer = Palette.Surface,
    surfaceContainerHigh = Palette.Surface,
    surfaceContainerHighest = Palette.Raised,
)

val ImageThatShapes = Shapes(
    extraSmall = RoundedCornerShape(10.dp),
    small = RoundedCornerShape(12.dp),
    medium = RoundedCornerShape(16.dp),
    large = RoundedCornerShape(18.dp),
    extraLarge = RoundedCornerShape(24.dp),
)

/** Dark only, whatever the system setting: the design has no light variant. */
@Composable
fun ImageThatTheme(content: @Composable () -> Unit) = MaterialTheme(
    colorScheme = ImageThatColors,
    typography = ImageThatTypography,
    shapes = ImageThatShapes,
    content = content,
)
