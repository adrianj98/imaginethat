module github.com/adrianm/imagethat/webserver

go 1.26.0

// Dependency versions are pinned to match gocryptfs-lib/go.mod so the vault
// crypto here behaves identically to the crypto the macOS app ships.
require (
	github.com/aperturerobotics/jacobsa-crypto v1.1.0
	github.com/hanwen/go-fuse/v2 v2.8.0
	github.com/rfjakob/eme v1.1.2
	golang.org/x/crypto v0.33.0
	golang.org/x/image v0.47.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.29.0
)

require golang.org/x/mobile v0.0.0-20261011034528-197770444867

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)
