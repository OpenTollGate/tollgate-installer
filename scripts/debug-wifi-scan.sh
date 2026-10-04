#!/usr/bin/env bash
# ============================================================================
#  debug-wifi-scan.sh — READ-ONLY diagnostics for
#  "WiFi scan refused by every method the installer tried" and
#  "my network / my phone's hotspot does not appear in the list".
#
#  USAGE (one line, no continuations):
#      bash <(curl -fsSL <raw-url>/debug-wifi-scan.sh) 192.168.1.1
#      bash <(curl -fsSL <raw-url>/debug-wifi-scan.sh) 192.168.1.1 root c03rad0r
#
#  Argument 1: router address (required).
#  Argument 2: SSH user (default root).
#  Argument 3: optional SSID to look for in the results.
#
#  It CHANGES NOTHING on the router. Every remote command is read-only. The
#  password is prompted for and passed to ssh on stdin, never in argv.
#
#  WHY THIS EXISTS
#  --------------
#  The installer's scan chain (main.go scanChain()) is tried in this order:
#     [1] `iwinfo scan`                (no device argument; vendor builds)
#     [2] `iwinfo <dev> scan`          per netdev discovered by `iw dev`
#     [3] `iw dev <dev> scan`          per netdev discovered by `iw dev`
#     [4] `iwinfo wlan0` / `iwinfo wlan1`   (GL.iNet-era names)
#  The netdev list comes from `iw dev`. So a radio with NO interface on it at
#  that instant is never scanned, and the scan only ever covers the radios that
#  happened to have a netdev up — which is why the same router "sometimes"
#  shows networks and sometimes refuses.
#
#  Measured on OpenWrt mainline / mac80211 (2026-10):
#    * `iwinfo scan` with no device       -> prints Usage and exits 1 (needs a device)
#    * `iwinfo <phy>` (a PHY, not netdev) -> "Scanning not possible"
#    * `iwinfo <dev> scan` on an AP VAP   -> WORKS when the radio is idle
#    * `iw dev <dev> scan` while a STA is
#       associating or associated         -> "command failed: Resource busy (-16)"
#    * `iw dev <dev> scan` on some drivers
#       (mt76, AP VAP)                    -> "command failed: No such device (-19)"
#    * cells received with `ESSID: unknown`
#       (hidden SSID / undecodable beacon)
#                                          -> the installer parses 0 networks,
#                                             so real cells look like a refusal
#
#  Exit code 0 always (it is a report, not a gate).
# ============================================================================
set -uo pipefail

IP="${1:-}"
USER="${2:-root}"
FIND="${3:-}"
if [ -z "$IP" ]; then
    echo "usage: bash <(curl -fsSL <url>) <ROUTER_IP> [USER] [SSID_TO_FIND]" >&2
    exit 2
fi
command -v ssh >/dev/null || { echo "ERROR: ssh not found" >&2; exit 2; }

# --- probe set (runs ON THE ROUTER, read-only) ------------------------------
read -r -d '' PROBE <<'EOS'
say() { printf '\n== %s ==\n' "$1"; }
have() { command -v "$1" >/dev/null 2>&1; }
S() { printf '@@%s=%s\n' "$1" "$2"; }   # machine-readable sentinel

say "identity"
cat /tmp/sysinfo/model 2>/dev/null
. /etc/openwrt_release 2>/dev/null
echo "release: ${DISTRIB_ID:-?} ${DISTRIB_RELEASE:-?} ${DISTRIB_REVISION:-?}"
echo "uptime: $(cut -d' ' -f1 /proc/uptime 2>/dev/null)s"

say "what the installer can discover (it enumerates with 'iw dev')"
VAPS=""
PHYS=""
if have iw; then
    iw dev 2>/dev/null
    VAPS=$(iw dev 2>/dev/null | awk '/^[[:space:]]*Interface /{print $2}')
else
    echo "!! iw is NOT installed: discovery finds nothing, so methods [2] and [3] cannot run"
fi
[ -d /sys/class/ieee80211 ] && PHYS=$(ls /sys/class/ieee80211/ 2>/dev/null)
echo "radios (phys):  ${PHYS:-NONE}"
echo "interfaces:     ${VAPS:-NONE}"
S vaps "$(printf '%s' "$VAPS" | tr '\n' ',')"
S phys "$(printf '%s' "$PHYS" | tr '\n' ',')"

say "which radio has an interface at all (this is the 'sometimes' part)"
NORADIO=""
for p in $PHYS; do
    n=0
    for d in $VAPS; do case "$d" in "$p"*) n=$((n+1));; esac; done
    band=""
    case "$p" in
        phy0) band=$(uci -q get wireless.radio0.band 2>/dev/null);;
        phy1) band=$(uci -q get wireless.radio1.band 2>/dev/null);;
    esac
    if [ "$n" -gt 0 ]; then
        echo "$p (band ${band:-?}): $n interface(s) -> will be scanned"
    else
        echo "!! $p (band ${band:-?}): NO interface -> every SSID on this band is INVISIBLE to the scan"
        NORADIO="$NORADIO $p"
    fi
done
S noradio "$(printf '%s' "$NORADIO" | tr ' ' ',' | sed -e 's/^,//' -e 's/,$//')"

say "interface types (a scan needs an interface on a radio that is not busy)"
for d in $VAPS; do
    t=$(iw dev "$d" info 2>/dev/null | awk '/type/{print $2; exit}')
    echo "$d: type=${t:-unknown}"
done

say "wireless configuration (uci) — enabled radios, bands, existing STA"
uci -q show wireless 2>/dev/null | grep -E "\.(disabled|mode|ssid|band|channel|htmode|ifname|network|device)="

say "attempt [2] iwinfo <dev> scan"
CELLS=0; GOOD=""; BAD=""; HID=0; SSIDS=""
for d in $VAPS; do
    printf -- '--- iwinfo %s scan\n' "$d"
    OUT=$(iwinfo "$d" scan 2>&1)
    if printf '%s' "$OUT" | grep -qiE 'Scanning not possible|Usage|No such device|not found|Operation not permitted|Busy'; then
        echo "refused: $(printf '%s' "$OUT" | head -1)"
        BAD="$BAD $d"
        continue
    fi
    if [ -z "$(printf '%s' "$OUT" | tr -d '[:space:]')" ]; then
        echo "no output at all"
        BAD="$BAD $d"
        continue
    fi
    c=$(printf '%s' "$OUT" | grep -c '^[[:space:]]*Cell ')
    e=$(printf '%s' "$OUT" | grep -c 'ESSID:')
    h=$(printf '%s' "$OUT" | grep -ciE 'ESSID: *(unknown|"unknown"|"")')
    r=$((e - h))
    echo "cells=$c readable-ESSID=$r hidden/undecodable=$h"
    printf '%s' "$OUT" | grep -i 'ESSID:' | sed -e 's/^[[:space:]]*//' | sort -u | head -25
    CELLS=$((CELLS + c))
    if [ "$r" -gt 0 ]; then GOOD="$GOOD $d"; else BAD="$BAD $d"; HID=$((HID + h)); fi
    while IFS= read -r line; do
        s=$(printf '%s' "$line" | sed -e 's/.*ESSID:[[:space:]]*//' -e 's/^"//' -e 's/"$//')
        case "$s" in ""|unknown) ;; *) printf '@@ssid=%s\n' "$s";; esac
    done <<EOF2
$(printf '%s' "$OUT" | grep -i 'ESSID:')
EOF2
done
S cells "$CELLS"
S iwinfo_ok "$(printf '%s' "$GOOD" | tr ' ' ',' | sed -e 's/^,//' -e 's/,$//')"
S iwinfo_bad "$(printf '%s' "$BAD" | tr ' ' ',' | sed -e 's/^,//' -e 's/,$//')"
S hidden "$HID"

say "attempt [3] iw dev <dev> scan"
IWOK=""; IWERR=""
for d in $VAPS; do
    printf -- '--- iw dev %s scan\n' "$d"
    OUT=$(iw dev "$d" scan 2>&1)
    if printf '%s' "$OUT" | grep -qiE 'Resource busy|No such device|not supported|Operation not permitted'; then
        echo "refused: $(printf '%s' "$OUT" | head -1)"
        IWERR="$IWERR $d:$(printf '%s' "$OUT" | head -1 | tr ' ' '_')"
    else
        c=$(printf '%s' "$OUT" | grep -c '^BSS ')
        echo "cells=$c (first line: $(printf '%s' "$OUT" | head -1))"
        [ "$c" -gt 0 ] && IWOK="$IWOK $d"
    fi
done
S iw_ok "$(printf '%s' "$IWOK" | tr ' ' ',' | sed -e 's/^,//' -e 's/,$//')"
S iw_err "$(printf '%s' "$IWERR" | tr ' ' ',' | sed -e 's/^,//' -e 's/,$//')"

say "attempt [1]/[4] the installer's vendor fallbacks"
printf -- '--- iwinfo scan (no device): '; iwinfo scan 2>&1 | head -1
for w in wlan0 wlan1; do
    printf -- '--- iwinfo %s scan: ' "$w"; iwinfo "$w" scan 2>&1 | head -1
done

say "who is holding the radio (a busy radio refuses scans)"
printf 'wpa_supplicant processes: '; ps w 2>/dev/null | grep -c "[w]pa_supplicant"
printf 'hostapd processes:        '; ps w 2>/dev/null | grep -c "[h]ostapd"
printf 'STA (managed) interfaces: '; iw dev 2>/dev/null | grep -c "type managed"
S wpa "$(ps w 2>/dev/null | grep -c '[w]pa_supplicant')"

say "radio-related kernel log (last lines)"
logread 2>/dev/null | grep -iE 'scan|mt76|ath|wpa_supplicant|hostapd' | tail -8

say "wireless service + interface state"
ubus call network.wireless status 2>/dev/null | head -c 800; echo
[ -f /etc/config/wireless ] && echo "/etc/config/wireless: present ($(wc -c < /etc/config/wireless) bytes)" || echo "!! /etc/config/wireless: MISSING"
EOS

# --- run it over ssh --------------------------------------------------------
echo "Read-only WiFi-scan diagnostics on $USER@$IP — nothing on the router is changed."
read -r -s -p "SSH password (leave blank if key auth): " PW; echo
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=8)
if [ -n "${PW:-}" ]; then
    command -v sshpass >/dev/null 2>&1 || { echo "ERROR: sshpass needed for password auth, or use key auth" >&2; exit 2; }
    sshpass -p "$PW" ssh "${SSH_OPTS[@]}" "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1 | tee /tmp/.tg-wifiscan-out.txt
else
    ssh "${SSH_OPTS[@]}" "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1 | tee /tmp/.tg-wifiscan-out.txt
fi
rc=${PIPESTATUS[0]}
unset PW

OUT=$(grep -E '^@@' /tmp/.tg-wifiscan-out.txt 2>/dev/null)
get() { printf '%s\n' "$OUT" | sed -n "s/^@@$1=//p" | head -1; }
getssids() { printf '%s\n' "$OUT" | sed -n 's/^@@ssid=//p'; }

echo
if [ "$rc" -ne 0 ]; then
    echo "!! ssh to $USER@$IP failed (rc=$rc) — wrong address, wrong password, or the router is not reachable."
    exit 0
fi

VAPS=$(get vaps); PHYS=$(get phys); NORADIO=$(get noradio)
CELLS=$(get cells); IWOK=$(get iwinfo_ok); IWERR2=$(get iw_err)
HID=$(get hidden); WPA=$(get wpa)

echo "== SSIDs the router could actually see right now =="
N=0
while IFS= read -r s; do
    [ -n "$s" ] || continue
    N=$((N + 1)); printf '  %s\n' "$s"
done <<EOF3
$(getssids)
EOF3
[ "$N" -eq 0 ] && echo "  (none)"

if [ -n "$FIND" ]; then
    echo
    if printf '%s' "$(getssids)" | grep -qix -- "$FIND"; then
        echo "  \"$FIND\" IS in the scan results."
    else
        echo "  \"$FIND\" is NOT in the scan results — it is not being heard by this radio at this moment."
    fi
fi

echo
echo "== VERDICT =="
if [ -z "$VAPS" ]; then
    echo "  The router lists NO wireless interface at all ('iw dev' is empty), so there is nothing"
    echo "  to scan: this is an interface/radio state problem, not a scan problem."
    echo "  => bring the radios up and retry:  wifi up   (or: uci set wireless.radio0.disabled=0;"
    echo "     uci set wireless.radio1.disabled=0; uci commit wireless; wifi reload)"
    echo "  This is also exactly why the list 'sometimes' works: while the interfaces are absent"
    echo "  every scan form is refused, and it starts working once they exist."
elif [ -n "$NORADIO" ]; then
    echo "  Radio(s) with NO interface:$NORADIO"
    echo "  => every SSID on those bands is invisible to the installer's scan, because it only scans"
    echo "     the netdevs 'iw dev' lists. A phone hotspot on the missing band can never appear, and"
    echo "     the same router shows more or fewer networks depending on which radios are up at that"
    echo "     moment. Put an interface on the radio, then rescan:  wifi up"
elif [ "$CELLS" = "0" ] && [ -n "$IWERR2" ]; then
    echo "  Every scan form was refused. The reasons above are the driver's, not the installer's:"
    echo "    * 'Resource busy (-16)'  a station is associating or associated — the radio is mid-use."
    echo "    * 'No such device (-19)' this driver refuses a scan issued on an AP interface."
    echo "    * 'Usage:'               'iwinfo scan' with no device argument always does this."
    echo "  => this is a POINT-IN-TIME probe. Retry in a few seconds, or scan before anything"
    echo "     associates. If it never succeeds on this box, use a router whose driver allows it, and"
    echo "     report the output above."
elif [ "$N" -eq 0 ] && [ "$HID" != "0" ]; then
    echo "  Cells WERE received ($HID) but none carried a readable ESSID (all 'ESSID: unknown')."
    echo "  The installer's parser needs an ESSID, so it scores this as 0 networks and prints a"
    echo "  refusal. A hidden SSID — or a beacon this driver/iwinfo cannot decode — produces exactly"
    echo "  this. => check whether the network you are looking for broadcasts its SSID."
else
    echo "  The scan works: the router hears $N network(s) on: ${IWOK:-?}"
    [ "$CELLS" != "0" ] && echo "  cells received=$CELLS, hidden/undecodable=$HID, wpa_supplicant processes=$WPA"
    echo "  => if the installer still showed a refusal for the SAME moment, that is a bug worth"
    echo "     reporting with this output. If it was a different moment, it was the busy-radio race"
    echo "     described above (retry)."
fi
echo
echo "  Raw probe output kept at /tmp/.tg-wifiscan-out.txt (paste it back if you want it read)."
exit 0
