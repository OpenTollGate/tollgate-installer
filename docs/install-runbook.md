# TollGate — Install Run Book (Review Club)

**Release under test:** the newest release of `FreedomTechFeed/packages`. Resolve
`$TAG`/`$VER` in §0 and use them everywhere — never paste a stale tag.

**Lanes** (test the one you were assigned; §4 is the shared acceptance check):
installer wizard (§2); manual `.apk` on 25.12 (§3); manual `.ipk` on 24.10 (§3);
offline bundle when the router has no uplink (§3.2).

**Devices:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53`/`mediatek-filogic`);
GL-AR300M (`mips_24kc`/`ath79-generic` — flash-constrained, §3.6).

> **`conwrt` (optional helper).** The `conwrt` tool
> (`git clone https://github.com/Amperstrand/conwrt`; runs on Linux/macOS) wraps
> the flashing and uplink steps below as `make` targets. Every step gives the raw
> command **and** its `make` equivalent — use whichever you prefer.

---

## 0. Resolve the release under test (first)

```sh
TAG=$(curl -fsSL 'https://api.github.com/repos/FreedomTechFeed/packages/releases?per_page=30' \
  | python3 -c 'import sys,json;print(max(json.load(sys.stdin), key=lambda r: r["published_at"])["tag_name"])')
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g'); echo "TAG=$TAG VER=$VER"
```

`jq` alternative: `... | jq -r 'max_by(.published_at).tag_name'`.

---

## 1. Prerequisites

- **Vanilla OpenWrt** — 25.12.x for the `.apk` lane, 24.10.x for the `.ipk` lane.
- SSH reachable; root password known (a fresh reset ships **empty**).
- **An uplink on the router** for the manual path (§1.2) — `apk`/`opkg update`
  fetch the OpenWrt deps. The wizard (§2) pre-stages them; the offline bundle
  (§3.2) needs no uplink at all.

### 1.1 Clean slate — flash vanilla OpenWrt

Take the profile from the official index for *your* device — never from memory
(`https://downloads.openwrt.org/releases/`).

| Device | Target | Profile | Notes |
|---|---|---|---|
| GL-MT3000 | `mediatek/filogic` | `glinet_gl-mt3000` | **no `factory` image** — use the OEM local-upgrade path or U-Boot `sysupgrade.bin` |
| GL-MT6000 | `mediatek/filogic` | `glinet_gl-mt6000` | has `factory.bin` — do **not** copy the MT3000 procedure |
| GL-AR300M | `ath79/generic` | `glinet_gl-ar300m-lite` / `-ar300m16` | there is no plain `glinet_gl-ar300m` profile |
| GL-AR300M (NAND) | `ath79/nand` | `glinet_gl-ar300m-nor` / `-nand` | **picking wrong is a brick** |

**Record which profile you used.**

**Flash it manually** (download the image, verify it, push it, `sysupgrade -n`):

```sh
RELEASE=25.12.5 ; TARGET=mediatek/filogic ; PROFILE=glinet_gl-mt3000 ; IP=192.168.1.1
BASE="https://downloads.openwrt.org/releases/$RELEASE/targets/$TARGET"
IMG="openwrt-$RELEASE-${TARGET//\//-}-$PROFILE-squashfs-sysupgrade.bin"

curl -fsSLO "$BASE/sha256sums"; curl -fsSLO "$BASE/$IMG"
grep -F "$IMG" sha256sums | sha256sum -c -          # verify BEFORE flashing

ssh root@$IP 'cat > /tmp/sysup.bin' < "$IMG"        # dropbear has no sftp-server — pipe, do not scp -O
ssh root@$IP "sha256sum /tmp/sysup.bin"             # must match the line you just verified
ssh root@$IP "sysupgrade -n /tmp/sysup.bin"         # -n = do NOT keep config (this is the clean slate)
# the router reboots to 192.168.1.1 with an EMPTY root password (~1-2 min)
```

> `sysupgrade -n` is the reset: it drops all config and packages. Use `-c` to
> **keep** config only when you deliberately want to preserve state.

With `conwrt`, the same flash is one step (it resolves the profile from `models/`):

```sh
make detect                                     # identify the connected router
make source-image MODEL=glinet-mt3000           # download + verify the image (RELEASE=25.12.5 to pin)
make flash MODEL=glinet-mt3000                  # source the image and run sysupgrade -n
# no SSH? recovery mode instead:
make recovery MODEL=glinet-mt3000 IFACE=enp0s31f6
make flash-uboot MODEL=glinet-mt3000            # force a U-Boot recovery flash
```

### 1.2 Give the router an uplink (WiFi STA) — **do this before §3**

The manual install runs `apk`/`opkg update`, so the router needs an uplink. Join
an upstream AP as a client and route it through the WAN zone:

```sh
# radio0 = 2.4 GHz, radio1 = 5 GHz on MT3000/MT6000 — use the one your AP is on.
RADIO=radio0 ; SSID='<upstream-ssid>' ; KEY='<upstream-key>'
uci set wireless.sta=wifi-iface
uci set wireless.sta.device="$RADIO"
uci set wireless.sta.mode=sta
uci set wireless.sta.ssid="$SSID"
uci set wireless.sta.key="$KEY"
uci set wireless.sta.encryption=psk2
uci set wireless.sta.network=wwan
uci set wireless.sta.disabled=0
uci set network.wwan=interface
uci set network.wwan.proto=dhcp
WANZONE=$(uci show firewall | sed -n 's/^firewall\.\(@zone\[[0-9]*\]\)\.name=.wan.$/\1/p' | head -1)
[ -n "$WANZONE" ] && uci add_list "$WANZONE.network=wwan"
uci commit wireless; uci commit network; uci commit firewall
wifi reload
ip route | grep default ; ping -c2 9.9.9.9      # confirm the uplink is up
```

`conwrt` equivalent (also installs an SSH key):

```sh
make sta SSID=<upstream-ssid> KEY=<upstream-key>            # 2.4 GHz
make sta SSID=<upstream-ssid> KEY=<upstream-key> BAND=5ghz  # 5 GHz
```

### 1.3 Which network is which (and what you can reach)

The install puts the router on **two bridges**, and **moves the wired LAN port
off `br-lan` onto `br-private`** — so your cable ends up on the management side.

| Bridge | Address | Who is on it | Can reach |
|---|---|---|---|
| **`br-lan`** — guest / captive | `192.168.1.1/24` | the open guest SSIDs | the captive portal (`:2050`/`:2051`) + the internet. The admin ports are **dropped** here |
| **`br-private`** — management | derived: `192.168.2.1`, or a `10.x` if your LAN collides | the wired LAN ports + the private (management) SSIDs | the **board** (`:8080`/`:443`), **LuCI** (`:8090`/`:8443`), and **SSH** |

So a **guest** on `br-lan` pays and gets internet but cannot reach the admin UIs;
**you**, on the wire or the private SSID (`br-private`), can. Your management
address is `uci get network.private.ipaddr`.

---

## 2. Path A — installer wizard

**Interactive** (serves the UI on `:8099`):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

**Headless** (no browser; full deploy + verification):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER_IP> '<ROUTER_PASSWORD>' <LIGHTNING_ADDRESS>
```

**Pin the release:** add `--tag "$TAG"` — a pass with no pinned tag is not
evidence about `$TAG`. Other flags: `--channel`, `--list`, `--bin <path>`,
`--trust-host-key`.

The wizard detects OS+arch, downloads `tollgate-installer`, serves it, scans the
LAN, and runs the deploy steps in order:
`verify → stage → flash → firmware → password → upstream → install → brand → portal → lnurl → services → health`.
The password fields are **optional** (advanced) — blank installs the `TollGate`
default (§3.1). The wizard also configures the uplink and name resolution for you.

---

## 3. Path B — manual install on vanilla OpenWrt

### 3.1 Passwords: `TollGate` is installed by default

The package installs a **fixed default, `TollGate`**, for the admin board and the
management WiFi when you have not chosen one, and marks the admin credential
*provisional* — the board **forces a change at first login**, so a fresh install
is never locked out. To choose your own up front instead:

```sh
passwd root                                            # admin password
tollgate network private set-password '<passphrase>'   # management WiFi
```

A value you set is preserved across reinstalls; skip it and you sign in with
`TollGate`.

### 3.2 Direct file install (both lanes)

There is **no feed to add** — `FreedomTechFeed/packages` publishes release assets
only (no `APKINDEX`/`Packages.gz`). Install the release **file by path**, exactly
as the wizard does.

> ⚠️ **The install drops your SSH session.** The `postinst` restarts the network
> and **moves the wired LAN port off `br-lan` (`192.168.1.1`) onto `br-private`**
> (the derived management subnet). Reconnect at the new address
> (`uci get network.private.ipaddr`) or over the router's WiFi at `192.168.1.1`.
> Running the block under `screen`/`tmux` survives the drop.

```sh
# Carry $TAG from §0 to the router (which has no curl/python3). An interactive
# shell does not abort on ${TAG:?}, so guard explicitly.
[ -n "${TAG:-}" ] || { echo "ERROR: set TAG (see §0)"; exit 1; }
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g')
BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"
ARCH=$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release | tr -d "\"'")
if command -v apk >/dev/null 2>&1; then EXT=apk; PM="apk add --allow-untrusted --force-overwrite"; apk update
else EXT=ipk; PM="opkg install"; opkg update; fi
FILE="tollgate-wrt_${VER}_${ARCH}.${EXT}"; cd /tmp

# A fresh image has no clock/ca-bundle, so HTTPS fails ("SSL verify error"). The
# SHA256SUMS gate below carries integrity, so skip TLS for the FETCH only.
wget -q --no-check-certificate "$BASE/SHA256SUMS" -O SHA256SUMS || { echo "ERROR: cannot fetch SHA256SUMS (TAG?)" >&2; exit 1; }
[ -s SHA256SUMS ] || { echo "ERROR: SHA256SUMS empty — wrong TAG?" >&2; exit 1; }
wget -q --no-check-certificate "$BASE/$FILE" -O "$FILE" || { echo "ERROR: cannot fetch $FILE (TAG/arch?)" >&2; exit 1; }
grep " $FILE\$" SHA256SUMS | sha256sum -c - || { echo "CHECKSUM FAILED — do not install"; exit 1; }
$PM "/tmp/$FILE"
```

`--allow-untrusted` is required on the apk lane — the packages carry **no
apk/opkg signature** (only the manifest is signed), so the `SHA256SUMS` check
above *is* the integrity gate. The wizard also passes the opkg force flags
(`--force-downgrade --force-reinstall --force-overwrite --force-depends`); on a
vanilla router **try it plain first** and add flags one at a time.

**No uplink on the router?** Use the release's **offline bundle**: on a machine
with internet, `curl -fsSL <release>/tollgate-wrt_${VER}_${ARCH}-offline.tar.gz |
tar -xz`, then `./install-offline.sh <ROUTER_IP> '<ROUTER_PASSWORD>'` from that
directory. It stages the package plus its full dependency closure and installs
offline.

### 3.3 Names you can use (Path B)

The router answers to its **hostname** on the `lan` domain — `<hostname>.lan`,
i.e. `tollgate-<4hex>.lan` (find it with `cat /proc/sys/kernel/hostname`). It
resolves to the **management** address (§1.3), so the board and LuCI are at
`http://<hostname>.lan:8080` and `http://<hostname>.lan:8090`. This works on
every path.

The fixed alias **`tollgate.lan`** is set by the **wizard**, not the package, so
a manual install has neither it nor a `gatewaydomainname` (the package removes
that on purpose — nodogsplash would redirect-loop, #428). Add the alias
yourself, after reconnecting (§3.2):

```sh
IP=$(uci -q get network.private.ipaddr); IP=${IP%%/*}; [ -n "$IP" ] || IP=192.168.1.1
grep -q tollgate.lan /etc/hosts || echo "$IP tollgate.lan tollgate.local" >> /etc/hosts
for a in $(uci -q get dhcp.@dnsmasq[0].address); do case "$a" in /tollgate.lan*) uci -q del_list dhcp.@dnsmasq[0].address="$a";; esac; done
uci -q add_list dhcp.@dnsmasq[0].address="/tollgate.lan/$IP"
uci commit dhcp; /etc/init.d/dnsmasq restart
```

### 3.4 Optional: set your own management-WiFi passphrase

```sh
read -r -s -p 'private WiFi passphrase (min 8 chars): ' WIFIPW; echo
tollgate network private set-password "$WIFIPW"; unset WIFIPW
```

### 3.5 If your device is on the management subnet

The management network is the /24 next to the LAN (`192.168.2.0/24` for a default
LAN). If your laptop/phone is also on `192.168.x.0/24`, two devices answer
`192.168.x.1` and your connectivity breaks. Reach the router on the **wire**
(`ssh -b <your-wired-ip> root@<x>.1`), or relocate:

```sh
uci set network.private.ipaddr=10.99.99.1; uci commit network; /etc/init.d/network restart
```

A reinstall re-derives this address (`tollgate-module-basic-go#906`).

### 3.6 AR300M flash-space caveat

The default payload (~20 MB) does **not** fit a 16 MB NOR device (~4.6 MB free
jffs2): expect `ENOSPC`; a tmpfs fallback is volatile. Record the exact variant.

---

## 4. Shared acceptance check (after ANY path)

The service is **`tollgate-wrt`**, the backend is **`:2121`**; the router has no
`curl` — use `uclient-fetch`.

```sh
/etc/init.d/tollgate-wrt status                 # "running: API :2121 …" + /var/run/tollgate.sock
uclient-fetch -q -O- http://127.0.0.1:2121/ 2>/dev/null | head -5   # backend live
uclient-fetch -q -O- http://127.0.0.1:2121/whoami 2>/dev/null       # answers while the wallet loads
uclient-fetch -q -O- http://127.0.0.1:2121/identity 2>/dev/null     # 404 unless a merchant key exists
tollgate version                                                    # confirm it matches $TAG/$VER
ps | grep -i tollgate; logread | grep -i tollgate                   # process + clean startup
ndsctl status; nft list ruleset | grep -i tollgate                  # nodogsplash active, no dup rules
cat /proc/sys/kernel/hostname                                       # TollGate-… hostname
```

Declare **pass** only if all hold. An install that completes but never starts is
a **fail** — attach `logread | grep -i tollgate`.

---

## 5. What ONLY the wizard does

A manual install is not crippled — the `postinst` runs the uci-defaults and
starts `tollgate-wrt`. The wizard additionally: flashes from stock GL.iNet
firmware and sets the root password + upstream/STA WAN; resolves `tollgate.lan`
(§3.3) and enforces the sha256 gate; configures LNURL; installs `mdnsd` for
`.local`; reads the version back; and, on the opkg lane, pre-installs
`nodogsplash`+`jq`. **Report a manual gap as exactly that** ("works manually but
X is missing") — not "the feature is broken".

---

## 6. Reporting

Use `FEEDBACK-TEMPLATE.md`. Record device + **variant**, OpenWrt version,
**`$TAG`/`$VER`**, the path (**A/B**), any exit code, the §4 output, and
pass/partial/fail. **Say which path you ran** — a Path A pass is not evidence
about Path B.

> Host-specific editions: [`runbook/README.md`](runbook/README.md).
