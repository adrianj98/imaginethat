package mobile

import (
	"context"
	"encoding/json"
	"time"

	"github.com/adrianm/imagethat/core/drive"
)

// Drive is a Google Drive account. The app stores the refresh token (in the
// Keystore-backed store) and hands it back to NewDrive on the next launch.
type Drive struct {
	c *drive.Client
}

// NewDrive returns a Drive for the user's OAuth client (a "Desktop app"
// client from Google Cloud, as on the Mac). refreshToken is "" until the
// user has signed in.
func NewDrive(clientID, clientSecret, refreshToken string) *Drive {
	return &Drive{c: drive.New(drive.Config{ClientID: clientID, ClientSecret: clientSecret, RefreshToken: refreshToken})}
}

// newDriveWith is for tests against a fake Drive.
func newDriveWith(cfg drive.Config) *Drive { return &Drive{c: drive.New(cfg)} }

// Connected reports whether the user has signed in.
func (d *Drive) Connected() bool { return d.c.Connected() }

// RefreshToken is the token to store after SignIn.
func (d *Drive) RefreshToken() string { return d.c.RefreshToken() }

// SignOut forgets the tokens; the app deletes its stored copy.
func (d *Drive) SignOut() { d.c.SignOut() }

// SignIn is a sign-in in progress.
type SignIn struct {
	f *drive.Flow
}

// StartSignIn begins signing in. Open URL() in a browser (a Custom Tab), then
// call Await on a worker thread.
func (d *Drive) StartSignIn() (*SignIn, error) {
	f, err := d.c.StartSignIn()
	if err != nil {
		return nil, wrapErr(err)
	}
	return &SignIn{f: f}, nil
}

// URL is Google's consent page.
func (s *SignIn) URL() string { return s.f.URL }

// Await blocks until Google redirects back (at most 10 minutes), then
// finishes signing in. Errors: "cancelled:", "auth:".
func (s *SignIn) Await() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return wrapErr(s.f.Wait(ctx))
}

// Cancel stops a pending Await. (Not "Wait": Java reserves Object.wait.)
func (s *SignIn) Cancel() { s.f.Cancel() }

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// List returns a folder's entries ("root" is My Drive) as a JSON array of
// {id, name, mimeType, size, modifiedTime, thumbnailLink, isFolder}.
func (d *Drive) List(folderID string) (string, error) {
	files, err := d.c.List(context.Background(), folderID)
	if err != nil {
		return "", wrapErr(err)
	}
	if files == nil {
		files = []drive.File{}
	}
	return jsonString(files)
}

// ListMedia is List restricted to folders, images and videos, sorted folders
// first, for browsing a plain Drive folder.
func (d *Drive) ListMedia(folderID string) (string, error) {
	files, err := d.c.ListMedia(context.Background(), folderID)
	if err != nil {
		return "", wrapErr(err)
	}
	if files == nil {
		files = []drive.File{}
	}
	return jsonString(files)
}

// FindVaultRoot reports the vault folderID is in, as JSON {rootId, path}, or
// "" when it is not in a vault.
func (d *Drive) FindVaultRoot(folderID string) (string, error) {
	root, err := d.c.FindVaultRoot(context.Background(), folderID, 0)
	if err != nil {
		if errcodeNotFound(err) {
			return "", nil
		}
		return "", wrapErr(err)
	}
	return jsonString(root)
}

// ReadRange downloads n bytes of a plain file at off; fewer at its end.
func (d *Drive) ReadRange(fileID string, off int64, n int) ([]byte, error) {
	b, err := d.c.ReadRange(context.Background(), fileID, off, int64(n))
	return b, wrapErr(err)
}

// Thumbnail fetches Drive's own preview of a plain file.
func (d *Drive) Thumbnail(thumbnailLink string, maxEdge int) ([]byte, error) {
	b, err := d.c.Thumbnail(context.Background(), thumbnailLink, maxEdge)
	return b, wrapErr(err)
}

// Upload creates a plain file and returns it as JSON.
func (d *Drive) Upload(parentID, name, mimeType string, data []byte) (string, error) {
	f, err := d.c.Upload(context.Background(), parentID, name, mimeType, data)
	if err != nil {
		return "", wrapErr(err)
	}
	return jsonString(f)
}

// CreateFolder makes a folder and returns it as JSON.
func (d *Drive) CreateFolder(parentID, name string) (string, error) {
	f, err := d.c.CreateFolder(context.Background(), parentID, name)
	if err != nil {
		return "", wrapErr(err)
	}
	return jsonString(f)
}

// Delete removes a file or folder for good.
func (d *Drive) Delete(fileID string) error {
	return wrapErr(d.c.Delete(context.Background(), fileID))
}

// Move moves fileID from oldParent to newParent and/or renames it ("" keeps
// the name).
func (d *Drive) Move(fileID, oldParent, newParent, newName string) error {
	_, err := d.c.Move(context.Background(), fileID, oldParent, newParent, newName)
	return wrapErr(err)
}

// OpenDrive unlocks the gocryptfs vault in the Drive folder rootID. Downloaded
// ciphertext is kept in the network cache in cacheDir (see networkRanges);
// "" caches nothing. The vault is writable.
func OpenDrive(d *Drive, rootID, password, cacheDir string) (*Vault, error) {
	ranges, err := networkRanges(cacheDir)
	if err != nil {
		return nil, err
	}
	s, v, err := drive.OpenVault(d.c, rootID, password, ranges)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: s, drive: v}, nil
}

// InitDrive creates a new empty vault in the (empty) Drive folder folderID.
func InitDrive(d *Drive, folderID, password string) error {
	return wrapErr(drive.InitVault(d.c, folderID, password))
}

// CachedRanges reports which parts of a Drive vault file are held on the
// device, as a JSON array of [start, end) fractions of its length, for a
// buffered strip on a video scrubber. "[]" for other vaults.
func (v *Vault) CachedRanges(path string) (string, error) {
	if v.drive == nil {
		return "[]", nil
	}
	cp, err := v.s.CipherPath(path)
	if err != nil {
		return "", wrapErr(err)
	}
	fr := v.drive.CachedFractions(cp)
	if fr == nil {
		fr = [][2]float64{}
	}
	return jsonString(fr)
}

// Refresh makes the next listing of dir in a Drive vault fetch it again,
// picking up changes made on other devices. A no-op for other vaults.
func (v *Vault) Refresh(dir string) error {
	if v.drive == nil {
		return nil
	}
	cp, err := v.s.CipherPath(dir)
	if err != nil {
		return wrapErr(err)
	}
	v.drive.Refresh(cp)
	return nil
}
