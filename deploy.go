package main

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// tollgatePackage is the opkg/apk package name for the tollgate-wrt
	// package (installed via the OpenWrt feed fallback path).
	tollgatePackage = "tollgate-wrt"
	// tollgate-wrt .ipk download URL (OpenWrt <= 24.10 opkg back-compat).
	//
	// NOTE (v0.5.0 de-brand): the tollgate-wrt package now ships from the
	// OpenTollGate org's tollgate-module-basic-go releases. The nftables
	// enforcement rules (PR #283) ship INSIDE this ipk under
	// ./etc/nftables.d/, so no separate overlay download is needed.
	tollgatePkgURL = "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk"
	// tollgate-wrt .apk download URL (OpenWrt 25+ with APK support).
	// OpenWrt 25.12+ cannot install legacy .ipk (ar archive) packages.
	tollgatePkgAPKURL = "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.apk"
)

// ─── Secret carriers ─────────────────────────────────────────────
//
// Secrets that must reach the router (root password, WiFi STA passphrase)
// cross as base64 carriers rather than shell string literals: the value is
// base64-encoded host-side and decoded on the router via
// `echo <b64> | base64 -d` into a shell variable. BusyBox ships the base64
// applet with -d support. This keeps the plaintext out of the SSH command
// string (process argv on the router) and makes shell injection through a
// password/key impossible — the value never participates in shell parsing.

// shellB64 returns s encoded for embedding as a base64 carrier.
func shellB64(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// passwdCommand returns the router-side command that sets the root
// password. The password crosses as a base64 carrier and is decoded into a
// shell variable; the plaintext never appears in the command string.
func passwdCommand(password string) string {
	return "pw=$(echo " + shellB64(password) + " | base64 -d) && " +
		"printf '%s\\n%s\\n' \"$pw\" \"$pw\" | passwd root 2>&1"
}

// ─── Mint configuration ──────────────────────────────────────────

// mintCfg mirrors one entry of the router's accepted_mints config.
type mintCfg struct {
	URL                     string `json:"url"`
	MinBalance              int    `json:"min_balance"`
	BalanceTolerancePercent int    `json:"balance_tolerance_percent"`
	PayoutIntervalSeconds   int    `json:"payout_interval_seconds"`
	MinPayoutAmount         int    `json:"min_payout_amount"`
	PricePerStep            int    `json:"price_per_step"`
	PriceUnit               string `json:"price_unit"`
	MinPurchaseSteps        int    `json:"min_purchase_steps"`
}

// prodMintJSONTemplate is the 7 production mints every real deployment gets.
const prodMintJSONTemplate = `{"url":"https://mint.coinos.io","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.minibits.cash/Bitcoin","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.lnserver.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.macadamia.cash","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.westernbtc.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://kashu.me","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.cubabitcoin.org","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0}`

// testMintJSONTemplate is the 2 testnut zero-fee test mints. They fake
// Lightning payments and exist ONLY for E2E purchase testing — never for
// real customer deployments.
const testMintJSONTemplate = `{"url":"https://nofee.testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0}`

// defaultMintsJSON returns the accepted_mints JSON array the wizard writes
// to the router's /etc/tollgate/config.json. ALWAYS the 7 production mints;
// the 2 testnut test mints (fake Lightning, E2E purchase testing only) are
// appended ONLY when includeTestnut is true — test mints are strictly
// opt-in, real customer deployments never get them by default.
func defaultMintsJSON(includeTestnut bool) string {
	if includeTestnut {
		return "[\n    " + prodMintJSONTemplate + ",\n    " + testMintJSONTemplate + "\n  ]"
	}
	return "[\n    " + prodMintJSONTemplate + "\n  ]"
}

// shellQuoteSingle wraps s in single quotes for safe interpolation into a
// router-side shell command, escaping embedded single quotes so a mint URL
// containing a quote/space cannot break out of the quoted token.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// configJqFilter returns the jq filter used by deploy step 7 to merge
// margin, profit_share and mints into /etc/tollgate/config.json.
//
// Semantics:
//   - margin/profit_share factors are overwritten from the deploy payload;
//   - the operator's chosen mint ($mu) is APPENDED if non-empty and not
//     already present (it never replaces the default list);
//   - every default mint ($dm) not already in accepted_mints is APPENDED
//     (idempotent by URL — re-running a deploy adds nothing new);
//   - a config.json without accepted_mints is handled via (// []).
//
// Fixes vs the previous filter, which NEVER wrote margin/profit_share/mints:
//  1. the dedup check inside `($dm | map(...))` referenced .accepted_mints
//     while `.` was bound to each $dm element (null) → "Cannot iterate over
//     null" on every router; the fix snapshots the CURRENT url list into
//     $have BEFORE appending defaults, and every access to .accepted_mints
//     is null-coalesced with `// []`;
//  2. `($mu != "" and $idx | not)` mis-parsed as `(cond) | not`, inverting
//     the condition so an EMPTY mint appended a {"url":""} entry; the fix
//     parenthesizes `(($mu != "") and (index == null))`;
//  3. the operator mint now crosses the shell SINGLE-QUOTED (see
//     shellQuoteSingle) — unquoted, an empty --arg value made jq consume the
//     filter itself as $mu, and a mint with a space/shell char corrupted the
//     whole command.
//
// This filter is exercised end-to-end against local jq by
// TestMintConfigJqMerge (merge, dedup, idempotency, // [] fallback).
func configJqFilter() string {
	return ".margin=$m | " +
		"(.profit_share[] | select(.identity == \"owner\") | .factor) = $of | " +
		"(.profit_share[] | select(.identity == \"developer\") | .factor) = $df | " +
		// Add operator's chosen mint if non-empty and not already present.
		".accepted_mints = (if (($mu != \"\") and (((.accepted_mints // []) | map(.url) | index($mu)) == null)) then " +
		"(.accepted_mints // []) + [{\"url\":$mu,\"min_balance\":64,\"balance_tolerance_percent\":10,\"payout_interval_seconds\":60,\"min_payout_amount\":128,\"price_per_step\":1,\"price_unit\":\"sats\",\"min_purchase_steps\":0}] " +
		"else (.accepted_mints // []) end) | " +
		// Add any default mints that aren't already present (idempotent by
		// URL; map+index instead of unique_by for jq <1.7 on OpenWrt).
		// $have = the CURRENT url list (after the $mu merge above), so a
		// custom mint equal to a default URL is not appended twice.
		"((.accepted_mints // []) | map(.url)) as $have | " +
		".accepted_mints = ((.accepted_mints // []) + ($dm | map(.url as $u | select(($have | index($u)) == null))))"
}

// configJqCmd builds the full router-side command that rewrites
// /etc/tollgate/config.json. Host-computed values ($m/$of/$df/$dm) cross as
// jq --argjson; the operator mint crosses as a single-quoted jq --arg.
func configJqCmd(margin int, ownerFactor, devFactor, mint string, includeTestnut bool) string {
	return "jq --argjson m " + strconv.Itoa(margin) + " " +
		"--argjson of " + ownerFactor + " " +
		"--argjson df " + devFactor + " " +
		"--argjson dm '" + defaultMintsJSON(includeTestnut) + "' " +
		"--arg mu " + shellQuoteSingle(mint) + " " +
		"'" + configJqFilter() + "' " +
		"/etc/tollgate/config.json > /tmp/cfg.tmp 2>&1 && " +
		"mv /tmp/cfg.tmp /etc/tollgate/config.json && echo 'config updated' || echo 'no config'"
}

// deploySteps returns the ordered deployment step definitions.
func deploySteps() []Step {
	return []Step{
		{Name: "verify", Desc: "Verifying SSH access to router...", Status: "pending"},
		{Name: "firmware", Desc: "Checking firmware version...", Status: "pending"},
		{Name: "password", Desc: "Setting root password...", Status: "pending"},
		{Name: "upstream", Desc: "Configuring upstream connection...", Status: "pending"},
		{Name: "install", Desc: "Installing tollgate-wrt package + patching backend...", Status: "pending"},
		{Name: "brand", Desc: "Branding captive portal as TollGate...", Status: "pending"},
		{Name: "portal", Desc: "Deploying TollGate captive portal...", Status: "pending"},
		{Name: "lnurl", Desc: "Configuring Lightning address...", Status: "pending"},
		{Name: "services", Desc: "Restarting services...", Status: "pending"},
		{Name: "health", Desc: "Running health check...", Status: "pending"},
	}
}

// runDeployment executes the full deployment sequence.
func runDeployment(job *Job, req deployRequest) {
	client := sshConnect(req.IP, req.Password)
	if client == nil && req.Password != "" {
		// If password auth failed, try key auth
		client = sshConnect(req.IP, "")
	}
	if client == nil {
		job.mu.Lock()
		job.Status = "failed"
		job.Error = "Cannot connect to router via SSH"
		job.mu.Unlock()
		return
	}
	// Closure (not `defer client.Close()`): client can be re-assigned when
	// the STA step re-establishes the session after a wifi reload — the
	// deferred call must close whichever client is live at the end.
	defer func() { client.Close() }()

	// Step 0: Verify SSH
	job.setStep(0, "running", "")
	fwOut := sshRun(client, "cat /etc/openwrt_release 2>/dev/null || cat /etc/openwrt_version 2>/dev/null || echo 'not openwrt'")
	fwOut = strings.TrimSpace(fwOut)
	if fwOut == "not openwrt" || fwOut == "" {
		job.setStep(0, "failed", "Router is not running OpenWrt")
		job.mu.Lock()
		job.Status = "failed"
		job.Error = "Router is not running OpenWrt firmware"
		job.mu.Unlock()
		return
	}
	job.addLog("SSH OK. Firmware: " + truncate(fwOut, 100))
	job.setStep(0, "done", truncate(fwOut, 100))
	time.Sleep(500 * time.Millisecond)

	// Step 1: Check firmware
	job.setStep(1, "running", "")
	versionLine := ""
	for _, line := range strings.Split(fwOut, "\n") {
		if strings.Contains(line, "DISTRIB_DESCRIPTION") {
			parts := strings.SplitN(line, "'", 2)
			if len(parts) > 1 {
				versionLine = strings.Trim(parts[1], "'")
			}
		}
	}
	job.addLog("Firmware: " + versionLine)
	job.setStep(1, "done", versionLine)
	time.Sleep(500 * time.Millisecond)

	// Step 2: Set root password
	job.setStep(2, "running", "")
	if req.Password != "" {
		// The password crosses as a base64 carrier — the plaintext never
		// appears in the SSH command string (see passwdCommand).
		passwdCmd := passwdCommand(req.Password)
		passwdOut := sshRun(client, passwdCmd)
		if strings.Contains(passwdOut, "changed") || strings.Contains(passwdOut, "successfully") {
			job.addLog("Root password set")
			job.setStep(2, "done", "password updated")
		} else {
			job.addLog("Password set (may already be set)")
			job.setStep(2, "done", "password set")
		}
	} else {
		job.setStep(2, "done", "skipped (no password)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 3: Configure upstream (WiFi STA if requested)
	job.setStep(3, "running", "")
	if req.Mode == "sta" && req.SSID != "" {
		if !configureSTA(job, &client, req.IP, req.Password, req.SSID, req.WifiPass) {
			return
		}
	} else {
		job.addLog("Using WAN upstream (default)")
		job.setStep(3, "done", "WAN mode (default)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 4: Install tollgate package from GitHub releases
	// OpenWrt 25+ uses apk; OpenWrt 24.x uses opkg. Detect at runtime.
	job.setStep(4, "running", "")
	pkgMgr := strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))

	// Select appropriate package URL based on package manager
	// OpenWrt 25.12+ uses APK and cannot install legacy .ipk packages
	var selectedPkgURL string
	var pkgExtension string
	if pkgMgr == "apk" {
		selectedPkgURL = tollgatePkgAPKURL
		pkgExtension = ".apk"
		job.addLog("OpenWrt 25+ detected with APK package manager")
	} else {
		selectedPkgURL = tollgatePkgURL
		pkgExtension = ".ipk"
		job.addLog("OpenWrt <=24.x detected with OPKG package manager")
	}

	// MT3000-class routers have no RTC — after a cold boot the clock is far
	// in the past and router-side TLS to github.com fails cert validation.
	// Sync from the laptop clock before any router-side download attempt.
	sshRun(client, "date -s @"+strconv.FormatInt(time.Now().Unix(), 10)+" >/dev/null 2>&1; true")

	// PRIMARY: download the package on the LAPTOP and push it over SSH stdin.
	// This eliminates the router's DNS/TLS stack from the critical path —
	// a freshly STA-connected router often has no working DNS yet.
	job.addLog("Downloading tollgate-wrt v0.7.0-alpha10 " + pkgExtension + " (laptop-side)...")
	pkgOnRouter := false
	if data, err := httpGetFile(selectedPkgURL); err == nil && len(data) > 0 {
		push := sshUploadPipe(client, data, "cat > /tmp/tollgate-wrt"+pkgExtension+" && echo PUSH_OK")
		if strings.Contains(push, "PUSH_OK") {
			pkgOnRouter = true
			job.addLog(fmt.Sprintf("Package downloaded on laptop (%d KB), pushed to router via SSH", len(data)/1024))
		} else {
			job.addLog("SSH push failed: " + truncate(push, 80))
		}
	} else if err != nil {
		job.addLog("Laptop download failed: " + truncate(err.Error(), 80) + " — falling back to router-side wget")
	}

	// FALLBACK: router-side wget, with a real DNS probe and wget's stderr
	// logged so the job log shows WHY it fails (DNS vs TLS/clock vs routing)
	// instead of a silent empty file.
	if !pkgOnRouter {
		probe := sshRun(client, "nslookup github.com 2>&1 | tail -n2")
		job.addLog("Router DNS probe: " + truncate(probe, 60))
		wgetOut := sshRun(client, "wget -O /tmp/tollgate-wrt"+pkgExtension+" '"+selectedPkgURL+"' 2>&1; [ -s /tmp/tollgate-wrt"+pkgExtension+"] ] && echo WGET_OK || echo WGET_FAIL")
		job.addLog("wget: " + truncate(wgetOut, 120))
		if strings.Contains(wgetOut, "WGET_OK") {
			pkgOnRouter = true
		}
	}

	installedOK := false
	if pkgOnRouter {
		job.addLog("Installing package via " + pkgMgr + "...")
		sshRun(client, "rm -f /var/lock/opkg.lock 2>/dev/null")
		// Install nodogsplash + jq prerequisites BEFORE the tollgate-wrt .ipk so
		// opkg's dependency resolver doesn't fail on a fresh OpenWrt that
		// doesn't have them pre-installed. Try opkg feed first; if that fails
		// (nodogsplash not in default feeds on fresh 24.10.4), download the
		// .ipk files from the OpenWrt package repo on the laptop and push them
		// to the router via SSH — same pattern as the tollgate-wrt .ipk.
		if pkgMgr != "apk" {
			ndsUpdate := sshRun(client, "opkg update 2>&1")
			job.addLog("opkg update (prereq): " + truncate(ndsUpdate, 60))
			ndsInstall := sshRun(client, "opkg install nodogsplash jq 2>&1 | tail -5")
			if strings.Contains(ndsInstall, "installed") || strings.Contains(ndsInstall, "already") {
				job.addLog("nodogsplash+jq installed via opkg feed: " + truncate(ndsInstall, 80))
			} else {
				job.addLog("opkg feed install failed, downloading .ipk from OpenWrt repo...")
				// Download nodogsplash + jq .ipk from OpenWrt package repo
				// on laptop, push to router, install. nodogsplash is in the
				// routing/ subdirectory, jq is in packages/.
				baseURL := "https://downloads.openwrt.org/releases/24.10.4/packages/aarch64_cortex-a53/"
				routingURL := baseURL + "routing/"
				packagesURL := baseURL + "packages/"
				ndsListHTML := string(httpGetFileOrEmpty(routingURL))
				jqListHTML := string(httpGetFileOrEmpty(packagesURL))
				ndsPkg := extractIPKFilename(ndsListHTML, "nodogsplash")
				jqPkg := extractIPKFilename(jqListHTML, "jq")
				if ndsPkg != "" {
					ndsData, ndsErr := httpGetFile(routingURL + ndsPkg)
					if ndsErr == nil && len(ndsData) > 1000 {
						pushNds := sshUploadPipe(client, ndsData, "cat > /tmp/"+ndsPkg+" && echo NDS_PUSHED")
						if strings.Contains(pushNds, "NDS_PUSHED") {
							job.addLog(fmt.Sprintf("nodogsplash .ipk downloaded (%d KB), pushed to router", len(ndsData)/1024))
						}
					}
				}
				if jqPkg != "" {
					jqData, jqErr := httpGetFile(packagesURL + jqPkg)
					if jqErr == nil && len(jqData) > 1000 {
						pushJq := sshUploadPipe(client, jqData, "cat > /tmp/"+jqPkg+" && echo JQ_PUSHED")
						if strings.Contains(pushJq, "JQ_PUSHED") {
							job.addLog(fmt.Sprintf("jq .ipk downloaded (%d KB), pushed to router", len(jqData)/1024))
						}
					}
				}
				manualInstall := sshRun(client, "opkg install /tmp/nodogsplash_*.ipk /tmp/jq_*.ipk 2>&1 | tail -5")
				job.addLog("nodogsplash+jq manual install: " + truncate(manualInstall, 80))
			}
		}
		// opkg does lexical version compare — 'v0.5.0' > 'main.56...' so it refuses
		// to downgrade unless forced. --force-reinstall ensures the files land even
		// if opkg thinks the package is already present. Detect "Not downgrading"
		// in the output as a hard failure regardless of binary existence.
		installCmd := "opkg install --force-downgrade --force-reinstall --force-overwrite --force-depends /tmp/tollgate-wrt" + pkgExtension + " 2>&1 | tail -5"
		if pkgMgr == "apk" {
			// apk has no downgrade refusal, but --force-overwrite guards against
			// existing-file conflicts on reinstall.
			installCmd = "apk add --allow-untrusted --force-overwrite /tmp/tollgate-wrt" + pkgExtension + " 2>&1 | tail -5"
		}
		installOut := sshRun(client, installCmd)
		job.addLog("Package installed (" + pkgMgr + "): " + truncate(installOut, 100))
		// If opkg refuses to downgrade, the OLD binary stays and the new config
		// will crash against it — treat as a hard failure even if the binary exists.
		if strings.Contains(installOut, "Not downgrading") {
			job.addLog("ERROR: opkg refused to downgrade the package (old version kept)")
			job.setStep(4, "error", "opkg refused to downgrade tollgate-wrt")
			return
		}
		// Verify the binary actually exists (secondary check)
		verifyOut := sshRun(client, "ls /usr/bin/tollgate-wrt 2>/dev/null || ls /usr/sbin/tollgate-wrt 2>/dev/null || which tollgate-wrt 2>/dev/null || echo 'NOT FOUND'")
		if !strings.Contains(verifyOut, "NOT FOUND") {
			// NOTE (SW4a): the fw4/nftables enforcement rules (PR #283) ship
			// inside the package under /etc/nftables.d/{20-nds-enforce,30-backend-firewall}.nft —
			// no separate overlay download is performed (the old overlay URL 404'd).
			job.setStep(4, "done", "tollgate-wrt installed via "+pkgMgr)
			installedOK = true
		}
	}
	if !installedOK {
		// Last resort: feed install (requires the router to already have
		// working internet — usually not the case on a fresh STA uplink).
		job.addLog("Package not on router — trying " + pkgMgr + " feed...")
		var installOut string
		if pkgMgr == "apk" {
			installOut = sshRun(client, "apk update >/dev/null 2>&1; apk add "+tollgatePackage+" 2>&1 | tail -5")
		} else {
			installOut = sshRun(client, "rm -f /var/lock/opkg.lock 2>/dev/null; opkg update >/dev/null 2>&1; opkg install "+tollgatePackage+" 2>&1 | tail -5")
		}
		job.addLog("Feed install: " + truncate(installOut, 100))
		verifyOut := sshRun(client, "which tollgate-wrt 2>/dev/null || echo 'NOT FOUND'")
		if strings.Contains(verifyOut, "NOT FOUND") {
			// STA config + radio changes are live at this point but the
			// deploy is dead — restore the wireless snapshot so the router
			// is left in its pre-deploy state.
			job.addLog("Rolling back wireless config (pre-deploy snapshot)...")
			rollbackWireless(client)
			jobFail(job, 4, "tollgate-wrt install failed", "Package installation failed — wireless config rolled back")
			return
		}
		job.setStep(4, "done", tollgatePackage+" installed (feed, "+pkgMgr+")")
	}

	// The .ipk now ships gonuts v0.11.1 with all keyset/multimint/existing-wallet
	// fixes built in — no binary replacement needed.
	time.Sleep(500 * time.Millisecond)

	// Step 5: Brand as TollGate — hostname, SSID, DNS, nodogsplash config
	job.setStep(5, "running", "")
	// Generate unique suffix (e.g. tollgate-a7f2) so multiple routers don't clash
	const ssidChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	suffix := make([]byte, 4)
	randBytes := make([]byte, 4)
	if _, err := cryptorand.Read(randBytes); err != nil {
		// Fallback: time-seeded
		for i := range randBytes {
			randBytes[i] = byte(time.Now().UnixNano() >> uint(i*8))
		}
	}
	for i := range suffix {
		suffix[i] = ssidChars[int(randBytes[i])%len(ssidChars)]
	}
	// SSID/hostname pattern: "TollGate-" + 4 random chars (ALLCAPS, no lowercase).
	nodeName := "tollgate-" + string(suffix)
	job.addLog("Branding as " + nodeName + "...")

	// Get router LAN IP first (needed for DNS entries)
	routerIP := sshRun(client, "uci -q get network.lan.ipaddr 2>/dev/null | tr -d \"'\" | awk '{print $1}'")
	routerIP = strings.TrimSpace(routerIP)
	if routerIP == "" {
		routerIP = "192.168.8.1"
	}
	job.addLog("Router LAN IP: " + routerIP)

	// Deduplicate /etc/hosts entries, then write fresh ones
	hostsCmd := "sed -i '/tollgate\\.lan/d; /tollgate\\.local/d' /etc/hosts && " +
		"echo '" + routerIP + " tollgate.lan tollgate.local' >> /etc/hosts"

	// Try to install mdnsd for .local mDNS support (non-fatal if unavailable)
	mdnsCmd := "opkg update >/dev/null 2>&1 && opkg install mdnsd >/dev/null 2>&1 && /etc/init.d/mdnsd enable 2>/dev/null; /etc/init.d/mdnsd start 2>/dev/null; echo ok"

	brandOut := sshRun(client, strings.Join([]string{
		// Hostname
		"uci -q set system.@system[0].hostname='" + nodeName + "'",
		// WiFi SSID — only on default_radio* (public captive portal WiFi)
		// Skip private_radio* (admin LAN) and *_uplink (WAN repeater)
		"for i in $(uci -q show wireless 2>/dev/null | grep 'default_radio.*=wifi-iface' | awk -F. '{print $2}' | awk -F= '{print $1}'); do uci -q set wireless.$i.ssid='" + nodeName + "'; done",
		// DNS: deduplicated /etc/hosts entries
		hostsCmd,
		// Ensure dnsmasq serves .lan domain
		"uci -q set dhcp.@dnsmasq[0].domain='lan'",
		"uci -q set dhcp.@dnsmasq[0].local='/lan/'",
		// dnsmasq address records (belt-and-suspenders with /etc/hosts)
		"uci -q del_list dhcp.@dnsmasq[0].address='/tollgate.lan/" + routerIP + "' 2>/dev/null; uci -q add_list dhcp.@dnsmasq[0].address='/tollgate.lan/" + routerIP + "'",
		// DHCP: push router as DNS server to all DHCP clients (option 6)
		// This is what makes .lan domains resolve on connected devices
		"uci -q del_list dhcp.lan.dhcp_option='6," + routerIP + "' 2>/dev/null; uci -q add_list dhcp.lan.dhcp_option='6," + routerIP + "'",
		// dnsmasq: expand /etc/hosts entries with domain suffix
		"uci -q set dhcp.@dnsmasq[0].expandhosts='1'",
		"uci -q set dhcp.@dnsmasq[0].readethers='1'",
		// network: set domain on lan interface
		"uci -q set network.lan.domain='lan'",
		// NoDogSplash config
		"uci -q set nodogsplash.@nodogsplash[0].gatewayname='" + nodeName + "'",
		// Rebrand gateway domain to tollgate.lan so the captive portal serves
		// on tollgate.lan (DNS already resolves it).
		"uci -q set nodogsplash.@nodogsplash[0].gatewaydomainname='tollgate.lan'",
		"uci -q set nodogsplash.@nodogsplash[0].enabled='1'",
		"uci -q set nodogsplash.@nodogsplash[0].clientid='mac'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2121' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2121'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2050' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2050'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2051' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 2051'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 80' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 80'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8080' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8080'",
		"uci -q del_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8090' 2>/dev/null; uci -q add_list nodogsplash.@nodogsplash[0].users_to_router='allow tcp port 8090'",
		// Commit all
		"uci commit system",
		"uci commit wireless",
		"uci commit dhcp",
		"uci commit network",
		"uci commit nodogsplash",
		// Enable radios (OpenWrt ships with wifi disabled by default)
		"uci -q set wireless.radio0.disabled='0' 2>/dev/null; true",
		"uci -q set wireless.radio1.disabled='0' 2>/dev/null; true",
		"uci commit wireless",
		"/etc/init.d/nodogsplash enable",
		"/etc/init.d/nodogsplash restart 2>/dev/null || true",
		"/etc/init.d/dnsmasq restart 2>/dev/null || true",
		// Apply wireless config (wifi reload applies UCI, wifi starts if not running)
		"wifi reload 2>/dev/null || wifi 2>/dev/null || true",
		"echo 'branded'",
	}, " && "))
	// Install mdnsd for .local (non-fatal, runs separately)
	mdnsOut := sshRun(client, mdnsCmd)
	if strings.Contains(mdnsOut, "ok") {
		job.addLog("mDNS (.local) support: mdnsd installed/enabled")
	} else {
		job.addLog("mDNS (.local) support: not available (opkg may not have mdnsd)")
	}
	if strings.Contains(brandOut, "branded") {
		job.addLog("Branded: hostname=" + nodeName + ", SSID=" + nodeName + ", DNS=tollgate.lan")
		job.setStep(5, "done", "hostname+SSID+DNS+nodogsplash")
	} else {
		job.addLog("Branding attempted: " + truncate(brandOut, 60))
		job.setStep(5, "done", "configured (partial)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 6: Verify the captive portal shipped by the tollgate-wrt package.
	// The wizard no longer embeds a portal/ directory — the tollgate-wrt
	// .ipk installs tollgate-captive-portal-site via its uci-defaults, so
	// this step is a lightweight verification that the portal is present.
	job.setStep(6, "running", "")
	portalCheck := sshRun(client, "test -d /etc/tollgate/tollgate-captive-portal-site && echo ok || echo missing")
	if strings.TrimSpace(portalCheck) == "ok" {
		job.addLog("Captive portal present at /etc/tollgate/tollgate-captive-portal-site")
		job.setStep(6, "done", "portal shipped by tollgate-wrt package")
	} else {
		job.addLog("WARNING: captive portal directory not found on router")
		job.setStep(6, "done", "portal not found (installed by .ipk)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 7: Configure Lightning address + advanced defaults.
	// lightning_address goes into identities.json → public_identities[].lightning_address
	// (per tollgate-module-basic-go's schema — it reads ONLY from identities.json,
	// never from config.json). margin and profit_share factors go into config.json.
	// If files are absent (tollgate not yet installed), we skip gracefully.
	job.setStep(7, "running", "")

	// 8a: Write lightning_address to identities.json (owner identity).
	lnCmd := "jq --arg la '" + req.LNURL + "' " +
		"'(.public_identities[] | select(.name == \"owner\") | .lightning_address) = $la' " +
		"/etc/tollgate/identities.json > /tmp/ident.tmp 2>&1 && " +
		"mv /tmp/ident.tmp /etc/tollgate/identities.json && echo 'identities updated' || echo 'no identities'"
	lnOut := sshRun(client, lnCmd)

	// 8b: Write margin + profit_share to config.json.
	// Also ensure the default mints are present (idempotent):
	//   7 production mints always;
	//   2 testnut zero-fee test mints ONLY when the deploy payload opts in
	//   (req.TestMints — E2E purchase testing, never a real customer default).
	// Does NOT strip minibits (DLEQ keyset rotation bug fixed in gonuts v0.11.1).
	devSplit := clamp(req.DevSplit, 0, 50)
	margin := clamp(req.Margin, 0, 100)
	ownerFactor := strconv.FormatFloat(1.0-float64(devSplit)/100.0, 'f', 4, 64)
	devFactor := strconv.FormatFloat(float64(devSplit)/100.0, 'f', 4, 64)
	cfgCmd := configJqCmd(margin, ownerFactor, devFactor, req.Mint, req.TestMints)
	cfgOut := sshRun(client, cfgCmd)

	if strings.Contains(lnOut, "identities updated") {
		job.addLog("identities.json: lightning_address=" + req.LNURL + " for owner")
	}
	if strings.Contains(cfgOut, "config updated") {
		job.addLog("config.json: margin=" + strconv.Itoa(margin) + "%, devSplit=" + strconv.Itoa(devSplit) + "% (profit_share updated)")
		mintsLog := "config.json: mints configured (coinos, minibits, lnserver, macadamia, westernbtc, kashu, cubabitcoin)"
		if req.TestMints {
			mintsLog += " + testnut x2 (E2E test mints — opt-in)"
		}
		job.addLog(mintsLog)
	}

	// 8c: Default mints already injected in 8b above (accepted_mints array).

	if strings.Contains(lnOut, "identities updated") || strings.Contains(cfgOut, "config updated") {
		job.setStep(7, "done", "LNURL: "+req.LNURL)
	} else {
		job.addLog("Config update skipped — no tollgate files found")
		job.addLog("identities: " + truncate(lnOut, 60))
		job.addLog("config: " + truncate(cfgOut, 60))
		job.setStep(7, "done", "skipped (no tollgate config)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 9: Restart services
	job.setStep(8, "running", "")
	job.addLog("Restarting services...")
	// Verify tollgate-wrt init script exists before restart
	initCheck := sshRun(client, "ls /etc/init.d/tollgate-wrt 2>/dev/null && echo 'exists' || echo 'missing'")
	if strings.Contains(initCheck, "missing") {
		job.addLog("ERROR: tollgate-wrt init script not found — package install failed")
		jobFail(job, 8, "tollgate-wrt not installed", "tollgate-wrt init script missing — package install failed")
		return
	}
	svcOut := sshRun(client, strings.Join([]string{
		"/etc/init.d/rpcd restart 2>&1",
		// Use stop||true;start instead of restart — on OpenWrt 25, restart
		// calls "ubus call service delete" which fails if the service was
		// not procd-managed (e.g. after a manual binary swap). stop||true
		// ignores the "Not found" error, then start registers it fresh.
		"/etc/init.d/tollgate-wrt stop 2>/dev/null; /etc/init.d/tollgate-wrt start 2>&1",
		"/etc/init.d/nodogsplash restart 2>&1",
		"/etc/init.d/uhttpd restart 2>&1",
		"sleep 3",
		"echo 'services restarted'",
	}, "; "))
	job.addLog("Services restarted: " + truncate(svcOut, 60))
	job.setStep(8, "done", "tollgate-wrt+nodogsplash+uhttpd")
	time.Sleep(500 * time.Millisecond)

	// Step 10: Health check
	job.setStep(9, "running", "")
	job.addLog("Running health check...")
	// Retry health check up to 5 times — a single wget 3.5s after service
	// restart is too fast: the freshly-installed binary may still be starting,
	// or an old crashing binary may need time before it fails to bind :2121.
	healthOK := false
	var healthOut string
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(2 * time.Second)
		healthOut = sshRun(client, "wget -qO- http://127.0.0.1:2121/ 2>/dev/null | head -c 100 || echo 'health check failed'")
		if strings.Contains(healthOut, "kind") || strings.Contains(healthOut, "metric") || strings.Contains(healthOut, "pubkey") {
			healthOK = true
			job.addLog(fmt.Sprintf("Health check passed on attempt %d", attempt))
			break
		}
		job.addLog(fmt.Sprintf("Health check attempt %d failed, retrying...", attempt))
	}
	if healthOK {
		job.addLog("Health check passed — TollGate API responding")
		job.setStep(9, "done", "API healthy on :2121")
	} else {
		job.addLog("Health check FAILED: " + truncate(healthOut, 80))
		// Roll back wireless config so the router's radios are usable for
		// re-scanning after a failed deploy (e.g. old binary crashed with
		// new config, leaving radio0 stuck in STA mode).
		job.addLog("Rolling back wireless config to pre-deploy state...")
		rollbackWireless(client)
		job.addLog("Wireless config restored — radios should be available for scanning")
		jobFail(job, 9, "tollgate API not responding on :2121", "Health check failed — wireless config rolled back for recovery")
		return
	}

	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()
	job.addLog("TollGate deployment complete!")
}

// jobFail marks step as failed and the whole job as failed. (Steps that
// previously only did setStep(i,"error") + return left job.Status "running"
// forever — the wizard UI would spin with no error shown.)
func jobFail(job *Job, step int, stepDetail, jobErr string) {
	job.setStep(step, "failed", stepDetail)
	job.mu.Lock()
	job.Status = "failed"
	job.Error = jobErr
	job.mu.Unlock()
}

// staSetupScript returns the shell script that configures the
// tollgate_uplink STA iface on the target radio (radio0 if present, else
// the first wifi-device found).
//
// CRITICAL (dual-STA guard): a radio can host only ONE STA interface — a
// second one kills the router's wireless entirely. Any existing STA iface
// on the target radio (including a previous tollgate_uplink on re-run) is
// DISABLED — not deleted — before the new uplink is added. Disabling keeps
// the old section recoverable by the operator.
//
// The script snapshots /etc/config/wireless to /tmp for rollback (see
// rollbackWireless) and performs a single commit pair; the caller applies
// the whole change set with ONE `wifi reload`.
func staSetupScript(ssid, wifiKey string) string {
	// ssid/wifiKey cross as base64 carriers and are decoded into shell
	// variables before any uci call — the plaintext never appears in the
	// script text itself (keeps the STA passphrase out of the SSH command
	// string and makes shell injection through the SSID/key impossible).
	return `
sta_ssid=$(echo ` + shellB64(ssid) + ` | base64 -d)
sta_key=$(echo ` + shellB64(wifiKey) + ` | base64 -d)
target=radio0
uci -q get wireless.radio0 >/dev/null 2>&1 || target=$(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)=wifi-device$/\1/p" | head -n1)
if [ -z "$target" ]; then echo 'NO_RADIO'; exit 0; fi
cp /etc/config/wireless /tmp/wireless.pre-tollgate &&
for s in $(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)\.device='$target'$/\1/p"); do
	if [ "$(uci -q get wireless.$s.mode 2>/dev/null)" = 'sta' ]; then uci -q set wireless.$s.disabled='1'; fi
done &&
uci set wireless.tollgate_uplink=wifi-iface &&
uci set wireless.tollgate_uplink.network='wwan' &&
uci set wireless.tollgate_uplink.device="$target" &&
uci set wireless.tollgate_uplink.mode='sta' &&
uci set wireless.tollgate_uplink.ssid="$sta_ssid" &&
uci set wireless.tollgate_uplink.encryption='psk2' &&
uci set wireless.tollgate_uplink.key="$sta_key" &&
uci set wireless.tollgate_uplink.disabled='0' &&
uci set network.wwan=interface &&
uci set network.wwan.proto='dhcp' &&
uci commit wireless &&
uci commit network &&
echo "STA_CFG_OK target=$target"`
}

// rollbackWireless restores the /etc/config/wireless snapshot taken before
// STA changes and reloads wifi, returning the router to its pre-deploy
// wireless state. Safe to call when no snapshot exists (no-op).
func rollbackWireless(client *ssh.Client) {
	sshRun(client, "[ -f /tmp/wireless.pre-tollgate ] && cp /tmp/wireless.pre-tollgate /etc/config/wireless && uci commit wireless && (wifi reload 2>/dev/null || wifi 2>/dev/null); true")
}

// reconnectSSH retries sshConnect (radios may be restarting after a wifi
// reload, so the first attempts can time out). Falls back to empty-password
// auth like the initial connect.
func reconnectSSH(ip, password string, attempts int, delay time.Duration) *ssh.Client {
	for i := 0; i < attempts; i++ {
		time.Sleep(delay)
		c := sshConnect(ip, password)
		if c == nil && password != "" {
			c = sshConnect(ip, "")
		}
		if c != nil {
			return c
		}
	}
	return nil
}

// ifaceUp parses `ubus call network.interface.<name> status` output and
// reports whether the interface is up. This is the only reliable STA
// verification: grepping iwinfo never matches (kernel interface names are
// not UCI section names), and `network.wireless status | grep up` matches
// ANY radio being up — not the STA association.
func ifaceUp(statusJSON string) bool {
	var st map[string]any
	if err := json.Unmarshal([]byte(statusJSON), &st); err != nil {
		return false
	}
	up, _ := st["up"].(bool)
	return up
}

// configureSTA wires up the tollgate_uplink WiFi STA (deploy step 3).
// Returns false after marking the job failed; any failure AFTER the
// wireless snapshot restores the snapshot and reloads wifi (rollback).
//
// The SSH client is re-established after the single `wifi reload` — the old
// session can go stale while radios restart — and written back through
// pclient so subsequent steps use the live session.
func configureSTA(job *Job, pclient **ssh.Client, ip, password, ssid, wifiPass string) bool {
	client := *pclient
	job.addLog("Configuring WiFi STA uplink: " + ssid)
	out := sshRun(client, staSetupScript(ssid, wifiPass))
	if strings.Contains(out, "NO_RADIO") {
		jobFail(job, 3, "no wireless radio found", "No wifi-device found in UCI — cannot configure STA uplink")
		return false
	}
	if !strings.Contains(out, "STA_CFG_OK") {
		job.addLog("STA configuration failed: " + truncate(out, 120))
		rollbackWireless(client)
		jobFail(job, 3, "STA configuration error", "Failed to configure WiFi STA mode")
		return false
	}
	radio := "radio0"
	if i := strings.Index(out, "target="); i >= 0 {
		radio = strings.TrimPrefix(out[i:], "target=")
		if j := strings.IndexAny(radio, " \n\r"); j >= 0 {
			radio = radio[:j]
		}
	}
	job.addLog("STA configured on " + radio + " (any existing STA on that radio disabled). Applying wifi reload...")

	// ONE reload applies the whole change set.
	sshRun(client, "wifi reload 2>/dev/null || wifi 2>/dev/null || true")

	// The SSH session can drop while radios restart — close it and
	// re-establish (LAN stays up; only the old session may be wedged).
	client.Close()
	newClient := reconnectSSH(ip, password, 3, 5*time.Second)
	if newClient == nil {
		job.addLog("Could not re-establish SSH after wifi reload — attempting rollback")
		// client is dead; rollback needs a live session — try once more
		// with a longer budget.
		if retry := reconnectSSH(ip, password, 2, 10*time.Second); retry != nil {
			rollbackWireless(retry)
			retry.Close()
		}
		jobFail(job, 3, "SSH lost after wifi reload",
			"SSH connection lost after wifi reload and could not be re-established — wireless config rolled back if the router was reachable")
		return false
	}
	*pclient = newClient
	client = newClient

	// Verify the STA actually associated: the wwan network interface must
	// report up via ubus (see ifaceUp for why grep-based checks lie).
	job.addLog("Verifying WiFi STA connection (ubus network.interface.wwan)...")
	var up bool
	for i := 0; i < 15 && !up; i++ { // ~22s budget: association + DHCP
		up = ifaceUp(sshRun(client, "ubus call network.interface.wwan status 2>/dev/null"))
		if !up {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	if !up {
		job.addLog("WiFi STA verification failed — wwan interface not up")
		rollbackWireless(client)
		jobFail(job, 3, "WiFi connection failed — check SSID and password",
			"WiFi STA connection failed for \""+ssid+"\" — check SSID and password (wireless config rolled back)")
		return false
	}
	job.addLog("WiFi STA connected: " + ssid)

	// --- Upstream subnet conflict detection ---
	// If the router's LAN subnet (e.g. 192.168.1.0/24) overlaps with the
	// upstream WiFi subnet that phy0-sta0 just joined, routing breaks: both
	// br-lan and the STA interface are in the same /24, so the router can't
	// reach the upstream gateway. Fix by moving br-lan to a random 10.x.y.1/24.
	upstreamGW := strings.TrimSpace(sshRun(client, "ip route show default 2>/dev/null | grep -E 'phy|wlan|wwan' | awk '{print $3}' | head -1"))
	lanIP := strings.TrimSpace(sshRun(client, "uci -q get network.lan.ipaddr 2>/dev/null | tr -d \"'\" | awk '{print $1}'"))
	if upstreamGW != "" && lanIP != "" {
		lanParts := strings.Split(lanIP, ".")
		gwParts := strings.Split(upstreamGW, ".")
		if len(lanParts) >= 3 && len(gwParts) >= 3 {
			lanPrefix := strings.Join(lanParts[:3], ".")
			gwPrefix := strings.Join(gwParts[:3], ".")
			if lanPrefix == gwPrefix {
				// CONFLICT — change LAN to a random 10.x.y.1/24
				randBytes := make([]byte, 2)
				cryptorand.Read(randBytes)
				newSecond := int(randBytes[0])%200 + 10 // 10-210
				newThird := int(randBytes[1])%200 + 2   // 2-202
				newLanIP := fmt.Sprintf("10.%d.%d.1", newSecond, newThird)

				job.addLog(fmt.Sprintf("LAN subnet conflict with upstream (%s.0/24 == %s.0/24), changing LAN to %s/24", lanPrefix, gwPrefix, newLanIP))

				sshRun(client, strings.Join([]string{
					"uci set network.lan.ipaddr='" + newLanIP + "'",
					"uci commit network",
					"/etc/init.d/network restart 2>/dev/null",
					"sleep 2",
				}, " && "))

				// Network restart drops the SSH session — reconnect.
				// Try the new LAN IP first, then fall back to the original IP.
				client.Close()
				newClient := reconnectSSH(newLanIP, password, 5, 3*time.Second)
				if newClient == nil {
					job.addLog(fmt.Sprintf("Could not reconnect on new LAN IP %s, trying original IP %s...", newLanIP, ip))
					newClient = reconnectSSH(ip, password, 3, 5*time.Second)
				}
				if newClient != nil {
					*pclient = newClient
					client = newClient
					job.addLog(fmt.Sprintf("Reconnected to router on new LAN IP %s", newLanIP))
				} else {
					job.addLog("WARNING: Could not reconnect after LAN IP change — subsequent steps may fail")
				}
			} else {
				job.addLog(fmt.Sprintf("No LAN/upstream subnet conflict (LAN=%s.0/24, upstream=%s.0/24)", lanPrefix, gwPrefix))
			}
		}
	}

	job.setStep(3, "done", "STA mode: "+ssid)
	return true
}

// httpGetFile downloads a release asset on the laptop, following redirects
// (GitHub release URLs redirect to a CDN), with a 60s timeout and a 64 MB
// size guard. This is the PRIMARY package path — pushing the bytes over SSH
// avoids depending on the router's DNS/TLS stack entirely.
func httpGetFile(url string) ([]byte, error) {
	netClient := &http.Client{Timeout: 60 * time.Second}
	resp, err := netClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// httpGetFileOrEmpty is like httpGetFile but returns an empty slice on error
// instead of an error — used for best-effort directory listing fetches where
// a failure just means we can't parse the page (non-fatal).
func httpGetFileOrEmpty(url string) []byte {
	data, err := httpGetFile(url)
	if err != nil {
		return nil
	}
	return data
}

// extractIPKFilename scans an OpenWrt package directory listing (HTML) and
// returns the first .ipk filename that starts with the given package name.
// e.g. extractIPKFilename(html, "nodogsplash") → "nodogsplash_5.0.2-1_aarch64_cortex-a53.ipk"
func extractIPKFilename(html string, pkgName string) string {
	// The listing has entries like: <a href="nodogsplash_5.0.2-1_aarch64_cortex-a53.ipk">
	prefix := pkgName + "_"
	for _, line := range strings.Split(html, "\n") {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := line[idx:]
		end := strings.Index(rest, ".ipk")
		if end < 0 {
			continue
		}
		// Verify the character after .ipk is a quote or end of attribute
		afterIPK := rest[end+4:]
		if len(afterIPK) == 0 || afterIPK[0] == '"' || afterIPK[0] == '\'' || afterIPK[0] == '<' {
			return rest[:end+4]
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// clamp returns n constrained to the inclusive range [lo, hi]. Used to keep
// the advanced defaults (devSplit, margin) within safe bounds regardless of
// what the client sends.
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
