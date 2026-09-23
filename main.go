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
	"net/url"
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

// Build metadata, injected at build time:
//
//	-ldflags "-X main.version=<tag> -X main.commit=<sha7>"
var (
	version = "dev"
	commit  = "unknown"
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
	// listenBind is the interface the wizard binds to. Defaults to loopback
	// (127.0.0.1) so the deploy API — which drives a root SSH session on the
	// router — is NOT reachable from other hosts on the LAN. Operators who
	// accept that risk can override with --bind=0.0.0.0 or a specific IP.
	listenBind = flag.String("bind", defaultBindHost, "HTTP bind address (default loopback-only)")
	// trustHostKey is the operator's explicit host-key pin for this run: an
	// OpenSSH SHA-256 fingerprint ("SHA256:…") of the router's SSH host key,
	// verified on the router's own console. Without it (or an entry in the
	// trust store) the wizard refuses to connect and prints the fingerprint —
	// see hostkey.go. TOLLGATE_TRUST_HOST_KEY carries the same value for the
	// curl|bash launcher, which runs the binary with its own argv.
	trustHostKey = flag.String("trust-host-key", "", "OpenSSH SHA256 fingerprint of the router's SSH host key to trust (verified out of band)")
	listenAddr   string
)

// defaultBindHost is the loopback interface the wizard serves on by default.
// The setup wizard drives a root SSH session on the router, so a wildcard
// bind (":8099" on all interfaces) would let any host on the LAN drive
// deploys against it.
const defaultBindHost = "127.0.0.1"

// listenAddress returns the host:port the wizard serves on: loopback by
// default, or the operator's --bind override. An empty bind falls back to
// loopback so a misconfigured flag can never widen the exposure.
func listenAddress(bind, port string) string {
	if strings.TrimSpace(bind) == "" {
		bind = defaultBindHost
	}
	return bind + ":" + port
}

// corsAllowedOrigin reports whether origin is a loopback origin the wizard
// trusts for cross-origin reads. Only the wizard's own localhost/127.0.0.1
// origins (any port) are allowlisted; a wildcard Access-Control-Allow-Origin
// on this service would let any website the operator visits drive the deploy
// API from their browser.
func corsAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// corsMiddleware dispatches to next after setting CORS headers.
// Access-Control-Allow-Origin is echoed only for allowlisted loopback
// origins — foreign origins get no ACAO header at all, so browsers block
// cross-origin reads. Methods/headers advertisement is unchanged.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); corsAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

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
	// progress drives the UI progress bar during a pre-download. current/total
	// are asset counts (total==0 => indeterminate); label is a short verb.
	// Guarded by mu; exported via the handleStatus snapshot.
	progressCurrent int
	progressTotal   int
	progressLabel   string
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

// setProgress records pre-download progress for the UI progress bar. total==0
// means "indeterminate" (nothing to report yet). Guarded by j.mu.
func (j *Job) setProgress(current, total int, label string) {
	j.mu.Lock()
	j.progressCurrent = current
	j.progressTotal = total
	j.progressLabel = label
	j.mu.Unlock()
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

// bandFromChannel infers a band from an 802.11 channel number, for iwinfo
// output that prints "Channel: 36" WITHOUT the "(5 GHz)" suffix (common on
// some builds — this is why a 5 GHz SSID was being configured on the 2.4 GHz
// radio). Channels 1-14 are 2.4 GHz; 32-177 are 5 GHz. 6 GHz reuses 1-233, so
// it is only inferred when the "(6 GHz)" token is present (see bandFromGHz).
func bandFromChannel(ch int) string {
	switch {
	case ch >= 1 && ch <= 14:
		return "2.4"
	case ch >= 32 && ch <= 177:
		return "5"
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

		// Band: iwinfo prints "Channel: 36 (5 GHz)" — or just "Channel: 36"
		// on some builds, in which case infer from the channel number.
		if b := bandFromGHz(trimmed); b != "" {
			currentBand = b
			continue
		}
		if idx := strings.Index(trimmed, "Channel:"); idx >= 0 {
			fields := strings.Fields(strings.TrimSpace(trimmed[idx+len("Channel:"):]))
			if len(fields) > 0 {
				if ch, err := strconv.Atoi(fields[0]); err == nil {
					if b := bandFromChannel(ch); b != "" {
						currentBand = b
					}
				}
			}
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

// scanFailureSignatures is the DATA list of strings that iwinfo/iw print
// *instead of* scan results. A strategy attempt whose output matches any of
// them is a FAILED attempt: the chain falls through to the next strategy
// instead of reporting an empty SSID list as a successful scan.
//
// Provenance — do not extend this list without executed evidence:
//   - "Scanning not possible"   iwinfo_cli.c:687 (printf -> STDOUT): the
//     scanlist op FAILED (radio down/busy, phy not in a scanning state).
//     This is the string the GL.iNet MT3000 reports; it was missing from the
//     list, so the refusal was read as a successful scan and strategies 3-5
//     never ran.
//   - "No scan results"         iwinfo_cli.c:692 (printf -> STDOUT): the
//     scanlist op succeeded with len <= 0 — not a single BSS was heard.
//     Also not a result: the later strategies may still hear the network.
//   - "No such wireless backend" / "No such wireless device"
//     iwinfo_cli.c:1009 / :1036 (stderr): `iwinfo scan` without a device and
//     an unknown interface name both land here.
//   - "Operation not supported" / "Operation not permitted" /
//     "Device or resource busy" / "No such device": nl80211/errno strings
//     observed in the field (see docs/wifi-scan-fallthrough.md).
//   - "command not found" / "Usage:": iwinfo/iw missing, or a command called
//     with invalid syntax (e.g. `iw dev scan`).
//
// iwinfo line numbers: openwrt/iwinfo @ 66bdd1a.
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

// scanFailedHeuristic reports whether a strategy's output is a refusal or an
// error rather than scan results. It is a lookup over scanFailureSignatures
// (data), so recognising a newly observed iwinfo refusal is a one-line data
// change instead of a new branch.
func scanFailedHeuristic(out string) bool {
	if strings.TrimSpace(out) == "" {
		return true
	}
	for _, sig := range scanFailureSignatures {
		if strings.Contains(out, sig) {
			return true
		}
	}
	return false
}

// strategyNone is the reported strategy when every attempt failed.
const strategyNone = "none"

// scanRunner runs one shell command on the router and returns its combined
// output. sshRun satisfies it; tests inject a fake router.
type scanRunner func(cmd string) string

// scanCommand is one executed router command and the output it produced.
type scanCommand struct {
	cmd string
	out string
}

// scanStrategy is one link of the WiFi-scan fallback chain: a named attempt
// with its own command(s) and its own parser. A strategy may run several
// commands (one per interface/phy); each command's output is judged
// separately, so one refusing interface cannot poison the whole attempt.
type scanStrategy struct {
	name   string
	parser func(string) []wifiSSID
	run    func(run scanRunner) (cmds []scanCommand, detail string)
}

// scanResult is the outcome of walking the chain.
type scanResult struct {
	SSIDs    []wifiSSID // networks found by the winning strategy (nil: none)
	Strategy string     // name of the winning strategy; strategyNone otherwise
	Log      []string   // one line per attempt, in execution order
	LastRaw  string     // output of the last attempt that produced any output
}

// wirelessInterfaces extracts interface names from `iwinfo` (no arguments)
// output, which prints one info block per interface, prefixed by its name:
//
//	phy0-ap0  ESSID: "TollGate-F794"
//	          Access Point: 94:83:C4:8C:59:C3
//	          Mode: Master  Channel: 1 (2.4 GHz)  HT Mode: HT20
func wirelessInterfaces(iwinfoOut string) []string {
	var devs []string
	for _, line := range strings.Split(iwinfoOut, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, "ESSID:")
		if idx <= 0 {
			continue
		}
		iface := strings.TrimSpace(trimmed[:idx])
		if iface == "" || strings.HasPrefix(iface, "Usage") {
			continue
		}
		devs = append(devs, iface)
	}
	return devs
}

// runCmd executes one command and records it.
func runCmd(run scanRunner, cmd string) scanCommand {
	return scanCommand{cmd: cmd, out: run(cmd)}
}

// scanChain is the ordered strategy list. Every attempt runs with stderr
// MERGED into stdout (2>&1): now that the failure class is recognised as
// data, the router's real error message stays visible in the log/debug
// fields instead of being discarded by 2>/dev/null.
func scanChain() []scanStrategy {
	return []scanStrategy{
		{
			// Retained first: some vendor iwinfo builds accept a device-less
			// scan. Upstream iwinfo needs the device argument, which now logs
			// as an explicit refusal instead of an empty result.
			name:   "iwinfo scan",
			parser: parseIwinfoScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				return []scanCommand{runCmd(run, "iwinfo scan 2>&1")}, "no device argument"
			},
		},
		{
			name:   "iwinfo <dev> scan",
			parser: parseIwinfoScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				list := runCmd(run, "iwinfo 2>&1")
				devs := wirelessInterfaces(list.out)
				if len(devs) == 0 {
					return []scanCommand{list}, "no wireless interfaces reported by iwinfo"
				}
				cmds := make([]scanCommand, 0, len(devs))
				for _, dev := range devs {
					cmds = append(cmds, runCmd(run, "iwinfo "+dev+" scan 2>&1"))
				}
				return cmds, "ifaces=" + strings.Join(devs, ",")
			},
		},
		{
			// phy-level scan works regardless of interface mode.
			name:   "iw phy <phy> scan",
			parser: parseIwScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				var cmds []scanCommand
				for _, phy := range []string{"phy0", "phy1"} {
					cmds = append(cmds, runCmd(run, "iw phy "+phy+" scan 2>&1"))
				}
				return cmds, "phy0,phy1"
			},
		},
		{
			name:   "iwinfo wlan0/wlan1 scan",
			parser: parseIwinfoScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				return []scanCommand{runCmd(run, "iwinfo wlan0 scan 2>&1 || iwinfo wlan1 scan 2>&1")}, "wlan0,wlan1"
			},
		},
		{
			// Last resort. `iw dev scan` is invalid syntax in iw >= 6 and
			// prints its usage text; that text is now recognised as a
			// refusal like any other, so it can never again be reported as a
			// successful empty scan.
			name:   "iw dev scan",
			parser: parseIwScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				return []scanCommand{runCmd(run, "iw dev scan 2>&1")}, "all wireless devices"
			},
		},
	}
}

// scanViaChain walks scanChain() and returns the first attempt that yields at
// least one network. An attempt is a SUCCESS only when at least one of its
// commands produced output that is not a recognised iwinfo/iw refusal and
// that parses to >= 1 network. Anything else — no output, a refusal like
// "Scanning not possible", or output that parses to zero networks — NEVER
// ends the walk: the next strategy runs. When every strategy fails, the
// result reports Strategy strategyNone with the per-attempt log and the last
// raw output, so the operator can see which methods were tried and why each
// one failed.
func scanViaChain(run scanRunner) scanResult {
	res := scanResult{Strategy: strategyNone}
	for i, st := range scanChain() {
		cmds, detail := st.run(run)
		label := fmt.Sprintf("[%d] %s", i+1, st.name)
		if detail != "" {
			label += " (" + detail + ")"
		}

		var good []string
		refusal := ""
		for _, c := range cmds {
			if strings.TrimSpace(c.out) == "" {
				continue
			}
			res.LastRaw = c.out
			if scanFailedHeuristic(c.out) {
				if refusal == "" {
					refusal = firstLine(c.out)
				}
				continue
			}
			good = append(good, c.out)
		}

		if len(good) == 0 {
			if refusal != "" {
				res.Log = append(res.Log, label+": refused: "+refusal)
			} else {
				res.Log = append(res.Log, label+": no output")
			}
			continue
		}

		raw := strings.Join(good, "\n")
		ssids := st.parser(raw)
		if len(ssids) == 0 {
			res.Log = append(res.Log, label+": parsed 0 networks")
			continue
		}

		res.Log = append(res.Log, fmt.Sprintf("%s: %d network(s)", label, len(ssids)))
		res.SSIDs = ssids
		res.Strategy = st.name
		return res
	}
	return res
}

// firstLine returns the first non-blank line of s, trimmed — used to keep the
// per-attempt log readable.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// buildScanResponse maps a chain outcome onto the /api/wifi-scan body and
// HTTP status. Pure — unit-tested without a router.
//
//   - networks found           -> 200 {ssids:[...], strategy:"<winner>", log}
//   - every strategy refused   -> 200 {ssids:[], strategy:"none", log, debug}
//   - nothing came back at all -> 500 {ssids:[], strategy:"none", log, error}
func buildScanResponse(res scanResult) (int, map[string]any) {
	ssids := res.SSIDs
	if ssids == nil {
		ssids = []wifiSSID{}
	}
	body := map[string]any{
		"ssids":    ssids,
		"strategy": res.Strategy,
		"log":      truncate(strings.Join(res.Log, "\n"), 700),
	}
	if len(ssids) > 0 {
		return http.StatusOK, body
	}
	if strings.TrimSpace(res.LastRaw) != "" {
		body["debug"] = truncate(res.LastRaw, 200)
		return http.StatusOK, body
	}
	body["error"] = "WiFi scan failed — no wireless interfaces found or iwinfo/iw not available. The router may have been left in a partially-configured state by a previous deployment. Try factory resetting the router."
	return http.StatusInternalServerError, body
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
		// Prefer the host-key refusal: it names the fingerprint and the exact
		// way to trust it, which a generic message would hide.
		writeError(w, 502, sshConnectFailureMessage(req.IP, "cannot connect to router via SSH"))
		return
	}
	defer client.Close()

	// A fresh-reset OpenWrt router ships with radios DISABLED in UCI —
	// `iwinfo scan` would return nothing and the UI would show an empty
	// SSID list. Enable all radios, bring wifi up, and wait until every
	// radio reports up before scanning (see enableWifiAndWait).
	enableWifiAndWait(client)

	// Walk the WiFi-scan fallback chain. scanViaChain falls through on
	// empty output, on any recognised iwinfo/iw refusal (see
	// scanFailureSignatures) and on output that parses to zero networks,
	// and reports which strategy produced the SSIDs.
	res := scanViaChain(func(cmd string) string { return sshRun(client, cmd) })
	for _, line := range res.Log {
		log.Printf("wifi-scan %s %s", req.IP, line)
	}
	status, body := buildScanResponse(res)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
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
	// TestMints is OPT-IN: when false/absent (the default for real customer
	// deployments) the wizard configures ONLY the 7 production mints. When
	// true it additionally appends the 2 testnut test mints, which fake
	// Lightning payments for E2E purchase testing — never for production.
	TestMints bool `json:"test_mints"` // advanced: include testnut test mints (E2E only, default false)
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

	// State WHICH feed release this deploy targets, before any download — the
	// package-source / version-verified lines only appear once the deploy is
	// underway, and a tester should see the target up front.
	pinSuffix := ""
	if pin, ok := cachedFeedPin(feedReleaseTag); ok && pin.SourceSHA7 != "" {
		pinSuffix = ", module " + pin.SourceSHA7
	}
	job.addLog(fmt.Sprintf("Feed: %s release %s (package %s%s)",
		feedRepoSlug, feedReleaseTag, feedPkgVersion(), pinSuffix))

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
		IP              string     `json:"ip"`
		Status          string     `json:"status"`
		Step            int        `json:"step"`
		Steps           []Step     `json:"steps"`
		Log             []LogEntry `json:"log"`
		Error           string     `json:"error,omitempty"`
		ProgressCurrent int        `json:"progressCurrent"`
		ProgressTotal   int        `json:"progressTotal"`
		ProgressLabel   string     `json:"progressLabel,omitempty"`
	}{job.IP, job.Status, job.Step, job.Steps, job.Log, job.Error,
		job.progressCurrent, job.progressTotal, job.progressLabel}
	job.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// configResponse is the installer's build + feed identity, served at
// /api/config so the UI and the curl|bash launcher can show exactly which
// release/feed is being installed — before a deploy starts, not only in the
// deploy log.
type configResponse struct {
	InstallerVersion string `json:"installer_version"`
	InstallerCommit  string `json:"installer_commit"`
	FeedRepo         string `json:"feed_repo"`
	FeedReleaseTag   string `json:"feed_release_tag"`
	FeedPkgVersion   string `json:"feed_pkg_version"`
	FeedReleaseURL   string `json:"feed_release_url"`
	FeedModulePin    string `json:"feed_module_pin,omitempty"`
	FeedModulePin7   string `json:"feed_module_pin7,omitempty"`
	FeedModuleTag    string `json:"feed_module_tag,omitempty"`
	FeedPkgHash      string `json:"feed_pkg_hash,omitempty"`
	FeedMakefileURL  string `json:"feed_makefile_url"`
	FeedPinError     string `json:"feed_pin_error,omitempty"`
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	// The module pin comes from the feed recipe at the effective tag (cached,
	// fail-soft): a missing/offline fetch leaves the pin empty + an error note
	// but still returns the rest of the identity.
	pin := resolveFeedModulePin(feedReleaseTag)
	resp := configResponse{
		InstallerVersion: version,
		InstallerCommit:  commit,
		FeedRepo:         feedRepoSlug,
		FeedReleaseTag:   feedReleaseTag,
		FeedPkgVersion:   feedPkgVersion(),
		FeedReleaseURL:   "https://github.com/" + feedRepoSlug + "/releases/tag/" + feedReleaseTag,
		FeedModulePin:    pin.SourceVersion,
		FeedModulePin7:   pin.SourceSHA7,
		FeedModuleTag:    pin.SourceTag,
		FeedPkgHash:      pin.PKGHash,
		FeedMakefileURL:  pin.URL,
		FeedPinError:     pin.Error,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	listenAddr = listenAddress(*listenBind, *listenPort)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scan", handleScan)
	mux.HandleFunc("/api/identify", handleIdentify)
	mux.HandleFunc("/api/wifi-scan", handleWifiScan)
	mux.HandleFunc("/api/wifi-test", handleWifiTest)
	mux.HandleFunc("/api/prestage", handlePreStage)
	mux.HandleFunc("/api/deploy", handleDeploy)
	mux.HandleFunc("/api/status/", handleStatus)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/", handleIndex)

	// CORS: loopback origins only (see corsMiddleware) — never a wildcard.
	handler := corsMiddleware(mux)

	fmt.Printf("TollGate setup wizard %s (%s) on http://%s\n", version, commit, listenAddr)
	fmt.Printf("Feed: %s release %s (package %s)\n", feedRepoSlug, feedReleaseTag, feedPkgVersion())
	fmt.Println("Open this URL in your browser to set up a router.")
	if strings.HasPrefix(listenAddr, defaultBindHost+":") || strings.HasPrefix(listenAddr, "localhost:") {
		fmt.Println("Bound to loopback (localhost only).")
	} else {
		fmt.Println("Bound to a non-loopback interface — the deploy API is reachable from the network.")
	}
	log.Fatal(http.ListenAndServe(listenAddr, handler))
	_ = io.Discard // keep import
}
