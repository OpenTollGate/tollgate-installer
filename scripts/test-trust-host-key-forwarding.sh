#!/usr/bin/env bash
# Process-level test for BLOCK 1 of the #41 review (PR #41, merged as b5b306a).
#
# The launcher consumed EVERY argument in its own loop (unknown args became
# POSITIONAL entries at install-and-test.sh:114) and then started the binary with
# "$@" — always empty by then. So:
#
#   * `--trust-host-key SHA256:… ` never reached the installer binary (the flag the
#     curl|bash trust instruction tells the operator to use did nothing), and
#   * the flag landed in POSITIONAL[0], which flips an interactive run to headless
#     and POSTs /api/deploy with ip="--trust-host-key"; with the flag first and a
#     real <IP> <PASSWORD> <LNURL> behind it, all three shift by two slots.
#
# Method: a fake installer binary (python3) records its OWN argv and answers the
# handful of endpoints the launcher's headless path calls, then the REAL
# install-and-test.sh is driven exactly as a curl|bash user would. The verdict is
# the recorded argv and the recorded POST /api/deploy body — not the launcher's
# own narration, and not the installer's self-report.
#
# Cases:
#   1. `--trust-host-key SHA256:… <IP> <PW> <LNURL>`  → argv carries the flag,
#      argv carries nothing else, and the positionals arrive unshifted.
#   2. same without the flag                          → argv is exactly "-port <p>"
#      (the "${TRUST_ARGS[@]+…}" guard, i.e. bash 3.2 + `set -u` stays clean).
#   3. flag AFTER the positionals                     → still forwarded (order does
#      not matter to the operator) and the positionals are still unshifted.
#   4. `--trust-host-key` with no value               → fails loudly, and does NOT
#      start a binary (no `shift 2` out-of-range surprise).
#
# Requirements: bash, curl, python3 (fake binary + server), and the repo tree.
# Prints PASS/FAIL; exit 0 = PASS, 2 = FAIL, 3 = environment.
#
# usage: scripts/test-trust-host-key-forwarding.sh [tree] [evidence-file]
set -u

TREE="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
EVIDENCE="${2:-}"
TAG="${TOLLGATE_TEST_TAG:-v0.6.0-alpha2-pre9}"   # only skips the feed-release lookup
PORT="${TOLLGATE_TEST_PORT:-8123}"
IP="127.0.0.1"
PASS="router-root-pw"
LNURL="e2e@example.com"
FP="SHA256:3mHd7Q0jVxZ2nXbq2Vn0YyA4CqTnYq3mHd7Q0jVxZ2n"   # not a real key: the fake binary never verifies it

WORK_ROOT="${TG_TEST_WORK_ROOT:-$HOME/.cache/tollgate-installer-test}"
mkdir -p "$WORK_ROOT"
WORK="$(mktemp -d "$WORK_ROOT/trust-fwd.XXXXXX")"
FAILED=0

emit() {
    printf '%s\n' "$*"
    if [ -n "${EVIDENCE}" ]; then printf '%s\n' "$*" >> "${EVIDENCE}"; fi
}

fail() { emit "  FAIL  $*"; FAILED=1; }
pass() { emit "  PASS  $*"; }

for tool in bash curl python3; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        emit "ENVIRONMENT: $tool is required (fake installer binary + HTTP server)"
        exit 3
    fi
done
if [ ! -f "$TREE/install-and-test.sh" ]; then
    emit "ENVIRONMENT: no install-and-test.sh under $TREE"
    exit 3
fi

emit "tree:      $TREE"
emit "work:      $WORK"
emit "launcher:  $(grep -c . "$TREE/install-and-test.sh") non-empty lines"

# --- the fake installer: records argv, serves the launcher's headless calls ----
FAKE="$WORK/fake-tollgate-installer"
cat > "$FAKE" <<'PY'
#!/usr/bin/env python3
"""Fake tollgate-installer for scripts/test-trust-host-key-forwarding.sh.

Writes its own argv (one argument per line) into $FAKE_LOG_DIR/argv and every
POST body into $FAKE_LOG_DIR/deploy-body.jsonl, then serves just enough of the
HTTP API for install-and-test.sh's headless path to run to completion.
"""
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

LOGDIR = os.environ["FAKE_LOG_DIR"]
os.makedirs(LOGDIR, exist_ok=True)

port = None
i = 1
while i < len(sys.argv):
    if sys.argv[i] == "-port" and i + 1 < len(sys.argv):
        port = sys.argv[i + 1]
        i += 2
        continue
    i += 1

with open(os.path.join(LOGDIR, "argv"), "a") as fh:
    for arg in sys.argv[1:]:
        fh.write(arg + "\n")

started = os.path.join(LOGDIR, "started")
with open(started, "a") as fh:
    fh.write("started\n")

if port is None:
    sys.exit("fake installer: no -port in argv")

STATUS = {
    "status": "done",
    "steps": [{"name": "install", "desc": "Installing tollgate-wrt",
               "status": "done", "detail": "tollgate-wrt source fake"}],
    "log": [{"msg": "tollgate-wrt source fake-installer-test"},
            {"msg": "Installed tollgate-wrt build fake"}],
}
CONFIG = {
    "feed_repo": "FreedomTechFeed/packages",
    "feed_release_tag": os.environ.get("FAKE_FEED_TAG", "v0.6.0-alpha2-pre9"),
    "feed_pkg_version": "0.6.0",
    "installer_version": "fake",
    "installer_commit": "fakecommit",
}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _send(self, obj, code=200):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/":
            self._send({"ok": True})
        elif self.path == "/api/config":
            self._send(CONFIG)
        elif self.path == "/api/scan":
            self._send({"routers": []})
        elif self.path.startswith("/api/status/"):
            self._send(STATUS)
        else:
            self._send({"error": "not found"}, 404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8", "replace")
        with open(os.path.join(LOGDIR, "deploy-body.jsonl"), "a") as fh:
            fh.write(body + "\n")
        self._send({"job_id": "fake-job-1"})


HTTPServer(("127.0.0.1", int(port)), Handler).serve_forever()
PY
chmod +x "$FAKE"

# --- drivers ------------------------------------------------------------------

# run_launcher <case-name> [args...] — drives the REAL launcher with the fake
# binary, in its own log directory. Returns the launcher's exit code.
run_launcher() {
    local case_name="$1"; shift
    local logdir="$WORK/$case_name"
    mkdir -p "$logdir"
    FAKE_LOG_DIR="$logdir" FAKE_FEED_TAG="$TAG" PORT="$PORT" \
        bash "$TREE/install-and-test.sh" --bin "$FAKE" --tag "$TAG" "$@" \
        >"$logdir/launcher.log" 2>&1
    return $?
}

argv_of() {  # argv_of <case-name> — the fake binary's recorded argv, one per line
    cat "$WORK/$1/argv" 2>/dev/null || true
}

expect_argv() {  # expect_argv <case-name> <expected-argv (newline separated)>
    local case_name="$1" expected="$2" got
    got="$(argv_of "$case_name")"
    if [ "$got" = "$expected" ]; then
        pass "$case_name: binary argv is exactly: $(printf '%s' "$expected" | tr '\n' ' ')"
    else
        fail "$case_name: binary argv mismatch
          expected: $(printf '%s' "$expected" | tr '\n' ' ')
          got:      $(printf '%s' "$got" | tr '\n' ' ')"
    fi
}

expect_positionals() {  # expect_positionals <case-name> <port>
    local case_name="$1" body
    body="$(cat "$WORK/$case_name/deploy-body.jsonl" 2>/dev/null || true)"
    if [ -z "$body" ]; then
        fail "$case_name: no POST /api/deploy body was recorded"
        return
    fi
    local want_ip="\"ip\":\"$IP\"" want_pw="\"password\":\"$PASS\"" want_ln="\"lnurl\":\"$LNURL\""
    if printf '%s' "$body" | grep -Fq "$want_ip" &&
       printf '%s' "$body" | grep -Fq "$want_pw" &&
       printf '%s' "$body" | grep -Fq "$want_ln"; then
        pass "$case_name: positionals unshifted (ip=$IP password=<redacted> lnurl=$LNURL)"
    else
        fail "$case_name: the deploy body does not carry the positionals unshifted
          want: ip=$IP password=<redacted> lnurl=$LNURL
          got:  $body"
    fi
    if printf '%s' "$body" | grep -Fq -- "--trust-host-key"; then
        fail "$case_name: the trust flag leaked into the deploy body ($body)"
    fi
}

emit "--- case 1: --trust-host-key <fp> <IP> <PW> <LNURL> ---"
run_launcher case1 --trust-host-key "$FP" "$IP" "$PASS" "$LNURL"
RC1=$?
port1="$(sed -n '2p' "$WORK/case1/argv" 2>/dev/null || true)"
expect_argv case1 "$(printf -- '-port\n%s\n--trust-host-key\n%s' "$port1" "$FP")"
expect_positionals case1
if [ "$RC1" -eq 0 ]; then
    pass "case1: launcher exited 0 after a completed headless deploy"
else
    fail "case1: launcher exited $RC1 (see $WORK/case1/launcher.log)"
    tail -20 "$WORK/case1/launcher.log" | sed 's/^/          | /'
fi

emit "--- case 2: no flag (empty TRUST_ARGS must not break the runner) ---"
run_launcher case2 "$IP" "$PASS" "$LNURL"
RC2=$?
port2="$(sed -n '2p' "$WORK/case2/argv" 2>/dev/null || true)"
expect_argv case2 "$(printf -- '-port\n%s' "$port2")"
expect_positionals case2
if [ "$RC2" -eq 0 ]; then
    pass "case2: launcher exited 0"
else
    fail "case2: launcher exited $RC2 (see $WORK/case2/launcher.log)"
    tail -20 "$WORK/case2/launcher.log" | sed 's/^/          | /'
fi

emit "--- case 3: flag after the positionals ---"
run_launcher case3 "$IP" "$PASS" "$LNURL" --trust-host-key "$FP"
RC3=$?
port3="$(sed -n '2p' "$WORK/case3/argv" 2>/dev/null || true)"
expect_argv case3 "$(printf -- '-port\n%s\n--trust-host-key\n%s' "$port3" "$FP")"
expect_positionals case3
if [ "$RC3" -eq 0 ]; then
    pass "case3: launcher exited 0"
else
    fail "case3: launcher exited $RC3 (see $WORK/case3/launcher.log)"
    tail -20 "$WORK/case3/launcher.log" | sed 's/^/          | /'
fi

emit "--- case 4: --trust-host-key with no value ---"
run_launcher case4 --trust-host-key
RC4=$?
if [ "$RC4" -ne 0 ]; then
    pass "case4: launcher refused the valueless flag (exit $RC4)"
else
    fail "case4: launcher accepted --trust-host-key with no fingerprint"
fi
if [ -f "$WORK/case4/started" ]; then
    fail "case4: the installer binary was started even though the flag had no value"
else
    pass "case4: no installer binary was started"
fi
if grep -q -- "--trust-host-key needs a fingerprint" "$WORK/case4/launcher.log" 2>/dev/null; then
    pass "case4: the operator is told what is missing"
else
    fail "case4: no actionable error text (see $WORK/case4/launcher.log)"
fi

emit ""
if [ "$FAILED" -eq 0 ]; then
    emit "PASS — --trust-host-key reaches the installer binary and the positionals stay put"
    emit "       fake-binary argv + deploy bodies under $WORK"
    exit 0
fi
emit "FAIL — see $WORK"
exit 2
