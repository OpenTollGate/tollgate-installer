package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// discover_diagnostics_test.go pins the two halves of the second half of the
// defect: an honest classification of what answered (an unidentified LAN host
// is NOT a router) and an actionable failure report when nothing answered.
//
// The old behaviour is one sentence — "Scan failed. Check that the wizard can
// reach your network." — naming no address, no interface state and no
// remediation. Everything below fails against that sentence.

// TestClassifyProbed pins the classification and its operator-facing label.
func TestClassifyProbed(t *testing.T) {
	cases := []struct {
		name         string
		info         RouterInfo
		wantIdent    bool
		wantNoteWant string
	}{
		{
			name:         "openwrt identified over ssh",
			info:         RouterInfo{IP: "192.168.1.1", SSH: true, Vendor: "OpenWrt", Model: "glinet,gl-mt3000", Firmware: "OpenWrt 25.12.5"},
			wantIdent:    true,
			wantNoteWant: "TollGate/OpenWrt router (identified)",
		},
		{
			name:         "stock gl.inet identified",
			info:         RouterInfo{IP: "192.168.8.1", SSH: true, Vendor: "GL.iNet", Model: "gl-mt3000", Firmware: "GL.iNet 4.5.16"},
			wantIdent:    true,
			wantNoteWant: "GL.iNet router (identified)",
		},
		{
			name:         "other identified vendor",
			info:         RouterInfo{Vendor: "RouterOS", SSH: true},
			wantIdent:    true,
			wantNoteWant: "RouterOS router (identified)",
		},
		{
			name:         "ssh only is not a router",
			info:         RouterInfo{IP: "192.168.2.34", SSH: true, Vendor: "unknown", Model: "unknown"},
			wantIdent:    false,
			wantNoteWant: "unverified host on your LAN — answers :22",
		},
		{
			name:         "http only is not a router",
			info:         RouterInfo{IP: "192.168.2.48", HTTPPort: 80, Vendor: "unknown"},
			wantIdent:    false,
			wantNoteWant: "unverified host on your LAN — answers :80",
		},
		{
			name:         "ssh and http",
			info:         RouterInfo{IP: "192.168.2.9", SSH: true, HTTPPort: 8080, Vendor: "unknown"},
			wantIdent:    false,
			wantNoteWant: "unverified host on your LAN — answers :22 and :8080",
		},
		{
			name:         "nothing answered at all",
			info:         RouterInfo{IP: "192.168.1.1", Vendor: "unknown"},
			wantIdent:    false,
			wantNoteWant: "did not answer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identified, note := classifyProbed(tc.info)
			if identified != tc.wantIdent {
				t.Errorf("identified = %v, want %v (note %q)", identified, tc.wantIdent, note)
			}
			if !strings.Contains(note, tc.wantNoteWant) {
				t.Errorf("note = %q, want it to contain %q", note, tc.wantNoteWant)
			}
		})
	}
}

// TestSortIdentifiedFirst: an identified router must be offered before a
// stranger's laptop that happens to answer :22, and the relative order inside
// each group must not change (candidate priority is still meaningful).
func TestSortIdentifiedFirst(t *testing.T) {
	list := []RouterInfo{
		{IP: "192.168.2.34", SSH: true},
		{IP: "10.47.41.1", Identified: true},
		{IP: "192.168.2.48", HTTPPort: 80},
		{IP: "192.168.2.1", Identified: true},
	}
	sortIdentifiedFirst(list)
	got := []string{}
	for _, r := range list {
		got = append(got, r.IP)
	}
	want := []string{"10.47.41.1", "192.168.2.1", "192.168.2.34", "192.168.2.48"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// diagFixture builds the measured CobradorWave shape: the real gateway on
// wlp58s0, a dead wired interface, an interface with a link but no lease, a
// container bridge, and two ARP strangers that answered.
func diagFixture() ([]ifaceState, []gatewayRoute, []candidateProbe) {
	ifaces := []ifaceState{
		{Name: "wlp58s0", Up: true, Carrier: "1", IPv4: []string{"192.168.2.33/24"}},
		{Name: "enp0s31f6", Up: false, Carrier: "0"},
		{Name: "wwp0s20f0u6i12", Up: true, Carrier: "1"},
		{Name: "docker0", Up: true, Carrier: "1", IPv4: []string{"172.17.0.1/16"}, Virtual: true},
	}
	gateways := []gatewayRoute{{IP: "192.168.2.1", Iface: "wlp58s0"}}
	probes := []candidateProbe{
		{IP: "192.168.2.1", Source: sourceGateway, Detail: "dev wlp58s0", SSH: false},
		{IP: "192.168.2.254", Source: sourceSubnet, Detail: "wlp58s0 192.168.2.33/24"},
		{IP: "192.168.1.1", Source: sourceCommon},
		{IP: "192.168.2.48", Source: sourceARP, Detail: "74:bf:c0:ac:ae:b4", HTTPPort: 80, Note: "unverified host on your LAN — answers :80"},
	}
	return ifaces, gateways, probes
}

// TestBuildScanDiagnosticsReportsInterfaceState pins the interface section:
// every non-loopback interface with its state and carrier, and the two
// conditions the operator cannot see from the wizard today spelled out.
func TestBuildScanDiagnosticsReportsInterfaceState(t *testing.T) {
	ifaces, gateways, probes := diagFixture()
	d := buildScanDiagnostics(ifaces, gateways, probes)

	byName := map[string]ifaceReport{}
	for _, r := range d.Interfaces {
		byName[r.Name] = r
	}
	if len(d.Interfaces) != len(ifaces) {
		t.Fatalf("interfaces = %d, want %d (%+v)", len(d.Interfaces), len(ifaces), d.Interfaces)
	}
	if r := byName["enp0s31f6"]; !strings.Contains(r.Problem, "no link") {
		t.Errorf("enp0s31f6 problem = %q, want it to flag \"no link\" (carrier=0)", r.Problem)
	}
	if r := byName["wwp0s20f0u6i12"]; !strings.Contains(r.Problem, "link up, NO IPv4") {
		t.Errorf("wwp0s20f0u6i12 problem = %q, want it to flag \"link up, NO IPv4\"", r.Problem)
	}
	if r := byName["wlp58s0"]; r.Problem != "" || r.State != "up" || r.Carrier != "1" {
		t.Errorf("wlp58s0 report = %+v, want a clean up/carrier=1 row", r)
	}
	if r := byName["docker0"]; !r.Virtual {
		t.Errorf("docker0 report = %+v, want it marked virtual (excluded from the scan)", r)
	}

	text := formatScanFailure(d)
	for _, want := range []string{
		"wlp58s0", "192.168.2.33/24", "enp0s31f6", "carrier=0",
		"link up, NO IPv4", "no link",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("failure text does not mention %q:\n%s", want, text)
		}
	}
}

// TestBuildScanDiagnosticsNamesAddressesAndSources pins the probe section: the
// operator sees every address that was tried, where it came from, and what each
// port answered — which is the evidence the one-line message withheld.
func TestBuildScanDiagnosticsNamesAddressesAndSources(t *testing.T) {
	ifaces, gateways, probes := diagFixture()
	d := buildScanDiagnostics(ifaces, gateways, probes)

	if len(d.Probes) != len(probes) {
		t.Fatalf("probes = %d, want %d", len(d.Probes), len(probes))
	}
	text := formatScanFailure(d)

	for _, want := range []string{
		"192.168.2.1", "192.168.2.254", "192.168.1.1", "192.168.2.48",
		sourceGateway, sourceSubnet, sourceCommon, sourceARP,
		"ssh=no", "http=80",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("failure text does not mention %q:\n%s", want, text)
		}
	}

	// Every candidate class is accounted for by name, with a count.
	joined := strings.Join(d.Sources, "\n")
	for _, class := range []string{sourceGateway, sourceSubnet, sourceCommon, sourceARP} {
		if !strings.Contains(joined, class) {
			t.Errorf("source summary %q does not name the class %q", joined, class)
		}
	}
	if !strings.Contains(joined, sourceGateway+": 1 ") {
		t.Errorf("source summary = %q, want a count for %q", joined, sourceGateway)
	}

	if !strings.Contains(d.Summary, "answered nothing") {
		t.Errorf("summary = %q, want it to state that nothing answered", d.Summary)
	}
	if !strings.Contains(d.Summary, "4") {
		t.Errorf("summary = %q, want it to name the number of addresses probed", d.Summary)
	}
}

// TestBuildScanDiagnosticsRemediationOrder pins the remediation list, in the
// operator's order, ending with the headless form that always works.
func TestBuildScanDiagnosticsRemediationOrder(t *testing.T) {
	ifaces, _, probes := diagFixture()
	d := buildScanDiagnostics(ifaces, []gatewayRoute{{IP: "192.168.2.1", Iface: "wlp58s0"}}, probes)

	markers := []string{
		"2.5 GbE",             // 1. MT3000 WAN/LAN ports
		"60-120 s",            // 2. still booting after a flash
		"dhclient",            // 3. force a lease
		":22 only",            // 4. vanilla OpenWrt has no LuCI
		"install-and-test.sh", // 5. headless form
	}
	pos := -1
	for _, m := range markers {
		i := strings.Index(d.Text, m)
		if i < 0 {
			t.Fatalf("remediation is missing %q:\n%s", m, d.Text)
		}
		if i < pos {
			t.Errorf("remediation item %q appears out of order:\n%s", m, d.Text)
		}
		pos = i
	}

	// The headless form must be usable as written: an address, an explicit
	// EMPTY password argument, and a Lightning address placeholder.
	if !strings.Contains(d.Text, "<ROUTER-IP> '' <") {
		t.Errorf("failure text does not carry the copy-pasteable headless command:\n%s", d.Text)
	}

	// A tunnel-owned default route is called out, in addition to the five
	// ordered steps (never instead of them).
	withTunnel := buildScanDiagnostics(ifaces, []gatewayRoute{{IP: "10.8.8.1", Iface: "wg0"}}, probes)
	if !strings.Contains(withTunnel.Text, "tunnel") {
		t.Errorf("a tunnel-owned default route was not called out:\n%s", withTunnel.Text)
	}
	if !strings.Contains(withTunnel.Text, "192.168.2.254") {
		t.Error("the tunnel note replaced the probe list instead of adding to it")
	}
	// The plain case must not invent a tunnel warning.
	if strings.Contains(d.Text, "tunnel owns the default route") {
		t.Errorf("a tunnel warning appeared with no tunnel gateway:\n%s", d.Text)
	}
}

// TestBuildScanDiagnosticsNeverDropsACandidate: the failure report must list
// EVERY address that was probed, including the unverified ones — that list is
// what lets the operator recognise their router's subnet.
func TestBuildScanDiagnosticsNeverDropsACandidate(t *testing.T) {
	ifaces, gateways, probes := diagFixture()
	probes = append(probes, candidateProbe{IP: "10.47.41.1", Source: sourceCommon})
	d := buildScanDiagnostics(ifaces, gateways, probes)
	text := formatScanFailure(d)
	for _, p := range probes {
		if !strings.Contains(text, p.IP) {
			t.Errorf("probed address %s is missing from the failure report:\n%s", p.IP, text)
		}
	}
	if !strings.Contains(text, "10.47.41.1") {
		t.Error("an unanswered candidate was dropped from the report")
	}
}

// TestScanNetworkClassificationAndProbes drives the scan itself with a frozen
// host state and frozen probe answers — no network, no shelling out — and pins
// the end-to-end contract: an identified router is first, an unverified host is
// labelled and kept, every candidate is probed exactly once.
func TestScanNetworkClassificationAndProbes(t *testing.T) {
	origNet, origProbe, origIfaces := collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn
	defer func() {
		collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn = origNet, origProbe, origIfaces
	}()

	collectHostNetworkFn = func() hostNetwork {
		return hostNetwork{
			Gateways: []gatewayRoute{{IP: "10.47.41.1", Iface: "enp1s0"}},
			Addrs:    []localAddr{{Iface: "enp1s0", CIDR: "10.47.41.23/24"}},
			Common:   []string{"192.168.1.1"},
			ARP:      []arpEntry{{IP: "192.168.2.48", MAC: "74:bf:c0:ac:ae:b4", Iface: "wlan0"}},
		}
	}
	collectInterfaceStatesFn = func() []ifaceState {
		return []ifaceState{{Name: "enp1s0", Up: true, Carrier: "1", IPv4: []string{"10.47.41.23/24"}}}
	}
	answers := map[string]RouterInfo{
		"10.47.41.1":   {SSH: true, Vendor: "OpenWrt", Model: "glinet,gl-mt3000", Firmware: "OpenWrt 25.12.5"},
		"192.168.2.48": {HTTPPort: 80},
	}
	var mu sync.Mutex
	probed := []string{}
	probeCandidateFn = func(ip string) RouterInfo {
		mu.Lock()
		probed = append(probed, ip)
		mu.Unlock()
		info, ok := answers[ip]
		if !ok {
			info = RouterInfo{Vendor: "unknown", Model: "unknown", Firmware: "unknown"}
		}
		info.IP = ip
		return info
	}

	res := scanNetwork()

	// The probes are FANNED OUT (see probeCandidates), so their completion order
	// is deliberately not deterministic. The contract is the SET: the candidate
	// list is route-derived-first and every candidate is probed EXACTLY once.
	wantProbed := []string{"10.47.41.1", "10.47.41.254", "192.168.1.1", "192.168.2.48"}
	counts := map[string]int{}
	for _, ip := range probed {
		counts[ip]++
	}
	if len(counts) != len(wantProbed) {
		t.Fatalf("probed %v, want exactly the candidate set %v", probed, wantProbed)
	}
	for _, ip := range wantProbed {
		if counts[ip] != 1 {
			t.Errorf("candidate %s was probed %d time(s), want exactly once (probed: %v)", ip, counts[ip], probed)
		}
	}
	// The route-derived evidence must still decide the candidate ORDER, even
	// though the probes complete concurrently.
	if got := candIPs(hostCandidates(hostNetwork{
		Gateways: []gatewayRoute{{IP: "10.47.41.1", Iface: "enp1s0"}},
		Addrs:    []localAddr{{Iface: "enp1s0", CIDR: "10.47.41.23/24"}},
		Common:   []string{"192.168.1.1"},
		ARP:      []arpEntry{{IP: "192.168.2.48", MAC: "74:bf:c0:ac:ae:b4", Iface: "wlan0"}},
	})); len(got) != len(wantProbed) || got[0] != "10.47.41.1" {
		t.Errorf("candidate order = %v, want the route-derived candidate first", got)
	}

	if len(res.Routers) != 2 {
		t.Fatalf("routers = %+v, want 2 (the router AND the unverified host)", res.Routers)
	}
	if res.Routers[0].IP != "10.47.41.1" || !res.Routers[0].Identified {
		t.Errorf("first router = %+v, want the identified 10.47.41.1 first", res.Routers[0])
	}
	if res.Routers[1].IP != "192.168.2.48" || res.Routers[1].Identified {
		t.Errorf("second router = %+v, want the unverified 192.168.2.48 kept and marked", res.Routers[1])
	}
	if !strings.Contains(res.Routers[1].Note, "unverified host on your LAN — answers :80") {
		t.Errorf("unverified host note = %q", res.Routers[1].Note)
	}
	if res.Routers[0].MAC != "" {
		t.Errorf("10.47.41.1 MAC = %q, want empty (no ARP entry)", res.Routers[0].MAC)
	}
	if res.Routers[1].MAC != "74:bf:c0:ac:ae:b4" {
		t.Errorf("ARP MAC was not attached to the found host: %q", res.Routers[1].MAC)
	}
	if res.Routers[0].Source != sourceGateway {
		t.Errorf("router source = %q, want %q", res.Routers[0].Source, sourceGateway)
	}
	for _, p := range res.Probes {
		if p.IP == "10.47.41.1" && (p.Source != sourceGateway || p.Detail == "") {
			t.Errorf("probe %+v lost its provenance", p)
		}
	}
	if res.Diagnostics != nil {
		t.Errorf("diagnostics must be nil when a router was found, got %+v", res.Diagnostics)
	}
}

// TestScanNetworkDiagnosticsWhenNothingAnswers pins the failure path
// end-to-end: zero routers ⇒ a structured diagnostic, not one sentence.
func TestScanNetworkDiagnosticsWhenNothingAnswers(t *testing.T) {
	origNet, origProbe, origIfaces := collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn
	defer func() {
		collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn = origNet, origProbe, origIfaces
	}()

	collectHostNetworkFn = func() hostNetwork {
		return hostNetwork{
			Gateways: []gatewayRoute{{IP: "192.168.2.1", Iface: "wlp58s0"}},
			Addrs:    []localAddr{{Iface: "wlp58s0", CIDR: "192.168.2.33/24"}},
			Common:   []string{"192.168.1.1"},
		}
	}
	collectInterfaceStatesFn = func() []ifaceState {
		return []ifaceState{{Name: "wlp58s0", Up: true, Carrier: "1", IPv4: []string{"192.168.2.33/24"}}}
	}
	probeCandidateFn = func(ip string) RouterInfo {
		return RouterInfo{IP: ip, Vendor: "unknown", Model: "unknown", Firmware: "unknown"}
	}

	res := scanNetwork()
	if res.Routers == nil {
		t.Error("Routers must be an empty slice, not nil (JSON null would break the UI list)")
	}
	if len(res.Routers) != 0 {
		t.Fatalf("routers = %+v, want none", res.Routers)
	}
	if res.Diagnostics == nil {
		t.Fatal("zero routers must produce a diagnostic block")
	}
	for _, want := range []string{"wlp58s0", "192.168.2.1", sourceGateway, "install-and-test.sh"} {
		if !strings.Contains(res.Diagnostics.Text, want) {
			t.Errorf("diagnostic text does not mention %q:\n%s", want, res.Diagnostics.Text)
		}
	}
}

// TestHandleScanReturnsDiagnostics pins the wire contract the UI depends on.
func TestHandleScanReturnsDiagnostics(t *testing.T) {
	orig := scanNetworkFn
	defer func() { scanNetworkFn = orig }()

	// 1. A router found: routers only, no failure block.
	scanNetworkFn = func() ScanResult {
		return ScanResult{Routers: []RouterInfo{{IP: "192.168.1.1", Identified: true}}}
	}
	rr := httptest.NewRecorder()
	handleScan(rr, httptest.NewRequest(http.MethodGet, "/api/scan", nil))
	var okBody struct {
		Routers     []RouterInfo     `json:"routers"`
		Diagnostics *ScanDiagnostics `json:"diagnostics"`
		Failure     string           `json:"failure"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &okBody); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rr.Body.String())
	}
	if len(okBody.Routers) != 1 || okBody.Diagnostics != nil || okBody.Failure != "" {
		t.Errorf("successful scan body = %s, want one router and no failure block", rr.Body.String())
	}

	// 2. Nothing found: the diagnostic block AND its rendered text travel.
	scanNetworkFn = func() ScanResult {
		d := ScanDiagnostics{Summary: "nothing answered", Text: "FULL FAILURE BLOCK"}
		return ScanResult{Routers: []RouterInfo{}, Probes: []candidateProbe{{IP: "192.168.1.1"}}, Diagnostics: &d}
	}
	rr = httptest.NewRecorder()
	handleScan(rr, httptest.NewRequest(http.MethodGet, "/api/scan", nil))
	var failBody struct {
		Routers     []RouterInfo     `json:"routers"`
		Diagnostics *ScanDiagnostics `json:"diagnostics"`
		Failure     string           `json:"failure"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &failBody); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rr.Body.String())
	}
	if failBody.Diagnostics == nil || failBody.Diagnostics.Summary != "nothing answered" {
		t.Errorf("failed scan body = %s, want the structured diagnostics", rr.Body.String())
	}
	if failBody.Failure != "FULL FAILURE BLOCK" {
		t.Errorf("failure = %q, want the rendered block", failBody.Failure)
	}
	if failBody.Routers == nil {
		t.Error("routers must serialise as [] and not null")
	}
}

// TestHandleIdentifyLabelsAManualAddress pins the manual-address path: the
// operator types an address, the wizard identifies it and labels it honestly —
// and never calls a stranger's machine a router.
func TestHandleIdentifyLabelsAManualAddress(t *testing.T) {
	orig := probeRouterWithPasswordFn
	defer func() { probeRouterWithPasswordFn = orig }()
	probeRouterWithPasswordFn = func(ip, password string) RouterInfo {
		return RouterInfo{IP: ip, SSH: true, Vendor: "unknown", Model: "unknown", Firmware: "unknown"}
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/identify", strings.NewReader(`{"ip":"192.168.2.34"}`))
	handleIdentify(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var info RouterInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rr.Body.String())
	}
	if info.IP != "192.168.2.34" {
		t.Errorf("ip = %q, want the typed address back", info.IP)
	}
	if info.Source != sourceManual {
		t.Errorf("source = %q, want %q (the operator typed it)", info.Source, sourceManual)
	}
	if info.Identified {
		t.Error("a device that only answers :22 must not be called identified")
	}
	if !strings.Contains(info.Note, "unverified host on your LAN — answers :22") {
		t.Errorf("note = %q, want the unverified-host label", info.Note)
	}
}
