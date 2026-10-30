// Client-decrypt mode: vaults are unlocked and decrypted in this page.
//
// The server sends only ciphertext (its raw/ endpoints). Keys live in the
// `keys` map below and nowhere else — not in storage, not in the service
// worker — so reloading or closing the tab locks every vault. The service
// worker asks this page for a key each time it decrypts a file.

import { ApiError, type Entry, type Listing } from './api'
import { plainSize } from './crypto/content'
import { decryptName, isLongName, isSpecialName } from './crypto/names'
import { type VaultKeys, unlock as unlockConf } from './crypto/vault'
import { mediaType } from './media'
import { DECRYPTED_PREFIX, KEY_REQUEST } from './swProtocol'

interface RawEntry {
  name: string
  isDir: boolean
  size: number
}

interface RawListing {
  entries: RawEntry[]
  dirIV?: string
  longNames: Record<string, string>
}

/** A decrypted directory entry, remembering where its ciphertext lives. */
export interface ClientEntry extends Entry {
  cipherPath: string
}

const keys = new Map<string, VaultKeys>()
// Per vault: plaintext directory path → its ciphertext path and entries.
const dirs = new Map<string, Map<string, { cipherDir: string; entries: ClientEntry[] }>>()

async function raw(id: string, op: 'list' | 'file', cipherPath: string): Promise<Response> {
  const res = await fetch(`/api/vaults/${encodeURIComponent(id)}/raw/${op}?path=${encodeURIComponent(cipherPath)}`, {
    credentials: 'same-origin',
    cache: 'no-store',
  })
  if (!res.ok) {
    let message = res.statusText
    try {
      message = (await res.json())?.error ?? message
    } catch {
      // keep the status text
    }
    throw new ApiError(res.status, message)
  }
  return res
}

export function isUnlocked(id: string): boolean {
  return keys.has(id)
}

/** Unlocks a vault in this page. The password goes nowhere but scrypt. */
export async function unlock(id: string, password: string): Promise<void> {
  const conf = await (await raw(id, 'file', 'gocryptfs.conf')).text()
  keys.set(id, await unlockConf(conf, password))
  dirs.set(id, new Map())
}

export function lock(id: string): void {
  keys.delete(id)
  dirs.delete(id)
}

export function lockAll(): void {
  keys.clear()
  dirs.clear()
}

function base64Decode(s: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0))
}

async function readDir(id: string, cipherDir: string, plainDir: string): Promise<ClientEntry[]> {
  const k = keys.get(id)
  if (!k) throw new ApiError(401, 'vault is locked')
  const listing: RawListing = await (await raw(id, 'list', cipherDir)).json()
  const iv = listing.dirIV ? base64Decode(listing.dirIV) : null
  if (k.nameKey && !iv) throw new Error(`folder “${plainDir || '/'}” has no gocryptfs.diriv`)

  const decrypted = await Promise.all(
    listing.entries.map(async (e): Promise<ClientEntry | null> => {
      if (isSpecialName(e.name)) return null
      let name = e.name
      if (k.nameKey && iv) {
        const encrypted = isLongName(e.name) ? listing.longNames[e.name] : e.name
        if (!encrypted) return null
        try {
          name = await decryptName(k.nameKey, iv, encrypted.trim())
        } catch {
          // Like the server: one unreadable name must not hide the folder.
          return null
        }
      }
      if (name.startsWith('.')) return null
      let size = 0
      if (!e.isDir) {
        try {
          size = plainSize(e.size)
        } catch {
          size = 0
        }
      }
      return {
        name,
        isDir: e.isDir,
        size,
        mediaType: e.isDir ? '' : mediaType(name),
        cipherPath: cipherDir ? `${cipherDir}/${e.name}` : e.name,
      }
    }),
  )
  const out = decrypted.filter((e): e is ClientEntry => e !== null)
  // Directories first, then case-insensitive by name, as the server sorts.
  out.sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    const x = a.name.toLowerCase()
    const y = b.name.toLowerCase()
    return x < y ? -1 : x > y ? 1 : 0
  })
  return out
}

/** Lists a plaintext directory, resolving its ciphertext path on the way. */
export async function listDir(id: string, plainPath: string): Promise<Listing> {
  const cache = dirs.get(id)
  if (!cache) throw new ApiError(401, 'vault is locked')

  // Walk from the root, so a deep link resolves each folder's encrypted name.
  let cipherDir = ''
  let plainDir = ''
  const parts = plainPath ? plainPath.split('/') : []
  for (let i = 0; i <= parts.length; i++) {
    let dir = cache.get(plainDir)
    // The target folder itself is always re-read so the view is current.
    if (!dir || i === parts.length) {
      dir = { cipherDir, entries: await readDir(id, cipherDir, plainDir) }
      cache.set(plainDir, dir)
    }
    if (i === parts.length) break
    const next = dir.entries.find((e) => e.isDir && e.name === parts[i])
    if (!next) throw new ApiError(404, `no such folder: ${plainPath}`)
    cipherDir = next.cipherPath
    plainDir = plainDir ? `${plainDir}/${parts[i]}` : parts[i]
  }

  const entries = cache.get(plainPath)!.entries
  let parent: string | null = null
  if (plainPath) {
    const slash = plainPath.lastIndexOf('/')
    parent = slash < 0 ? '' : plainPath.slice(0, slash)
  }
  return { path: plainPath, parent, entries }
}

/** URL the service worker answers with the file's decrypted bytes. */
export function fileUrl(id: string, entry: ClientEntry, download = false): string {
  const cipherPath = entry.cipherPath.split('/').map(encodeURIComponent).join('/')
  const q = new URLSearchParams({ name: entry.name })
  if (download) q.set('download', '1')
  return `${DECRYPTED_PREFIX}${encodeURIComponent(id)}/${cipherPath}?${q}`
}

let workerReady: Promise<void> | null = null

// Set across the one reload ensureServiceWorker may do, so it cannot loop.
// It holds no secret; sessionStorage only stops a second automatic reload.
const RELOAD_FLAG = 'imagethat-sw-reload'

function controlled(timeoutMs: number): Promise<boolean> {
  if (navigator.serviceWorker.controller) return Promise.resolve(true)
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), timeoutMs)
    navigator.serviceWorker.addEventListener(
      'controllerchange',
      () => {
        clearTimeout(timer)
        resolve(true)
      },
      { once: true },
    )
  })
}

/**
 * Registers the decrypting service worker and resolves once it controls this
 * page, so media requests reach it. Also answers its key requests.
 *
 * A hard reload (Shift+Reload) opens the page without its service worker,
 * and the worker cannot take it over; the page then reloads itself once,
 * normally. Call this before any vault is unlocked: that reload would lock it.
 */
export function ensureServiceWorker(): Promise<void> {
  if (workerReady) return workerReady
  workerReady = (async () => {
    if (!window.isSecureContext || !('serviceWorker' in navigator) || !crypto.subtle) {
      throw new Error(
        'Decrypting in the browser needs a secure connection. Open this server over HTTPS (or on localhost).',
      )
    }
    navigator.serviceWorker.addEventListener('message', (e: MessageEvent) => {
      if (e.data?.type !== KEY_REQUEST || !e.ports[0]) return
      e.ports[0].postMessage(keys.get(e.data.vaultId)?.contentKey ?? null)
    })
    await navigator.serviceWorker.register('/sw.js', { scope: '/' })
    await navigator.serviceWorker.ready
    // On first install the worker claims this page from its activate event.
    if (await controlled(3000)) {
      sessionStorage.removeItem(RELOAD_FLAG)
      return
    }
    if (!sessionStorage.getItem(RELOAD_FLAG) && keys.size === 0) {
      sessionStorage.setItem(RELOAD_FLAG, '1')
      window.location.reload()
      await new Promise(() => {}) // the page is going away
    }
    throw new Error('The decrypting service worker is not running. Reload the page (a normal reload, not Shift+Reload).')
  })()
  workerReady.catch(() => {
    workerReady = null
  })
  return workerReady
}
