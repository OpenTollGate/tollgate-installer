#!/usr/bin/env bash
# Regression test: the installer's OWN build version must never read as the
# feed release it is installing.
#
# WHY THIS EXISTS (2026-09-28, real operator terminal, feed pre20)
#
#   Installing: FreedomTechFeed/packages v0.6.0-alpha4-pre20 module afc86b3
#               package 0.6.0_alpha4_pre20
#               installer v0.6.0-alpha2-pre19-rc1 (0209805)
#
#   The operator read that and asked "is it using pre19 or pre20?" — a fair
#   question, because the third line carries a `pre19` that has nothing to do
#   with the release on the first line. The installer's version string embeds
#   the feed pre-number it was cut alongside; it is NOT stale and NOT wrong
#   (v0.6.0-alpha2-pre19-rc1 is a real tag on a real release), but printed bare
#   beneath the feed line the two namespaces are indistinguishable.
#
#   So the installer line is labelled, in BOTH printf branches (python3 and the
#   sed/grep fallback) and in the UI banner. This test pins all three, because
#   a fix in one branch only is the same bug for the operator who has no
#   python3.
#
# Method: hermetic. No network, no router, no docker. It extracts the launcher's
# real python3 printer out of install-and-test.sh and RUNS it against a fixture
# /api/config, then asserts the emitted line is unambiguous; the sed/grep branch
# and the UI banner are checked for the same label so the three cannot drift.
#
# usage: tests/shell/test-installer-version-not-conflated-with-feed.sh [tree]
# exit 0 = PASS, 2 = FAIL, 3 = environment
set -u

TREE="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
LAUNCHER="${TREE}/install-and-test.sh"
# Sources live under internal/app since the layout move (the wizard's Go package).
# Keep the old root path working so a pin still runs against a pre-move tree.
APP="${TREE}/internal/app"
app_file() { if [ -f "${APP}/$1" ]; then printf '%s\n' "${APP}/$1"; else printf '%s\n' "${TREE}/$1"; fi; }
UI="$(app_file index.html)"

LABEL="[installer build tag]"

fail() { echo "FAIL: $*"; exit 2; }
ok()   { echo "ok   - $*"; }

[ -f "$LAUNCHER" ] || { echo "environment: no $LAUNCHER"; exit 3; }

PY="$(command -v python3 || true)"
[ -n "$PY" ] || { echo "environment: python3 required to extract the printer"; exit 3; }

echo "=== tree: $TREE"

# ---- 1. extract the launcher's own python3 printer ------------------------
# It sits inside: printf '%s' "${cfg}" | "${PYTHON3}" -c '<snippet>' 2>/dev/null
#
# There is deliberately NO pluggable extractor here. An earlier draft preferred
# an external helper when one existed at a fixed path; that is a latent trap -
# a file appearing at that path silently replaces the extraction, and a failing
# helper aborts the test instead of falling back. The extraction stays inline so
# it cannot be swapped out from under the assertions.
SNIPPET="$("$PY" -c '
import sys
lines = open(sys.argv[1], encoding="utf-8", errors="replace").read().splitlines()
start = None
for i, l in enumerate(lines):
    if "-c " in l and "PYTHON3" in l and "printf" in l:
        start = i + 1
        break
if start is None:
    sys.exit(1)
end = None
for j in range(start, len(lines)):
    if lines[j].lstrip().startswith(chr(39)):
        end = j
        break
if end is None:
    sys.exit(1)
sys.stdout.write(chr(10).join(lines[start:end]))
' "$LAUNCHER")" || SNIPPET=""

[ -n "$SNIPPET" ] || fail "could not extract the python3 printer from $LAUNCHER (did the call shape change?)"
echo "$SNIPPET" | grep -q 'installer_version' || fail "extracted snippet does not mention installer_version — extraction is wrong"
ok "extracted the launcher's real printer ($(echo "$SNIPPET" | wc -l | tr -d ' ') lines)"

# ---- 2. a fixture that reproduces the exact confusion ---------------------
CFG='{"feed_repo":"FreedomTechFeed/packages","feed_release_tag":"v0.6.0-alpha4-pre20","feed_module_pin7":"afc86b3","feed_pkg_version":"0.6.0_alpha4_pre20","installer_version":"v0.6.0-alpha2-pre19-rc1","installer_commit":"0209805"}'

echo "--- launcher output for the fixture (the pre20/pre19 pair) ---"
OUT="$(printf '%s' "$CFG" | "$PY" -c "$SNIPPET" 2>&1)"
echo "$OUT"
echo "-------------------------------------------------------------"

# ---- 3. both facts must still be present (no information dropped) ---------
echo "$OUT" | grep -q 'v0.6.0-alpha4-pre20'    || fail "the feed release tag is gone from the output"
echo "$OUT" | grep -q 'v0.6.0-alpha2-pre19-rc1' || fail "the installer version is gone from the output"
ok "both the feed release and the installer version are still printed"

# ---- 4. the installer line must be LABELLED ------------------------------
# Bound to the installer line specifically, not to the output as a whole: a
# label anywhere else in the block would not disambiguate this line.
ILINE="$(echo "$OUT" | grep -F 'installer v0.6.0-alpha2-pre19-rc1' || true)"
[ -n "$ILINE" ] || fail "no installer line found in the output"
case "$ILINE" in
  *"$LABEL"*) ok "the installer line is labelled: $ILINE" ;;
  *) fail "the installer line is UNLABELLED and can be misread as the feed release: $ILINE" ;;
esac

# ---- 5. the no-python fallback branch must carry the SAME label ----------
# Both branches are reachable; a fix in one only leaves the other broken.
#
# Count-based checking is NOT sufficient here, and that is a real trap (found by
# the cross-family review of this test): `grep -c` counts the label ANYWHERE in
# the file, so a label moved into a comment - or duplicated inside the python3
# snippet - keeps the count up while the fallback printf emits an unlabelled
# line to the operator who has no python3. The label must be ON the fallback
# print line, and the two labelled sites must be CODE, not comments.
N_SITES="$(grep -c 'installer %s (%s)' "$LAUNCHER")"
[ "$N_SITES" -eq 2 ] || fail "expected exactly 2 'installer %s (%s)' print sites (python3 + sed/grep), found $N_SITES - a branch may have been dropped"
ok "both installer print sites are present ($N_SITES)"

# The fallback is the printf form; the python3 branch is the print(...) form.
FALLBACK="$(grep -n 'installer %s (%s)' "$LAUNCHER" | grep -v 'print(' || true)"
[ -n "$FALLBACK" ] || fail "could not locate the sed/grep fallback installer printf"
case "$FALLBACK" in
  *"$LABEL"*) ok "the no-python fallback prints the label ON the installer line" ;;
  *) fail "the sed/grep fallback prints an UNLABELLED installer line: $FALLBACK" ;;
esac

# A comment must not be able to satisfy the label requirement.
N_LABELLED_CODE="$(grep -v '^[[:space:]]*#' "$LAUNCHER" | grep -Fc "$LABEL")"
[ "$N_LABELLED_CODE" -ge 2 ] || fail "the label appears on only $N_LABELLED_CODE non-comment line(s); both print branches must carry it"
ok "the label is on code in both branches ($N_LABELLED_CODE non-comment lines)"

# ---- 6. the UI banner must not conflate them either ----------------------
[ -f "$UI" ] || fail "no index.html at $UI - the banner check would silently pass otherwise"
if grep -Fq 'installer build ${c.installer_version}' "$UI"; then
  ok "the UI banner says 'installer build'"
else
  fail "index.html still prints the installer version without saying it is the installer's own build"
fi

# ---- 7. the installer line must fit a default terminal -------------------
# The label exists to be READ. A 100-column line wraps on an 80-column terminal
# (a tmux pane, a piped log tail) and the wrapped fragment is the disambiguating
# part itself, which reintroduces the confusion the label was added to end.
# Found by the cross-family review of this change, which measured the line at 100.
IWIDTH="$(printf '%s' "$ILINE" | wc -c)"
[ "$IWIDTH" -le 80 ] || fail "the installer line is $IWIDTH columns; it must fit 80 or the label wraps out of sight: $ILINE"
ok "the installer line is $IWIDTH columns (<= 80)"

echo
echo "PASS: the installer's own version cannot be read as the feed release"
exit 0
