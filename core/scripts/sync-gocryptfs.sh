#!/bin/bash
#
# Materializes core/internal/gocryptfs/ from the gocryptfs module cache.
#
# gocryptfs keeps its crypto in internal/ packages, which Go refuses to let
# another module import. The only way to build against them is to copy the tree
# into this module and rewrite the import paths — the same thing gocryptfs-lib/
# does (its internal/ dir is gitignored and generated the same way).
#
set -euo pipefail

GOCRYPTFS_VERSION="v2.6.1"
SRC_MODULE="github.com/rfjakob/gocryptfs/v2"
DST_IMPORT="github.com/adrianm/imagethat/core/internal/gocryptfs"

# The transitive closure of what vault/ actually imports, as reported by
# `go list -deps`. Copying only these keeps go-fuse, pflag and go-gitignore —
# needed by fusefrontend/ctlsocksrv, which we do not use — out of the module.
PACKAGES=(
    configfile
    contentenc
    cryptocore
    exitcodes
    nametransform
    siv_aead
    stupidgcm
    syscallcompat
    tlog
)

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODULE_DIR="$(dirname "$SCRIPT_DIR")"
DST_DIR="${MODULE_DIR}/internal/gocryptfs"

if ! command -v go &> /dev/null; then
    echo "error: Go is not installed" >&2
    exit 1
fi

cd "$MODULE_DIR"

# Ensure the module is in the local cache before we go looking for it.
echo "Downloading ${SRC_MODULE}@${GOCRYPTFS_VERSION}..."
go mod download "${SRC_MODULE}@${GOCRYPTFS_VERSION}"

GOMODCACHE="$(go env GOMODCACHE)"
SRC_DIR="${GOMODCACHE}/github.com/rfjakob/gocryptfs/v2@${GOCRYPTFS_VERSION}/internal"

if [ ! -d "$SRC_DIR" ]; then
    echo "error: gocryptfs internal/ tree not found at ${SRC_DIR}" >&2
    exit 1
fi

echo "Copying ${#PACKAGES[@]} packages -> ${DST_DIR}"
rm -rf "$DST_DIR"
mkdir -p "$DST_DIR"
for pkg in "${PACKAGES[@]}"; do
    if [ ! -d "${SRC_DIR}/${pkg}" ]; then
        echo "error: expected package ${pkg} not found in ${SRC_DIR}" >&2
        exit 1
    fi
    cp -R "${SRC_DIR}/${pkg}" "$DST_DIR/"
done

# The module cache is read-only; the copy must not be.
chmod -R u+w "$DST_DIR"

# Point the copied packages at each other rather than at the upstream module.
echo "Rewriting import paths -> ${DST_IMPORT}"
find "$DST_DIR" -name '*.go' -type f -print0 \
    | xargs -0 sed -i.bak "s|${SRC_MODULE}/internal|${DST_IMPORT}|g"
find "$DST_DIR" -name '*.go.bak' -type f -delete

# _test.go files reference test-only helpers and fixtures we did not copy, and
# nothing here imports them.
find "$DST_DIR" -name '*_test.go' -type f -delete

cat > "${DST_DIR}/README.md" <<EOF
# Generated — do not edit

Copies of these \`${SRC_MODULE}@${GOCRYPTFS_VERSION}\` \`internal/\` packages,
with import paths rewritten to \`${DST_IMPORT}\`:

$(printf -- '- %s\n' "${PACKAGES[@]}")

Upstream \`internal/\` packages cannot be imported across module boundaries, so
copying is the only way to build against gocryptfs' crypto. The list above is
the transitive closure of what \`vault/\` imports; regenerate it with
\`go list -deps ./vault/\` if the imports change.

Regenerate with \`make sync\` (or \`scripts/sync-gocryptfs.sh\`). This directory
is gitignored.
EOF

echo "Done. ${#PACKAGES[@]} packages copied."
