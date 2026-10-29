#!/bin/sh
# Build imagethat-web on a Synology using only a GitHub SSH key (no token).
#
#   sh build-on-nas.sh            # latest vX.Y.Z tag
#   sh build-on-nas.sh v0.1.0     # a specific release
#
# Needs git (DSM "Git Server" package) and an SSH key GitHub accepts in
# ~/.ssh (default ~/.ssh/id_rsa; override with SSH_KEY=...). Go (go.dev) and
# Node (nodejs.org) are downloaded — public, no login — into ./go-toolchain
# and ./node-toolchain on first run; Node builds the browser UI.
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

# Node builds the browser UI, which go:embed compiles into the binary (the
# release job does the same). Latest Node 22, as in CI, checksum-verified.
if [ ! -x node-toolchain/bin/npm ]; then
    case "$ARCH" in amd64) NARCH=x64 ;; arm64) NARCH=arm64 ;; esac
    NODE_DIST="https://nodejs.org/dist/latest-v22.x"
    LINE=$(curl -fsSL "$NODE_DIST/SHASUMS256.txt" | grep " node-v[0-9.]*-linux-$NARCH\.tar\.gz\$")
    FILE=${LINE##* }
    echo "Downloading $FILE"
    curl -fsSL -o "$FILE" "$NODE_DIST/$FILE"
    [ "$(sha256sum "$FILE" | cut -d' ' -f1)" = "${LINE%% *}" ] || { echo "Node checksum mismatch"; exit 1; }
    mkdir -p node-toolchain && tar -xzf "$FILE" -C node-toolchain --strip-components=1 && rm -f "$FILE"
fi
export PATH="$PWD/go-toolchain/bin:$PWD/node-toolchain/bin:$PATH" GOTOOLCHAIN=local GOPATH="$PWD/go-path" GOCACHE="$PWD/go-cache"
export npm_config_cache="$PWD/npm-cache" npm_config_update_notifier=false
export GOFLAGS=-modcacherw   # keep the module cache deletable with plain rm -rf

rm -rf src && git clone --quiet --depth 1 --branch "$TAG" "$REPO" src
echo "Building web UI"
(cd src/webserver/web && npm ci --no-audit --no-fund --loglevel=error && npm run build >/dev/null)
echo "Building server"
(cd src/webserver && sh scripts/sync-gocryptfs.sh >/dev/null && \
    CGO_ENABLED=0 go build -tags without_openssl -trimpath -ldflags "-s -w -X main.version=$TAG" \
    -o ../../imagethat-web ./cmd/imagethat-web)
rm -rf src
echo "Done: $PWD/imagethat-web ($(./imagethat-web -version))"
