#!/bin/sh
# kunj preset: android — Android SDK command-line tools plus the packages listed.
# tools: { "android": ["platforms@android-35", "build-tools@35.0.0", "ndk@26.1.10909125"] }
# Each word is an sdkmanager package with ';' written as '@' (';' is not allowed in a tool value).
# Needs the java preset. Google ships the NDK and aapt2 for x86_64 Linux only, so on an arm64
# host they run through the Docker VM's x86_64 emulation; this installs the amd64 libc they need.
set -eu
. "$(dirname "$0")/_lib.sh"
kunj_check_words '[a-z][a-z-]*@[A-Za-z0-9.-]*'
kunj_need curl
kunj_need_pkg unzip
[ -f /etc/profile.d/kunj-tools.sh ] && . /etc/profile.d/kunj-tools.sh   # JAVA_HOME from the java preset
kunj_need java

if [ "$(kunj_arch)" = aarch64 ]; then
  dpkg --add-architecture amd64
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    libc6:amd64 libstdc++6:amd64 zlib1g:amd64 >/dev/null
  rm -rf /var/lib/apt/lists/*
fi

sdk=/opt/android-sdk
tools_build=16111833
if [ ! -x "$sdk/cmdline-tools/latest/bin/sdkmanager" ]; then
  tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/tools.zip" "https://dl.google.com/android/repository/commandlinetools-linux-${tools_build}_latest.zip"
  unzip -q "$tmp/tools.zip" -d "$tmp"
  mkdir -p "$sdk/cmdline-tools"
  rm -rf "$sdk/cmdline-tools/latest"
  mv "$tmp/cmdline-tools" "$sdk/cmdline-tools/latest"
  rm -rf "$tmp"
fi

ndk=""
pkgs="platform-tools"
set -f
for w in ${KUNJ_TOOL_VERSION:-}; do
  p="$(echo "$w" | tr '@' ';')"
  pkgs="$pkgs $p"
  case "$p" in ndk\;*) ndk="$sdk/ndk/${p#ndk;}" ;; esac
done
yes | "$sdk/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$sdk" --licenses >/dev/null 2>&1 || true
"$sdk/cmdline-tools/latest/bin/sdkmanager" --sdk_root="$sdk" $pkgs >/dev/null
set +f

# The sandbox user owns the SDK so Gradle can add a missing component itself.
chown -R "$KUNJ_TOOL_USER" "$sdk"
ln -sf "$sdk/cmdline-tools/latest/bin/sdkmanager" $KUNJ_PREFIX/bin/sdkmanager
ln -sf "$sdk/platform-tools/adb" $KUNJ_PREFIX/bin/adb

# Gradle's cache outside the home volume, so the snapshot keeps the wrapper and dependencies.
mkdir -p /opt/gradle
chown "$KUNJ_TOOL_USER" /opt/gradle

kunj_env "ANDROID_HOME=$sdk"
kunj_env "ANDROID_SDK_ROOT=$sdk"
[ -n "$ndk" ] && kunj_env "ANDROID_NDK_HOME=$ndk"
kunj_env "GRADLE_USER_HOME=/opt/gradle"
kunj_done "cmdline-tools-$tools_build"
