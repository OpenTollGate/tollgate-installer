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

### 3.1 Variant B1 — direct install, `.apk` lane (OpenWrt 25.12.x)

**The admin and private-WiFi passwords are REQUIRED.** The package never
generates or prints a credential. **apk-tools 3 does not forward the caller's
environment to package scriptlets** (measured on 25.12.5 / apk-tools 3.0.5), so
an exported `TOLLGATE_ADMIN_PASSWORD` **does not reach the package** on this
lane. Write the credentials to two ROOT-ONLY files instead — the package reads,
applies and removes them:

```sh
BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"

# verify the manifest before trusting the bytes
cd /tmp && wget -q "$BASE/SHA256SUMS"

# the credentials YOU choose (>= 8 chars), written root-only BEFORE the install
mkdir -p /etc/tollgate && umask 077
printf '%s' 'choose-a-strong-admin-password' > /etc/tollgate/admin-password
printf '%s' 'choose-a-strong-wifi-password'  > /etc/tollgate/wifi-password

# MT3000 / MT6000 (aarch64_cortex-a53)
wget "$BASE/tollgate-wrt_${VER}_aarch64_cortex-a53.apk" -O /tmp/tollgate-wrt.apk
grep " tollgate-wrt_${VER}_aarch64_cortex-a53.apk\$" SHA256SUMS | sha256sum -c -
apk update
apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt.apk

# AR300M (mips_24kc)
# wget "$BASE/tollgate-wrt_${VER}_mips_24kc.apk" -O /tmp/tollgate-wrt.apk
# grep " tollgate-wrt_${VER}_mips_24kc.apk\$" SHA256SUMS | sha256sum -c -
# apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt.apk
```

**`--allow-untrusted` is required, not optional.** The `.apk`/`.ipk` files carry
**no apk/opkg signature** (no signing key is published; the only release key
signs the manifest). The manifest check above *is* your integrity gate. It
mirrors the wizard.

### 3.2 Variant B2 — direct install, `.ipk` lane (OpenWrt 24.10.x)

```sh
BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"
cd /tmp && wget -q "$BASE/SHA256SUMS"

# the credentials YOU choose (>= 8 chars) — required, see 3.1. opkg DOES forward
# the environment, so `TOLLGATE_ADMIN_PASSWORD=... opkg install` also works
# here; the root-only files below work on both lanes.
mkdir -p /etc/tollgate && umask 077
printf '%s' 'choose-a-strong-admin-password' > /etc/tollgate/admin-password
printf '%s' 'choose-a-strong-wifi-password'  > /etc/tollgate/wifi-password

# MT3000 / MT6000
wget "$BASE/tollgate-wrt_${VER}_aarch64_cortex-a53.ipk" -O /tmp/tollgate-wrt.ipk
grep " tollgate-wrt_${VER}_aarch64_cortex-a53.ipk\$" SHA256SUMS | sha256sum -c -
opkg update && opkg install /tmp/tollgate-wrt.ipk

# AR300M
# wget "$BASE/tollgate-wrt_${VER}_mips_24kc.ipk" -O /tmp/tollgate-wrt.ipk
# grep " tollgate-wrt_${VER}_mips_24kc.ipk\$" SHA256SUMS | sha256sum -c -
# opkg update && opkg install /tmp/tollgate-wrt.ipk
```

The wizard uses heavier flags on this lane
(`--force-downgrade --force-reinstall --force-overwrite --force-depends`). **On a
vanilla router try it plain first** — if dependencies resolve cleanly,
`--force-depends` would only mask a real problem. Add flags one at a time and
report which was needed.

### 3.3 AR300M flash-space caveat

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
version, **`$TAG`/`$VER`**, path (**A / B1 / B2**), the exit code if any, §4
output, and pass/partial/fail.

**State which path you actually ran**, and **confirm the installed version**
(`tollgate version`) matches `$TAG` — a Path A pass is not evidence about Path B,
and vice versa.

> **Host-specific editions:** see [`runbook/README.md`](runbook/README.md).
