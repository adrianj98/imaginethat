// gocryptfs file content: an 18-byte header, then independent AES-256-GCM
// blocks of 4096 plaintext bytes. Each block is nonce(16) ‖ ciphertext ‖
// tag(16), authenticated with blockNo (uint64 big-endian) ‖ fileID, so blocks
// can be decrypted in any order and a seek only touches the blocks it needs.
//
// Decryption uses WebCrypto, which runs on the CPU's AES instructions.

/** Bytes backed by a plain ArrayBuffer, which is what WebCrypto accepts. */
export type Bytes = Uint8Array<ArrayBuffer>

export const HEADER_LEN = 18
export const PLAIN_BS = 4096
const IV_LEN = 16
const TAG_LEN = 16
export const CIPHER_BS = PLAIN_BS + IV_LEN + TAG_LEN

export class CryptoError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'CryptoError'
  }
}

/** Returns the 16-byte file ID from a file's header. */
export function parseHeader(header: Bytes): Bytes {
  if (header.length < HEADER_LEN) throw new CryptoError('file header is truncated')
  const version = (header[0] << 8) | header[1]
  if (version !== 2) throw new CryptoError(`unsupported file header version ${version}`)
  return header.slice(2, HEADER_LEN)
}

/** Plaintext size of a file whose ciphertext is cipherSize bytes. */
export function plainSize(cipherSize: number): number {
  if (cipherSize === 0) return 0
  if (cipherSize < HEADER_LEN) throw new CryptoError('file is truncated')
  const body = cipherSize - HEADER_LEN
  const full = Math.floor(body / CIPHER_BS)
  const rest = body % CIPHER_BS
  if (rest !== 0 && rest <= IV_LEN + TAG_LEN) throw new CryptoError('file ends in a partial block')
  return full * PLAIN_BS + (rest === 0 ? 0 : rest - IV_LEN - TAG_LEN)
}

/**
 * The ciphertext needed for plaintext bytes [offset, offset+length):
 * whole blocks from `firstBlock`, at file offsets [start, end) (end may run
 * past the file; a Range request clamps it). `skip` is how far into the first
 * decrypted block the requested bytes begin.
 */
export function cipherRange(offset: number, length: number) {
  const firstBlock = Math.floor(offset / PLAIN_BS)
  const lastBlock = Math.floor((offset + Math.max(length, 1) - 1) / PLAIN_BS)
  return {
    firstBlock,
    start: HEADER_LEN + firstBlock * CIPHER_BS,
    end: HEADER_LEN + (lastBlock + 1) * CIPHER_BS,
    skip: offset - firstBlock * PLAIN_BS,
  }
}

function additionalData(blockNo: number, fileID: Bytes): Bytes {
  const ad = new Uint8Array(8 + fileID.length)
  new DataView(ad.buffer).setBigUint64(0, BigInt(blockNo))
  ad.set(fileID, 8)
  return ad
}

/** Decrypts one block. blockNo counts from the start of the file. */
export async function decryptBlock(
  key: CryptoKey,
  block: Bytes,
  blockNo: number,
  fileID: Bytes,
): Promise<Bytes> {
  if (block.length === 0) return block
  // gocryptfs stores a sparse hole as an all-zero block.
  if (block.length === CIPHER_BS && block.every((b) => b === 0)) return new Uint8Array(PLAIN_BS)
  if (block.length <= IV_LEN + TAG_LEN) throw new CryptoError('block is truncated')
  const nonce = block.subarray(0, IV_LEN)
  if (nonce.every((b) => b === 0)) throw new CryptoError('block has an all-zero nonce')
  try {
    const plain = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: nonce, additionalData: additionalData(blockNo, fileID), tagLength: TAG_LEN * 8 },
      key,
      block.subarray(IV_LEN),
    )
    return new Uint8Array(plain)
  } catch {
    throw new CryptoError(`block ${blockNo} failed authentication`)
  }
}

/**
 * Decrypts consecutive whole blocks starting at block number firstBlock.
 * The blocks are independent, so WebCrypto works on them in parallel.
 */
export async function decryptBlocks(
  key: CryptoKey,
  cipher: Bytes,
  firstBlock: number,
  fileID: Bytes,
): Promise<Bytes> {
  const jobs: Promise<Bytes>[] = []
  for (let off = 0, n = firstBlock; off < cipher.length; off += CIPHER_BS, n++) {
    jobs.push(decryptBlock(key, cipher.subarray(off, off + CIPHER_BS), n, fileID))
  }
  const parts = await Promise.all(jobs)
  const out = new Uint8Array(parts.reduce((sum, p) => sum + p.length, 0))
  let pos = 0
  for (const p of parts) {
    out.set(p, pos)
    pos += p.length
  }
  return out
}

/** Decrypts a whole file held in memory. */
export async function decryptFile(key: CryptoKey, cipher: Bytes): Promise<Bytes> {
  if (cipher.length === 0) return cipher
  const fileID = parseHeader(cipher)
  const plain = await decryptBlocks(key, cipher.subarray(HEADER_LEN), 0, fileID)
  if (plain.length !== plainSize(cipher.length)) throw new CryptoError('file is truncated')
  return plain
}
