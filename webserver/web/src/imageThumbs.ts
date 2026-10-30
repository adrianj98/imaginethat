// Client-side image thumbnails, for client-decrypt mode where the server
// cannot make them (it never sees plaintext).
//
// The decrypted image comes from the service worker; it is decoded and
// scaled here, and only the small JPEG is kept. Like videoThumbs.ts, the
// cache is an in-memory Map of data URLs (not blob: URLs, which a browser may
// page to disk) and is dropped when a vault locks.

const cache = new Map<string, string>()
const MAX_CACHE = 400

// Decoding a full-size photo is expensive; a burst of tiles must queue.
const MAX_ACTIVE = 4
let active = 0
const waiters: (() => void)[] = []
const inflight = new Map<string, Promise<string>>()

async function acquire(): Promise<void> {
  if (active < MAX_ACTIVE) {
    active++
    return
  }
  await new Promise<void>((resolve) => waiters.push(resolve))
}

function release(): void {
  const next = waiters.shift()
  if (next) next()
  else active--
}

export function imageThumb(url: string, key: string, maxEdge = 320): Promise<string> {
  const hit = cache.get(key)
  if (hit) return Promise.resolve(hit)
  const pending = inflight.get(key)
  if (pending) return pending

  const p = (async () => {
    await acquire()
    try {
      const res = await fetch(url, { cache: 'no-store' })
      if (!res.ok) throw new Error(`image failed to load (${res.status})`)
      const bitmap = await createImageBitmap(await res.blob())
      try {
        const scale = Math.min(1, maxEdge / Math.max(bitmap.width, bitmap.height))
        const canvas = document.createElement('canvas')
        canvas.width = Math.max(1, Math.round(bitmap.width * scale))
        canvas.height = Math.max(1, Math.round(bitmap.height * scale))
        const ctx = canvas.getContext('2d')
        if (!ctx) throw new Error('canvas unavailable')
        ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height)
        const dataUrl = canvas.toDataURL('image/jpeg', 0.8)
        if (cache.size >= MAX_CACHE) {
          const oldest = cache.keys().next().value
          if (oldest !== undefined) cache.delete(oldest)
        }
        cache.set(key, dataUrl)
        return dataUrl
      } finally {
        bitmap.close()
      }
    } finally {
      release()
      inflight.delete(key)
    }
  })()
  inflight.set(key, p)
  return p
}

/** Drops every thumbnail, e.g. when a vault locks. */
export function clearImageThumbs(): void {
  cache.clear()
}
