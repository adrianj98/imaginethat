#!/usr/bin/env bash
# Builds api_test against a libgocryptfs shared library and runs it against a
# writable copy of the fixture vault.
#
#   run.sh <path to libgocryptfs.so|.dylib> [build dir]
#
# The library's directory must also hold nothing else named libgocryptfs; the
# header is always CLibGocryptfs/libgocryptfs.h, the one the macOS app uses.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
LIB="${1:?path to libgocryptfs.so or .dylib}"
OUT="${2:-$REPO/build/ctest}"
FIXTURE="$REPO/core/vault/testdata/vault"
PASSWORD="test-password-123"

mkdir -p "$OUT"
LIBDIR="$(cd "$(dirname "$LIB")" && pwd)"

cc -std=c11 -Wall -Wextra -D_GNU_SOURCE -I"$REPO/CLibGocryptfs" \
    -o "$OUT/api_test" "$HERE/api_test.c" -L"$LIBDIR" -lgocryptfs

VAULT="$(mktemp -d "${TMPDIR:-/tmp}/imagethat-ctest.XXXXXX")"
trap 'rm -rf "$VAULT"' EXIT
cp -R "$FIXTURE"/. "$VAULT"/
# gocryptfs writes its bookkeeping files read-only; the copy must be writable.
chmod -R u+w "$VAULT"

case "$(uname -s)" in
    Darwin) export DYLD_LIBRARY_PATH="$LIBDIR${DYLD_LIBRARY_PATH:+:$DYLD_LIBRARY_PATH}" ;;
    *)      export LD_LIBRARY_PATH="$LIBDIR${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" ;;
esac

"$OUT/api_test" "$VAULT" "$PASSWORD" "$REPO/core/vault/testdata/plain/full"
