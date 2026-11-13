package drivetest

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// An upload chunk whose sender stalls must not hold up the rest of the
// server, as it does not on the real Drive.
func TestStalledChunkDoesNotBlockOthers(t *testing.T) {
	s := New()
	defer s.Close()
	s.mu.Lock()
	s.sessions["1"] = &session{meta: map[string]any{"name": "f", "parents": []any{"root"}}}
	s.mu.Unlock()

	// Half a chunk, then silence, on a connection held open.
	c, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "PUT /upload/session/1 HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Range: bytes 0-99/*\r\nContent-Length: 100\r\n\r\n", s.token)
	_, _ = c.Write(make([]byte, 50))
	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, s.URL+"/drive/v3/files/root?fields=id", nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("another request waited on the stalled upload")
	}
}
