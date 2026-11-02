package app

// The scan-failure screen, in the operator's language.
//
// # Why this surface was still English
//
// The wizard's strings come from the catalogue, and the Go-authored surfaces
// that feed it (JSON error bodies, job.Error, Step.Detail) are translated in the
// handler — see writeError and handleStatus. The FAILURE BLOCK was not: its
// prose is authored in discover.go (the summary, the three per-interface problem
// sentences, the five class labels, the five remediation steps, the manual hint,
// and the note under each probed address), serialized as-is, and rendered by
// renderScanFailure() with textContent. So a German operator whose scan found
// nothing — the single most common failure on a fresh router — got an otherwise
// German wizard wrapped around an entirely English page, which is exactly the
// defect the catalogue exists to prevent.
//
// # Why it is translated HERE and not in the page
//
// Same argument as writeError: the handler knows the request's language and the
// page does not. The page also must not grow a second copy of the resolution
// rule (there is one, in i18n.Resolve). And the block carries RUNTIME values —
// interface names, addresses, raw probe answers — which must pass through
// untouched; translating here lets each string be decided on its own, so a
// half-translated line with a raw tail never happens.
//
// # What stays English, deliberately
//
// ScanDiagnostics.Text is the copy-pasteable block written into the deploy LOG
// (and shown by the headless path). A log is a diagnostic artifact that an
// operator pastes into a bug report or greps; translating it would make two
// operators' logs incomparable and would put translated prose next to raw
// command output. The page does not read it (renderScanFailure builds the block
// from the structured fields), so it is the one field left alone.
//
// Free-form relayed values stay English for the same reason they do everywhere
// else: a probe's Detail is another tool's output, and interface names, MACs and
// addresses are identifiers.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// diagFixedKeys maps a FIXED English sentence authored by discover.go to its
// catalogue key. Only strings with no runtime value belong here: a table entry
// can be compared to the catalogue verbatim, which is what
// TestScanFailureProseIsInTheCatalogue does, so the table cannot drift away from
// the text it claims to translate.
//
// Parameterised sentences (the summary count, the per-class source count, the
// tunnel note, and the two composed probe notes) are handled explicitly in
// localizeDiagnostics, because inserting sample values into them would make the
// table check meaningless.
var diagFixedKeys = map[string]string{
	// interfaceProblem — the reason a port cannot carry the router.
	"no link (carrier=0): nothing is attached to this port": "scan.fail.problem.noLink",
	"administratively down: the interface is disabled":      "scan.fail.problem.down",
	"link up, NO IPv4: no DHCP lease and no static address": "scan.fail.problem.noIPv4",

	// The five evidence classes. They appear in the source summary and in each
	// probe line ("[default-route gateway]"), so they are user-facing twice.
	sourceGateway: "scan.fail.source.gateway",
	sourceSubnet:  "scan.fail.source.subnet",
	sourceCommon:  "scan.fail.source.common",
	sourceARP:     "scan.fail.source.arp",
	sourceManual:  "scan.fail.source.manual",

	// classifyProbed's fixed answers. "did not answer" and the identified
	// sentence carry no value; the other two are parameterised (below).
	"TollGate/OpenWrt router (identified)": "scan.note.identified",
	"did not answer":                       "scan.note.noAnswer",

	// The remediation list, in order. Each is a full instruction the operator
	// acts on, so each gets its own key rather than being assembled from
	// fragments (a fragment-assembled sentence cannot be reordered by a
	// translator, and most of these languages reorder).
	"1. Use the LAN port: on the GL-MT3000 the 2.5 GbE port is WAN and the 1 GbE port is LAN — plugged into WAN you get no DHCP lease and no route.":                               "scan.fail.step1",
	"2. Give it time: after a flash or a reboot a router needs 60-120 s before it answers.":                                                                                        "scan.fail.step2",
	"3. Force a fresh lease on the wired interface: dhclient -v <interface>.":                                                                                                      "scan.fail.step3",
	"4. Vanilla OpenWrt answers on :22 only — there is no LuCI web UI on :80 or :8080 until it is installed.":                                                                      "scan.fail.step4",
	"5. Or skip discovery entirely: bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER-IP> '' <you@wallet.app>": "scan.fail.step5",

	// The manual-address hint under the failure block.
	"Enter your router's address in the box above — the wizard identifies it directly, bypassing discovery entirely.": "scan.fail.manualHint",
}

// tFill renders a catalogue key with {name} placeholders substituted. The
// page's t() does the same substitution client-side; this is its Go twin, so a
// translation may reorder the placeholders freely.
func tFill(lang, key string, vars map[string]string) string {
	s := i18n.T(lang, key)
	for name, val := range vars {
		s = strings.ReplaceAll(s, "{"+name+"}", val)
	}
	return s
}

// localizeDiagnostics returns a copy of d with every string WE authored replaced
// by its translation for this request. Runtime values (interface names,
// addresses, raw details, counts' digits) are copied through unchanged.
//
// It works on a COPY for the same reason handleStatus does: the diagnostics
// value belongs to the scan, not to the request. Writing a German sentence into
// it would serve German to the next requester, and would make the block's own
// log text depend on who happened to load the page first.
func localizeDiagnostics(r *http.Request, d *ScanDiagnostics) *ScanDiagnostics {
	if d == nil {
		return nil
	}
	lang, _ := pageLanguage(r)

	out := *d
	// The summary is normally "<n> address(es) were probed…", which is why it is
	// rebuilt with the count rather than looked up as a whole sentence. Anything
	// else (a summary another caller set) is translated by exact text if the
	// catalogue knows it, and otherwise relayed untouched — never replaced with
	// the count template, which would silently swallow a different sentence.
	if n, ok := summaryCount(d.Summary); ok {
		out.Summary = tFill(lang, "scan.fail.summary", map[string]string{"n": strconv.Itoa(n)})
	} else if key, ok := i18n.KeyForText(d.Summary); ok {
		out.Summary = i18n.T(lang, key)
	}
	out.ManualHint = tFill(lang, "scan.fail.manualHint", nil)

	out.Interfaces = make([]ifaceReport, len(d.Interfaces))
	for i, i2 := range d.Interfaces {
		if key, ok := diagFixedKeys[i2.Problem]; ok {
			i2.Problem = i18n.T(lang, key)
		}
		out.Interfaces[i] = i2
	}

	out.Probes = make([]candidateProbe, len(d.Probes))
	for i, p := range d.Probes {
		if key, ok := diagFixedKeys[p.Source]; ok {
			p.Source = i18n.T(lang, key)
		}
		p.Note = localizeProbeNote(lang, p.Note)
		out.Probes[i] = p
	}

	// The source summary is "<class>: <n> address(es)". Rebuilt from the class
	// label plus the count rather than translated as a whole, because the count
	// and the class are the only parts it has that vary.
	if d.Sources != nil {
		out.Sources = make([]string, 0, len(d.Sources))
		for _, s := range d.Sources {
			class, n, ok := splitSourceSummary(s)
			if !ok {
				out.Sources = append(out.Sources, s)
				continue
			}
			label := class
			if key, ok := diagFixedKeys[class]; ok {
				label = i18n.T(lang, key)
			}
			out.Sources = append(out.Sources, tFill(lang, "scan.fail.sourceLine", map[string]string{"class": label, "n": n}))
		}
	}

	// The remediation list is the five fixed steps, plus — when the default
	// route belongs to a tunnel — a note naming the interface that owns it. The
	// note's interface and address are runtime values, so it is rebuilt from the
	// fields remediationSteps recorded, not parsed back out of the string.
	if d.Remediation != nil {
		out.Remediation = make([]string, 0, len(d.Remediation))
		for _, s := range d.Remediation {
			if key, ok := diagFixedKeys[s]; ok {
				out.Remediation = append(out.Remediation, i18n.T(lang, key))
				continue
			}
			if d.TunnelIface != "" && strings.HasPrefix(s, scanTunnelNotePrefix) {
				out.Remediation = append(out.Remediation, tFill(lang, "scan.fail.tunnelNote",
					map[string]string{"iface": d.TunnelIface, "ip": d.TunnelVia}))
				continue
			}
			out.Remediation = append(out.Remediation, s)
		}
	}

	return &out
}

// scanTunnelNotePrefix is the fixed opening of the tunnel note, kept next to the
// builder that emits it so the two cannot drift silently:
// TestScanFailureProseIsInTheCatalogue pins the built sentence's prefix.
const scanTunnelNotePrefix = "Note: the default route is owned by the tunnel interface "

// localizeProbeNote translates the two COMPOSED probe notes, leaving anything
// else (a raw diagnostic) verbatim.
//
// classifyProbed builds these from a vendor name or a list of open ports, so
// they are parameterised sentences rather than table entries. They are worth the
// two keys: they sit under the address the operator is about to type in, and
// "unverified host on your LAN" is the sentence that stops them flashing the
// wrong box.
func localizeProbeNote(lang, note string) string {
	if key, ok := diagFixedKeys[note]; ok {
		return i18n.T(lang, key)
	}
	const identifiedSuffix = " router (identified)"
	if strings.HasSuffix(note, identifiedSuffix) {
		vendor := strings.TrimSuffix(note, identifiedSuffix)
		if vendor != "" {
			return tFill(lang, "scan.note.vendorIdentified", map[string]string{"vendor": vendor})
		}
	}
	const unverifiedPrefix = "unverified host on your LAN — answers "
	if strings.HasPrefix(note, unverifiedPrefix) {
		ports := strings.TrimPrefix(note, unverifiedPrefix)
		if ports != "" {
			return tFill(lang, "scan.note.unverified", map[string]string{"ports": ports})
		}
	}
	return note
}

// summaryCount recognises the rendered scan.fail.summary: "<n> address(es) were
// probed and answered nothing that identifies as a TollGate/OpenWrt router."
// The digits are the only part the catalogue does not carry, which is why the
// localizer rebuilds the sentence from the count instead of looking the whole
// string up.
func summaryCount(s string) (int, bool) {
	suffix := " address(es) were probed and answered nothing that identifies as a TollGate/OpenWrt router."
	if !strings.HasSuffix(s, suffix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, suffix))
	if err != nil {
		return 0, false
	}
	return n, true
}

// splitSourceSummary splits "<class>: <n> address(es)" into its class and count.
// It returns ok=false for anything that is not that exact shape, so a future
// source line with different wording passes through untranslated rather than
// being mangled.
func splitSourceSummary(s string) (class, n string, ok bool) {
	const tail = " address(es)"
	if !strings.HasSuffix(s, tail) {
		return "", "", false
	}
	body := strings.TrimSuffix(s, tail)
	i := strings.LastIndex(body, ": ")
	if i < 0 {
		return "", "", false
	}
	class = body[:i]
	n = body[i+2:]
	if class == "" || n == "" {
		return "", "", false
	}
	for _, c := range n {
		if c < '0' || c > '9' {
			return "", "", false
		}
	}
	return class, n, true
}
