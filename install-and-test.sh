#!/usr/bin/env bash
# ============================================================================
#  TollGate Installer — curl | bash test script
#
#  Downloads the Go tollgate-installer binary for your OS/arch, runs it
#  (serves the web UI at :8099), and — given a router IP — deploys TollGate
#  to a physical OpenWrt router via the REST API, then verifies the result.
#
#  USAGE
#  ---------------------------------------------------------------
#  Interactive (just opens the web UI, you drive it in a browser):
#      bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
#
#  Headless full test against a router (no browser needed):
#      bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
#          <ROUTER_IP> <ROUTER_PASSWORD> <LIGHTNING_ADDRESS>
#
#  Example:
#      bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
#          10.47.41.1 '' user@coinos.io
#
#  The script:
#    1. detects OS + arch, downloads the right `tollgate-installer` binary
#    2. runs it on :8099 (auto-picks a free port if taken)
#    3. runs a Wifi probe via the API, triggers POST /api/deploy
#    4. polls /api/status/<id> until deploy completes
#    5. verifies the router post-deploy: hostname, ports, DNS, LNURL,
#       captive portal, TollGate health ad.
#  ---------------------------------------------------------------
set -euo pipefail

# --- config ---------------------------------------------------------------
BIN_NAME="tollgate-installer"
PORT="${PORT:-8099}"
# Download source. Prefers the OpenTollGate org release; falls back to the
# felixfelix-bot fork pre-merge build.
GH_REPO="OpenTollGate/tollgate-installer"
FORK_REPO="felixfelix-bot/tollgate-installer"

# --- 1. detect platform ---------------------------------------------------
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
    esac
    PLATFORM="${os}-${arch}"
}

detect_platform

echo "Detected platform: ${PLATFORM}"# --- 2. find + download binary -----------------------------------------------
download() {
    local repo="$1" url tries
    url="https://github.com/${repo}/releases/latest/download/${BIN_NAME}-${PLATFORM}"
    echo "Downloading ${BIN_NAME}-${PLATFORM} from ${repo} ..."
    if curl -fsSL --retry 3 -o "${BIN_NAME}" "${url}"; then
        chmod +x "${BIN_NAME}"
        echo "OK: ./${BIN_NAME} ($(du -h ${BIN_NAME} | cut -f1))"
        return 0
    fi
    return 1
}

# Try the fork pre-merge release first (contains the Go binary built from
# PR #2 head). Once the PR merges, this same URL pattern works on the org repo.
if ! download "${FORK_REPO}"; then
    echo "Fork download failed; trying OpenTollGate org release..." >&2
    if ! download "${GH_REPO}"; then
        echo "ERROR: could not download ${BIN_NAME}-${PLATFORM} from either repo." >&2
        exit 1
    fi
fi

# --- 3. find a free port (default 8099) ------------------------------------
pick_port() {
    local p="$1"
    while curl -s -o /dev/null --max-time 1 "http://127.0.0.1:${p}" 2>/dev/null; do
        p=$((p+1))
    done
    echo "$p"
}
PORT="$(pick_port "${PORT}")"

# --- 4. run the installer ---------------------------------------------------
echo
echo "Starting ${BIN_NAME} on http://localhost:${PORT} ..."
./${BIN_NAME} -port "${PORT}" >/tmp/${BIN_NAME}.log 2>&1 &
SERVER_PID=$!
trap 'kill ${SERVER_PID} 2>/dev/null || true' EXIT
sleep 1.5

if ! curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${PORT}/"; then
    echo "ERROR: installer did not come up on :${PORT}. Log:" >&2
    tail -20 /tmp/${BIN_NAME}.log >&2
    exit 1
fi
echo "Installer UI is up: http://localhost:${PORT}/"

# --- 5. optional headless deploy --------------------------------------------
ROUTER_IP="${1:-}"
ROUTER_PASS="${2:-}"
LNURL="${3:-}"

if [ -z "${ROUTER_IP}" ]; then
    echo
    echo "No router IP given — interactive mode. Open http://localhost:${PORT}/"
    echo "in your browser, or re-run with: <IP> <PASSWORD> <LNURL_ADDRESS>"
    wait "${SERVER_PID}" 2>/dev/null || true
    exit 0
fi

echo
echo "=== Scanning for routers on LAN ==="
curl -s "http://127.0.0.1:${PORT}/api/scan" | python3 -m json.tool 2>/dev/null | head -40 || true

echo
echo "=== Deploying TollGate to ${ROUTER_IP} ==="
DEPLOY_JSON="{\"ip\":\"${ROUTER_IP}\",\"password\":\"${ROUTER_PASS}\",\"lnurl\":\"${LNURL}\",\"mode\":\"wan\"}"
RESP="$(curl -s -X POST "http://127.0.0.1:${PORT}/api/deploy" \
    -H 'Content-Type: application/json' -d "${DEPLOY_JSON}")"
echo "Deploy response: ${RESP}"
JOB_ID="$(echo "${RESP}" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("job_id",""))' 2>/dev/null || true)"
if [ -z "${JOB_ID}" ]; then
    echo "ERROR: no job_id in deploy response" >&2
    exit 1
fi

echo "Job: ${JOB_ID}"
echo "Polling status..."
while true; do
    STATUS="$(curl -s "http://127.0.0.1:${PORT}/api/status/${JOB_ID}")"
    STATE="$(echo "${STATUS}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("status",""))' 2>/dev/null || true)"
    echo "  ${STATE}: $(echo "${STATUS}" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(", ".join([f"{s.get(chr(115)+chr(116)+chr(101)+chr(112))}:{s.get(chr(115)+chr(116)+chr(97)+chr(116)+chr(117)+chr(115))}" for s in d.get("steps",[])]))' 2>/dev/null || echo "")"
    if [ "${STATE}" = "done" ]; then
        echo "=== DEPLOY COMPLETE ==="
        break
    elif [ "${STATE}" = "failed" ] || [ "${STATE}" = "error" ]; then
        echo "=== DEPLOY FAILED ===" >&2
        echo "${STATUS}" | python3 -m json.tool >&2
        exit 1
    fi
    sleep 3
done

# --- 6. post-deploy router verification --------------------------------------
echo
echo "=== Verifying router ${ROUTER_IP} post-deploy ==="
sshpass -p "${ROUTER_PASS}" ssh -o StrictHostKeyChecking=no -o ConnectTimeout=8 root@"${ROUTER_IP}" '
    echo "--- hostname ---";          cat /proc/sys/kernel/hostname
    echo "--- tollgate-wrt build ---"; tollgate version 2>/dev/null || opkg list-installed tollgate-wrt 2>/dev/null || apk info -v tollgate-wrt 2>/dev/null || echo "build version unavailable"
    echo "--- ports ---";             netstat -tln 2>/dev/null | grep -E ":80 |:2050|:2121" || true
    echo "--- DNS tollgate.lan ---";  nslookup tollgate.lan 127.0.0.1 2>/dev/null | tail -3 || true
    echo "--- LNURL ---";             jq -r ".public_identities[] | select(.name==\"owner\") | .lightning_address" /etc/tollgate/identities.json 2>/dev/null || true
    echo "--- captive portal ---";    ls -la /etc/tollgate/tollgate-captive-portal-site/splash.html /etc/nodogsplash/htdocs/splash.html 2>/dev/null || echo MISSING
    echo "--- TollGate health ---";   wget -qO- --timeout=5 http://127.0.0.1:2121/ 2>/dev/null | head -c 200 || echo "health ad unreachable"
' 2>&1 || echo "(ssh verification failed — check password/host)"

echo
echo "Done. Installer log: /tmp/${BIN_NAME}.log"
echo "Router: Telnet/SSH root@${ROUTER_IP} — TollGate API :2121, portal :2050"
kill "${SERVER_PID}" 2>/dev/null || true
