package faulttest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/core/internal/gocryptfs/configfile"
	"github.com/adrianm/imagethat/core/vault"
)

// cheapLogN is the lowest scrypt cost gocryptfs loads (its scryptMinLogN).
const cheapLogN = 10

// Template is a new, empty vault held in memory, to stamp out as many
// copies as a test needs. Its password hashing is set to the lowest cost
// gocryptfs accepts, so a test can unlock it many times, also under the race
// detector, where the normal cost takes about a second per unlock.
// Everything else about it is a normal vault. Never do this to a real one.
type Template struct {
	Password string
	files    map[string][]byte
}

// NewTemplate makes the vault once. That costs two full-price password
// hashes, so a test binary should make one template and share it.
func NewTemplate(t testing.TB, password string) *Template {
	t.Helper()
	dir := t.TempDir()
	if err := vault.Init(vault.NewLocalFS(dir), password); err != nil {
		t.Fatalf("faulttest: init vault: %v", err)
	}
	cf, err := configfile.Load(filepath.Join(dir, configfile.ConfDefaultName))
	if err != nil {
		t.Fatalf("faulttest: load config: %v", err)
	}
	key, err := cf.DecryptMasterKey([]byte(password))
	if err != nil {
		t.Fatalf("faulttest: decrypt master key: %v", err)
	}
	cf.EncryptKey(key, []byte(password), cheapLogN)
	if err := cf.WriteFile(); err != nil {
		t.Fatalf("faulttest: write config: %v", err)
	}

	tp := &Template{Password: password, files: make(map[string][]byte)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("faulttest: %v", err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("faulttest: %v", err)
		}
		tp.files[e.Name()] = data
	}
	return tp
}

// WriteTo puts a copy of the vault at the root of v.
func (tp *Template) WriteTo(v vault.WriteVFS) error {
	for name, data := range tp.files {
		if err := v.WriteFile(name, data); err != nil {
			return err
		}
	}
	return nil
}

// Dir puts a copy of the vault in dir, creating it if needed.
func (tp *Template) Dir(t testing.TB, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("faulttest: %v", err)
	}
	if err := tp.WriteTo(vault.NewLocalFS(dir)); err != nil {
		t.Fatalf("faulttest: write vault to %s: %v", dir, err)
	}
}
