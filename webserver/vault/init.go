package vault

import (
	"crypto/rand"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/configfile"
)

// Init creates an empty gocryptfs vault (AES-GCM, encrypted and long names,
// per-directory IVs) in the root of v, protected by password.
//
// It refuses to touch a location that already holds a gocryptfs.conf; the
// returned error then wraps fs.ErrExist.
func Init(v WriteVFS, password string) error {
	if v.Exists(configfile.ConfDefaultName) {
		return fmt.Errorf("vault: %s already exists: %w", configfile.ConfDefaultName, fs.ErrExist)
	}

	// configfile.Create only writes to a file path, so build the config in a
	// scratch directory. It holds nothing but the password-encrypted master
	// key and is removed before returning.
	tmp, err := os.MkdirTemp("", "vault-init-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	confPath := filepath.Join(tmp, configfile.ConfDefaultName)
	pw := []byte(password)
	defer wipe(pw)
	err = configfile.Create(&configfile.CreateArgs{
		Filename: confPath,
		Password: pw,
		LogN:     configfile.ScryptDefaultLogN,
		Creator:  "imagethat",
	})
	if err != nil {
		return fmt.Errorf("vault: create config: %w", err)
	}
	conf, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}

	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return err
	}
	if err := v.WriteFile(configfile.ConfDefaultName, conf); err != nil {
		return err
	}
	return v.WriteFile("gocryptfs.diriv", iv)
}
