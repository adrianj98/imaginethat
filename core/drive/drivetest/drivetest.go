// Package drivetest is an in-memory fake of the parts of the Google Drive v3
// API and OAuth token endpoint that package drive uses, for tests here and in
// the app bindings.
package drivetest

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adrianm/imagethat/core/drive"
)

// Server is a fake Drive. The zero folder "root" exists from the start.
type Server struct {
	*httptest.Server

	// PageSize caps files.list pages, to exercise paging. Default 1000.
	PageSize int

	mu       sync.Mutex
	files    map[string]*entry
	nextID   int
	clock    int
	sessions map[string]*session
	token    string
	failNext int // media requests to fail with 503

	// Requests counts every API request; MediaRequests only downloads.
	Requests, MediaRequests atomic.Int64
}

type entry struct {
	id, name, mime string
	parent         string
	data           []byte
	modified       string
}

type session struct {
	meta   map[string]any
	fileID string
	data   []byte
}

const refreshToken = "fake-refresh-token"

// New starts a fake Drive.
func New() *Server {
	s := &Server{
		PageSize: 1000,
		files:    map[string]*entry{"root": {id: "root", name: "My Drive", mime: drive.FolderMime}},
		sessions: make(map[string]*session),
		token:    "access-1",
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Config is a drive.Config for this server, already signed in.
func (s *Server) Config() drive.Config {
	return drive.Config{
		ClientID: "client", ClientSecret: "secret", RefreshToken: refreshToken,
		APIBase: s.URL, TokenURL: s.URL + "/token", AuthURL: s.URL + "/auth",
	}
}

// Client is a signed-in client for this server.
func (s *Server) Client() *drive.Client { return drive.New(s.Config()) }

// ExpireToken makes the current access token invalid, so the next request
// gets a 401 and the client must refresh.
func (s *Server) ExpireToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, _ := strconv.Atoi(strings.TrimPrefix(s.token, "access-"))
	s.token = fmt.Sprintf("access-%d", n+1)
}

// FailMedia makes the next n downloads fail with 503.
func (s *Server) FailMedia(n int) {
	s.mu.Lock()
	s.failNext = n
	s.mu.Unlock()
}

func (s *Server) tick() string {
	s.clock++
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(s.clock) * time.Second).Format(time.RFC3339Nano)
}

func (s *Server) add(parent, name, mimeType string, data []byte) *entry {
	s.nextID++
	e := &entry{id: fmt.Sprintf("id%d", s.nextID), name: name, mime: mimeType, parent: parent, data: data, modified: s.tick()}
	s.files[e.id] = e
	return e
}

// AddFolder creates a folder and returns its ID.
func (s *Server) AddFolder(parent, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(parent, name, drive.FolderMime, nil).id
}

// AddFile creates a file and returns its ID.
func (s *Server) AddFile(parent, name, mimeType string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(parent, name, mimeType, append([]byte(nil), data...)).id
}

// SetContent replaces a file's bytes as another device would.
func (s *Server) SetContent(id string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.files[id]
	e.data = append([]byte(nil), data...)
	e.modified = s.tick()
}

// Content returns a file's bytes, or nil.
func (s *Server) Content(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.files[id]; ok {
		return append([]byte(nil), e.data...)
	}
	return nil
}

// Lookup finds the entry called name in parent, returning its ID or "".
func (s *Server) Lookup(parent, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.files {
		if e.parent == parent && e.name == name {
			return e.id
		}
	}
	return ""
}

// Children lists the names in a folder, sorted.
func (s *Server) Children(parent string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.files {
		if e.parent == parent && e.id != "root" {
			out = append(out, e.name)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) entryJSON(e *entry, withParents bool) map[string]any {
	m := map[string]any{"id": e.id, "name": e.name, "mimeType": e.mime, "modifiedTime": e.modified}
	if e.mime != drive.FolderMime {
		m["size"] = strconv.Itoa(len(e.data))
		if strings.HasPrefix(e.mime, "image/") {
			m["thumbnailLink"] = s.URL + "/thumb/" + e.id + "=s220"
		}
	}
	if withParents && e.parent != "" {
		m["parents"] = []string{e.parent}
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg}})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		s.serveToken(w, r)
		return
	}
	s.Requests.Add(1)
	s.mu.Lock()
	ok := r.Header.Get("Authorization") == "Bearer "+s.token
	s.mu.Unlock()
	if !ok {
		apiError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/thumb/"):
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("thumb:" + strings.TrimPrefix(p, "/thumb/")))
	case strings.HasPrefix(p, "/upload/session/"):
		s.serveSession(w, r, strings.TrimPrefix(p, "/upload/session/"))
	case p == "/upload/drive/v3/files" || strings.HasPrefix(p, "/upload/drive/v3/files/"):
		s.serveUpload(w, r, strings.TrimPrefix(strings.TrimPrefix(p, "/upload/drive/v3/files"), "/"))
	case p == "/drive/v3/files":
		if r.Method == http.MethodPost {
			s.serveCreate(w, r)
		} else {
			s.serveList(w, r)
		}
	case strings.HasPrefix(p, "/drive/v3/files/"):
		s.serveFile(w, r, strings.TrimPrefix(p, "/drive/v3/files/"))
	default:
		apiError(w, http.StatusNotFound, "no such endpoint")
	}
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	tok := s.token
	s.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "refresh_token":
		if r.Form.Get("refresh_token") != refreshToken {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "Token has been revoked."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "expires_in": 3600})
	case "authorization_code":
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "refresh_token": refreshToken, "expires_in": 3600})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
	}
}

var (
	reParent = regexp.MustCompile(`'((?:[^'\\]|\\.)*)' in parents`)
	reName   = regexp.MustCompile(`name='((?:[^'\\]|\\.)*)'`)
)

func unquote(s string) string {
	return strings.NewReplacer(`\'`, "'", `\\`, `\`).Replace(s)
}

func (s *Server) serveList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	m := reParent.FindStringSubmatch(q)
	if m == nil {
		apiError(w, http.StatusBadRequest, "unsupported query")
		return
	}
	parent := unquote(m[1])
	name := ""
	if n := reName.FindStringSubmatch(q); n != nil {
		name = unquote(n[1])
	}
	mediaOnly := strings.Contains(q, "mimeType contains")

	s.mu.Lock()
	var all []*entry
	for _, e := range s.files {
		if e.parent != parent || e.id == "root" || (name != "" && e.name != name) {
			continue
		}
		if mediaOnly && e.mime != drive.FolderMime && !strings.HasPrefix(e.mime, "image/") && !strings.HasPrefix(e.mime, "video/") {
			continue
		}
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
	end := start + s.PageSize
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	} else {
		end = len(all)
	}
	files := make([]map[string]any, 0, end-start)
	for _, e := range all[start:end] {
		files = append(files, s.entryJSON(e, false))
	}
	s.mu.Unlock()
	body := map[string]any{"files": files}
	if next != "" {
		body["nextPageToken"] = next
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) serveCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string   `json:"name"`
		MimeType string   `json:"mimeType"`
		Parents  []string `json:"parents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Parents) != 1 {
		apiError(w, http.StatusBadRequest, "bad create")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[body.Parents[0]]; !ok {
		apiError(w, http.StatusNotFound, "parent not found")
		return
	}
	writeJSON(w, http.StatusOK, s.entryJSON(s.add(body.Parents[0], body.Name, body.MimeType, nil), false))
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	e, ok := s.files[id]
	s.mu.Unlock()
	if !ok {
		apiError(w, http.StatusNotFound, "File not found: "+id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("alt") != "media" {
			s.mu.Lock()
			m := s.entryJSON(e, true)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, m)
			return
		}
		s.MediaRequests.Add(1)
		s.mu.Lock()
		fail := s.failNext > 0
		if fail {
			s.failNext--
		}
		data := e.data
		s.mu.Unlock()
		if fail {
			apiError(w, http.StatusServiceUnavailable, "backend error")
			return
		}
		serveRange(w, r, data)
	case http.MethodDelete:
		s.mu.Lock()
		s.deleteLocked(id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		defer s.mu.Unlock()
		q := r.URL.Query()
		if add := q.Get("addParents"); add != "" {
			if q.Get("removeParents") != e.parent {
				apiError(w, http.StatusBadRequest, "removeParents does not match")
				return
			}
			e.parent = add
		}
		if body.Name != "" {
			e.name = body.Name
		}
		e.modified = s.tick()
		writeJSON(w, http.StatusOK, s.entryJSON(e, false))
	default:
		apiError(w, http.StatusMethodNotAllowed, "method")
	}
}

func (s *Server) deleteLocked(id string) {
	delete(s.files, id)
	for cid, c := range s.files {
		if c.parent == id {
			s.deleteLocked(cid)
		}
	}
}

func serveRange(w http.ResponseWriter, r *http.Request, data []byte) {
	rng := r.Header.Get("Range")
	if rng == "" {
		_, _ = w.Write(data)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &start, &end); err != nil {
		apiError(w, http.StatusBadRequest, "bad range")
		return
	}
	if start >= int64(len(data)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if end >= int64(len(data)) {
		end = int64(len(data)) - 1
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[start : end+1])
}

func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request, id string) {
	switch r.URL.Query().Get("uploadType") {
	case "multipart":
		meta, data, err := readMultipart(r)
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		e, status := s.commitLocked(meta, id, data)
		if e == nil {
			apiError(w, status, "commit failed")
			return
		}
		writeJSON(w, http.StatusOK, s.entryJSON(e, false))
	case "resumable":
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		s.mu.Lock()
		s.nextID++
		sid := strconv.Itoa(s.nextID)
		s.sessions[sid] = &session{meta: meta, fileID: id}
		s.mu.Unlock()
		w.Header().Set("Location", s.URL+"/upload/session/"+sid)
		w.WriteHeader(http.StatusOK)
	default:
		apiError(w, http.StatusBadRequest, "uploadType")
	}
}

func (s *Server) commitLocked(meta map[string]any, id string, data []byte) (*entry, int) {
	if id != "" {
		e, ok := s.files[id]
		if !ok {
			return nil, http.StatusNotFound
		}
		e.data = data
		e.modified = s.tick()
		return e, 0
	}
	name, _ := meta["name"].(string)
	parents, _ := meta["parents"].([]any)
	if len(parents) != 1 {
		return nil, http.StatusBadRequest
	}
	parent, _ := parents[0].(string)
	if _, ok := s.files[parent]; !ok {
		return nil, http.StatusNotFound
	}
	return s.add(parent, name, "application/octet-stream", data), 0
}

func readMultipart(r *http.Request) (map[string]any, []byte, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, nil, err
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	var meta map[string]any
	if err := json.NewDecoder(part).Decode(&meta); err != nil {
		return nil, nil, err
	}
	part, err = mr.NextPart()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(part)
	return meta, data, err
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request, sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sid]
	if !ok {
		apiError(w, http.StatusNotFound, "no such upload")
		return
	}
	if r.Method == http.MethodDelete {
		delete(s.sessions, sid)
		w.WriteHeader(499)
		return
	}
	data, _ := io.ReadAll(r.Body)
	cr := r.Header.Get("Content-Range") // "bytes a-b/total", "bytes */total", "bytes */*"
	rest := strings.TrimPrefix(cr, "bytes ")
	slash := strings.LastIndex(rest, "/")
	if slash < 0 {
		apiError(w, http.StatusBadRequest, "bad Content-Range")
		return
	}
	rng, total := rest[:slash], rest[slash+1:]
	if rng != "*" {
		var a, b int64
		if _, err := fmt.Sscanf(rng, "%d-%d", &a, &b); err != nil || a != int64(len(sess.data)) || b-a+1 != int64(len(data)) {
			apiError(w, http.StatusBadRequest, "chunk out of order")
			return
		}
		sess.data = append(sess.data, data...)
	}
	if total == "*" || total != strconv.Itoa(len(sess.data)) {
		if len(sess.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(sess.data)-1))
		}
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	delete(s.sessions, sid)
	e, status := s.commitLocked(sess.meta, sess.fileID, sess.data)
	if e == nil {
		apiError(w, status, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, s.entryJSON(e, false))
}
