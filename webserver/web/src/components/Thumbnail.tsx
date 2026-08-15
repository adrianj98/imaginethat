import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry } from '../api'
import { videoThumb } from '../videoThumbs'

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
 *
 * Still images come from the server's /thumb endpoint. Videos try that same
 * endpoint first (the server answers when it has ffmpeg); a 204 or failure
 * falls back to capturing a frame in the browser (see videoThumbs.ts).
 */
export default function Thumbnail({ vaultId, dir, entry }: Props) {
  const ref = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  const [failed, setFailed] = useState(false)
  const [serverThumbFailed, setServerThumbFailed] = useState(false)
  const [videoSrc, setVideoSrc] = useState<string | null>(null)

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
  const isVideo = !entry.isDir && entry.mediaType === 'video'
  // In-browser capture needs the browser itself to decode the container;
  // server-side ffmpeg does not, so only the fallback is gated on this.
  const canCaptureLocally = isVideo && !api.isLikelyUnplayable(entry.name)

  useEffect(() => {
    if (!visible || !canCaptureLocally || !serverThumbFailed || videoSrc || failed) return
    let cancelled = false
    videoThumb(api.fileUrl(vaultId, path), `${vaultId}|${path}|320`)
      .then((dataUrl) => {
        if (!cancelled) setVideoSrc(dataUrl)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    // The capture itself is not aborted on unmount — it stays cached for the
    // tile's likely return — but a stale result must not touch state.
    return () => {
      cancelled = true
    }
  }, [visible, canCaptureLocally, serverThumbFailed, videoSrc, failed, vaultId, path])

  // Images always have a server thumbnail; videos only until proven otherwise.
  const showImage =
    visible &&
    !failed &&
    !entry.isDir &&
    (api.hasThumbnail(entry.name) || (isVideo && !serverThumbFailed))

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
          onError={() => {
            // For videos a 204 means "no ffmpeg here" — hand over to the
            // in-browser capture rather than giving up on the tile.
            if (isVideo) setServerThumbFailed(true)
            else setFailed(true)
          }}
        />
      ) : videoSrc ? (
        <img src={videoSrc} alt="" decoding="async" />
      ) : (
        <span className="thumb-glyph" aria-hidden="true">
          {entry.mediaType === 'video' ? '🎞️' : entry.mediaType === 'image' ? '🖼️' : '📄'}
        </span>
      )}
    </div>
  )
}
