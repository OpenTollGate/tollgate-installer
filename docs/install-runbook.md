# TollGate — Install Run Book (Review Club)

Test the newest `FreedomTechFeed/packages` release on a GL.iNet router.
**The installer wizard (§1) is the only supported install path in this
release.** It flashes the router, sets the root password, brings up the uplink,
installs TollGate and configures name resolution.

**After install**, the router answers at **`https://tollgate-<4hex>.lan`** (its
hostname, e.g. `tollgate-e753.lan`) and the default admin **and** management-WiFi
password is **`TollGate`** — the board forces a change at first login.

> **Manual install is temporarily unsupported in v0.6.0.** See §2. The wizard
> installs both package formats, so the manual procedure is not needed to test
> the release.

## Contents

| § | What | When |
|---|---|---|
| [0](#0-resolve-the-release-under-test-first) | Resolve the release under test | always, first |
| [1](#1-installer-wizard) | **tollgate-installer** — the supported path | the default |
| [2](#2-manual-install--temporarily-unsupported) | Manual install — temporarily unsupported | pointer only |
| [3](#3-access-after-install) | Access after install — bridges · names · password · cert | to reach the UI |
| [4](#4-shared-acceptance-check) | Acceptance check | after install |
| [5](#5-reporting) | Reporting | when you're done |

**Lane:** installer wizard only. It selects `.apk` on OpenWrt 25.12+ and `.ipk`
on OpenWrt 24.10; on a router already running OpenWrt it skips flashing and
picks the package by package manager.

**Devices:** GL-MT3000, GL-MT6000 (`aarch64_cortex-a53`/`mediatek-filogic` — the
**validated** pair). The feed publishes eight `(arch, target)` pairs; see the
feed's `TARGETS.md` for the full list and per-device validation status. GL-AR300M
is flash-constrained: the payload does not fit a 16 MB NOR device, and the
wizard refuses rather than half-install.

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

Release assets are named per **`(arch, target)`**
(`tollgate-wrt_${VER}_${ARCH}-${TARGET}.${EXT}`), not per arch alone. The wizard
resolves both on the router; an arch-only name 404s.

---

## 1. Installer wizard

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

The wizard detects OS+arch, scans the LAN, and installs. The admin and
management-WiFi password fields are **optional** (under *advanced*) — leave them
blank to install the **`TollGate`** default and change it at first login.

On a router that is **already running OpenWrt**, the wizard skips the flash step
and installs the package matching the router's package manager: `.apk` on
25.12+, `.ipk` on 24.10. That is how one wizard run covers both lanes.

A wired install over an existing OpenWrt router moves the wired LAN port from
`br-lan` to `br-private` during the package postinst. The wizard captures the
management address before that move and, if it cannot reach the router
afterwards, ends **pending** — not failed — with the address and reconnect
instructions. Join the private WiFi (management SSID) or renew DHCP on the wired
interface, then use **Re-check**.

**Get the current build.** The launcher resolves the newest installer for its
channel. Since `v0.6.0` the stable channel is the default; pin both the
installer build and the release under test when you need an exact pairing:

```sh
... install-and-test.sh --installer-tag v0.6.0 --tag "$TAG"
```

A pass with no `--tag "$TAG"` is not evidence about `$TAG`.

**What the wizard sets beyond the package:** the guest SSID becomes
`!TollGate-<4hex>` (the leading `!` sorts it first in client lists), the fixed
alias **`tollgate.lan`** resolves to the management address, LNURL is
configured, and `mdnsd` serves `.local`. The board is the **default face** on
the entry pair (`:8080`/`:443`); LuCI moves to `:8090`/`:8443`.

---

## 2. Manual install — temporarily unsupported

**Use the wizard (§1).** The manual install procedure is withdrawn for v0.6.0
and will return in the next release after the findings below are fixed.

The last maintained manual procedure (both `.apk` and `.ipk`, plus the offline
bundle) is in the
[`v0.6.0-alpha2-rc33` runbook](https://github.com/OpenTollGate/tollgate-installer/blob/v0.6.0-alpha2-rc33/docs/install-runbook.md#2-path-b--manual-install-on-vanilla-openwrt).

Tracked at
[OpenTollGate/tollgate-installer#155](https://github.com/OpenTollGate/tollgate-installer/issues/155).
Known manual-path issues deferred to that release:

- a fresh install can print the fail-closed `NOT being served` warning while
  the package's default-credential write does not take;
- the first pass can fall back to the legacy `entry_ui=luci` mapping;
- `tollgate.lan` and the leading-`!` guest SSID are wizard-only.

---

## 3. Access after install

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
choose your own** at first login.

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

## 4. Shared acceptance check

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
> **module** build the package embeds. Since the feed pins
> `PKG_SOURCE_TAG:=v0.6.0`, `v0.6.0` reports `v0.6.0-g9364f2e` (the release
> commit; runtime code is still `cf7cad3`). Confirm the feed release with the
> **package manager** instead:
> ```sh
> opkg list-installed | grep tollgate-wrt     # opkg lane → "tollgate-wrt - 0.6.0-r1"
> apk list -I 2>/dev/null | grep tollgate-wrt # apk lane  → "tollgate-wrt-0.6.0-r1 …"
> ```

Declare **pass** only if all hold. An install that completes but never starts is
a **fail** — attach `logread | grep -i tollgate`.

---

## 5. Reporting

Record device + **variant**, OpenWrt version, **`$TAG`/`$VER`**, the installer
build (`--installer-tag`), any exit code, the §4 output, and pass/partial/fail.
Open an issue in `OpenTollGate/tollgate-installer` with that block.

> Host mechanics: [`runbook/README.md`](runbook/README.md).
