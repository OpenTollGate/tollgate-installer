# tollgate-installer — TollGate router setup wizard

Cross-platform onboarding wizard that turns an OpenWrt router into a TollGate
Bitcoin WiFi access point. Single Go binary — serves a web UI that auto-discovers
routers on your LAN and deploys the tollgate-wrt backend over SSH.

```
┌─ Your Laptop ────────────────────────┐
│                                     │
│  tollgate-installer binary                │
│  └─ web UI at http://localhost:8099 │
│     └─ scans LAN for routers        │
│     └─ deploys via SSH              │
│                                     │
└─────────┬───────────────────────────┘
          │ SSH
          ▼
┌─ Router (OpenWrt) ──────────────────┐
│  tollgate-wrt      (:2121) backend  │
│  nodogsplash       (:2050) portal   │
│  uhttpd :2051      captive portal   │
│  LuCI :8080        OpenWrt admin    │
└─────────────────────────────────────┘
```

## Quick start

1. **Flash your router to OpenWrt** (the wizard does NOT flash firmware). For
   GL.iNet routers, use the web UI at `http://192.168.8.1` → Advanced →
   Upload Firmware → untick "Keep settings".

   Alternatively, SSH in and sysupgrade:
   ```sh
   # GL-MT3000 on OpenWrt 25.12.5 (clean install, no config kept)
   curl -O https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin
   cat openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin | \
     ssh root@192.168.1.1 'cat > /tmp/sysupgrade.bin && sysupgrade -n /tmp/sysupgrade.bin'
   ```
   See [OpenWrt firmware downloads](https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/)
   for other devices.

2. **Set a root password:**
   ```sh
   ssh root@192.168.1.1
   passwd
   ```

3. **Download the wizard** from the
   [latest release](https://github.com/OpenTollGate/tollgate-installer/releases/latest):

   | OS | File |
   |---|---|
   | macOS (Intel) | `tollgate-installer-darwin-amd64` |
   | macOS (Apple Silicon) | `tollgate-installer-darwin-arm64` |
   | Linux (x86_64) | `tollgate-installer-linux-amd64` |
   | Windows | `tollgate-installer-windows-amd64.exe` |

4. **Run it:**
   ```sh
   chmod +x tollgate-installer-*
   ./tollgate-installer-darwin-arm64   # replace with your OS
   ```

5. **Browser opens at** `http://localhost:8099` — follow the wizard:
   - It scans your network and lists detected routers
   - Select your router, enter the root password
   - Choose upstream connection (Ethernet WAN or WiFi repeater)
   - Enter your Lightning address (where payouts go)
   - Click **"Deploy TollGate"**

6. After ~30 seconds: connect to the `TollGate-XXXX` WiFi (4 random chars,
   all-caps) and open any website — the captive portal appears with payment
   options.

## Testing

No release needed — the wizard is a single binary, so you can run the latest
build directly from the repo. This is the fastest way to try it on a router.

### 1. Quick test (interactive, browser UI)

Download, run, and open the web UI — nothing else to install:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)
```

The script auto-detects your OS/arch, downloads the matching binary, and serves
the UI at `http://localhost:8099` (auto-picks a free port if taken). Drive the
wizard in the browser exactly as in **Quick start** step 5.

### 2. Full router test (headless, no browser)

Same command plus a router IP and credentials — the script deploys TollGate
over SSH and verifies the router afterward:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
    <ROUTER_IP> <ROOT_PASSWORD> <LIGHTNING_ADDRESS>
```

Example (fresh-reset GL.iNet, empty root password):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
    192.168.1.1 '' you@walletofsatoshi.com
```

It runs these steps automatically:

1. Long-runs the installer, pings `/api/scan` for detected routers
2. POSTs `/api/deploy` with your router IP / password / Lightning address
3. Polls `/api/status/<job_id>` until all deploy steps complete
4. SSHes in and verifies: hostname, ports (`:80 :2050 :2121`), `tollgate.lan`
   DNS, LNURL in `identities.json`, captive portal, TollGate health ad
5. Prints a clear `Deploy COMPLETE` / `Deploy FAILED` result

### 3. Run the latest code without curl

Just clone, build, and go:

```bash
git clone https://github.com/OpenTollGate/tollgate-installer.git
cd tollgate-installer
go build -o tollgate-installer .
./tollgate-installer            # serves at :8099
# or on another port:
./tollgate-installer -port 8200
```

### API endpoints (what the wizard exposes)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/` | GET | Web UI |
| `/api/scan` | GET | Discover routers on LAN |
| `/api/deploy` | POST | Start a deploy job |
| `/api/status/<id>` | GET | Poll deploy progress |
| `/api/wifi-scan` | GET | Scan SSIDs (STA/repeater mode) |

> **Note:** `felixfelix-bot`-owned clones may serve a pre-release build. The
> canonical source is `OpenTollGate/tollgate-installer`. If the raw URL above
> 404s, the PR with `install-and-test.sh` hasn't merged yet — use option 3
> (clone + build) until it does.

## What the wizard does

The deployment runs a sequence of steps over SSH:

| # | Step | Action |
|---|------|--------|
| 1 | Verify SSH | Connects, reads `/etc/openwrt_release` |
| 2 | Check firmware | Parses OpenWrt version |
| 3 | Set password | Sets the root password you entered |
| 4 | Configure upstream | WiFi STA mode or WAN passthrough |
| 5 | Install tollgate-wrt | Downloads the .ipk/.apk on the laptop, pushes over SSH, installs via opkg/apk |
| 6 | Brand router | Sets hostname + SSID to `TollGate-XXXX`, DNS to `tollgate.lan`, nodogsplash gateway name |
| 7 | Configure Lightning | Sets your Lightning address, dev split, margin, mint |
| 8 | Restart services | Restarts `tollgate-wrt` + `nodogsplash` + `rpcd` |
| 9 | Health check | Verifies the TollGate API is responding on `:2121` |

The tollgate-wrt `.ipk`/`.apk` ships the captive portal
(`/etc/tollgate/tollgate-captive-portal-site`), the rpcd plugin, and the
nftables enforcement rules — the wizard only installs the package and points
nodogsplash/uhttpd at it.

### Which `tollgate-wrt` package gets installed

Step 5 downloads the **feed-built** package from
`FreedomTechFeed/packages`, so a wizard run is also an end-to-end test of the
feed's package build:

| | |
|---|---|
| Selected release tag | `v0.6.0-alpha2-pre2` (the main-tip pre-release) |
| Package version | `0.6.0_alpha2_pre2` — the tag with `v` dropped and `-` → `_`, installed as `0.6.0_alpha2_pre2-r1` |
| Source commit | `7cd1882` of `tollgate-module-basic-go` |
| Asset name | `tollgate-wrt_0.6.0_alpha2_pre2_<arch>.{ipk,apk}` for 7 arches |

The installed binary reports `v0.6.0-alpha2-g7cd1882` — the version string plus
the **source commit**. That string is not the package version, on purpose: the
commit identifies the build, the version string only identifies the release
line. The wizard reads the installed build back off the router and logs it
(`Installed tollgate-wrt build: …`).

To install a different published release tag — a newer pre-release, or an older
tag to reproduce an old build — set the override before launching the wizard:

```sh
TOLLGATE_FEED_RELEASE_TAG=v0.6.0-alpha1 ./tollgate-installer
```

An empty or malformed value is ignored in favour of the default. If the feed
does not publish the selected tag, the wizard falls back to the pinned
`v0.5.0` GitHub release asset (aarch64 only) and the deploy log names which
source was used. `go test ./...` fails if the selected tag does not exist on the
feed. See [docs/package-provenance.md](docs/package-provenance.md).

### Pre-download (staging) + on-disk re-deploy cache

The wizard has an optional **PreStage** phase (checkbox in the deploy UI —
"Pre-download required packages before deploy"): before running the
flash/install steps it downloads the OpenWrt sysupgrade image (for stock
GL.iNet routers being flashed) and the tollgate-wrt package into an in-memory
cache, so the actual deploy runs entirely offline from the laptop's
perspective. This matters when the laptop's only internet path is *via* the
router being flashed/reconfigured (STA/repeater mode).

Staged binaries are also persisted to an **on-disk cache** so a second deploy
to a different router re-uses them instead of re-downloading:

| | |
|---|---|
| **Location** | `~/.tollgate-stage/` (`$HOME/.tollgate-stage`) |
| **File name** | hex `sha256` of the asset URL |
| **What is persisted** | ONLY the version-pinned OpenWrt flash image (consultant RISK 3). Package binaries (.ipk/.apk, nodogsplash, jq) are **never** written to the disk cache — they can change between releases, and a stale cached copy could shadow a newer package. |
| **Invalidation** | None needed — see note below. |

**Clearing the cache:** delete the directory — it is always safe to remove;
the wizard re-stages the flash image on the next deploy:

```sh
rm -rf ~/.tollgate-stage
```

The installer never writes outside `~/.tollgate-stage`. TTL/invalidation is
deliberately minimal: because only the version-pinned flash image is
persisted, a cache entry is either correct (exact image for that pinned
release) or superseded when the plan bumps `openWrtVersion` — at which point
the image URL changes, the sha256 filename changes, and the old entry is
simply orphaned (harmless, remove with `rm -rf` above).

## Build from source

```sh
git clone https://github.com/OpenTollGate/tollgate-installer.git
cd tollgate-installer
go build -o tollgate-installer .
./tollgate-installer
```

Cross-compile for all platforms:

```sh
GOOS=darwin  GOARCH=arm64 go build -o dist/tollgate-installer-darwin-arm64 .
GOOS=darwin  GOARCH=amd64 go build -o dist/tollgate-installer-darwin-amd64 .
GOOS=linux   GOARCH=amd64 go build -o dist/tollgate-installer-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -o dist/tollgate-installer-windows-amd64.exe .
```

## Prerequisites

- **Router** running OpenWrt (24.10.x or 25.x). The wizard does NOT flash
  firmware — see GL.iNet or OpenWrt docs for flashing.
- **SSH access** — port 22 open, root password set (empty on a fresh reset).
- **Upstream internet** — either Ethernet cable into the WAN port, or WiFi
  credentials for the router to join an existing network.
- **Lightning address** — where Bitcoin payments from customers route
  (e.g. `you@walletofsatoshi.com` or a raw LNURL).

## Architecture

This is a **thin UI wrapper**. All business logic lives in
[tollgate-module-basic-go](https://github.com/OpenTollGate/tollgate-module-basic-go).
The wizard only discovers routers, renders a deployment UI, and runs SSH
commands — no payment processing, identity derivation, or Nostr logic.

| Repo | Role |
|------|------|
| **tollgate-installer** (this repo) | Laptop-side onboarding wizard |
| [tollgate-captive-portal-site](https://github.com/OpenTollGate/tollgate-captive-portal-site) | Router-side captive portal (ships in the .ipk) |
| [tollgate-module-basic-go](https://github.com/OpenTollGate/tollgate-module-basic-go) | Payment backend (Cashu + Lightning) |

## License

MIT
