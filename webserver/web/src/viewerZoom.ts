// Zoom and pan of the photo or video in the viewer, the same arithmetic as
// the macOS app's ViewerZoom. Scale 1 is "fit"; x and y move the zoomed media
// and are in CSS pixels. Points are measured from the centre of the stage.

export interface Zoom {
  scale: number
  x: number
  y: number
}

export interface Size {
  width: number
  height: number
}

export const MIN_SCALE = 1
export const MAX_SCALE = 10
/** Where a double-click zooms to. */
export const TOGGLE_SCALE = 2.5
export const FIT: Zoom = { scale: 1, x: 0, y: 0 }

const clamp = (v: number, limit: number) => Math.max(-limit, Math.min(v, limit))

/** Keeps the zoomed stage covering the stage, so the media can't be dragged out of sight. */
function contain(z: Zoom, stage: Size): Zoom {
  if (z.scale <= MIN_SCALE) return FIT
  const maxX = Math.max(0, (stage.width * (z.scale - 1)) / 2)
  const maxY = Math.max(0, (stage.height * (z.scale - 1)) / 2)
  return { scale: z.scale, x: clamp(z.x, maxX), y: clamp(z.y, maxY) }
}

/** Multiplies the zoom, keeping the anchor point where it is on screen. */
export function zoomBy(z: Zoom, factor: number, stage: Size, anchor = { x: 0, y: 0 }): Zoom {
  const scale = Math.max(MIN_SCALE, Math.min(z.scale * factor, MAX_SCALE))
  const ratio = scale / z.scale
  return contain(
    { scale, x: anchor.x - (anchor.x - z.x) * ratio, y: anchor.y - (anchor.y - z.y) * ratio },
    stage,
  )
}

export function panBy(z: Zoom, dx: number, dy: number, stage: Size): Zoom {
  return contain({ scale: z.scale, x: z.x + dx, y: z.y + dy }, stage)
}

/** Double-click: zoom in on the point, or back to fit when already zoomed. */
export function toggleAt(z: Zoom, point: { x: number; y: number }, stage: Size): Zoom {
  return z.scale > MIN_SCALE ? FIT : zoomBy(z, TOGGLE_SCALE, stage, point)
}
