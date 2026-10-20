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

for tool in bash env grep mkdir rm tr awk; do
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
    printf '%s\n' "$out" | grep -Fqx "Asset URL: https://github.com/OpenTollGate/tollgate-installer/releases/latest/download/$want_asset" \
        || fail "$name asset URL mismatch: $out"
    ok "$name => $want_platform and $want_asset"
}

check "Linux x86_64" Linux x86_64 linux-amd64 tollgate-installer-linux-amd64
check "Linux aarch64" Linux aarch64 linux-arm64 tollgate-installer-linux-arm64
check "Darwin arm64" Darwin arm64 darwin-arm64 tollgate-installer-darwin-arm64
check "MINGW64" MINGW64_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe
check "MSYS" MSYS_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe
check "CYGWIN" CYGWIN_NT-10.0-19045 x86_64 windows-amd64 tollgate-installer-windows-amd64.exe

# Installer-tag resolution: the launcher must serve the CURRENT build, not
# GitHub's `releases/latest` (which pinned the newest non-pre-release and served
# a stale installer — rc19 while rc23 was current). A fixture makes it hermetic.
tag_check() {
    name="$1" fixture="$2" want_tag="$3" want_asset="$4"
    out="$(FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64 PATH="$TMP" \
        TOLLGATE_TEST_PLATFORM_ONLY=1 TOLLGATE_TEST_RELEASES_JSON="$fixture" \
        bash "$SCRIPT" 2>&1)" || fail "$name exited nonzero: $out"
    printf '%s\n' "$out" | grep -Fqx "Asset URL: https://github.com/OpenTollGate/tollgate-installer/releases/download/$want_tag/$want_asset" \
        || fail "$name URL mismatch: $out"
    ok "$name => $want_tag/"
}

# alpha = newest pre-release, so a freshly cut rc beats an older rc.
tag_check "installer alpha picks the newest pre-release" '[
  {
    "tag_name": "v0.6.0-alpha2-rc19",
    "created_at": "2026-10-11T14:33:01Z",
    "published_at": "2026-10-11T14:33:01Z"
  },
  {
    "tag_name": "v0.6.0-alpha2-rc23",
    "created_at": "2026-10-13T10:10:53Z",
    "published_at": "2026-10-13T10:11:00Z"
  }
]' v0.6.0-alpha2-rc23 tollgate-installer-linux-amd64

# An explicit --installer-tag (through the env) wins outright.
out="$(FAKE_UNAME_S=Linux FAKE_UNAME_M=x86_64 PATH="$TMP" \
    TOLLGATE_TEST_PLATFORM_ONLY=1 TOLLGATE_INSTALLER_TAG=v9.9.9 \
    bash "$SCRIPT" 2>&1)" || fail "explicit installer tag exited nonzero: $out"
printf '%s\n' "$out" | grep -Fqx "Asset URL: https://github.com/OpenTollGate/tollgate-installer/releases/download/v9.9.9/tollgate-installer-linux-amd64" \
    || fail "explicit installer tag URL mismatch: $out"
ok "explicit --installer-tag wins"

echo "PASS: launcher platform detection"

# busybox tr (OpenWrt, Alpine busybox variants) does NOT support character classes:
# `tr '[:upper:]' '[:lower:]'` maps the literal char SETS pairwise (u->l, p->o, e->w…),
# mangling "Linux" into "Linlx" — observed on a stock OpenWrt 24.10.2 GL-MT6000, where
# the launcher then 404s on tollgate-installer-Linlx-arm64 from every release source.
# Emulate that behaviour and assert the platform string survives it.
BUSYBOX_TR="$TMP/busybox-tr"
cat >"$BUSYBOX_TR" <<'EOF'
#!/bin/sh
# busybox shape: '[:upper:]'/'[:lower:]' treated as literal sets, mapped pairwise
if [ "$1" = "[:upper:]" ] && [ "$2" = "[:lower:]" ]; then
    sed -e 's/u/l/g' -e 's/p/o/g' -e 's/e/w/g' -e 's/r/r/g' -e 's/U/L/g'
else
    exec /usr/bin/tr "$@"
fi
EOF
chmod +x "$BUSYBOX_TR"

out="$(FAKE_UNAME_S=Linux FAKE_UNAME_M=aarch64 PATH="$TMP" TR_SHIM="$BUSYBOX_TR" \
    TOLLGATE_TEST_PLATFORM_ONLY=1 bash -c 'tr() { "$TR_SHIM" "$@"; }; export -f tr 2>/dev/null; source_code=1; bash "$0"' "$SCRIPT" 2>&1)" || true
# simpler and shell-agnostic: run with a PATH whose tr IS the busybox shim
BBIN="$(mktemp -d "$TMP/bb.XXXXXX")"
for tool in bash env grep mkdir rm awk sed; do ln -s "$(command -v "$tool")" "$BBIN/$tool"; done
cp "$TMP/uname" "$BBIN/uname"
ln -s "$BUSYBOX_TR" "$BBIN/tr"
out="$(FAKE_UNAME_S=Linux FAKE_UNAME_M=aarch64 PATH="$BBIN" \
    TOLLGATE_TEST_PLATFORM_ONLY=1 bash "$SCRIPT" 2>&1)" || fail "busybox-tr run exited nonzero: $out"
printf '%s\n' "$out" | grep -Fqx "Detected platform: linux-arm64" \
    || fail "busybox tr platform mismatch (asset would 404): $out"
ok "busybox tr (Linux aarch64) => linux-arm64"
