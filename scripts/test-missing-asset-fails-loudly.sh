#!/usr/bin/env bash
# Process-level test for C2-I-02: a requested release whose asset does not exist
# must FAIL LOUDLY — non-zero exit, the tag named in the operator-visible
# message, and NOTHING installed.
#
# Method: the installer under test is the binary built from THIS tree, pointed
# at a fixture SSH "router" (alpine + fake OpenWrt identity, scripts/fixture-router)
# in a container and driven by the repo's own headless test script
# install-and-test.sh — the same path a curl|bash user takes. The requested
# TOLLGATE_FEED_RELEASE_TAG does not exist, so the derived primary URL is a real
# 404 from github.com while the pinned older fallback asset is still 200: the
# pre-fix installer silently installed that older build.
#
# The verdict is install-and-test.sh's exit code, the failure text the operator
# sees, and what the fixture was asked to do — not the installer's self-report.
#
# Requirements: docker with alpine:3.20, curl, go, and network access to
# github.com. Prints PASS/FAIL; exit 0 = PASS, 2 = FAIL, 3 = environment.
#
# usage: scripts/test-missing-asset-fails-loudly.sh [worktree] [evidence-file]
set -u

TREE="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
EVIDENCE="${2:-}"
TAG="${TOLLGATE_TEST_MISSING_TAG:-v0.6.0-alpha2-pre999}"   # nonexistent by construction
PORT="${TOLLGATE_TEST_PORT:-8155}"
WORK_ROOT="${TG_TEST_WORK_ROOT:-$HOME/.cache/tollgate-installer-test}"
mkdir -p "$WORK_ROOT"
WORK="$(mktemp -d "$WORK_ROOT/run.XXXXXX")"
CONTAINER="tg-missing-asset-fixture"

log() { printf '%s\n' "$*"; }
emit() { log "$*"; if [ -n "$EVIDENCE" ]; then printf '%s\n' "$*" >> "$EVIDENCE"; fi; }

# emit_lines — send each line of stdin through emit(), so a section's content
# lands in the evidence artifact as well as on the console. A plain pipeline
# (`grep … | sed …`) writes to stdout only: section 5 was empty in the RED and
# GREEN artifacts for exactly that reason, so the artifact never showed the
# failure text it exists to record.
emit_lines() { while IFS= read -r line; do emit "  $line"; done; }

# emit_matches <pattern> — the run-output lines matching <pattern>, trimmed and
# indented, into the console AND the artifact. An empty match set is reported
# explicitly rather than silently leaving the section blank.
emit_matches() {
  local lines
  lines="$(grep -n "$1" "$OUT" 2>/dev/null | tail -20 | sed -e 's/\(.\{400\}\).*/\1.../')"
  if [ -z "$lines" ]; then
    emit "  (no line in the run output matches: $1)"
    return 0
  fi
  printf '%s\n' "$lines" | emit_lines
}

if ! command -v docker >/dev/null 2>&1; then
  log "ENVIRONMENT: docker is required for this process-level test"; exit 3
fi
command -v curl >/dev/null 2>&1 || { log "ENVIRONMENT: curl is required"; exit 3; }

cleanup() { docker rm -f "$CONTAINER" >/dev/null 2>&1; }
trap cleanup EXIT

[ -n "$EVIDENCE" ] && : > "$EVIDENCE"

emit "================================================================================"
emit " C2-I-02 PROCESS-LEVEL TEST — a missing feed asset must FAIL LOUDLY (tag named),"
emit " and install NOTHING.  PASS = non-zero exit + tag named + no install attempt."
emit "================================================================================"
emit "host        : $(hostname)"
emit "date        : $(date -Is)"
emit "tree        : $TREE"
emit "commit      : $(git -C "$TREE" rev-parse HEAD)"
emit "commit subj : $(git -C "$TREE" log -1 --format=%s)"
emit "tree dirty  : $(git -C "$TREE" status --porcelain | wc -l) tracked/untracked change(s) (0 = the verified tree IS the commit)"
emit "requested   : TOLLGATE_FEED_RELEASE_TAG=$TAG (nonexistent by construction)"
emit

emit "--- 1. the primary URL derived for that tag really 404s ---"
PRIMARY="https://github.com/FreedomTechFeed/packages/releases/download/$TAG/tollgate-wrt_0.6.0_alpha2_pre999_aarch64_cortex-a53.ipk"
emit "URL: $PRIMARY"
PRIMARY_CODE=$(curl -s -o /dev/null -w '%{http_code}' -L --max-time 30 "$PRIMARY")
emit "HTTP status: $PRIMARY_CODE"
emit
emit "--- 1b. the pinned OLDER fallback asset it must NOT silently install (still 200) ---"
FB="https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk"
emit "URL: $FB"
FB_CODE=$(curl -s -o /dev/null -w '%{http_code}' -L --max-time 60 "$FB")
emit "HTTP status: $FB_CODE"
emit

emit "--- 2. binary under test (built from this tree) ---"
BIN="$WORK/installer-missing"
if ! ( cd "$TREE" && GOFLAGS=-buildvcs=false CGO_ENABLED=0 go build -o "$BIN" . ); then
  log "ENVIRONMENT: go build failed"; exit 3
fi
emit "binary: $BIN"
emit "sha256: $(sha256sum "$BIN" | cut -d' ' -f1)"
emit

emit "--- 3. fixture router (real SSH server, alpine + fake OpenWrt identity) ---"
docker rm -f "$CONTAINER" >/dev/null 2>&1
STUB_BIN="$WORK/fixture-router"
if ! ( cd "$TREE" && GOFLAGS=-buildvcs=false CGO_ENABLED=0 go build -o "$STUB_BIN" ./scripts/fixture-router ); then
  log "ENVIRONMENT: fixture-router build failed"; exit 3
fi
if ! docker run -d --name "$CONTAINER" \
  -v "$STUB_BIN:/stub:ro" \
  -e STUB_CMD_LOG=/tmp/stub-cmds.log \
  alpine:3.20 sh -c "mkdir -p /tmp/sysinfo &&
    printf \"DISTRIB_ID='OpenWrt'\nDISTRIB_RELEASE='24.10.0'\nDISTRIB_REVISION='r28597-0425664679'\nDISTRIB_TARGET='mediatek/filogic'\nDISTRIB_ARCH='aarch64_cortex-a53'\nDISTRIB_DESCRIPTION='OpenWrt 24.10.0 r28597-0425664679'\n\" > /etc/openwrt_release &&
    printf 'glinet,gl-mt6000\n' > /tmp/sysinfo/board_name &&
    exec /stub" >/dev/null; then
  log "ENVIRONMENT: could not start the fixture container"; exit 3
fi
sleep 2
if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" != "true" ]; then
  log "ENVIRONMENT: the fixture container exited immediately:"
  docker logs "$CONTAINER" 2>&1 | head -10 | emit_lines
  exit 3
fi
ROUTER_IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$CONTAINER")
if [ -z "$ROUTER_IP" ]; then
  log "ENVIRONMENT: the fixture container has no IP address"; exit 3
fi
emit "fixture router container IP: $ROUTER_IP"
docker logs "$CONTAINER" 2>&1 | head -2 | emit_lines
emit

emit "--- 4. drive it with the repo's own headless script (install-and-test.sh) ---"
emit "    TOLLGATE_FEED_RELEASE_TAG=$TAG install-and-test.sh --bin $BIN $ROUTER_IP <pw> e2e@example.com"
# The fixture generates a FRESH host key on every start and PRINTS the
# fingerprint it will present, so read it from the fixture's own log — that is
# the authoritative value, and on this host the only one available: ssh-keyscan
# gets nothing usable out of the stub (measured: repeated attempts return an
# empty fingerprint while the stub's own line names the one the installer then
# presents and refuses). Without the key the deploy stops at the verify step
# ("not trusted … no credentials were sent") and never reaches step 6 — which
# left section 5 empty, while the "refus" assertions below were satisfied by
# that host-key refusal ("CONNECTION REFUSED") rather than by the
# missing-asset refusal this test exists for.
fixture_fingerprint() {
  docker logs "$CONTAINER" 2>&1 |
    sed -n 's/.*host key fingerprint \(SHA256:[A-Za-z0-9+/=]*\).*/\1/p' | tail -1
}
FINGERPRINT=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  FINGERPRINT="$(fixture_fingerprint)"
  [ -n "$FINGERPRINT" ] && break
  FINGERPRINT="$(ssh-keyscan -T 5 -t ssh-ed25519 -p 22 "$ROUTER_IP" 2>/dev/null | awk 'NF>=3 {print $2" "$3}' | head -1 | ssh-keygen -lf - 2>/dev/null | awk '{print $2}')"
  [ -n "$FINGERPRINT" ] && break
  sleep 2
done
if [ -z "$FINGERPRINT" ]; then
  log "ENVIRONMENT: could not read the fixture's SSH host key fingerprint"
  docker logs "$CONTAINER" 2>&1 | head -5 | emit_lines
  exit 3
fi
emit "fixture SSH host key (trusted out of band, from the fixture's own log): $FINGERPRINT"
# Make every package-manager install attempt observable *on the fixture*: a
# shim that records the call and refuses. If the refusal path is correct, this
# is never invoked.
docker exec "$CONTAINER" sh -c 'rm -f /tmp/stub-cmds.log; : > /tmp/install-attempts.log; printf "#!/bin/sh\necho \"apk \$@\" >> /tmp/install-attempts.log\nexit 1\n" > /usr/local/bin/apk; chmod +x /usr/local/bin/apk' 2>/dev/null
OUT="$WORK/install-and-test.out"
# Only reachable with the pin above: without it the deploy stops at the verify
# step ("not trusted … no credentials were sent"), never reaches step 6, and
# section 5 stays empty while the "refus" assertions are satisfied by the
# host-key refusal instead of the missing-asset refusal this test exists for.
TOLLGATE_FEED_RELEASE_TAG="$TAG" PORT="$PORT" \
  bash "$TREE/install-and-test.sh" --bin "$BIN" "$ROUTER_IP" "router-root-pw" "e2e@example.com" \
  --trust-host-key "$FINGERPRINT" \
  > "$OUT" 2>&1
RC=$?
emit "install-and-test.sh EXIT CODE: $RC"
emit
emit "--- 5. the failure the operator sees (verbatim, untrimmed) ---"
emit_matches 'ERROR\|"error"\|DEPLOY FAILED\|source:'
emit
emit "--- 6. what the fixture router was asked to do (its own log) ---"
emit "install attempts recorded on the fixture:"
ATTEMPTS="$(docker exec "$CONTAINER" cat /tmp/install-attempts.log 2>/dev/null || true)"
if [ -z "$ATTEMPTS" ]; then
  emit "  (none — the fixture was never asked to install anything)"
else
  printf '%s\n' "$ATTEMPTS" | emit_lines
fi
emit "tollgate-wrt package files on the fixture:"
PKGFILES="$(docker exec "$CONTAINER" sh -c 'ls -la /tmp/*.ipk /tmp/*.apk /etc/tollgate 2>/dev/null' || true)"
if [ -z "$PKGFILES" ]; then
  emit "  (none — nothing was installed)"
else
  printf '%s\n' "$PKGFILES" | emit_lines
fi
emit

# ---------------------------------------------------------------- assertions
FAILED=0
raw_attempts=$(docker exec "$CONTAINER" cat /tmp/install-attempts.log 2>/dev/null || true)
staged_files=$(docker exec "$CONTAINER" sh -c 'ls /tmp/*.ipk /tmp/*.apk 2>/dev/null' || true)

emit "--- 7. assertions ---"
if [ "$RC" -ne 0 ]; then
  emit "  PASS  install-and-test.sh exited non-zero ($RC)"
else
  emit "  FAIL  install-and-test.sh exited 0 — a missing asset was rendered as success"; FAILED=1
fi
if [ "$PRIMARY_CODE" = "404" ]; then
  emit "  PASS  the requested tag's asset is really unavailable (404)"
else
  emit "  FAIL  primary URL returned $PRIMARY_CODE, so the premise is not testable"; FAILED=1
fi
if [ "$FB_CODE" = "200" ]; then
  emit "  PASS  the older fallback asset really exists (200) — the silent-downgrade target"
else
  emit "  FAIL  the pinned fallback asset returned $FB_CODE; test premise changed"; FAILED=1
fi
if grep -q "$TAG" "$OUT"; then
  emit "  PASS  the requested tag $TAG appears in the run output (fail-loud message)"
else
  emit "  FAIL  the requested tag $TAG is never named in the run output"; FAILED=1
fi
if grep -qi 'refus' "$OUT"; then
  emit "  PASS  the refusal wording is present"
else
  emit "  FAIL  no refusal wording in the run output"; FAILED=1
fi
# The two checks above are generic: an SSH host-key refusal at the verify step
# ("CONNECTION REFUSED") also satisfies them, and so does the tag the run prints
# from /api/config — which is how this test could report PASS without ever
# reaching the code under test. Pin the step-6 detail itself.
if grep -q "refusing to install an older package" "$OUT"; then
  emit "  PASS  the run reached step 6 and refused there (step detail present)"
else
  emit "  FAIL  the run never reached step 6's missing-asset refusal: the step detail"
  emit "        \"refusing to install an older package\" is absent — section 5 shows what"
  emit "        happened instead"; FAILED=1
fi
if [ -z "$raw_attempts" ]; then
  emit "  PASS  fixture recorded zero install attempts"
else
  emit "  FAIL  the fixture was asked to install something:"; emit "        $raw_attempts"; FAILED=1
fi
if [ -z "$staged_files" ]; then
  emit "  PASS  no package file landed on the fixture"
else
  emit "  FAIL  package files on the fixture: $staged_files"; FAILED=1
fi

emit
if [ "$FAILED" -eq 0 ]; then
  emit "RESULT: PASS — the missing asset failed loudly, named $TAG, and installed nothing."
  exit 0
fi
emit "RESULT: FAIL — see the assertions above."
exit 2
