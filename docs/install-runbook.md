# TollGate — Install Run Book (Review Club)

**Release under test:** the newest release of `FreedomTechFeed/packages`. Resolve
`$TAG`/`$VER` in §0 and use them everywhere — never paste a stale tag.

**Lanes** (test the one you were assigned; §4 is the shared acceptance check):
installer wizard (§2); manual `.apk` on 25.12 (§3); manual `.ipk` on 24.10 (§3);
offline bundle when the router has no uplink (§3.2).

**Devices:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53`/`mediatek-filogic`);
GL-AR300M (`mips_24kc`/`ath79-generic` — flash-constrained, §3.5).

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
- **An uplink on the router** for the manual path (§3) — `apk`/`opkg update`
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
default (§3.1).

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
> (the derived management subnet, usually `192.168.2.1`). Reconnect at the new
> address (`uci get network.private.ipaddr`) or over the router's WiFi at
> `192.168.1.1`. Running the block under `screen`/`tmux` survives the drop.

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

### 3.3 Optional: set your own management-WiFi passphrase

```sh
read -r -s -p 'private WiFi passphrase (min 8 chars): ' WIFIPW; echo
tollgate network private set-password "$WIFIPW"; unset WIFIPW
```

### 3.4 If your device is on the management subnet

The management network is the /24 next to the LAN (`192.168.2.0/24` for a default
LAN). If your laptop/phone is also on `192.168.x.0/24`, two devices answer
`192.168.x.1` and your connectivity breaks. Reach the router on the **wire**
(`ssh -b <your-wired-ip> root@<x>.1`), or relocate:

```sh
uci set network.private.ipaddr=10.99.99.1; uci commit network; /etc/init.d/network restart
```

A reinstall re-derives this address (`tollgate-module-basic-go#906`).

### 3.5 AR300M flash-space caveat

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
starts `tollgate-wrt`. The wizard additionally: resolves `tollgate.lan`; enforces
the sha256 gate; flashes from stock GL.iNet firmware and sets the root password +
upstream/STA WAN; configures LNURL; installs `mdnsd` for `.local`; reads the
version back; and, on the opkg lane, pre-installs `nodogsplash`+`jq`. **Report a
manual gap as exactly that** ("works manually but X is missing") — not "the
feature is broken".

---

## 6. Reporting

Use `FEEDBACK-TEMPLATE.md`. Record device + **variant**, OpenWrt version,
**`$TAG`/`$VER`**, the path (**A/B**), any exit code, the §4 output, and
pass/partial/fail. **Say which path you ran** — a Path A pass is not evidence
about Path B.

> Host-specific editions: [`runbook/README.md`](runbook/README.md).
