import { useCallback, useEffect } from 'react'
import * as api from '../api'
import type { Entry } from '../api'

interface Props {
  vaultId: string
  dir: string
  items: Entry[]
  index: number
  onIndexChange: (index: number) => void
  onClose: () => void
}

/** Full-screen viewer with keyboard navigation and next-image preloading. */
export default function Lightbox({ vaultId, dir, items, index, onIndexChange, onClose }: Props) {
  const item = items[index]

  const step = useCallback(
    (delta: number) => {
      const next = index + delta
      if (next >= 0 && next < items.length) onIndexChange(next)
    },
    [index, items.length, onIndexChange],
  )

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
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [step, onClose])

  // Warm the next image so stepping through a folder feels instant, the same
  // trick AppState.preloadImage plays in the macOS app.
  useEffect(() => {
    const next = items[index + 1]
    if (next && next.mediaType === 'image') {
      const img = new Image()
      img.src = api.fileUrl(vaultId, api.joinPath(dir, next.name))
    }
  }, [vaultId, dir, items, index])

  if (!item) return null
  const src = api.fileUrl(vaultId, api.joinPath(dir, item.name))

  return (
    <div className="lightbox" role="dialog" aria-modal="true" aria-label={item.name}>
      <div className="lightbox-backdrop" onClick={onClose} />

      <div className="lightbox-bar">
        <span className="lightbox-title">{item.name}</span>
        <span className="muted">
          {index + 1} / {items.length}
        </span>
        <a className="btn" href={api.fileUrl(vaultId, api.joinPath(dir, item.name), true)}>
          Download
        </a>
        <button className="btn" type="button" onClick={onClose} aria-label="Close">
          ✕
        </button>
      </div>

      <div className="lightbox-stage">
        {index > 0 && (
          <button className="nav prev" type="button" onClick={() => step(-1)} aria-label="Previous">
            ‹
          </button>
        )}

        {item.mediaType === 'video' ? (
          <div className="video-wrap">
            {/* Range requests make this seekable straight out of the vault. */}
            <video key={src} src={src} controls autoPlay playsInline />
            {api.isLikelyUnplayable(item.name) && (
              <p className="muted note">
                Most browsers cannot play {item.name.split('.').pop()?.toUpperCase()} files. Download
                it to watch in a native player.
              </p>
            )}
          </div>
        ) : (
          <img className="lightbox-image" src={src} alt={item.name} />
        )}

        {index < items.length - 1 && (
          <button className="nav next" type="button" onClick={() => step(1)} aria-label="Next">
            ›
          </button>
        )}
      </div>
    </div>
  )
}
