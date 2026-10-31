// Package drive talks to Google Drive (REST API v3) for the apps: listing and
// moving files, ranged downloads, streaming uploads, and OAuth. VaultFS puts
// a gocryptfs vault stored in Drive behind vault.WriteVFS, so the vault engine
// reads, writes and caches it like any other backend.
//
// Everything that crosses the network for a vault is ciphertext. The refresh
// token is the only long-lived secret; the host stores it (Keychain, Android
// Keystore) and hands it back with Config.RefreshToken.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FolderMime is the MIME type Drive gives folders.
const FolderMime = "application/vnd.google-apps.folder"

// Errors a caller can tell apart with errors.Is.
var (
	// ErrNotConnected means there is no refresh token: sign in first.
	ErrNotConnected = errors.New("drive: not connected")
	// ErrAuth means Google refused the credentials (revoked token, wrong
	// client secret). Signing in again fixes it.
	ErrAuth = errors.New("drive: authorization failed")
)

// HTTPError is a Drive API failure. A 404 also matches fs.ErrNotExist and a
// 403 that is not a rate limit matches fs.ErrPermission, so callers (and
// xfer's retry logic) can treat them like filesystem errors.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("drive: HTTP %d: %s", e.Status, e.Message) }

func (e *HTTPError) Is(target error) bool {
	switch target {
	case fs.ErrNotExist:
		return e.Status == http.StatusNotFound
	case fs.ErrPermission:
		return e.Status == http.StatusForbidden && !e.rateLimited()
	}
	return false
}

func (e *HTTPError) rateLimited() bool {
	m := strings.ToLower(e.Message)
	return strings.Contains(m, "rate limit") || strings.Contains(m, "ratelimit") || strings.Contains(m, "quota")
}

// Config is what a Client needs. ClientID and ClientSecret are the OAuth
// client the user registered (a "Desktop app" client in Google Cloud).
type Config struct {
	ClientID     string
	ClientSecret string
	RefreshToken string

	// Tests point these at a fake server. Empty means Google's.
	APIBase  string // default https://www.googleapis.com
	TokenURL string // default https://oauth2.googleapis.com/token
	AuthURL  string // default https://accounts.google.com/o/oauth2/v2/auth
	// HTTPClient overrides the HTTP client. Default: 60s timeout.
	HTTPClient *http.Client
}

func (c *Config) defaults() {
	if c.APIBase == "" {
		c.APIBase = "https://www.googleapis.com"
	}
	if c.TokenURL == "" {
		c.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if c.AuthURL == "" {
		c.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	c.APIBase = strings.TrimRight(c.APIBase, "/")
}

// Client is a signed-in Drive client. It is safe for concurrent use.
type Client struct {
	cfg Config

	mu      sync.Mutex
	refresh string
	access  string
	expiry  time.Time
}

// New returns a client for cfg. With no refresh token every call fails with
// ErrNotConnected until the user signs in (see Flow).
func New(cfg Config) *Client {
	cfg.defaults()
	return &Client{cfg: cfg, refresh: cfg.RefreshToken}
}

// RefreshToken is the current refresh token, for the host to store.
func (c *Client) RefreshToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refresh
}

// Connected reports whether the client has a refresh token.
func (c *Client) Connected() bool { return c.RefreshToken() != "" }

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (c *Client) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive: token: %w", err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return nil, fmt.Errorf("drive: token: HTTP %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 500 {
		return nil, &HTTPError{Status: resp.StatusCode, Message: "token endpoint unavailable"}
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := tr.ErrorDescription
		if msg == "" {
			msg = tr.Error
		}
		return nil, fmt.Errorf("%w: %s", ErrAuth, msg)
	}
	return &tr, nil
}

func (c *Client) setToken(tr *tokenResponse) {
	c.access = tr.AccessToken
	exp := tr.ExpiresIn
	if exp <= 0 {
		exp = 3600
	}
	c.expiry = time.Now().Add(time.Duration(exp-60) * time.Second)
	if tr.RefreshToken != "" {
		c.refresh = tr.RefreshToken
	}
}

// token returns a valid access token, refreshing it when needed.
func (c *Client) token(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.access != "" && time.Now().Before(c.expiry) {
		return c.access, nil
	}
	if c.refresh == "" {
		return "", ErrNotConnected
	}
	tr, err := c.postToken(ctx, url.Values{
		"refresh_token": {c.refresh},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"grant_type":    {"refresh_token"},
	})
	if err != nil {
		return "", err
	}
	c.setToken(tr)
	return c.access, nil
}

// do sends an authorized request. build is called again for the one retry
// after a 401 (an access token revoked early), so bodies must be rebuildable.
func (c *Client) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		tok, err := c.token(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := c.cfg.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("drive: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
}

// check turns a non-2xx response into an *HTTPError and closes it.
func check(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		msg = e.Error.Message
	}
	return &HTTPError{Status: resp.StatusCode, Message: msg}
}

func (c *Client) api(path string, q url.Values) string {
	u := c.cfg.APIBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.api(path, q), nil)
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(method, c.api(path, q), bytes.NewReader(data))
		if err == nil {
			req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		}
		return req, err
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// File is one Drive entry.
type File struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MimeType      string `json:"mimeType"`
	Size          int64  `json:"size,string"`
	ModifiedTime  string `json:"modifiedTime"`
	ThumbnailLink string `json:"thumbnailLink,omitempty"`
	IsFolder      bool   `json:"isFolder"`
}

const fileFields = "id,name,mimeType,size,modifiedTime,thumbnailLink"

func (f *File) fix() { f.IsFolder = f.MimeType == FolderMime }

func quote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}

// list runs a files.list query, following nextPageToken to the end: Drive
// returns at most 1000 entries per page.
func (c *Client) list(ctx context.Context, query, orderBy string) ([]File, error) {
	var all []File
	page := ""
	for {
		q := url.Values{
			"q":        {query},
			"fields":   {"nextPageToken,files(" + fileFields + ")"},
			"pageSize": {"1000"},
		}
		if orderBy != "" {
			q.Set("orderBy", orderBy)
		}
		if page != "" {
			q.Set("pageToken", page)
		}
		var body struct {
			NextPageToken string `json:"nextPageToken"`
			Files         []File `json:"files"`
		}
		if err := c.getJSON(ctx, "/drive/v3/files", q, &body); err != nil {
			return nil, err
		}
		for i := range body.Files {
			body.Files[i].fix()
		}
		all = append(all, body.Files...)
		if body.NextPageToken == "" {
			return all, nil
		}
		page = body.NextPageToken
	}
}

// List returns every entry in a folder ("root" is My Drive).
func (c *Client) List(ctx context.Context, folderID string) ([]File, error) {
	return c.list(ctx, quote(folderID)+" in parents and trashed=false", "")
}

// ListMedia returns the subfolders and the image and video files in a
// folder, sorted by name, for browsing a plain (unencrypted) Drive folder.
func (c *Client) ListMedia(ctx context.Context, folderID string) ([]File, error) {
	return c.list(ctx, quote(folderID)+" in parents and trashed=false and (mimeType="+quote(FolderMime)+
		" or mimeType contains 'image/' or mimeType contains 'video/')", "folder,name")
}

// Stat returns one file's metadata, including its parents.
func (c *Client) Stat(ctx context.Context, fileID string) (*File, []string, error) {
	var body struct {
		File
		Parents []string `json:"parents"`
	}
	if err := c.getJSON(ctx, "/drive/v3/files/"+url.PathEscape(fileID),
		url.Values{"fields": {fileFields + ",parents"}}, &body); err != nil {
		return nil, nil, err
	}
	body.File.fix()
	return &body.File, body.Parents, nil
}

// Find returns the entry called name in a folder, or fs.ErrNotExist.
func (c *Client) Find(ctx context.Context, folderID, name string) (*File, error) {
	files, err := c.list(ctx, "name="+quote(name)+" and "+quote(folderID)+" in parents and trashed=false", "")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, &HTTPError{Status: http.StatusNotFound, Message: name + " not found"}
	}
	return &files[0], nil
}

// VaultRoot is a vault found by FindVaultRoot.
type VaultRoot struct {
	RootID string `json:"rootId"`
	// Path is the folder names from the vault root down to the folder asked
	// about; empty when that folder is the root itself.
	Path []string `json:"path"`
}

// FindVaultRoot reports whether folderID is a gocryptfs vault or lies inside
// one, by looking for gocryptfs.conf in it and then in up to maxDepth
// ancestors. It returns fs.ErrNotExist when there is none.
func (c *Client) FindVaultRoot(ctx context.Context, folderID string, maxDepth int) (*VaultRoot, error) {
	if maxDepth <= 0 {
		maxDepth = 8
	}
	var names []string
	cur := folderID
	for i := 0; i <= maxDepth; i++ {
		if _, err := c.Find(ctx, cur, "gocryptfs.conf"); err == nil {
			return &VaultRoot{RootID: cur, Path: names}, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		f, parents, err := c.Stat(ctx, cur)
		if err != nil {
			return nil, err
		}
		if len(parents) == 0 {
			break
		}
		names = append([]string{f.Name}, names...)
		cur = parents[0]
	}
	return nil, &HTTPError{Status: http.StatusNotFound, Message: "not inside a vault"}
}

// Download streams a whole file.
func (c *Client) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.api("/drive/v3/files/"+url.PathEscape(fileID), url.Values{"alt": {"media"}}), nil)
	})
	if err != nil {
		return nil, err
	}
	if err := check(resp); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ReadRange downloads length bytes from offset. It returns fewer at the end
// of the file and none past it.
func (c *Client) ReadRange(ctx context.Context, fileID string, offset, length int64) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}
	resp, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, c.api("/drive/v3/files/"+url.PathEscape(fileID), url.Values{"alt": {"media"}}), nil)
		if err == nil {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		resp.Body.Close()
		return nil, nil
	}
	if err := check(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		// No range support on the other end: skip to the offset.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			return nil, nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, length))
	if err != nil {
		return nil, fmt.Errorf("drive: read %s: %w", fileID, err)
	}
	return data, nil
}

// Thumbnail fetches Drive's own preview for a plain file from its
// thumbnailLink, sized to about maxEdge pixels.
func (c *Client) Thumbnail(ctx context.Context, link string, maxEdge int) ([]byte, error) {
	if link == "" {
		return nil, &HTTPError{Status: http.StatusNotFound, Message: "no thumbnail"}
	}
	if maxEdge > 0 {
		// thumbnailLink ends in "=s220"; ask for the size we want instead.
		if i := strings.LastIndex(link, "=s"); i > 0 {
			if _, err := strconv.Atoi(link[i+2:]); err == nil {
				link = link[:i] + "=s" + strconv.Itoa(maxEdge)
			}
		}
	}
	resp, err := c.do(ctx, func() (*http.Request, error) { return http.NewRequest(http.MethodGet, link, nil) })
	if err != nil {
		return nil, err
	}
	if err := check(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// CreateFolder makes a folder called name in parentID.
func (c *Client) CreateFolder(ctx context.Context, parentID, name string) (*File, error) {
	var f File
	err := c.sendJSON(ctx, http.MethodPost, "/drive/v3/files", url.Values{"fields": {fileFields}},
		map[string]any{"name": name, "mimeType": FolderMime, "parents": []string{parentID}}, &f)
	if err != nil {
		return nil, err
	}
	f.fix()
	return &f, nil
}

// Delete removes a file, or a folder with everything in it, for good.
func (c *Client) Delete(ctx context.Context, fileID string) error {
	resp, err := c.do(ctx, func() (*http.Request, error) {
		return http.NewRequest(http.MethodDelete, c.api("/drive/v3/files/"+url.PathEscape(fileID), nil), nil)
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Move renames fileID to newName and, when newParent differs from oldParent,
// moves it there. An empty newName keeps the name.
func (c *Client) Move(ctx context.Context, fileID, oldParent, newParent, newName string) (*File, error) {
	q := url.Values{"fields": {fileFields}}
	if newParent != "" && newParent != oldParent {
		q.Set("addParents", newParent)
		q.Set("removeParents", oldParent)
	}
	body := map[string]any{}
	if newName != "" {
		body["name"] = newName
	}
	var f File
	if err := c.sendJSON(ctx, http.MethodPatch, "/drive/v3/files/"+url.PathEscape(fileID), q, body, &f); err != nil {
		return nil, err
	}
	f.fix()
	return &f, nil
}

// simpleUploadMax is the largest upload sent as one multipart request; bigger
// ones use a resumable session.
const simpleUploadMax = 5 << 20

// Upload creates a file called name in parentID from data.
func (c *Client) Upload(ctx context.Context, parentID, name, mimeType string, data []byte) (*File, error) {
	if len(data) > simpleUploadMax {
		w, err := c.NewUpload(ctx, parentID, name, mimeType, "")
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			w.Abort()
			return nil, err
		}
		return w.Finish()
	}
	return c.multipart(ctx, http.MethodPost, "", map[string]any{"name": name, "parents": []string{parentID}}, mimeType, data)
}

// Update replaces fileID's content with data.
func (c *Client) Update(ctx context.Context, fileID, mimeType string, data []byte) (*File, error) {
	if len(data) > simpleUploadMax {
		w, err := c.NewUpload(ctx, "", "", mimeType, fileID)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			w.Abort()
			return nil, err
		}
		return w.Finish()
	}
	return c.multipart(ctx, http.MethodPatch, fileID, map[string]any{}, mimeType, data)
}

func (c *Client) multipart(ctx context.Context, method, fileID string, meta map[string]any, mimeType string, data []byte) (*File, error) {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}})
	part.Write(metaJSON)
	part, _ = mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mimeOr(mimeType)}})
	part.Write(data)
	mw.Close()
	path := "/upload/drive/v3/files"
	if fileID != "" {
		path += "/" + url.PathEscape(fileID)
	}
	raw := body.Bytes()
	resp, err := c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(method, c.api(path, url.Values{"uploadType": {"multipart"}, "fields": {fileFields}}), bytes.NewReader(raw))
		if err == nil {
			req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if err := check(resp); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var f File
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&f); err != nil {
		return nil, fmt.Errorf("drive: upload response: %w", err)
	}
	f.fix()
	return &f, nil
}

func mimeOr(m string) string {
	if m == "" {
		return "application/octet-stream"
	}
	return m
}
