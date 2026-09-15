// tollgate-installer — cross-platform TollGate router onboarding wizard.
// Single binary: serves web UI + API, auto-discovers routers,
// deploys TollGate over SSH.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	lnAddrRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	// lnurlRe matches a raw LNURL: the "lnurl1" bech32 separator followed by
	// at least 6 lowercase bech32 data characters (covers the 6-char checksum).
	// Real LNURLs are far longer; this is a lenient plausibility gate.
	lnurlRe = regexp.MustCompile(`^lnurl1[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{6,}$`)
)

// validLightningAddress reports whether s is a plausible Lightning payout
// target. Two forms are accepted:
//  1. Lightning address — email-shaped: localpart@domain.tld
//  2. Raw LNURL — bech32-encoded: lnurl1<data>
//
// The Lightning target is a required MVP field — payouts route here, so an
// empty/invalid value would silently send payments nowhere. The check is
// intentionally lenient; actual resolution happens at payout time on the router.
func validLightningAddress(s string) bool {
	s = strings.TrimSpace(s)
	return lnAddrRe.MatchString(s) || lnurlRe.MatchString(s)
}

var (
	listenPort = flag.String("port", "8099", "HTTP listen port")
	listenAddr string
)

// ─── Job tracking ─────────────────────────────────────────────

type Step struct {
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Status string `json:"status"` // pending | running | done | failed
	Detail string `json:"detail,omitempty"`
}

type LogEntry struct {
	Time float64 `json:"time"`
	Msg  string  `json:"msg"`
}

type Job struct {
	mu     sync.Mutex
	IP     string     `json:"ip"`
	Status string     `json:"status"` // running | done | failed
	Step   int        `json:"step"`
	Steps  []Step     `json:"steps"`
	Log    []LogEntry `json:"log"`
	Error  string     `json:"error,omitempty"`
	// stageCache holds pre-downloaded deploy assets keyed by the exact
	// asset URL, populated by the PreStage phase (stageAssets) so the
	// flash/install steps can consume staged bytes without live network.
	// Guarded by j.mu — the cache deliberately lives on the Job (which may
	// outlive a single deployRequest), NOT on deployRequest. Not serialized
	// to JSON (unexported; handleStatus builds an explicit snapshot).
	stageCache map[string][]byte
}

var (
	jobs      = make(map[string]*Job)
	jobsMutex sync.RWMutex
)

func newJob(ip string) *Job {
	return &Job{
		IP:         ip,
		Status:     "running",
		Step:       0,
		Steps:      deploySteps(),
		Log:        []LogEntry{},
		stageCache: map[string][]byte{},
	}
}

// newPreStageJob creates a job for a selection-time pre-download
// (/api/prestage). Same Job type (so /api/status works unchanged) but a short
// prestageSteps() list.
func newPreStageJob(ip string) *Job {
	return &Job{
		IP:         ip,
		Status:     "running",
		Step:       0,
		Steps:      prestageSteps(),
		Log:        []LogEntry{},
		stageCache: map[string][]byte{},
	}
}

func (j *Job) addLog(msg string) {
	j.mu.Lock()
	j.Log = append(j.Log, LogEntry{Time: float64(time.Now().Unix()), Msg: msg})
	j.mu.Unlock()
}

func (j *Job) setStep(i int, status, detail string) {
	j.mu.Lock()
	if i < len(j.Steps) {
		j.Step = i
		j.Steps[i].Status = status
		if detail != "" {
			j.Steps[i].Detail = detail
		}
	}
	j.mu.Unlock()
}

// stageAsset stores pre-downloaded asset bytes in the Job's stage cache,
// keyed by the exact source URL. Guarded by j.mu. A zero-length payload is
// not cached — an empty body means the fetch produced nothing usable.
func (j *Job) stageAsset(url string, data []byte) {
	if url == "" || len(data) == 0 {
		return
	}
	j.mu.Lock()
	if j.stageCache == nil {
		j.stageCache = map[string][]byte{}
	}
	j.stageCache[url] = data
	j.mu.Unlock()
}

// stagedAsset returns the staged bytes for url and whether the URL is
// present in the cache. Guarded by j.mu.
func (j *Job) stagedAsset(url string) ([]byte, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	data, ok := j.stageCache[url]
	return data, ok
}

// adoptStageCache copies every staged asset from src into dst and returns how
// many it moved. It hands a /api/prestage job's downloads to the deploy job so
// the install/flash steps reuse them instead of re-fetching. Safe with a nil
// src or dst (returns 0). Locks are taken one at a time (never nested) to
// avoid any lock-order deadlock.
func adoptStageCache(dst, src *Job) int {
	if dst == nil || src == nil {
		return 0
	}
	src.mu.Lock()
	staged := make(map[string][]byte, len(src.stageCache))
	for u, d := range src.stageCache {
		staged[u] = d
	}
	src.mu.Unlock()
	for u, d := range staged {
		dst.stageAsset(u, d)
	}
	return len(staged)
}

// ─── API handlers ─────────────────────────────────────────────

func handleScan(w http.ResponseWriter, r *http.Request) {
	routers := discoverRouters()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"routers": routers})
}

// identifyRequest is the JSON body for /api/identify.
type identifyRequest struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
}

// handleIdentify re-identifies a router (vendor/model/firmware/name) using the
// supplied root password. The LAN scan only tries passwordless SSH, so a
// password-protected router shows as "Router" until the operator types the
// password; this endpoint lets the UI refresh the label then. Read-only: it
// probes ports and runs one SSH identification, never changes the router.
func handleIdentify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req identifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.IP == "" {
		writeError(w, 400, "IP required")
		return
	}
	info := probeRouterWithPassword(req.IP, req.Password)
	for _, a := range readARPTable() {
		if a.IP == req.IP && info.MAC == "" {
			info.MAC = a.MAC
		}
	}
	info.Name = friendlyRouterName(info)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// wifiScanRequest is the JSON body for /api/wifi-scan.
type wifiScanRequest struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
}

// wifiSSID represents a single SSID found during a WiFi scan.
type wifiSSID struct {
	Name       string `json:"name"`
	Encryption string `json:"encryption"`
	Signal     int    `json:"signal"`         // dBm, e.g. -45 (0 if unknown)
	Band       string `json:"band,omitempty"` // "2.4", "5", "6" (empty = unknown)
}

// bandFromGHz extracts the band from an iwinfo "Channel: 36 (5 GHz)" style
// fragment: "2.4", "5", "6", or "" when no band token is present.
func bandFromGHz(s string) string {
	i := strings.Index(s, "GHz")
	if i < 0 {
		return ""
	}
	j := i - 1
	for j >= 0 && s[j] == ' ' {
		j--
	}
	end := j + 1
	for j >= 0 && (s[j] == '.' || (s[j] >= '0' && s[j] <= '9')) {
		j--
	}
	switch strings.TrimSpace(s[j+1 : end]) {
	case "2.4":
		return "2.4"
	case "5":
		return "5"
	case "6":
		return "6"
	}
	return ""
}

// bandFromFreq maps an 802.11 centre frequency (MHz) to a band label.
func bandFromFreq(freq int) string {
	switch {
	case freq >= 2400 && freq < 2500:
		return "2.4"
	case freq >= 4900 && freq < 5925:
		return "5"
	case freq >= 5925 && freq <= 7125:
		return "6"
	}
	return ""
}

// normalizeBand returns b only if it is exactly one of "2.4", "5", "6" — used
// before interpolating a band into the STA shell script.
func normalizeBand(b string) string {
	switch strings.TrimSpace(b) {
	case "2.4", "5", "6":
		return strings.TrimSpace(b)
	}
	return ""
}

// parseIwinfoScan parses `iwinfo scan` output and returns deduplicated SSIDs
// sorted by signal strength (strongest first). iwinfo output on OpenWrt:
//
//	wl0-sha0   ESSID: "MyWiFi"
//	          Mode: Master  Channel: 6 (2.4 GHz)
//	          Signal: -45 dBm  Quality: 70/70
//	          Encryption: WPA2 PSK (CCMP)
//
// Some versions prefix with "Cell 01 - Address: ..." instead of the interface name.
func parseIwinfoScan(output string) []wifiSSID {
	seen := map[string]bool{}
	ssids := []wifiSSID{}
	var currentName, currentEnc, currentBand string
	var currentSignal int

	flush := func() {
		if currentName != "" && !seen[currentName] {
			seen[currentName] = true
			ssids = append(ssids, wifiSSID{Name: currentName, Encryption: currentEnc, Signal: currentSignal, Band: currentBand})
		}
		currentName = ""
		currentEnc = ""
		currentBand = ""
		currentSignal = 0
	}

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		// "Cell" prefix (some iwinfo versions) or a new ESSID line indicates a new block
		if strings.HasPrefix(trimmed, "Cell ") {
			flush()
			continue
		}

		// ESSID — may appear on the interface-name line or indented
		if idx := strings.Index(trimmed, "ESSID:"); idx >= 0 {
			// If we already have a name, this is a new block (interface-name-prefixed format)
			if currentName != "" {
				flush()
			}
			val := strings.TrimSpace(trimmed[idx+len("ESSID:"):])
			val = strings.Trim(val, "\"")
			if val != "" {
				currentName = val
			}
			continue
		}

		if strings.HasPrefix(trimmed, "Signal:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Signal:"))
			fields := strings.Fields(val)
			if len(fields) > 0 {
				if dbm, err := strconv.Atoi(fields[0]); err == nil {
					currentSignal = dbm
				}
			}
			continue
		}

		if strings.HasPrefix(trimmed, "Encryption:") {
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "Encryption:"))
			currentEnc = val
			continue
		}

		// Band: iwinfo prints "Channel: 36 (5 GHz)" (or "Channel: 6 (2.4 GHz)").
		if b := bandFromGHz(trimmed); b != "" {
			currentBand = b
			continue
		}
	}
	flush()

	// Sort by signal strength descending (strongest = highest dBm first)
	sort.Slice(ssids, func(i, j int) bool {
		return ssids[i].Signal > ssids[j].Signal
	})

	return ssids
}

// parseIwScan parses `iw dev wlan0 scan` output (fallback when iwinfo absent).
// iw output uses:
//
//	BSS aa:bb:cc:dd:ee:ff on wlan0
//	    freq: 2412
//	    SSID: NetworkName
//	    ...
//	    capability: ...
//	    * primary channel: 1
func parseIwScan(output string) []wifiSSID {
	seen := map[string]bool{}
	ssids := []wifiSSID{}
	var currentName, currentBand string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BSS ") {
			if currentName != "" && !seen[currentName] {
				seen[currentName] = true
				ssids = append(ssids, wifiSSID{Name: currentName, Encryption: "unknown", Band: currentBand})
			}
			currentName = ""
			currentBand = ""
			continue
		}
		if strings.HasPrefix(line, "freq:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if f, err := strconv.Atoi(fields[1]); err == nil {
					currentBand = bandFromFreq(f)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "SSID:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
			if val != "" {
				currentName = val
			}
		}
	}
	if currentName != "" && !seen[currentName] {
		seen[currentName] = true
		ssids = append(ssids, wifiSSID{Name: currentName, Encryption: "unknown", Band: currentBand})
	}
	return ssids
}

// scanFailedHeuristic returns true if the output text indicates a scan
// failure rather than real scan results. Checks for common error strings
// emitted by iwinfo and iw on OpenWrt when a device doesn't exist, is in
// the wrong mode, or the command is unavailable.
func scanFailedHeuristic(out string) bool {
	return strings.TrimSpace(out) == "" ||
		strings.Contains(out, "command not found") ||
		strings.Contains(out, "No such device") ||
		strings.Contains(out, "No such wireless device") ||
		strings.Contains(out, "Operation not supported") ||
		strings.Contains(out, "Operation not permitted") ||
		strings.Contains(out, "Device or resource busy") ||
		strings.Contains(out, "Usage:")
}

func handleWifiScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req wifiScanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	// Password is optional: a fresh-reset OpenWrt router ships with an
	// EMPTY root password (see sshConnect's auth chain).
	if req.IP == "" {
		writeError(w, 400, "IP required")
		return
	}

	client := sshConnect(req.IP, req.Password)
	if client == nil && req.Password != "" {
		client = sshConnect(req.IP, "")
	}
	if client == nil {
		writeError(w, 502, "cannot connect to router via SSH")
		return
	}
	defer client.Close()

	// A fresh-reset OpenWrt router ships with radios DISABLED in UCI —
	// `iwinfo scan` would return nothing and the UI would show an empty
	// SSID list. Enable all radios, bring wifi up, and wait until every
	// radio reports up before scanning (see enableWifiAndWait).
	enableWifiAndWait(client)

	// ---- WiFi scan with prioritised fallback chain ----
	//
	// Priority:
	//   1. `iwinfo scan` (no device arg) — scans ALL radios, works on
	//      OpenWrt 25.12 / GL-MT3000 without needing to pick a specific
	//      interface. This is what worked in the July version.
	//   2. Per-interface: only Client/Managed mode interfaces (skip
	//      Master/AP mode interfaces like phy0-ap0 which can't scan).
	//   3. `iw phy phy0 scan` + `iw phy phy1 scan` — phy-level scan
	//      works regardless of interface mode.
	//   4. Existing fallbacks: iwinfo wlan0/wlan1 scan, iw dev scan.
	//
	// parseIwinfoScan and parseIwScan are kept as-is.

	var debugLog strings.Builder

	// --- Strategy 1: iwinfo scan (no args) — scans all radios ---
	scanOut := sshRun(client, "iwinfo scan 2>/dev/null")
	debugLog.WriteString("[1] iwinfo scan (no args): ")
	if strings.TrimSpace(scanOut) != "" && !scanFailedHeuristic(scanOut) {
		debugLog.WriteString("got results\n")
		ssids := parseIwinfoScan(scanOut)
		w.Header().Set("Content-Type", "application/json")
		if len(ssids) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"ssids": ssids,
				"debug": truncate(scanOut, 200),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
		}
		return
	}
	debugLog.WriteString("no results\n")

	// --- Strategy 2: per-interface scan (Client/Managed only) ---
	// Auto-detect wireless interfaces from `iwinfo` (no args) listing.
	// For each interface, check `iwinfo <dev> info` Mode: field and skip
	// Master (AP) mode interfaces — they can't scan.
	iwinfoOut := sshRun(client, "iwinfo 2>/dev/null")
	var clientDevs []string
	for _, line := range strings.Split(iwinfoOut, "\n") {
		trimmed := strings.TrimSpace(line)
		if idx := strings.Index(trimmed, "ESSID:"); idx > 0 {
			iface := strings.TrimSpace(trimmed[:idx])
			if iface == "" || strings.HasPrefix(iface, "Usage") {
				continue
			}
			// Check mode: skip Master (AP) interfaces.
			infoOut := sshRun(client, "iwinfo "+iface+" info 2>/dev/null")
			mode := ""
			for _, infoLine := range strings.Split(infoOut, "\n") {
				infoLine = strings.TrimSpace(infoLine)
				if strings.HasPrefix(infoLine, "Mode:") {
					mode = strings.TrimSpace(strings.TrimPrefix(infoLine, "Mode:"))
					break
				}
			}
			debugLog.WriteString("[2] iface " + iface + " mode=" + mode + " — will scan\n")
			// Don't skip Master (AP) interfaces — iwinfo <dev> scan works
			// on AP interfaces on many OpenWrt versions (e.g. GL-MT3000
			// with OpenWrt 25.12). The scan triggers a passive/active
			// scan on the underlying phy regardless of interface mode.
			clientDevs = append(clientDevs, iface)
		}
	}

	if len(clientDevs) > 0 {
		var perIfaceScan string
		for _, dev := range clientDevs {
			out := sshRun(client, "iwinfo "+dev+" scan 2>/dev/null")
			if strings.TrimSpace(out) != "" && !scanFailedHeuristic(out) {
				perIfaceScan += out + "\n"
			}
		}
		if strings.TrimSpace(perIfaceScan) != "" {
			debugLog.WriteString("[2] per-interface scan: got results\n")
			scanOut = perIfaceScan
			ssids := parseIwinfoScan(scanOut)
			w.Header().Set("Content-Type", "application/json")
			if len(ssids) == 0 {
				json.NewEncoder(w).Encode(map[string]any{
					"ssids": ssids,
					"debug": truncate(scanOut, 200),
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
			}
			return
		}
	}
	debugLog.WriteString("[2] per-interface scan: no results\n")

	// --- Strategy 3: phy-level scan (mode-independent) ---
	// `iw phy phy0 scan` works regardless of interface mode — it creates
	// a temporary scan request on the physical device. Try phy0 and phy1.
	var phyScanOut string
	for _, phy := range []string{"phy0", "phy1"} {
		out := sshRun(client, "iw phy "+phy+" scan 2>/dev/null")
		if strings.TrimSpace(out) != "" && !strings.Contains(out, "command not found") {
			phyScanOut += out + "\n"
		}
	}
	if strings.TrimSpace(phyScanOut) != "" {
		debugLog.WriteString("[3] phy-level scan: got results\n")
		ssids := parseIwScan(phyScanOut)
		w.Header().Set("Content-Type", "application/json")
		if len(ssids) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"ssids": ssids,
				"debug": truncate(phyScanOut, 200),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
		}
		return
	}
	debugLog.WriteString("[3] phy-level scan: no results\n")

	// --- Strategy 4: existing fallbacks ---
	// Try common interface names (wlan0/wlan1) then iw dev scan.
	scanOut = sshRun(client, "iwinfo wlan0 scan 2>/dev/null || iwinfo wlan1 scan 2>/dev/null")
	if strings.TrimSpace(scanOut) != "" && !scanFailedHeuristic(scanOut) {
		debugLog.WriteString("[4] iwinfo wlan0/wlan1 fallback: got results\n")
		ssids := parseIwinfoScan(scanOut)
		w.Header().Set("Content-Type", "application/json")
		if len(ssids) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"ssids": ssids,
				"debug": truncate(scanOut, 200),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
		}
		return
	}

	// Last resort: iw dev scan
	scanOut = sshRun(client, "iw dev scan 2>/dev/null")
	if strings.TrimSpace(scanOut) != "" && !strings.Contains(scanOut, "command not found") {
		debugLog.WriteString("[4] iw dev scan: got results\n")
		ssids := parseIwScan(scanOut)
		w.Header().Set("Content-Type", "application/json")
		if len(ssids) == 0 {
			json.NewEncoder(w).Encode(map[string]any{
				"ssids": ssids,
				"debug": truncate(scanOut, 200),
			})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"ssids": ssids})
		}
		return
	}
	debugLog.WriteString("[4] all fallbacks exhausted\n")

	// All strategies failed — return diagnostic info.
	w.Header().Set("Content-Type", "application/json")
	// If 0 SSIDs found despite non-empty scan output from the last strategy,
	// include diagnostic info so the operator can see what the router returned.
	// This catches format mismatches (new iwinfo output) and error text that
	// slipped past the checks above.
	if strings.TrimSpace(scanOut) != "" {
		json.NewEncoder(w).Encode(map[string]any{
			"ssids": []string{},
			"debug": truncate(scanOut, 200),
		})
		return
	}
	w.WriteHeader(500)
	json.NewEncoder(w).Encode(map[string]any{
		"error": "WiFi scan failed — no wireless interfaces found or iwinfo/iw not available. The router may have been left in a partially-configured state by a previous deployment. Try factory resetting the router.",
		"debug": truncate(debugLog.String(), 500),
	})
}

// handlePreStage starts a background pre-download of the deploy assets for a
// router, so the download begins as soon as the operator selects it. The
// resulting job id can be passed to /api/deploy as prestageJobId to reuse the
// cached bytes. The router is not modified.
func handlePreStage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req prestageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.IP == "" {
		writeError(w, 400, "IP required")
		return
	}
	jobID := fmt.Sprintf("%d", time.Now().UnixNano()%100000000)
	job := newPreStageJob(req.IP)
	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()
	go runPreStageJob(job, req)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
}

// wifiTestRequest is the JSON body for /api/wifi-test.
type wifiTestRequest struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
	SSID     string `json:"ssid"`
	WifiPass string `json:"wifiPass"`
	// Band is the scan-derived band of the selected SSID ("2.4"/"5"/"6"), so
	// the STA is configured on the radio that can actually see it.
	Band string `json:"band"`
}

// handleWifiTest proactively verifies the upstream WiFi (SSID + password)
// before deploy: it applies the STA config, waits for association, then rolls
// the wireless config back, so a wrong password surfaces on the form instead
// of after the flash/install steps. Blocks up to ~40s. Read-only-ish: the
// router's wireless config is restored before returning.
func handleWifiTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req wifiTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if req.IP == "" || req.SSID == "" {
		writeError(w, 400, "ip and ssid are required")
		return
	}
	ok, msg := testSTAConfig(req.IP, req.Password, req.SSID, req.WifiPass, req.Band)
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "message": msg})
}

type deployRequest struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
	Mode     string `json:"mode"`     // wan | sta
	SSID     string `json:"ssid"`     // for sta mode
	WifiPass string `json:"wifiPass"` // for sta mode
	Band     string `json:"band"`     // sta mode: scan-derived band of SSID ("2.4"/"5"/"6")
	LNURL    string `json:"lnurl"`    // Lightning address or raw LNURL
	DevSplit *int   `json:"devSplit"` // advanced: % to dev fund (0-50); nil => defaultDevSplit
	Margin   *int   `json:"margin"`   // advanced: operator markup % (0-100); nil => defaultMargin
	Mint     string `json:"mint"`     // advanced: preferred Cashu mint URL
	// PreStage asks the wizard to download all deploy binaries up-front into
	// the Job's stageCache before running the flash/install steps, so the
	// deploy can proceed even if the laptop's internet path dies mid-deploy
	// (e.g. a STA-mode laptop whose only uplink is the router being flashed).
	// This is a per-request FLAG ONLY — the cache itself lives on the Job
	// struct (guarded by j.mu), NOT here (deployRequest is per-POST and
	// synchronous).
	PreStage bool `json:"preStage"`
	// ForceFlash runs the OpenWrt sysupgrade even when the router is ALREADY
	// running OpenWrt — the default flash step is skipped in that case. It
	// moves a running OpenWrt install onto the pinned release in images.go
	// (e.g. 24.10 -> 25.12) and ALWAYS wipes config (sysupgrade -n), then
	// continues the normal deploy on a clean system. Explicit opt-in only.
	ForceFlash bool `json:"forceFlash"`
	// PrestageJobID optionally references a /api/prestage job whose stage
	// cache should be adopted by this deploy, so the assets pre-downloaded
	// when the router was selected are reused instead of fetched again.
	PrestageJobID string `json:"prestageJobId"`
}

// Advanced-field defaults. The wizard pre-fills the sliders with these; the
// server applies them too when the field is omitted (nil), so API callers get
// the same defaults without having to know the values. An explicit 0
// ("devSplit":0) is honoured — only ABSENCE means "use the default".
const (
	defaultDevSplit = 21
	defaultMargin   = 21
)

// resolvedAdvanced returns the effective dev split / margin, substituting the
// defaults for omitted (nil) fields.
func (r deployRequest) resolvedAdvanced() (int, int) {
	devSplit, margin := defaultDevSplit, defaultMargin
	if r.DevSplit != nil {
		devSplit = *r.DevSplit
	}
	if r.Margin != nil {
		margin = *r.Margin
	}
	return devSplit, margin
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	// Password is optional: a fresh-reset OpenWrt router ships with an
	// EMPTY root password (see sshConnect's auth chain).
	if req.IP == "" {
		writeError(w, 400, "IP required")
		return
	}
	if !validLightningAddress(req.LNURL) {
		writeError(w, 400, "a valid Lightning address is required")
		return
	}

	jobID := fmt.Sprintf("%d", time.Now().UnixNano()%100000000)
	job := newJob(req.IP)

	// Adopt assets pre-downloaded by a /api/prestage job (started when the
	// router was selected) so the deploy reuses them instead of re-fetching.
	if pid := strings.TrimSpace(req.PrestageJobID); pid != "" {
		jobsMutex.RLock()
		pj, ok := jobs[pid]
		jobsMutex.RUnlock()
		if ok {
			if n := adoptStageCache(job, pj); n > 0 {
				job.addLog(fmt.Sprintf("Using %d pre-downloaded asset(s) from pre-stage job %s", n, pid))
			}
		}
	}

	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()

	go runDeployment(job, req)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Path[len("/api/status/"):]
	jobsMutex.RLock()
	job, ok := jobs[jobID]
	jobsMutex.RUnlock()
	if !ok {
		writeError(w, 404, "job not found")
		return
	}
	// Return a snapshot for thread-safe JSON
	job.mu.Lock()
	snapshot := struct {
		IP     string     `json:"ip"`
		Status string     `json:"status"`
		Step   int        `json:"step"`
		Steps  []Step     `json:"steps"`
		Log    []LogEntry `json:"log"`
		Error  string     `json:"error,omitempty"`
	}{job.IP, job.Status, job.Step, job.Steps, job.Log, job.Error}
	job.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// enableWifiAndWait enables all UCI wifi-devices, starts wifi, and polls
// `ubus call network.wireless status` until every radio reports up
// (~15s budget; fixed sleeps race slow driver init, polling removes that).
// Best-effort: the scan proceeds regardless after the timeout — errors
// surface in the scan step itself where they are actionable.
func enableWifiAndWait(client *ssh.Client) {
	sshRun(client, strings.Join([]string{
		`for r in $(uci -q show wireless 2>/dev/null | sed -n "s/^wireless\.\([^.]*\)=wifi-device$/\1/p"); do uci -q set wireless.$r.disabled='0'; done`,
		`uci commit wireless`,
		`wifi up 2>/dev/null || wifi 2>/dev/null || true`,
	}, " && "))
	for i := 0; i < 10; i++ {
		if allRadiosUp(sshRun(client, "ubus call network.wireless status 2>/dev/null")) {
			return
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

// allRadiosUp parses `ubus call network.wireless status` output
// ({"radio0":{"up":true,...},"radio1":{...}}) and reports whether EVERY
// radio reports up. Empty/garbage output parses to false (keep polling).
func allRadiosUp(statusJSON string) bool {
	var status map[string]map[string]any
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		return false
	}
	if len(status) == 0 {
		return false
	}
	for _, radio := range status {
		if up, _ := radio["up"].(bool); !up {
			return false
		}
	}
	return true
}

func main() {
	flag.Parse()
	listenAddr = ":" + *listenPort
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/identify", handleIdentify)
	mux.HandleFunc("/api/wifi-scan", handleWifiScan)
	mux.HandleFunc("/api/wifi-test", handleWifiTest)
	mux.HandleFunc("/api/prestage", handlePreStage)
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/status/", handleStatus)
	mux.HandleFunc("/", handleIndex)

	// CORS for local dev
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		mux.ServeHTTP(w, r)
	})

	fmt.Printf("TollGate setup wizard running on http://localhost%s\n", listenAddr)
	fmt.Println("Open this URL in your browser to set up a router.")
	log.Fatal(http.ListenAndServe(listenAddr, handler))
	_ = io.Discard // keep import
}
