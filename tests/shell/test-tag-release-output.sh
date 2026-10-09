#!/usr/bin/env bash
set -u
TREE="${1:?tree required}"
TMP=$(mktemp -d "${HOME}/tag-release-output.XXXXXX"); trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"; mkdir -p "$BIN"
cat >"$BIN/gh" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  "auth status"*) exit 0;; "api user"*) echo bot;;
  "api repos/OpenTollGate/tollgate-installer --jq .permissions.push"*) echo true;;
  "repo view OpenTollGate/tollgate-installer"*) echo main;;
  *"git/ref/tags/v-output"*".object.sha"*) echo cccccccccccccccccccccccccccccccccccccccc;;
  *"git/ref/tags/v-output"*".object.type"*) echo commit;;
  *"release view"*) printf '%s\n' \
    'tollgate-installer-linux-amd64	10	https://raw.example/linux' \
    'tollgate-installer-linux-arm64	10	https://raw.example/arm' \
    'tollgate-installer-darwin-amd64	10	https://raw.example/mac' \
    'tollgate-installer-darwin-arm64	10	https://raw.example/mac-arm' \
    'tollgate-installer-windows-amd64.exe	10	https://raw.example/win' \
    'SHA256SUMS	10	https://raw.example/sums' \
    'REPRODUCE.txt	10	https://raw.example/repro';;
  *) exit 0;;
esac
EOF
chmod +x "$BIN/gh"
cat >"$BIN/go" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$BIN/go"
cat >"$BIN/git" <<'EOF'
#!/usr/bin/env bash
if [ "$1" = clone ]; then dest="${@: -1}"; mkdir -p "$dest/scripts"; printf '#!/bin/sh\nexit 0\n' > "$dest/scripts/release-binaries.sh"; chmod +x "$dest/scripts/release-binaries.sh"; exit 0; fi
if [ "$1" = -C ] && [ "$3" = rev-parse ]; then echo cccccccccccccccccccccccccccccccccccccccc; exit 0; fi
if [ "$1" = -C ] && [ "$3" = status ]; then exit 0; fi
exec /usr/bin/git "$@"
EOF
chmod +x "$BIN/git"
out="$TMP/out"
PATH="$BIN:$PATH" HOME="$TMP" bash "$TREE/scripts/tag-release.sh" --tag v-output --repo OpenTollGate/tollgate-installer >"$out" 2>&1 || { sed -n '1,160p' "$out"; echo 'FAIL: script failed'; exit 1; }
launcher=$(grep '^Launcher: ' "$out")
printf '%s\n' "$launcher" | grep -Eq 'raw\.githubusercontent\.com/OpenTollGate/tollgate-installer/[^ ]+/scripts/tag-release\.sh' || { echo 'FAIL: launcher URL omitted owner/repo/ref'; exit 1; }
header=$(grep -n '^OS | File | Download URL$' "$out" | cut -d: -f1)
evidence=$(grep -n '^asset: ' "$out" | tail -1 | cut -d: -f1)
test -n "$header" && test -n "$evidence" && test "$header" -gt "$evidence" || { echo 'FAIL: table header precedes asset evidence'; exit 1; }
echo 'ok - launcher raw URL carries owner/repo/ref'
echo 'ok - asset table header follows per-asset evidence'
echo 'PASS: tag release output contract'
