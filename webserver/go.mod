module github.com/adrianm/imagethat/webserver

go 1.26.0

// Minimum toolchain: the latest Go 1.26 patch release when this was written.
// CI and release builds install the newest 1.26.x (setup-go go-version '1.26'
// with check-latest), so they pick up later patch releases too. Stay on 1.26:
// Go 1.27 changes behaviour the vault fixture tests depend on.
toolchain go1.26.10

// The gocryptfs engine, caches and bindings live in ../core and are shared
// with the macOS and Android apps.
require github.com/adrianm/imagethat/core v0.0.0

replace github.com/adrianm/imagethat/core => ../core

require golang.org/x/image v0.47.0

require (
	github.com/aperturerobotics/jacobsa-crypto v1.1.0 // indirect
	github.com/hanwen/go-fuse/v2 v2.8.0 // indirect
	github.com/rfjakob/eme v1.1.2 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.29.0 // indirect
)
