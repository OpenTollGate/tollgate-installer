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
# Feed release selection. Default: the newest pre-release (alpha channel), so
# the launcher never lags behind the packages repo the way a compiled pin does.
# Override with --tag / --channel, or TOLLGATE_FEED_RELEASE_TAG. The chosen tag
# is exported as TOLLGATE_FEED_RELEASE_TAG, which the installer binary resolves
# at startup, so the whole run targets that exact release.
FEED_REPO="FreedomTechFeed/packages"
FEED_CHANNEL="${TOLLGATE_FEED_CHANNEL:-alpha}"
FEED_TAG_OVERRIDE="${TOLLGATE_FEED_RELEASE_TAG:-}"
LIST_RELEASES=0
POSITIONAL=()

usage() {
    sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
    cat <<'USAGE'

Options:
  --tag <tag>        Install an exact feed release tag (e.g. v0.6.0-alpha2-pre12).
                     Same as TOLLGATE_FEED_RELEASE_TAG=<tag>.
  --channel <name>   Newest feed release in a channel: alpha (default, newest
                     pre-release), beta, stable, or any.
  --list             List recent feed releases (newest first) and exit.
  -h, --help         Show this help.
USAGE
}

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)     FEED_TAG_OVERRIDE="${2:-}"; shift 2 ;;
        --channel) FEED_CHANNEL="${2:-}"; shift 2 ;;
        --list)    LIST_RELEASES=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *)         POSITIONAL+=("$1"); shift ;;
    esac
done

# gh_api_json <url> — curl the GitHub API, optionally authenticated via
# GITHUB_TOKEN (raising the 60/hr unauthenticated limit when present).
gh_api_json() {
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        curl -fsSL --retry 2 -H "Authorization: Bearer ${GITHUB_TOKEN}" "$1"
    else
        curl -fsSL --retry 2 "$1"
    fi
}

# feed_releases_json — the feed repo's recent releases (JSON array).
feed_releases_json() {
    gh_api_json "https://api.github.com/repos/${FEED_REPO}/releases?per_page=100"
}

# print_feed_releases — tag + published date, newest first.
print_feed_releases() {
    feed_releases_json | python3 -c '
import sys, json
try:
    rel = json.load(sys.stdin)
except Exception:
    sys.exit(1)
rel.sort(key=lambda r: r.get("published_at") or r.get("created_at") or "", reverse=True)
for r in rel:
    print("  %-30s %s" % (r.get("tag_name", ""), (r.get("published_at") or "")[:10]))
'
}

# resolve_feed_tag — the newest tag in FEED_CHANNEL, or empty if it cannot be
# resolved (the caller then falls back to the installer's compiled pin).
resolve_feed_tag() {
    if [ -n "${FEED_TAG_OVERRIDE}" ]; then
        printf '%s' "${FEED_TAG_OVERRIDE}"
        return 0
    fi
    feed_releases_json | FEED_CHANNEL="${FEED_CHANNEL}" python3 -c '
import sys, json, os, re
ch = os.environ.get("FEED_CHANNEL", "alpha").lower()
try:
    rel = json.load(sys.stdin)
except Exception:
    sys.exit(1)
if not isinstance(rel, list):
    sys.exit(1)
def is_pre(t):
    return bool(re.search(r"(pre|alpha|beta|rc)", t, re.I))
def ok(t):
    if ch in ("any", "latest", ""):
        return True
    if ch == "alpha":
        return is_pre(t)
    if ch == "beta":
        return bool(re.search(r"(beta|rc)", t, re.I))
    if ch == "stable":
        return not is_pre(t)
    return True
c = [r for r in rel if ok(r.get("tag_name", ""))]
c.sort(key=lambda r: r.get("published_at") or r.get("created_at") or "", reverse=True)
print(c[0]["tag_name"] if c else "")
'
}

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

echo "Detected platform: ${PLATFORM}"

# --- feed release resolution ----------------------------------------------
if [ "${LIST_RELEASES}" = 1 ]; then
    echo "Feed releases (${FEED_REPO}), newest first:"
    print_feed_releases || echo "  (could not fetch releases)" >&2
    exit 0
fi

RESOLVED_FEED_TAG="$(resolve_feed_tag 2>/dev/null || true)"
if [ -n "${RESOLVED_FEED_TAG}" ]; then
    export TOLLGATE_FEED_RELEASE_TAG="${RESOLVED_FEED_TAG}"
    echo "Feed release: ${RESOLVED_FEED_TAG} (channel ${FEED_CHANNEL})"
elif [ -n "${FEED_TAG_OVERRIDE}" ]; then
    export TOLLGATE_FEED_RELEASE_TAG="${FEED_TAG_OVERRIDE}"
    echo "Feed release: ${FEED_TAG_OVERRIDE} (explicit)"
else
    echo "Feed release: could not resolve channel '${FEED_CHANNEL}' from ${FEED_REPO}; using the installer's pinned default" >&2
fi

# --- 2. find + download binary -----------------------------------------------
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

# Show the installer build + the exact feed release/feed it will install, from
# /api/config. Older installer binaries lack the endpoint → say so, don't fail.
print_config() {
    local cfg
    cfg="$(curl -s --max-time 5 "http://127.0.0.1:${PORT}/api/config" 2>/dev/null || true)"
    if [ -z "${cfg}" ] || ! printf '%s' "${cfg}" | grep -q '"feed_release_tag"'; then
        echo "Version info: /api/config unavailable on this installer build"
        return 0
    fi
    printf '%s' "${cfg}" | python3 -c '
import sys, json
try:
    c = json.load(sys.stdin)
except Exception:
    sys.exit(0)
mod = (" module " + c["feed_module_pin7"]) if c.get("feed_module_pin7") else ""
print("Installing: %s %s%s" % (c.get("feed_repo", "?"), c.get("feed_release_tag", "?"), mod))
pkg = c.get("feed_pkg_version", "?")
err = ("  [" + c["feed_pin_error"] + "]") if c.get("feed_pin_error") else ""
print("            package %s%s" % (pkg, err))
print("            installer %s (%s)" % (c.get("installer_version", "?"), c.get("installer_commit", "?")))
' 2>/dev/null || true
}
print_config

# --- 5. optional headless deploy --------------------------------------------
ROUTER_IP="${POSITIONAL[0]:-}"
ROUTER_PASS="${POSITIONAL[1]:-}"
LNURL="${POSITIONAL[2]:-}"

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
SEEN_FILE="$(mktemp /tmp/tollgate-seen.XXXXXX)"
STATUS_FILE="$(mktemp /tmp/tollgate-status.XXXXXX)"

# Print the deploy state + step summary, and echo provenance lines (package
# source + installed build) exactly once as they appear in the job log.
print_status() {
    printf '%s' "$1" > "${STATUS_FILE}"
    python3 - "${SEEN_FILE}" "${STATUS_FILE}" <<'PY'
import sys, json
seen_path, status_path = sys.argv[1], sys.argv[2]
try:
    with open(status_path) as fh:
        data = json.load(fh)
except Exception:
    print("  (status unavailable)")
    sys.exit(0)

state = data.get("status", "")
steps = data.get("steps", []) or []
summary = ", ".join(
    f"{s.get('desc') or s.get('name') or '?'}:{s.get('status', '?')}" for s in steps
)
print(f"  {state}: {summary}")

try:
    with open(seen_path) as fh:
        seen = set(fh.read().splitlines())
except FileNotFoundError:
    seen = set()

markers = ("tollgate-wrt source", "Installed tollgate-wrt build")
try:
    with open(seen_path, "a") as fh:
        for entry in data.get("logs", []) or []:
            msg = entry.get("msg", "")
            if any(m in msg for m in markers) and msg not in seen:
                print(f"    | {msg}")
                seen.add(msg)
                fh.write(msg + "\n")
except Exception:
    pass
PY
}

while true; do
    STATUS="$(curl -s "http://127.0.0.1:${PORT}/api/status/${JOB_ID}")"
    STATE="$(echo "${STATUS}" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("status",""))' 2>/dev/null || true)"
    print_status "${STATUS}"
    if [ "${STATE}" = "done" ]; then
        echo "=== DEPLOY COMPLETE ==="
        echo
        echo "=== package provenance ==="
        printf '%s' "${STATUS}" > "${STATUS_FILE}"
        python3 - "${STATUS_FILE}" <<'PY'
import sys, json
try:
    with open(sys.argv[1]) as fh:
        data = json.load(fh)
except Exception:
    sys.exit(0)
for entry in data.get("logs", []) or []:
    msg = entry.get("msg", "")
    if "tollgate-wrt source" in msg or "Installed tollgate-wrt build" in msg:
        print(f"  {msg}")
step = next((s for s in data.get("steps", []) or []
             if "Installing tollgate-wrt" in (s.get("desc") or "")), None)
if step and step.get("detail"):
    print(f"  {step['detail']}")
PY
        break
    elif [ "${STATE}" = "failed" ] || [ "${STATE}" = "error" ]; then
        echo "=== DEPLOY FAILED ===" >&2
        echo "${STATUS}" | python3 -m json.tool >&2
        rm -f "${SEEN_FILE}" "${STATUS_FILE}"
        exit 1
    fi
    sleep 3
done
rm -f "${SEEN_FILE}" "${STATUS_FILE}"

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
