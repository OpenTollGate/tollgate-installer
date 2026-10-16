# TollGate — Install Run Book (Review Club)

Test the newest `FreedomTechFeed/packages` release on a GL.iNet router.
**The fastest path is the installer wizard (§1)** — it flashes the router,
sets the root password, brings up the uplink, installs TollGate and configures
name resolution. §2 covers a manual install if the wizard can't run.

**After install**, the router answers at **`https://tollgate-<4hex>.lan`** (its
hostname, e.g. `tollgate-e753.lan`) and the default admin **and** management-WiFi
password is **`TollGate`** — the board forces a change at first login.

## Contents

| § | What | When |
|---|---|---|
| [0](#0-resolve-the-release-under-test-first) | Resolve the release under test | always, first |
| [1](#1-path-a--tollgate-installer-start-here) | **tollgate-installer** — the fastest path | the default |
| [2](#2-path-b--manual-install-on-vanilla-openwrt) | Manual install (fallback): prerequisites · install · names | if the wizard can't run |
| [3](#3-access-after-install-either-path) | Access after install — bridges · names · password · cert | to reach the UI |
| [4](#4-shared-acceptance-check-after-any-path) | Acceptance check | after either path |
| [5](#5-reporting) | Reporting | when you're done |
| [A](#appendix-a--flash-vanilla-uplink-conwrt) | Appendix — flash vanilla, WiFi uplink, `conwrt` | reference |

**Lanes:** installer wizard ([§1](#1-path-a--tollgate-installer-start-here)) · manual `.apk` on 25.12 and `.ipk` on 24.10
([§2](#2-path-b--manual-install-on-vanilla-openwrt)) · offline bundle when the router has no uplink ([§2.2](#22-direct-file-install-both-lanes)).
**Devices:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53`/`mediatek-filogic` — the
**validated** pair, both upgraded in place on `v0.6.0-rc6`); GL-AR300M
(`mips_24kc`/`ath79-generic` or `mips_24kc`/`ath79-nand` — flash-constrained,
[§A.4](#a4-ar300m-flash-space-caveat)). `v0.6.0-rc6` widened the matrix to 8
pairs; see the feed's `TARGETS.md` for the full list and per-device validation
status.

**Recordings (Review Club):** three narrated happy-path films — install,
captive portal, and the config UI. Each runs against a real router.

**Watch:**
- **Install** — the wizard driving a real deploy (router picked from the list,
  your Lightning address, wired uplink): [video](https://blossom2.orangesync.tech/b685203b9744d67e175ed36093482a64ded2669721e90847555522b17ff49698.mp4) (1:21)
- **Captive portal** — a guest pays with e-cash and the gate opens:
  [video](https://blossom2.orangesync.tech/1961106d3a4e525b64c3155a9bc9062f5bd5f04239f90ff5ad45642fb736061f.mp4) (0:49)
- **Config UI** — the board: log in, the wallet holding the guest's payment, and
  Drain: [video](https://blossom2.orangesync.tech/b03c4017e19cc592d9d71de1d62877820b9b6947b8767df6f1c801ad227a692d.mp4) (1:09)

---

## 0. Resolve the release under test (first)

```sh
TAG=$(curl -fsSL 'https://api.github.com/repos/FreedomTechFeed/packages/releases?per_page=30' \
  | python3 -c 'import sys,json;print(max(json.load(sys.stdin), key=lambda r: r["published_at"])["tag_name"])')
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g'); echo "TAG=$TAG VER=$VER"
```

`jq` alternative: `... | jq -r 'max_by(.published_at).tag_name'`. Use `$TAG`/`$VER`
everywhere below — never paste a stale tag.

From `v0.6.0-rc6` the release assets are named per **`(arch, target)`**
(`tollgate-wrt_${VER}_${ARCH}-${TARGET}.${EXT}`), not per arch alone. Always
resolve **both** on the router — `DISTRIB_ARCH` *and* `DISTRIB_TARGET`
(`/` → `-`) — as §2.2 does. An arch-only name 404s.

---

## 1. Path A — `tollgate-installer` (start here)

`tollgate-installer` (`OpenTollGate/tollgate-installer`) is a small Go binary that
serves a setup wizard at `:8099` and drives the whole install over SSH: flash →
root password → uplink → install → brand → portal → LNURL → health.

> **Watch (1:21, narrated):** the wizard driving a **real deploy** — [video](https://blossom2.orangesync.tech/b685203b9744d67e175ed36093482a64ded2669721e90847555522b17ff49698.mp4). It picks the router from the dropdown, names the mandatory fields (router, upstream, Lightning address), and runs the install to a healthy merchant.

**Interactive** (opens the wizard in your browser):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

**Headless** (no browser; full deploy + verification):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER_IP> '<ROUTER_PASSWORD>' <LIGHTNING_ADDRESS>
```

> _(No recording for the headless path — install via the wizard above or the commands in this section.)_

The wizard detects OS+arch, scans the LAN, and installs. The admin and
management-WiFi password fields are **optional** (under *advanced*) — leave them
blank to install the **`TollGate`** default and change it at first login.

**Get the current build.** The launcher resolves the newest installer for its
channel (`alpha` = newest pre-release). Pin it explicitly if you need a specific
one, and pin the release under test:

```sh
... install-and-test.sh --installer-tag v0.6.0-alpha2-rc32 --tag "$TAG"
```

A pass with no `--tag "$TAG"` is not evidence about `$TAG`.

---

## 2. Path B — manual install on vanilla OpenWrt

### 2.1 Prerequisites — flash vanilla OpenWrt + give it an uplink

Do both **before** §2.2 (the direct install runs `apk`/`opkg update`). Full
commands, and the `conwrt` equivalents, are in **Appendix A**. In short:

- **Flash vanilla** (25.12.x for `.apk`, 24.10.x for `.ipk`) — §A.1.
- **Uplink** the router to a WiFi AP as a client — §A.2.

You can also **choose your own passwords up front** — a value you set is
preserved across reinstalls and never overwritten; skip it and the **`TollGate`**
default is installed (§3):

```sh
passwd root                                            # admin password
tollgate network private set-password '<passphrase>'   # management WiFi (after install)
```

### 2.2 Direct file install (both lanes)

There is **no feed to add** — `FreedomTechFeed/packages` publishes release assets
only (no `APKINDEX`/`Packages.gz`). Install the release **file by path**, exactly
as the wizard does.

> _(No recording for the manual path — the wizard film above covers the happy path; the commands are below.)_


> ⚠️ **The install drops your SSH session.** The `postinst` restarts the network
> and **moves the wired LAN port off `br-lan` onto `br-private`** (§3). Reconnect
> at the new address (`uci get network.private.ipaddr`) or over the router's WiFi
> at `192.168.1.1`. Running the block under `screen`/`tmux` survives the drop.

```sh
# Carry $TAG from §0 to the router (which has no curl/python3). An interactive
# shell does not abort on ${TAG:?}, so guard explicitly.
[ -n "${TAG:-}" ] || { echo "ERROR: set TAG (see §0)"; exit 1; }
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g')
BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"
ARCH=$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release | tr -d "\"'")
# The asset name is TARGET-QUALIFIED: <arch>-<target>. mips_24kc ships both
# ath79-generic and ath79-nand, so the arch alone selects the wrong asset.
TARGET=$(sed -n 's/^DISTRIB_TARGET=//p' /etc/openwrt_release | tr -d "\"'" | tr / -)
[ -n "$ARCH" ] && [ -n "$TARGET" ] || { echo "ERROR: cannot read DISTRIB_ARCH/DISTRIB_TARGET" >&2; exit 1; }
if command -v apk >/dev/null 2>&1; then EXT=apk; PM="apk add --allow-untrusted --force-overwrite"; apk update
else EXT=ipk; PM="opkg install"; opkg update; fi
FILE="tollgate-wrt_${VER}_${ARCH}-${TARGET}.${EXT}"; cd /tmp

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
with internet, `curl -fsSL <release>/tollgate-wrt-${VER}-${ARCH}-${TARGET}-offline.tar.gz |
tar -xz`, then `./install-offline.sh <ROUTER_IP> '<ROUTER_PASSWORD>'` from that
directory. It stages the package plus its full dependency closure and installs
offline.

> _(No recording for the offline-bundle path; the commands are below.)_

### 2.3 Names you can use

The router answers to its **hostname** on the `lan` domain — `<hostname>.lan`,
i.e. `tollgate-<4hex>.lan` (find it with `cat /proc/sys/kernel/hostname`). It
resolves to the **management** address (§3), so the board and LuCI are at
`http://<hostname>.lan:8080` and `http://<hostname>.lan:8090`. This works on
every path.

The fixed alias **`tollgate.lan`** is set by the **wizard**, not the package, so
a manual install has neither it nor a `gatewaydomainname` (the package removes
that on purpose — nodogsplash would redirect-loop, #428). Add the alias
yourself, after reconnecting (§2.2):

```sh
IP=$(uci -q get network.private.ipaddr); IP=${IP%%/*}; [ -n "$IP" ] || IP=192.168.1.1
grep -q tollgate.lan /etc/hosts || echo "$IP tollgate.lan tollgate.local" >> /etc/hosts
for a in $(uci -q get dhcp.@dnsmasq[0].address); do case "$a" in /tollgate.lan*) uci -q del_list dhcp.@dnsmasq[0].address="$a";; esac; done
uci -q add_list dhcp.@dnsmasq[0].address="/tollgate.lan/$IP"
uci commit dhcp; /etc/init.d/dnsmasq restart
```

---

## 3. Access after install (either path)

### The two bridges — and what you can reach

The install puts the router on **two bridges**, and **moves the wired LAN port
off `br-lan` onto `br-private`** — so your cable ends up on the management side.

| Bridge | Address | Who is on it | Can reach |
|---|---|---|---|
| **`br-lan`** — guest / captive | `192.168.1.1/24` | the open guest SSIDs | the captive portal (`:2050`/`:2051`) + the internet. The admin ports are **dropped** here |
| **`br-private`** — management | derived: `192.168.2.1`, or a `10.x` if your LAN collides | the wired LAN ports + the private (management) SSIDs | the **board** (`:8080`/`:443`), **LuCI** (`:8090`/`:8443`), and **SSH** |

A **guest** on `br-lan` pays and gets internet but cannot reach the admin UIs;
**you**, on the wire or the private SSID (`br-private`), can. Your management
address is `uci get network.private.ipaddr`.

> **Watch (0:49 + 1:09, narrated):** the **captive portal** — a guest pays with e-cash and the gate opens: [video](https://blossom2.orangesync.tech/1961106d3a4e525b64c3155a9bc9062f5bd5f04239f90ff5ad45642fb736061f.mp4) — and the **config UI** (board): log in, the wallet holding the guest's payment, and Drain: [video](https://blossom2.orangesync.tech/b03c4017e19cc592d9d71de1d62877820b9b6947b8767df6f1c801ad227a692d.mp4). The board is the **default face** at `https://tollgate-<4hex>.lan/`; LuCI is one **button** away.

### The password

The default admin **and** management-WiFi password is **`TollGate`**. Sign in to
`https://tollgate-<4hex>.lan` with `root` / `TollGate`; the board **forces you to
choose your own** at first login. (If your SSH session is key-based — e.g. after
`make sta` installed a key — you won't be prompted for it, but the password is
still the one the board wants.)

### The certificate warning is expected

`https://tollgate-<4hex>.lan` presents a **self-signed** certificate — no public
CA issues certs for `.lan`, so the browser's "not secure / self-signed" warning
is correct, not a fault. On your own box you can add an exception, or import the
router's `/etc/tollgate/ssl/server.crt` into your trust store. For a managed
deployment, install your own CA-signed cert covering the name:

```sh
tollgate ssl apply <cert.pem> <key.pem>     # the module keeps an operator-installed covering identity
```

Do **not** "fix" it by disabling TLS (`tollgate ssl remove` serves the
root-capable board over plain HTTP) or by clicking through on a network you
don't control.

---

## 4. Shared acceptance check (after ANY path)

The service is **`tollgate-wrt`**, the backend is **`:2121`**; the router has no
`curl` — use `uclient-fetch`.

```sh
/etc/init.d/tollgate-wrt status                 # "running: API :2121 …" + /var/run/tollgate.sock
uclient-fetch -q -O- http://127.0.0.1:2121/ 2>/dev/null | head -5   # backend live
uclient-fetch -q -O- http://127.0.0.1:2121/whoami 2>/dev/null       # answers while the wallet loads
uclient-fetch -q -O- http://127.0.0.1:2121/identity 2>/dev/null     # 404 unless a merchant key exists
tollgate version                                                    # the MODULE build (see note)
ps | grep -i tollgate; logread | grep -i tollgate                   # process + clean startup
ndsctl status; nft list ruleset | grep -i tollgate                  # nodogsplash active, no dup rules
cat /proc/sys/kernel/hostname                                       # TollGate-… hostname
```

> **`tollgate version` is NOT the feed `$TAG`/`$VER`.** It reports the
> **module** build the package embeds (`version: v0.6.0-rc4-gcf7cad3`, commit
> `cf7cad34`), which the feed `PKG_SOURCE_TAG` pins and does **not** change per
> feed RC. Confirm the feed RC landed with the **package manager** instead:
> ```sh
> opkg list-installed | grep tollgate-wrt     # opkg lane → "tollgate-wrt - 0.6.0_rc6-r1"
> apk list -I 2>/dev/null | grep tollgate-wrt # apk lane  → "tollgate-wrt-0.6.0_rc6-r1 …"
> ```
> Observed on rc6 (2026-10-15): MT3000 (apk) + MT6000 (opkg) both report the
> package `0.6.0_rc6-r1` while `tollgate version` stays `v0.6.0-rc4-gcf7cad3`.

Declare **pass** only if all hold. An install that completes but never starts is
a **fail** — attach `logread | grep -i tollgate`.

---

## 5. Reporting

Use `FEEDBACK-TEMPLATE.md`. Record device + **variant**, OpenWrt version,
**`$TAG`/`$VER`**, the path (**A/B**), any exit code, the §4 output, and
pass/partial/fail. **Say which path you ran** — a Path A pass is not evidence
about Path B. Note the installer build (`--installer-tag`) too.

> Host-specific editions: [`runbook/README.md`](runbook/README.md).

---

## Appendix A — flash vanilla, uplink, `conwrt`

**`conwrt`** (`git clone https://github.com/Amperstrand/conwrt`; macOS/Linux) is
a router-flashing and device-configuration framework. It wraps the two steps
below as `make` targets, so you can skip pasting commands — but it is **optional**;
the raw commands are given too. Run `make help` for the full target list.

### A.1 Flash vanilla OpenWrt

Take the profile from the official index for *your* device — never from memory
(`https://downloads.openwrt.org/releases/`).

| Device | Target | Profile | Notes |
|---|---|---|---|
| GL-MT3000 | `mediatek/filogic` | `glinet_gl-mt3000` | **no `factory` image** — OEM local-upgrade or U-Boot `sysupgrade.bin` |
| GL-MT6000 | `mediatek/filogic` | `glinet_gl-mt6000` | has `factory.bin` — do **not** copy the MT3000 procedure |
| GL-AR300M | `ath79/generic` | `glinet_gl-ar300m-lite` / `-ar300m16` | there is no plain `glinet_gl-ar300m` profile |
| GL-AR300M (NAND) | `ath79/nand` | `glinet_gl-ar300m-nor` / `-nand` | **picking wrong is a brick** |

Manual (`sysupgrade -n`), and the `conwrt` equivalent:

```sh
RELEASE=25.12.5 ; TARGET=mediatek/filogic ; PROFILE=glinet_gl-mt3000 ; IP=192.168.1.1
BASE="https://downloads.openwrt.org/releases/$RELEASE/targets/$TARGET"
IMG="openwrt-$RELEASE-${TARGET//\//-}-$PROFILE-squashfs-sysupgrade.bin"
curl -fsSLO "$BASE/sha256sums"; curl -fsSLO "$BASE/$IMG"
grep -F "$IMG" sha256sums | sha256sum -c -          # verify BEFORE flashing
ssh root@$IP 'cat > /tmp/sysup.bin' < "$IMG"        # dropbear has no sftp-server — pipe, do not scp -O
ssh root@$IP "sha256sum /tmp/sysup.bin"             # must match the line you just verified
ssh root@$IP "sysupgrade -n /tmp/sysup.bin"         # -n = do NOT keep config (clean slate)

# conwrt:
make detect                                  # identify the connected router
make source-image MODEL=glinet-mt3000        # download + verify (RELEASE=25.12.5 to pin)
make flash MODEL=glinet-mt3000               # source + sysupgrade -n
make recovery MODEL=glinet-mt3000 IFACE=enp0s31f6   # no SSH? recovery mode
make flash-uboot MODEL=glinet-mt3000         # force a U-Boot recovery flash
```

`sysupgrade -n` is the reset (drops config and packages); `-c` keeps config when
you deliberately want to.

### A.2 WiFi uplink (repeater), before §2.2

The manual install runs `apk`/`opkg update`, so the router needs an uplink. Join
an upstream AP as a client and route it through the WAN zone:

> _(No recording for the vanilla-flash path; the commands are below.)_

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

# conwrt (also installs an SSH key):
make sta SSID=<upstream-ssid> KEY=<upstream-key>            # 2.4 GHz
make sta SSID=<upstream-ssid> KEY=<upstream-key> BAND=5ghz  # 5 GHz
```

### A.3 If your device is on the management subnet

The management network is the /24 next to the LAN (`192.168.2.0/24` for a
default LAN). If your laptop/phone is also on `192.168.x.0/24`, two devices
answer `192.168.x.1` and your connectivity breaks. Reach the router on the
**wire** (`ssh -b <your-wired-ip> root@<x>.1`), or relocate:

```sh
uci set network.private.ipaddr=10.99.99.1; uci commit network; /etc/init.d/network restart
```

A reinstall re-derives this address (`tollgate-module-basic-go#906`).

### A.4 AR300M flash-space caveat

The default payload (~20 MB) does **not** fit a 16 MB NOR device (~4.6 MB free
jffs2): expect `ENOSPC`; a tmpfs fallback is volatile. Record the exact variant.

### A.5 What ONLY the wizard does

A manual install is not crippled — the `postinst` runs the uci-defaults and
starts `tollgate-wrt`. The wizard additionally: flashes from stock GL.iNet
firmware and sets the root password + upstream/STA WAN; resolves `tollgate.lan`
(§2.3) and enforces the sha256 gate; configures LNURL; installs `mdnsd` for
`.local`; reads the version back; and, on the opkg lane, pre-installs
`nodogsplash`+`jq`. **Report a manual gap as exactly that** ("works manually but
X is missing") — not "the feature is broken".
