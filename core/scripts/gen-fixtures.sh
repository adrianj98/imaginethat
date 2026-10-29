#!/bin/bash
#
# Regenerates the test vaults under vault/testdata/.
#
# The vaults are produced by the real gocryptfs binary so the read path is
# checked against genuine upstream ciphertext rather than against this
# package's own output. There is one vault per content cipher: they take
# different code paths, and a GCM-only fixture once hid a panic that only
# AES-SIV reached.
#
# Requires the gocryptfs binary and a mountable FUSE:
#
#   go install -tags without_openssl github.com/rfjakob/gocryptfs/v2@v2.6.1
#
set -euo pipefail

PASSWORD="test-password-123"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODULE_DIR="$(dirname "$SCRIPT_DIR")"
TESTDATA="${MODULE_DIR}/vault/testdata"

if ! command -v gocryptfs &> /dev/null; then
    echo "error: gocryptfs is not on PATH" >&2
    echo "  go install -tags without_openssl github.com/rfjakob/gocryptfs/v2@v2.6.1" >&2
    exit 1
fi
if [ ! -e /dev/fuse ]; then
    echo "error: /dev/fuse is missing; cannot mount a vault to populate it" >&2
    exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf '%s' "$PASSWORD" > "$WORK/pw"

unmount() {
    local mnt="$1"
    for cmd in fusermount fusermount3 umount; do
        if command -v "$cmd" &> /dev/null; then
            if [ "$cmd" = "umount" ]; then
                "$cmd" "$mnt" 2>/dev/null && return 0
            else
                "$cmd" -u "$mnt" 2>/dev/null && return 0
            fi
        fi
    done
    echo "error: could not unmount $mnt" >&2
    return 1
}

# generate <output-dir-name> <file-set> [extra gocryptfs -init flags...]
generate() {
    local name="$1"; shift
    local set="$1"; shift

    local cipher="$WORK/$name/cipher"
    local mnt="$WORK/$name/mnt"
    mkdir -p "$cipher" "$mnt"

    echo "==> $name (set=$set, flags=${*:-none})"
    gocryptfs -init "$@" -passfile "$WORK/pw" -q "$cipher"
    gocryptfs -passfile "$WORK/pw" -q "$cipher" "$mnt"

    # The generator lives in the test binary so the fixture content and the
    # test's expectations come from exactly the same code.
    (
        cd "$MODULE_DIR"
        VAULT_GEN_FIXTURE="$mnt" VAULT_GEN_SET="$set" \
            go test -tags without_openssl ./vault/ -run TestGenerateFixture -count=1
    )

    unmount "$mnt"

    rm -rf "${TESTDATA:?}/$name"
    mkdir -p "$TESTDATA"
    cp -R "$cipher" "$TESTDATA/$name"
    chmod -R u+w "$TESTDATA/$name"
    echo "    -> vault/testdata/$name ($(du -sh "$TESTDATA/$name" | cut -f1))"
}

generate vault          full
generate vault-aessiv   alt  -aessiv
generate vault-xchacha  alt  -xchacha

echo
echo "Done. Regenerated:"
du -sh "$TESTDATA"/vault*
