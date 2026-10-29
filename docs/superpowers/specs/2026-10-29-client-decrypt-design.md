# Client-decrypt mode: the server never sees a vault password

Date: 2026-10-29
Status: in progress (steps 1–3 landed 2026-10-29)

## Intent

**What the user asked for**

Decrypt gocryptfs vaults in the browser instead of on the webserver, so the
server never sees a vault password, a key or a byte of plaintext. Every client
of the server (browser, Mac app, Android app) is updated to work this way.

**Decisions taken with the user**

- The server is gated by a **server access password**, separate from vault
  passwords. It has to be: a client cannot derive anything from a vault
  password without `gocryptfs.conf`, and handing out `gocryptfs.conf`
  unauthenticated would allow offline password guessing.
- **Read-only first.** Browse, view, stream and download. Writes come later.
- **Both modes stay**, chosen by a flag. The default is today's behaviour.

**Rules this respects (`CLAUDE.md`)**

- Nothing working is replaced: server-decrypt mode, its endpoints and the apps'
  current code paths are untouched when the server runs in that mode.
- Keychain + LAContext stays the way vault passwords are stored on the Mac.
- Decrypted content never touches disk, in any client.

## Research that led here

Measured on the fixture vaults in `core/vault/testdata` and a 100 MB vault:

| Approach | Result |
|---|---|
| Go `core/vault` compiled to `js/wasm` | Works after small patches; output identical to native; 35 MB/s; 1.1 MB brotli |
| WebCrypto AES-GCM, hand-written TS | Output identical to native; **~450–530 MB/s in Chrome**; 1 MB seek in 1.7 ms |
| EME filename decryption on WebCrypto | Identical listing to native, 17 names in 6 ms |

WebCrypto wins: hardware AES, no download, and the format is small:

- File: 18-byte header (`uint16` version 2, 16-byte file ID), then blocks of
  4096 plaintext bytes stored as `nonce(16) ‖ ciphertext ‖ tag(16)` = 4128.
  AES-256-GCM, additional data `blockNo (uint64 BE) ‖ fileID`. An all-zero
  block is a hole. Content key `HKDF-SHA256(master, info="AES-GCM file content
  encryption")`.
- Names: base64url (no padding with `Raw64`) → EME with the directory's
  `gocryptfs.diriv` as tweak → strip pad16. Name key `HKDF-SHA256(master,
  "EME filename encryption")`. `gocryptfs.longname.*` entries take their real
  encrypted name from the `.name` sidecar. WebCrypto has no raw AES, so single
  blocks use AES-CBC with a zero IV (encrypt) and a crafted padding block
  (decrypt).
- Master key: `scrypt(password, conf.ScryptObject)` → the same block
  decryption on `conf.EncryptedKey` with block number 0 and an empty file ID.
  scrypt is the one piece WebCrypto lacks (`@noble/hashes`).

Only AES-GCM vaults (the gocryptfs default, and what ImageThat creates) are
supported in the browser. AES-SIV and XChaCha20 vaults report "unsupported";
the Go clients handle all three.

## Server (`webserver`)

- `-mode=server-decrypt` (default, unchanged) or `-mode=client-decrypt`.
- `-access-password-file=PATH`: a bcrypt hash, required in client-decrypt mode
  and refused otherwise. `imagethat-web -hash-password` reads a password from
  the terminal (or stdin) and prints the hash to put in that file.
- `GET /api/info` (no auth): `{"mode": "...", "version": "..."}`.
- `POST /api/login` `{"password": "..."}`: client-decrypt only, rate limited
  per client, origin checked, creates the session and marks it logged in.
- In client-decrypt mode:
  - `raw/list` and `raw/file` require a logged-in session instead of an
    unlocked vault. `GET /api/vaults` lists vaults only to a logged-in session.
  - `/unlock`, `/lock`, the decrypted endpoints, thumbnails and writes return
    `404` — they do not exist in this mode.
  - The server process never holds a vault key.
- `raw/list` gains `dirIV` (the directory's `gocryptfs.diriv`) and `longNames`
  (sidecar contents keyed by entry name) in **both** modes, so a client opens a
  directory in one request. Older clients ignore the extra fields.

## Browser (`web/src`)

- `crypto/`: `unlock(conf, password)`, `decryptName`, `decryptBlocks`, range
  maths. Interop-tested against the fixture vaults under Node.
- A login screen when `/api/info` says client-decrypt; then the vault list;
  a vault's password is entered and used in the browser only.
- Browsing decrypts listings in the page. Images are fetched, decrypted and
  shown from a `blob:` URL. Thumbnails are made in the browser from the
  decrypted image (the server cannot make them).
- Video plays through a Service Worker that answers `<video>` Range requests
  by fetching just the needed ciphertext blocks and decrypting them. The page
  hands the worker the content key (non-extractable `CryptoKey`) per vault;
  the worker holds keys in memory only.
- Downloads stream through the same worker, so a large file never sits whole
  in memory.
- Nothing decrypted goes to IndexedDB, the Cache API or `localStorage`.
  Locking a vault or logging out drops the keys from page and worker.

## Go core (`core/remote`)

- `Client.Info()` reads the mode. `Client.Login(accessPassword)` for
  client-decrypt servers. `OpenVault` unlocks locally in that mode instead of
  calling `/unlock`; on a `401` it logs in again with the in-memory access
  password, as it re-unlocks today.
- Exposed as `GocryptfsServerInfo` / `GocryptfsServerLogin` (Mac, cgo) and
  `mobile.ServerInfo` / `mobile.ServerLogin` (Android).

## Mac and Android

- When a server reports client-decrypt, the app asks for the server's access
  password (stored the way each app already stores server credentials), then
  opens vaults with `GocryptfsOpenRemote` / `mobile.OpenRemote`, entering the
  vault password locally.
- Upload, new folder, move and delete are hidden for client-decrypt servers in
  this version. Server-decrypt servers behave exactly as today.

## Order of work

1. Server: modes, access password, login, info, enriched `raw/list`, tests.
2. Browser crypto module with Node interop tests.
3. Browser UI: login, browse, images, thumbnails, Service Worker video,
   streaming download.
4. `core/remote` login and exports.
5. Mac app.
6. Android app.

Each step is committed separately and logged in `aj-log.md`.

## Later

- Writes from clients (the Go core already encrypts; the browser needs EME
  encryption, block encryption, diriv and long-name creation).
- AES-SIV / XChaCha in the browser.
