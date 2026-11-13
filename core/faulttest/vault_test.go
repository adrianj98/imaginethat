package faulttest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/core/vault"
)

func TestTemplateVaultsAreCheapToUnlockAndSeparate(t *testing.T) {
	tp := NewTemplate(t, "pw")
	first, second := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	tp.Dir(t, first)
	tp.Dir(t, second)

	raw, err := os.ReadFile(filepath.Join(first, "gocryptfs.conf"))
	if err != nil {
		t.Fatal(err)
	}
	var conf struct{ ScryptObject struct{ N int } }
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	if conf.ScryptObject.N != 1024 {
		t.Errorf("scrypt N = %d, want the minimum, 1024", conf.ScryptObject.N)
	}

	s, err := vault.OpenLocal(first, tp.Password)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	defer s.Close()
	if _, err := s.Create("a.txt", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile("a.txt"); err != nil || string(got) != "hello" {
		t.Errorf("read back %q, %v", got, err)
	}

	other, err := vault.OpenLocal(second, tp.Password)
	if err != nil {
		t.Fatalf("unlock the second copy: %v", err)
	}
	defer other.Close()
	if list, err := other.ListDir(""); err != nil || len(list) != 0 {
		t.Errorf("the second copy lists %v, %v; want it empty", list, err)
	}
}
