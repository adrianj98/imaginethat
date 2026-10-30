// Unlocking a gocryptfs vault from its gocryptfs.conf, entirely in the
// browser: scrypt(password) unwraps the master key, and HKDF derives the
// content and filename keys from it. The vault password and master key never
// leave this function; what comes out are non-extractable CryptoKeys.

import { scryptAsync } from '@noble/hashes/scrypt'
import { type Bytes, CryptoError, decryptBlock } from './content'

export class WrongPasswordError extends Error {
  constructor() {
    super('wrong password')
    this.name = 'WrongPasswordError'
  }
}

export interface VaultKeys {
  /** AES-256-GCM key for file contents. */
  contentKey: CryptoKey
  /** AES key for EME filename decryption; null when names are plaintext. */
  nameKey: CryptoKey | null
}

interface Conf {
  Version: number
  EncryptedKey: string
  ScryptObject: { Salt: string; N: number; R: number; P: number; KeyLen: number }
  FeatureFlags: string[] | null
}

// Flags the browser understands. AES-SIV and XChaCha20 content, FIDO2 and
// pre-2017 vaults without HKDF are left to the Go clients.
const KNOWN_FLAGS = new Set(['HKDF', 'GCMIV128', 'DirIV', 'EMENames', 'LongNames', 'LongNameMax', 'Raw64', 'PlaintextNames'])
const REQUIRED_FLAGS = ['HKDF', 'GCMIV128']

// scrypt N above this would need more than 1 GiB of memory in the tab.
const MAX_SCRYPT_N = 1 << 20

const HKDF_CONTENT = 'AES-GCM file content encryption'
const HKDF_NAMES = 'EME filename encryption'

function base64Decode(s: string): Bytes {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0))
}

/** Parses gocryptfs.conf and checks the browser can open this vault. */
export function parseConf(text: string): Conf {
  let conf: Conf
  try {
    conf = JSON.parse(text)
  } catch {
    throw new CryptoError('gocryptfs.conf is not valid JSON')
  }
  if (conf.Version !== 2) throw new CryptoError(`unsupported vault format version ${conf.Version}`)
  const flags = conf.FeatureFlags ?? []
  const unknown = flags.filter((f) => !KNOWN_FLAGS.has(f))
  if (unknown.length > 0) {
    throw new CryptoError(`this vault uses ${unknown.join(', ')}, which the browser cannot open yet; use the Mac or Android app`)
  }
  for (const f of REQUIRED_FLAGS) {
    if (!flags.includes(f)) throw new CryptoError('this vault was made by a gocryptfs older than 1.3, which the browser cannot open')
  }
  const s = conf.ScryptObject
  if (!s || !(s.N >= 1 << 10) || s.N > MAX_SCRYPT_N || (s.N & (s.N - 1)) !== 0 || !(s.R >= 8) || !(s.P >= 1) || !(s.KeyLen >= 32)) {
    throw new CryptoError('gocryptfs.conf has unsupported scrypt parameters')
  }
  return conf
}

export function hasPlaintextNames(conf: Conf): boolean {
  return (conf.FeatureFlags ?? []).includes('PlaintextNames')
}

async function hkdf(ikm: Bytes, info: string, derived: AesKeyAlgorithm, usages: KeyUsage[]): Promise<CryptoKey> {
  const base = await crypto.subtle.importKey('raw', ikm, 'HKDF', false, ['deriveKey'])
  return crypto.subtle.deriveKey(
    { name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(0), info: new TextEncoder().encode(info) },
    base,
    derived,
    false,
    usages,
  )
}

/** Unlocks a vault. Throws WrongPasswordError if the password is wrong. */
export async function unlock(confText: string, password: string): Promise<VaultKeys> {
  const conf = parseConf(confText)
  const s = conf.ScryptObject
  const derived = await scryptAsync(new TextEncoder().encode(password), base64Decode(s.Salt), {
    N: s.N,
    r: s.R,
    p: s.P,
    dkLen: s.KeyLen,
  })
  const scryptHash: Bytes = new Uint8Array(derived)
  derived.fill(0)

  // The master key is stored like a content block: number 0, no file ID,
  // under the content key derived from the scrypt hash.
  const wrapKey = await hkdf(scryptHash, HKDF_CONTENT, { name: 'AES-GCM', length: 256 }, ['decrypt'])
  scryptHash.fill(0)
  let master: Bytes
  try {
    master = await decryptBlock(wrapKey, base64Decode(conf.EncryptedKey), 0, new Uint8Array(0))
  } catch {
    throw new WrongPasswordError()
  }

  try {
    const contentKey = await hkdf(master, HKDF_CONTENT, { name: 'AES-GCM', length: 256 }, ['decrypt'])
    const nameKey = hasPlaintextNames(conf)
      ? null
      : await hkdf(master, HKDF_NAMES, { name: 'AES-CBC', length: 256 }, ['encrypt', 'decrypt'])
    return { contentKey, nameKey }
  } finally {
    master.fill(0)
  }
}
