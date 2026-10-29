#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRCROOT="$(dirname "$SCRIPT_DIR")"
HELPER_DIR="${SRCROOT}/gocryptfs-lib"

# Skip if Go is not installed or gocryptfs-lib doesn't have go.mod yet.
# The shim depends on ../core, whose generated internal/gocryptfs copy must
# exist (make golib-sync).
if ! command -v go &> /dev/null; then
    echo "warning: Go not installed, skipping gocryptfs library build"
    exit 0
fi

if [ ! -f "${HELPER_DIR}/go.mod" ]; then
    echo "warning: gocryptfs-lib/go.mod not found, skipping library build"
    exit 0
fi

cd "$HELPER_DIR"

# Determine output directory
if [ -n "$BUILT_PRODUCTS_DIR" ]; then
    OUTPUT_DIR="$BUILT_PRODUCTS_DIR"
else
    OUTPUT_DIR="${SRCROOT}/build"
    mkdir -p "$OUTPUT_DIR"
fi

echo "Building gocryptfs shared library..."
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
    go build -tags without_openssl -buildmode=c-shared \
    -o "${OUTPUT_DIR}/libgocryptfs.dylib" .

# The hand-written header in CLibGocryptfs/ is the API; cgo's generated one is
# not needed by the Swift build.
rm -f "${OUTPUT_DIR}/libgocryptfs.h"

echo "gocryptfs library built at: ${OUTPUT_DIR}/libgocryptfs.dylib"
