package drive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Scope is the OAuth scope the apps ask for: full Drive, because a vault's
// files were usually written by gocryptfs or another app, not by us.
const Scope = "https://www.googleapis.com/auth/drive"

// ErrCancelled means the sign-in was cancelled (Flow.Cancel).
var ErrCancelled = errors.New("drive: sign-in cancelled")

// Flow is one sign-in with Google using a loopback redirect, the flow Google
// documents for desktop apps. Go listens on 127.0.0.1, the host opens URL in
// the system browser (or a Custom Tab on Android), and Wait returns once
// Google redirects back with the authorization code. PKCE and a random state
// tie the redirect to this flow.
type Flow struct {
	// URL is the page the host must open.
	URL string

	c        *Client
	redirect string
	verifier string
	state    string
	ln       net.Listener
	srv      *http.Server

	once   sync.Once
	result chan flowResult
	cancel chan struct{}
}

type flowResult struct {
	code string
	err  error
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// StartSignIn begins a sign-in for c's OAuth client. The host opens f.URL,
// then calls Wait.
func (c *Client) StartSignIn() (*Flow, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("drive: sign-in listener: %w", err)
	}
	f := &Flow{
		c:        c,
		redirect: fmt.Sprintf("http://127.0.0.1:%d/oauth", ln.Addr().(*net.TCPAddr).Port),
		verifier: randomString(48),
		state:    randomString(24),
		ln:       ln,
		result:   make(chan flowResult, 1),
		cancel:   make(chan struct{}),
	}
	challenge := sha256.Sum256([]byte(f.verifier))
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {f.redirect},
		"response_type":         {"code"},
		"scope":                 {Scope},
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"state":                 {f.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	f.URL = c.cfg.AuthURL + "?" + q.Encode()

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", f.callback)
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go f.srv.Serve(ln)
	return f, nil
}

const signedInPage = `<!doctype html><meta charset="utf-8"><title>ImageThat</title>
<body style="font:16px -apple-system,system-ui,sans-serif;text-align:center;padding:4em">
<h2>%s</h2><p>You can close this window and return to ImageThat.</p>`

func (f *Flow) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var res flowResult
	switch {
	case q.Get("state") != f.state:
		// Not our redirect (or a stale tab): ignore it and keep waiting.
		http.Error(w, "unexpected sign-in response", http.StatusBadRequest)
		return
	case q.Get("error") != "":
		res.err = fmt.Errorf("%w: %s", ErrAuth, q.Get("error"))
		fmt.Fprintf(w, signedInPage, "Google Drive was not connected")
	case q.Get("code") == "":
		res.err = fmt.Errorf("%w: no authorization code", ErrAuth)
		fmt.Fprintf(w, signedInPage, "Google Drive was not connected")
	default:
		res.code = q.Get("code")
		fmt.Fprintf(w, signedInPage, "Google Drive is connected")
	}
	select {
	case f.result <- res:
	default:
	}
}

// Wait blocks until the browser comes back (or ctx ends, or Cancel), then
// exchanges the code for tokens. On success the client is signed in and
// RefreshToken returns the token to store.
func (f *Flow) Wait(ctx context.Context) error {
	defer f.close()
	var res flowResult
	select {
	case res = <-f.result:
	case <-f.cancel:
		return ErrCancelled
	case <-ctx.Done():
		return ctx.Err()
	}
	if res.err != nil {
		return res.err
	}
	tr, err := f.c.postToken(ctx, url.Values{
		"code":          {res.code},
		"client_id":     {f.c.cfg.ClientID},
		"client_secret": {f.c.cfg.ClientSecret},
		"redirect_uri":  {f.redirect},
		"grant_type":    {"authorization_code"},
		"code_verifier": {f.verifier},
	})
	if err != nil {
		return err
	}
	if tr.RefreshToken == "" {
		return fmt.Errorf("%w: Google returned no refresh token", ErrAuth)
	}
	f.c.mu.Lock()
	f.c.setToken(tr)
	f.c.mu.Unlock()
	return nil
}

// Cancel stops a pending Wait with ErrCancelled.
func (f *Flow) Cancel() {
	f.once.Do(func() { close(f.cancel) })
}

func (f *Flow) close() {
	f.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = f.srv.Shutdown(ctx)
}

// SignOut forgets the tokens. The host deletes the stored refresh token.
func (c *Client) SignOut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh, c.access, c.expiry = "", "", time.Time{}
}
