// Client for the imagethat-web JSON API.
//
// Every request is same-origin and relies on the HttpOnly session cookie the
// server sets; there is no token for this code to hold or leak.

export type MediaType = '' | 'image' | 'video'

export interface Entry {
  name: string
  isDir: boolean
  size: number
  mediaType: MediaType
}

export interface Vault {
  id: string
  name: string
  unlocked: boolean
}

export interface Listing {
  path: string
  parent: string | null
  entries: Entry[]
}

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

async function request<T>(input: string, init?: RequestInit): Promise<T> {
  const res = await fetch(input, {
    credentials: 'same-origin',
    ...init,
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    let message = res.statusText
    try {
      const body = await res.json()
      if (body?.error) message = body.error
    } catch {
      // Non-JSON error body; the status text will have to do.
    }
    throw new ApiError(res.status, message)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export type Mode = 'server-decrypt' | 'client-decrypt'

export function info(): Promise<{ mode: Mode; version: string }> {
  return request('/api/info')
}

/** Logs in with the server access password (client-decrypt mode). */
export function login(password: string): Promise<{ vaults: Vault[] }> {
  return request('/api/login', {
    method: 'POST',
    body: JSON.stringify({ password }),
  })
}

export function listVaults(): Promise<{ vaults: Vault[] }> {
  return request('/api/vaults')
}

export function unlockVault(id: string, password: string): Promise<{ vaults: Vault[] }> {
  return request(`/api/vaults/${id}/unlock`, {
    method: 'POST',
    body: JSON.stringify({ password }),
  })
}

export function lockVault(id: string): Promise<{ vaults: Vault[] }> {
  return request(`/api/vaults/${id}/lock`, { method: 'POST' })
}

export function logout(): Promise<unknown> {
  return request('/api/logout', { method: 'POST' })
}

export function listDir(id: string, path: string): Promise<Listing> {
  return request(`/api/vaults/${id}/list?path=${encodeURIComponent(path)}`)
}

export function mkdir(id: string, path: string): Promise<unknown> {
  return request(`/api/vaults/${id}/mkdir`, {
    method: 'POST',
    body: JSON.stringify({ path }),
  })
}

export function move(id: string, from: string, to: string): Promise<unknown> {
  return request(`/api/vaults/${id}/move`, {
    method: 'POST',
    body: JSON.stringify({ from, to }),
  })
}

export function remove(id: string, path: string): Promise<unknown> {
  return request(`/api/vaults/${id}/entry?path=${encodeURIComponent(path)}`, {
    method: 'DELETE',
  })
}

/** URL of a file's decrypted bytes. Supports Range, so <video> can seek. */
export function fileUrl(id: string, path: string, download = false): string {
  const q = download ? '&download=1' : ''
  return `/api/vaults/${id}/file?path=${encodeURIComponent(path)}${q}`
}

export function thumbUrl(id: string, path: string, size = 320): string {
  return `/api/vaults/${id}/thumb?path=${encodeURIComponent(path)}&size=${size}`
}

/**
 * Streams a file into the vault, reporting progress.
 *
 * Uses XMLHttpRequest rather than fetch because upload progress events are
 * still the only reliable way to drive a progress bar in browsers.
 */
export function upload(
  id: string,
  dir: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    const url =
      `/api/vaults/${id}/upload?path=${encodeURIComponent(dir)}` +
      `&name=${encodeURIComponent(file.name)}`

    xhr.open('POST', url)
    xhr.withCredentials = true

    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total)
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve()
        return
      }
      let message = xhr.statusText
      try {
        const body = JSON.parse(xhr.responseText)
        if (body?.error) message = body.error
      } catch {
        // keep statusText
      }
      reject(new ApiError(xhr.status, message))
    }
    xhr.onerror = () => reject(new ApiError(0, 'network error'))
    xhr.onabort = () => reject(new ApiError(0, 'upload cancelled'))

    signal?.addEventListener('abort', () => xhr.abort())
    xhr.send(file)
  })
}

/** Formats a byte count for display. */
export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let i = 0
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024
    i++
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[i]}`
}

/** Extensions most browsers cannot play, even though the vault lists them. */
const UNPLAYABLE = new Set(['mkv', 'avi'])

export function isLikelyUnplayable(name: string): boolean {
  const ext = name.split('.').pop()?.toLowerCase() ?? ''
  return UNPLAYABLE.has(ext)
}

/** Extensions the server can generate a thumbnail for. */
const THUMBNAILABLE = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'bmp'])

export function hasThumbnail(name: string): boolean {
  const ext = name.split('.').pop()?.toLowerCase() ?? ''
  return THUMBNAILABLE.has(ext)
}

export function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}
