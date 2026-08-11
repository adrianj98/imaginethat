module github.com/adrianm/imagethat/webserver

go 1.24

// Dependency versions are pinned to match gocryptfs-lib/go.mod so the vault
// crypto here behaves identically to the crypto the macOS app ships.
require (
	github.com/aperturerobotics/jacobsa-crypto v1.1.0
	github.com/hanwen/go-fuse/v2 v2.8.0
	github.com/rfjakob/eme v1.1.2
	golang.org/x/crypto v0.33.0
	golang.org/x/image v0.24.0
	golang.org/x/sys v0.30.0
	golang.org/x/term v0.29.0
)
