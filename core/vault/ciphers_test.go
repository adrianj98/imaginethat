package vault

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestAltCipherVaultsRead reads the AES-SIV and XChaCha20-Poly1305 fixtures.
//
// The main fixture uses the default AES-GCM cipher, so without these the other
// two content ciphers were never exercised at all.
func TestAltCipherVaultsRead(t *testing.T) {
	for name, dir := range altFixtureVaults() {
		t.Run(name, func(t *testing.T) {
			s := openVault(t, dir)

			entries, err := s.ListDir("")
			if err != nil {
				t.Fatalf("ListDir: %v", err)
			}
			listed := map[string]bool{}
			for _, e := range entries {
				listed[e.Name] = true
			}

			for _, f := range altFixtureFiles() {
				want := f.Content()

				got, err := s.ReadFile(f.Path)
				if err != nil {
					t.Errorf("ReadFile(%s): %v", f.Path, err)
					continue
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s: content mismatch (got %d bytes, want %d)", f.Path, len(got), len(want))
				}

				size, err := s.FileSize(f.Path)
				if err != nil || size != int64(len(want)) {
					t.Errorf("FileSize(%s) = %d, %v; want %d", f.Path, size, err, len(want))
				}
			}

			// The long name must still round-trip through its .name sidecar.
			if !listed[longFixtureName] {
				t.Errorf("long name missing from listing")
			}

			// And ranged reads must be right for these ciphers too — the block
			// overhead differs between GCM, SIV and XChaCha.
			fh, err := s.Open("medium.bin")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer fh.Close()
			want := patternBytes(104, 20000)
			for _, off := range []int{0, 1, 4095, 4096, 4097, 8192, 19999} {
				length := 1000
				if off+length > len(want) {
					length = len(want) - off
				}
				buf := make([]byte, length)
				if _, err := fh.ReadAt(buf, int64(off)); err != nil && !errors.Is(err, io.EOF) {
					t.Fatalf("ReadAt(off=%d): %v", off, err)
				}
				if !bytes.Equal(buf, want[off:off+length]) {
					t.Errorf("ReadAt(off=%d): bytes differ from plaintext", off)
				}
			}
		})
	}
}

// TestAltCipherVaultsWrite checks the write path against the other ciphers,
// since EncryptBlock takes a different backend for each.
func TestAltCipherVaultsWrite(t *testing.T) {
	for name, dir := range altFixtureVaults() {
		t.Run(name, func(t *testing.T) {
			s, _ := openWritableVault(t, dir)

			want := patternBytes(7777, 9000)
			if _, err := s.Create("written.bin", bytes.NewReader(want)); err != nil {
				t.Fatalf("Create: %v", err)
			}
			got, err := s.ReadFile("written.bin")
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Error("written content did not round-trip")
			}
		})
	}
}

// TestCloseEveryCipher is the regression test for the double-wipe panic.
//
// Session.Close used to call contentEnc.Wipe() and then cryptoCore.Wipe() on
// the same core. ContentEnc.Wipe already wipes the core and nils it, so the
// second call type-asserted a nil AEADCipher. On AES-GCM that branch is never
// taken; on AES-SIV it panicked and, from the session reaper goroutine, would
// have taken the whole server down.
func TestCloseEveryCipher(t *testing.T) {
	vaults := map[string]string{"gcm": fixtureDir}
	for name, dir := range altFixtureVaults() {
		vaults[name] = dir
	}

	for name, dir := range vaults {
		t.Run(name, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(dir, "gocryptfs.conf")); err != nil {
				t.Skipf("test vault missing at %s", dir)
			}
			s, err := OpenLocal(dir, fixturePassword)
			if err != nil {
				t.Fatalf("OpenLocal: %v", err)
			}
			// Any of these panicking is the bug.
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("second Close: %v", err)
			}
			if _, err := s.ListDir(""); !errors.Is(err, ErrClosed) {
				t.Errorf("ListDir after Close: got %v, want ErrClosed", err)
			}
		})
	}
}

// TestCloseDuringRead locks a vault while a read is in flight.
//
// Close wipes the cipher objects, so without cryptoMu held across the whole
// read this nil-derefs inside DecryptBlock — which is what happens when a user
// hits Lock while a video is streaming, or when idle expiry fires mid-stream.
func TestCloseDuringRead(t *testing.T) {
	for i := 0; i < 20; i++ {
		s := openFixtureNoCleanup(t)

		fh, err := s.Open("large.bin")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		var wg sync.WaitGroup
		start := make(chan struct{})

		// Several readers streaming concurrently...
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				buf := make([]byte, 4096)
				for off := int64(0); off < 100000; off += 4096 {
					// The only acceptable outcomes are correct data or a
					// clean ErrClosed. A panic fails the test by crashing it.
					if _, err := fh.ReadAt(buf, off); err != nil {
						if errors.Is(err, ErrClosed) || errors.Is(err, io.EOF) {
							return
						}
						t.Errorf("ReadAt: unexpected error %v", err)
						return
					}
				}
			}()
		}

		// ...while the vault is locked out from under them.
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.Close()
		}()

		close(start)
		wg.Wait()
		fh.Close()
	}
}

// TestCloseDuringWrite is the same race on the upload path.
func TestCloseDuringWrite(t *testing.T) {
	for i := 0; i < 10; i++ {
		s, _ := openWritableVault(t, fixtureDir)

		var wg sync.WaitGroup
		start := make(chan struct{})

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Errors are fine; a panic is not.
			_, _ = s.Create("racy.bin", bytes.NewReader(patternBytes(1, 200000)))
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s.Close()
		}()

		close(start)
		wg.Wait()
	}
}

// openFixtureNoCleanup opens the fixture without registering a Close on
// cleanup, for tests that close it themselves.
func openFixtureNoCleanup(t *testing.T) *Session {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skipf("test vault missing at %s", fixtureDir)
	}
	s, err := OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	return s
}

// copyVaultTree copies a fixture vault's ciphertext into a temp dir so a test
// can mutate it without disturbing testdata.
func copyVaultTree(t *testing.T, src string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(src, "gocryptfs.conf")); err != nil {
		t.Skipf("test vault missing at %s (see fixture_test.go)", src)
	}
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy vault: %v", err)
	}
	return dst
}

// openWritableVault unlocks a private, writable copy of any fixture vault.
func openWritableVault(t *testing.T, src string) (*Session, string) {
	t.Helper()
	dst := copyVaultTree(t, src)
	s, err := OpenLocal(dst, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dst
}
