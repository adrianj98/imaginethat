#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SRCROOT="$(dirname "$SCRIPT_DIR")"
HELPER_DIR="${SRCROOT}/gocryptfs-lib"

# Skip if Go is not installed or gocryptfs-lib doesn't have go.mod yet
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

# Copy header to source tree for bridging header
cp "${OUTPUT_DIR}/libgocryptfs.h" "${SRCROOT}/ImageThat/" 2>/dev/null || true

echo "gocryptfs library built at: ${OUTPUT_DIR}/libgocryptfs.dylib"
