// Service worker for client-decrypt mode.
//
// It answers requests for /decrypted/<vaultId>/<cipherPath>?name=<plainName>
// by fetching the ciphertext from the server's raw endpoint and decrypting it
// here, so <img>, <video> (with seeking) and downloads work on plain URLs
// while the server only ever sends ciphertext.
//
// Keys are never stored in the worker. Browsers stop idle workers at will,
// and a vault's keys must die with the page that unlocked it, so each request
// asks the page for the vault's content key over a MessageChannel. Responses
// are built here and never enter the HTTP cache.

import { HEADER_LEN, PLAIN_BS, cipherRange, decryptBlocks, parseHeader, plainSize } from './crypto/content'
import { contentType } from './media'
import { DECRYPTED_PREFIX, KEY_REQUEST } from './swProtocol'

declare const self: ServiceWorkerGlobalScope

// Plaintext bytes decrypted per network round trip while streaming: big
// enough to keep a fast link busy, small enough that a seek wastes little.
const CHUNK_BLOCKS = 256 // 1 MiB

self.addEventListener('install', () => {
  void self.skipWaiting()
})

self.addEventListener('activate', (event) => {
  // Take over the already-open page so its first <video> is decrypted too.
  event.waitUntil(self.clients.claim())
})

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url)
  if (url.origin !== self.location.origin || !url.pathname.startsWith(DECRYPTED_PREFIX)) return
  event.respondWith(serve(event, url))
})

class HttpError extends Error {
  constructor(readonly status: number, message: string) {
    super(message)
  }
}

async function serve(event: FetchEvent, url: URL): Promise<Response> {
  try {
    const rest = url.pathname.slice(DECRYPTED_PREFIX.length)
    const slash = rest.indexOf('/')
    if (slash <= 0) throw new HttpError(400, 'bad decrypted URL')
    const vaultId = decodeURIComponent(rest.slice(0, slash))
    const cipherPath = rest.slice(slash + 1).split('/').map(decodeURIComponent).join('/')
    const name = url.searchParams.get('name') ?? 'file'

    const key = await keyFor(event, vaultId)
    if (!key) throw new HttpError(403, 'vault is locked')

    const rawURL = `/api/vaults/${encodeURIComponent(vaultId)}/raw/file?path=${encodeURIComponent(cipherPath)}`
    const head = await fetchRange(rawURL, 0, HEADER_LEN - 1)
    const size = plainSize(head.total)
    const fileID = head.total === 0 ? new Uint8Array(0) : parseHeader(head.bytes)

    const range = parseRange(event.request.headers.get('Range'), size)
    const headers = new Headers({
      'Content-Type': contentType(name),
      'Accept-Ranges': 'bytes',
      'Cache-Control': 'no-store',
      'X-Content-Type-Options': 'nosniff',
    })
    if (url.searchParams.get('download') === '1') {
      headers.set('Content-Disposition', `attachment; filename*=UTF-8''${encodeURIComponent(name)}`)
    }
    if (range === 'unsatisfiable') {
      headers.set('Content-Range', `bytes */${size}`)
      return new Response(null, { status: 416, headers })
    }
    const [start, end] = range ?? [0, size - 1]
    headers.set('Content-Length', String(Math.max(0, end - start + 1)))
    if (range) headers.set('Content-Range', `bytes ${start}-${end}/${size}`)
    const body = size === 0 ? null : decryptingStream(rawURL, key, fileID, start, end)
    return new Response(body, { status: range ? 206 : 200, headers })
  } catch (e) {
    const status = e instanceof HttpError ? e.status : 502
    return new Response(e instanceof Error ? e.message : String(e), {
      status,
      headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' },
    })
  }
}

/** Asks the page that made the request (or any open page) for a vault key. */
async function keyFor(event: FetchEvent, vaultId: string): Promise<CryptoKey | null> {
  const candidates: Client[] = []
  if (event.clientId) {
    const c = await self.clients.get(event.clientId)
    if (c) candidates.push(c)
  }
  // Navigations (a download link) have no requesting client yet.
  for (const c of await self.clients.matchAll({ type: 'window' })) {
    if (!candidates.some((x) => x.id === c.id)) candidates.push(c)
  }
  for (const client of candidates) {
    const key = await askForKey(client, vaultId)
    if (key) return key
  }
  return null
}

function askForKey(client: Client, vaultId: string): Promise<CryptoKey | null> {
  return new Promise((resolve) => {
    const channel = new MessageChannel()
    const timer = setTimeout(() => resolve(null), 2000)
    channel.port1.onmessage = (e) => {
      clearTimeout(timer)
      resolve(e.data instanceof CryptoKey ? e.data : null)
    }
    client.postMessage({ type: KEY_REQUEST, vaultId }, [channel.port2])
  })
}

/** Fetches ciphertext bytes [start, end] and the file's total size. */
async function fetchRange(url: string, start: number, end: number): Promise<{ bytes: Uint8Array<ArrayBuffer>; total: number }> {
  const res = await fetch(url, { headers: { Range: `bytes=${start}-${end}` }, cache: 'no-store' })
  if (res.status === 401) throw new HttpError(401, 'login required')
  if (res.status === 404) throw new HttpError(404, 'not found')
  if (res.status === 416) return { bytes: new Uint8Array(0), total: 0 }
  if (!res.ok) throw new HttpError(502, `server returned ${res.status}`)
  const bytes = new Uint8Array(await res.arrayBuffer())
  let total = bytes.length
  const cr = res.headers.get('Content-Range')
  const m = cr && /\/(\d+)$/.exec(cr)
  if (m) total = Number(m[1])
  return { bytes, total }
}

/** Parses a single-range Range header against a plaintext size. */
function parseRange(header: string | null, size: number): [number, number] | null | 'unsatisfiable' {
  if (!header) return null
  const m = /^bytes=(\d*)-(\d*)$/.exec(header.trim())
  if (!m || (m[1] === '' && m[2] === '')) return null
  let start: number
  let end: number
  if (m[1] === '') {
    start = Math.max(0, size - Number(m[2]))
    end = size - 1
  } else {
    start = Number(m[1])
    end = m[2] === '' ? size - 1 : Math.min(Number(m[2]), size - 1)
  }
  if (start >= size || start > end) return 'unsatisfiable'
  return [start, end]
}

/**
 * Streams plaintext bytes [start, end], fetching and decrypting one chunk
 * per pull, so a large file never sits whole in memory and a cancelled
 * stream (a seek, a closed player) stops fetching.
 */
function decryptingStream(rawURL: string, key: CryptoKey, fileID: Uint8Array<ArrayBuffer>, start: number, end: number): ReadableStream<Uint8Array> {
  let pos = start
  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      if (pos > end) {
        controller.close()
        return
      }
      // Align each chunk to the block grid so chunks never re-fetch a block.
      const blockStart = Math.floor(pos / PLAIN_BS)
      const chunkEnd = Math.min(end, (blockStart + CHUNK_BLOCKS) * PLAIN_BS - 1)
      const r = cipherRange(pos, chunkEnd - pos + 1)
      const { bytes } = await fetchRange(rawURL, r.start, r.end - 1)
      if (bytes.length === 0) throw new Error('short read')
      const plain = await decryptBlocks(key, bytes, r.firstBlock, fileID)
      const want = chunkEnd - pos + 1
      const slice = plain.subarray(r.skip, r.skip + want)
      if (slice.length !== want) throw new Error('file is shorter than expected')
      controller.enqueue(slice)
      pos += want
    },
  })
}
