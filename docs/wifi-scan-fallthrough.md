# WiFi scan: iwinfo's own refusals, and the fall-through contract

**Status:** implemented, unit-tested, and exercised against a scripted router in
the Go suite; **NOT yet run against a physical router by this change** — see
"What is not verified".

## Why

The installer's STA/repeater path asks the router to scan for nearby networks.
On a GL.iNet MT3000 the operator saw six `Scanning not possible` lines and the
UI ended at "No WiFi networks detected" instead of a real SSID list, even
though four successive scan strategies exist in `handleWifiScan`.

Two things were wrong, and they compounded:

1. **`scanFailedHeuristic` did not know iwinfo's own refusal strings.**
   Upstream iwinfo (`openwrt/iwinfo` @ `66bdd1a`) prints:

   ```
   iwinfo_cli.c:687:		printf("Scanning not possible\n\n");
   iwinfo_cli.c:692:		printf("No scan results\n\n");
   ```

   Both are `printf()` — **stdout** — so the strategies' `2>/dev/null` never hid
   them. `Scanning not possible` means the scanlist op *failed*; `No scan
   results` means it succeeded with zero BSSes. Neither is a scan result, but
   neither was in the heuristic, so the text was parsed as if it were output:
   `parseIwinfoScan` found 0 ESSIDs and the handler **returned**
   `{"ssids":[], "debug":"Scanning not possible"}`. Strategies 3-5
   (`iw phy phy0/phy1 scan`, `iwinfo wlan0/wlan1 scan`, `iw dev scan`) never
   ran. `iwinfo_cli.c:1009/1036` (`No such wireless backend` /
   `No such wireless device`, stderr) were missing too.

2. **No strategy could fall through on a zero-network parse.** An attempt that
   produced no error but also no networks was treated as the answer.

The ×6 repetition was a second, independent bug in the UI: `index.html`
re-scanned on a 600 ms debounce (`_wifiScanTimer`) wired to the password
field's `oninput`, so every typing pause fired another `/api/wifi-scan` — and
each request scans every radio.

## What this change adds

### 1. The refusal class is DATA — `scanFailureSignatures`

```go
var scanFailureSignatures = []string{
	"command not found",
	"No such device",
	"No such wireless device",
	"No such wireless backend",
	"Operation not supported",
	"Operation not permitted",
	"Device or resource busy",
	"Scanning not possible",
	"No scan results",
	"Usage:",
}
```

`scanFailedHeuristic` is now a lookup over that list, so a newly observed
refusal is a one-line data change rather than a new branch. Each entry carries
its provenance in the code comment (iwinfo source line, field observation, or
shell/`iw` class).

### 2. The chain falls through — `scanChain` / `scanViaChain`

The strategy list is data: `iwinfo scan` → `iwinfo <dev> scan` (per interface)
→ `iw dev <dev> scan` (per interface) → `iwinfo wlan0`/`wlan1` (vendor naming,
two separate commands). The interface list comes from `discoverWifiDevices`
(`iw dev` → bare `iwinfo` → sysfs) — see "Mainline OpenWrt" below for why the
older, hardcoded list (`iw phy phy0/phy1 scan`, `iw dev scan`) could never work
on a stock image.
`scanViaChain` runs them in order and treats an attempt as a **success only
when at least one of its commands produced output that is not a recognised
refusal *and* parses to ≥ 1 network**. Otherwise it moves to the next strategy:

| Attempt outcome | Chain behaviour |
|---|---|
| no output at all | fall through, log `no output` |
| recognised refusal (`Scanning not possible`, `Usage:`, …) | fall through, log `refused: <first line>` |
| output that parses to 0 networks | fall through, log `parsed 0 networks` |
| ≥ 1 network | **win** — return those SSIDs and the strategy name |

Commands are judged *per command*, so one refusing interface can no longer
poison a multi-interface strategy. Every attempt now runs with stderr merged
(`2>&1`) instead of `2>/dev/null`: with the refusal class recognised as data,
the router's real error message survives into the log and debug fields.

### 3. The winning strategy is exposed

`/api/wifi-scan` (POST) now answers:

```json
{"ssids":[{"name":"Cafe WiFi","encryption":"wpa2","band":"2.4"}],
 "strategy":"iwinfo <dev> scan",
 "log":"[1] iwinfo scan (no device argument): refused: No such wireless backend: scan\n[2] iwinfo <dev> scan (ifaces=phy0-ap0,phy1-ap0): 2 network(s)"}
```

* networks found → `200` + `strategy` = the winning strategy
* every strategy refused → `200` + `strategy:"none"`, `log`, `debug` (the
  router's own last words). The handler reached the router; the scan simply
  found nothing. The status is no longer a silent `{"ssids":[]}`.
* not one byte came back → `500` + `error` + `log` (no iwinfo/iw, or no
  wireless interfaces at all)
* no SSH → `502` (unchanged)

`log` is one line per attempt, in execution order, and each line is also
written to the installer's stdout (`log.Printf("wifi-scan <ip> %s", line)`), so
the next person can see which strategy ran without a packet capture.

### 4. The UI can no longer scan per keystroke

* `onPasswordChange` no longer schedules a scan at all — typing the router
  password (which is only the *SSH credential* the scan needs) cannot start
  one. The list already on screen is kept, with a "press Rescan" hint.
* `wifiScan()` is latched by `wifiScanInFlight` (one request in flight) and
  guarded by a token, so a double-clicked Rescan or a mode toggle mid-scan
  cannot stack requests. A stale response can no longer overwrite a newer
  result, and the hint now names the strategy that won (`Found 2 network(s) via
  iwinfo <dev> scan`) or lists the attempts that failed.

## Decision record

* **Refusals are data, not branches.** Adding a refusal string must never
  require reasoning about control flow: one entry in `scanFailureSignatures`
  with its evidence in the comment.
* **Zero networks is never "success"** while an untried strategy remains.
  Returning an empty list early is what hid the MT3000 refusal behind a
  plausible-looking empty scan.
* **An attempt that yields nothing keeps the router's own words** (`debug`,
  `log`) and names the strategy that produced a result (`strategy`), so a
  failure is diagnosable from the API response alone.
* The device-less `iwinfo scan` strategy is **retained as strategy 1** (some
  vendor builds accept it) but now logs an explicit refusal on upstream
  iwinfo instead of silently returning nothing.

## Verification

* `go test ./...` — `wifi_scan_chain_test.go` drives a scripted router through
  `scanViaChain`: a router that answers `Scanning not possible` to every
  iwinfo attempt must still be persuaded to run the phy-level strategy and
  return its networks; a winning strategy must stop the walk; `Usage:` text
  from `iw dev scan` must be a refusal, not an empty success.
* The installed UI is the embedded `index.html` (`embed.go`), so a UI check
  against a built binary exercises the shipped file.

## What is not verified

**A physical router was not reachable while this change was written**, so the
`Scanning not possible` → real SSID list path has NOT been observed on the
MT3000. The card that produced this change requires that evidence and is
blocked on access, not closed green on unit tests. What is needed is either

```sh
# on the box (or via the installer's SSH path), with the radios up:
iwinfo phy0-ap0 scan ; iwinfo phy1-ap1 scan        # raw output pasted
curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"ip":"192.168.8.1","password":"<root pw>"}' \
     http://localhost:8099/api/wifi-scan | jq        # strategy + ssids
```

or an SSH login (key or root password) to the router on its LAN address.
Until that runs, treat the MT3000 result as unverified.

## Mainline OpenWrt: why ALL five strategies refused (fixed here)

A second field failure, on a **stock OpenWrt 25.12.5 filogic box (GL-MT3000)**,
reached the UI as `No WiFi networks detected. Router returned: Usage: iw
[options] command … (6.17)`. That usage text is iw's TOP-LEVEL usage, and it is
the tell: every link of the chain was dead on mainline, for four *different*
reasons, and the discovery that would have saved it never ran.

| Strategy | On a stock mainline filogic box | Why |
|---|---|---|
| `iwinfo scan` | dead | upstream iwinfo prints usage when a device is missing: `argc > 1 && argc < 3` (iwinfo_cli.c:962-977) |
| `iwinfo <dev> scan` | **should have worked** | it is the OpenWrt-native per-interface scan — but its device list came ONLY from bare `iwinfo` (main.go, `wirelessInterfaces`), an OPTIONAL CLI. With no `iwinfo` binary (or an empty enumeration) the strategy is dead before it starts, and no other enumerator existed |
| `iw phy phy0/phy1 scan` | dead by construction | `scan` is declared `TOPLEVEL(scan, …, CIB_NETDEV, handle_scan_combined)` (iw scan.c:2642) — a NETDEV command. Identifying it by phy mismatches the idby, `__handle_cmd` returns `HANDLER_RET_USAGE` (iw.c:471-474; iw.h:70) with no command matched, and main prints iw's top-level usage (iw.c:640-641). **There is no phy-level scan in iw at all.** The names were hardcoded too |
| `iwinfo wlan0 scan \|\| iwinfo wlan1 scan` | dead | 25.12 names wireless netdevs `phy0-ap0`/`phy1-ap1`; `wlan0`/`wlan1` is GL.iNet naming. The `\|\|` also made it ONE scanCommand, so the first device's refusal could never be logged or judged |
| `iw dev scan` | dead by construction | `iw dev` needs a device: `dev <devname> scan`. Being last, its usage text is what `LastRaw`/`debug` carried to the UI — hence the operator's message |

### The fix

* **Real discovery** — `discoverWifiDevices()` runs `iw dev` (authoritative, and
  present: iw 6.17 on the box; line shapes `phy#0` / `\tInterface phy0-ap0`,
  iw interface.c:386/391) → bare `iwinfo` (vendor/older images, `%-9s ESSID:`
  blocks, iwinfo_cli.c:635) → `ls /sys/class/ieee80211/` (answers "are there
  radios at all?", which turns "no wireless interfaces" into the actionable
  "2 phy(s) present but no wireless interface — the radios are down").
* **No dead command, no hardcoded name** — the chain is now
  `iwinfo scan` (vendor) → `iwinfo <dev> scan` per discovered interface →
  `iw dev <dev> scan` per discovered interface (new; the only form that works
  when the `iwinfo` CLI is absent) → `iwinfo wlan0`/`wlan1` as two separate
  commands (vendor naming retained, still judged per command). The invalid
  `iw phy <phy> scan` and `iw dev scan` are gone.
* **Honest failure** — when every method refuses, `/api/wifi-scan` now returns
  an `error` naming the discovery result and each method with the router's own
  reason (`WiFi scan refused by every method the installer tried (4) — this is
  the router refusing to scan, NOT an empty list of nearby networks. …`), and
  the UI shows that sentence instead of "No WiFi networks detected". The
  password hint is kept ONLY for responses with no router evidence at all
  (e.g. the SSH failure), because a refused scan is not a credential problem.
* `"not found"` joined `scanFailureSignatures`: busybox ash reports a missing
  CLI as `-ash: iw: not found` — *not* bash's "command not found".

### Verification (hermetic)

`wifi_scan_chain_test.go` drives a scripted router; no hardware involved. The
mainline fixture is the operator's box (iw present and printing usage, iwinfo
CLI absent, netdevs `phy0-ap0`/`phy1-ap1`, 2.4 GHz scan answers, 5 GHz busy):

* `TestScanChainFindsNetworksOnMainlineFilogic` — the fixed chain wins with
  `iw dev <dev> scan` and returns the two networks; it also asserts no
  device-less `iw dev scan` and no `iw phy` command is ever run.
* `TestLegacyChainCannotScanMainline` — the PRE-FIX chain, kept in the test file
  as a control, finds nothing on the same fixture through the same walker
  (guarded by a probe proving the fixture's scan command really returns 2
  networks, so the control is not vacuous).
* `TestScanRefusalSummaryIsHonest` — the all-refused path names every method,
  the absent tools, and the radios-down hint, and never claims "no networks".
* `TestScanChainTreatsUsageTextAsRefusal` — the operator's verbatim iw usage
  text is classified as a refusal and surfaces as such.
* `TestScanChainUsesDiscoveredNamesOnly`, `TestDiscoverWifiDevices`,
  `TestWirelessInterfaces`, `TestScanUiNeverClaimsNoNetworks`.

**Still not verified:** the fixed chain has not been run against a physical
mainline router from here — no router access. The hermetic fixture encodes the
observed refusal (the iw usage text in the UI) and the tool inventory the box
demonstrably has (`iw` 6.17), but the winning `iw dev phy0-ap0 scan` output is
modelled, not captured.

