package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/core/vault"
)

// The browser's crypto (web/src/crypto) is tested against this manifest: what
// native Go decrypts from the fixture vault, file by file. This test keeps the
// manifest honest — it fails when the manifest no longer matches what Go
// decrypts, and -update-browser-manifest rewrites it.
var updateBrowserManifest = flag.Bool("update-browser-manifest", false, "rewrite web/src/crypto/testdata/manifest.json")

const browserManifestPath = "../web/src/crypto/testdata/manifest.json"

type manifestEntry struct {
	Path       string `json:"path"`
	CipherPath string `json:"cipherPath"`
	IsDir      bool   `json:"isDir"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256,omitempty"`
}

type browserManifest struct {
	Vault    string          `json:"vault"`
	Password string          `json:"password"`
	Entries  []manifestEntry `json:"entries"`
}

func TestBrowserCryptoManifest(t *testing.T) {
	s, err := vault.OpenLocal(fixtureSrc, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	m := browserManifest{Vault: "core/vault/testdata/vault", Password: fixturePassword}
	var walkDir func(dir string)
	walkDir = func(dir string) {
		entries, err := s.ListDir(dir)
		if err != nil {
			t.Fatalf("ListDir(%q): %v", dir, err)
		}
		for _, e := range entries {
			p := path.Join(dir, e.Name)
			cp, err := s.CipherPath(p)
			if err != nil {
				t.Fatalf("CipherPath(%q): %v", p, err)
			}
			me := manifestEntry{Path: p, CipherPath: cp, IsDir: e.IsDir}
			if e.IsDir {
				m.Entries = append(m.Entries, me)
				walkDir(p)
				continue
			}
			data, err := s.ReadFile(p)
			if err != nil {
				t.Fatalf("ReadFile(%q): %v", p, err)
			}
			sum := sha256.Sum256(data)
			me.Size, me.SHA256 = int64(len(data)), hex.EncodeToString(sum[:])
			m.Entries = append(m.Entries, me)
		}
	}
	walkDir("")

	want, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if *updateBrowserManifest {
		if err := os.MkdirAll(filepath.Dir(browserManifestPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(browserManifestPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(browserManifestPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is stale or missing (%v); run: go test ./server -run TestBrowserCryptoManifest -update-browser-manifest", browserManifestPath, err)
	}
}
