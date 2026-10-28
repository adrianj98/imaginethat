#!/bin/sh
# kunj preset: gomobile — gomobile and gobind into /usr/local/bin. Needs the go preset.
# tools: { "gomobile": "v0.0.0-20261011034528-197770444867" }, the golang.org/x/mobile version
# in webserver/go.mod, so the generated glue matches the bind sources the module builds against.
# On arm64 they are built for amd64: gomobile only knows the NDK's linux-x86_64 toolchain (the only
# one Google ships), so it runs under the same x86_64 emulation as the NDK.
set -eu
. "$(dirname "$0")/_lib.sh"
kunj_check_words 'v[0-9][0-9A-Za-z.+-]*'
want="${KUNJ_TOOL_VERSION:-}"
[ -n "$want" ] || { echo "gomobile: give the golang.org/x/mobile version from go.mod" >&2; exit 1; }
[ -f /etc/profile.d/kunj-tools.sh ] && . /etc/profile.d/kunj-tools.sh
kunj_need go
tmp="$(mktemp -d)"
# GOBIN cannot be set for a cross-compiled install, so it lands in $GOPATH/bin[/linux_amd64].
GOPATH="$tmp" GOMODCACHE="$tmp/mod" GOARCH=amd64 GOFLAGS=-modcacherw \
  go install "golang.org/x/mobile/cmd/gomobile@$want" "golang.org/x/mobile/cmd/gobind@$want"
for b in gomobile gobind; do
  if [ -x "$tmp/bin/linux_amd64/$b" ]; then cp "$tmp/bin/linux_amd64/$b" $KUNJ_PREFIX/bin/; else cp "$tmp/bin/$b" $KUNJ_PREFIX/bin/; fi
done
rm -rf "$tmp"
kunj_done "$want"
