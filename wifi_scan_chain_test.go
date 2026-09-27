package main

import (
	"encoding/json"
	"net/http"
	"os"
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

// calledExactly reports whether the chain ran a command equal to cmd.
func (f *fakeRouter) calledExactly(cmd string) bool {
	for _, c := range f.calls {
		if c == cmd {
			return true
		}
	}
	return false
}

const (
	iwinfoEnumeration = "phy0-ap0  ESSID: \"TollGate-F794\"\n" +
		"          Access Point: 94:83:C4:8C:59:C3\n" +
		"          Mode: Master  Channel: 1 (2.4 GHz)  HT Mode: HT20\n" +
		"          Supports VAPs: yes  PHY name: phy0\n" +
		"\n" +
		"phy1-ap0  ESSID: \"TollGate-F794-5G\"\n" +
		"          Mode: Master  Channel: 36 (5 GHz)  HT Mode: HT20\n" +
		"          Supports VAPs: yes  PHY name: phy1\n"

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

	// iwUsage is the operator's REAL pasted output verbatim: iw 6.17 printing
	// its top-level usage, which is what `iw dev scan` (and, on iw >= 6,
	// `iw phy phy0 scan`) produce — `scan` is declared CIB_NETDEV, so a
	// phy-identified or device-less invocation matches no command and iw falls
	// back to its top-level usage (iw.c:471-474 → iw.c:640-641).
	iwUsage = "Usage: iw [options] command\n" +
		"Options:\n" +
		"\t--debug\t\tenable netlink debugging\n" +
		"\t--version\tshow version (6.17)\n" +
		"Commands:\n" +
		"\tdev <devname> disconnect\n" +
		"\tdev <devname> scan [-u] [freq <freq>*] [ies <hex>] [ssid <ssid>*|passive]\n"

	// iwDevMainline is `iw dev` on the failing box: a stock OpenWrt 25.12.5
	// filogic image (GL-MT3000, one MT7981B chip → phy0 + phy1) whose wireless
	// netdevs are named <phy>-apN, NOT wlan0/wlan1.
	// Line shapes: iw interface.c:386 `phy#%d`, :391 `\tInterface %s`.
	iwDevMainline = "phy#0\n" +
		"\tInterface phy0-ap0\n" +
		"\t\tifindex 10\n" +
		"\t\twdev 0x1\n" +
		"\t\taddr 94:83:c4:8c:59:c3\n" +
		"\t\ttype AP\n" +
		"\t\tchannel 1 (2412 MHz), width: 20 MHz, center1: 2412 MHz\n" +
		"\t\ttxpower 20.00 dBm\n" +
		"phy#1\n" +
		"\tInterface phy1-ap1\n" +
		"\t\tifindex 11\n" +
		"\t\twdev 0x2\n" +
		"\t\ttype AP\n" +
		"\t\tchannel 36 (5180 MHz), width: 80 MHz, center1: 5210 MHz\n"

	// `iw dev <dev> scan` results — iw's own scan output.
	iwDevScanCells = "BSS aa:bb:cc:dd:ee:ff(on phy0-ap0)\n" +
		"\tfreq: 2412\n" +
		"\tSSID: Cafe WiFi\n" +
		"\t* signal: -45.00 dBm\n" +
		"BSS 11:22:33:44:55:66(on phy0-ap0)\n" +
		"\tfreq: 2437\n" +
		"\tSSID: HomeNet\n" +
		"\t* signal: -67.00 dBm\n"

	// busybox ash's wording when the CLI is not installed. Deliberately NOT
	// bash's "command not found".
	iwinfoAbsent = "sh: iwinfo: not found\n"
	iwAbsent     = "sh: iw: not found\n"
)

// mainlineRouter is one stock filogic box's answers to EVERY command the old
// chain and the fixed chain can ask — hermetically, no hardware. It encodes
// the failure the operator hit: `iw` is present (iw 6.17) and its usage text
// reached the UI, while the `iwinfo` CLI is absent and no strategy ever
// discovered phy0-ap0/phy1-ap1.
func mainlineRouter() *fakeRouter {
	return &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwDevMainline},
		{"iwinfo 2>&1", iwinfoAbsent},
		{"iwinfo scan 2>&1", iwinfoAbsent},
		{"iwinfo phy0-ap0 scan 2>&1", iwinfoAbsent},
		{"iwinfo phy1-ap1 scan 2>&1", iwinfoAbsent},
		{"iwinfo wlan0 scan 2>&1", iwinfoAbsent},
		{"iwinfo wlan1 scan 2>&1", iwinfoAbsent},
		// Old-chain-only commands. Both print iw's top-level usage on iw >= 6:
		// `iw dev scan` has no device, and `scan` is not a phy command.
		{"iw dev scan 2>&1", iwUsage},
		{"iw phy phy0 scan 2>&1", iwUsage},
		{"iw phy phy1 scan 2>&1", iwUsage},
		// The fixed chain's per-interface scans: 2.4 GHz answers, the 5 GHz vif
		// is refused by the driver (the "busy" class).
		{"iw dev phy0-ap0 scan 2>&1", iwDevScanCells},
		{"iw dev phy1-ap1 scan 2>&1", "command failed: Device or resource busy (-16)\n"},
	}}
}

// TestScanChainFindsNetworksOnMainlineFilogic is the regression test for the
// live defect: on a stock mainline filogic box the chain must discover the real
// netdevs (`iw dev` → phy0-ap0/phy1-ap1) and scan them with `iw dev <dev>
// scan`, even though the `iwinfo` CLI is absent. Before the fix every strategy
// refused here and the UI showed iw's usage text as "No WiFi networks
// detected".
func TestScanChainFindsNetworksOnMainlineFilogic(t *testing.T) {
	router := mainlineRouter()

	res := scanViaChain(router.run)

	if res.Strategy != "iw dev <dev> scan" {
		t.Errorf("Strategy = %q, want %q — the chain must enumerate with `iw dev` and scan the discovered interfaces", res.Strategy, "iw dev <dev> scan")
	}
	if len(res.SSIDs) != 2 || res.SSIDs[0].Name != "Cafe WiFi" || res.SSIDs[1].Name != "HomeNet" {
		t.Fatalf("SSIDs = %+v, want [Cafe WiFi, HomeNet]", res.SSIDs)
	}
	// The device-less form that produced the operator's usage text must be gone.
	if router.calledExactly("iw dev scan 2>&1") {
		t.Errorf("the chain still runs the invalid device-less `iw dev scan`; calls = %q", router.calls)
	}
	if router.calledWith("iw phy ") {
		t.Errorf("the chain still runs a phy-level scan (no such command exists in iw); calls = %q", router.calls)
	}
	// Discovery must be reported, naming the interfaces it found.
	joined := strings.Join(res.Log, "\n")
	if !strings.Contains(joined, "[discovery] iw dev: ifaces=phy0-ap0,phy1-ap1") {
		t.Errorf("log does not report the discovered interfaces:\n%s", joined)
	}
}

// TestLegacyChainCannotScanMainline is the non-vacuity CONTROL for the test
// above: the pre-fix chain (`legacyScanChain`) driven against the SAME fixture
// through the SAME walker must find nothing. If this ever starts passing, the
// mainline test proves nothing about the fix.
func TestLegacyChainCannotScanMainline(t *testing.T) {
	// First: the fixture is not vacuous — the per-interface `iw` scan really
	// does return networks on this router.
	probe := mainlineRouter()
	if got := parseIwScan(probe.run("iw dev phy0-ap0 scan 2>&1")); len(got) != 2 {
		t.Fatalf("fixture is stale: `iw dev phy0-ap0 scan` returned %d networks, want 2", len(got))
	}

	router := mainlineRouter()
	res := walkChain(router.run, legacyScanChain(), scanResult{Strategy: strategyNone})

	if res.Strategy != strategyNone {
		t.Errorf("legacy chain Strategy = %q, want %q — the fixture must still defeat the pre-fix chain", res.Strategy, strategyNone)
	}
	if len(res.SSIDs) != 0 {
		t.Errorf("legacy chain SSIDs = %+v, want none", res.SSIDs)
	}
	if router.calledWith("iw dev phy0-ap0 scan") {
		t.Errorf("the pre-fix chain is not the pre-fix chain: it ran a discovered-interface scan; calls = %q", router.calls)
	}
}

// TestScanChainFallsThroughIwinfoRefusal keeps the original MT3000 regression's
// intent: a refusing per-interface iwinfo scan must not end the walk — the
// chain must reach iw's per-interface scan and return its networks.
func TestScanChainFallsThroughIwinfoRefusal(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwDevMainline},
		{"iwinfo scan 2>&1", "No such wireless backend: scan\n"},
		{"iwinfo phy0-ap0 scan 2>&1", "Scanning not possible\n\n"},
		{"iwinfo phy1-ap1 scan 2>&1", "Scanning not possible\n\n"},
		{"iw dev phy0-ap0 scan 2>&1", iwCells},
		{"iw dev phy1-ap1 scan 2>&1", "Scanning not possible\n\n"},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iw dev <dev> scan" {
		t.Errorf("Strategy = %q, want %q (the chain must fall through the refused iwinfo attempts)", res.Strategy, "iw dev <dev> scan")
	}
	if len(res.SSIDs) != 1 || res.SSIDs[0].Name != "Neighbour-AP" {
		t.Errorf("SSIDs = %+v, want exactly [Neighbour-AP]", res.SSIDs)
	}
	if !router.calledWith("iw dev phy0-ap0 scan") {
		t.Errorf("the per-interface iw scan was never attempted; calls = %q", router.calls)
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
	// The only `iw` command allowed before the winner is discovery itself.
	for _, c := range router.calls {
		if strings.HasPrefix(c, "iw ") && c != "iw dev 2>&1" {
			t.Errorf("chain kept scanning after a winning strategy; calls = %q", router.calls)
			break
		}
	}
}

// TestScanChainFallsThroughZeroNetworkParse covers the second half of the
// original defect: an attempt that does not look like an error but parses to
// zero networks must not be returned as the answer either.
func TestScanChainFallsThroughZeroNetworkParse(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwDevMainline},
		{"iwinfo scan 2>&1", "Scanning not possible\n\n"},
		// A Cell block with no ESSID line: not a refusal, but zero networks.
		{"iwinfo phy0-ap0 scan 2>&1", "Cell 01 - Address: AA:BB:CC:DD:EE:FF\n          Signal: -45 dBm\n"},
		{"iw dev phy0-ap0 scan 2>&1", iwDevScanCells},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iw dev <dev> scan" {
		t.Errorf("Strategy = %q, want %q", res.Strategy, "iw dev <dev> scan")
	}
	if len(res.SSIDs) != 2 {
		t.Errorf("SSIDs = %+v, want 2 networks", res.SSIDs)
	}
	if joined := strings.Join(res.Log, "\n"); !strings.Contains(joined, "parsed 0 networks") {
		t.Errorf("log does not record the zero-network parse: %q", joined)
	}
}

// TestScanChainTreatsUsageTextAsRefusal pins the operator's own observed text:
// iw's usage output is a REFUSAL, never an empty-but-successful scan. It is the
// text a device-less `iw dev scan` printed in the field, and the text iw prints
// for a phy-identified scan on iw >= 6.
func TestScanChainTreatsUsageTextAsRefusal(t *testing.T) {
	if !scanFailedHeuristic(iwUsage) {
		t.Fatal("scanFailedHeuristic(iw usage text) = false: the operator's exact output would be parsed as scan results")
	}

	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwDevMainline},
		{"iwinfo scan 2>&1", "Scanning not possible\n\n"},
		{"iwinfo 2>&1", ""},
		{"iw dev phy0-ap0 scan 2>&1", iwUsage},
		{"iw dev phy1-ap1 scan 2>&1", iwUsage},
		{"iwinfo wlan0 scan 2>&1", "No such wireless device: wlan0\n"},
		{"iwinfo wlan1 scan 2>&1", "No such wireless device: wlan1\n"},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != strategyNone {
		t.Errorf("Strategy = %q, want %q", res.Strategy, strategyNone)
	}
	if len(res.SSIDs) != 0 {
		t.Errorf("SSIDs = %+v, want none", res.SSIDs)
	}
	joined := strings.Join(res.Log, "\n")
	if !strings.Contains(joined, "refused: Usage:") {
		t.Errorf("the usage text is not logged as a refusal: %q", joined)
	}
	// And the refusal reaches the API as an explanation, not as an empty result.
	status, body := buildScanResponse(res)
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200 (the router answered, it refused)", status)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "Usage:") {
		t.Errorf("error = %q, want the router's own usage text in it", msg)
	}
}

// TestScanRefusalSummaryIsHonest pins the all-refused path: the operator must
// be told WHICH methods ran and WHY each refused — never "no networks
// detected", which reads as "nothing is nearby".
func TestScanRefusalSummaryIsHonest(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwAbsent},
		{"iwinfo 2>&1", iwinfoAbsent},
		{"ls /sys/class/ieee80211/ 2>&1", "phy0\nphy1\n"},
		{"iwinfo scan 2>&1", iwinfoAbsent},
		{"iw dev scan 2>&1", iwUsage},
		{"iw phy phy0 scan 2>&1", iwUsage},
		{"iwinfo wlan0 scan 2>&1", iwinfoAbsent},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != strategyNone || len(res.SSIDs) != 0 {
		t.Fatalf("res = %+v, want no strategy and no SSIDs", res)
	}
	if !strings.Contains(res.Log[0], "2 phy(s) present") {
		t.Errorf("discovery line does not flag radios-present-but-down: %q", res.Log[0])
	}

	status, body := buildScanResponse(res)
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Fatal("error is empty: the UI has nothing to show but a bare empty list")
	}
	for _, want := range []string{
		"refused by every method",
		"[1] iwinfo scan",
		"[2] iwinfo <dev> scan",
		"[3] iw dev <dev> scan",
		"[4] iwinfo wlan0/wlan1 scan",
		"not found",
		"radios are down",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "No WiFi networks detected") {
		t.Errorf("error still claims no networks were detected:\n%s", msg)
	}
	// The router's own last words stay available for the debug field.
	if !strings.Contains(body["debug"].(string), "not found") {
		t.Errorf("debug = %q, want the router's own last output", body["debug"])
	}
	// Every strategy is represented in the log, plus the discovery line.
	if len(res.Log) != 1+len(scanChain(wifiDevices{})) {
		t.Errorf("log has %d lines, want 1 discovery + one per strategy (%d)", len(res.Log), len(scanChain(wifiDevices{})))
	}
}

// TestDiscoverWifiDevices covers the enumerator order: iw dev first (it is on
// the box), bare iwinfo as the vendor/older fallback, sysfs only to answer
// "are there radios at all?".
func TestDiscoverWifiDevices(t *testing.T) {
	t.Run("iw dev wins", func(t *testing.T) {
		r := &fakeRouter{script: []fakeCmd{{"iw dev 2>&1", iwDevMainline}}}
		d := discoverWifiDevices(r.run)
		if got := strings.Join(d.ifaces, ","); got != "phy0-ap0,phy1-ap1" {
			t.Errorf("ifaces = %q, want phy0-ap0,phy1-ap1", got)
		}
		if got := strings.Join(d.phys, ","); got != "phy0,phy1" {
			t.Errorf("phys = %q, want phy0,phy1", got)
		}
		if !strings.HasPrefix(d.log, "iw dev: ifaces=") {
			t.Errorf("log = %q, want it to name the enumerator", d.log)
		}
		if r.calledWith("iwinfo 2>&1") {
			t.Error("iwinfo was consulted although `iw dev` already answered")
		}
	})

	t.Run("iw absent falls back to iwinfo", func(t *testing.T) {
		r := &fakeRouter{script: []fakeCmd{
			{"iw dev 2>&1", iwAbsent},
			{"iwinfo 2>&1", iwinfoEnumeration},
		}}
		d := discoverWifiDevices(r.run)
		if got := strings.Join(d.ifaces, ","); got != "phy0-ap0,phy1-ap0" {
			t.Errorf("ifaces = %q, want phy0-ap0,phy1-ap0", got)
		}
		if got := strings.Join(d.phys, ","); got != "phy0,phy1" {
			t.Errorf("phys = %q, want phy0,phy1 (from iwinfo's PHY name: lines)", got)
		}
		if !strings.Contains(d.log, "iwinfo: ifaces=") || !strings.Contains(d.log, "not found") {
			t.Errorf("log = %q, want the iwinfo source AND iw's refusal", d.log)
		}
	})

	t.Run("nothing enumerates, radios present", func(t *testing.T) {
		r := &fakeRouter{script: []fakeCmd{
			{"iw dev 2>&1", iwAbsent},
			{"iwinfo 2>&1", iwinfoAbsent},
			{"ls /sys/class/ieee80211/ 2>&1", "phy0\nphy1\n"},
		}}
		d := discoverWifiDevices(r.run)
		if len(d.ifaces) != 0 {
			t.Errorf("ifaces = %q, want none", d.ifaces)
		}
		if !strings.Contains(d.log, "2 phy(s) present") {
			t.Errorf("log = %q, want the radios-down hint", d.log)
		}
	})

	t.Run("no wireless hardware at all", func(t *testing.T) {
		r := &fakeRouter{script: []fakeCmd{
			{"iw dev 2>&1", ""},
			{"iwinfo 2>&1", ""},
			{"ls /sys/class/ieee80211/ 2>&1", "ls: /sys/class/ieee80211/: No such file or directory\n"},
		}}
		d := discoverWifiDevices(r.run)
		if len(d.ifaces) != 0 || len(d.phys) != 0 {
			t.Errorf("d = %+v, want nothing discovered", d)
		}
		if !strings.Contains(d.log, "No such file or directory") {
			t.Errorf("log = %q, want the kernel's own answer", d.log)
		}
	})
}

// TestScanChainUsesDiscoveredNamesOnly proves no name is hardcoded any more: a
// box whose single interface is phy2-ap7 must be scanned under that name, and
// nothing may reference phy0/phy1.
func TestScanChainUsesDiscoveredNamesOnly(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", "phy#2\n\tInterface phy2-ap7\n\t\ttype AP\n"},
		{"iwinfo scan 2>&1", "No such wireless backend: scan\n"},
		{"iwinfo phy2-ap7 scan 2>&1", "Scanning not possible\n\n"},
		{"iw dev phy2-ap7 scan 2>&1", iwDevScanCells},
	}}

	res := scanViaChain(router.run)

	if res.Strategy != "iw dev <dev> scan" {
		t.Fatalf("Strategy = %q, want %q", res.Strategy, "iw dev <dev> scan")
	}
	if !router.calledWith("iw dev phy2-ap7 scan") {
		t.Errorf("the discovered interface was not scanned; calls = %q", router.calls)
	}
	for _, calls := range router.calls {
		if strings.Contains(calls, "phy0") || strings.Contains(calls, "phy1") {
			t.Errorf("hardcoded phy name in command %q", calls)
		}
	}
}

// TestWirelessInterfaces covers the interface enumeration used by the iwinfo
// fallback plus every inventory parser the discovery probe relies on.
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
	if devs := wirelessInterfaces(iwinfoAbsent); len(devs) != 0 {
		t.Errorf("wirelessInterfaces(tool absent) = %q, want none", devs)
	}
	if phys := wirelessPhys(iwinfoEnumeration); strings.Join(phys, ",") != "phy0,phy1" {
		t.Errorf("wirelessPhys = %q, want phy0,phy1", phys)
	}
	if ifaces := iwInterfaceNames(iwDevMainline); strings.Join(ifaces, ",") != "phy0-ap0,phy1-ap1" {
		t.Errorf("iwInterfaceNames = %q, want phy0-ap0,phy1-ap1", ifaces)
	}
	// iw's usage text contains the word "Interface"? No — but it DOES contain
	// `dev <devname>`; the parser must not invent interfaces from it.
	if ifaces := iwInterfaceNames(iwUsage); len(ifaces) != 0 {
		t.Errorf("iwInterfaceNames(iw usage) = %q, want none", ifaces)
	}
	if phys := iwPhyNames(iwDevMainline); strings.Join(phys, ",") != "phy0,phy1" {
		t.Errorf("iwPhyNames = %q, want phy0,phy1", phys)
	}
	if phys := sysfsPhyNames("phy0\nphy1\n"); strings.Join(phys, ",") != "phy0,phy1" {
		t.Errorf("sysfsPhyNames = %q, want phy0,phy1", phys)
	}
	if phys := sysfsPhyNames("ls: /sys/class/ieee80211/: No such file or directory\n"); len(phys) != 0 {
		t.Errorf("sysfsPhyNames(error) = %q, want none", phys)
	}
}

// TestBuildScanResponse pins the API contract the next person reads: WHICH
// strategy produced the SSIDs, plus the per-attempt log.
func TestBuildScanResponse(t *testing.T) {
	t.Run("networks found", func(t *testing.T) {
		res := scanResult{
			SSIDs:    []wifiSSID{{Name: "Cafe WiFi", Encryption: "wpa2", Band: "2.4"}},
			Strategy: "iwinfo <dev> scan",
			Log:      []string{"[discovery] iw dev: ifaces=phy0-ap0 phys=phy0", "[2] iwinfo <dev> scan: 1 network(s)"},
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
		if _, ok := body["error"]; ok {
			t.Error("a successful scan must not carry an error field")
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
			Log: []string{
				"[discovery] iw dev: ifaces=phy0-ap0 phys=phy0",
				"[1] iwinfo scan (no device argument): refused: sh: iwinfo: not found",
				"[2] iwinfo <dev> scan (ifaces=phy0-ap0): refused: Scanning not possible",
			},
			LastRaw: "Scanning not possible\n\n",
		}
		status, body := buildScanResponse(res)
		if status != http.StatusOK {
			t.Errorf("status = %d, want 200 (the handler reached the router)", status)
		}
		msg, _ := body["error"].(string)
		if !strings.Contains(msg, "[1] iwinfo scan") || !strings.Contains(msg, "[2] iwinfo <dev> scan") {
			t.Errorf("error does not name the methods tried: %q", msg)
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
		if !strings.Contains(body["error"].(string), "Not one byte came back from the router") {
			t.Errorf("error does not explain the silence: %v", body["error"])
		}
		if body["log"] == nil {
			t.Error("500 response carries no per-strategy log")
		}
	})
}

// TestScanChainAllStrategiesFailKeepsEvidence checks the negative case keeps
// the router's own words for the debug field.
func TestScanChainAllStrategiesFailKeepsEvidence(t *testing.T) {
	router := &fakeRouter{script: []fakeCmd{
		{"iw dev 2>&1", iwDevMainline},
		{"", "Scanning not possible\n\n"}, // every other command refuses
	}}

	res := scanViaChain(router.run)

	if res.Strategy != strategyNone || len(res.SSIDs) != 0 {
		t.Errorf("res = %+v, want no strategy and no SSIDs", res)
	}
	if !strings.Contains(res.LastRaw, "Scanning not possible") {
		t.Errorf("LastRaw = %q, want the router's refusal text", res.LastRaw)
	}
	// One discovery line plus one line per strategy.
	devs := discoverWifiDevices(mainlineRouter().run)
	if len(res.Log) != 1+len(scanChain(devs)) {
		t.Errorf("log has %d lines, want 1 + one per strategy (%d):\n%s",
			len(res.Log), 1+len(scanChain(devs)), strings.Join(res.Log, "\n"))
	}
}

// TestScanUiNeverClaimsNoNetworks pins the UI half of the honesty fix: the
// refusal path must surface the server's explanation, and the misleading
// "No WiFi networks detected" wording (which sent the operator hunting for a
// password problem) must be gone.
func TestScanUiNeverClaimsNoNetworks(t *testing.T) {
	page, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	html := string(page)

	if strings.Contains(html, "No WiFi networks detected") {
		t.Error("index.html still claims no networks were detected — a refused scan is not an empty scan")
	}
	if !strings.Contains(html, "const evidence = data.log || data.debug;") {
		t.Error("index.html does not gate the password hint on router evidence: a refusal is not a credential problem")
	}
	if !strings.Contains(html, "data.error + ' — check router password and try Rescan.'") {
		t.Error("index.html lost the password hint for responses with no router evidence at all")
	}
}

// legacyScanChain is the PRE-FIX chain, kept in the test file ONLY as the
// non-vacuity control for TestLegacyChainCannotScanMainline. It is the exact
// strategy list that shipped and failed on the operator's stock MT3000:
// device-less `iwinfo scan`, `iwinfo <dev> scan` off bare-iwinfo enumeration,
// hardcoded `iw phy phy0/phy1 scan`, the `wlan0 || wlan1` shell chain, and the
// invalid device-less `iw dev scan`. If this control ever starts winning on the
// mainline fixture, the fixture (or the walker) has been weakened.
func legacyScanChain() []scanStrategy {
	return []scanStrategy{
		{
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
			name:   "iw dev scan",
			parser: parseIwScan,
			run: func(run scanRunner) ([]scanCommand, string) {
				return []scanCommand{runCmd(run, "iw dev scan 2>&1")}, "all wireless devices"
			},
		},
	}
}
