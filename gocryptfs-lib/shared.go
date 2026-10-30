package main

/*
#include "shim.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
	"unsafe"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/errcode"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// --- Google Drive and imagethat-web servers -------------------------------------
//
// The same Go code the Android app binds through core/mobile: a Drive account
// or a server connection is a handle; vaults opened through them are ordinary
// sessions. JSON results come back in a DecryptResult (free it with
// GocryptfsFreeDecryptResult); its error string starts with a stable code
// ("not_found: ...", see core/errcode), the same codes Android gets.
//
// Return codes of the int-returning calls here: 0 ok, -1 not found (or bad
// handle), -2 wrong password, -4 already exists, -5 network or other failure,
// -6 certificate mismatch, -7 cancelled (or wrong access password, for
// GocryptfsServerLogin), -8 locked on the server, -9 Drive not signed in or
// authorization refused, -10 the server refused the change.

const (
	rcLocked    = -8
	rcAuth      = -9
	rcForbidden = -10
)

func codeRC(err error) C.int {
	if err == nil {
		return rcOK
	}
	switch errcode.Code(err) {
	case errcode.NotFound, errcode.NoConfig, errcode.Closed:
		return rcNoConfig
	case errcode.WrongPassword:
		return rcWrongPassword
	case errcode.Exists:
		return rcExists
	case errcode.CertMismatch:
		return -6
	case errcode.WrongAccessPassword, errcode.Cancelled:
		return rcCancelled
	case errcode.Locked:
		return rcLocked
	case errcode.NotConnected, errcode.Auth:
		return rcAuth
	case errcode.Forbidden, errcode.ReadOnly:
		return rcForbidden
	}
	return rcBackend
}

func errResult(err error) C.DecryptResult {
	return C.DecryptResult{error: C.CString(errcode.Message(err))}
}

func jsonResult(v any, err error) C.DecryptResult {
	if err != nil {
		return errResult(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errResult(err)
	}
	return toCResult(b)
}

// handles holds Drive accounts and server connections by ID.
type handles[T any] struct {
	mu   sync.Mutex
	m    map[int]T
	next int
}

func (h *handles[T]) add(v T) C.int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = make(map[int]T)
	}
	h.next++
	h.m[h.next] = v
	return C.int(h.next)
}

func (h *handles[T]) get(id C.int) (T, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.m[int(id)]
	return v, ok
}

func (h *handles[T]) remove(id C.int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.m, int(id))
}

var errBadHandle = errors.New("not_found: no such handle")

// --- Drive -----------------------------------------------------------------------

type driveAccount struct {
	c *drive.Client

	mu   sync.Mutex
	flow *drive.Flow
}

var drives handles[*driveAccount]

// GocryptfsDriveNew returns a handle for the user's Google OAuth client.
// refreshToken is "" before the first sign-in.
//
//export GocryptfsDriveNew
func GocryptfsDriveNew(clientID, clientSecret, refreshToken *C.char) C.int {
	cfg := drive.Config{ClientID: C.GoString(clientID), ClientSecret: C.GoString(clientSecret), RefreshToken: C.GoString(refreshToken)}
	if testDriveConfig != nil {
		testDriveConfig(&cfg)
	}
	return drives.add(&driveAccount{c: drive.New(cfg)})
}

// testDriveConfig points new Drive handles at a fake Drive in the shim tests.
var testDriveConfig func(*drive.Config)

//export GocryptfsDriveFree
func GocryptfsDriveFree(h C.int) {
	if d, ok := drives.get(h); ok {
		d.mu.Lock()
		if d.flow != nil {
			d.flow.Cancel()
		}
		d.mu.Unlock()
	}
	drives.remove(h)
}

// GocryptfsDriveRefreshToken returns the refresh token to store ("" when
// signed out). Caller frees.
//
//export GocryptfsDriveRefreshToken
func GocryptfsDriveRefreshToken(h C.int) *C.char {
	d, ok := drives.get(h)
	if !ok {
		return C.CString("")
	}
	return C.CString(d.c.RefreshToken())
}

//export GocryptfsDriveSignOut
func GocryptfsDriveSignOut(h C.int) {
	if d, ok := drives.get(h); ok {
		d.c.SignOut()
	}
}

// GocryptfsDriveSignInStart starts a sign-in and returns the URL to open in
// the browser (NULL on failure). Caller frees. Then call
// GocryptfsDriveSignInWait on a background thread.
//
//export GocryptfsDriveSignInStart
func GocryptfsDriveSignInStart(h C.int) *C.char {
	d, ok := drives.get(h)
	if !ok {
		return nil
	}
	f, err := d.c.StartSignIn()
	if err != nil {
		return nil
	}
	d.mu.Lock()
	if d.flow != nil {
		d.flow.Cancel()
	}
	d.flow = f
	d.mu.Unlock()
	return C.CString(f.URL)
}

// GocryptfsDriveSignInWait blocks until the browser comes back (at most 10
// minutes) and finishes signing in. 0, -7 cancelled, -9 refused, -5 failed.
//
//export GocryptfsDriveSignInWait
func GocryptfsDriveSignInWait(h C.int) C.int {
	d, ok := drives.get(h)
	if !ok {
		return rcNoConfig
	}
	d.mu.Lock()
	f := d.flow
	d.mu.Unlock()
	if f == nil {
		return rcNoConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	err := f.Wait(ctx)
	d.mu.Lock()
	if d.flow == f {
		d.flow = nil
	}
	d.mu.Unlock()
	return codeRC(err)
}

//export GocryptfsDriveSignInCancel
func GocryptfsDriveSignInCancel(h C.int) {
	if d, ok := drives.get(h); ok {
		d.mu.Lock()
		if d.flow != nil {
			d.flow.Cancel()
		}
		d.mu.Unlock()
	}
}

func driveCall[T any](h C.int, fn func(c *drive.Client, ctx context.Context) (T, error)) C.DecryptResult {
	d, ok := drives.get(h)
	if !ok {
		return errResult(errBadHandle)
	}
	return jsonResult(fn(d.c, context.Background()))
}

// GocryptfsDriveList lists a folder ("root" is My Drive) as a JSON array of
// {id, name, mimeType, size, modifiedTime, thumbnailLink, isFolder}.
// mediaOnly keeps folders, images and videos, sorted folders first.
//
//export GocryptfsDriveList
func GocryptfsDriveList(h C.int, folderID *C.char, mediaOnly C.int) C.DecryptResult {
	id := C.GoString(folderID)
	return driveCall(h, func(c *drive.Client, ctx context.Context) ([]drive.File, error) {
		var files []drive.File
		var err error
		if mediaOnly != 0 {
			files, err = c.ListMedia(ctx, id)
		} else {
			files, err = c.List(ctx, id)
		}
		if files == nil {
			files = []drive.File{}
		}
		return files, err
	})
}

// GocryptfsDriveFindVaultRoot returns {rootId, path} for the vault folderID
// is in; the error starts with "not_found:" when it is in none.
//
//export GocryptfsDriveFindVaultRoot
func GocryptfsDriveFindVaultRoot(h C.int, folderID *C.char) C.DecryptResult {
	id := C.GoString(folderID)
	return driveCall(h, func(c *drive.Client, ctx context.Context) (*drive.VaultRoot, error) {
		return c.FindVaultRoot(ctx, id, 0)
	})
}

// GocryptfsDriveReadRange downloads length bytes of a plain file at offset.
//
//export GocryptfsDriveReadRange
func GocryptfsDriveReadRange(h C.int, fileID *C.char, offset C.int64_t, length C.int64_t) C.DecryptResult {
	d, ok := drives.get(h)
	if !ok {
		return errResult(errBadHandle)
	}
	b, err := d.c.ReadRange(context.Background(), C.GoString(fileID), int64(offset), int64(length))
	if err != nil {
		return errResult(err)
	}
	return toCResult(b)
}

// GocryptfsDriveThumbnail fetches Drive's own preview of a plain file.
//
//export GocryptfsDriveThumbnail
func GocryptfsDriveThumbnail(h C.int, thumbnailLink *C.char, maxEdge C.int) C.DecryptResult {
	d, ok := drives.get(h)
	if !ok {
		return errResult(errBadHandle)
	}
	b, err := d.c.Thumbnail(context.Background(), C.GoString(thumbnailLink), int(maxEdge))
	if err != nil {
		return errResult(err)
	}
	return toCResult(b)
}

// GocryptfsDriveUpload creates a plain file (updateFileID "") or replaces a
// file's content, and returns it as JSON.
//
//export GocryptfsDriveUpload
func GocryptfsDriveUpload(h C.int, parentID, name, mimeType, updateFileID *C.char, data unsafe.Pointer, dataLen C.int64_t) C.DecryptResult {
	buf := C.GoBytes(data, C.int(dataLen))
	parent, n, mt, upd := C.GoString(parentID), C.GoString(name), C.GoString(mimeType), C.GoString(updateFileID)
	return driveCall(h, func(c *drive.Client, ctx context.Context) (*drive.File, error) {
		if upd != "" {
			return c.Update(ctx, upd, mt, buf)
		}
		return c.Upload(ctx, parent, n, mt, buf)
	})
}

//export GocryptfsDriveCreateFolder
func GocryptfsDriveCreateFolder(h C.int, parentID, name *C.char) C.DecryptResult {
	parent, n := C.GoString(parentID), C.GoString(name)
	return driveCall(h, func(c *drive.Client, ctx context.Context) (*drive.File, error) {
		return c.CreateFolder(ctx, parent, n)
	})
}

//export GocryptfsDriveDelete
func GocryptfsDriveDelete(h C.int, fileID *C.char) C.int {
	d, ok := drives.get(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(d.c.Delete(context.Background(), C.GoString(fileID)))
}

// GocryptfsDriveMove moves fileID from oldParent to newParent ("" keeps it
// where it is) and renames it to newName ("" keeps the name).
//
//export GocryptfsDriveMove
func GocryptfsDriveMove(h C.int, fileID, oldParent, newParent, newName *C.char) C.int {
	d, ok := drives.get(h)
	if !ok {
		return rcNoConfig
	}
	_, err := d.c.Move(context.Background(), C.GoString(fileID), C.GoString(oldParent), C.GoString(newParent), C.GoString(newName))
	return codeRC(err)
}

// GocryptfsOpenDrive unlocks the vault in Drive folder rootID. Ciphertext is
// cached in cacheDir ("" for none), shared by every Drive vault. Returns a
// session ID (writable, like a local vault) or a negative code.
//
//export GocryptfsOpenDrive
func GocryptfsOpenDrive(h C.int, rootID, password, cacheDir *C.char) C.int {
	d, ok := drives.get(h)
	if !ok {
		return rcNoConfig
	}
	var ranges *cache.Ranges
	if dir := C.GoString(cacheDir); dir != "" {
		r, err := cache.SharedRanges(dir, 0)
		if err != nil {
			return rcBackend
		}
		ranges = r
	}
	s, v, err := drive.OpenVault(d.c, C.GoString(rootID), C.GoString(password), ranges)
	if err != nil {
		return codeRC(err)
	}
	return registerSession(&session{s: s, drive: v})
}

// GocryptfsInitDrive creates a new empty vault in Drive folder folderID.
//
//export GocryptfsInitDrive
func GocryptfsInitDrive(h C.int, folderID, password *C.char) C.int {
	d, ok := drives.get(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(drive.InitVault(d.c, C.GoString(folderID), C.GoString(password)))
}

// GocryptfsDriveCacheLimit sets the Drive cache's size limit. Files kept with
// GocryptfsCacheFile are never evicted.
//
//export GocryptfsDriveCacheLimit
func GocryptfsDriveCacheLimit(dir *C.char, limit C.int64_t) C.int {
	r, err := cache.SharedRanges(C.GoString(dir), int64(limit))
	if err != nil {
		return rcBackend
	}
	r.SetBudget(int64(limit))
	return rcOK
}

//export GocryptfsDriveCacheSize
func GocryptfsDriveCacheSize(dir *C.char) C.int64_t {
	r, err := cache.SharedRanges(C.GoString(dir), 0)
	if err != nil {
		return 0
	}
	return C.int64_t(r.Size())
}

//export GocryptfsDriveCacheClear
func GocryptfsDriveCacheClear(dir *C.char) C.int {
	r, err := cache.SharedRanges(C.GoString(dir), 0)
	if err != nil {
		return rcBackend
	}
	return codeRC(r.Clear())
}

// GocryptfsCachedRanges returns, for a file in a Drive vault, the parts held
// on disk as a JSON array of [start, end) fractions of its length; "[]" for
// other sessions.
//
//export GocryptfsCachedRanges
func GocryptfsCachedRanges(sessionID C.int, relativePath *C.char) C.DecryptResult {
	sess := getSession(sessionID)
	if sess == nil {
		return errResult(errBadHandle)
	}
	if sess.drive == nil {
		return toCResult([]byte("[]"))
	}
	cp, err := sess.s.CipherPath(C.GoString(relativePath))
	if err != nil {
		return errResult(err)
	}
	fr := sess.drive.CachedFractions(cp)
	if fr == nil {
		fr = [][2]float64{}
	}
	return jsonResult(fr, nil)
}

// GocryptfsRefreshDir makes the next listing of a Drive vault directory fetch
// it again, to pick up changes made elsewhere. A no-op for other sessions.
//
//export GocryptfsRefreshDir
func GocryptfsRefreshDir(sessionID C.int, relativePath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	if sess.drive == nil {
		return rcOK
	}
	cp, err := sess.s.CipherPath(C.GoString(relativePath))
	if err != nil {
		return codeRC(err)
	}
	sess.drive.Refresh(cp)
	sess.dropAllFiles()
	return rcOK
}

// --- imagethat-web servers --------------------------------------------------------

var servers handles[*remote.Client]

// GocryptfsServerNew returns a handle for the server at baseURL (pinnedSHA256
// as for GocryptfsOpenRemote), or -5 for a bad URL. Its session cookie and
// the passwords it re-authenticates with stay in this process's memory.
//
//export GocryptfsServerNew
func GocryptfsServerNew(baseURL, pinnedSHA256 *C.char) C.int {
	c, err := remote.New(C.GoString(baseURL), remote.Options{PinnedSHA256: C.GoString(pinnedSHA256)})
	if err != nil {
		return rcBackend
	}
	return servers.add(c)
}

//export GocryptfsServerFree
func GocryptfsServerFree(h C.int) { servers.remove(h) }

func serverHandle(h C.int) (*remote.Client, context.Context, bool) {
	c, ok := servers.get(h)
	return c, context.Background(), ok
}

// GocryptfsServerGetMode: 0 server-decrypt, 1 client-decrypt, or a negative code.
//
//export GocryptfsServerGetMode
func GocryptfsServerGetMode(h C.int) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	mode, err := c.Info(ctx)
	if err != nil {
		return codeRC(err)
	}
	if mode == remote.ModeClientDecrypt {
		return 1
	}
	return 0
}

// GocryptfsServerLogin sends a client-decrypt server its access password:
// 0, -7 wrong access password, or another negative code.
//
//export GocryptfsServerLogin
func GocryptfsServerLogin(h C.int, accessPassword *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Login(ctx, C.GoString(accessPassword)))
}

// GocryptfsServerVaults returns the server's vaults as JSON {id, name, unlocked}.
//
//export GocryptfsServerVaults
func GocryptfsServerVaults(h C.int) C.DecryptResult {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return errResult(errBadHandle)
	}
	vs, err := c.Vaults(ctx)
	if vs == nil {
		vs = []remote.VaultInfo{}
	}
	return jsonResult(vs, err)
}

//export GocryptfsServerUnlock
func GocryptfsServerUnlock(h C.int, vaultID, password *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Unlock(ctx, C.GoString(vaultID), C.GoString(password)))
}

//export GocryptfsServerLock
func GocryptfsServerLock(h C.int, vaultID *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Lock(ctx, C.GoString(vaultID)))
}

//export GocryptfsServerLogout
func GocryptfsServerLogout(h C.int) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Logout(ctx))
}

// GocryptfsServerList returns a decrypted directory as JSON
// {name, isDir, size, mediaType} entries.
//
//export GocryptfsServerList
func GocryptfsServerList(h C.int, vaultID, dir *C.char) C.DecryptResult {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return errResult(errBadHandle)
	}
	entries, err := c.List(ctx, C.GoString(vaultID), C.GoString(dir))
	if entries == nil {
		entries = []vault.Entry{}
	}
	return jsonResult(entries, err)
}

// GocryptfsServerFileSize is a file's decrypted size, or a negative code.
//
//export GocryptfsServerFileSize
func GocryptfsServerFileSize(h C.int, vaultID, path *C.char) C.int64_t {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	n, err := c.FileSize(ctx, C.GoString(vaultID), C.GoString(path))
	if err != nil {
		return C.int64_t(codeRC(err))
	}
	return C.int64_t(n)
}

// GocryptfsServerReadRange reads up to length decrypted bytes at offset into
// outBuf and returns how many, or a negative code.
//
//export GocryptfsServerReadRange
func GocryptfsServerReadRange(h C.int, vaultID, path *C.char, offset C.int64_t, length C.int32_t, outBuf unsafe.Pointer) C.int32_t {
	c, ctx, ok := serverHandle(h)
	if !ok || length < 0 || offset < 0 {
		return rcNoConfig
	}
	if length == 0 {
		return 0
	}
	rc, _, err := c.Open(ctx, C.GoString(vaultID), C.GoString(path), int64(offset), int64(length))
	if err != nil {
		return C.int32_t(codeRC(err))
	}
	defer rc.Close()
	buf := unsafe.Slice((*byte)(outBuf), int(length))
	n, err := io.ReadFull(rc, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return rcBackend
	}
	return C.int32_t(n)
}

// GocryptfsServerThumbnail is the server's JPEG preview; size 0 when it has
// none.
//
//export GocryptfsServerThumbnail
func GocryptfsServerThumbnail(h C.int, vaultID, path *C.char, size C.int) C.DecryptResult {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return errResult(errBadHandle)
	}
	b, err := c.Thumbnail(ctx, C.GoString(vaultID), C.GoString(path), int(size))
	if err != nil {
		return errResult(err)
	}
	return toCResult(b)
}

// GocryptfsServerUploadBegin starts streaming dir/name to the server and
// returns a writer ID for GocryptfsWriterWrite/Close/Abort. size is the byte
// count or -1.
//
//export GocryptfsServerUploadBegin
func GocryptfsServerUploadBegin(h C.int, vaultID, dir, name *C.char, size C.int64_t) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	id, d, n := C.GoString(vaultID), C.GoString(dir), C.GoString(name)
	return newWriter(func(r io.Reader) error {
		return c.Upload(ctx, id, d, n, r, int64(size))
	}, nil)
}

//export GocryptfsServerMkdir
func GocryptfsServerMkdir(h C.int, vaultID, path *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Mkdir(ctx, C.GoString(vaultID), C.GoString(path)))
}

//export GocryptfsServerMove
func GocryptfsServerMove(h C.int, vaultID, from, to *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Move(ctx, C.GoString(vaultID), C.GoString(from), C.GoString(to)))
}

//export GocryptfsServerDelete
func GocryptfsServerDelete(h C.int, vaultID, path *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	return codeRC(c.Delete(ctx, C.GoString(vaultID), C.GoString(path)))
}

// GocryptfsServerOpenVault opens a vault over the server's ciphertext
// endpoints with this connection (after GocryptfsServerLogin on a
// client-decrypt server) and decrypts here. cacheDir, when not "", keeps
// files passed to GocryptfsCacheFile. Returns a read-only session ID.
//
//export GocryptfsServerOpenVault
func GocryptfsServerOpenVault(h C.int, vaultID, password, cacheDir *C.char) C.int {
	c, ctx, ok := serverHandle(h)
	if !ok {
		return rcNoConfig
	}
	id, pw, dir := C.GoString(vaultID), C.GoString(password), C.GoString(cacheDir)
	if dir != "" {
		s, err := c.OpenVaultCached(ctx, id, pw, dir)
		if err != nil {
			return codeRC(err)
		}
		return register(s)
	}
	s, err := c.OpenVault(ctx, id, pw)
	if err != nil {
		return codeRC(err)
	}
	return register(s)
}
