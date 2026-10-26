package app

// Guards for the SCAN-FAILURE surface: the block an operator sees when discovery
// finds no router, which is the most common failure on a fresh router.
//
// i18n_diag.go translates it. These tests are what stop it going back to
// English, and they are deliberately of two different kinds:
//
//   - TestScanFailureProseIsInTheCatalogue is STRICT. Every sentence the block
//     can show is authored by us, so every one must be reachable through the
//     table in i18n_diag.go and resolved from the catalogue. No allowlist: "this
//     sentence only exists in English" is not a state this surface may be in.
//     It works by BUILDING the diagnostics and checking each produced string,
//     so it fails on a sentence that was reworded in discover.go without the
//     table being updated — the exact way this surface silently reverted to
//     English in the first place.
//
//   - TestScanFailureLeavesRuntimeValuesAlone is about what must NOT change. A
//     translation that ate an interface name, an address or a port list would
//     hide the evidence the operator needs to fix the cable.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// diagFixtureForI18n builds the failure block with every distinct string class
// present: an interface with no link, one administratively down, one with no
// IPv4, a healthy one, a virtual one, a tunnel-owned default route, and probes
// covering every evidence class and every classifyProbed outcome.
func diagFixtureForI18n() *ScanDiagnostics {
	ifaces := []ifaceState{
		{Name: "wlp58s0", Up: true, Carrier: "1", IPv4: []string{"192.168.2.33/24"}},
		{Name: "enp0s31f6", Up: true, Carrier: "0"},
		{Name: "wwan0", Up: false, Carrier: "1"},
		{Name: "eth9", Up: true, Carrier: "1"},
		{Name: "docker0", Up: true, Carrier: "1", Virtual: true},
	}
	gateways := []gatewayRoute{{IP: "10.8.8.1", Iface: "wg0"}}
	probes := []candidateProbe{
		{IP: "192.168.2.1", Source: sourceGateway, SSH: true, HTTPPort: 80, Note: "unverified host on your LAN — answers :22 and :80"},
		{IP: "192.168.2.254", Source: sourceSubnet},
		{IP: "192.168.1.1", Source: sourceCommon},
		{IP: "192.168.2.48", Source: sourceARP, Demoted: true},
		{IP: "10.0.0.9", Source: sourceManual, Identified: true, Note: "OpenWrt router (identified)"},
		{IP: "10.0.0.10", Source: sourceManual, Note: "did not answer"},
	}
	d := buildScanDiagnostics(ifaces, gateways, probes)
	return &d
}

// TestScanFailureProseIsInTheCatalogue is the strict half: every string the
// block can display must be produced by a catalogue key.
func TestScanFailureProseIsInTheCatalogue(t *testing.T) {
	d := diagFixtureForI18n()

	// Every key the table names must exist, or the surface renders that key's
	// own name to the operator.
	for english, key := range diagFixedKeys {
		if !i18n.Has(key) {
			t.Errorf("diagFixedKeys maps %q to %q, which no catalogue defines — the operator would see the key name", english, key)
		}
		// The table must be honest about the text it claims to translate: the
		// catalogue's English value has to BE that sentence, or the mapping has
		// drifted and the wrong string is being replaced.
		if got := i18n.T(i18n.DefaultLanguage, key); got != english {
			t.Errorf("catalogue %q = %q, but diagFixedKeys claims it translates %q; the mapping has drifted", key, got, english)
		}
	}

	// Every sentence actually PRODUCED must be either catalogued or a runtime
	// value (an address, an interface name, a count). Splitting the fields the
	// page renders from the ones it does not is the whole point: `Text` is the
	// log block and is deliberately English (see i18n_diag.go).
	produced := []string{d.Summary, d.ManualHint}
	for _, i := range d.Interfaces {
		produced = append(produced, i.Problem)
	}
	for _, p := range d.Probes {
		produced = append(produced, p.Source, p.Note)
	}
	produced = append(produced, d.Sources...)
	produced = append(produced, d.Remediation...)

	for _, s := range produced {
		if s == "" {
			continue
		}
		if _, ok := i18n.KeyForText(s); ok {
			continue
		}
		if isRuntimeOnly(s) {
			continue
		}
		if _, ok := diagFixedKeys[s]; ok {
			t.Errorf("%q is in diagFixedKeys but the catalogue has no key with exactly that English text, so KeyForText does not resolve it: add or fix the value in locales/en.json", s)
			continue
		}
		t.Errorf("the failure block produces %q, which is neither in the catalogue nor a pure runtime value.\n"+
			"Every sentence this block shows must be translatable: add the English text to locales/en.json\n"+
			"and map it in diagFixedKeys (i18n_diag.go).", s)
	}
}

// isRuntimeOnly reports whether s carries no translatable prose: it is a bare
// address/identifier, or a sentence-shaped value whose prose part is already
// covered by a parameterised catalogue key (the summary count, a source line,
// the tunnel note, and the two composed probe notes).
func isRuntimeOnly(s string) bool {
	// A bare address, interface name or port list has no prose to translate.
	if !strings.Contains(s, " ") {
		return true
	}
	// Parameterised families. Each is a catalogue key with a {placeholder}, so an
	// exact-text lookup cannot match the produced value — the sentence is
	// translatable but its rendered form is not a catalogue VALUE. Matching the
	// shape here (rather than exempting these fields wholesale) keeps a NEW
	// unkeyed sentence in one of them a failure.
	switch {
	case strings.HasPrefix(s, scanTunnelNotePrefix):
		return true
	case strings.HasSuffix(s, " router (identified)"):
		return true
	case strings.HasPrefix(s, "unverified host on your LAN — answers "):
		return true
	}
	if _, _, ok := splitSourceSummary(s); ok {
		return true
	}
	if _, ok := summaryCount(s); ok {
		return true
	}
	return false
}

// summaryCount is defined in i18n_diag.go: the localizer needs the same shape
// test, so the recogniser lives with the code that depends on it.

// TestScanFailureLeavesRuntimeValuesAlone is the other direction: the evidence
// the operator needs must survive translation verbatim.
func TestScanFailureLeavesRuntimeValuesAlone(t *testing.T) {
	d := diagFixtureForI18n()
	r := indexRequest(t, "de-DE,de;q=0.9", nil)
	got := localizeDiagnostics(r, d)

	// The block must actually BE translated — otherwise the assertions below
	// would pass on the English original and prove nothing.
	if got.Summary == d.Summary {
		t.Fatalf("the summary was not translated for a German request: %q", got.Summary)
	}
	if got.ManualHint == d.ManualHint {
		t.Errorf("the manual hint was not translated: %q", got.ManualHint)
	}

	// Counts, class labels' VALUES, interface names, addresses and port lists
	// are data. Check each by looking for the value the fixture put in.
	joined := strings.Join(append(append([]string{got.Summary, got.ManualHint}, got.Remediation...), got.Sources...), "\n")
	for _, must := range []string{
		"6",        // the probe count
		"wg0",      // the tunnel interface
		"10.8.8.1", // its address
	} {
		if !strings.Contains(joined, must) {
			t.Errorf("the translated block lost the runtime value %q:\n%s", must, joined)
		}
	}

	// Probe lines keep their IPs and their raw answers.
	for _, p := range got.Probes {
		if p.IP == "" {
			t.Error("a probe lost its address")
		}
		if p.Source == "" {
			t.Error("a probe lost its source class")
		}
	}
	for _, want := range []string{":22 and :80", "OpenWrt"} {
		found := false
		for _, p := range got.Probes {
			if strings.Contains(p.Note, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no translated probe note carries %q; the composed note lost its payload", want)
		}
	}

	// The copy must not have been written back into the scan's own value: the
	// next requester in another language would otherwise inherit this one's.
	if d.Summary == got.Summary {
		t.Error("localizeDiagnostics mutated the caller's diagnostics value")
	}
	if d.Remediation[0] != "1. Use the LAN port: on the GL-MT3000 the 2.5 GbE port is WAN and the 1 GbE port is LAN — plugged into WAN you get no DHCP lease and no route." {
		t.Errorf("localizeDiagnostics wrote the translation back into the caller's remediation list: %q", d.Remediation[0])
	}
	// enp0s31f6 (carrier=0) is index 1; index 0 is the healthy interface, whose
	// Problem is "". Assert on the one that HAS a problem, or the check would
	// pass against an empty string.
	if d.Interfaces[1].Problem != "no link (carrier=0): nothing is attached to this port" {
		t.Errorf("localizeDiagnostics wrote the translation back into the caller's interface list: %q", d.Interfaces[1].Problem)
	}
	if d.Probes[0].Source != sourceGateway {
		t.Errorf("localizeDiagnostics wrote the translation back into the caller's probe list: %q", d.Probes[0].Source)
	}
}

// TestScanFailureRendersInTheOperatorsLanguage is the end-to-end assertion on
// the real handler: the German request must get German, the Arabic one Arabic,
// and an unknown language must fall back to English rather than to a key name.
func TestScanFailureRendersInTheOperatorsLanguage(t *testing.T) {
	// Pin the scan so the test does not depend on the host's interfaces.
	orig := scanNetworkFn
	scanNetworkFn = func() ScanResult {
		return ScanResult{Diagnostics: diagFixtureForI18n()}
	}
	defer func() { scanNetworkFn = orig }()

	for _, tc := range []struct {
		header string
		lang   string
		// mustContain: a fragment of the translation, so an English body cannot
		// pass by accident.
		mustContain string
	}{
		{"de-DE,de;q=0.9", "de", "Adresse"},
		{"ar", "ar", "عنوان"},
		{"en-US,en;q=0.9", "en", "address"},
	} {
		w := httptest.NewRecorder()
		r := indexRequest(t, tc.header, nil)
		handleScan(w, r)

		body := w.Body.String()
		for _, want := range []string{tc.mustContain, "192.168.2.1"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s request: /api/scan body is missing %q:\n%s", tc.lang, want, body[:min(600, len(body))])
			}
		}
		// The page renders these fields, so a raw key name here is a visible bug.
		if strings.Contains(body, "scan.fail.") {
			t.Errorf("%s request: the scan body carries raw catalogue KEY NAMES, which the page would render literally:\n%s", tc.lang, body[:min(600, len(body))])
		}
	}
}

// TestEveryScanFailureKeyIsReachable stops a translation nobody can ever read:
// a scan.* key that no field of the block can emit, which is what a rename
// applied to one side only looks like. The page's own fixed keys are excluded
// because index.html emits those.
func TestEveryScanFailureKeyIsReachable(t *testing.T) {
	page := string(indexHTML)
	d := diagFixtureForI18n()
	produced := map[string]bool{}
	for _, key := range []string{"scan.fail.summary", "scan.fail.manualHint", "scan.fail.sourceLine", "scan.fail.tunnelNote",
		"scan.note.identified", "scan.note.noAnswer", "scan.note.vendorIdentified", "scan.note.unverified"} {
		produced[key] = true
	}
	for _, key := range diagFixedKeys {
		produced[key] = true
	}

	for _, key := range i18n.Keys() {
		if !strings.HasPrefix(key, "scan.") {
			continue
		}
		if produced[key] {
			continue
		}
		// Not produced by the Go side: it must be emitted by the page, or it is
		// dead.
		if strings.Contains(page, `"`+key+`"`) || strings.Contains(page, `'`+key+`'`) {
			continue
		}
		t.Errorf("catalogue key %q is never produced by the failure block and never named in index.html; nothing can render it", key)
	}

	// And the reverse: every key the Go side produces must exist.
	for key := range produced {
		if !i18n.Has(key) {
			t.Errorf("the failure block emits %q, which no catalogue defines", key)
		}
	}

	// The fixture must exercise the block, or this whole file proves nothing.
	if len(d.Probes) == 0 || len(d.Remediation) < 5 || d.TunnelIface == "" {
		t.Fatalf("the fixture no longer covers the block (probes=%d remediation=%d tunnel=%q)", len(d.Probes), len(d.Remediation), d.TunnelIface)
	}
}
