import { useEffect, useRef, useState } from 'react'
import * as api from '../api'
import type { Entry } from '../api'
import type { Backend } from '../backend'
import { imageThumb } from '../imageThumbs'
import { videoThumb } from '../videoThumbs'
import Icon from './Icon'

interface Props {
  backend: Backend
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
 *
 * In client-decrypt mode the server cannot make thumbnails, so images are
 * scaled and video frames captured here, from the service worker's decrypted
 * URL (see imageThumbs.ts).
 */
export default function Thumbnail({ backend, vaultId, dir, entry }: Props) {
  const clientSide = backend.mode === 'client-decrypt'
  const ref = useRef<HTMLDivElement>(null)
  const [visible, setVisible] = useState(false)
  const [failed, setFailed] = useState(false)
  const [serverThumbFailed, setServerThumbFailed] = useState(false)
  const [localSrc, setLocalSrc] = useState<string | null>(null)

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
  const canScaleLocally = !entry.isDir && api.hasThumbnail(entry.name)

  useEffect(() => {
    if (!visible || localSrc || failed) return
    let capture: Promise<string>
    const key = `${vaultId}|${path}|320`
    const url = backend.fileUrl(vaultId, dir, entry)
    if (clientSide && canScaleLocally) capture = imageThumb(url, key)
    else if (canCaptureLocally && (clientSide || serverThumbFailed)) capture = videoThumb(url, key)
    else return
    let cancelled = false
    capture
      .then((dataUrl) => {
        if (!cancelled) setLocalSrc(dataUrl)
      })
      .catch(() => {
        if (!cancelled) setFailed(true)
      })
    // The capture itself is not aborted on unmount — it stays cached for the
    // tile's likely return — but a stale result must not touch state.
    return () => {
      cancelled = true
    }
  }, [visible, clientSide, canScaleLocally, canCaptureLocally, serverThumbFailed, localSrc, failed, backend, vaultId, dir, entry, path])

  // Images always have a server thumbnail; videos only until proven otherwise.
  const showImage =
    !clientSide &&
    visible &&
    !failed &&
    !entry.isDir &&
    (api.hasThumbnail(entry.name) || (isVideo && !serverThumbFailed))

  return (
    <div className="thumb" ref={ref}>
      {entry.isDir ? (
        <>
          <span className="folder-glyph">
            <Icon name="folder" size={44} />
          </span>
          <span className="folder-name">{entry.name}</span>
        </>
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
      ) : localSrc ? (
        <img src={localSrc} alt="" decoding="async" />
      ) : (
        <Icon
          name={entry.mediaType === 'video' ? 'film' : entry.mediaType === 'image' ? 'photo' : 'doc'}
          size={32}
        />
      )}
    </div>
  )
}
