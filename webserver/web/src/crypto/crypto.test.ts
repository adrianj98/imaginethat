// Interop tests: the browser crypto must decrypt the fixture vault exactly as
// native Go does. testdata/manifest.json is written and kept honest by
// TestBrowserCryptoManifest in webserver/server.

import { createHash } from 'node:crypto'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { beforeAll, describe, expect, it } from 'vitest'

import { CIPHER_BS, CryptoError, HEADER_LEN, cipherRange, decryptBlocks, decryptFile, parseHeader, plainSize } from './content'
import { DIRIV_NAME, LONGNAME_SUFFIX, decryptName, isLongName, isSpecialName } from './names'
import { type VaultKeys, WrongPasswordError, unlock } from './vault'

interface ManifestEntry {
  path: string
  cipherPath: string
  isDir: boolean
  size: number
  sha256?: string
}

const repo = resolve(__dirname, '../../../..')
const manifest: { vault: string; password: string; entries: ManifestEntry[] } = JSON.parse(
  readFileSync(join(__dirname, 'testdata/manifest.json'), 'utf8'),
)
const vaultDir = join(repo, manifest.vault)
const read = (rel: string) => new Uint8Array(readFileSync(join(vaultDir, rel)))
const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest('hex')
const conf = readFileSync(join(vaultDir, 'gocryptfs.conf'), 'utf8')

let keys: VaultKeys

beforeAll(async () => {
  keys = await unlock(conf, manifest.password)
}, 30_000)

describe('unlock', () => {
  it('rejects a wrong password', async () => {
    await expect(unlock(conf, 'not the password')).rejects.toBeInstanceOf(WrongPasswordError)
  }, 30_000)

  it('refuses vaults the browser cannot open, before running scrypt', async () => {
    for (const other of ['vault-aessiv', 'vault-xchacha']) {
      const c = readFileSync(join(repo, 'core/vault/testdata', other, 'gocryptfs.conf'), 'utf8')
      await expect(unlock(c, manifest.password)).rejects.toThrow(CryptoError)
    }
  })

  it('makes keys that cannot be exported', () => {
    expect(keys.contentKey.extractable).toBe(false)
    expect(keys.nameKey?.extractable).toBe(false)
  })
})

describe('names', () => {
  // Walk the ciphertext tree the way a client does over raw/list.
  async function walk(cipherDir: string, plainDir: string, out: Map<string, string>) {
    const abs = join(vaultDir, cipherDir)
    const iv = read(join(cipherDir, DIRIV_NAME))
    for (const name of readdirSync(abs)) {
      if (isSpecialName(name)) continue
      const encrypted = isLongName(name)
        ? readFileSync(join(abs, name + LONGNAME_SUFFIX), 'utf8')
        : name
      const plain = await decryptName(keys.nameKey!, iv, encrypted)
      const cipherPath = cipherDir ? `${cipherDir}/${name}` : name
      const plainPath = plainDir ? `${plainDir}/${plain}` : plain
      out.set(plainPath, cipherPath)
      if (statSync(join(vaultDir, cipherPath)).isDirectory()) await walk(cipherPath, plainPath, out)
    }
  }

  it('decrypts every name, long names included, as Go does', async () => {
    const got = new Map<string, string>()
    await walk('', '', got)
    const want = new Map(manifest.entries.map((e) => [e.path, e.cipherPath]))
    expect(got).toEqual(want)
    expect([...got.values()].some(isLongName)).toBe(true)
  })

  it('rejects a name from another directory', async () => {
    const nested = manifest.entries.find((e) => e.cipherPath.split('/').length === 2)!
    const [, leaf] = nested.cipherPath.split('/')
    if (isLongName(leaf)) return
    await expect(decryptName(keys.nameKey!, read(DIRIV_NAME), leaf)).rejects.toThrow()
  })
})

describe('content', () => {
  const files = manifest.entries.filter((e) => !e.isDir)

  it('decrypts every file as Go does', async () => {
    for (const f of files) {
      const cipher = read(f.cipherPath)
      expect(plainSize(cipher.length), f.path).toBe(f.size)
      const plain = await decryptFile(keys.contentKey, cipher)
      expect(sha256(plain), f.path).toBe(f.sha256)
    }
  })

  it('decrypts any byte range from just the blocks that cover it', async () => {
    const big = files.reduce((a, b) => (b.size > a.size ? b : a))
    const cipher = read(big.cipherPath)
    const whole = await decryptFile(keys.contentKey, cipher)
    const fileID = parseHeader(cipher)
    let seed = 1
    const rand = (n: number) => ((seed = (seed * 1103515245 + 12345) % 2 ** 31) % n)
    for (let i = 0; i < 100; i++) {
      const offset = rand(whole.length)
      const length = 1 + rand(Math.min(50_000, whole.length - offset))
      const r = cipherRange(offset, length)
      const blocks = await decryptBlocks(keys.contentKey, cipher.subarray(r.start, r.end), r.firstBlock, fileID)
      expect(blocks.subarray(r.skip, r.skip + length)).toEqual(whole.subarray(offset, offset + length))
    }
  })

  it('detects tampering, truncation and blocks moved between positions', async () => {
    const f = files.find((e) => e.size > 2 * 4096)!
    const cipher = read(f.cipherPath)

    const flipped = cipher.slice()
    flipped[HEADER_LEN + 100] ^= 1
    await expect(decryptFile(keys.contentKey, flipped)).rejects.toThrow(CryptoError)

    await expect(decryptFile(keys.contentKey, cipher.slice(0, cipher.length - 1))).rejects.toThrow(CryptoError)

    const swapped = cipher.slice()
    swapped.set(cipher.subarray(HEADER_LEN + CIPHER_BS, HEADER_LEN + 2 * CIPHER_BS), HEADER_LEN)
    swapped.set(cipher.subarray(HEADER_LEN, HEADER_LEN + CIPHER_BS), HEADER_LEN + CIPHER_BS)
    await expect(decryptFile(keys.contentKey, swapped)).rejects.toThrow(CryptoError)
  })
})
