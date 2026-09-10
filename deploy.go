package main

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	// tollgatePackage is the opkg/apk package name for the tollgate-wrt
	// package (installed via the OpenWrt feed fallback path).
	tollgatePackage = "tollgate-wrt"
)

var (
	// tollgate-wrt .ipk download URL (OpenWrt <= 24.10 opkg back-compat).
	//
	// NOTE (v0.5.0 de-brand): the tollgate-wrt package now ships from the
	// OpenTollGate org's tollgate-module-basic-go releases. The nftables
	// enforcement rules (PR #283) ship INSIDE this ipk under
	// ./etc/nftables.d/, so no separate overlay download is needed.
	//
	// NOTE (feat/feed-per-arch-urls): the per-arch selectable URLs are derived
	// generically in arch.go via feedAssetURL (feed-primary, GitHub fallback).
	// These two vars are the aarch64_cortex-a53 PRIMARY feed assets, used by
	// the PreStage cache (stageAssetURLs) to pre-download the bench arch in both
	// formats before arch detection runs at install time. They are DERIVED from
	// feedAssetURL so they can never drift from the generic URL builder.
	tollgatePkgURL = feedAssetURL("aarch64_cortex-a53", ".ipk")
	// tollgate-wrt .apk download URL (OpenWrt 25+ with APK support).
	// OpenWrt 25.12+ cannot install legacy .ipk (ar archive) packages.
	tollgatePkgAPKURL = feedAssetURL("aarch64_cortex-a53", ".apk")
)

// deploySteps returns the ordered deployment step definitions.
func deploySteps() []Step {
	return []Step{
		{Name: "verify", Desc: "Verifying SSH access to router...", Status: "pending"},
		{Name: "stage", Desc: "Pre-downloading packages and firmware...", Status: "pending"},
		{Name: "flash", Desc: "Flashing OpenWrt on stock GL.iNet...", Status: "pending"},
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
	isStockGL := false
	var glModel string
	if fwOut == "not openwrt" || fwOut == "" {
		// Not OpenWrt — check for stock GL.iNet firmware. If present, we
		// proceed to the flash step (step 2) instead of failing.
		glOut := sshRun(client, "cat /etc/gl-inet-release 2>/dev/null || echo ''")
		glOut = strings.TrimSpace(glOut)
		if glOut != "" {
			isStockGL = true
			glModel, _ = parseGLInetRelease(glOut)
			glModel = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(glModel), " ", "-"))
			if glModel == "" {
				glModel = strings.TrimSpace(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
			}
			job.addLog("Detected stock GL.iNet firmware (model: " + glModel + ")")
			job.setStep(0, "done", "GL.iNet "+glModel)
		} else {
			job.setStep(0, "failed", "Router is not running OpenWrt")
			job.mu.Lock()
			job.Status = "failed"
			job.Error = "Router is not running OpenWrt firmware"
			job.mu.Unlock()
			return
		}
	} else {
		job.addLog("SSH OK. Firmware: " + truncate(fwOut, 100))
		job.setStep(0, "done", truncate(fwOut, 100))
	}
	time.Sleep(500 * time.Millisecond)

	// Step 1: Stage — pre-download deploy assets (optional; req.PreStage).
	// If the operator asked for it, pre-download every asset the deploy will
	// need into the Job's stageCache NOW, while the laptop's current
	// internet path is still up. This matters when the laptop's only
	// internet is via the router being flashed or reconfigured (STA mode):
	// the flash/install steps later consume the staged bytes instead of
	// fetching live. Runs immediately after verify (glModel/isStockGL from
	// the SSH probe are required to pick the right image/package URLs) and
	// before flash. Failures are non-fatal — the existing live-fetch →
	// router-wget → feed fallbacks remain on a cache miss.
	job.setStep(1, "running", "")
	if req.PreStage {
		runPreStage(job, client, isStockGL, glModel)
		job.setStep(1, "done", "deploy assets pre-downloaded")
	} else {
		job.setStep(1, "done", "skipped (no pre-stage requested)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 2: Flash OpenWrt on stock GL.iNet (skipped if already OpenWrt)
	job.setStep(2, "running", "")
	if isStockGL {
		job.addLog("Flashing OpenWrt on GL.iNet " + glModel + "...")

		img, ok := glModelMap[glModel]
		if !ok {
			jobFail(job, 2, "Unknown GL.iNet model: "+glModel, "Unknown GL.iNet model "+glModel+". Please update the model table in images.go or flash manually.")
			return
		}

		// Obtain the image on the laptop (not the router — limited storage).
		// USE the staging cache first: when the PreStage step pre-downloaded
		// this exact image URL, flashImageBytes serves the staged bytes with
		// zero network (the offline case staging exists for). On a cache miss
		// it downloads live via downloadWithRetry (3 attempts, exponential
		// backoff) — transient network errors and 5xx are retried; a definitive
		// 4xx (bad image pin) fails immediately. Raw http.Get is NOT used (no
		// timeout/redirect/size handling).
		imageURL := img.URL()
		imageData, err := flashImageBytes(job, imageURL)
		if err != nil {
			jobFail(job, 2, "Download failed after 3 attempts: "+err.Error(), "Failed to download OpenWrt image after 3 attempts: "+err.Error()+"\nURL: "+imageURL)
			return
		}

		// Push the image to the router via the SSH stdin pipe.
		// USE sshUploadPipe (ssh.go:74) — same pattern as the package-install
		// step (deploy.go:157). A function called "sshWrite" does NOT exist.
		pushOut := sshUploadPipe(client, imageData, "cat > /tmp/openwrt-sysupgrade.bin && echo PUSH_OK")
		if !strings.Contains(pushOut, "PUSH_OK") {
			// Push failed — check router storage space so the operator knows
			// whether it's a full /tmp (common on small-flash GL.iNet boards)
			// vs a network/SSH problem.
			dfOut := sshRun(client, "df -h /tmp 2>&1")
			jobFail(job, 2, "Image push failed", "Failed to push OpenWrt image to router: "+truncate(pushOut, 80)+"\nRouter /tmp storage:\n"+truncate(dfOut, 200)+"\nIf /tmp is full, free space (remove old images) and retry, or flash manually via GL.iNet recovery mode.")
			return
		}
		job.addLog("Image pushed to router")

		// Run sysupgrade. The router will go down and reboot onto OpenWrt.
		upgradeOut := sshRun(client, "sysupgrade -n /tmp/openwrt-sysupgrade.bin 2>&1")
		job.addLog("sysupgrade: " + truncate(upgradeOut, 200))
		// If sysupgrade returned a recognizable error (image rejected, no
		// space, missing binary), surface it immediately instead of waiting
		// 3 minutes for a router that never reboots.
		if strings.Contains(upgradeOut, "failed") || strings.Contains(upgradeOut, "error") ||
			strings.Contains(upgradeOut, "not found") || strings.Contains(upgradeOut, "invalid") {
			jobFail(job, 2, "sysupgrade failed", parseSysupgradeError(upgradeOut))
			return
		}

		// Wait for the router to reboot. Stock GL.iNet uses 192.168.8.1,
		// OpenWrt defaults to 192.168.1.1. Poll with tcpProbe + reconnectSSH
		// (which retries AND falls back to empty-password auth for the fresh
		// OpenWrt root) until the router comes back on OpenWrt, up to 3min.
		job.addLog("Router rebooting. Waiting for it to come back (up to 3 min)...")
		newIP, newClient, err := waitForRouterAfterFlash(req.IP, req.Password, 3*time.Minute)
		if err != nil {
			// Last-resort fallback: the router may have come back on an
			// unexpected IP (LAN bridge changed the subnet). Scan the /24
			// subnet for any host presenting the OpenWrt banner.
			job.addLog("Router not found on expected IPs. Scanning LAN subnet for OpenWrt...")
			scannedIP := scanSubnetForOpenWrt(req.IP, req.Password, 60*time.Second)
			if scannedIP != "" {
				newClient = reconnectSSH(scannedIP, req.Password, 3, 2*time.Second)
				if newClient != nil {
					newIP = scannedIP
					err = nil
				}
			}
		}
		if err != nil {
			jobFail(job, 2, "Router unreachable after flash", "Router did not come back after flash. Last known IP: "+req.IP+". See manual recovery docs (GL.iNet recovery mode).")
			return
		}
		client.Close()
		client = newClient
		job.addLog("Reconnected to router at " + newIP)
		job.setStep(2, "done", "OpenWrt flashed on "+glModel)
	} else {
		job.setStep(2, "done", "skipped (already OpenWrt)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 3: Check firmware
	job.setStep(3, "running", "")
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
	job.setStep(3, "done", versionLine)
	time.Sleep(500 * time.Millisecond)

	// Step 4: Set root password
	job.setStep(4, "running", "")
	if req.Password != "" {
		passwdCmd := "echo -e '" + req.Password + "\\n" + req.Password + "' | passwd root 2>&1"
		passwdOut := sshRun(client, passwdCmd)
		if strings.Contains(passwdOut, "changed") || strings.Contains(passwdOut, "successfully") {
			job.addLog("Root password set")
			job.setStep(4, "done", "password updated")
		} else {
			job.addLog("Password set (may already be set)")
			job.setStep(4, "done", "password set")
		}
	} else {
		job.setStep(4, "done", "skipped (no password)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 5: Configure upstream (WiFi STA if requested)
	job.setStep(5, "running", "")
	if req.Mode == "sta" && req.SSID != "" {
		if !configureSTA(job, &client, req.IP, req.Password, req.SSID, req.WifiPass) {
			return
		}
	} else {
		job.addLog("Using WAN upstream (default)")
		job.setStep(5, "done", "WAN mode (default)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 6: Install tollgate package from GitHub releases
	// OpenWrt 25+ uses apk; OpenWrt 24.x uses opkg. Detect at runtime.
	job.setStep(6, "running", "")
	pkgMgr := strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))

	// Auto-detect the router's CPU architecture and select the matching
	// tollgate-wrt asset per-arch. Previously the download URL was hardcoded
	// to aarch64_cortex-a53 — on any other router the wrong-arch binary simply
	// won't execute, which is the exact silent failure this removes.
	routerArch := detectArch(client)
	job.addLog("Detected router CPU arch: " + routerArch)
	if routerArch == "" {
		// FAIL LOUDLY on an undetectable arch. Never silently default to
		// aarch64_cortex-a53 — that is the bug being fixed.
		job.addLog("Could not determine router CPU architecture")
		jobFail(job, 6,
			"Could not determine router CPU architecture",
			"Could not determine router CPU architecture")
		return
	}

	// Select appropriate package URL based on package manager + arch.
	// OpenWrt 25.12+ uses APK and cannot install legacy .ipk packages.
	_, pkgExtension, ok := selectPkgURL(routerArch, pkgMgr)
	if !ok {
		// Undetectable arch — fail, never substitute aarch64.
		jobFail(job, 6, "Unsupported CPU arch "+routerArch,
			"Unsupported CPU arch "+routerArch)
		return
	}
	// Candidate download URLs: the generic feed URL first, then the GitHub
	// release fallback (aarch64 only) if one exists. The first that yields
	// bytes wins.
	pkgCandidates := pkgCandidateURLs(routerArch, pkgExtension)
	if pkgExtension == ".apk" {
		job.addLog("OpenWrt 25+ detected with APK package manager (arch " + routerArch + ")")
	} else {
		job.addLog("OpenWrt <=24.x detected with OPKG package manager (arch " + routerArch + ")")
	}

	// MT3000-class routers have no RTC — after a cold boot the clock is far
	// in the past and router-side TLS to github.com fails cert validation.
	// Sync from the laptop clock before any router-side download attempt.
	sshRun(client, "date -s @"+strconv.FormatInt(time.Now().Unix(), 10)+" >/dev/null 2>&1; true")

	// PRIMARY: prefer the tollgate-wrt bytes the PreStage step cached for this
	// exact URL — the point of staging is that the actual install performs zero
	// network fetches. On a cache miss, download the package on the LAPTOP and
	// push it over SSH stdin. Pushing eliminates the router's DNS/TLS stack
	// from the critical path — a freshly STA-connected router often has no
	// working DNS yet. A live-fetch failure falls through to the router-side
	// wget (and then feed) paths below.
	//
	// pkgCandidates holds the ordered URLs to try (feed first, then the GitHub
	// release fallback for aarch64). The first that yields bytes wins.
	pkgOnRouter := false
	var pkgData []byte
	var pkgFromCache bool
	var pkgErr error
	for _, candURL := range pkgCandidates {
		pkgData, pkgFromCache, pkgErr = stagedOrLiveBytes(job, "tollgate-wrt "+pkgExtension, candURL)
		if pkgErr == nil && len(pkgData) > 0 {
			break
		}
		if pkgErr != nil {
			job.addLog("Laptop download failed for " + candURL + ": " + truncate(pkgErr.Error(), 80))
		}
	}
	if pkgErr == nil && len(pkgData) > 0 {
		push := sshUploadPipe(client, pkgData, "cat > /tmp/tollgate-wrt"+pkgExtension+" && echo PUSH_OK")
		if strings.Contains(push, "PUSH_OK") {
			pkgOnRouter = true
			if pkgFromCache {
				job.addLog(fmt.Sprintf("Staged tollgate-wrt %s used from cache (%d KB), pushed to router via SSH", pkgExtension, len(pkgData)/1024))
			} else {
				job.addLog(fmt.Sprintf("Package downloaded on laptop (%d KB), pushed to router via SSH", len(pkgData)/1024))
			}
		} else {
			job.addLog("SSH push failed: " + truncate(push, 80))
		}
	} else if pkgErr != nil {
		job.addLog("Laptop download failed: " + truncate(pkgErr.Error(), 80) + " — falling back to router-side wget")
	}

	// FALLBACK: router-side wget, with a real DNS probe and wget's stderr
	// logged so the job log shows WHY it fails (DNS vs TLS/clock vs routing)
	// instead of a silent empty file.
	if !pkgOnRouter {
		probe := sshRun(client, "nslookup github.com 2>&1 | tail -n2")
		job.addLog("Router DNS probe: " + truncate(probe, 60))
		for _, candURL := range pkgCandidates {
			wgetOut := sshRun(client, "wget -O /tmp/tollgate-wrt"+pkgExtension+" '"+candURL+"' 2>&1; [ -s /tmp/tollgate-wrt"+pkgExtension+" ] && echo WGET_OK || echo WGET_FAIL")
			job.addLog("wget: " + truncate(wgetOut, 120))
			if strings.Contains(wgetOut, "WGET_OK") {
				pkgOnRouter = true
				break
			}
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
				baseURL := "https://downloads.openwrt.org/releases/24.10.4/packages/" + routerArch + "/"
				routingURL := baseURL + "routing/"
				packagesURL := baseURL + "packages/"
				ndsListHTML := string(httpGetFileOrEmpty(routingURL))
				jqListHTML := string(httpGetFileOrEmpty(packagesURL))
				ndsPkg := extractIPKFilename(ndsListHTML, "nodogsplash")
				jqPkg := extractIPKFilename(jqListHTML, "jq")
				if ndsPkg != "" {
					ndsData, ndsFromCache, ndsErr := stagedOrLiveBytes(job, "nodogsplash .ipk", routingURL+ndsPkg)
					if ndsErr == nil && len(ndsData) > 1000 {
						pushNds := sshUploadPipe(client, ndsData, "cat > /tmp/"+ndsPkg+" && echo NDS_PUSHED")
						if strings.Contains(pushNds, "NDS_PUSHED") {
							if ndsFromCache {
								job.addLog(fmt.Sprintf("nodogsplash .ipk used from staging cache (%d KB), pushed to router", len(ndsData)/1024))
							} else {
								job.addLog(fmt.Sprintf("nodogsplash .ipk downloaded (%d KB), pushed to router", len(ndsData)/1024))
							}
						}
					}
				}
				if jqPkg != "" {
					jqData, jqFromCache, jqErr := stagedOrLiveBytes(job, "jq .ipk", packagesURL+jqPkg)
					if jqErr == nil && len(jqData) > 1000 {
						pushJq := sshUploadPipe(client, jqData, "cat > /tmp/"+jqPkg+" && echo JQ_PUSHED")
						if strings.Contains(pushJq, "JQ_PUSHED") {
							if jqFromCache {
								job.addLog(fmt.Sprintf("jq .ipk used from staging cache (%d KB), pushed to router", len(jqData)/1024))
							} else {
								job.addLog(fmt.Sprintf("jq .ipk downloaded (%d KB), pushed to router", len(jqData)/1024))
							}
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
			job.setStep(6, "error", "opkg refused to downgrade tollgate-wrt")
			return
		}
		// Verify the binary actually exists (secondary check)
		verifyOut := sshRun(client, "ls /usr/bin/tollgate-wrt 2>/dev/null || ls /usr/sbin/tollgate-wrt 2>/dev/null || which tollgate-wrt 2>/dev/null || echo 'NOT FOUND'")
		if !strings.Contains(verifyOut, "NOT FOUND") {
			// NOTE (SW4a): the fw4/nftables enforcement rules (PR #283) ship
			// inside the package under /etc/nftables.d/{20-nds-enforce,30-backend-firewall}.nft —
			// no separate overlay download is performed (the old overlay URL 404'd).
			job.setStep(6, "done", "tollgate-wrt installed via "+pkgMgr)
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
			jobFail(job, 6, "tollgate-wrt install failed", "Package installation failed — wireless config rolled back")
			return
		}
		job.setStep(6, "done", tollgatePackage+" installed (feed, "+pkgMgr+")")
	}

	// The .ipk now ships gonuts v0.11.1 with all keyset/multimint/existing-wallet
	// fixes built in — no binary replacement needed.
	time.Sleep(500 * time.Millisecond)

	// Step 7: Brand as TollGate — hostname, SSID, DNS, nodogsplash config
	job.setStep(7, "running", "")
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
		job.setStep(7, "done", "hostname+SSID+DNS+nodogsplash")
	} else {
		job.addLog("Branding attempted: " + truncate(brandOut, 60))
		job.setStep(7, "done", "configured (partial)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 8: Verify the captive portal shipped by the tollgate-wrt package.
	// The wizard no longer embeds a portal/ directory — the tollgate-wrt
	// .ipk installs tollgate-captive-portal-site via its uci-defaults, so
	// this step is a lightweight verification that the portal is present.
	job.setStep(8, "running", "")
	portalCheck := sshRun(client, "test -d /etc/tollgate/tollgate-captive-portal-site && echo ok || echo missing")
	if strings.TrimSpace(portalCheck) == "ok" {
		job.addLog("Captive portal present at /etc/tollgate/tollgate-captive-portal-site")
		job.setStep(8, "done", "portal shipped by tollgate-wrt package")
	} else {
		job.addLog("WARNING: captive portal directory not found on router")
		job.setStep(8, "done", "portal not found (installed by .ipk)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 9: Configure Lightning address + advanced defaults.
	// lightning_address goes into identities.json → public_identities[].lightning_address
	// (per tollgate-module-basic-go's schema — it reads ONLY from identities.json,
	// never from config.json). margin and profit_share factors go into config.json.
	// If files are absent (tollgate not yet installed), we skip gracefully.
	job.setStep(9, "running", "")

	// 8a: Write lightning_address to identities.json (owner identity).
	lnCmd := "jq --arg la '" + req.LNURL + "' " +
		"'(.public_identities[] | select(.name == \"owner\") | .lightning_address) = $la' " +
		"/etc/tollgate/identities.json > /tmp/ident.tmp 2>&1 && " +
		"mv /tmp/ident.tmp /etc/tollgate/identities.json && echo 'identities updated' || echo 'no identities'"
	lnOut := sshRun(client, lnCmd)

	// 8b: Write margin + profit_share to config.json.
	// Also ensure 9 default mints (7 production + 2 testnut zero-fee) are present (idempotent).
	// Does NOT strip minibits (DLEQ keyset rotation bug fixed in gonuts v0.11.1).
	devSplit := clamp(req.DevSplit, 0, 50)
	margin := clamp(req.Margin, 0, 100)
	ownerFactor := strconv.FormatFloat(1.0-float64(devSplit)/100.0, 'f', 4, 64)
	devFactor := strconv.FormatFloat(float64(devSplit)/100.0, 'f', 4, 64)
	defaultMints := `[
    {"url":"https://mint.coinos.io","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.minibits.cash/Bitcoin","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.lnserver.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.macadamia.cash","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.westernbtc.com","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://kashu.me","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://mint.cubabitcoin.org","min_balance":64,"balance_tolerance_percent":10,"payout_interval_seconds":60,"min_payout_amount":128,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://nofee.testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0},
    {"url":"https://testnut.cashu.space","min_balance":0,"balance_tolerance_percent":0,"payout_interval_seconds":999999,"min_payout_amount":999999,"price_per_step":1,"price_unit":"sats","min_purchase_steps":0}
  ]`
	cfgCmd := "jq --argjson m " + strconv.Itoa(margin) + " " +
		"--argjson of " + ownerFactor + " " +
		"--argjson df " + devFactor + " " +
		"--argjson dm '" + defaultMints + "' " +
		"--arg mu " + req.Mint + " " +
		"'.margin=$m | " +
		"(.profit_share[] | select(.identity == \"owner\") | .factor) = $of | " +
		"(.profit_share[] | select(.identity == \"developer\") | .factor) = $df | " +
		// Add operator's chosen mint if non-empty and not already present.
		".accepted_mints = (if ($mu != \"\" and (.accepted_mints | map(.url) | index($mu)) | not) then " +
		".accepted_mints + [{\"url\":$mu,\"min_balance\":64,\"balance_tolerance_percent\":10,\"payout_interval_seconds\":60,\"min_payout_amount\":128,\"price_per_step\":1,\"price_unit\":\"sats\",\"min_purchase_steps\":0}] " +
		"else .accepted_mints end) | " +
		// Add any of the 7 default mints that aren't already present (idempotent by URL).
		// Uses map + index instead of unique_by for jq <1.7 compatibility on OpenWrt.
		".accepted_mints = (.accepted_mints + ($dm | map(select(.url as $u | (.accepted_mints | map(.url) | index($u)) | not))))' " +
		"/etc/tollgate/config.json > /tmp/cfg.tmp 2>&1 && " +
		"mv /tmp/cfg.tmp /etc/tollgate/config.json && echo 'config updated' || echo 'no config'"
	cfgOut := sshRun(client, cfgCmd)

	if strings.Contains(lnOut, "identities updated") {
		job.addLog("identities.json: lightning_address=" + req.LNURL + " for owner")
	}
	if strings.Contains(cfgOut, "config updated") {
		job.addLog("config.json: margin=" + strconv.Itoa(margin) + "%, devSplit=" + strconv.Itoa(devSplit) + "% (profit_share updated)")
		job.addLog("config.json: mints configured (coinos, minibits, lnserver, macadamia, westernbtc, kashu, cubabitcoin, testnut x2)")
	}

	// 8c: Default mints already injected in 8b above (accepted_mints array).

	if strings.Contains(lnOut, "identities updated") || strings.Contains(cfgOut, "config updated") {
		job.setStep(9, "done", "LNURL: "+req.LNURL)
	} else {
		job.addLog("Config update skipped — no tollgate files found")
		job.addLog("identities: " + truncate(lnOut, 60))
		job.addLog("config: " + truncate(cfgOut, 60))
		job.setStep(9, "done", "skipped (no tollgate config)")
	}
	time.Sleep(500 * time.Millisecond)

	// Step 10: Restart services
	job.setStep(10, "running", "")
	job.addLog("Restarting services...")
	// Verify tollgate-wrt init script exists before restart
	initCheck := sshRun(client, "ls /etc/init.d/tollgate-wrt 2>/dev/null && echo 'exists' || echo 'missing'")
	if strings.Contains(initCheck, "missing") {
		job.addLog("ERROR: tollgate-wrt init script not found — package install failed")
		jobFail(job, 10, "tollgate-wrt not installed", "tollgate-wrt init script missing — package install failed")
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
	job.setStep(10, "done", "tollgate-wrt+nodogsplash+uhttpd")
	time.Sleep(500 * time.Millisecond)

	// Step 11: Health check
	job.setStep(11, "running", "")
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
		job.setStep(11, "done", "API healthy on :2121")
	} else {
		job.addLog("Health check FAILED: " + truncate(healthOut, 80))
		// Roll back wireless config so the router's radios are usable for
		// re-scanning after a failed deploy (e.g. old binary crashed with
		// new config, leaving radio0 stuck in STA mode).
		job.addLog("Rolling back wireless config to pre-deploy state...")
		rollbackWireless(client)
		job.addLog("Wireless config restored — radios should be available for scanning")
		jobFail(job, 11, "tollgate API not responding on :2121", "Health check failed — wireless config rolled back for recovery")
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
	return `
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
uci set wireless.tollgate_uplink.ssid='` + ssid + `' &&
uci set wireless.tollgate_uplink.encryption='psk2' &&
uci set wireless.tollgate_uplink.key='` + wifiKey + `' &&
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

// waitForRouterAfterFlash polls for the router to come back after sysupgrade.
// After `sysupgrade -n` the router reboots onto a fresh OpenWrt install whose
// root password is EMPTY, so each candidate IP is probed with tcpProbe and then
// connected via reconnectSSH (which retries AND falls back to empty-password
// auth). The connection is only accepted once it verifies the OpenWrt banner,
// so a stock GL.iNet still mid-reboot is not mistaken for the new install.
// Returns the new IP and a live SSH client, or an error on timeout.
func waitForRouterAfterFlash(originalIP, password string, timeout time.Duration) (string, *ssh.Client, error) {
	deadline := time.Now().Add(timeout)
	// Candidate IPs: the original (stock GL may keep it), the OpenWrt default,
	// and the stock GL default. Dedupe against the original.
	candidates := []string{originalIP}
	for _, ip := range []string{"192.168.1.1", "192.168.8.1"} {
		if ip != originalIP {
			candidates = append(candidates, ip)
		}
	}

	for time.Now().Before(deadline) {
		for _, ip := range candidates {
			if !tcpProbe(ip, 22, 500*time.Millisecond) {
				continue
			}
			// Port 22 is open — try to connect. reconnectSSH retries and
			// falls back to empty-password auth for the fresh OpenWrt root.
			client := reconnectSSH(ip, password, 2, 1*time.Second)
			if client == nil {
				continue
			}
			// Verify it's actually OpenWrt (not stock GL still booting).
			out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null | head -1")
			if strings.Contains(out, "OpenWrt") {
				return ip, client, nil
			}
			client.Close()
		}
		// Only pause between polls if we still have time left.
		if time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
		}
	}
	return "", nil, fmt.Errorf("router did not come back within %v", timeout)
}

// scanSubnetForOpenWrt scans the /24 subnet containing baseIP for a host with
// port 22 open that presents the OpenWrt banner. It is the last-resort fallback
// when the router comes back on an unexpected IP (e.g. the LAN bridge changed
// the subnet). Returns the first matching IP, or "" if none found.
func scanSubnetForOpenWrt(baseIP, password string, timeout time.Duration) string {
	// Derive the /24 prefix from baseIP (e.g. 192.168.1.5 -> 192.168.1).
	parts := strings.Split(baseIP, ".")
	if len(parts) != 4 {
		return ""
	}
	prefix := parts[0] + "." + parts[1] + "." + parts[2] + "."
	deadline := time.Now().Add(timeout)
	for i := 1; i <= 254 && time.Now().Before(deadline); i++ {
		ip := prefix + strconv.Itoa(i)
		if !tcpProbe(ip, 22, 300*time.Millisecond) {
			continue
		}
		client := reconnectSSH(ip, password, 1, 500*time.Millisecond)
		if client == nil {
			continue
		}
		out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null | head -1")
		client.Close()
		if strings.Contains(out, "OpenWrt") {
			return ip
		}
	}
	return ""
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

// configureSTA wires up the tollgate_uplink WiFi STA (deploy step 5).
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
		jobFail(job, 5, "no wireless radio found", "No wifi-device found in UCI — cannot configure STA uplink")
		return false
	}
	if !strings.Contains(out, "STA_CFG_OK") {
		job.addLog("STA configuration failed: " + truncate(out, 120))
		rollbackWireless(client)
		jobFail(job, 5, "STA configuration error", "Failed to configure WiFi STA mode")
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
		jobFail(job, 5, "SSH lost after wifi reload",
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
		jobFail(job, 5, "WiFi connection failed — check SSID and password",
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

	job.setStep(5, "done", "STA mode: "+ssid)
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

// downloadWithRetry downloads url with up to `attempts` tries, backing off
// exponentially (baseDelay * 2^attempt) between failures. Transient network
// errors and HTTP 5xx responses are retried; a definitive 4xx (e.g. 404 for a
// bad image pin) is NOT retried — retrying a 404 wastes time and masks a
// broken URL. Returns the first non-retryable error or the last error.
func downloadWithRetry(url string, attempts int, baseDelay time.Duration) ([]byte, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		data, err := httpGetFile(url)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// Do not retry definitive client errors (404, 403, 410, etc.) — the
		// URL is broken and retrying will not fix it.
		if isDefinitiveHTTPError(err) {
			return nil, err
		}
		if i < attempts-1 {
			time.Sleep(baseDelay * time.Duration(1<<i))
		}
	}
	return nil, lastErr
}

// isDefinitiveHTTPError reports whether err is a non-retryable HTTP client
// error (4xx). Network errors and 5xx are transient and retryable.
func isDefinitiveHTTPError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// httpGetFile returns errors of the form "HTTP 404 Not Found".
	if !strings.HasPrefix(msg, "HTTP ") {
		return false
	}
	// Extract the status code.
	rest := strings.TrimPrefix(msg, "HTTP ")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	code, convErr := strconv.Atoi(fields[0])
	if convErr != nil {
		return false
	}
	return code >= 400 && code < 500
}

// parseSysupgradeError inspects sysupgrade output for common failure modes and
// returns a human-readable, actionable message. Unknown output falls back to a
// generic message with the raw output truncated.
func parseSysupgradeError(out string) string {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "image check failed"),
		strings.Contains(low, "invalid image"),
		strings.Contains(low, "wrong image"),
		strings.Contains(low, "unsupported image"),
		strings.Contains(low, "not a valid sysupgrade"):
		return "sysupgrade rejected the image (incompatible or corrupt). Re-download and retry, or flash manually via GL.iNet recovery mode."
	case strings.Contains(low, "no space left"),
		strings.Contains(low, "not enough space"),
		strings.Contains(low, "insufficient space"),
		strings.Contains(low, "cannot allocate"):
		return "Router storage is full. Free space on /tmp (e.g. remove old images) and retry, or flash manually."
	case strings.Contains(low, "command not found"),
		strings.Contains(low, "sysupgrade: not found"):
		return "sysupgrade is not available on this firmware — it is not a standard OpenWrt install. Flash manually via GL.iNet recovery mode."
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "connection reset"),
		strings.Contains(low, "broken pipe"):
		return "SSH connection dropped during sysupgrade (expected — the router reboots). Waiting for it to come back."
	default:
		return "sysupgrade output: " + truncate(out, 200)
	}
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

// stageAssetURLs returns the ordered list of asset URLs a PreStage run must
// download for the given router state:
//
//   - stock GL.iNet router that will be flashed (isStockGL): the OpenWrt
//     sysupgrade image for the detected model (when known) PLUS the
//     tollgate-wrt package in BOTH formats — the post-flash package manager
//     is only known after the reboot, and staging both guarantees a cache
//     hit whichever one the install step probes.
//   - router already on OpenWrt: only the package format matching its live
//     package manager (apk vs opkg). An empty/unknown pkgMgr stages both so
//     the deploy still works offline.
//
// Unknown GL models contribute no image URL (the flash step reports its own
// actionable "unknown model" error); the package formats are still staged so
// an operator can fix the model table and re-deploy offline.
func stageAssetURLs(isStockGL bool, glModel, pkgMgr string) []string {
	urls := []string{}
	if isStockGL {
		if img, ok := glModelMap[glModel]; ok {
			urls = append(urls, img.URL())
		}
		urls = append(urls, tollgatePkgURL, tollgatePkgAPKURL)
		return urls
	}
	switch pkgMgr {
	case "apk":
		urls = append(urls, tollgatePkgAPKURL)
	case "opkg":
		urls = append(urls, tollgatePkgURL)
	default: // unknown — stage both so a later probe hits the cache
		urls = append(urls, tollgatePkgURL, tollgatePkgAPKURL)
	}
	return urls
}

// runPreStage is the PreStage wiring point called from runDeployment right
// after verify (so glModel/isStockGL are known) and before flash. It probes
// the router's package manager (when the router is already OpenWrt — a stock
// GL.iNet router will be flashed to the pinned OpenWrt release whose package
// manager stageAssetURLs covers by staging both formats), picks the asset
// URLs for the router state, and stages them into the Job's stageCache.
// Failures are logged but non-fatal: the flash/install steps keep their
// live-fetch → router-wget → feed fallbacks on a cache miss.
func runPreStage(job *Job, client *ssh.Client, isStockGL bool, glModel string) {
	pkgMgr := ""
	if !isStockGL && client != nil {
		pkgMgr = strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))
	}
	urls := stageAssetURLs(isStockGL, glModel, pkgMgr)
	if len(urls) == 0 {
		job.addLog("PreStage: nothing to stage for this router state")
		return
	}
	job.addLog(fmt.Sprintf("PreStage: downloading %d asset(s) to staging cache...", len(urls)))
	failed := stageAssets(job, urls)
	if len(failed) > 0 {
		for _, u := range failed {
			job.addLog("PreStage: could not stage " + truncate(u, 100) + " — deploy will fall back to live download")
		}
	}
	staged := len(urls) - len(failed)
	job.addLog(fmt.Sprintf("PreStage: %d/%d asset(s) staged", staged, len(urls)))
}

// stageAssets downloads every URL in urls into the Job's stageCache (keyed
// by the exact URL) unless that URL is already staged. Already-cached URLs
// are skipped — staging is idempotent, so re-running it (e.g. a retried
// deploy sharing the Job) performs zero network fetches. Uses
// downloadWithRetry (3 attempts, 4xx short-circuit) for every asset, the
// same reliability pattern as the flash-image download. Returns the URLs
// that failed to stage; callers log them and continue, because the
// flash/install steps fall back to live fetch → router-side wget → feed on
// a cache miss.
func stageAssets(job *Job, urls []string) []string {
	var failed []string
	for _, u := range urls {
		if u == "" {
			continue
		}
		if _, ok := job.stagedAsset(u); ok {
			continue
		}
		// Disk re-deploy cache (Task 5): if a previous deploy to another
		// router already staged this URL, load the persisted bytes instead of
		// downloading again. Only version-pinned assets are eligible (see
		// persistableDiskAsset) — a package binary is never loaded from disk,
		// so a stale cached package can never shadow a newer release.
		if persistableDiskAsset(u) {
			if data, ok := loadStageDisk(u); ok {
				job.addLog("PreStage: using disk cache for " + truncate(u, 100) + " (no download)")
				job.stageAsset(u, data)
				continue
			}
		}
		data, err := downloadWithRetry(u, 3, 2*time.Second)
		if err != nil || len(data) == 0 {
			failed = append(failed, u)
			continue
		}
		job.stageAsset(u, data)
		// Write through to the disk cache so a later deploy to another router
		// re-uses these bytes. Non-fatal: a read-only HOME or full disk must
		// not fail the deploy — the live-fetch fallback remains for next time.
		if persistableDiskAsset(u) {
			if err := saveStageDisk(u, data); err != nil {
				job.addLog("PreStage: could not persist " + truncate(u, 100) + " to disk cache: " + err.Error())
			}
		}
	}
	return failed
}

// stagedOrLiveBytes returns the bytes for a small deploy asset — the
// tollgate-wrt package (.ipk/.apk), nodogsplash .ipk, or jq .ipk — keyed by
// the EXACT source URL. When the PreStage step cached that URL the bytes are
// served with zero network (the offline-install case staging exists for). On a
// cache miss the asset is fetched live with httpGetFile — packages are small,
// single downloads, so they intentionally do NOT use the heavier
// downloadWithRetry path (that is reserved for the flash image, see
// flashImageBytes in images.go); a live failure here is what triggers the
// install step's router-side wget → feed fallback chain. The second return
// reports whether the bytes came from the cache so callers can log the actual
// acquisition path. A "Downloading ..." log is emitted before any live fetch
// so the operator sees progress during the (up to 60s) download.
func stagedOrLiveBytes(job *Job, label, url string) ([]byte, bool, error) {
	if data, ok := job.stagedAsset(url); ok {
		job.addLog("Using staged " + label + " from cache (no download)")
		return data, true, nil
	}
	job.addLog("Downloading " + label + " (laptop-side)...")
	data, err := httpGetFile(url)
	return data, false, err
}

// ---- On-disk re-deploy cache (~/.tollgate-stage) ----
//
// The Job stageCache is in-memory and per-Job, so a second deploy to a
// different router (a fresh Job) would re-download every asset. Task 5 adds a
// small on-disk cache so re-deploys re-use previously staged binaries.
//
// RISK 3 (consultant): ONLY the version-pinned flash image is persisted.
// Package binaries (tollgate-wrt .ipk/.apk, nodogsplash, jq) can change
// between releases — a stale cached copy would shadow the newer package and
// could install an outdated backend. The flash image URL is pinned to a fixed
// OpenWrt release (openWrtVersion), so its bytes are stable by construction.

// stageDiskDirOverride redirects the on-disk staging cache directory.
// Non-empty in tests to keep the real home directory untouched.
var stageDiskDirOverride = ""

// stageDiskDir returns the on-disk staging cache directory: ~/.tollgate-stage
// (or stageDiskDirOverride, set by tests to keep the real home directory
// untouched). Empty when no home dir exists — callers then skip disk
// persistence (in-memory cache + live fallback only).
func stageDiskDir() string {
	if stageDiskDirOverride != "" {
		return stageDiskDirOverride
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".tollgate-stage")
}

// stageDiskPath returns the on-disk cache file path for an asset URL:
// <cacheDir>/<hex sha256 of url>. Addressing by URL keeps one file per asset
// across deploys and makes corruption detectable by size (see loadStageDisk);
// a changed URL (new release) naturally misses and re-downloads.
func stageDiskPath(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(stageDiskDir(), hex.EncodeToString(sum[:]))
}

// persistableDiskAsset reports whether an asset URL may be written to / read
// from the on-disk re-deploy cache. Default policy: ONLY version-pinned flash
// images from glModelMap qualify — package binaries are never persisted
// (consultant RISK 3, staging plan 2026-09-08). Var so tests can pin the
// policy to a local httptest URL without network access.
var persistableDiskAsset = func(url string) bool {
	for _, img := range glModelMap {
		if img.URL() == url {
			return true
		}
	}
	return false
}

// loadStageDisk returns the persisted bytes for url from the on-disk staging
// cache. ok=false on any miss or when the file is empty/corrupt (size 0) —
// the caller then falls back to a live download.
func loadStageDisk(url string) ([]byte, bool) {
	if stageDiskDir() == "" {
		return nil, false
	}
	path := stageDiskPath(url)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// saveStageDisk persists data for url to the on-disk staging cache, creating
// the cache directory if needed. The write is atomic (temp file + rename) so
// a crash mid-write can never leave a truncated file that a later deploy
// would trust as a complete image.
func saveStageDisk(url string, data []byte) error {
	if stageDiskDir() == "" || len(data) == 0 {
		return nil
	}
	path := stageDiskPath(url)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stage-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
