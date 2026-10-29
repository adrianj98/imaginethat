module github.com/adrianm/imagethat/gocryptfs-lib

go 1.26.0

toolchain go1.26.10

// The engine lives in ../core; this module is only the cgo shim the macOS app
// links as libgocryptfs.dylib.
require github.com/adrianm/imagethat/core v0.0.0

require (
	github.com/aperturerobotics/jacobsa-crypto v1.1.0 // indirect
	github.com/hanwen/go-fuse/v2 v2.8.0 // indirect
	github.com/rfjakob/eme v1.1.2 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/image v0.47.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/term v0.29.0 // indirect
)

replace github.com/adrianm/imagethat/core => ../core
