#!/usr/bin/env bash
# Builds the Go vault engine (core/mobile) into android/app/libs/vaultcore.aar.
# Tool locations come from the environment; defaults match a standard macOS setup.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
export ANDROID_NDK_HOME="${ANDROID_NDK_HOME:-$ANDROID_HOME/ndk/26.1.10909125}"
export PATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")/bin:$HOME/go/bin:$PATH"

for tool in go gomobile gobind; do
    command -v "$tool" >/dev/null || {
        echo "vaultcore: '$tool' not found. Install: go install golang.org/x/mobile/cmd/{gomobile,gobind}@latest && gomobile init" >&2
        exit 1
    }
done
[ -d "$ANDROID_NDK_HOME" ] || { echo "vaultcore: NDK not found at $ANDROID_NDK_HOME (set ANDROID_NDK_HOME)" >&2; exit 1; }

mkdir -p "$HERE/../app/libs"
cd "$REPO/core"
make sync
gomobile bind -target=android/arm64,android/amd64 -androidapi 29 \
    -tags without_openssl -ldflags=-s \
    -o ../android/app/libs/vaultcore.aar ./mobile
