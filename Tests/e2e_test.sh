#!/bin/bash
# End-to-end tests for ImageThat
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
APP_BUNDLE="$PROJECT_DIR/build/ImageThat.app"
TEST_DIR="/tmp/imagethat-e2e-test"
PASSED=0
FAILED=0

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass() { echo -e "  ${GREEN}PASS${NC}: $1"; PASSED=$((PASSED+1)); }
fail() { echo -e "  ${RED}FAIL${NC}: $1 — $2"; FAILED=$((FAILED+1)); }

kill_app() { pkill -9 ImageThat 2>/dev/null || true; sleep 1; }

# Prints the CGWindowID of ImageThat's main window (empty if none)
window_id() {
    osascript -l JavaScript -e 'ObjC.import("CoreGraphics");
        const l = ObjC.deepUnwrap(ObjC.castRefToObject($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly, 0)));
        const w = l.find(w => w.kCGWindowOwnerName === "ImageThat" && w.kCGWindowLayer === 0);
        w ? String(w.kCGWindowNumber) : ""' 2>/dev/null
}

has_window() {
    [ -n "$(window_id)" ]
}

wait_for_window() {
    for i in $(seq 1 ${1:-10}); do
        has_window && return 0
        sleep 1
    done
    return 1
}

# ============================================================
echo ""
echo "=== ImageThat E2E Tests ==="

# --- Setup ---
echo ""
echo "--- Setup ---"
rm -rf "$TEST_DIR"
mkdir -p "$TEST_DIR/photos" "$TEST_DIR/screenshots" "$TEST_DIR/empty"
python3 -c "
import struct, zlib
def png(w,h,r,g,b):
    sig=b'\x89PNG\r\n\x1a\n'
    ihdr=struct.pack('>IIBBBBB',w,h,8,2,0,0,0)
    ic=zlib.crc32(b'IHDR'+ihdr)
    raw=b''.join(b'\x00'+bytes([r,g,b])*w for _ in range(h))
    comp=zlib.compress(raw)
    dc=zlib.crc32(b'IDAT'+comp)
    ec=zlib.crc32(b'IEND')
    return sig+struct.pack('>I',13)+b'IHDR'+ihdr+struct.pack('>I',ic)+struct.pack('>I',len(comp))+b'IDAT'+comp+struct.pack('>I',dc)+struct.pack('>I',0)+b'IEND'+struct.pack('>I',ec)
for name,r,g,b in [('red',255,0,0),('green',0,255,0),('blue',0,0,255)]:
    open(f'$TEST_DIR/photos/{name}.png','wb').write(png(100,100,r,g,b))
open(f'$TEST_DIR/screenshots/desktop.png','wb').write(png(100,100,128,128,128))
"
echo "  Test data ready."
kill_app

# ============================================================
echo ""
echo "--- Test 1: App launches with window ---"
open "$APP_BUNDLE"
if wait_for_window 10; then
    pass "Window appears"
else
    fail "Window appears" "not found after 10s"
fi
kill_app

# ============================================================
echo ""
echo "--- Test 2: Open folder with images via --args ---"
open -n "$APP_BUNDLE" --args "$TEST_DIR/photos"
if wait_for_window 20; then
    pass "App launches with --args"
    # Wait for folder to load (the .task has 1s delay)
    sleep 4
    screencapture -x -o -l "$(window_id)" /tmp/e2e-test2.png 2>/dev/null || true
    if [ -f "/tmp/e2e-test2.png" ]; then
        pass "Screenshot captured"
    else
        fail "Screenshot" "file not created"
    fi
else
    fail "App launches with --args" "window not found after 20s"
fi
kill_app

# ============================================================
echo ""
echo "--- Test 3: Integration tests (logic layer) ---"
cd "$PROJECT_DIR"
if DYLD_LIBRARY_PATH="$PROJECT_DIR/build" \
    "$(swift build --show-bin-path)/IntegrationTest" 2>/dev/null; then
    pass "All integration tests pass"
else
    fail "Integration tests" "some tests failed"
fi

# ============================================================
echo ""
echo "=== Results: $PASSED passed, $FAILED failed ==="
echo ""

rm -rf "$TEST_DIR" /tmp/e2e-*.png
[ "$FAILED" -eq 0 ]
