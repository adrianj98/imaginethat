// Client-side video thumbnail capture.
//
// The server deliberately has no video decoder (handleThumb returns 204 for
// videos), but the browser decodes MP4/MOV/WebM natively. A hidden <video>
// pointed at the Range-capable /file endpoint fetches only the metadata and
// the few frames around the seek target, so even multi-GB files over a slow
// link cost roughly the moov atom plus one GOP — never the whole file.
//
// Captured frames live in this in-memory Map only. Like the server's
// encrypted thumbnail LRU, they derive from vault content, so they must never
// reach localStorage, IndexedDB, or any other on-disk store.

const cache = new Map<string, string>() // vaultId|path|size → JPEG data URL
const MAX_CACHE = 200

// One capture per <video> element and each holds decoder resources, so cap
// how many run at once. Slow origins (NAS, Drive-backed vaults) are the
// normal case here — a burst of tiles must queue, not stampede the link.
const MAX_ACTIVE = 3
let active = 0
const waiters: (() => void)[] = []

function acquire(): Promise<void> {
  if (active < MAX_ACTIVE) {
    active++
    return Promise.resolve()
  }
  return new Promise((resolve) => waiters.push(resolve))
}

function release(): void {
  const next = waiters.shift()
  if (next) {
    next()
    return
  }
  active--
}

const inflight = new Map<string, Promise<string>>()

/**
 * Returns a JPEG data URL for a frame of the video, capturing it on first
 * request and serving repeats (re-scrolls, re-mounts) from memory.
 */
export function videoThumb(url: string, key: string, maxEdge = 320): Promise<string> {
  const hit = cache.get(key)
  if (hit) return Promise.resolve(hit)

  const pending = inflight.get(key)
  if (pending) return pending

  const p = (async () => {
    await acquire()
    try {
      const dataUrl = await captureFrame(url, maxEdge)
      if (cache.size >= MAX_CACHE) {
        // Map iterates in insertion order, so this evicts the oldest entry.
        const oldest = cache.keys().next().value
        if (oldest !== undefined) cache.delete(oldest)
      }
      cache.set(key, dataUrl)
      return dataUrl
    } finally {
      release()
      inflight.delete(key)
    }
  })()
  inflight.set(key, p)
  return p
}

/** Drops every captured frame, e.g. when a vault locks. */
export function clearVideoThumbs(): void {
  cache.clear()
}

// Generous because the metadata fetch can crawl on a slow origin; the
// concurrency cap above keeps a stuck capture from starving the rest for long.
const CAPTURE_TIMEOUT_MS = 60_000

function captureFrame(url: string, maxEdge: number): Promise<string> {
  return new Promise((resolve, reject) => {
    const video = document.createElement('video')
    video.preload = 'metadata'
    video.muted = true

    let settled = false
    const cleanup = () => {
      clearTimeout(timer)
      video.onerror = null
      video.onloadedmetadata = null
      video.onseeked = null
      // Detach and reload so the browser tears down the decoder and any
      // buffered (decrypted) media data promptly instead of at GC time.
      video.removeAttribute('src')
      video.load()
    }
    const fail = (message: string) => {
      if (settled) return
      settled = true
      cleanup()
      reject(new Error(message))
    }
    const timer = setTimeout(() => fail('video thumbnail timed out'), CAPTURE_TIMEOUT_MS)

    video.onerror = () => fail('video failed to load')
    video.onloadedmetadata = () => {
      // Skip the black or blank first frame most encodes start with, but stay
      // inside clips shorter than two seconds.
      const duration = Number.isFinite(video.duration) ? video.duration : 0
      video.currentTime = Math.min(1, duration / 2)
    }
    video.onseeked = () => {
      if (settled) return
      const w = video.videoWidth
      const h = video.videoHeight
      if (!w || !h) {
        fail('no decodable video track')
        return
      }
      let dw = w
      let dh = h
      if (w > maxEdge || h > maxEdge) {
        if (w >= h) {
          dw = maxEdge
          dh = Math.max(1, Math.round((h * maxEdge) / w))
        } else {
          dh = maxEdge
          dw = Math.max(1, Math.round((w * maxEdge) / h))
        }
      }
      const canvas = document.createElement('canvas')
      canvas.width = dw
      canvas.height = dh
      const ctx = canvas.getContext('2d')
      if (!ctx) {
        fail('canvas unavailable')
        return
      }
      ctx.drawImage(video, 0, 0, dw, dh)
      let dataUrl: string
      try {
        dataUrl = canvas.toDataURL('image/jpeg', 0.8)
      } catch {
        fail('frame capture failed')
        return
      }
      settled = true
      cleanup()
      resolve(dataUrl)
    }

    video.src = url
  })
}
