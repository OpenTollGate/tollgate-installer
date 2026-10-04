#!/usr/bin/env bash
# ============================================================================
#  debug-router-upstream.sh — READ-ONLY diagnostics for a TollGate install that
#  stopped at "the router cannot use the internet" / STA association.
#
#  USAGE (one line, no continuations):
#      bash <(curl -fsSL <raw-url>/debug-router-upstream.sh) 192.168.1.1
#
#  Optional second argument: the SSH user (default root).
#
#  It CHANGES NOTHING on the router. Every remote command is read-only. The
#  password is prompted for and passed to ssh on stdin, never in argv.
#
#  It answers, in the order the installer's own gate answers it:
#    1. is there a default route, and does the first hop answer      (routing)
#    2. does dnsmasq resolve                                          (DNS)
#    3. do the UPSTREAM resolvers the router was handed answer
#    4. does a PUBLIC resolver answer
#    5. can the router actually fetch https (what the install needs)
#    6. is the STA uplink still configured at all
#  ...and prints a VERDICT naming which side owns the failure.
#
#  Exit code 0 always (it is a report, not a gate).
#
#  NOTE on busybox: `ip route show default` is NOT filtered on OpenWrt's
#  busybox ip — it returns the whole table. This script greps `ip route`
#  itself for that reason. (An earlier version trusted the filter, reported
#  "default route: present" on a router that had none, and pinged a gateway
#  named "br-lan".)
# ============================================================================
set -uo pipefail

IP="${1:-}"
USER="${2:-root}"
if [ -z "$IP" ]; then
    echo "usage: bash <(curl -fsSL <url>) <ROUTER_IP> [USER]" >&2
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
echo "uptime: $(cut -d' ' -f1 /proc/uptime 2>/dev/null)s  date: $(date -u 2>/dev/null)"

say "layer 2/3: addresses, routes"
have ip && { ip -4 addr show 2>/dev/null | grep -E '^[0-9]+:|inet '; echo "--- routes"; ip route 2>/dev/null; }

# busybox `ip route show default` is unfiltered on OpenWrt -> grep the table.
DROUTE=$(ip route 2>/dev/null | grep -E '^default' | head -1)
GW=$(printf '%s' "$DROUTE" | awk '{for(i=1;i<=NF;i++) if($i=="via") print $(i+1)}' | head -1)
if [ -n "$DROUTE" ]; then echo "default route: $DROUTE"; else echo "!! NO DEFAULT ROUTE"; fi
S defaultroute "$([ -n "$DROUTE" ] && echo yes || echo no)"
S gw "${GW:-none}"

say "uplink (wwan / STA) — is the installer's STA config still present?"
ubus call network.interface.wwan status 2>/dev/null | head -c 1200; echo
STA=$(uci -q show wireless 2>/dev/null | grep -c "mode='sta'")
if [ "$STA" -gt 0 ]; then echo "wireless: $STA STA (client) interface(s) configured"
else echo "!! wireless: NO STA/client interface configured — nothing can be an uplink"; fi
S sta "$([ "$STA" -gt 0 ] && echo yes || echo no)"
have iw && iw dev 2>/dev/null | grep -E 'Interface|ssid|channel'
have iwinfo && iwinfo 2>/dev/null | grep -E 'ESSID|Mode|Signal|Bit Rate' | head -8

say "reachability"
ping -c1 -W3 1.1.1.1 >/dev/null 2>&1 && { echo "ping 1.1.1.1: OK"; S ping OK; } || { echo "ping 1.1.1.1: FAIL"; S ping FAIL; }
if [ -n "$GW" ]; then
    ping -c1 -W3 "$GW" >/dev/null 2>&1 && { echo "ping first hop $GW: OK"; S pinggw OK; } || { echo "ping first hop $GW: FAIL"; S pinggw FAIL; }
else
    echo "ping first hop: n/a (no default route)"; S pinggw NA
fi

say "resolvers the router has"
echo "--- /etc/resolv.conf"; cat /etc/resolv.conf 2>/dev/null
echo "--- /tmp/resolv.conf.d/*"; cat /tmp/resolv.conf.d/* 2>/dev/null
UPS=$(grep -hE '^nameserver' /tmp/resolv.conf.d/* /etc/resolv.conf 2>/dev/null | awk '{print $2}' | sort -u | grep -v '^127\.' | grep -v '^::1')
if [ -n "$UPS" ]; then echo "upstream resolvers found: $UPS"; else echo "upstream resolvers found: NONE"; echo "!! dnsmasq has nothing to forward to"; fi
S ups "$(printf '%s' "$UPS" | tr '\n' ' ')"

say "name resolution"
if have nslookup; then
    NOUT=$(nslookup github.com 2>&1); NRC=$?
    printf '%s\n' "$NOUT" | tail -4
    if [ $NRC -eq 0 ] && ! printf '%s' "$NOUT" | grep -qi 'refused\|can.t find'; then echo "dnsmasq resolution: OK"; S dns_dnsmasq OK
    else echo "dnsmasq resolution: FAIL"; S dns_dnsmasq FAIL; fi
    UPSOK=FAIL; PUBSOK=FAIL
    for s in $UPS; do
        printf 'resolver %-18s : ' "$s"
        if nslookup github.com "$s" >/dev/null 2>&1; then echo OK; UPSOK=OK; else echo FAIL; fi
    done
    [ -z "$UPS" ] && UPSOK=NA
    S dns_upstream "$UPSOK"
    for s in 1.1.1.1 9.9.9.9; do
        printf 'resolver %-18s : ' "$s"
        if nslookup github.com "$s" >/dev/null 2>&1; then echo OK; PUBSOK=OK; else echo FAIL; fi
    done
    S dns_pub "$PUBSOK"
else
    echo "!! nslookup not present on this router"; S dns_dnsmasq NA; S dns_upstream NA; S dns_pub NA
fi

say "dnsmasq"
if pidof dnsmasq >/dev/null 2>&1 || pgrep -f dnsmasq >/dev/null 2>&1; then echo "dnsmasq: running"; S dnsmasq running
else echo "!! dnsmasq: NOT running"; S dnsmasq stopped; fi
uci -q show dhcp 2>/dev/null | grep -iE "server|address=|noresolv" | head -20
logread 2>/dev/null | grep -iE 'dnsmasq' | tail -6

say "https reachability (what the install actually needs)"
WOK=FAIL
for u in https://github.com https://raw.githubusercontent.com; do
    printf 'wget %-38s : ' "$u"
    if wget -q -T6 -O /dev/null "$u" 2>/dev/null; then echo OK; WOK=OK; else echo FAIL; fi
done
S wget "$WOK"

say "nat / firewall"
if have nft; then nft list ruleset 2>/dev/null | grep -c masquerade | sed 's/^/nft masquerade rules: /'
else iptables -t nat -S 2>/dev/null | grep -c MASQUERADE | sed 's/^/iptables MASQUERADE rules: /'; fi
uci -q get firewall.@zone[1].masq 2>/dev/null | sed 's/^/wan zone masq: /'

say "tollgate state (if installed)"
if [ -d /etc/tollgate ]; then
    ls /etc/tollgate 2>/dev/null
    head -c 400 /etc/tollgate/install.json 2>/dev/null; echo
    S tollgate installed
else
    echo "no /etc/tollgate (not installed)"
    S tollgate absent
fi
[ -x /etc/init.d/tollgate-wrt ] && /etc/init.d/tollgate-wrt status 2>&1 | head -3
if have netstat; then
    netstat -ltn 2>/dev/null | grep -E ':(2121|2050|2051|8090)' || echo ":2121/:2050/:2051/:8090: none listening"
elif have ss; then
    ss -ltn 2>/dev/null | grep -E ':(2121|2050|2051|8090)' || echo ":2121/:2050/:2051/:8090: none listening"
fi

say "recent router log (last lines)"
logread 2>/dev/null | tail -5
EOS

# --- run it over ssh --------------------------------------------------------
echo "Read-only diagnostics on $USER@$IP — nothing on the router is changed."
read -r -s -p "SSH password (leave blank if key auth): " PW; echo
SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=8)
if [ -n "${PW:-}" ]; then
    command -v sshpass >/dev/null 2>&1 || { echo "ERROR: sshpass needed for password auth, or use key auth" >&2; exit 2; }
    sshpass -p "$PW" ssh "${SSH_OPTS[@]}" "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1 | tee /tmp/.tg-dbg-out.txt
else
    ssh "${SSH_OPTS[@]}" "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1 | tee /tmp/.tg-dbg-out.txt
fi
rc=${PIPESTATUS[0]}
unset PW

OUT=$(grep -E '^@@' /tmp/.tg-dbg-out.txt 2>/dev/null)
get() { printf '%s\n' "$OUT" | sed -n "s/^@@$1=//p" | head -1; }

echo
if [ "$rc" -ne 0 ]; then
    echo "!! ssh to $USER@$IP failed (rc=$rc) — wrong address, wrong password, or the router is not reachable."
    exit 0
fi

# --- client-side check (runs on THIS machine) -------------------------------
echo
echo "== client-side resolution (from THIS machine: $HOSTNAME) =="
echo "   (decisive only if this machine is on the SAME upstream network as the router)"
CL_UP=$(get ups)
if command -v nslookup >/dev/null 2>&1; then
    printf 'this machine, default resolver : '; nslookup github.com >/dev/null 2>&1 && echo OK || echo FAIL
    for s in $CL_UP 1.1.1.1; do
        printf 'this machine, resolver %-9s : ' "$s"; nslookup github.com "$s" >/dev/null 2>&1 && echo OK || echo FAIL
    done
else
    printf 'this machine, default resolver : '; getent hosts github.com >/dev/null 2>&1 && echo OK || echo FAIL
fi

# --- verdict ----------------------------------------------------------------
DR=$(get defaultroute); ST=$(get sta); PG=$(get ping); UPGW=$(get pinggw)
UPS=$(get ups); DNSM=$(get dns_dnsmasq); DNSU=$(get dns_upstream); DNSP=$(get dns_pub)
WGET=$(get wget); DM=$(get dnsmasq); TG=$(get tollgate)

echo
echo "== VERDICT =="
if [ "$TG" = installed ]; then echo "  tollgate-wrt is INSTALLED on this router."; fi
if [ "$ST" = no ] && { [ "$DR" = no ] || [ "$PG" = FAIL ]; }; then
    echo "  NO STA UPLINK IS CONFIGURED — the installer's rollback removed it (that is its normal"
    echo "  failure path: it restores /etc/config/wireless from the pre-STA snapshot)."
    echo "  => nothing can reach the internet until an uplink exists again. Re-run the wizard and"
    echo "     re-enter the wifi credentials, or attach a WAN ethernet cable. Any diagnosis of the"
    echo "     upstream network below is only valid while an uplink is up."
fi
if [ "$DR" = no ]; then
    echo "  No default route => the router is not on any upstream. Fix the uplink first."
elif [ "$PG" = FAIL ] && [ "$UPGW" = FAIL ]; then
    echo "  Default route exists but the first hop does not answer => association/AP problem, not DNS."
    echo "  => router side (or the AP's side): the STA is associated but the upstream is not passing traffic."
elif [ "$UPS" = "" ]; then
    echo "  The router was handed NO upstream resolvers (dnsmasq: 'no servers found in"
    echo "  /tmp/resolv.conf.d/resolv.conf.auto'). => the UPSTREAM/DHCP side: the network it joined"
    echo "  gave it an address but no working DNS. Router-side is not at fault."
elif [ "$DNSM" = OK ] && [ "$WGET" = OK ]; then
    echo "  DNS and https both work from the router => the installer's upstream gate should PASS."
    echo "  If the wizard still refused here, that is an INSTALLER problem — report it with this output."
elif [ "$UPGW" = OK ] && [ "$DNSU" = FAIL ] && [ "$DNSP" = OK ]; then
    echo "  The upstream handed out resolvers it does not serve, but a public resolver answers."
    echo "  => fixable ON THE ROUTER (one command, changes state):"
    echo "     uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'; uci commit dhcp; /etc/init.d/dnsmasq restart"
elif [ "$DNSU" = FAIL ] && [ "$DNSP" = FAIL ]; then
    echo "  Name resolution fails through dnsmasq AND through 1.1.1.1/9.9.9.9 => the UPSTREAM NETWORK"
    echo "  blocks DNS (typical guest SSID / captive portal). Use a network whose DNS works."
    echo "  Nothing to fix on the router."
elif [ "$DM" = stopped ]; then
    echo "  dnsmasq is NOT running => router side: /etc/init.d/dnsmasq restart, then re-run this script."
elif [ "$WGET" = FAIL ] && [ "$DNSM" = OK ]; then
    echo "  Names resolve but https fails => routing/NAT/MTU or a TLS-intercepting captive portal,"
    echo "  not DNS. Check the masquerade rule above."
else
    echo "  Mixed signals — read the sections above."
fi
echo
echo "  Raw probe output kept at /tmp/.tg-dbg-out.txt (paste it back if you want it read)."
exit 0
