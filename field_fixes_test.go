package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestScanFailedHeuristic covers the error-string detection used by the
// WiFi scan fallback chain to distinguish real scan results from error
// text emitted by iwinfo/iw on OpenWrt.
func TestScanFailedHeuristic(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty output", "", true},
		{"whitespace only", "   \n	  ", true},
		{"command not found", "iwinfo: command not found", true},
		{"No such device", "No such device: wlan0", true},
		{"No such wireless device", "No such wireless device: phy0-ap0", true},
		{"Operation not supported", "Operation not supported", true},
		{"Operation not permitted", "Operation not permitted", true},
		{"Device or resource busy", "Device or resource busy", true},
		{"real scan output", "Cell 01 - Address: AA:BB:CC:DD:EE:FF\n  ESSID: \"MyWiFi\"\n  Signal: -45 dBm", false},
		{"iwinfo scan with SSIDs", "phy0-ap0   ESSID: \"TollGate-F794\"\n          Cell 01 - Address: ...\n          ESSID: \"TollGate-Cafe\"", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanFailedHeuristic(tc.in); got != tc.want {
				t.Errorf("scanFailedHeuristic(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestAllRadiosUp covers the `ubus call network.wireless status` parser used
// by enableWifiAndWait (WiFi scan pre-flight): every radio must report up.
func TestAllRadiosUp(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			"all radios up",
			`{"radio0":{"up":true,"pending":false,"disabled":false},"radio1":{"up":true,"pending":false,"disabled":false}}`,
			true,
		},
		{
			"single radio up",
			`{"radio0":{"up":true,"pending":false,"disabled":false,"interfaces":[{}]}}`,
			true,
		},
		{
			"one radio down — keep polling",
			`{"radio0":{"up":true},"radio1":{"up":false,"pending":true}}`,
			false,
		},
		{"radio still pending", `{"radio0":{"up":false,"pending":true}}`, false},
		{"empty object — keep polling", `{}`, false},
		{"empty output", "", false},
		{"ubus error text", "Failed to parse message", false},
		{"array not object", `["radio0"]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allRadiosUp(tc.in); got != tc.want {
				t.Errorf("allRadiosUp(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIfaceUp covers the `ubus call network.interface.wwan status` parser
// used by configureSTA — the only reliable STA verification (grep-based
// checks against iwinfo / network.wireless status are false positives).
func TestIfaceUp(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			"wwan up with dhcp address",
			`{"up":true,"pending":false,"available":true,"autostart":true,"uptime":42,"l3_device":"phy1-sta0","proto":"dhcp","updated":["addresses"],"route":[],"dns-server":[],"data":{}}`,
			true,
		},
		{
			"wwan up minimal",
			`{"up":true,"pending":false}`,
			true,
		},
		{
			"wwan down",
			`{"up":false,"pending":true,"available":true}`,
			false,
		},
		{"missing up field", `{"pending":false}`, false},
		{"empty output", "", false},
		{"ubus not found", "Object not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ifaceUp(tc.in); got != tc.want {
				t.Errorf("ifaceUp(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHTTPGetFile exercises the laptop-side download helper used as the
// PRIMARY package install path: it must follow redirects (GitHub release
// URLs redirect to a CDN) and surface non-200s as errors.
func TestHTTPGetFile(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("FAKE_IPK_BYTES"))
	}))
	defer final.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirector.Close()

	data, err := httpGetFile(redirector.URL)
	if err != nil {
		t.Fatalf("httpGetFile(redirecting URL): unexpected error: %v", err)
	}
	if string(data) != "FAKE_IPK_BYTES" {
		t.Errorf("httpGetFile data = %q, want %q", data, "FAKE_IPK_BYTES")
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	defer gone.Close()
	if _, err := httpGetFile(gone.URL); err == nil {
		t.Error("httpGetFile(404) should return an error")
	}
}

// TestStaSetupScript pins the safety properties of the STA setup script:
// rollback snapshot taken, existing STAs disabled (not deleted), single
// commit, and idempotent re-run support.
func TestStaSetupScript(t *testing.T) {
	s := staSetupScript("TollGate-Field", "correct horse", "")
	for _, want := range []string{
		// Snapshot for rollback BEFORE any change.
		"cp /etc/config/wireless /tmp/wireless.pre-tollgate",
		// Dual-STA guard: existing STAs on the target radio are disabled…
		"uci -q set wireless.$s.disabled='1'",
		// …not deleted…
		"rm /etc/config/wireless",
		// …and the new uplink is explicitly enabled (idempotent re-run).
		"wireless.tollgate_uplink.disabled='0'",
		// Single commit pair before the marker.
		"uci commit wireless",
		"uci commit network",
		// Success marker parsed by configureSTA.
		"STA_CFG_OK target=$target",
		// Operator-supplied credentials now cross as printf-expanded octal
		// carriers and are decoded into shell variables — never
		// string-interpolated into the UCI commands (injection-safe; plaintext
		// out of argv; no router-side base64 needed — stock OpenWrt has none).
		octalCarrierVar("sta_ssid", "TollGate-Field"),
		octalCarrierVar("sta_key", "correct horse"),
		"wireless.tollgate_uplink.ssid=\"$sta_ssid\"",
		"wireless.tollgate_uplink.key=\"$sta_key\"",
	} {
		if want == "rm /etc/config/wireless" {
			if strings.Contains(s, want) {
				t.Errorf("staSetupScript must NOT contain %q (config is snapshotted+disabled, never deleted)", want)
			}
			continue
		}
		if !strings.Contains(s, want) {
			t.Errorf("staSetupScript missing %q", want)
		}
	}
	// Exactly ONE commit per config file (no partial applies).
	if got := strings.Count(s, "uci commit"); got != 2 {
		t.Errorf("staSetupScript has %d `uci commit` calls, want exactly 2 (wireless+network)", got)
	}
}

// TestStaSetupScriptBandSelection pins the band-aware radio choice: a 5 GHz
// SSID must be configured on the 5 GHz radio (the old script always used
// radio0 = 2.4 GHz, which is why a 5 GHz uplink never associated).
func TestStaSetupScriptBandSelection(t *testing.T) {
	s := staSetupScript("Up-5G", "pw", "5")
	for _, want := range []string{
		`want_band="5"`,
		`[ "$rb" = "$want_band" ]`,      // radio band match
		`NO_BAND_RADIO`,                 // no matching radio marker
		`wireless.$target.disabled='0'`, // target radio enabled before use
	} {
		if !strings.Contains(s, want) {
			t.Errorf("staSetupScript(band=5) missing %q", want)
		}
	}
	if got := strings.Count(s, "uci commit"); got != 2 {
		t.Errorf("band script has %d `uci commit` calls, want 2", got)
	}

	// The band is sanitized before interpolation: a hostile value must not
	// reach the generated shell script.
	hostile := staSetupScript("x", "y", `5"; rm -rf /`)
	if strings.Contains(hostile, "rm -rf") {
		t.Error("band must be normalized before interpolation")
	}
	if !strings.Contains(hostile, `want_band=""`) {
		t.Errorf("hostile band should normalize to empty, got script without want_band=\"\"")
	}
}

// writeExecutable writes a small shell script into dir and makes it executable.
func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestStaSetupScriptForcedRadioRunsUnderAStrictUci RUNS the generated script in a
// real POSIX shell against a uci that behaves like OpenWrt's, because the failure
// this pins is invisible to string assertions: the carriers block's last line and
// the selector's first line are two shell statements only while the block is
// newline-terminated. Without that newline
//
//	sta_key=$(echo ... | base64 -d)target='radio0'
//
// assigns sta_key the literal "...target=radio0" and leaves `target` UNSET, so
// the forced-radio path (attemptSTA, i.e. every STA attempt) probes
//
//	uci -q get wireless.
//
// which a real uci rejects with a non-zero status even under -q, trips its own
// NO_RADIO guard, and exits before writing any config: step 5 then fails with a
// misleading "check SSID and password". The script is driven through sh with a
// strict uci shim so the shell semantics, not a substring, decide the outcome.
//
// The script writes its rollback markers into the real /tmp (they are shell
// redirections, not command calls, so they cannot be shimmed); they are removed
// again on cleanup.
func TestStaSetupScriptForcedRadioRunsUnderAStrictUci(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX sh on PATH")
	}
	for _, p := range []string{"/tmp/network.wwan.pre-tollgate", "/tmp/network.wwan.proto.pre-tollgate"} {
		t.Cleanup(func() { os.Remove(p) })
	}

	dir := t.TempDir()
	uciLog := filepath.Join(dir, "uci.log")

	// A uci that is STRICT, like OpenWrt's: a lookup that cannot resolve — an
	// empty section as much as an unknown one — exits non-zero, and `-q` only
	// silences the message (uci's cli.c returns 1 either way).
	writeExecutable(t, filepath.Join(dir, "uci"), `#!/bin/sh
echo "uci $*" >> "$UCI_LOG"
[ "${1:-}" = "-q" ] && shift
cmd="${1:-}"; shift || true
case "$cmd" in
  get)
    case "${1:-}" in
      wireless.radio0)      echo wifi-device ;;
      wireless.radio0.band) echo 2g ;;
      wireless.*.mode)      echo ap ;;
      *) exit 1 ;;          # unset option / empty section: hard error
    esac ;;
  show)
    case "${1:-}" in
      wireless) printf 'wireless.radio0=wifi-device\nwireless.radio0.band=2g\nwireless.default_radio0.device=radio0\n' ;;
      network.wwan) exit 1 ;;   # this run creates it
      *) exit 1 ;;
    esac ;;
  set|delete|commit) : ;;
  *) : ;;
esac
exit 0
`)
	// base64 and cp are not what this test is about; /etc must stay untouched.
	writeExecutable(t, filepath.Join(dir, "base64"), "#!/bin/sh\ncat >/dev/null\nprintf 'decoded\\n'\n")
	writeExecutable(t, filepath.Join(dir, "cp"), "#!/bin/sh\nexit 0\n")

	cmd := exec.Command("sh", "-c", staSetupScriptFor("BenchUpstream", "benchpass", "2.4", "radio0"))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "UCI_LOG="+uciLog)
	out, err := cmd.CombinedOutput()
	got := string(out)
	if err != nil || !strings.Contains(got, "STA_CFG_OK target=radio0") {
		t.Fatalf("the generated STA script did not configure radio0 — the selector did not run on its own "+
			"line (err=%v):\n%s", err, got)
	}
	if strings.Contains(got, "NO_RADIO") {
		t.Errorf("the script hit its own NO_RADIO guard (target was not set):\n%s", got)
	}

	uciCalls, err := os.ReadFile(uciLog)
	if err != nil {
		t.Fatalf("no uci calls were made: %v", err)
	}
	// The router-side effects the deploy depends on, in order.
	for _, want := range []string{
		"get wireless.radio0",
		"set wireless.tollgate_uplink=wifi-iface",
		"set wireless.tollgate_uplink.mode=sta",
		"set network.wwan=interface",
		"commit wireless",
		"commit network",
	} {
		if !strings.Contains(string(uciCalls), want) {
			t.Errorf("the script did not reach %q; uci saw:\n%s", want, uciCalls)
		}
	}
}
