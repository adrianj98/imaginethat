#!/bin/sh
# Build imagethat-web on a Synology using only a GitHub SSH key (no token).
#
#   sh build-on-nas.sh            # latest vX.Y.Z tag
#   sh build-on-nas.sh v0.1.0     # a specific release
#
# Needs git (DSM "Git Server" package) and an SSH key GitHub accepts in
# ~/.ssh (default ~/.ssh/id_rsa; override with SSH_KEY=...). Go is downloaded
# from go.dev (public, no login) into ./go-toolchain on first run.
# Result: ./imagethat-web
set -eu

REPO="git@github.com:adrianj98/imaginethat.git"
GO_VERSION="1.26.10"
KEY="${SSH_KEY:-$HOME/.ssh/id_rsa}"
export GIT_SSH_COMMAND="ssh -i $KEY -o IdentitiesOnly=yes"
cd "$(dirname "$0")"

case "$(uname -m)" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; *) echo "unsupported CPU"; exit 1 ;; esac

TAG="${1:-$(git ls-remote --tags --refs "$REPO" 'v*' | sed 's|.*refs/tags/||' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -1 | sed 's/^/v/')}"
[ -n "$TAG" ] || { echo "no release tag found"; exit 1; }
echo "Building $TAG for linux/$ARCH"

if [ ! -x go-toolchain/bin/go ]; then
    echo "Downloading Go $GO_VERSION"
    mkdir -p go-toolchain
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" | tar -xz -C go-toolchain --strip-components=1
fi
export PATH="$PWD/go-toolchain/bin:$PATH" GOTOOLCHAIN=local GOPATH="$PWD/go-path" GOCACHE="$PWD/go-cache"

rm -rf src && git clone --quiet --depth 1 --branch "$TAG" "$REPO" src
(cd src/webserver && sh scripts/sync-gocryptfs.sh >/dev/null && \
    CGO_ENABLED=0 go build -tags without_openssl -trimpath -ldflags "-s -w -X main.version=$TAG" \
    -o ../../imagethat-web ./cmd/imagethat-web)
rm -rf src
echo "Done: $PWD/imagethat-web ($(./imagethat-web -version))"
