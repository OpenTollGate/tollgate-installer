#!/usr/bin/env bash
# Regression test: the installer must not be stamped with a FEED pre-number.
#
# WHY (2026-09-28, real operator terminal)
#
#   Installing: FreedomTechFeed/packages v0.6.0-alpha4-pre20 module afc86b3
#               package 0.6.0_alpha4_pre20
#               installer v0.6.0-alpha2-pre19-rc1 (0209805)
#
#   The installer's version is stamped from its release --tag and printed right
#   beneath the feed release being installed, so a `pre19` in the installer's
#   own version reads as "this installs pre19" while the feed resolves to pre20.
#   The operator asked which one they had actually got. Labelling the print
#   (PR #62) makes it readable; refusing the tag removes the trap at source.
#
# This test drives scripts/release-binaries.sh with a PATH that has no `go`, so
# the script stops at its own preflight and NOTHING is built. That is enough to
# observe the guard's decision, and keeps the test hermetic: no network, no Go
# toolchain, no release.
#
# Controls, both directions (see the fleet review doctrine):
#   - FALSE-PASS control: `...-pre19-...` MUST be refused.
#   - FALSE-FAIL controls: `...-pre-rc1` (real historical tag, `pre` with no
#     digits), `v0.7.0`, and the documented override MUST NOT be refused.
#     Without these, a guard that refused everything would look green.
#
# usage: scripts/test-installer-tag-scheme.sh [tree]
# exit 0 = PASS, 2 = FAIL, 3 = environment
set -u

TREE="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
SCRIPT="${TREE}/scripts/release-binaries.sh"
REFUSE_MARKER="FEED pre-number"
OVERRIDE_MARKER="ALLOW_FEED_PRENUMBER_TAG=1"

fail() { echo "FAIL: $*"; exit 2; }
ok()   { echo "ok   - $*"; }

[ -f "$SCRIPT" ] || { echo "environment: no $SCRIPT"; exit 3; }

# A PATH with no `go`, so the script always stops at its preflight and never
# builds. The guard runs BEFORE that preflight, so its decision is fully
# observable from the output: refused => the tag-scheme message, otherwise the
# "go not found" preflight error. `go` lives in /usr/bin on this fleet, so a
# hardcoded "safe" PATH is not safe — build one that provably lacks it.
NOGO_BIN="$(mktemp -d)"
for _t in bash env grep dirname basename sed cat mkdir rm sh; do
  _s="$(command -v "$_t" 2>/dev/null)" && ln -sf "$_s" "$NOGO_BIN/$_t"
done
trap 'rm -rf "$NOGO_BIN"' EXIT
NOGO_PATH="$NOGO_BIN"
if PATH="$NOGO_PATH" command -v go >/dev/null 2>&1; then
  echo "environment: could not build a go-free PATH ($NOGO_PATH still sees go)"
  exit 3
fi

echo "=== tree: $TREE"

# run_tag <tag> [override] -> prints "<rc>\n<output>"
run_tag() {
  local tag="$1" override="${2:-}"
  local out rc
  if [ -n "$override" ]; then
    out="$(env PATH="$NOGO_PATH" ALLOW_FEED_PRENUMBER_TAG="$override" \
           bash "$SCRIPT" --tag "$tag" --out /tmp/tgi-tag-scheme-out 2>&1)"
  else
    out="$(env -u ALLOW_FEED_PRENUMBER_TAG PATH="$NOGO_PATH" \
           bash "$SCRIPT" --tag "$tag" --out /tmp/tgi-tag-scheme-out 2>&1)"
  fi
  rc=$?
  printf '%s\n%s' "$rc" "$out"
}

# ---- FALSE-PASS control: the exact tag that caused the confusion ----------
R="$(run_tag 'v0.6.0-alpha2-pre19-rc1')"
rc="${R%%$'\n'*}"; out="${R#*$'\n'}"
[ "$rc" != "0" ] || fail "a FEED pre-number tag was ACCEPTED (rc=0): this is the defect"
echo "$out" | grep -q "$REFUSE_MARKER" \
  || fail "refused, but not for the tag-scheme reason (output does not mention '$REFUSE_MARKER'): $out"
echo "$out" | grep -q 'v0.6.0-alpha2-pre19-rc1' \
  || fail "the refusal does not name the offending tag: $out"
ok "refuses 'v0.6.0-alpha2-pre19-rc1' and names it"

# ---- and it must refuse BEFORE doing any work ----------------------------
[ -d /tmp/tgi-tag-scheme-out ] && fail "the guard ran after work started (output dir was created)"
ok "refuses before the preflight (no build attempted)"

# ---- FALSE-FAIL control 1: `pre` with no digits is a real historical tag --
R="$(run_tag 'v0.6.0-alpha2-pre-rc1')"
rc="${R%%$'\n'*}"; out="${R#*$'\n'}"
echo "$out" | grep -q "$REFUSE_MARKER" && fail "wrongly refused a legitimate tag 'v0.6.0-alpha2-pre-rc1': $out"
echo "$out" | grep -q 'go not found' \
  || fail "expected to reach the go preflight for 'v0.6.0-alpha2-pre-rc1', got: $out"
ok "'v0.6.0-alpha2-pre-rc1' (pre, no digits) passes the guard"

# ---- FALSE-FAIL control 2: the documented scheme -------------------------
R="$(run_tag 'v0.7.0')"
out="${R#*$'\n'}"
echo "$out" | grep -q "$REFUSE_MARKER" && fail "wrongly refused 'v0.7.0' (the scheme README documents): $out"
echo "$out" | grep -q 'go not found' || fail "expected the go preflight for 'v0.7.0', got: $out"
ok "'v0.7.0' passes the guard"

R="$(run_tag 'v0.6.0-alpha2-rc2')"
out="${R#*$'\n'}"
echo "$out" | grep -q "$REFUSE_MARKER" && fail "wrongly refused 'v0.6.0-alpha2-rc2' (the own-scheme example): $out"
ok "'v0.6.0-alpha2-rc2' (own scheme) passes the guard"

# ---- the override must still work, and say it is an override -------------
R="$(run_tag 'v0.6.0-alpha2-pre19-rc1' '1')"
out="${R#*$'\n'}"
echo "$out" | grep -q "$REFUSE_MARKER" && fail "the documented override did not override: $out"
echo "$out" | grep -q "$OVERRIDE_MARKER" \
  || fail "the override proceeded but did not announce itself: $out"
ok "ALLOW_FEED_PRENUMBER_TAG=1 overrides, and announces it"

echo
echo "PASS: a feed pre-number cannot be stamped into the installer unnoticed"
exit 0
