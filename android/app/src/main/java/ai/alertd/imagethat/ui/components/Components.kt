package ai.alertd.imagethat.ui.components

import ai.alertd.imagethat.ui.theme.Palette
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.em
import androidx.compose.ui.unit.sp

// Shared building blocks of the redesign. Colours come from [Palette] directly (not the
// MaterialTheme scheme) so they look the same under any theme, including test hosts.

private val TitleStyle @Composable get() = MaterialTheme.typography.titleMedium
private val SecondaryStyle @Composable get() = MaterialTheme.typography.bodySmall

/** Uppercase, tracked section heading above a [GroupCard]. */
@Composable
fun SectionLabel(text: String, modifier: Modifier = Modifier, color: Color = Palette.TextSecondary) {
    Text(
        text.uppercase(),
        modifier.padding(horizontal = 2.dp).semantics { heading() },
        color = color,
        style = MaterialTheme.typography.labelMedium.copy(letterSpacing = 0.08.em),
    )
}

/** Screen title with an optional one-line subtitle. [large] is the Home size (32sp), otherwise 28sp. */
@Composable
fun ScreenTitle(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    large: Boolean = false,
    subtitleLeading: (@Composable () -> Unit)? = null,
) {
    Column(modifier, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(
            title,
            Modifier.semantics { heading() },
            color = Palette.Text,
            style = if (large) MaterialTheme.typography.headlineLarge else MaterialTheme.typography.headlineMedium,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
        if (subtitle != null) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                subtitleLeading?.invoke()
                Text(subtitle, color = Palette.TextSecondary, style = SecondaryStyle, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/** 8dp status dot, e.g. [Palette.Online] next to "Online". */
@Composable
fun StatusDot(color: Color = Palette.Online, modifier: Modifier = Modifier) {
    Box(modifier.size(8.dp).clip(CircleShape).background(color))
}

/** Rounded card (radius 18, 1dp border) holding rows separated by [InsetDivider]s. */
@Composable
fun GroupCard(modifier: Modifier = Modifier, content: @Composable ColumnScope.() -> Unit) {
    Column(
        modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(18.dp))
            .background(Palette.Surface)
            .border(1.dp, Palette.Divider, RoundedCornerShape(18.dp)),
        content = content,
    )
}

/** 1dp divider inset 16dp from both card edges. */
@Composable
fun InsetDivider(modifier: Modifier = Modifier, inset: Dp = 16.dp) {
    Box(modifier.fillMaxWidth().padding(horizontal = inset).height(1.dp).background(Palette.Divider))
}

/**
 * A row inside a [GroupCard] (or a plain list): optional leading tile, title, optional subtitle,
 * trailing content (a chevron by default when clickable).
 */
@Composable
fun CardRow(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    onClick: (() -> Unit)? = null,
    titleColor: Color = Palette.Text,
    leading: (@Composable () -> Unit)? = null,
    trailing: (@Composable RowScope.() -> Unit)? = if (onClick != null) ({ Chevron() }) else null,
    contentPadding: PaddingValues = PaddingValues(horizontal = 16.dp, vertical = 14.dp),
) {
    Row(
        modifier
            .fillMaxWidth()
            .heightIn(min = 56.dp)
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .padding(contentPadding),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        leading?.invoke()
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(title, color = titleColor, style = TitleStyle, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (subtitle != null) Text(subtitle, color = Palette.TextSecondary, style = SecondaryStyle, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        trailing?.invoke(this)
    }
}

/** Trailing value text before a chevron, e.g. "5 min" on the Auto-lock row. */
@Composable
fun RowValue(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier, color = Palette.TextSecondary, style = MaterialTheme.typography.bodyMedium)
}

@Composable
fun Chevron(modifier: Modifier = Modifier, tint: Color = Palette.TextTertiary) {
    Icon(AppIcons.Chevron, contentDescription = null, modifier.size(18.dp), tint = tint)
}

/** 40dp rounded icon tile. [dashed] draws the dashed "add" outline instead of a fill. */
@Composable
fun IconTile(
    icon: ImageVector,
    modifier: Modifier = Modifier,
    tint: Color = Palette.Text,
    background: Color = Palette.Raised,
    size: Dp = 40.dp,
    radius: Dp = 12.dp,
    iconSize: Dp = 20.dp,
    dashed: Boolean = false,
    border: Color? = null,
    contentDescription: String? = null,
) {
    val shape = RoundedCornerShape(radius)
    Box(
        modifier
            .size(size)
            .then(
                when {
                    dashed -> Modifier.drawBehind {
                        val stroke = 1.dp.toPx()
                        drawRoundRect(
                            color = Palette.DashedBorder,
                            topLeft = androidx.compose.ui.geometry.Offset(stroke / 2, stroke / 2),
                            size = androidx.compose.ui.geometry.Size(this.size.width - stroke, this.size.height - stroke),
                            cornerRadius = CornerRadius(radius.toPx()),
                            style = Stroke(width = stroke, pathEffect = PathEffect.dashPathEffect(floatArrayOf(4.dp.toPx(), 3.dp.toPx()))),
                        )
                    }
                    else -> Modifier.clip(shape).background(background).then(if (border != null) Modifier.border(1.dp, border, shape) else Modifier)
                },
            ),
        contentAlignment = Alignment.Center,
    ) {
        Icon(icon, contentDescription, Modifier.size(iconSize), tint = tint)
    }
}

/** Full-radius accent button (the primary action). */
@Composable
fun PillButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
    enabled: Boolean = true,
    loading: Boolean = false,
    height: Dp = 56.dp,
) {
    Surface(
        onClick = onClick,
        enabled = enabled && !loading,
        modifier = modifier.height(height),
        shape = CircleShape,
        color = if (enabled) Palette.Accent else Palette.Raised,
        contentColor = if (enabled) Palette.OnAccent else Palette.TextTertiary,
    ) {
        PillContent(text, icon, loading, if (height >= 52.dp) 16.sp else 14.sp, FontWeightBold)
    }
}

/** Full-radius outline button on the card colour (secondary actions such as "Select", "Lock all"). */
@Composable
fun OutlinePill(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: ImageVector? = null,
    enabled: Boolean = true,
    height: Dp = 40.dp,
    contentColor: Color = Palette.Text,
    background: Color = Palette.Surface,
) {
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.height(height),
        shape = CircleShape,
        color = background,
        contentColor = if (enabled) contentColor else Palette.TextTertiary,
        border = BorderStroke(1.dp, Palette.OutlineBorder),
    ) {
        PillContent(text, icon, false, if (height >= 52.dp) 15.sp else 14.sp, androidx.compose.ui.text.font.FontWeight.SemiBold)
    }
}

private val FontWeightBold = androidx.compose.ui.text.font.FontWeight.Bold

@Composable
private fun PillContent(text: String, icon: ImageVector?, loading: Boolean, size: androidx.compose.ui.unit.TextUnit, weight: androidx.compose.ui.text.font.FontWeight) {
    Row(
        Modifier.fillMaxHeight().padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
    ) {
        if (loading) {
            CircularProgressIndicator(Modifier.size(18.dp), color = LocalContentColor.current, strokeWidth = 2.dp)
        } else if (icon != null) {
            Icon(icon, null, Modifier.size(18.dp))
        }
        Text(text, style = MaterialTheme.typography.labelLarge.copy(fontSize = size, fontWeight = weight), maxLines = 1)
    }
}

/**
 * Round icon-only button. Defaults to the outlined card style of the gallery header;
 * pass [background] = [Palette.Scrim] and [bordered] = false for buttons floating over media.
 */
@Composable
fun RoundIconButton(
    icon: ImageVector,
    contentDescription: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    tint: Color = Palette.Text,
    background: Color = Color.Transparent,
    bordered: Boolean = false,
    size: Dp = 44.dp,
    iconSize: Dp = 22.dp,
) {
    Surface(
        onClick = onClick,
        modifier = modifier.size(size).semantics { this.contentDescription = contentDescription },
        shape = CircleShape,
        color = background,
        contentColor = tint,
        border = if (bordered) BorderStroke(1.dp, Palette.OutlineBorder) else null,
    ) {
        Box(contentAlignment = Alignment.Center) { Icon(icon, null, Modifier.size(iconSize)) }
    }
}

/** Header row: back button on the left, [actions] on the right. Add statusBarsPadding at the screen level. */
@Composable
fun TopBar(
    onBack: (() -> Unit)?,
    modifier: Modifier = Modifier,
    backDescription: String = "Back",
    backTag: String? = null,
    center: (@Composable () -> Unit)? = null,
    actions: @Composable RowScope.() -> Unit = {},
) {
    Box(modifier.fillMaxWidth().heightIn(min = 44.dp)) {
        if (onBack != null) {
            RoundIconButton(
                AppIcons.Back, backDescription, onBack,
                Modifier.align(Alignment.CenterStart).then(if (backTag != null) Modifier.testTag(backTag) else Modifier),
            )
        }
        if (center != null) Box(Modifier.align(Alignment.Center)) { center() }
        Row(Modifier.align(Alignment.CenterEnd), horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically, content = actions)
    }
}

/**
 * Small pill label. [filled] is the accent "Set up" chip; otherwise the outlined accent chip
 * ("Encrypted at rest").
 */
@Composable
fun AccentChip(text: String, modifier: Modifier = Modifier, filled: Boolean = true, icon: ImageVector? = null, color: Color = Palette.Accent) {
    Row(
        modifier
            .clip(CircleShape)
            .then(if (filled) Modifier.background(color) else Modifier.border(1.dp, Palette.InputBorder, CircleShape))
            .padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        val fg = if (filled) Palette.OnAccent else color
        if (icon != null) Icon(icon, null, Modifier.size(12.dp), tint = fg)
        Text(text, color = fg, style = MaterialTheme.typography.labelMedium)
    }
}

/** Two equal buttons side by side inside a [GroupCard], split by a vertical divider. */
@Composable
fun SplitRow(modifier: Modifier = Modifier, content: @Composable RowScope.() -> Unit) {
    Row(modifier.fillMaxWidth().height(androidx.compose.foundation.layout.IntrinsicSize.Min), content = content)
}

@Composable
fun RowScope.SplitButton(text: String, icon: ImageVector?, onClick: () -> Unit, modifier: Modifier = Modifier, divider: Boolean = false) {
    Row(
        modifier
            .weight(1f)
            .heightIn(min = 52.dp)
            .clickable(role = Role.Button, onClick = onClick)
            .padding(horizontal = 12.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
    ) {
        if (icon != null) Icon(icon, null, Modifier.size(18.dp), tint = Palette.Text)
        Text(text, color = Palette.Text, style = MaterialTheme.typography.labelLarge)
    }
    if (divider) Box(Modifier.width(1.dp).fillMaxHeight().background(Palette.Divider))
}

enum class ActionTone { Default, Accent, Destructive }

/** Floating bottom card (radius 24) holding [ActionTile]s. Add navigationBarsPadding at the call site. */
@Composable
fun ActionBar(modifier: Modifier = Modifier, background: Color = Palette.Surface, bordered: Boolean = true, content: @Composable RowScope.() -> Unit) {
    Row(
        modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(24.dp))
            .background(background)
            .then(if (bordered) Modifier.border(1.dp, Palette.OutlineBorder, RoundedCornerShape(24.dp)) else Modifier)
            .padding(10.dp),
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        content = content,
    )
}

/** Icon-over-label tile (56dp, radius 16) for an [ActionBar] or the viewer panel. */
@Composable
fun RowScope.ActionTile(
    label: String,
    icon: ImageVector,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    tone: ActionTone = ActionTone.Default,
    enabled: Boolean = true,
    height: Dp = 56.dp,
    background: Color = Palette.Raised,
) {
    val (bg, fg) = when (tone) {
        ActionTone.Accent -> Palette.Accent to Palette.OnAccent
        ActionTone.Destructive -> background to Palette.Destructive
        ActionTone.Default -> background to Palette.Text
    }
    Surface(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.weight(1f).height(height),
        shape = RoundedCornerShape(16.dp),
        color = bg,
        contentColor = if (enabled) fg else Palette.TextTertiary,
    ) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(4.dp, Alignment.CenterVertically)) {
            Icon(icon, null, Modifier.size(18.dp))
            Text(
                label, maxLines = 1, overflow = TextOverflow.Ellipsis,
                style = MaterialTheme.typography.labelMedium.copy(
                    fontSize = 13.sp,
                    fontWeight = if (tone == ActionTone.Accent) FontWeightBold else androidx.compose.ui.text.font.FontWeight.SemiBold,
                ),
            )
        }
    }
}

/**
 * The design's text input: 54dp, radius 16, card fill, 1dp border, label above.
 * Callers keep the text in plain state (never rememberSaveable for secrets).
 */
@Composable
fun AppTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    label: String? = null,
    placeholder: String? = null,
    tag: String? = null,
    leadingIcon: ImageVector? = null,
    trailing: (@Composable () -> Unit)? = null,
    isError: Boolean = false,
    enabled: Boolean = true,
    keyboardOptions: KeyboardOptions = KeyboardOptions.Default,
    keyboardActions: KeyboardActions = KeyboardActions.Default,
    visualTransformation: VisualTransformation = VisualTransformation.None,
    textStyle: TextStyle = MaterialTheme.typography.bodyLarge,
    height: Dp = 54.dp,
) {
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        if (label != null) Text(label, color = Palette.TextSecondary, style = MaterialTheme.typography.labelLarge.copy(fontSize = 13.sp))
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            enabled = enabled,
            singleLine = true,
            textStyle = textStyle.copy(color = Palette.Text),
            cursorBrush = SolidColor(Palette.Accent),
            keyboardOptions = keyboardOptions,
            keyboardActions = keyboardActions,
            visualTransformation = visualTransformation,
            modifier = Modifier.fillMaxWidth().then(if (tag != null) Modifier.testTag(tag) else Modifier).semantics {
                if (label != null) contentDescription = label
            },
            decorationBox = { inner ->
                Row(
                    Modifier
                        .fillMaxWidth()
                        .height(height)
                        .clip(RoundedCornerShape(16.dp))
                        .background(Palette.Surface)
                        .border(1.dp, if (isError) Palette.Destructive else Palette.InputBorder, RoundedCornerShape(16.dp))
                        .padding(start = 16.dp, end = if (trailing != null) 8.dp else 16.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    if (leadingIcon != null) Icon(leadingIcon, null, Modifier.size(18.dp), tint = Palette.TextSecondary)
                    Box(Modifier.weight(1f)) {
                        if (value.isEmpty() && placeholder != null) {
                            Text(placeholder, color = Palette.TextTertiary, style = textStyle, maxLines = 1)
                        }
                        inner()
                    }
                    trailing?.invoke()
                }
            },
        )
    }
}

/** Compact (44dp, radius 14) search input for filtering lists. */
@Composable
fun SearchField(value: String, onValueChange: (String) -> Unit, modifier: Modifier = Modifier, placeholder: String = "Search", tag: String? = null) {
    Box(modifier) {
        AppTextField(
            value, onValueChange,
            placeholder = placeholder, tag = tag, leadingIcon = AppIcons.Search,
            textStyle = MaterialTheme.typography.bodyLarge.copy(fontSize = 15.sp), height = 44.dp,
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
        )
    }
}

/**
 * Password input with a show/hide eye. The visibility flag is the only state kept here;
 * the password itself is the caller's plain (non-saveable) state.
 */
@Composable
fun PasswordInput(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    label: String? = "Password",
    tag: String? = null,
    placeholder: String? = null,
    isError: Boolean = false,
    enabled: Boolean = true,
    imeAction: ImeAction = ImeAction.Done,
    onImeAction: (() -> Unit)? = null,
) {
    var visible by remember { mutableStateOf(false) }
    AppTextField(
        value, onValueChange, modifier,
        label = label, placeholder = placeholder, tag = tag, isError = isError, enabled = enabled,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false, imeAction = imeAction),
        keyboardActions = KeyboardActions(onAny = { onImeAction?.invoke() }),
        visualTransformation = if (visible) VisualTransformation.None else PasswordVisualTransformation(),
        textStyle = MaterialTheme.typography.bodyLarge.copy(fontSize = 18.sp, letterSpacing = if (visible) 0.sp else 0.12.em),
        trailing = {
            RoundIconButton(
                if (visible) AppIcons.EyeOff else AppIcons.Eye,
                if (visible) "Hide password" else "Show password",
                onClick = { visible = !visible },
                tint = Palette.TextSecondary,
                size = 40.dp, iconSize = 20.dp,
                modifier = if (tag != null) Modifier.testTag("$tag-visibility") else Modifier,
            )
        },
    )
}

/** Inline error line under a field. */
@Composable
fun ErrorText(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier, color = Palette.Destructive, style = MaterialTheme.typography.bodySmall)
}

/** Small tertiary footer text, e.g. "Decrypted only in memory…". */
@Composable
fun FootnoteText(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier, color = Palette.TextTertiary, style = MaterialTheme.typography.labelMedium.copy(fontWeight = androidx.compose.ui.text.font.FontWeight.Normal, lineHeight = 18.sp))
}

/** Text button for [ai.alertd.imagethat.ui.SecureAlertDialog]: accent, or [destructive] coral. */
@Composable
fun DialogButton(text: String, onClick: () -> Unit, modifier: Modifier = Modifier, destructive: Boolean = false, enabled: Boolean = true, emphasized: Boolean = true) {
    val color = when {
        !enabled -> Palette.TextTertiary
        destructive -> Palette.Destructive
        emphasized -> Palette.Accent
        else -> Palette.TextSecondary
    }
    Surface(onClick = onClick, enabled = enabled, modifier = modifier.heightIn(min = 44.dp), shape = CircleShape, color = Color.Transparent, contentColor = color) {
        Box(Modifier.padding(horizontal = 14.dp), contentAlignment = Alignment.Center) {
            Text(text, style = MaterialTheme.typography.labelLarge.copy(fontSize = 15.sp))
        }
    }
}

/** Vertical gap helper. */
@Composable
fun Gap(height: Dp) = Spacer(Modifier.height(height))
