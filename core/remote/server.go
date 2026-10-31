package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Server session calls the apps share. Nothing here asks the server to
// decrypt: vaults are opened with OpenVault, which fetches ciphertext only.

// VaultInfo is one vault the server offers.
type VaultInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Vaults lists the server's vaults. A client-decrypt server answers only
// after Login.
func (c *Client) Vaults(ctx context.Context) ([]VaultInfo, error) {
	resp, err := c.do(func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/api/vaults", nil), nil)
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrLocked
	default:
		return nil, fmt.Errorf("remote: vaults: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Vaults []VaultInfo `json:"vaults"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("remote: vaults: %w", err)
	}
	return body.Vaults, nil
}

// Logout ends the session on the server and forgets the access password.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	c.accessPassword = ""
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/api/logout", nil), nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return wrapTransport(err)
	}
	resp.Body.Close()
	return nil
}
