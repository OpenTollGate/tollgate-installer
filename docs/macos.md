# Running the tollgate-installer launcher on macOS

`install-and-test.sh` is the `curl | bash` entry point. It downloads the
`tollgate-installer` binary for your OS/arch, serves the wizard at
`http://localhost:8099`, and — given a router IP — deploys TollGate to that
router over SSH and verifies the result.

It runs on macOS on both Apple Silicon (`darwin-arm64`) and Intel
(`darwin-amd64`).

## What a Mac user has to do

Nothing but run it:

```bash
# interactive: opens the wizard UI, you drive it in the browser
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh)

# headless: deploy to a router and verify, no browser
bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \
    192.168.1.1 '' you@walletofsatoshi.com
```

The three positional arguments are `<ROUTER_IP> <ROOT_PASSWORD> <LIGHTNING_ADDRESS>`.
Pass `''` for an empty root password (a freshly reset router).

Useful options:

```bash
... --list                     # show recent feed releases, newest first
... --channel stable           # pick a channel instead of the default alpha
... --tag v0.6.0-alpha4-pre15  # pin an exact feed release
... --bin ./tollgate-installer # use a binary you built yourself
```

## Prerequisites

| What | Needed? | Why |
|------|---------|-----|
| `curl` | yes | macOS ships it |
| `bash` ≥ 3.2 | yes | macOS ships 3.2.57 at `/bin/bash`; the script only uses 3.2 syntax |
| `mktemp` | yes | macOS ships it; used for the per-run working directory |
| `python3` | **no** | only prettifies JSON. Comes from Xcode Command Line Tools (`xcode-select --install`) |
| `ssh` | optional | used by the post-deploy router probe (step 6) only |
| `sshpass` | **no** | not part of macOS, and the deploy does not use it |

The script checks all of this **before** it downloads anything and prints the fix
for whatever is missing, so a missing tool never shows up as a mysterious
failure half-way through a deploy.

## The installer host does not have to be Linux

The Mac is only the **driver**. It talks to the router over the network
(HTTP to the local wizard, SSH to the router); the router runs the TollGate
backend. Your Mac does not become a gateway, does not need a static IP, and
does not need to be on the same subnet as anything except the router.

## Gatekeeper

Binaries fetched with `curl` do **not** get the `com.apple.quarantine` attribute,
so the downloaded `tollgate-installer` runs without any prompt.

A copy saved through a browser (Safari/Chrome download, AirDrop, mail
attachment) *is* quarantined. If macOS refuses to open it:

1. System Settings → Privacy & Security → scroll to the blocked-item notice →
   **Open Anyway**; or
2. clear the flag yourself:

```bash
xattr -d com.apple.quarantine ./tollgate-installer
```

## python3 (optional)

`python3` is not part of macOS; it arrives with the Xcode Command Line Tools.
The launcher reads every JSON field it needs — the feed release tag, the
`job_id` from `POST /api/deploy`, `/api/status/<id>`, `/api/config` — with
`awk`/`sed`, so a Mac without the CLT completes a headless deploy. What you lose
without python3:

- the scan result and the failure dump are printed verbatim instead of indented;
- nothing else (the step summary and the package-provenance lines are extracted
  with `sed`/`grep`/`awk` and look the same).

To install it: `xcode-select --install` (or `brew install python3`).

## Router verification (step 6) and sshpass

`sshpass` is not installed on macOS and is not in Homebrew core. It is only used
for the *optional* step-6 probe of the router (hostname, ports, `tollgate.lan`
DNS, LNURL, captive portal, health ad). The deploy itself never uses it: the Go
binary opens SSH in-process with `golang.org/x/crypto/ssh`.

The launcher picks the first route that works on your Mac:

1. `sshpass` if you have it (the password is passed through the environment,
   not `argv`, so it does not show up in `ps`);
2. otherwise ssh's own `SSH_ASKPASS` helper when your OpenSSH is 8.4 or newer
   (macOS 12 and later); the helper reads the password from the environment, so
   it is never written to disk, and ssh is fed `/dev/null`;
3. otherwise it prints

   ```
   skipped: sshpass is not installed and this ssh cannot use SSH_ASKPASS
            ...
            router-side verification not performed on this host
   ```

   and continues — that is *not* a failed deploy. Verify by hand with
   `ssh root@<ROUTER_IP>` (check `:2050` portal, `:2121` API, LNURL in
   `/etc/tollgate/identities.json`).

The script's exit status reflects the deploy, not the probe: a completed deploy
exits `0` even when step 6 was skipped.

## Files and folders

Everything a run writes lives in one private directory created with
`mktemp -d` under `$TMPDIR` (on macOS a per-user `/var/folders/...` path):

```
$TMPDIR/tollgate-installer.XXXXXX/installer.log   # the wizard's stdout/stderr
                                     seen         # provenance de-dup scratch
                                     status       # last /api/status body
                                     askpass.sh   # only while step 6 runs (0700)
```

The path is printed at the end of the run (`Done. Installer log: ...`). The
downloaded binary lands in the directory you ran the script from
(`./tollgate-installer`).

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ERROR: missing required tool(s): curl` | curl absent from `PATH` | macOS ships it — check `$PATH` / `brew install curl` |
| `installer did not come up on :8099 (waited 15s)` | port busy or the binary cannot run on this Mac | the log tail is printed; try `PORT=8300 bash <(curl …)`, or `--bin ./tollgate-installer` with a locally built binary |
| `ERROR: no job_id in deploy response` | the installer build is older than this launcher | rebuild/re-release; the launcher needs `POST /api/deploy` to return `{"job_id": "..."}` |
| `skipped: sshpass is not installed …` | expected on most Macs | the deploy still completed; verify by hand over ssh, or install `sshpass` if you really want the probe |
| `skipped: no ssh client on this host` | ssh missing from `PATH` (rare) | `xcode-select --install` |
| `(router verification failed — wrong password or host unreachable…)` | step 6 could not log in | check `ROUTER_PASSWORD` / router IP; the deploy result itself is unaffected |
| macOS refuses to open the downloaded binary | browser-downloaded copy, quarantined | see **Gatekeeper** above |

## Notes on platform support in the source

The Mac path is not special-cased anywhere: `uname -s`/`uname -m` map to
`darwin-arm64` / `darwin-amd64`, the same release asset naming as Linux, and
`discover.go` parses BSD `arp -a` output (`? (192.168.2.1) at e8:8f:6f:df:9e:11 on
en0 ifscope [ethernet]`, plus the unpadded-MAC form BSD prints) as well as
Linux `ip neigh`. `discover_arp_test.go` pins both formats.
