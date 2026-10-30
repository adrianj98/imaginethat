// The two ways this UI can work, behind one interface:
//
// - server-decrypt: the server unlocks vaults and serves plaintext (api.ts).
// - client-decrypt: the server serves ciphertext; this page unlocks vaults
//   and a service worker decrypts files (clientVaults.ts, sw.ts).

import * as api from './api'
import type { Entry, Listing, Mode, Vault } from './api'
import * as client from './clientVaults'
import type { ClientEntry } from './clientVaults'
import { clearImageThumbs } from './imageThumbs'
import { clearVideoThumbs } from './videoThumbs'

export interface Backend {
  mode: Mode
  /** Runs once when the app starts with this backend. */
  prepare(): Promise<void>
  /** Upload, new folder, rename and delete. */
  canWrite: boolean
  listVaults(): Promise<Vault[]>
  unlock(id: string, password: string): Promise<void>
  lock(id: string): Promise<void>
  listDir(id: string, path: string): Promise<Listing>
  /** URL of a file's decrypted bytes; supports Range. */
  fileUrl(id: string, dir: string, entry: Entry, download?: boolean): string
}

export const serverBackend: Backend = {
  mode: 'server-decrypt',
  canWrite: true,
  prepare: async () => {},
  listVaults: async () => (await api.listVaults()).vaults,
  unlock: async (id, password) => {
    await api.unlockVault(id, password)
  },
  lock: async (id) => {
    await api.lockVault(id)
    // Captured video frames derive from vault content; drop them with the
    // vault, mirroring the server's per-vault thumbnail LRU dying on lock.
    clearVideoThumbs()
  },
  listDir: (id, path) => api.listDir(id, path),
  fileUrl: (id, dir, entry, download) => api.fileUrl(id, api.joinPath(dir, entry.name), download),
}

export const clientBackend: Backend = {
  mode: 'client-decrypt',
  canWrite: false,
  prepare: () => client.ensureServiceWorker(),
  listVaults: async () => {
    const { vaults } = await api.listVaults()
    // The server never holds a vault open here; this page does.
    return vaults.map((v) => ({ ...v, unlocked: client.isUnlocked(v.id) }))
  },
  unlock: async (id, password) => {
    await client.ensureServiceWorker()
    await client.unlock(id, password)
  },
  lock: async (id) => {
    client.lock(id)
    clearVideoThumbs()
    clearImageThumbs()
  },
  listDir: (id, path) => client.listDir(id, path),
  fileUrl: (id, _dir, entry, download) => client.fileUrl(id, entry as ClientEntry, download),
}

/** Ends the session: the server forgets the login, this page every key. */
export async function logout(): Promise<void> {
  client.lockAll()
  clearVideoThumbs()
  clearImageThumbs()
  await api.logout()
}
