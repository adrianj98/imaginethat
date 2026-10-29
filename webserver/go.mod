module github.com/adrianm/imagethat/webserver

go 1.26.0

// Minimum toolchain: the latest Go 1.26 patch release when this was written.
// CI and release builds install the newest 1.26.x (setup-go go-version '1.26'
// with check-latest), so they pick up later patch releases too. Stay on 1.26:
// Go 1.27 changes behaviour the vault fixture tests depend on.
toolchain go1.26.10

// The crypto dependencies (jacobsa-crypto, eme, x/crypto, go-fuse) match
// gocryptfs-lib/go.mod, so the vault crypto here behaves like the crypto the
// macOS app ships. x/sys, x/image and x/mobile are newer here; they do not
// touch the crypto paths.
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
