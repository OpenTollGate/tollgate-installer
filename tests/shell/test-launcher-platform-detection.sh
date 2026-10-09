#!/usr/bin/env bash
# Regression test for install-and-test.sh platform and release-asset selection.
# usage: bash tests/shell/test-launcher-platform-detection.sh [tree]
set -eu

TREE="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
SCRIPT="${TREE}/install-and-test.sh"
TMP="$(mktemp -d "${HOME}/tollgate-launcher-test.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

fail() { echo "FAIL: $*"; exit 2; }
ok() { echo "ok   - $*"; }

for tool in bash env grep mkdir rm tr; do
    ln -s "$(command -v "$tool")" "$TMP/$tool"
done
cat >"$TMP/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) printf '%s\n' "${FAKE_UNAME_S:?}" ;;
  -m) printf '%s\n' "${FAKE_UNAME_M:?}" ;;
  *) exit 1 ;;
esac
EOF
chmod +x "$TMP/uname"

check() {
    name="$1" sys="$2" machine="$3" want_platform="$4" want_asset="$5"
    out="$(FAKE_UNAME_S="$sys" FAKE_UNAME_M="$machine" PATH="$TMP" \
        TOLLGATE_TEST_PLATFORM_ONLY=1 bash "$SCRIPT" 2>&1)" || fail "$name exited nonzero: $out"
    printf '%s\n' "$out" | grep -Fqx "Detected platform: $want_platform" \
        || fail "$name platform mismatch: $out"
    printf '%s\n' "$out" | grep -Fqx "Asset URL: https://github.com/felixfelix-bot/tollgate-installer/releases/latest/download/$want_asset" \
        || fail "$name asset URL mismatch: $out"
    ok "$name => $want_platform and $want_asset"
}

check "Linux x86_64" Linux x86_64 linux-amd64 tollgate-installer-linux-amd64
check "Linux aarch64" Linux aarch64 linux-arm64 tollgate-installer-linux-arm64
check "Darwin arm64" Darwin arm64 darwin-arm64 tollgate-installer-darwin-arm64
check "MINGW64" MINGW64_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe
check "MSYS" MSYS_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe
check "CYGWIN" CYGWIN_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe
echo "PASS: launcher platform detection"