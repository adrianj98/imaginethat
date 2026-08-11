import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry } from '../api'

interface Props {
  vaultId: string
  dir: string
  entry: Entry
}

/**
 * Lazily-loaded preview tile.
 *
 * Thumbnails are only requested once the tile scrolls into view — a folder of
 * a few thousand photos would otherwise ask the server to decrypt and scale
 * every one of them at once.
 */
export default function Thumbnail({ vaultId, dir, entry }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    const el = ref.current
    if (!el || visible) return
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisible(true)
          observer.disconnect()
        }
      },
      { rootMargin: '200px' },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [visible])

  const path = api.joinPath(dir, entry.name)
  const showImage = visible && !failed && !entry.isDir && api.hasThumbnail(entry.name)

  return (
    <div className="thumb" ref={ref}>
      {entry.isDir ? (
        <span className="thumb-glyph" aria-hidden="true">
          📁
        </span>
      ) : showImage ? (
        <img
          src={api.thumbUrl(vaultId, path)}
          alt=""
          loading="lazy"
          decoding="async"
          onError={() => setFailed(true)}
        />
      ) : (
        <span className="thumb-glyph" aria-hidden="true">
          {entry.mediaType === 'video' ? '🎞️' : entry.mediaType === 'image' ? '🖼️' : '📄'}
        </span>
      )}
    </div>
  )
}
