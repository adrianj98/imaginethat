import { describe, expect, it } from 'vitest'
import { FIT, MAX_SCALE, TOGGLE_SCALE, panBy, toggleAt, zoomBy } from './viewerZoom'

const stage = { width: 800, height: 600 }

describe('viewer zoom', () => {
  it('zooms in and stops at the maximum', () => {
    expect(zoomBy(FIT, 1.25, stage).scale).toBe(1.25)
    expect(zoomBy(FIT, 100, stage).scale).toBe(MAX_SCALE)
  })

  it('stops at fit and recentres', () => {
    const panned = panBy(zoomBy(FIT, 2, stage), 100, 100, stage)
    expect(zoomBy(panned, 0.0001, stage)).toEqual(FIT)
  })

  it('does not pan at fit', () => {
    expect(panBy(FIT, 50, 50, stage)).toEqual(FIT)
  })

  it('pans while zoomed and stops at the edge', () => {
    const z = zoomBy(FIT, 2, stage)
    expect(panBy(z, 100, -50, stage)).toEqual({ scale: 2, x: 100, y: -50 })
    expect(panBy(z, 5000, -5000, stage)).toEqual({ scale: 2, x: 400, y: -300 })
  })

  it('pulls the pan back inside when zooming out', () => {
    const z = panBy(zoomBy(FIT, 2, stage), 5000, -5000, stage)
    expect(zoomBy(z, 0.75, stage)).toEqual({ scale: 1.5, x: 200, y: -150 })
  })

  it('keeps the anchor point in place', () => {
    const z = zoomBy(FIT, 2.5, stage, { x: 100, y: -60 })
    expect(z.x).toBeCloseTo(100 - 100 * 2.5)
    expect(z.y).toBeCloseTo(-60 + 60 * 2.5)
  })

  it('double-click zooms in at the point, then back to fit', () => {
    const z = toggleAt(FIT, { x: 400, y: 300 }, stage)
    expect(z).toEqual({ scale: TOGGLE_SCALE, x: -600, y: -450 })
    expect(toggleAt(z, { x: 0, y: 0 }, stage)).toEqual(FIT)
  })
})
