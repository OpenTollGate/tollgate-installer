#!/usr/bin/env bash
# Regression test: the install run book must not hand the operator a
# COPY-PASTEABLE credential — and must not describe the removed install-time
# credential mechanism.
#
# WHY (2026-10-12, real operator lockout)
#
#   §3.1 of the run book showed `printf '%s' 'choose-a-strong-admin-password'
#   > /etc/tollgate/admin-password`. A tester pasted it verbatim, so the package
#   set root to the literal string `choose-a-strong-admin-password` — a password
#   the operator never chose and had no record of. The package no longer accepts
#   a credential from the environment or a file at all; the operator sets it
#   themselves with `passwd` (which prompts and echoes nothing).
#
# This test is a guard rail on the PROSE: it fails if the run book reintroduces a
# literal credential, an install-environment variable, or the staged-file dance.
# Cheap, hermetic, no router.
#
# Controls, both directions:
#   - the CURRENT run book MUST pass;
#   - a fixture carrying a `printf '%s' '<literal>' > /etc/tollgate/admin-password`
#     line MUST be refused (so the check cannot pass by matching nothing).
#
# usage: tests/shell/test-runbook-no-credential-literal.sh [tree]
# exit 0 = PASS, 2 = FAIL, 3 = environment
set -u

TREE="${1:-$(cd "$(dirname "$0")/../.." && pwd)}"
RUNBOOK="${TREE}/docs/install-runbook.md"

fail() { echo "FAIL: $*"; exit 2; }
ok()   { echo "ok   - $*"; }

[ -f "$RUNBOOK" ] || { echo "environment: no $RUNBOOK"; exit 3; }

# Forbidden patterns: the removed install-env vars, the staged root-only files,
# and a plaintext literal written into any /etc/tollgate credential file.
check_clean() { # check_clean <file> ; returns 0 when clean
    local f="$1" hit
    hit="$(grep -nE 'TOLLGATE_ADMIN_PASSWORD|TOLLGATE_PRIVATE_WIFI_PASSWORD' "$f")" && { printf '%s\n' "$hit" >&2; return 1; }
    hit="$(grep -nE '>[[:space:]]*/etc/tollgate/(admin-password|wifi-password)' "$f")" && { printf '%s\n' "$hit" >&2; return 1; }
    hit="$(grep -nE "printf[[:space:]]+'%s'[[:space:]]+'[^']+'[[:space:]]*>[[:space:]]*/etc/tollgate/" "$f")" && { printf '%s\n' "$hit" >&2; return 1; }
    return 0
}

if check_clean "$RUNBOOK"; then
    ok "the run book carries no install-time credential variable or staged credential file"
else
    fail "the run book reintroduced an install-time credential (env var, staged file, or literal)"
fi

# It MUST still tell the operator how to set both passwords, by hand.
grep -qE '(^|[^[:alnum:]])passwd root' "$RUNBOOK" \
    && ok "the run book sets the admin password with 'passwd root'" \
    || fail "the run book no longer tells the operator to run 'passwd root'"
grep -qE 'tollgate network private set-password' "$RUNBOOK" \
    && ok "the run book sets the private WiFi passphrase through the CLI" \
    || fail "the run book no longer tells the operator how to set the private WiFi passphrase"

# False-pass control: the SAME check must reject a fixture with the literal.
FIXTURE="$(mktemp)"
trap 'rm -f "$FIXTURE"' EXIT
printf "printf '%%s' 'choose-a-strong-admin-password' > /etc/tollgate/admin-password\n" > "$FIXTURE"
if check_clean "$FIXTURE"; then
    fail "control: the check accepted a fixture with a literal staged credential"
else
    ok "control: the check rejects a fixture with a literal staged credential"
fi

echo "PASS"
exit 0
