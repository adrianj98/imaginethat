import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry } from '../api'
import type { Backend } from '../backend'
import Icon from './Icon'
import InfoCard from './InfoCard'

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
 * arrows that fade when the mouse rests, zoom, and an optional info card.
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
  const [zoom, setZoom] = useState(1)
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

  const zoomBy = useCallback((factor: number) => {
    setZoom((z) => Math.min(10, Math.max(1, z * factor)))
  }, [])

  useEffect(() => {
    setZoom(1)
    setDimensions(null)
  }, [index])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      switch (e.key) {
        case 'Escape':
          onClose()
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
  }, [step, onClose, zoomBy])

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

        {isImage && (
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
            <span className="zoom-label">{zoom <= 1 ? 'Fit' : `${Math.round(zoom * 100)}%`}</span>
            <button
              className="icon-btn"
              type="button"
              title="Zoom In"
              aria-label="Zoom In"
              disabled={zoom >= 10}
              onClick={() => zoomBy(1.25)}
            >
              <Icon name="plus" />
            </button>
          </div>
        )}

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
        className="viewer-stage"
        onWheel={(e) => {
          if (isImage && (e.ctrlKey || zoom > 1)) zoomBy(e.deltaY < 0 ? 1.1 : 1 / 1.1)
        }}
      >
        {item.mediaType === 'video' ? (
          <>
            {/* Range requests make this seekable straight out of the vault. */}
            <video key={src} src={src} controls autoPlay playsInline />
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
            style={zoom > 1 ? { transform: `scale(${zoom})` } : undefined}
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
