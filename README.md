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

1. **Start from vanilla OpenWrt.** The wizard can flash supported GL.iNet
   routers during deployment; for a clean-slate/manual-package test, use the
   [pre26 install run book](docs/pre26-install-runbook.md), which covers both
   wizard deployment and direct `.apk`/`.ipk` installation on vanilla OpenWrt.

   Alternatively, SSH in and sysupgrade manually:
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
   [latest release](https://github.com/felixfelix-bot/tollgate-installer/releases/latest)
   (the published cross-platform binaries currently live in this release source):

   | OS | File |
   |---|---|
   | macOS (Intel) | `tollgate-installer-darwin-amd64` |
   | macOS (Apple Silicon) | `tollgate-installer-darwin-arm64` |
   | Linux (x86_64) | `tollgate-installer-linux-amd64` |
   | Linux (ARM64) | `tollgate-installer-linux-arm64` |
   | Windows | `tollgate-installer-windows-amd64.exe` |

   Verify the download against the release's `SHA256SUMS` before running it. On
   macOS:
   ```sh
   shasum -a 256 -c SHA256SUMS
   ```
   On Linux:
   ```sh
   sha256sum -c SHA256SUMS
   ```
   In Windows PowerShell:
   ```powershell
   Get-FileHash .\tollgate-installer-windows-amd64.exe -Algorithm SHA256
   ```
   Compare the displayed hash with the matching line in `SHA256SUMS`.

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

The installer never sends the router's root password before it has verified the
router's SSH host key. On a router it has not seen before it refuses, prints the
fingerprint (SHA256:…) it was shown, and stops. Verify that fingerprint on the
router's own console, then hand the launcher the value it printed — it is
forwarded to the installer binary and the key is remembered for later runs (also
after a re-flash, when the router comes back with a new key):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
    --trust-host-key SHA256:<fingerprint> <ROUTER_IP> <ROOT_PASSWORD> <LIGHTNING_ADDRESS>
```

The same decision can be passed in the environment as
TOLLGATE_TRUST_HOST_KEY=<fingerprint>, or as -trust-host-key SHA256:<fingerprint>
when running the binary directly; the launcher's --help lists its options.

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

### 4. macOS (Intel and Apple Silicon)

The launcher runs on a Mac. The installer host only has to reach the router over
the network — it does not need to be Linux, and it does not act as a gateway:

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
    192.168.1.1 '' you@walletofsatoshi.com
```

Nothing to install first: `curl`, `bash` (the 3.2.57 that ships with macOS) and
`mktemp` are all that the script requires, and it reports up front which of them
is missing and how to get it.

- **python3 is not required.** On macOS it only exists with the Xcode Command
  Line Tools, so the launcher parses every JSON field it needs (feed release,
  `job_id`, deploy status, `/api/config`) with `awk`/`sed`. Without python3 you
  lose pretty-printed JSON and nothing else; `xcode-select --install` adds it.
- **`sshpass` is not part of macOS**, so the optional post-deploy router probe
  (step 6) either uses ssh's own `SSH_ASKPASS` (OpenSSH ≥ 8.4, i.e. macOS 12
  and later) or is skipped with an explicit message. The deploy itself never
  uses sshpass — the binary speaks SSH in-process. A skipped probe is not a
  failed deploy: the script still exits 0 once the deploy reports done.
- **Gatekeeper**: the binary arrives via `curl`, so macOS does not set the
  quarantine flag and it just runs. A copy saved through a browser *is*
  quarantined — if macOS refuses to open it, either allow it in System Settings
  → Privacy & Security → "Open Anyway", or run
  `xattr -d com.apple.quarantine ./tollgate-installer`.
- Everything the run writes goes into one private directory under `$TMPDIR`
  (its path is printed at the end, e.g. `/var/folders/…/tollgate-installer.ab12cd/`)
  — installer log included.

See [`docs/macos.md`](docs/macos.md) for the full walkthrough and the
troubleshooting table.

### API endpoints (what the wizard exposes)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/` | GET | Web UI |
| `/api/scan` | GET | Discover routers on LAN |
| `/api/identify` | POST | Re-identify a router (returns `ssh_refusal` + `ssh_fingerprint`) |
| `/api/trust-host-key` | POST | Trust a router's SSH host key after verifying its fingerprint on the console |
| `/api/deploy` | POST | Start a deploy job |
| `/api/status/<id>` | GET | Poll deploy progress |
| `/api/wifi-scan` | GET | Scan SSIDs (STA/repeater mode) |

> **Note:** `install-and-test.sh` tries the `felixfelix-bot` release source first
> because that is where the current published installer binaries live, then
> falls back to the `OpenTollGate` release source. If the raw URL above 404s,
> use option 3 (clone + build) until the launcher is published there.

## What the wizard does

The deployment runs a sequence of steps over SSH:

| # | Step | Action |
|---|------|--------|
| 1 | Verify SSH | Connects, reads `/etc/openwrt_release` |
| 2 | Check firmware | Parses OpenWrt version |
| 3 | Set password | Sets the root password you entered |
| 4 | Configure upstream | WiFi STA mode or WAN passthrough |
| 5 | Install tollgate-wrt | Downloads the .ipk/.apk on the laptop, pushes over SSH, installs via opkg/apk |
| 6 | Brand router | Resolves the router's one device code (stored in UCI, reused) and writes hostname + captive SSID + private SSID from it, DNS to `tollgate.lan`, nodogsplash gateway name |
| 7 | Configure Lightning | Sets your Lightning address, dev split, margin, mint |
| 8 | Restart services | Restarts `tollgate-wrt` + `nodogsplash` + `rpcd` |
| 9 | Health check | Verifies the TollGate API is responding on `:2121` |

The tollgate-wrt `.ipk`/`.apk` ships the captive portal
(`/etc/tollgate/tollgate-captive-portal-site`), the rpcd plugin, and the
nftables enforcement rules — the wizard only installs the package and points
nodogsplash/uhttpd at it.

### A generated root password is saved before it is set

If the router has **no** root password and you did not type one, the wizard
generates a 20-character credential and sets it (step 3 above). It is shown
**once** on the final screen — and, because a missed one-shot screen would leave
you locked out of a router whose only previous access was an empty root
password, a copy is written to a recovery file **before** it is applied:

```sh
~/.tollgate-root-credentials     # mode 0600, appended — one line per deploy
# <RFC3339 time> <tab> <router address> <tab> <password>
```

Override the location with `TOLLGATE_CREDENTIAL_FILE`. Its permissions are set
to 0600 on creation and re-applied on every write, so a umask cannot publish a
router's root password to other local users, and earlier entries are never
rewritten. The deploy log names the path (never the password — the log is
returned in full by every status poll), so the credential stays findable even
after the one-shot screen has been missed.

### The router's device identity — one code, minted once

Branding resolves **one** four-character code on the router and builds every
name from it: hostname tollgate-<code>, captive SSID TollGate-<code>, private
SSID nym-<code>. The code is kept in the router's uci store, etc/config/tollgate,
under the `code` option, and REUSED — a redeploy of an existing router keeps the
name it already answers to, instead of minting a new one. Adoption order: the
store, then a machine-shaped hostname, then a machine-shaped captive SSID, then
a mint. The contract is shared with tollgate-module-basic-go (same store, same
order, same alphabet); see [docs/device-identity.md](docs/device-identity.md).

### Which `tollgate-wrt` package gets installed

Step 5 downloads the **feed-built** package from
`FreedomTechFeed/packages`, so a wizard run is also an end-to-end test of the
feed's package build:

| | |
|---|---|
| Selected release tag | `v0.6.0-rc1-pre26` (the pinned pre-release default) |
| Package version | `0.6.0_rc1_pre26` — the tag with `v` dropped and `-` → `_`, installed as `0.6.0_rc1_pre26-r1` |
| Source commit | `4614ac2` of `tollgate-module-basic-go` |
| Asset name | `tollgate-wrt_0.6.0_rc1_pre26_<arch>.{ipk,apk}` for 14 arches |

The installed binary reports `v0.6.0-alpha2-g373770a` — the version string plus
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
does not publish the selected tag, the wizard **refuses** rather than
silently downgrading: the GitHub `v0.5.0` asset (aarch64 only) is reachable
only by explicit opt-in — the `--allow-fallback` flag or
`TOLLGATE_ALLOW_GITHUB_FALLBACK=1` — and the deploy log names which source
was used. `go test ./...` fails if the selected tag does not exist on the
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

## Security: is the package the one the release published?

Step 5 installs the package as root through a package manager whose own
verification is deliberately disabled for this path, so the wizard checks the
bytes itself right before pushing them to the router:

- a format/size sanity check rejects a truncated transfer or an error page saved
  as "the package";
- the bytes must match the sha256 the release publishes — taken from the release
  manifest, a per-asset sidecar, or the GitHub release API's per-asset digest,
  which every feed release publishes today, so this works with no feed change.
  Only the API digest is an **independent** anchor: the manifest and the sidecar
  are fetched from the *same host* that served the package, so a host (or a
  MITM) serving altered bytes could serve a matching digest too — a match from
  those two sources is reported as not-verified, never as a pass;
- a mismatch FAILS the deploy, naming the asset and both digests;
- with no published digest — or only a same-origin one — the install step renders
  as a warning rather than a green "done", and the
  `TOLLGATE_REQUIRE_PACKAGE_DIGEST` environment variable turns that into a hard
  failure for unattended release runs;
- an install that came from the ROUTER's own package feeds (the last-resort feed
  path, where the bytes never pass through the wizard) also renders as a warning
  carrying an explicit not-verified marker: step 6 is green only for a verdict
  that actually verified the bytes against an independent published digest;
- the router-side wget path is covered too, by hashing the file on the router.

Not covered: a compromise of the release itself, where the digest and the package
would be replaced together. That needs a signed manifest published by the feed
with a key pinned in the binary. See
[docs/package-provenance.md](docs/package-provenance.md#package-integrity-verification-audit-c2-i-03)
for the full policy, the live evidence, and the honest limits.

## Build from source

```sh
git clone https://github.com/OpenTollGate/tollgate-installer.git
cd tollgate-installer
go build -o tollgate-installer .
./tollgate-installer
```

### Release binaries (reproducible, all platforms)

```sh
scripts/release-binaries.sh                  # build every target into dist/
scripts/release-binaries.sh --repro-check    # + assert a byte-identical rebuild
scripts/release-binaries.sh --tag v0.7.0 --publish   # create/update the release
```

It builds linux/darwin × amd64/arm64 plus windows/amd64 with `CGO_ENABLED=0`,
`-trimpath` and `-buildvcs=false`, stamps `-X internal/app.version` /
`-X internal/app.commit`,
and writes `SHA256SUMS` (verify with `shasum -a 256 -c SHA256SUMS` on macOS or
`sha256sum -c SHA256SUMS` on Linux) and `REPRODUCE.txt` (the exact toolchain and
flags, so anyone can repeat the build). `--publish` needs `gh` with write access
and uploads with `--clobber`, so re-running after a fix updates the release in
place. Nothing here depends on GitHub Actions.

### Installer release tags are the installer's OWN version line

The tag given to release-binaries.sh is stamped into the binary as its version,
and an operator reads it printed **directly beneath the feed release being
installed**. So the tag must NOT embed a feed pre-number: an installer cut
alongside feed pre19 and tagged v0.6.0-alpha2-pre19-rc1 announces pre19 while
installing whatever the feed channel resolves — on 2026-09-28 that was feed
pre20, and the operator reasonably asked which one they had actually got.

Tag the installer on its own version line (v0.6.0-alpha2-rc2, or v0.7.0) and
describe the feed it was cut alongside in the release notes. The script refuses
a tag containing pre followed by digits; setting ALLOW_FEED_PRENUMBER_TAG to 1
is the deliberate override. A bare "pre" with no digits is fine:
v0.6.0-alpha2-pre-rc1 is a real historical tag. Pinned by
tests/shell/test-installer-tag-scheme.sh.

(Values here are deliberately not backticked: this README carries a
pre-existing table row naming a password, which arms the fleet pre-commit
markdown value scan on the whole file, and a backticked value in an added line
is then reported as a password-like value. See the repo's own combined
"password-like value in markdown table" block.)

Asset names are `tollgate-installer-<os>-<arch>[.exe]` — exactly what
`install-and-test.sh` fetches from the selected fork or upstream
`releases/latest/download/` source. For a one-off
single binary, `GOOS=darwin GOARCH=arm64 go build -o dist/tollgate-installer-darwin-arm64 .`
still works.

## Prerequisites

- **Router** running OpenWrt (24.10.x or 25.x). The wizard flashes supported
  GL.iNet stock routers during deployment; see [the pre26 install run book](docs/pre26-install-runbook.md)
  for clean-slate and manual package-install paths.
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
