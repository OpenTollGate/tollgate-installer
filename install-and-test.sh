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
#
#  Runs on macOS (Intel + Apple Silicon) and Linux. Both are supported as
#  HOSTS: the installer only has to reach the router over the network, it
#  does not need to be Linux and does not act as a gateway. Every JSON field
#  this script reads is parsed with awk/sed, so a stock macOS without Xcode
#  Command Line Tools (i.e. without python3) still completes a headless
#  deploy; python3, when present, is used only to prettify output.
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
# Filled in by preflight(). python3 is optional; ssh/sshpass are only needed
# for the step-6 router verification.
PYTHON3=""
SSH_BIN=""
SSHPASS_BIN=""

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

# --- 0. preflight ----------------------------------------------------------
# Everything the script actually needs, checked up front with the fix for each,
# instead of failing deep inside the run. curl/mktemp/bash abort; python3, ssh
# and sshpass only degrade individual steps.

note() { printf '  ! %s\n' "$1" >&2; }

preflight() {
    local missing=""

    # bash >= 3.2: macOS ships 3.2.57. The script uses arrays, `+=`, and
    # `set -o pipefail`, all of which predate 3.2.
    if [ "${BASH_VERSINFO[0]:-0}" -lt 3 ] ||
       { [ "${BASH_VERSINFO[0]:-0}" -eq 3 ] && [ "${BASH_VERSINFO[1]:-0}" -lt 2 ]; }; then
        missing="${missing} bash(>=3.2)"
    fi
    command -v curl   >/dev/null 2>&1 || missing="${missing} curl"
    command -v mktemp >/dev/null 2>&1 || missing="${missing} mktemp"
    if [ -n "${missing}" ]; then
        echo "ERROR: missing required tool(s):${missing}" >&2
        echo "  Install, then re-run:" >&2
        echo "    curl:   macOS 'brew install curl' · Debian/Ubuntu 'apt install curl'" >&2
        echo "    bash:   macOS ships bash 3.2 — 'brew install bash' for a newer one" >&2
        echo "    mktemp: part of coreutils (Debian/Ubuntu: 'apt install coreutils')" >&2
        exit 1
    fi

    # python3 — OPTIONAL. Every JSON field this script must read (feed release
    # tag, job_id, deploy status, /api/config) is parsed with awk/sed below, so
    # a stock macOS without Xcode Command Line Tools can still run a headless
    # deploy. python3 is only used to prettify JSON for humans.
    if command -v python3 >/dev/null 2>&1; then
        PYTHON3="$(command -v python3)"
    fi
    if [ -z "${PYTHON3}" ]; then
        note "python3 not found — JSON is printed verbatim (not required: the deploy path parses with awk/sed)"
        if [ "${PLATFORM%%-*}" = "darwin" ]; then
            note "for prettier output: xcode-select --install"
        fi
    fi

    # ssh / sshpass — step-6 router verification only. The deploy itself talks
    # SSH from inside the Go binary (golang.org/x/crypto/ssh) and needs neither.
    SSH_BIN="$(command -v ssh 2>/dev/null || true)"
    SSHPASS_BIN="$(command -v sshpass 2>/dev/null || true)"
    if [ -z "${SSH_BIN}" ]; then
        note "ssh not found — router verification (step 6) will be skipped"
    elif [ -z "${SSHPASS_BIN}" ]; then
        note "sshpass not found — step 6 uses ssh's own SSH_ASKPASS (needs OpenSSH >= 8.4)"
    fi
}

# --- portable JSON helpers (no python3 needed) ------------------------------
# `tr -d '\n'` first: JSON strings cannot contain raw newlines, so collapsing
# the body to one line is lossless and lets one pattern set serve both the
# compact bodies the Go installer serves (json.Encoder) and the pretty-printed
# bodies the GitHub API returns.

# first_json_string <json> <field> — value of the FIRST `<field>: "value"` in
# the body. Top-level fields come first in both the installer's responses and
# the GitHub API's, which is what makes "first" the right one to take.
first_json_string() {
    printf '%s' "$1" | tr -d '\n' |
        grep -oE "\"$2\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        sed -n '1p' |
        sed -e 's/^"[^"]*"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' || true
}

# pretty_json — indent a JSON body when python3 is available, else echo it back
# verbatim. Buffers stdin so a failure cannot swallow the body.
pretty_json() {
    local body
    body="$(cat)"
    if [ -n "${PYTHON3}" ] && printf '%s' "${body}" | "${PYTHON3}" -m json.tool 2>/dev/null; then
        return 0
    fi
    printf '%s\n' "${body}"
}

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

# feed_awk is the python-free reader for that JSON (see the program text): the
# GitHub REST API pretty-prints with every release key indented by exactly four
# spaces, so the four-space anchor picks release keys and nothing from nested
# objects, arrays or release bodies.
FEED_AWK='
BEGIN { n = 0; tag = ""; pub = ""; crea = "" }
/^  \{[ \t]*$/ { commit(); next }
/^    "tag_name":[ \t]*/     { tag  = val() }
/^    "created_at":[ \t]*/   { crea = val() }
/^    "published_at":[ \t]*/ { pub  = val() }
function val(  p) { split($0, p, "\""); return p[4] }
function commit() {
    if (tag != "") { n++; tags[n] = tag; pubs[n] = pub; dates[n] = (pub != "" ? pub : crea) }
    tag = ""; pub = ""; crea = ""
}
function is_pre(t) { return (tolower(t) ~ /(pre|alpha|beta|rc)/) }
function chan_ok(t) {
    if (channel == "" || channel == "any" || channel == "latest") return 1
    if (channel == "alpha")  return is_pre(t)
    if (channel == "beta")   return (tolower(t) ~ /(beta|rc)/)
    if (channel == "stable") return (is_pre(t) ? 0 : 1)
    return 1
}
END {
    commit()
    # stable insertion sort, newest first (ISO-8601 UTC sorts lexicographically)
    for (i = 2; i <= n; i++) {
        kt = tags[i]; kd = dates[i]; kp = pubs[i]; j = i - 1
        while (j >= 1 && dates[j] < kd) {
            tags[j+1] = tags[j]; dates[j+1] = dates[j]; pubs[j+1] = pubs[j]
            j--
        }
        tags[j+1] = kt; dates[j+1] = kd; pubs[j+1] = kp
    }
    if (mode == "list") {
        for (i = 1; i <= n; i++) printf "  %-30s %s\n", tags[i], substr(pubs[i], 1, 10)
        exit
    }
    for (i = 1; i <= n; i++) {
        if (chan_ok(tags[i])) { printf "%s\n", tags[i]; exit }
    }
}
'

# print_feed_releases — tag + published date, newest first.
print_feed_releases() {
    feed_releases_json | awk -v mode=list "${FEED_AWK}"
}

# resolve_feed_tag — the newest tag in FEED_CHANNEL, or empty if it cannot be
# resolved (the caller then falls back to the installer's compiled pin).
resolve_feed_tag() {
    if [ -n "${FEED_TAG_OVERRIDE}" ]; then
        printf '%s' "${FEED_TAG_OVERRIDE}"
        return 0
    fi
    feed_releases_json | awk -v mode=newest -v channel="${FEED_CHANNEL}" "${FEED_AWK}"
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

preflight

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
    if [ -n "${PYTHON3}" ]; then
        printf '%s' "${cfg}" | "${PYTHON3}" -c '
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
        return 0
    fi
    # No python3: same four facts, read with sed/grep instead.
    local mod err
    mod="$(first_json_string "${cfg}" feed_module_pin7)"
    err="$(first_json_string "${cfg}" feed_pin_error)"
    printf 'Installing: %s %s%s\n' \
        "$(first_json_string "${cfg}" feed_repo)" \
        "$(first_json_string "${cfg}" feed_release_tag)" \
        "${mod:+ module ${mod}}"
    printf '            package %s%s\n' \
        "$(first_json_string "${cfg}" feed_pkg_version)" \
        "${err:+  [${err}]}"
    printf '            installer %s (%s)\n' \
        "$(first_json_string "${cfg}" installer_version)" \
        "$(first_json_string "${cfg}" installer_commit)"
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
curl -s "http://127.0.0.1:${PORT}/api/scan" | pretty_json | head -40 || true

echo
echo "=== Deploying TollGate to ${ROUTER_IP} ==="
DEPLOY_JSON="{\"ip\":\"${ROUTER_IP}\",\"password\":\"${ROUTER_PASS}\",\"lnurl\":\"${LNURL}\",\"mode\":\"wan\"}"
RESP="$(curl -s -X POST "http://127.0.0.1:${PORT}/api/deploy" \
    -H 'Content-Type: application/json' -d "${DEPLOY_JSON}")"
echo "Deploy response: ${RESP}"
# job_id is the one field the headless path cannot continue without, so it is
# read with sed/grep (never python3). python3 is only a fallback for exotic
# escaping in the response body.
JOB_ID="$(first_json_string "${RESP}" job_id)"
if [ -z "${JOB_ID}" ] && [ -n "${PYTHON3}" ]; then
    JOB_ID="$(printf '%s' "${RESP}" | "${PYTHON3}" -c 'import sys,json; print(json.load(sys.stdin).get("job_id",""))' 2>/dev/null || true)"
fi
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
#
# python3 is used when it is installed. Without it the same information is
# read out of the body with sed/grep (see steps_summary / provenance_lines),
# so the headless path does not depend on Xcode Command Line Tools.
print_status() {
    printf '%s' "$1" > "${STATUS_FILE}"
    if [ -n "${PYTHON3}" ]; then
        "${PYTHON3}" - "${SEEN_FILE}" "${STATUS_FILE}" <<'PY'
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
# The installer serialises the job log as "log"; "logs" is accepted too so the
# launcher keeps working if that tag ever changes.
try:
    with open(seen_path, "a") as fh:
        for entry in (data.get("log") or data.get("logs") or []):
            msg = entry.get("msg", "")
            if any(m in msg for m in markers) and msg not in seen:
                print(f"    | {msg}")
                seen.add(msg)
                fh.write(msg + "\n")
except Exception:
    pass
PY
        return 0
    fi
    printf '  %s: %s\n' "$(first_json_string "$1" status)" "$(steps_summary "$1")"
    provenance_lines "$1"
}

# steps_tail <status-json> — just the `"steps":[...]` slice of the body, so the
# job-level "status" field can never be mistaken for a step's status. Both the
# compact form the Go installer serves (`"steps":[{"name":...`) and the spaced
# form other encoders produce (`"steps": [ {...}`) are accepted.
steps_tail() {
    printf '%s' "$1" | tr -d '\n' |
        sed -e 's/.*"steps"[[:space:]]*:[[:space:]]*\[//' -e 's/\].*//'
}

# steps_awk reads the key/value pairs of one step object in the order the Go
# serializer writes them (name, desc, status, detail) and either prints the
# "desc:status" summary (-v summary=1) or the detail of the step whose desc
# contains detail_for.
STEPS_AWK='
{ key = $2; val = $4
  if (key == "name") { name = val }
  else if (key == "desc") {
      desc = val
      if (detail_for != "" && index(val, detail_for) > 0) { want = 1 }
  }
  else if (key == "status") {
      label = (desc != "" ? desc : (name != "" ? name : "?"))
      if (summary) out = (out == "" ? label ":" val : out ", " label ":" val)
      desc = ""; name = ""
  }
  else if (key == "detail" && want) { print "  " val; want = 0 }
}
END { if (summary && out != "") print out }
'

# steps_summary <status-json> — "desc:status" for every step, joined with ", ".
steps_summary() {
    steps_tail "$1" |
        grep -oE '"(name|desc|status)"[[:space:]]*:[[:space:]]*"[^"]*"' |
        awk -F'"' -v summary=1 "${STEPS_AWK}" || true
}

# provenance_lines <status-json> — the package-source / installed-build lines
# from the job log, each printed at most once (SEEN_FILE dedupes across polls).
provenance_lines() {
    printf '%s' "$1" | tr -d '\n' |
        grep -oE '"msg"[[:space:]]*:[[:space:]]*"[^"]*"' |
        sed -e 's/^"msg"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' |
        grep -F -e 'tollgate-wrt source' -e 'Installed tollgate-wrt build' |
        while IFS= read -r msg; do
            if ! grep -Fxq "${msg}" "${SEEN_FILE}" 2>/dev/null; then
                printf '    | %s\n' "${msg}"
                printf '%s\n' "${msg}" >> "${SEEN_FILE}"
            fi
        done || true
}

# provenance_all <status-json> — every provenance line in the final status,
# plus the detail of the "Installing tollgate-wrt" step.
provenance_all() {
    printf '%s' "$1" | tr -d '\n' |
        grep -oE '"msg"[[:space:]]*:[[:space:]]*"[^"]*"' |
        sed -e 's/^"msg"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' |
        grep -F -e 'tollgate-wrt source' -e 'Installed tollgate-wrt build' |
        sed 's/^/  /' || true
    steps_tail "$1" |
        grep -oE '"(name|desc|status|detail)"[[:space:]]*:[[:space:]]*"[^"]*"' |
        awk -F'"' -v detail_for="Installing tollgate-wrt" "${STEPS_AWK}" || true
}

while true; do
    STATUS="$(curl -s "http://127.0.0.1:${PORT}/api/status/${JOB_ID}")"
    STATE="$(first_json_string "${STATUS}" status)"
    print_status "${STATUS}"
    if [ "${STATE}" = "done" ]; then
        echo "=== DEPLOY COMPLETE ==="
        echo
        echo "=== package provenance ==="
        printf '%s' "${STATUS}" > "${STATUS_FILE}"
        if [ -n "${PYTHON3}" ]; then
            "${PYTHON3}" - "${STATUS_FILE}" <<'PY'
import sys, json
try:
    with open(sys.argv[1]) as fh:
        data = json.load(fh)
except Exception:
    sys.exit(0)
for entry in (data.get("log") or data.get("logs") or []):
    msg = entry.get("msg", "")
    if "tollgate-wrt source" in msg or "Installed tollgate-wrt build" in msg:
        print(f"  {msg}")
step = next((s for s in data.get("steps", []) or []
             if "Installing tollgate-wrt" in (s.get("desc") or "")), None)
if step and step.get("detail"):
    print(f"  {step['detail']}")
PY
        else
            provenance_all "${STATUS}"
        fi
        break
    elif [ "${STATE}" = "failed" ] || [ "${STATE}" = "error" ]; then
        echo "=== DEPLOY FAILED ===" >&2
        printf '%s' "${STATUS}" | pretty_json >&2
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
