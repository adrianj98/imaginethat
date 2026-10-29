#!/usr/bin/env bash
# Deploy imagethat-web from a GitHub release to a Synology NAS over SSH.
#
#   Scripts/synology/deploy.sh user@nas            # latest release
#   Scripts/synology/deploy.sh user@nas v0.1.0     # a specific release
#
# Runs on your Mac: gh downloads the release (using your gh login, so the NAS
# needs no GitHub token), the checksum is verified here, then the archive,
# SHA256SUMS and imagethat-web.sh are streamed to the NAS over ssh and
# installed with sudo (you will be asked for the NAS password once).
#
# Environment:
#   SSH_KEY         private key to use (default: ssh's own defaults/config)
#   IMAGETHAT_BASE  install folder on the NAS (default /volume1/imagethat)

set -euo pipefail

REPO=adrianj98/imaginethat
HOST=${1:?usage: $0 user@nas [vX.Y.Z]}
TAG=${2:-}
BASE=${IMAGETHAT_BASE:-/volume1/imagethat}
HERE=$(cd "$(dirname "$0")" && pwd)

SSH=(ssh -o ConnectTimeout=10)
[ -n "${SSH_KEY:-}" ] && SSH+=(-i "$SSH_KEY")
say() { echo "==> $*"; }

say "checking $HOST"
case "$("${SSH[@]}" "$HOST" uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "unsupported NAS CPU; releases cover x86_64 and aarch64" >&2; exit 1 ;;
esac

[ -n "$TAG" ] || TAG=$(gh release view -R "$REPO" --json tagName -q .tagName)
NAME="imagethat-web_${TAG}_linux_${ARCH}.tar.gz"

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
say "downloading $NAME"
gh release download "$TAG" -R "$REPO" -D "$TMP" -p "$NAME" -p SHA256SUMS
(cd "$TMP" && shasum -a 256 -c SHA256SUMS --ignore-missing)

STAGE=/tmp/imagethat-deploy
say "copying to $HOST:$STAGE"
"${SSH[@]}" "$HOST" "rm -rf $STAGE && mkdir -p $STAGE"
for f in "$TMP/$NAME" "$TMP/SHA256SUMS" "$HERE/imagethat-web.sh"; do
    # Stream through ssh instead of scp: DSM's scp needs the SFTP service on.
    "${SSH[@]}" "$HOST" "cat > $STAGE/$(basename "$f")" < "$f"
done

say "installing on $HOST (sudo)"
"${SSH[@]}" -t "$HOST" "sudo sh -c '
    set -e
    mkdir -p $BASE
    install -m 755 $STAGE/imagethat-web.sh $BASE/imagethat-web.sh
    IMAGETHAT_BASE=$BASE sh $BASE/imagethat-web.sh install-file $STAGE/$NAME $STAGE/SHA256SUMS
    if IMAGETHAT_BASE=$BASE sh $BASE/imagethat-web.sh status | grep -q \"running:   pid\"; then
        IMAGETHAT_BASE=$BASE sh $BASE/imagethat-web.sh restart
    else
        echo \"==> not started; edit $BASE/imagethat-web.conf (VAULT_ROOT) then run: sudo sh $BASE/imagethat-web.sh start\"
    fi
    rm -rf $STAGE
'"
say "done: $TAG on $HOST"
