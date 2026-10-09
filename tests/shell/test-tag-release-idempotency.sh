#!/usr/bin/env bash
set -u
TREE="${1:?tree required}"
TMP=$(mktemp -d "${HOME}/tag-release-idem.XXXXXX"); trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"; mkdir -p "$BIN"
A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
cat >"$BIN/gh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${GH_LOG:?}"
case "$*" in
  "auth status"*) exit 0;; "api user"*) echo bot;;
  "api repos/OpenTollGate/tollgate-installer --jq .permissions.push"*) echo true;;
  "repo view OpenTollGate/tollgate-installer"*) echo main;;
  "api repos/OpenTollGate/tollgate-installer/git/ref/tags/v-test --jq .object.sha") echo "$TAG_OBJECT";;
  "api repos/OpenTollGate/tollgate-installer/git/ref/tags/v-test --jq .object.type") echo commit;;
  "release view"*) printf '%s\t%s\t%s\n' \
    tollgate-installer-linux-amd64 1048576 https://example.invalid/linux-amd64 \
    tollgate-installer-linux-arm64 1048576 https://example.invalid/linux-arm64 \
    tollgate-installer-darwin-amd64 1048576 https://example.invalid/darwin-amd64 \
    tollgate-installer-darwin-arm64 1048576 https://example.invalid/darwin-arm64 \
    tollgate-installer-windows-amd64.exe 1048576 https://example.invalid/windows-amd64.exe \
    SHA256SUMS 512 https://example.invalid/SHA256SUMS \
    REPRODUCE.txt 576 https://example.invalid/REPRODUCE.txt;;
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
if [ "$1" = clone ]; then dest="${@: -1}"; mkdir -p "$dest/scripts"; cp "$FIXTURE_SCRIPT" "$dest/scripts/release-binaries.sh"; git -C "$dest" init -q; git -C "$dest" config user.email x; git -C "$dest" config user.name x; git -C "$dest" add .; git -C "$dest" commit -qm fixture; exit 0; fi
if [ "$1" = -C ] && [ "$3" = rev-parse ] && [ "$4" = HEAD ]; then echo "$TREE_COMMIT"; exit 0; fi
exec /usr/bin/git "$@"
EOF
chmod +x "$BIN/git"
# Existing script must fail to protect the release before release-binaries runs.
printf "#!/bin/sh\nprintf publish >> \"\$PUBLISH_LOG\"\n" > "$TMP/release-binaries.sh"; chmod +x "$TMP/release-binaries.sh"
GH_LOG="$TMP/gh.log" PUBLISH_LOG="$TMP/publish.log" FIXTURE_SCRIPT="$TMP/release-binaries.sh" TAG_OBJECT="$A" TREE_COMMIT="$B" PATH="$BIN:$PATH" HOME="$TMP" \
  bash "$TREE/scripts/tag-release.sh" --tag v-test --repo OpenTollGate/tollgate-installer >"$TMP/out" 2>&1
rc=$?
printf '%s\n' "--- script output ---"; sed -n '1,120p' "$TMP/out"
grep -q "$A" "$TMP/out" || { echo "FAIL: mismatch output omitted tag commit $A"; exit 1; }
grep -q "$B" "$TMP/out" || { echo "FAIL: mismatch output omitted built commit $B"; exit 1; }
grep -q 'pass --ref' "$TMP/out" || { echo 'FAIL: mismatch output omitted --ref remedy'; exit 1; }
test "$rc" -ne 0 || { echo 'FAIL: mismatch unexpectedly succeeded'; exit 1; }
! grep -Eq 'release (upload|create)' "$TMP/gh.log" || { echo 'FAIL: release mutation invoked'; exit 1; }
! test -s "$TMP/publish.log" || { echo 'FAIL: build/publish invoked'; exit 1; }
echo 'ok - mismatched remote tag aborts before build or publish'

: > "$TMP/publish.log"
GH_LOG="$TMP/gh-match.log" PUBLISH_LOG="$TMP/publish.log" FIXTURE_SCRIPT="$TMP/release-binaries.sh" TAG_OBJECT="$B" TREE_COMMIT="$B" PATH="$BIN:$PATH" HOME="$TMP" \
  bash "$TREE/scripts/tag-release.sh" --tag v-test --repo OpenTollGate/tollgate-installer >"$TMP/match.out" 2>&1
test "$?" -eq 0 || { echo 'FAIL: matching tag did not complete idempotent path'; exit 1; }
test -s "$TMP/publish.log" || { echo 'FAIL: matching tag did not proceed to build/publish path'; exit 1; }
grep -q 'Tag exists: tag v-test exists at' "$TMP/match.out" || { echo 'FAIL: matching tag did not announce tag source'; exit 1; }
echo 'ok - matching remote tag proceeds down idempotent re-run path'

echo 'PASS: tag release idempotency'
