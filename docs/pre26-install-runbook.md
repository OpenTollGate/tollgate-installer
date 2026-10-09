# TollGate v0.6.0-rc1-pre26 — Install Run Book (Review Club)

**Release under test:** `v0.6.0-rc1-pre26` (published 2026-10-09T00:51:41Z)
**Devices in scope:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53` / `mediatek-filogic`), GL-AR300M (`mips_24kc` / `ath79-generic`)

> **Why this run book exists.** Every review so far went through the installer wizard. If the wizard ever fails, or a user refuses to run a binary, the package must still install by hand on stock OpenWrt. That is the fallback of record, so it gets tested every release — not just the wizard.
>

---

## 0. The two paths at a glance

| | Path A — installer wizard | Path B — manual install |
|---|---|---|
| **Entry point** | `install-and-test.sh`, UI on `:8099` | shell on the workstation + router |
| **Who it is for** | first-time users, review sessions | fallback, WAN-less sites, anyone avoiding a binary |
| **Proven?** | **yes** — pre26 deployed this way | **no — this is what the session must prove** |
| **Internet needed on router?** | no | no for the offline bundle; yes for the single-file variants |

Both paths must end in the same observable state. **§4 is the shared acceptance check** — run it after whichever path you used. A path only "passes" if §4 passes.

---

## 1. Prerequisites

### Hardware
| Device | Arch / target | Notes |
|---|---|---|
| GL.iNet MT3000 (Beryl AX) | `aarch64_cortex-a53` / `mediatek-filogic` | primary target |
| GL.iNet MT6000 (Flint 2) | `aarch64_cortex-a53` / `mediatek-filogic` | same arch pair as MT3000 |
| GL.iNet AR300M | `mips_24kc` / `ath79-generic` | **flash-constrained — read §3.4** |

> MT3000 and MT6000 share one arch pair, so a packaging pass on one implies the other. Keep both in the matrix anyway: **they differ in flash layout**, and the MT3000 publishes no `factory` image while the MT6000 does.

### Router state
- **Vanilla OpenWrt** — 25.12.x for the `.apk` lane, 24.10.x for the `.ipk` lane.
- SSH reachable, root password known (a freshly reset OpenWrt ships with an **empty** root password).
- **Either** an uplink, or a workstation with internet (the offline bundle needs internet on the *laptop*, never on the router).

### 1.1 Clean slate — getting to vanilla OpenWrt

Take the profile from the official index for *your* device; never from memory. Verified against `downloads.openwrt.org/releases/25.12.5` (current stable) and `24.10.8` (oldstable):

| Device | Target | Profile | Images published |
|---|---|---|---|
| GL-MT3000 | `mediatek/filogic` | `glinet_gl-mt3000` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` — **no `factory` image** |
| GL-MT6000 | `mediatek/filogic` | `glinet_gl-mt6000` | `squashfs-sysupgrade.bin`, `squashfs-factory.bin`, `initramfs-kernel.bin`, `preloader.bin`, `bl31-uboot.fip` |
| GL-AR300M | `ath79/generic` | `glinet_gl-ar300m-lite` or `glinet_gl-ar300m16` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` |
| GL-AR300M (NAND units) | `ath79/nand` | `glinet_gl-ar300m-nor` or `glinet_gl-ar300m-nand` | `squashfs-sysupgrade.bin`, `initramfs-kernel.bin` |

> ⚠️ **There is no plain `glinet_gl-ar300m` profile** in 25.12.5 or 24.10.8 — only `-lite`, `-ar300m16` (ath79/**generic**) and `-nor`, `-nand` (ath79/**nand**). Picking wrong is a brick, not a misconfiguration. **Record which profile you used.** The installer resolves this itself by board name (all four boards verified present in 25.12.5); a human flashing by hand must not guess.
>
> ⚠️ **MT3000 publishes no `factory` image.** No OEM→OpenWrt web-recovery upload with a `factory.bin` on that device — use the GL.iNet OEM local-upgrade path, or vendor U-Boot recovery with the `sysupgrade.bin`. The MT6000 *does* publish `factory.bin`: do not copy MT3000's procedure onto it.

---

## 2. Path A — installer wizard (reference, proven)

**Interactive** (serves the UI at `:8099`; drive it in a browser):
```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

**Headless** (no browser; full deploy + verification):
```sh
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER_IP> '<ROUTER_PASSWORD>' <LIGHTNING_ADDRESS>
```

Flags: `--tag <tag>` (pin the release under test), `--channel`, `--list`, `--bin <path>`, `--trust-host-key`.

**Pin the tag** (`--tag v0.6.0-rc1-pre26`). A pass with no pinned tag is not evidence about pre26.

What it does: detect OS+arch → download `tollgate-installer` → serve on `:8099` → scan LAN → identify router → `POST /api/deploy` → poll `/api/status/<id>` → verify. Deploy steps in order: `verify → stage → flash → firmware → password → upstream → install → brand → portal → lnurl → services → health`.

**Arch → asset mapping is derived at deploy time, not stored** — the wizard detects the arch on the router (`DISTRIB_ARCH` → `opkg print-architecture` → `ubus call system board` → `apk --print-arch` → `uname -m`) and builds the asset URL from the tag. Asset URL shape:
`https://github.com/FreedomTechFeed/packages/releases/download/<tag>/tollgate-wrt_<pkg_version>_<arch>.<ext>` — version = tag minus leading `v`, `-`→`_`.

---

## 3. Path B — manual install on vanilla OpenWrt

### 3.0 Read this first: there is no feed to add

`FreedomTechFeed/packages` publishes **GitHub release assets only** — per-arch `.apk`/`.ipk` files plus `SHA256SUMS`. There is **no `APKINDEX` and no `Packages.gz` anywhere in that repo**, so a GitHub release page cannot be used as an apk/opkg repository. Pointing `/etc/apk/repositories` or `/etc/opkg/customfeeds.conf` at it fails with an index-fetch error.

So the honest manual path is a **direct file install**, which is byte-for-byte what the wizard does (the wizard never writes feed config to the router either):
`fetch asset → verify sha256 → install the file by path`.

### 3.1 Variant B1 — offline bundle (recommended; router needs NO uplink)

The release ships a per-arch bundle containing the package **plus its whole dependency closure**, proven closed (every dependency is either in the bundle or already in the base image).

```sh
# on the workstation — this is where internet is needed
curl -fsSL 'https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-rc1-pre26/tollgate-wrt-0.6.0_rc1_pre26-aarch64_cortex-a53-offline.tar.gz' | tar -xz
sha256sum --check --strict MANIFEST.sha256
./install-offline.sh <ROUTER_IP> '<ROUTER_PASSWORD>'
```
For the AR300M use the `mips_24kc` bundle: `…/tollgate-wrt-0.6.0_rc1_pre26-mips_24kc-offline.tar.gz`

Options: `--bundle <dir>`, `--user`, `--port`, `--staging-dir`, `--trust-mac`, `--iface`, `--password-file`, `--report <file>`, `--dry-run`, `--version`, `-h/--help`.
**Run `--dry-run` first** and paste its output — it shows what would be staged before anything is written.

Order of operations (from the driver itself): verify the bundle's own manifest *before pushing a byte* → stage and **apply the management keepalive seed first** (its own comment records that skipping it bricked the bench on 2026-08-16/17) → stage the closure over `ssh 'cat > …'` (**never `scp -O`** — a fresh dropbear has no `sftp-server`) → run the router-side sequence → probe the br-lan client view from the workstation → merge both halves into one machine-readable report.

**Exit codes — quote the number in your feedback:**

| code | meaning | | code | meaning |
|---|---|---|---|---|
| 0 | pass | | 6 | staged name/hash binding |
| 2 | usage | | 7 | `apk` failed |
| 3 | bundle failed its own manifest check | | 8 | runtime payload missing |
| 4 | missing dependency | | 9 | a post-install assertion failed |
| 5 | keepalive/lockout risk | | 10 | cannot reach the router |
| | | | 11 | router image unsupported for this lane |

**Limits of B1, from the bundle's own README:** the `.ipk` / ≤24.10 `opkg` lane is **not covered** (apk-tools 3 only, OpenWrt 25.x); the dependency closure is pinned to *this* release+target, so a different release or target needs its own bundle.
**Doc-vs-artifact bug:** the bundle README says `cd tollgate-wrt-<version>-<arch>-offline`, but the tarball extracts **flat** — no such directory exists. Extract into a directory you create.

### 3.2 Variant B2 — direct install, `.apk` lane (OpenWrt 25.12.x)

```sh
URL_BASE="https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-rc1-pre26"

# verify the manifest before trusting the bytes
cd /tmp && wget -q "$URL_BASE/SHA256SUMS" && grep aarch64_cortex-a53 SHA256SUMS

# MT3000 / MT6000 (aarch64_cortex-a53)
wget "$URL_BASE/tollgate-wrt_0.6.0_rc1_pre26_aarch64_cortex-a53.apk" -O /tmp/tollgate-wrt.apk
sha256sum /tmp/tollgate-wrt.apk   # expect bdb4b2e3d233729ff41750e6a60bffd11cfc55fdbd819ebbedd962c7afd479c4
apk update
apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt.apk

# AR300M (mips_24kc)
# wget "$URL_BASE/tollgate-wrt_0.6.0_rc1_pre26_mips_24kc.apk" -O /tmp/tollgate-wrt.apk
# sha256sum /tmp/tollgate-wrt.apk  # expect 2f36c09091c7ffae998dc5192fa7158965508b3e6e1ed778dfbf6158eadb787f
# apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt.apk
```

**`--allow-untrusted` is required, not optional.** The `.apk`/`.ipk` files carry **no apk/opkg signature** (no signing key is published; the only release key signs the manifest). That is exactly why the sha256 check above is not decoration: you are bypassing signature verification, so the manifest check *is* your integrity gate. It mirrors the wizard, which does the same on the router.

### 3.3 Variant B3 — direct install, `.ipk` lane (OpenWrt 24.10.x) — least proven

No offline bundle covers this lane, so it is the most valuable one to test by hand.

```sh
URL_BASE="https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-rc1-pre26"

# MT3000 / MT6000
wget "$URL_BASE/tollgate-wrt_0.6.0_rc1_pre26_aarch64_cortex-a53.ipk" -O /tmp/tollgate-wrt.ipk
sha256sum /tmp/tollgate-wrt.ipk   # expect 6ab80e0b5924808a1e29171e27c9d80b1f7a9ff4a1c9766d219fdb6e03e3c46c
opkg update && opkg install /tmp/tollgate-wrt.ipk

# AR300M
# wget "$URL_BASE/tollgate-wrt_0.6.0_rc1_pre26_mips_24kc.ipk" -O /tmp/tollgate-wrt.ipk
# sha256sum /tmp/tollgate-wrt.ipk  # expect 1cb93418718bf8462fddc3e234b877c550ae453194dfb6179869929d791b9872
# opkg update && opkg install /tmp/tollgate-wrt.ipk
```

The wizard uses heavier flags on this lane (`--force-downgrade --force-reinstall --force-overwrite --force-depends`). **On a vanilla router try it plain first** — if dependencies resolve cleanly, `--force-depends` would only mask a real problem. Add flags one at a time and report which one was needed; that difference is itself a finding.

### 3.4 AR300M flash-space caveat — read before installing on that device

The package's declared dependencies are `+nodogsplash +jq` plus the Go arch deps, and the closure is ~37 packages (nodogsplash, libmicrohttpd-no-ssl, iptables-nft, a set of kernel modules). Kernel modules are **kernel-version-locked**, so the offline bundle pins 25.12.5.

More seriously: the default payload (`/usr/bin/tollgate-wrt` ≈ 12.4 MB + `/usr/bin/tollgate` ≈ 7.4 MB) does **not** fit a 16 MB NOR device against roughly 4.6 MB of free jffs2. Expect `ENOSPC`, and note that a tmpfs fallback is **volatile** — it works until the next reboot and then silently disappears.

**Therefore: record the exact AR300M variant** (`-lite`, `-16`, `-nor`, `-nand`) and the flash/free-space situation in your feedback. If the install only succeeds on the offline bundle, that is a real finding about the release, not a testing error.

---

## 4. Shared acceptance check (run after ANY path)

> Note the names: service is **`tollgate-wrt`**, port is **`:2121`**. The router has **no `curl`** — use `uclient-fetch`.

```sh
/etc/init.d/tollgate-wrt status
# expect: running: API :2121 listening and /var/run/tollgate.sock present

uclient-fetch -q -O- http://127.0.0.1:2121/ 2>/dev/null | head -5   # backend live
uclient-fetch -q -O- http://127.0.0.1:2121/whoami 2>/dev/null       # answers even while the wallet loads — best cold-boot probe
uclient-fetch -q -O- http://127.0.0.1:2121/identity 2>/dev/null     # identity; 404 is EXPECTED unless identities.json has a merchant key
tollgate version                                                    # build identity

ps | grep -i tollgate                 # process present
logread | grep -i tollgate            # startup lines, no panic
ndsctl status                         # nodogsplash active, no duplicate firewall rules
nft list ruleset | grep -i tollgate   # firewall rules present, no duplicates
cat /proc/sys/kernel/hostname         # TollGate-… hostname set
```

Declare pass only if all hold. An install that completes but never starts is a **fail**, and the single most useful attachment is `logread | grep -i tollgate` from the failing boot.

---

## 5. What ONLY the wizard does (so you know what a manual install will not give you)

A manual install is not crippled — the package's `postinst` already runs the uci-defaults, restarts network/wifi/firewall/dnsmasq/uhttpd/nodogsplash, and enables+starts `tollgate-wrt`. But the wizard does these **additional** things that the package does not:

1. **`tollgate.lan` name resolution** — `/etc/hosts` entry, `dhcp.@dnsmasq[0].address='/tollgate.lan/<IP>'`, `dhcp.lan.dhcp_option='6,<IP>'`. A manual install **will not serve the portal under that name**; use the IP. This is the most likely source of a "portal doesn't work" false report.
2. **Package integrity gate** — sha256 against `SHA256SUMS` before a root install. Manual path: that is §3.2/§3.3's verify step — do not skip it.
3. **Flashing OpenWrt from stock GL.iNet firmware**, root-password setup, and upstream/STA WAN configuration.
4. **LNURL / Lightning-address configuration.**
5. **`mdnsd` install** for `.local` mDNS.
6. **Post-install version read-back and provenance logging** — manual equivalent: `tollgate version`.
7. **opkg lane only:** pre-install of `nodogsplash`+`jq` with a laptop-side fallback download. Manual opkg pulls them from the default feeds or fails loudly.

**Report a manual-install gap as exactly that** — "works manually but X is missing" is useful; inferring "the feature is broken" from a missing wizard-only step is not.

---

## 6. Reporting

Use `FEEDBACK-TEMPLATE.md`. For each run record: device + **variant**, OpenWrt version, pinned tag, path (**A / B1 / B2 / B3**), the exit code if any, §4 output, and pass/partial/fail.

**State which path you actually ran.** A Path A pass is not evidence about Path B, and vice versa — that separation is the entire point of this session.


> **Host-specific editions:** see [`runbook/README.md`](runbook/README.md) for the OS/architecture download table.
