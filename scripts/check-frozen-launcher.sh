#!/usr/bin/env bash
# check-frozen-launcher.sh — refuse edits to the frozen v0.6.0 curl|bash script.
#
# The CI workflow does this on PRs, but CI can be skipped, is not available in
# every clone, and does not help someone editing the file locally. This runs the
# same check from a pre-commit hook or by hand.
#
#   scripts/check-frozen-launcher.sh [--staged | --against <ref>]
#
# Exit 0 = frozen file untouched (or explicitly overridden). Exit 1 = it changed.
#
# Override for a deliberate change:
#   TOLLGATE_UNFREEZE_LAUNCHER=1 scripts/check-frozen-launcher.sh
set -uo pipefail

FROZEN="install-and-test.sh"
TAG="${FROZEN_TAG:-v0.6.0}"
MODE="staged"
REF=""

while [ $# -gt 0 ]; do
    case "$1" in
        --staged) MODE="staged"; shift ;;
        --against) MODE="against"; REF="${2:-}"; shift 2 ;;
        -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
        *) echo "check-frozen-launcher: unknown arg '$1'" >&2; exit 2 ;;
    esac
done

if [ "${TOLLGATE_UNFREEZE_LAUNCHER:-}" = "1" ]; then
    echo "check-frozen-launcher: OVERRIDE active (TOLLGATE_UNFREEZE_LAUNCHER=1) — not checking."
    exit 0
fi

if [ ! -f "$FROZEN" ]; then
    echo "check-frozen-launcher: $FROZEN not present in this tree — nothing to check."
    exit 0
fi

fail=0

case "$MODE" in
    staged)
        # Is the frozen file staged for commit?
        if git diff --cached --name-only | grep -qx "$FROZEN"; then
            echo "check-frozen-launcher: $FROZEN is STAGED for commit." >&2
            fail=1
        fi
        ;;
    against)
        [ -n "$REF" ] || { echo "check-frozen-launcher: --against needs a ref" >&2; exit 2; }
        if ! git diff --quiet "$REF" -- "$FROZEN" 2>/dev/null; then
            echo "check-frozen-launcher: $FROZEN differs from $REF." >&2
            fail=1
        fi
        ;;
esac

if [ "$fail" -eq 0 ]; then
    echo "check-frozen-launcher: OK — $FROZEN untouched."
    exit 0
fi

cat >&2 <<EOF

  $FROZEN is FROZEN at $TAG.

  It is served live from main:
    https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/$FROZEN
  and that URL shipped with the $TAG release. Editing it changes what every
  existing user executes on their next run, with no version boundary.

  Put post-release changes in:  install-and-test-v0.6.1.sh

  Deliberate change?  TOLLGATE_UNFREEZE_LAUNCHER=1 $0 $*
EOF
exit 1
