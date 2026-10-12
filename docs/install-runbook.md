# TollGate — Install Run Book (Review Club)

**Release under test:** the **latest release** of `FreedomTechFeed/packages` — the
release candidate, or the newest pre-release while still pre-testing.

> **Never hardcode a `preNN` in this run book.** Resolve the tag once at the top
> of every session (below) and use `$TAG` / `$VER` everywhere. A run book pinned
> to an old `preNN` is how a tester ends up installing a stale package (that
> exact mistake shipped pre26 to a bench that was believed to be on pre28).

**Devices in scope:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53` / `mediatek-filogic`), GL-AR300M (`mips_24kc` / `ath79-generic`)

## 0. Resolve the release under test (do this first)

```sh
# Newest release tag of the feed (the RC, or the newest pre-release while testing)
TAG=$(curl -fsSL 'https://api.github.com/repos/FreedomTechFeed/packages/releases?per_page=30' \
  | python3 -c 'import sys,json;print(max(json.load(sys.stdin), key=lambda r: r["published_at"])["tag_name"])')
# Package version = tag without the leading 'v', with '-' -> '_'
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g')
echo "TAG=$TAG  VER=$VER"
```

(`jq` equivalent if `python3` is unavailable:
`... | jq -r 'max_by(.published_at).tag_name'`.)

Everything below uses `$TAG` (release tag) and `$VER` (package version). Record
both in your feedback.

> **Why this run book exists.** Every review so far went through the installer
> wizard. If the wizard ever fails, or a user refuses to run a binary, the
> package must still install by hand on stock OpenWrt. That is the fallback of
> record, so it gets tested every release — not just the wizard.

---

## 0b. The two paths at a glance

| | Path A — installer wizard | Path B — manual install |
|---|---|---|
| **Entry point** | `install-and-test.sh`, UI on `:8099` | shell on the workstation + router |
| **Who it is for** | first-time users, review sessions | fallback; anyone avoiding a binary |
| **Proven?** | proven on earlier pre-releases | **no — this is what the session must prove** |
| **Internet needed on router?** | no (assets pre-staged on the workstation) | **yes** — the direct variants run `apk`/`opkg update` for the OpenWrt deps |

Both paths must end in the same observable state. **§4 is the shared acceptance
check** — run it after whichever path you used. A path only "passes" if §4 passes.

---

## 1. Prerequisites

### Hardware
| Device | Arch / target | Notes |
|---|---|---|
| GL.iNet MT3000 (Beryl AX) | `aarch64_cortex-a53` / `mediatek-filogic` | primary target |
| GL.iNet MT6000 (Flint 2) | `aarch64_cortex-a53` / `mediatek-filogic` | same arch pair as MT3000 |
| GL.iNet AR300M | `mips_24kc` / `ath79-generic` | **flash-constrained — read §3.3** |

> MT3000 and MT6000 share one arch pair, so a packaging pass on one implies the
> other. Keep both in the matrix anyway: **they differ in flash layout**, and the
> MT3000 publishes no `factory` image while the MT6000 does.

### Router state
- **Vanilla OpenWrt** — 25.12.x for the `.apk` lane, 24.10.x for the `.ipk` lane.
- SSH reachable, root password known (a freshly reset OpenWrt ships with an **empty** root password).
- **An uplink on the router** for the direct `.apk`/`.ipk` variants — `apk`/`opkg
  update` fetch `nodogsplash`/`jq`/`ca-bundle` from the OpenWrt repos. Path A
  pre-stages its assets on the workstation.

### 1.1 Clean slate — getting to vanilla OpenWrt

Take the profile from the official index for *your* device; never from memory.
Check the **current** release directories under
`https://downloads.openwrt.org/releases/` for the newest stable (`25.12.x`) and
oldstable (`24.10.x`) — do not trust a version quoted in a doc.

| Device | Target | Profile | Images published |
|---|---|---|---|
| GL-MT3000 | `mediatek/filogic` | `glinet_gl-mt3000` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` — **no `factory` image** |
| GL-MT6000 | `mediatek/filogic` | `glinet_gl-mt6000` | `squashfs-sysupgrade.bin`, `squashfs-factory.bin`, `initramfs-kernel.bin`, `preloader.bin`, `bl31-uboot.fip` |
| GL-AR300M | `ath79/generic` | `glinet_gl-ar300m-lite` or `glinet_gl-ar300m16` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` |
| GL-AR300M (NAND units) | `ath79/nand` | `glinet_gl-ar300m-nor` or `glinet_gl-ar300m-nand` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` |

> ⚠️ **There is no plain `glinet_gl-ar300m` profile** — only `-lite`,
> `-ar300m16` (ath79/**generic**) and `-nor`, `-nand` (ath79/**nand**). Picking
> wrong is a brick, not a misconfiguration. **Record which profile you used.**
> The installer resolves this itself by board name; a human flashing by hand must
> not guess.
>
> ⚠️ **MT3000 publishes no `factory` image.** Use the GL.iNet OEM local-upgrade
> path, or vendor U-Boot recovery with the `sysupgrade.bin`. The MT6000 *does*
> publish `factory.bin`: do not copy MT3000's procedure onto it.

---

## 2. Path A — installer wizard (reference)

**Interactive** (serves the UI at `:8099`; drive it in a browser):
```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

**Headless** (no browser; full deploy + verification):
```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER_IP> '<ROUTER_PASSWORD>' <LIGHTNING_ADDRESS>
```

Flags: `--tag <tag>` (pin the release under test), `--channel`, `--list`, `--bin <path>`, `--trust-host-key`.

**Pin the tag to the resolved release:** `--tag "$TAG"`. A pass with no
pinned tag is not evidence about `$TAG`.

What it does: detect OS+arch → download `tollgate-installer` → serve on `:8099`
→ scan LAN → identify router → `POST /api/deploy` → poll `/api/status/<id>` →
verify. Deploy steps in order: `verify → stage → flash → firmware → password →
upstream → install → brand → portal → lnurl → services → health`.

**Arch → asset mapping is derived at deploy time, not stored** — the wizard
detects the arch on the router (`DISTRIB_ARCH` → `opkg print-architecture` →
`ubus call system board` → `apk --print-arch` → `uname -m`) and builds the asset
URL from the tag. Asset URL shape:
`https://github.com/FreedomTechFeed/packages/releases/download/$TAG/tollgate-wrt_${VER}_<arch>.<ext>`.

---

## 3. Path B — manual install on vanilla OpenWrt

### 3.0 Read this first: there is no feed to add

`FreedomTechFeed/packages` publishes **GitHub release assets only** — per-arch
`.apk`/`.ipk` files plus `SHA256SUMS`. There is **no `APKINDEX` and no
`Packages.gz` anywhere in that repo**, so a GitHub release page cannot be used as
an apk/opkg repository. Pointing `/etc/apk/repositories` or
`/etc/opkg/customfeeds.conf` at it fails with an index-fetch error.

So the honest manual path is a **direct file install**, which is byte-for-byte
what the wizard does: `fetch asset → verify sha256 → install the file by path`.

### 3.1 Set the admin password FIRST — do not skip

**The package never sets a credential** — not the admin board's, not the private
WiFi's — and the admin board's login is checked against root's shadow hash. A
freshly reset router has an **empty** root password, so the package fails closed
and does **not** serve the board until root has one. Set it **before**
installing. `passwd` prompts twice and echoes nothing:

```sh
passwd root
```

> ⚠️ **If you install first and the board is "NOT being served", that is expected
> fail-closed behaviour — not an install failure.** The install output ends with
> something like:
> ```
> WARNING: the TollGate admin board (…) is NOT being served:
>   root has no usable password (state: empty).
> …
> TollGate: root has no usable password — the :8090 admin board is NOT served.
> ```
> The backend, portal and captive network are all up; only the admin login is
> withheld (rpcd would accept **any** password against the empty hash). To
> **recover**, set a password and re-run the setup — the gate lives in the
> package's `uci-defaults`, which are consumed at install, so reinstall to re-run
> them:
> ```sh
> passwd root                        # the password you want
> # opkg lane:
> opkg install --force-reinstall /tmp/tollgate-wrt_${VER}_${ARCH}.ipk
> # apk lane:
> apk del tollgate-wrt && apk add --allow-untrusted /tmp/tollgate-wrt_${VER}_${ARCH}.apk
> ```

### 3.2 Direct file install (both lanes)

> ⚠️ **The install restarts the network and WILL drop your SSH session.** The
> package's `postinst` restarts network/wifi/firewall/dnsmasq/uhttpd/nodogsplash,
> and the setup **moves the wired LAN port off the captive bridge (`br-lan`,
> `192.168.1.1`) onto a new bridge, `br-private`** — the management subnet, a
> derived `192.168.x.0/24` next to the LAN. So:
> * a wired `ssh root@192.168.1.1` session **drops mid-install**;
> * on that same cable the router is now at the **management** address — the
>   install prints it as `Private SSID …, IP <x>.1`, and it is
>   `uci get network.private.ipaddr`. Reconnect with `ssh root@<x>.1`;
> * or reconnect over the router's WiFi at `192.168.1.1`;
> * to survive the drop, run the block under `screen`/`tmux` — or just let it
>   finish and run §4 in a fresh session; the install does not need the session
>   to stay up.

§3.0 applies: there is no feed to add, so install the release **file by path** —
byte-for-byte what the wizard does (`fetch asset → verify sha256 → install the
file`). One block serves both package managers: it detects `apk` vs `opkg`,
resolves the arch from the router, verifies the manifest, then installs.

```sh
# $TAG is the tag you resolved in §0; derive the version exactly as §0 does.
: "${TAG:?run §0 first, then re-run this block with TAG exported}"
VER=$(printf '%s' "$TAG" | sed -e 's/^v//' -e 's/-/_/g')
BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"
ARCH=$(sed -n 's/^DISTRIB_ARCH=//p' /etc/openwrt_release | tr -d "\"'")
if command -v apk >/dev/null 2>&1; then
    EXT=apk; PM="apk add --allow-untrusted --force-overwrite"
    apk update
else
    EXT=ipk; PM="opkg install"
    opkg update
fi
FILE="tollgate-wrt_${VER}_${ARCH}.${EXT}"
cd /tmp

# A fresh image has no clock and no ca-bundle, so HTTPS verification fails with
# "SSL verify error: unknown error". Skip TLS verification for the FETCH only —
# the SHA256SUMS gate below is what carries integrity.
wget -q --no-check-certificate "$BASE/SHA256SUMS" -O SHA256SUMS
wget -q --no-check-certificate "$BASE/$FILE" -O "$FILE"
grep " $FILE\$" SHA256SUMS | sha256sum -c - || { echo "CHECKSUM FAILED — do not install"; exit 1; }
$PM "/tmp/$FILE"
```

**`--allow-untrusted` is required on the apk lane, not optional.** The
`.apk`/`.ipk` files carry **no apk/opkg signature** (no signing key is published;
the only release key signs the manifest). The manifest check above *is* your
integrity gate. It mirrors the wizard.

> The wizard uses heavier flags on the opkg lane
> (`--force-downgrade --force-reinstall --force-overwrite --force-depends`). **On
> a vanilla router try it plain first** — if dependencies resolve cleanly,
> `--force-depends` would only mask a real problem. Add flags one at a time and
> report which was needed.

### 3.3 Set the private WiFi passphrase AFTER install

The package leaves the private (management) **wireless** AP **disabled** until a
passphrase exists — the wired LAN ports on `br-private` are unaffected. Set one
once the service is up; this writes the key and enables the radio (the value is
never echoed):

```sh
read -r -s -p 'private WiFi passphrase (min 8 chars): ' WIFIPW; echo
tollgate network private set-password "$WIFIPW"
unset WIFIPW
```

### 3.4 If your own device is on the management subnet

The router puts its management network on the /24 **next to its LAN** — with the
default LAN `192.168.1.0/24`, usually `192.168.2.0/24`. The chooser only avoids
the router's **own** uplinks; it cannot see the networks **your** device is on.
So if your laptop/phone is also on `192.168.x.0/24` (a common overlap), two
devices answer `192.168.x.1` on your machine and your connectivity breaks.

If that happens:
* reach the router on the **wire only** (its management address is on the cable —
  `ssh -b <your-wired-ip> root@<x>.1`), or disconnect the colliding network; or
* relocate the management subnet for the test:
  ```sh
  uci set network.private.ipaddr=10.99.99.1
  uci commit network
  /etc/init.d/network restart      # reconnect at 10.99.99.1
  ```

> ⚠️ A reinstall/upgrade **re-derives** this address (it is not preserved today).
> Tracked in OpenTollGate/tollgate-module-basic-go#906.

### 3.5 AR300M flash-space caveat

The default payload (`/usr/bin/tollgate-wrt` ≈ 12.4 MB + `/usr/bin/tollgate` ≈
7.4 MB) does **not** fit a 16 MB NOR device against roughly 4.6 MB of free
jffs2. Expect `ENOSPC`; a tmpfs fallback is **volatile** — it works until the
next reboot and then silently disappears. **Record the exact AR300M variant**
(`-lite`, `-16`, `-nor`, `-nand`) and the free-space situation in your feedback.

---

## 4. Shared acceptance check (run after ANY path)

> Note the names: service is **`tollgate-wrt`**, port is **`:2121`**. The router
> has **no `curl`** — use `uclient-fetch`.

```sh
/etc/init.d/tollgate-wrt status
# expect: running: API :2121 listening and /var/run/tollgate.sock present

uclient-fetch -q -O- http://127.0.0.1:2121/ 2>/dev/null | head -5   # backend live
uclient-fetch -q -O- http://127.0.0.1:2121/whoami 2>/dev/null       # answers even while the wallet loads
uclient-fetch -q -O- http://127.0.0.1:2121/identity 2>/dev/null     # 404 EXPECTED unless a merchant key exists
tollgate version                                                    # build identity — CONFIRM it matches $TAG/$VER

ps | grep -i tollgate                 # process present
logread | grep -i tollgate            # startup lines, no panic
ndsctl status                         # nodogsplash active, no duplicate firewall rules
nft list ruleset | grep -i tollgate   # firewall rules present, no duplicates
cat /proc/sys/kernel/hostname         # TollGate-… hostname set

grep -i "NOT served\|no usable password" /tmp/tollgate-setup.log   # fail-closed: board withheld when root has no password — see §3.1
```

Declare pass only if all hold. An install that completes but never starts is a
**fail**; the most useful attachment is `logread | grep -i tollgate`.

---

## 5. What ONLY the wizard does

A manual install is not crippled — the package's `postinst` already runs the
uci-defaults, restarts network/wifi/firewall/dnsmasq/uhttpd/nodogsplash, and
enables+starts `tollgate-wrt`. The wizard additionally does:

1. **`tollgate.lan` name resolution** — `/etc/hosts` entry, dnsmasq `address`.
   A manual install **will not serve the portal under that name**; use the IP.
2. **Package integrity gate** — sha256 against `SHA256SUMS` before install
   (manual equivalent: §3.1/§3.2's verify step).
3. **Flashing OpenWrt from stock GL.iNet firmware**, root-password setup, and
   upstream/STA WAN configuration.
4. **LNURL / Lightning-address configuration.**
5. **`mdnsd` install** for `.local` mDNS.
6. **Post-install version read-back and provenance logging** — manual equivalent:
   `tollgate version`.
7. **opkg lane only:** pre-install of `nodogsplash`+`jq` with a laptop-side
   fallback download.

**Report a manual-install gap as exactly that** — "works manually but X is
missing" is useful; inferring "the feature is broken" from a missing
wizard-only step is not.

---

## 6. Reporting

Use `FEEDBACK-TEMPLATE.md`. For each run record: device + **variant**, OpenWrt
version, **`$TAG`/`$VER`**, path (**A / B**), the exit code if any, §4
output, and pass/partial/fail.

**State which path you actually ran**, and **confirm the installed version**
(`tollgate version`) matches `$TAG` — a Path A pass is not evidence about Path B,
and vice versa.

> **Host-specific editions:** see [`runbook/README.md`](runbook/README.md).
