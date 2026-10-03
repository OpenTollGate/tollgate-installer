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
#  It CHANGES NOTHING. Every command is read-only. The password is prompted for
#  and passed to ssh via stdin, never on the command line.
#
#  What it answers, in the order the installer's own gate answers it:
#    1. does the router reach the upstream at layer 3   (ping)
#    2. does name resolution work through dnsmasq       (nslookup 127.0.0.1)
#    3. do the UPSTREAM resolvers answer                (nslookup <their IP>)
#    4. does a PUBLIC resolver answer                   (nslookup 1.1.1.1)
#    5. is it routing or DNS, and is it the router or the upstream network
#
#  Exit code 0 always (it is a report, not a gate).
# ============================================================================
set -uo pipefail

IP="${1:-}"
USER="${2:-root}"
if [ -z "$IP" ]; then
    echo "usage: bash <(curl -fsSL <url>) <ROUTER_IP> [USER]" >&2
    exit 2
fi

command -v ssh >/dev/null || { echo "ERROR: ssh not found" >&2; exit 2; }

# --- probe set (runs on the router, read-only) ------------------------------
read -r -d '' PROBE <<'EOS'
say() { printf '\n== %s ==\n' "$1"; }
have() { command -v "$1" >/dev/null 2>&1; }

say "identity"
cat /tmp/sysinfo/model 2>/dev/null
. /etc/openwrt_release 2>/dev/null
echo "release: ${DISTRIB_ID:-?} ${DISTRIB_RELEASE:-?} ${DISTRIB_REVISION:-?}"
echo "uptime: $(cat /proc/uptime 2>/dev/null | cut -d' ' -f1)s  date: $(date -u 2>/dev/null)"

say "layer 2/3: addresses, routes"
have ip && { ip -4 addr show 2>/dev/null | grep -E '^[0-9]+:|inet '; ip route 2>/dev/null; }
[ -z "$(ip route show default 2>/dev/null)" ] && echo "!! NO DEFAULT ROUTE" || echo "default route: present"

say "uplink (wwan / STA)"
ubus call network.interface.wwan status 2>/dev/null | head -c 1200; echo
have iw && iw dev 2>/dev/null | grep -E 'Interface|ssid|channel'
have iwinfo && iwinfo 2>/dev/null | grep -E 'ESSID|Mode|Signal|Bit Rate' | head -8

say "reachability"
ping -c1 -W3 1.1.1.1 >/dev/null 2>&1 && echo "ping 1.1.1.1: OK" || echo "ping 1.1.1.1: FAIL"
GW=$(ip route show default 2>/dev/null | awk '{print $3}' | head -1)
[ -n "$GW" ] && { ping -c1 -W3 "$GW" >/dev/null 2>&1 && echo "ping default-gateway $GW: OK" || echo "ping default-gateway $GW: FAIL"; }

say "resolvers the router has"
echo "--- /etc/resolv.conf"; cat /etc/resolv.conf 2>/dev/null
echo "--- /tmp/resolv.conf.d/*"; cat /tmp/resolv.conf.d/* 2>/dev/null
UPS=$(grep -hE '^nameserver' /tmp/resolv.conf.d/* /etc/resolv.conf 2>/dev/null | awk '{print $2}' | sort -u | grep -v '^127\.' | grep -v '^::1')
echo "upstream resolvers found: ${UPS:-none}"
[ -z "$UPS" ] && echo "!! the router has NO upstream resolver — dnsmasq has nothing to forward to"

say "name resolution"
nslookup github.com 2>&1 | tail -4; echo "dnsmasq(github.com): $?"
for s in $UPS 1.1.1.1 9.9.9.9; do
    printf 'resolver %-16s : ' "$s"
    if nslookup github.com "$s" >/dev/null 2>&1; then echo OK; else echo FAIL; fi
done

say "dnsmasq"
pgrep -f dnsmasq >/dev/null 2>&1 && echo "dnsmasq: running" || echo "!! dnsmasq: NOT running"
uci -q show dhcp 2>/dev/null | grep -iE 'server|address|noresolv|dnsmasq' | head -12
logread 2>/dev/null | grep -iE 'dnsmasq' | tail -6

say "https reachability (what the install actually needs)"
for u in https://github.com https://raw.githubusercontent.com https://1.1.1.1; do
    printf 'wget %-34s : ' "$u"
    if wget -q -T6 -O /dev/null "$u" 2>/dev/null; then echo OK; else echo FAIL; fi
done

say "nat / firewall"
if have nft; then nft list ruleset 2>/dev/null | grep -c masquerade | sed 's/^/nft masquerade rules: /';
else iptables -t nat -S 2>/dev/null | grep -c MASQUERADE | sed 's/^/iptables MASQUERADE rules: /'; fi
uci -q get firewall.@zone[1].masq 2>/dev/null | sed 's/^/wan zone masq: /'

say "tollgate state (if installed)"
[ -d /etc/tollgate ] && { ls /etc/tollgate 2>/dev/null; head -c 400 /etc/tollgate/install.json 2>/dev/null; echo; } || echo "no /etc/tollgate (not installed)"
[ -x /etc/init.d/tollgate-wrt ] && /etc/init.d/tollgate-wrt status 2>&1 | head -3
netstat -ltn 2>/dev/null | grep -qE ':(2121|2050|2051|8090)' && netstat -ltn 2>/dev/null | grep -E ':(2121|2050|2051|8090)' || echo ":2121/:2050/:2051/:8090: none listening"

say "client-side resolution (what a guest sees)"
logread 2>/dev/null | tail -5
EOS

# --- run it over ssh --------------------------------------------------------
echo "Read-only diagnostics on $USER@$IP — nothing will be changed."
read -r -s -p "SSH password (leave blank if key auth): " PW; echo
if [ -n "$PW" ]; then
    command -v sshpass >/dev/null 2>&1 || { echo "ERROR: sshpass needed for password auth, or use key auth" >&2; exit 2; }
    sshpass -p "$PW" ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 \
        "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1
    rc=$?
else
    ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8 \
        "$USER@$IP" "$(printf '%s' "$PROBE")" 2>&1
    rc=$?
fi

echo
if [ $rc -ne 0 ]; then
    echo "!! ssh to $USER@$IP failed (rc=$rc) — wrong address, wrong password, or the router is not reachable."
    exit 0
fi
cat <<'HOWTO'

== how to read this ==
  ping FAIL                                  -> the router is not on the upstream at all (association/AP problem)
  ping OK + every resolver FAIL              -> the UPSTREAM NETWORK is the problem (guest network / captive portal
                                                blocking DNS). Use a network whose DNS works; nothing to fix on the router.
  ping OK + upstream resolvers FAIL + 1.1.1.1 OK
                                             -> the upstream handed out resolvers it does not serve.
                                                Fix on the router: uci add_list dhcp.@dnsmasq[0].server='1.1.1.1'; uci commit dhcp; /etc/init.d/dnsmasq restart
  ping OK + dnsmasq NOT running              -> router-side: /etc/init.d/dnsmasq restart, then re-run this script
  wget https FAIL while nslookup OK          -> routing/NAT or MTU, not DNS: check the masquerade rule above
HOWTO
exit 0
