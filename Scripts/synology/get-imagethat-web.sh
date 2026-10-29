#!/bin/sh
# Download the latest imagethat-web release for this machine (Synology or any
# Linux x86_64/aarch64) into the current folder. No login needed.
#
#   sh get-imagethat-web.sh            # latest release
#   sh get-imagethat-web.sh v0.1.0     # a specific release
#
# Result: ./imagethat-web (checksum-verified against the release's SHA256SUMS)
set -eu

REPO="adrianj98/imaginethat"

case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "unsupported CPU $(uname -m)"; exit 1 ;;
esac

# github.com/<repo>/releases/latest redirects to .../tag/vX.Y.Z
TAG="${1:-$(curl -fsSI "https://github.com/$REPO/releases/latest" | tr -d '\r' \
    | sed -n 's|^[Ll]ocation: .*/tag/||p')}"
[ -n "$TAG" ] || { echo "could not find the latest release"; exit 1; }

NAME="imagethat-web_${TAG}_linux_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$TAG"

echo "Downloading $NAME"
curl -fsSL -o "$NAME" "$BASE/$NAME"
curl -fsSL -o SHA256SUMS "$BASE/SHA256SUMS"

WANT=$(awk -v n="$NAME" '{f=$2; sub(/^\*?\.\//, "", f); if (f == n) {print $1; exit}}' SHA256SUMS)
GOT=$(sha256sum "$NAME" | cut -d' ' -f1)
[ -n "$WANT" ] && [ "$GOT" = "$WANT" ] || { echo "checksum mismatch — not installing"; exit 1; }

tar -xzf "$NAME"
mv -f "${NAME%.tar.gz}/imagethat-web" ./imagethat-web
chmod +x ./imagethat-web
rm -rf "$NAME" "${NAME%.tar.gz}" SHA256SUMS
echo "Done: $PWD/imagethat-web $TAG"
