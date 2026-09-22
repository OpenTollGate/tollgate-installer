package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// fakeRouter is a scripted router: cmd substrings (matched in order, first
// match wins) map to canned output. Every command the chain runs is recorded
// so a test can prove which strategies were actually attempted.
type fakeRouter struct {
	script []fakeCmd
	calls  []string
}

type fakeCmd struct {
	match string
	out   string
}

func (f *fakeRouter) run(cmd string) string {
	f.calls = append(f.calls, cmd)
	for _, c := range f.script {
		if strings.Contains(cmd, c.match) {
			return c.out
		}
	}
	return ""
}

// calledWith reports whether the chain ran a command containing sub.
func (f *fakeRouter) calledWith(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

const (
	iwinfoEnumeration = "phy0-ap0  ESSID: \"TollGate-F794\"\n" +
		"          Access Point: 94:83:C4:8C:59:C3\n" +
		"          Mode: Master  Channel: 1 (2.4 GHz)  HT Mode: HT20\n" +
		"\n" +
		"phy1-ap0  ESSID: \"TollGate-F794-5G\"\n" +
		"          Mode: Master  Channel: 36 (5 GHz)  HT Mode: HT20\n"

	iwinfoCells = "Cell 01 - Address: AA:BB:CC:DD:EE:FF\n" +
		"          ESSID: \"Cafe WiFi\"\n" +
		"          Mode: Master  Frequency: 2.412 GHz  Band: 2.4 GHz  Channel: 1\n" +
		"          Signal: -45 dBm  Quality: 60/70\n" +
		"          Encryption: WPA2 PSK (CCMP)\n" +
		"Cell 02 - Address: 11:22:33:44:55:66\n" +
		"          ESSID: \"HomeNet\"\n" +
		"          Mode: Master  Frequency: 2.437 GHz  Band: 2.4 GHz  Channel: 6\n" +
		"          Signal: -67 dBm  Quality: 45/70\n" +
		"          Encryption: open\n"

	iwCells = "BSS aa:bb:cc:dd:ee:ff(on phy1-sta0)\n" +
		"\tfreq: 5180\n" +
		"\tSSID: Neighbour-AP\n" +
		"\t* signal: -52.00 dBm\n"

	iwUsage = "Usage:	iw [options] dev <devname> scan [-u] [freq <freq>*] [ies <hex>] [ssid <ssid>*|passive]\n"
)

// TestScanChainFallsThroughIwinfoRefusal is the regression test for the
// MT3000 defect: `iwinfo <dev> scan` answering "Scanning not possible" must
// NOT end the walk. The chain has to reach the phy-level strategy and report
// the networks it finds.
func TestScanChainFallsThroughIwinfoRefusal(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iwinfo scan 2>&1", "No such wireless backend: scan\n"},
		{"iwinfo 2>&1", iwinfoEnumeration},
		{"iwinfo phy0-ap0 scan 2>&1", "Scanning not possible\n\n"},
		{"iwinfo phy1-ap0 scan 2>&1", "Scanning not possible\n\n"},
		{"iw phy phy0 scan 2>&1", "Scanning not possible\n\n"},
		{"iw phy phy1 scan 2>&1", iwCells},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iw phy <phy> scan" {
		t.Errorf("Strategy = %q, want %q (the chain must fall through the refused iwinfo attempts)", res.Strategy, "iw phy <phy> scan")
	}
	if len(res.SSIDs) != 1 || res.SSIDs[0].Name != "Neighbour-AP" {
		t.Errorf("SSIDs = %+v, want exactly [Neighbour-AP]", res.SSIDs)
	}
	if !router.calledWith("iw phy phy1 scan") {
		t.Errorf("the phy-level strategy was never attempted; calls = %q", router.calls)
	}
	// The refusal must be visible in the log, not swallowed.
	if joined := strings.Join(res.Log, "\n"); !strings.Contains(joined, "Scanning not possible") {
		t.Errorf("log does not record the refusal: %q", joined)
	}
}

// TestScanChainStopsAtFirstWinningStrategy pins the other half of the
// contract: the chain returns as soon as a strategy yields networks.
func TestScanChainStopsAtFirstWinningStrategy(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iwinfo scan 2>&1", "No such wireless backend: scan\n"},
		{"iwinfo 2>&1", iwinfoEnumeration},
		{"iwinfo phy0-ap0 scan 2>&1", iwinfoCells},
		{"iwinfo phy1-ap0 scan 2>&1", "Scanning not possible\n\n"},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iwinfo <dev> scan" {
		t.Errorf("Strategy = %q, want %q", res.Strategy, "iwinfo <dev> scan")
	}
	if len(res.SSIDs) != 2 {
		t.Fatalf("SSIDs = %+v, want the 2 networks from the per-interface scan", res.SSIDs)
	}
	if router.calledWith("iw phy") || router.calledWith("iw dev scan") {
		t.Errorf("chain kept scanning after a winning strategy; calls = %q", router.calls)
	}
}

// TestScanChainFallsThroughZeroNetworkParse covers the second half of the
// defect: an attempt that does not look like an error but parses to zero
// networks must not be returned as the answer either.
func TestScanChainFallsThroughZeroNetworkParse(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iwinfo scan 2>&1", "Scanning not possible\n\n"},
		{"iwinfo 2>&1", iwinfoEnumeration},
		{"iwinfo phy0-ap0 scan 2>&1", iwinfoCells},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iwinfo <dev> scan" {
		t.Errorf("Strategy = %q, want %q", res.Strategy, "iwinfo <dev> scan")
	}
	if len(res.SSIDs) != 2 {
		t.Errorf("SSIDs = %+v, want 2 networks", res.SSIDs)
	}
}

// TestScanChainTreatsUsageTextAsRefusal pins `iw dev scan`'s invalid-syntax
// usage text: the last-resort branch used to accept it because it only
// checked for "command not found".
func TestScanChainTreatsUsageTextAsRefusal(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iwinfo scan 2>&1", "Scanning not possible\n\n"},
		{"iwinfo 2>&1", ""},
		{"iw phy phy0 scan 2>&1", ""},
		{"iw phy phy1 scan 2>&1", ""},
		{"iwinfo wlan0 scan", "No such wireless device: wlan0\n"},
		{"iw dev scan 2>&1", iwUsage},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != strategyNone {
		t.Errorf("Strategy = %q, want %q", res.Strategy, strategyNone)
	}
	if len(res.SSIDs) != 0 {
		t.Errorf("SSIDs = %+v, want none", res.SSIDs)
	}
	if strings.Join(res.Log, "\n") == "" {
		t.Error("log is empty: the operator cannot see which attempts were made")
	}
}

// TestScanChainAllStrategiesFailKeepsEvidence checks the negative case keeps
// the router's own words for the debug field.
func TestScanChainAllStrategiesFailKeepsEvidence(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"", "Scanning not possible\n\n"}, // every command refuses
	}}

	res := scanViaChain(router.run)

	if res.Strategy != strategyNone || len(res.SSIDs) != 0 {
		t.Errorf("res = %+v, want no strategy and no SSIDs", res)
	}
	if !strings.Contains(res.LastRaw, "Scanning not possible") {
		t.Errorf("LastRaw = %q, want the router's refusal text", res.LastRaw)
	}
	if len(res.Log) != len(scanChain()) {
		t.Errorf("log has %d lines, want one per strategy (%d)", len(res.Log), len(scanChain()))
	}
}

// TestWirelessInterfaces covers the interface enumeration used by strategy 2.
func TestWirelessInterfaces(t *testing.T) {
	got := wirelessInterfaces(iwinfoEnumeration)
	want := []string{"phy0-ap0", "phy1-ap0"}
	if len(got) != len(want) {
		t.Fatalf("wirelessInterfaces = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("wirelessInterfaces[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if devs := wirelessInterfaces("Usage:\n\tiwinfo <device> info\n"); len(devs) != 0 {
		t.Errorf("wirelessInterfaces(usage text) = %q, want none", devs)
	}
}

// TestBuildScanResponse pins the API contract the next person reads: WHICH
// strategy produced the SSIDs, plus the per-attempt log.
func TestBuildScanResponse(t *testing.T) {
	t.Run("networks found", func(t *testing.T) {
		res := scanResult{
			SSIDs:    []wifiSSID{{Name: "Cafe WiFi", Encryption: "wpa2", Band: "2.4"}},
			Strategy: "iwinfo <dev> scan",
			Log:      []string{"[2] iwinfo <dev> scan: 1 network(s)"},
		}
		status, body := buildScanResponse(res)
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200", status)
		}
		if body["strategy"] != "iwinfo <dev> scan" {
			t.Errorf("strategy = %v, want iwinfo <dev> scan", body["strategy"])
		}
		if _, ok := body["log"]; !ok {
			t.Error("log field missing from the response")
		}
		enc, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(enc), "\"strategy\"") {
			t.Errorf("JSON body has no strategy field: %s", enc)
		}
	})

	t.Run("all strategies refused", func(t *testing.T) {
		res := scanResult{
			Strategy: strategyNone,
			Log:      []string{"[1] iwinfo scan (no device argument): refused: No such wireless backend: scan"},
			LastRaw:  "Scanning not possible\n\n",
		}
		status, body := buildScanResponse(res)
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200 (the handler reached the router)", status)
		}
		got, _ := body["debug"].(string)
		if !strings.Contains(got, "Scanning not possible") {
			t.Errorf("debug = %q, want the router's refusal text", got)
		}
		ssids, ok := body["ssids"].([]wifiSSID)
		if !ok || len(ssids) != 0 {
			t.Errorf("ssids = %#v, want an empty (non-null) list", body["ssids"])
		}
	})

	t.Run("nothing came back at all", func(t *testing.T) {
		res := scanResult{Strategy: strategyNone, Log: []string{"[1] iwinfo scan (no device argument): no output"}}
		status, body := buildScanResponse(res)
		if status != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", status)
		}
		if body["error"] == nil || body["error"] == "" {
			t.Error("500 response carries no error message")
		}
		if body["log"] == nil {
			t.Error("500 response carries no per-strategy log")
		}
	})
}
