import { useEffect, useState } from 'react'
import Icon from './Icon'

function clock(seconds: number): string {
  if (!Number.isFinite(seconds)) return '0:00'
  const s = Math.floor(seconds)
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  const ss = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m % 60).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

/**
 * Play, seek and time for a zoomed video. The browser's own controls are part
 * of the picture and would be zoomed and panned with it, so they are switched
 * off while zoomed and this bar stands in at normal size.
 */
export default function VideoBar({ video }: { video: HTMLVideoElement }) {
  const [, tick] = useState(0)

  useEffect(() => {
    const update = () => tick((n) => n + 1)
    const events = ['timeupdate', 'durationchange', 'play', 'pause', 'seeked']
    events.forEach((e) => video.addEventListener(e, update))
    return () => events.forEach((e) => video.removeEventListener(e, update))
  }, [video])

  const duration = Number.isFinite(video.duration) ? video.duration : 0

  return (
    <div
      className="capsule glass video-bar"
      onPointerDown={(e) => e.stopPropagation()}
      onDoubleClick={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
    >
      <button
        className="icon-btn"
        type="button"
        title={video.paused ? 'Play' : 'Pause'}
        aria-label={video.paused ? 'Play' : 'Pause'}
        onClick={() => (video.paused ? void video.play() : video.pause())}
      >
        <Icon name={video.paused ? 'play' : 'pause'} />
      </button>
      <input
        type="range"
        aria-label="Seek"
        min={0}
        max={duration || 1}
        step="any"
        value={Math.min(video.currentTime, duration || 1)}
        onChange={(e) => {
          video.currentTime = Number(e.target.value)
        }}
      />
      <span className="time">
        {clock(video.currentTime)} / {clock(duration)}
      </span>
    </div>
  )
}
