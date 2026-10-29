// gocryptfs filename decryption: base64url → EME (keyed by the name key,
// tweaked by the directory's gocryptfs.diriv) → strip pad16.
//
// EME needs single-block AES, which WebCrypto does not offer, so it is built
// from AES-CBC: a zero IV makes one CBC block an ECB encryption, and a second
// block chosen to decrypt to valid padding lets CBC decryption return one
// ECB-decrypted block.

import { type Bytes, CryptoError } from './content'

const ZERO16 = new Uint8Array(16)
const FULL_PAD = new Uint8Array(16).fill(16)

export const DIRIV_NAME = 'gocryptfs.diriv'
export const LONGNAME_PREFIX = 'gocryptfs.longname.'
export const LONGNAME_SUFFIX = '.name'
const CONF_NAME = 'gocryptfs.conf'

/** Files gocryptfs keeps for itself, never shown as directory entries. */
export function isSpecialName(cipherName: string): boolean {
  return (
    cipherName === CONF_NAME ||
    cipherName === DIRIV_NAME ||
    (cipherName.startsWith(LONGNAME_PREFIX) && cipherName.endsWith(LONGNAME_SUFFIX))
  )
}

export function isLongName(cipherName: string): boolean {
  return cipherName.startsWith(LONGNAME_PREFIX) && !cipherName.endsWith(LONGNAME_SUFFIX)
}

function xor16(a: Bytes, b: Bytes): Bytes {
  const out = new Uint8Array(16)
  for (let i = 0; i < 16; i++) out[i] = a[i] ^ b[i]
  return out
}

// Multiplication by 2 in GF(2^128), as in github.com/rfjakob/eme.
function mulTwo(x: Bytes): Bytes {
  const out = new Uint8Array(16)
  out[0] = (2 * x[0]) & 0xff
  if (x[15] >= 128) out[0] ^= 135
  for (let j = 1; j < 16; j++) out[j] = ((2 * x[j]) & 0xff) + (x[j - 1] >= 128 ? 1 : 0)
  return out
}

async function aesEncryptBlock(key: CryptoKey, block: Bytes): Promise<Bytes> {
  const out = await crypto.subtle.encrypt({ name: 'AES-CBC', iv: ZERO16 }, key, block)
  return new Uint8Array(out, 0, 16)
}

async function aesDecryptBlock(key: CryptoKey, block: Bytes): Promise<Bytes> {
  // CBC-decrypting [block, c2] gives [D(block), D(c2) ^ block]; choosing
  // c2 = E(FULL_PAD ^ block) makes the second a full padding block, which
  // WebCrypto strips, leaving D(block).
  const c2 = await aesEncryptBlock(key, xor16(FULL_PAD, block))
  const both = new Uint8Array(32)
  both.set(block)
  both.set(c2, 16)
  return new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-CBC', iv: ZERO16 }, key, both))
}

/** EME decryption (Halevi–Rogaway), ported from rfjakob/eme Transform. */
async function emeDecrypt(key: CryptoKey, tweak: Bytes, data: Bytes): Promise<Bytes> {
  const m = data.length / 16
  if (!Number.isInteger(m) || m < 1 || m > 128) throw new CryptoError('bad encrypted name length')
  if (tweak.length !== 16) throw new CryptoError('bad directory IV')

  const L: Bytes[] = []
  let li = await aesEncryptBlock(key, ZERO16)
  for (let i = 0; i < m; i++) {
    li = mulTwo(li)
    L.push(li)
  }
  const C = await Promise.all(
    L.map((l, j) => aesDecryptBlock(key, xor16(data.subarray(j * 16, j * 16 + 16), l))),
  )
  let MP = xor16(C[0], tweak)
  for (let j = 1; j < m; j++) MP = xor16(MP, C[j])
  const MC = await aesDecryptBlock(key, MP)
  let M = xor16(MP, MC)
  for (let j = 1; j < m; j++) {
    M = mulTwo(M)
    C[j] = xor16(C[j], M)
  }
  let CCC1 = xor16(MC, tweak)
  for (let j = 1; j < m; j++) CCC1 = xor16(CCC1, C[j])
  C[0] = CCC1

  const out = new Uint8Array(m * 16)
  await Promise.all(
    C.map(async (c, j) => out.set(xor16(await aesDecryptBlock(key, c), L[j]), j * 16)),
  )
  return out
}

function base64urlDecode(s: string): Bytes {
  if (/[\r\n]/.test(s)) throw new CryptoError('encrypted name contains a line break')
  if (!/^[A-Za-z0-9_-]*={0,2}$/.test(s)) throw new CryptoError('encrypted name is not base64url')
  const std = s.replace(/=+$/, '').replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(std + '='.repeat((4 - (std.length % 4)) % 4))
  return Uint8Array.from(bin, (c) => c.charCodeAt(0))
}

function unpad16(b: Bytes): Bytes {
  const pad = b[b.length - 1]
  if (pad === 0 || pad > 16 || pad >= b.length) throw new CryptoError('bad name padding')
  for (let i = b.length - pad; i < b.length; i++) {
    if (b[i] !== pad) throw new CryptoError('bad name padding')
  }
  return b.subarray(0, b.length - pad)
}

/**
 * Decrypts one encrypted name. For a gocryptfs.longname.* entry, pass the
 * content of its .name sidecar, which holds the full encrypted name.
 */
export async function decryptName(nameKey: CryptoKey, dirIV: Bytes, cipherName: string): Promise<string> {
  const bin = base64urlDecode(cipherName)
  if (bin.length === 0 || bin.length % 16 !== 0) throw new CryptoError('bad encrypted name length')
  const plain = unpad16(await emeDecrypt(nameKey, dirIV, bin))
  let name: string
  try {
    name = new TextDecoder('utf-8', { fatal: true }).decode(plain)
  } catch {
    throw new CryptoError('decrypted name is not UTF-8')
  }
  if (name === '' || name === '.' || name === '..' || name.includes('/') || name.includes('\0')) {
    throw new CryptoError('decrypted name is invalid')
  }
  return name
}
