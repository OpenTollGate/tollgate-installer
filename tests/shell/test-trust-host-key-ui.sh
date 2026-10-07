#!/usr/bin/env bash
# Test: the browser wizard can trust a router's SSH host key.
#
# WHY THIS EXISTS (2026-09-28, live MT3000, feed pre20)
#
# A `sysupgrade -n` re-keys Dropbear, so the wizard correctly refuses to send the
# root password until the new key is trusted (hostkey.go, C2-I-01). The wizard
# showed the fingerprint and told the operator to relaunch with
# --trust-host-key — but offered no way to act on it, and the WiFi/pre-stage
# failure path labelled that refusal "check router password and try Rescan". A
# browser-first operator was dead-ended with a wrong diagnosis.
#
# The fix is a trust control bound to the fingerprint the server already sends
# (ssh_fingerprint), an /api/trust-host-key endpoint, and a guard so a host-key
# refusal is never reported as a password problem.
#
# Method: hermetic source-pin. No network, no router, no docker. It asserts the
# SHIPPED bytes carry each half, so the UI, the API and the surface guard cannot
# drift apart. The endpoint's behaviour (mismatch refused, key remembered, no
# credential offered) is pinned by trust_api_test.go against the real handler and
# a real in-process SSH server; this script pins the wiring those tests cannot
# see (index.html and the route registration).
#
# usage: tests/shell/test-trust-host-key-ui.sh [tree]
# exit 0 = PASS, 2 = FAIL, 3 = environment
set -u

TREE="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
# Sources live under internal/app since the layout move (the wizard's Go package).
# Keep the old root path working so a pin still runs against a pre-move tree.
APP="${TREE}/internal/app"
app_file() { if [ -f "${APP}/$1" ]; then printf '%s\n' "${APP}/$1"; else printf '%s\n' "${TREE}/$1"; fi; }
UI="$(app_file index.html)"
MAIN="$(app_file main.go)"
API="$(app_file trust_api.go)"
HOSTKEY="$(app_file hostkey.go)"
DISCOVER="$(app_file discover.go)"

fail() { echo "FAIL: $*"; exit 2; }
ok()   { echo "ok   - $*"; }

# These four exist before the change; their absence is an environment problem.
for f in "$UI" "$MAIN" "$HOSTKEY" "$DISCOVER"; do
    [ -f "$f" ] || { echo "environment: no $f"; exit 3; }
done
# trust_api.go is part of the change: its absence is a FAIL, not an environment
# problem, so the RED control on the base tree reports the missing feature.
[ -f "$API" ] || fail "no trust_api.go — the /api/trust-host-key endpoint is missing"

echo "=== tree: $TREE"

# ---- 1. the UI offers the trust control ----------------------------------
grep -Fq 'id="trust-btn"' "$UI" \
    || fail "index.html has no trust button — the operator still cannot act on the fingerprint"
ok "index.html renders a trust control"

grep -Fq 'function trustHostKey' "$UI" \
    || fail "index.html defines no trustHostKey() handler"
grep -Fq "fetch('/api/trust-host-key'" "$UI" \
    || fail "trustHostKey() does not call /api/trust-host-key"
ok "the trust control calls /api/trust-host-key"

# The confirm step must name the fingerprint (the consent the operator chose).
grep -Fq 'Verify this exact fingerprint' "$UI" \
    || fail "the trust confirmation does not tell the operator to verify the fingerprint on the console"
ok "the trust confirmation points at the out-of-band check"

# ---- 2. the fingerprint is consumed structurally -------------------------
grep -Fq 'ssh_fingerprint' "$UI" \
    || fail "index.html does not read the server's ssh_fingerprint field (it would have to parse prose)"
grep -Fq 'ssh_fingerprint' "$DISCOVER" \
    || fail "RouterInfo does not expose ssh_fingerprint"
ok "the fingerprint travels as its own field from server to UI"

# ---- 3. a host-key refusal is not reported as a password problem ---------
grep -Fq 'function isHostKeyRefusal' "$UI" \
    || fail "index.html has no isHostKeyRefusal() guard"
grep -Fq 'isHostKeyRefusal(data.error)' "$UI" \
    || fail "wifiScan() does not route a host-key refusal to the trust control"
ok "a host-key refusal is not misreported as a password/scan failure"

# ---- 4. the server wires the endpoint ------------------------------------
grep -Fq '"/api/trust-host-key"' "$MAIN" \
    || fail "main.go does not register /api/trust-host-key"
grep -Fq 'func handleTrustHostKey' "$API" \
    || fail "trust_api.go declares no handleTrustHostKey"
grep -Fq 'func looksLikeFingerprint' "$API" \
    || fail "the endpoint has no fingerprint shape check"
ok "the endpoint is registered and validates its input"

# ---- 5. the trust dial verifies, offers nothing, and remembers -----------
grep -Fq 'func trustHostKeyForHost' "$HOSTKEY" \
    || fail "hostkey.go declares no trustHostKeyForHost"
grep -Fq 'HostKeyCallback' "$HOSTKEY" \
    || fail "the trust dial has no host-key verification"
grep -Fq 'rememberHostKey(' "$HOSTKEY" \
    || fail "the trust dial does not persist the verified key"

# No credential may be reachable from the trust dial. Bound to the helper's own
# body so an unrelated ssh.Password elsewhere in the file cannot satisfy it.
BODY="$(awk '/^func trustHostKeyForHost/,/^}/' "$HOSTKEY")"
[ -n "$BODY" ] || fail "could not extract trustHostKeyForHost's body"
for forbidden in 'ssh.Password' 'keyboardInteractiveAuth' 'tryDefaultKeys' 'sshConnect(' 'ssh.PublicKeys'; do
    case "$BODY" in
        *"$forbidden"*) fail "trustHostKeyForHost offers $forbidden — it must only verify the host key, never authenticate" ;;
    esac
done
ok "the trust dial verifies the key, offers no credential, and remembers it"

echo
echo "PASS: the wizard can trust a router's SSH host key from the browser"
exit 0
