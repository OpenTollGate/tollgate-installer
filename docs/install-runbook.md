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
**Devices:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53`/`mediatek-filogic`);
GL-AR300M (`mips_24kc`/`ath79-generic` — flash-constrained, [§A.4](#a4-ar300m-flash-space-caveat)).

**Recordings (Review Club):** a narrated reel of every path — the wizard, the
merged manual install, and the post-install board + payment —
[watch all](https://blossom2.orangesync.tech/f0329daccf09d71413f86ada64bc9abc2ffa88bb0ea3f20fa9365ca24cb43d8e.html).

---

## 0. Resolve the release under test (first)

```sh
TAG=$(curl -fsSL 'https://api.github.com/repos/FreedomTechFeed/packages/releases?per_page=30' \
  | python3 -c 'import sys,json;print(max(json.load(sys.stdin), key=lambda r: r["published_at"])["tag_name"])')
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g'); echo "TAG=$TAG VER=$VER"
```

`jq` alternative: `... | jq -r 'max_by(.published_at).tag_name'`. Use `$TAG`/`$VER`
everywhere below — never paste a stale tag.

---

## 1. Path A — `tollgate-installer` (start here)

`tollgate-installer` (`OpenTollGate/tollgate-installer`) is a small Go binary that
serves a setup wizard at `:8099` and drives the whole install over SSH: flash →
root password → uplink → install → brand → portal → LNURL → health.

> **Watch (55 s, narrated):** the wizard walkthrough — [video](https://blossom2.orangesync.tech/f202527b2ab8909b89c20ce0014059d0d0b7d994661d4c3a1617bb1b1ca3ae27.mp4) · [captions](https://blossom2.orangesync.tech/133d3cf07954935e893197e4446784e96004d787ae69fe39d4edf4371b0f3f52.vtt) · [still](https://blossom2.orangesync.tech/95d129ee3028d94c4d6174eccd0cf8399d5cb6a8008ed49cc939b89b7353f14d.png). It shows the real wizard with a visible cursor, and names the mandatory fields (router, upstream, Lightning address).

> **Watch (94 s, narrated):** the wizard end-to-end — the real UI (mandatory fields named) plus the headless one-liner and the shared board + payment ending: [video](https://blossom2.orangesync.tech/37d821c79b89d776c5b16af5575324ba8b361928e9340b4afc34b487339db590.mp4) · [captions](https://blossom2.orangesync.tech/1ee12203c639fef974066a04d72c68902155df50a1069c2d41de889f580e5bc4.vtt).

**Interactive** (opens the wizard in your browser):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

**Headless** (no browser; full deploy + verification):

```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER_IP> '<ROUTER_PASSWORD>' <LIGHTNING_ADDRESS>
```

> **Watch (20 s, narrated):** the [headless install walkthrough](https://blossom2.orangesync.tech/b7f9c8a4cb9aeb4f477d4924c39ee250a82e918f7aba43ead389414ce3ff2105.mp4) · [captions](https://blossom2.orangesync.tech/8bba32cb270b77e012907abccd14f3b6be331d82ff831e18dd38c7e45e3ddd3f.vtt).

The wizard detects OS+arch, scans the LAN, and installs. The admin and
management-WiFi password fields are **optional** (under *advanced*) — leave them
blank to install the **`TollGate`** default and change it at first login.

**Get the current build.** The launcher resolves the newest installer for its
channel (`alpha` = newest pre-release). Pin it explicitly if you need a specific
one, and pin the release under test:

```sh
... install-and-test.sh --installer-tag v0.6.0-alpha2-rc23 --tag "$TAG"
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

> **Watch:** [manual install — `.apk` on OpenWrt 25.12](https://blossom2.orangesync.tech/ac19249bc124f46dcb9e494d7303e5c12d2493c1c2dac240468d5dd95a66f014.mp4) · [captions](https://blossom2.orangesync.tech/335ddc34ef9ebca253d81633e8945cb45404c3606696f162c0d6b90128a3e43e.vtt) · [`.ipk` on OpenWrt 24.10](https://blossom2.orangesync.tech/f8fdb4b6e589241bf9a28f41e8631dbd5e00d5cd5b587f289502d28f04619257.mp4) · [captions](https://blossom2.orangesync.tech/a5e976cfd6cb59a95c4aba93b302d6fadf7aeec990f6ac85e5b6416a732a02db.vtt).

> **Watch (287 s, narrated):** the full manual path — flash + uplink, the merged `.apk`/`.ipk` install, and the shared board + payment ending: [video](https://blossom2.orangesync.tech/c007a43623ca4fe18ffe9582be08a48d2d38794b78142f68a9629084c62d3659.mp4) · [captions](https://blossom2.orangesync.tech/1afec0cfee18915d4b5bcd68d3ffb5cc3edf8b911974750e19093cb217668cd6.vtt).

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

> **Watch (20 s, narrated):** the [offline-bundle walkthrough](https://blossom2.orangesync.tech/75800809a3be1911431a98795a16b72411fde5479b35ecfc1b31012838179942.mp4) · [captions](https://blossom2.orangesync.tech/b8459fc70f7156278dff0aaddb66467e04e10b850a39e67149a755e4b4a149ff.vtt).

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

> **Watch (160 s, narrated):** the board + payment — [the captive portal (Cashu + Lightning) and the admin board (dashboard → wallet → wifi → settings → Drain)](https://blossom2.orangesync.tech/5c2856066f3e2f6003bb3fcbca72725a5f751fe5851af7808dd5f59690b31f8a.mp4) · [captions](https://blossom2.orangesync.tech/0fef5e40ef971b1d1d07282e9c8d9a54b3d344a6ca5d57c13e5c0da4a3c060a1.vtt). The board is the **default face** at `https://tollgate-<4hex>.lan/`; LuCI is one **button** away.

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
tollgate version                                                    # confirm it matches $TAG/$VER
ps | grep -i tollgate; logread | grep -i tollgate                   # process + clean startup
ndsctl status; nft list ruleset | grep -i tollgate                  # nodogsplash active, no dup rules
cat /proc/sys/kernel/hostname                                       # TollGate-… hostname
```

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

> **Watch (36 s, narrated):** the [flash vanilla + WiFi uplink walkthrough](https://blossom2.orangesync.tech/afc2576f0be3091e5db6cbf37fa22ea9ce68be7ec7788763aa04ac18a948caec.mp4) · [captions](https://blossom2.orangesync.tech/613a3805dd2a54938192a9b5ec934512bc3cb26c263765aa3c63c2759411519a.vtt).

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
