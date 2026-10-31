#!/bin/bash
# Build ImageThat from this checkout and install it as a Mac app.
#
#   Scripts/install-mac.sh            # installs to /Applications
#   Scripts/install-mac.sh --user     # installs to ~/Applications (no admin rights)
#   Scripts/install-mac.sh --to DIR   # installs to DIR
#   Scripts/install-mac.sh --open     # launch it afterwards
#
# Needs Xcode (or the Command Line Tools) and Go. The bundled Go library is
# built for Apple silicon only.
set -euo pipefail

cd "$(dirname "$0")/.."

DEST=/Applications
OPEN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --user) DEST="$HOME/Applications" ;;
        --to) DEST="${2:?--to needs a directory}"; shift ;;
        --open) OPEN=1 ;;
        -h|--help) sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
    shift
done

fail() { echo "error: $*" >&2; exit 1; }

[ "$(uname -s)" = Darwin ] || fail "this installs the macOS app; run it on a Mac"
[ "$(uname -m)" = arm64 ] || fail "the app's Go library is built for Apple silicon (arm64) only"
command -v swift >/dev/null || fail "swift not found: install Xcode or run 'xcode-select --install'"
command -v go >/dev/null || fail "go not found: install it from https://go.dev/dl or 'brew install go'"

echo "==> Building ImageThat.app"
make app

APP=build/ImageThat.app
[ -d "$APP" ] || fail "build did not produce $APP"

mkdir -p "$DEST"
TARGET="$DEST/ImageThat.app"

# Replacing the bundle under a running copy confuses it. Only the copy being
# replaced matters (a dev build in build/ can keep running), and it may hold
# unsaved edits, so ask rather than quit it.
if pgrep -fq "^$TARGET/Contents/MacOS/ImageThat"; then
    fail "ImageThat is running from $TARGET; quit it and run this again"
fi

SUDO=""
if [ ! -w "$DEST" ] || { [ -e "$TARGET" ] && [ ! -w "$TARGET" ]; }; then
    echo "==> $DEST needs admin rights (or rerun with --user)"
    SUDO=sudo
fi

echo "==> Installing to $TARGET"
$SUDO rm -rf "$TARGET"
$SUDO ditto "$APP" "$TARGET"

# Let Finder and the Dock pick up the new icon and version.
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$TARGET" >/dev/null 2>&1 || true
$SUDO touch "$TARGET"

echo "Installed ImageThat to $TARGET"
if [ "$OPEN" = 1 ]; then
    open "$TARGET"
fi
