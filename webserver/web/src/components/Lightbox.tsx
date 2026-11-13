import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry } from '../api'
import type { Backend } from '../backend'
import Icon from './Icon'
import InfoCard from './InfoCard'
import VideoBar from './VideoBar'
import { FIT, MAX_SCALE, panBy, toggleAt, zoomBy as scaled, type Zoom } from '../viewerZoom'

interface Props {
  backend: Backend
  vaultId: string
  dir: string
  folderName: string
  items: Entry[]
  index: number
  onIndexChange: (index: number) => void
  onClose: () => void
}

const INFO_KEY = 'imagethat.viewerInfo'

function initialShowInfo(): boolean {
  try {
    return localStorage.getItem(INFO_KEY) === '1'
  } catch {
    return false
  }
}

/**
 * Full-screen dark viewer (the macOS DetailView): floating glass toolbars,
 * arrows that fade when the mouse rests, zoom and pan for photos and videos,
 * fullscreen, and an optional info card.
 */
export default function Lightbox({
  backend,
  vaultId,
  dir,
  folderName,
  items,
  index,
  onIndexChange,
  onClose,
}: Props) {
  const item = items[index]
  const [zoomState, setZoom] = useState<Zoom>(FIT)
  const zoom = zoomState.scale
  // The scale as of the last render, for handlers that must decide at once.
  const scaleRef = useRef(1)
  scaleRef.current = zoom
  const [dragging, setDragging] = useState(false)
  const [fullscreen, setFullscreen] = useState(false)
  const [videoEl, setVideoEl] = useState<HTMLVideoElement | null>(null)
  const viewerRef = useRef<HTMLDivElement>(null)
  const stageRef = useRef<HTMLDivElement>(null)
  // Fingers or the mouse button currently down on the stage, by pointer id.
  const pointers = useRef(new Map<number, { x: number; y: number }>())
  const moved = useRef(false)
  // Presses the stage has seen. The grid opens the viewer on the first click
  // of a double-click, so the second lands here: that one must not zoom.
  const presses = useRef(0)
  const [showInfo, setShowInfo] = useState(initialShowInfo)
  const [showArrows, setShowArrows] = useState(true)
  const [dimensions, setDimensions] = useState<{ width: number; height: number } | null>(null)
  const hideTimer = useRef<number | undefined>(undefined)
  const overArrow = useRef(false)

  const step = useCallback(
    (delta: number) => {
      const next = index + delta
      if (next >= 0 && next < items.length) onIndexChange(next)
    },
    [index, items.length, onIndexChange],
  )

  const stageSize = useCallback(() => {
    const r = stageRef.current?.getBoundingClientRect()
    return { width: r?.width ?? 0, height: r?.height ?? 0 }
  }, [])

  /** A viewport point measured from the centre of the stage. */
  const fromCentre = useCallback((clientX: number, clientY: number) => {
    const r = stageRef.current?.getBoundingClientRect()
    if (!r) return { x: 0, y: 0 }
    return { x: clientX - r.left - r.width / 2, y: clientY - r.top - r.height / 2 }
  }, [])

  const zoomBy = useCallback(
    (factor: number, anchor?: { x: number; y: number }) => {
      setZoom((z) => scaled(z, factor, stageSize(), anchor))
    },
    [stageSize],
  )

  useEffect(() => {
    setZoom(FIT)
    setDimensions(null)
  }, [index])

  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) void document.exitFullscreen()
    else void viewerRef.current?.requestFullscreen?.().catch(() => {})
  }, [])

  // Fullscreen follows the browser (Esc and the browser's own exit count too),
  // and a resize keeps a panned photo inside the stage.
  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === viewerRef.current)
    const onResize = () => setZoom((z) => panBy(z, 0, 0, stageSize()))
    document.addEventListener('fullscreenchange', onChange)
    window.addEventListener('resize', onResize)
    return () => {
      document.removeEventListener('fullscreenchange', onChange)
      window.removeEventListener('resize', onResize)
      if (document.fullscreenElement) void document.exitFullscreen()
    }
  }, [stageSize])

  // Wheel and trackpad pinch. Registered by hand because React's onWheel is
  // passive, and a pinch (ctrl+wheel) must not also zoom the whole page.
  useEffect(() => {
    const stage = stageRef.current
    if (!stage) return
    function onWheel(e: WheelEvent) {
      const anchor = fromCentre(e.clientX, e.clientY)
      if (e.ctrlKey) {
        e.preventDefault()
        zoomBy(Math.exp(-e.deltaY * 0.01), anchor)
      } else if (scaleRef.current > 1) {
        e.preventDefault()
        zoomBy(e.deltaY < 0 ? 1.1 : 1 / 1.1, anchor)
      }
    }
    // Safari reports a trackpad pinch as gesture events, with a running scale.
    let last = 1
    function onGesture(e: Event) {
      e.preventDefault()
      const g = e as Event & { scale: number; clientX: number; clientY: number }
      if (e.type === 'gesturestart') last = 1
      else zoomBy(g.scale / last, fromCentre(g.clientX, g.clientY))
      last = e.type === 'gesturestart' ? 1 : g.scale
    }
    stage.addEventListener('wheel', onWheel, { passive: false })
    stage.addEventListener('gesturestart', onGesture)
    stage.addEventListener('gesturechange', onGesture)
    return () => {
      stage.removeEventListener('wheel', onWheel)
      stage.removeEventListener('gesturestart', onGesture)
      stage.removeEventListener('gesturechange', onGesture)
    }
  }, [fromCentre, stageSize, zoomBy])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      switch (e.key) {
        case 'Escape':
          // Leave fullscreen first; the next press closes the viewer.
          if (document.fullscreenElement) void document.exitFullscreen()
          else onClose()
          break
        case 'f':
        case 'F':
          if (!e.metaKey && !e.ctrlKey && !e.altKey) toggleFullscreen()
          break
        case '0':
          setZoom(FIT)
          break
        case 'ArrowLeft':
          step(-1)
          break
        case 'ArrowRight':
        case ' ':
          e.preventDefault()
          step(1)
          break
        case '+':
        case '=':
          zoomBy(1.25)
          break
        case '-':
          zoomBy(1 / 1.25)
          break
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [step, onClose, zoomBy, toggleFullscreen])

  // Warm the next image so stepping through a folder feels instant, the same
  // trick AppState.preloadImage plays in the macOS app.
  useEffect(() => {
    const next = items[index + 1]
    if (next && next.mediaType === 'image') {
      const img = new Image()
      img.src = backend.fileUrl(vaultId, dir, next)
    }
  }, [backend, vaultId, dir, items, index])

  // Arrows show on mouse movement and fade after two idle seconds, unless
  // the pointer rests on one of them.
  const scheduleHide = useCallback(() => {
    window.clearTimeout(hideTimer.current)
    hideTimer.current = window.setTimeout(() => {
      if (!overArrow.current) setShowArrows(false)
    }, 2000)
  }, [])

  useEffect(() => {
    scheduleHide()
    return () => window.clearTimeout(hideTimer.current)
  }, [scheduleHide])

  function toggleInfo() {
    setShowInfo((on) => {
      try {
        localStorage.setItem(INFO_KEY, on ? '0' : '1')
      } catch {
        // Not remembered; harmless.
      }
      return !on
    })
  }

  if (!item) return null
  const src = backend.fileUrl(vaultId, dir, item)
  const isImage = item.mediaType === 'image'

  const zoomed = zoom > 1
  const transform = zoomed
    ? { transform: `translate(${zoomState.x}px, ${zoomState.y}px) scale(${zoom})` }
    : undefined

  function pinchSpan() {
    const [a, b] = [...pointers.current.values()]
    return { dist: Math.hypot(a.x - b.x, a.y - b.y), x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
  }

  function onPointerDown(e: React.PointerEvent<HTMLDivElement>) {
    if (e.pointerType === 'mouse' && e.button !== 0) return
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    moved.current = false
    presses.current += 1
    // One pointer drags a zoomed picture; two pinch. At fit a single pointer
    // is left alone, so the video's own controls keep working.
    if (zoomed || pointers.current.size > 1) {
      try {
        e.currentTarget.setPointerCapture(e.pointerId)
      } catch {
        // The pointer is already gone; the drag just ends at the stage edge.
      }
      setDragging(true)
    }
  }

  function onPointerMove(e: React.PointerEvent<HTMLDivElement>) {
    const prev = pointers.current.get(e.pointerId)
    if (!prev) return
    if (pointers.current.size >= 2) {
      const before = pinchSpan()
      pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      const after = pinchSpan()
      moved.current = true
      setZoom((z) => {
        const factor = before.dist > 0 ? after.dist / before.dist : 1
        // Zoom about where the fingers were, then follow them to where they are.
        const pinched = scaled(z, factor, stageSize(), fromCentre(before.x, before.y))
        return panBy(pinched, after.x - before.x, after.y - before.y, stageSize())
      })
      return
    }
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
    if (!dragging) return
    const dx = e.clientX - prev.x
    const dy = e.clientY - prev.y
    if (dx !== 0 || dy !== 0) moved.current = true
    setZoom((z) => panBy(z, dx, dy, stageSize()))
  }

  function onPointerUp(e: React.PointerEvent<HTMLDivElement>) {
    pointers.current.delete(e.pointerId)
    if (pointers.current.size === 0) setDragging(false)
  }

  const arrowProps = {
    onMouseEnter: () => {
      overArrow.current = true
    },
    onMouseLeave: () => {
      overArrow.current = false
      scheduleHide()
    },
  }

  return (
    <div
      ref={viewerRef}
      className={`viewer dark-scope${showInfo ? ' with-info' : ''}`}
      role="dialog"
      aria-modal="true"
      aria-label={item.name}
      onMouseMove={() => {
        setShowArrows(true)
        scheduleHide()
      }}
    >
      <header className="toolbar">
        <button className="capsule glass grid-btn" type="button" onClick={onClose} title="Back to grid">
          <Icon name="grid" size={15} />
          Grid
        </button>

        <div className="capsule text glass crumbs">
          <span className="parent">{folderName}</span>
          <span className="sep">
            <Icon name="chevron-right" size={10} />
          </span>
          <span className="title" title={item.name}>
            {item.name}
          </span>
          <span className="count">
            {index + 1} of {items.length}
          </span>
        </div>

        <div className="spacer" />

        <div className="capsule glass">
          <button
            className="icon-btn"
            type="button"
            title="Zoom Out"
            aria-label="Zoom Out"
            disabled={zoom <= 1}
            onClick={() => zoomBy(1 / 1.25)}
          >
            <Icon name="minus" />
          </button>
          <button
            className="zoom-label"
            type="button"
            title="Zoom to Fit"
            aria-label="Zoom to Fit"
            onClick={() => setZoom(FIT)}
          >
            {zoomed ? `${Math.round(zoom * 100)}%` : 'Fit'}
          </button>
          <button
            className="icon-btn"
            type="button"
            title="Zoom In"
            aria-label="Zoom In"
            disabled={zoom >= MAX_SCALE}
            onClick={() => zoomBy(1.25)}
          >
            <Icon name="plus" />
          </button>
          {document.fullscreenEnabled && (
            <button
              className={`icon-btn${fullscreen ? ' on' : ''}`}
              type="button"
              title={fullscreen ? 'Exit Full Screen' : 'Enter Full Screen'}
              aria-label={fullscreen ? 'Exit Full Screen' : 'Enter Full Screen'}
              aria-pressed={fullscreen}
              onClick={toggleFullscreen}
            >
              <Icon name={fullscreen ? 'compress' : 'expand'} />
            </button>
          )}
        </div>

        <div className="capsule glass">
          <a className="icon-btn" href={backend.fileUrl(vaultId, dir, item, true)} title="Download" aria-label="Download">
            <Icon name="download" />
          </a>
          <button
            className={`icon-btn${showInfo ? ' on' : ''}`}
            type="button"
            title={showInfo ? 'Hide Info' : 'Show Info'}
            aria-label={showInfo ? 'Hide Info' : 'Show Info'}
            aria-pressed={showInfo}
            onClick={toggleInfo}
          >
            <Icon name="sidebar-right" />
          </button>
          <button className="icon-btn" type="button" title="Close" aria-label="Close" onClick={onClose}>
            <Icon name="close" />
          </button>
        </div>
      </header>

      <div
        ref={stageRef}
        className={`viewer-stage${zoomed ? ' zoomed' : ''}${dragging ? ' dragging' : ''}`}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerUp}
        onClick={() => {
          // A zoomed video has no browser controls: a click that wasn't a
          // drag plays or pauses it.
          if (!zoomed || moved.current || !videoEl || isImage) return
          if (videoEl.paused) void videoEl.play()
          else videoEl.pause()
        }}
        onDoubleClick={(e) => {
          if (presses.current < 2) return
          const point = fromCentre(e.clientX, e.clientY)
          setZoom((z) => toggleAt(z, point, stageSize()))
        }}
      >
        {item.mediaType === 'video' ? (
          <>
            {/* Range requests make this seekable straight out of the vault.
                The browser's controls are part of the picture, so while zoomed
                they are off and VideoBar stands in; a click plays or pauses. */}
            <video
              key={src}
              ref={setVideoEl}
              src={src}
              controls={!zoomed}
              controlsList="nofullscreen"
              autoPlay
              playsInline
              style={transform}
            />
            {zoomed && videoEl && <VideoBar video={videoEl} />}
            {api.isLikelyUnplayable(item.name) && (
              <p className="note">
                Most browsers cannot play {item.name.split('.').pop()?.toUpperCase()} files. Download
                it to watch in a native player.
              </p>
            )}
          </>
        ) : (
          <img
            src={src}
            alt={item.name}
            draggable={false}
            style={transform}
            onLoad={(e) =>
              setDimensions({ width: e.currentTarget.naturalWidth, height: e.currentTarget.naturalHeight })
            }
          />
        )}
      </div>

      {index > 0 && (
        <button
          className={`nav prev glass${showArrows ? '' : ' hidden'}`}
          type="button"
          onClick={() => step(-1)}
          aria-label="Previous"
          {...arrowProps}
        >
          <Icon name="chevron-left" size={18} />
        </button>
      )}
      {index < items.length - 1 && (
        <button
          className={`nav next glass${showArrows ? '' : ' hidden'}`}
          type="button"
          onClick={() => step(1)}
          aria-label="Next"
          {...arrowProps}
        >
          <Icon name="chevron-right" size={18} />
        </button>
      )}

      {showInfo && (
        <InfoCard
          backend={backend}
          vaultId={vaultId}
          dir={dir}
          entry={item}
          folderName={folderName}
          itemCount={items.length}
          dimensions={isImage ? dimensions : null}
          showPreview={false}
        />
      )}
    </div>
  )
}
