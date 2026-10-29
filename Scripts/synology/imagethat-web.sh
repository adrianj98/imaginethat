#!/bin/sh
# Install, update and run imagethat-web on a Synology NAS (DSM 7).
#
#   sudo sh imagethat-web.sh install   # download the latest release and verify it
#   sudo sh imagethat-web.sh start     # start in the background
#   sudo sh imagethat-web.sh stop
#   sudo sh imagethat-web.sh restart
#   sudo sh imagethat-web.sh status
#   sudo sh imagethat-web.sh update    # install the latest release if newer, then restart
#   sudo sh imagethat-web.sh install-file <tarball> <SHA256SUMS>
#
# Easiest: run Scripts/synology/deploy.sh on your Mac — it downloads the release
# with gh and pushes it over SSH, so the NAS needs no GitHub token.
#
# For `install`/`update` on the NAS itself the repository is private, so downloads go through the GitHub API with a
# token (fine-grained, "Contents: read" on adrianj98/imaginethat). Put it in
# $BASE/github-token (chmod 600) or export GITHUB_TOKEN.
#
# To start on boot: DSM > Control Panel > Task Scheduler > Create >
# Triggered Task > Boot-up, user root, command:
#   sh /volume1/imagethat/imagethat-web.sh start
# Settings live in $BASE/imagethat-web.conf (created on first install).
#
# Only plain POSIX sh, curl, tar, sha256sum, awk and sed are used: DSM has no
# gh or jq.

set -eu

REPO="adrianj98/imaginethat"
BASE="${IMAGETHAT_BASE:-/volume1/imagethat}"
CONF="$BASE/imagethat-web.conf"
BIN="$BASE/bin/imagethat-web"
PIDFILE="$BASE/imagethat-web.pid"
LOG="$BASE/imagethat-web.log"
API="https://api.github.com/repos/$REPO"

die() { echo "error: $*" >&2; exit 1; }
say() { echo "==> $*"; }

write_default_conf() {
    cat > "$CONF" <<'EOF'
# imagethat-web settings (sourced by imagethat-web.sh)

# Folder scanned for gocryptfs vaults.
VAULT_ROOT=/volume1/vaults

# Listen on every interface. A non-loopback address requires TLS.
LISTEN=0.0.0.0:8347

# TLS certificate and key. Empty = use the DSM default certificate
# (Control Panel > Security > Certificate), copied here on each start so the
# server does not need to read /usr/syno directly.
TLS_CERT=
TLS_KEY=

# Lock vaults after this much inactivity.
IDLE_TIMEOUT=30m

# Extra flags passed to imagethat-web, e.g. "-scan-depth 4".
EXTRA_FLAGS=
EOF
    chmod 600 "$CONF"
}

load_conf() {
    [ -f "$CONF" ] || { mkdir -p "$BASE"; write_default_conf; say "created $CONF — review it"; }
    # shellcheck disable=SC1090
    . "$CONF"
}

token() {
    if [ -n "${GITHUB_TOKEN:-}" ]; then echo "$GITHUB_TOKEN"; return; fi
    [ -f "$BASE/github-token" ] || die "no token: export GITHUB_TOKEN or write it to $BASE/github-token"
    tr -d ' \r\n' < "$BASE/github-token"
}

arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo amd64 ;;
        aarch64|arm64) echo arm64 ;;
        *) die "unsupported CPU $(uname -m): releases cover x86_64 and aarch64 only" ;;
    esac
}

api() { curl -fsSL -H "Authorization: Bearer $(token)" -H "Accept: application/vnd.github+json" "$@"; }

# Print the API URL of the asset called $2 in release JSON $1. GitHub lists an
# asset's "url" before its "name", one field per line.
asset_url() {
    awk -v want="$2" '
        /"url": *"https:\/\/api\.github\.com\/repos\/.*\/releases\/assets\/[0-9]+"/ {
            u = $0; sub(/.*"url": *"/, "", u); sub(/".*/, "", u)
        }
        /"name": *"/ {
            n = $0; sub(/.*"name": *"/, "", n); sub(/".*/, "", n)
            if (n == want && u != "") { print u; exit }
        }' "$1"
}

download_asset() { # url dest
    curl -fsSL -H "Authorization: Bearer $(token)" -H "Accept: application/octet-stream" -o "$2" "$1"
}

installed_version() { [ -x "$BIN" ] && "$BIN" -version 2>/dev/null | awk '{print $NF}' || echo none; }

cmd_install() {
    load_conf
    a=$(arch)
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    say "looking up the latest release"
    api "$API/releases/latest" > "$tmp/release.json" || die "GitHub API request failed (token valid? repo access?)"
    tag=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/release.json" | head -1)
    [ -n "$tag" ] || die "could not read the release tag"
    name="imagethat-web_${tag}_linux_${a}.tar.gz"

    if [ "$(installed_version)" = "$tag" ] && [ "${FORCE:-0}" != 1 ]; then
        say "$tag already installed"; return 0
    fi

    url=$(asset_url "$tmp/release.json" "$name"); [ -n "$url" ] || die "release $tag has no $name"
    sums=$(asset_url "$tmp/release.json" SHA256SUMS); [ -n "$sums" ] || die "release $tag has no SHA256SUMS"

    say "downloading $name"
    download_asset "$url" "$tmp/$name"
    download_asset "$sums" "$tmp/SHA256SUMS"
    install_tarball "$tmp/$name" "$tmp/SHA256SUMS"
}

# Verify $1 against SHA256SUMS file $2 and install the binary inside it.
# Used by `install` and by `install-file` (archives copied over by deploy.sh).
install_tarball() {
    tarball=$1 sumsfile=$2
    name=$(basename "$tarball")
    dir=$(dirname "$tarball")
    case "$name" in imagethat-web_v*_linux_*.tar.gz) ;; *) die "unexpected archive name $name" ;; esac
    [ "${name##*_linux_}" = "$(arch).tar.gz" ] || die "$name is not for this CPU ($(arch))"

    say "verifying checksum"
    # SHA256SUMS lines look like "<hash>  ./<name>". Compare explicitly rather
    # than via `sha256sum -c`: some builds accept empty input as success.
    want=$(awk -v n="$name" '{f=$2; sub(/^\*?\.\//, "", f); if (f == n) {print $1; exit}}' "$sumsfile")
    [ -n "$want" ] || die "$name is not listed in SHA256SUMS — not installing"
    got=$(sha256sum "$tarball" | awk '{print $1}')
    [ "$got" = "$want" ] || die "checksum mismatch for $name — not installing"

    x=$(mktemp -d)
    tar -xzf "$tarball" -C "$x"
    top=${name%.tar.gz}
    mkdir -p "$BASE/bin"
    install -m 755 "$x/$top/imagethat-web" "$BIN.new"
    mv -f "$BIN.new" "$BIN"
    cp -f "$x/$top/README.md" "$BASE/README.md" 2>/dev/null || true
    rm -rf "$x"
    say "installed $("$BIN" -version)"
}

cmd_install_file() {
    load_conf
    [ $# -eq 2 ] || die "usage: $0 install-file <imagethat-web_vX.Y.Z_linux_ARCH.tar.gz> <SHA256SUMS>"
    install_tarball "$1" "$2"
}

pid() { [ -f "$PIDFILE" ] && cat "$PIDFILE" || true; }
running() { p=$(pid); [ -n "$p" ] && kill -0 "$p" 2>/dev/null; }

# Copy the DSM default certificate into $BASE so it is readable and survives
# DSM renewing it (re-copied on every start).
tls_files() {
    if [ -n "${TLS_CERT:-}" ] && [ -n "${TLS_KEY:-}" ]; then
        echo "$TLS_CERT $TLS_KEY"; return
    fi
    d=/usr/syno/etc/certificate/system/default
    [ -f "$d/fullchain.pem" ] && [ -f "$d/privkey.pem" ] \
        || die "no TLS_CERT/TLS_KEY set and no DSM default certificate at $d"
    mkdir -p "$BASE/tls"
    cp -f "$d/fullchain.pem" "$BASE/tls/cert.pem"
    cp -f "$d/privkey.pem" "$BASE/tls/key.pem"
    chmod 600 "$BASE/tls/key.pem"
    echo "$BASE/tls/cert.pem $BASE/tls/key.pem"
}

cmd_start() {
    load_conf
    [ -x "$BIN" ] || die "not installed; run: $0 install"
    if running; then say "already running (pid $(pid))"; return 0; fi
    [ -d "$VAULT_ROOT" ] || die "VAULT_ROOT $VAULT_ROOT does not exist (edit $CONF)"
    set -- $(tls_files)
    # shellcheck disable=SC2086
    nohup "$BIN" -root "$VAULT_ROOT" -addr "$LISTEN" -tls-cert "$1" -tls-key "$2" \
        -idle-timeout "$IDLE_TIMEOUT" $EXTRA_FLAGS >> "$LOG" 2>&1 &
    echo $! > "$PIDFILE"
    sleep 1
    running || { tail -20 "$LOG" >&2; die "failed to start (log: $LOG)"; }
    say "started pid $(pid) on https://$LISTEN — log: $LOG"
    say "certificate SHA-256 (compare in the app):"
    openssl x509 -in "$1" -noout -fingerprint -sha256 2>/dev/null | sed 's/.*=/    /' || true
}

cmd_stop() {
    if running; then
        kill "$(pid)"; i=0
        while running && [ $i -lt 10 ]; do sleep 1; i=$((i+1)); done
        running && kill -9 "$(pid)"
        say "stopped"
    else
        say "not running"
    fi
    rm -f "$PIDFILE"
}

cmd_status() {
    echo "installed: $(installed_version)"
    if running; then echo "running:   pid $(pid)"; else echo "running:   no"; fi
}

case "${1:-}" in
    install) cmd_install ;;
    install-file) shift; cmd_install_file "$@" ;;
    arch)    arch ;;
    start)   cmd_start ;;
    stop)    cmd_stop ;;
    restart) cmd_stop; cmd_start ;;
    status)  cmd_status ;;
    update)  before=$(installed_version); cmd_install
             if [ "$(installed_version)" != "$before" ] && running; then cmd_stop; cmd_start; fi ;;
    *) sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
