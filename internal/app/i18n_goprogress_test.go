package app

// Guards for the Go-side PROGRESS surfaces: the sentences a job stores about
// itself, which handleStatus hands to the page.
//
// The JSON error bodies are covered by i18n_goerrors_test.go (writeError). This
// file covers the other half of the same problem: `Step.Detail` and `job.Error`
// are rendered by pollStatus() (a step's detail under the step, job.error into
// the failure banner), and both are authored in Go. A German operator whose
// flash failed on the board check saw an otherwise German wizard with
// "Router is not running OpenWrt firmware" across the failure banner — the same
// leak, from a different struct field.
//
// # Why a baseline instead of a hard requirement
//
// Most of these strings are DIAGNOSTICS, not prose: they carry a MAC, an
// address, a fingerprint, a raw command's output, or a runtime count
// ("password set (generated)", "API healthy on :2121"). Translating them would
// mean translating the payload, and a translated half-sentence with a raw tail
// is worse for the operator than the English sentence plus the detail.
//
// So the rule is a RATCHET rather than a blanket ban:
//   - a literal that HAS a catalogue entry is translated (handleStatus resolves
//     it through localizeMessage, the same relay rule as writeError);
//   - a literal that does not must be listed in i18n_gostrings_allow.txt with
//     the class it belongs to;
//   - a NEW literal that is neither fails the build, so this surface cannot
//     grow in English by accident — which is exactly how it grew before;
//   - a baseline entry that is now catalogued, or that no longer exists, also
//     fails: the list may only shrink, so it cannot become a graveyard of
//     exemptions nobody re-reads.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

const goStringBaselinePath = "i18n_gostrings_allow.txt"

// TestJobErrorsAreInTheCatalogue is the STRICT half. A job error is prose the
// operator is meant to read, so it must be translated — there is no allowlist
// here on purpose: "this sentence exists only in English" is not a state this
// surface is allowed to be in.
func TestJobErrorsAreInTheCatalogue(t *testing.T) {
	errs := goProgressLiterals(t, true)
	if len(errs) == 0 {
		t.Fatal("found no literal job errors — this guard is not scanning anything")
	}
	for msg, pos := range errs {
		if _, ok := i18n.KeyForText(msg); !ok {
			t.Errorf("%s: the job error %q is not in the catalogue, so the failure banner\n"+
				"renders it in English on a translated page. Add a key to\n"+
				"internal/i18n/locales/en.json whose value is EXACTLY this string.", pos, msg)
		}
	}
	t.Logf("checked %d literal job errors", len(errs))
}

// TestGoStepDetailsAreCataloguedOrBaselined is the RATCHET, for the status
// tokens that may stay English only when declared.
func TestGoStepDetailsAreCataloguedOrBaselined(t *testing.T) {
	details := goProgressLiterals(t, false)
	if len(details) == 0 {
		t.Fatal("found no literal step details — this guard is not scanning anything")
	}
	baseline := readGoStringBaseline(t)

	var newOnes []string
	for msg, pos := range details {
		if _, ok := i18n.KeyForText(msg); ok {
			continue
		}
		if baseline[msg] {
			continue
		}
		newOnes = append(newOnes, msg+"  ("+pos.String()+")")
	}
	if len(newOnes) > 0 {
		if os.Getenv("UPDATE_GOSTRINGS") != "" {
			// Regenerating is deliberate and auditable in the diff: the file is
			// committed and reviewed like code, so an exemption has an author.
			if err := writeGoStringBaseline(details); err != nil {
				t.Fatalf("write %s: %v", goStringBaselinePath, err)
			}
			t.Fatalf("rewrote %s with %d entries; review the diff and re-run without UPDATE_GOSTRINGS", goStringBaselinePath, len(details))
		}
		sort.Strings(newOnes)
		t.Errorf("%d step detail(s) are neither in the catalogue nor declared in %s.\n"+
			"Either add a key to internal/i18n/locales/en.json whose value is EXACTLY the literal\n"+
			"(it will then be translated by handleStatus), or add it to the allowlist as a\n"+
			"status token:\n  %s",
			len(newOnes), goStringBaselinePath, strings.Join(newOnes, "\n  "))
	}

	// The list may only shrink: an entry that is now translated, or that no
	// longer exists, must be removed rather than left as a standing exemption
	// for whatever text lands there next.
	var stale []string
	for msg := range baseline {
		if _, ok := i18n.KeyForText(msg); ok {
			stale = append(stale, msg+"  (in the catalogue now — remove it so it gets translated)")
			continue
		}
		if _, ok := details[msg]; !ok {
			stale = append(stale, msg+"  (no call site any more — remove it)")
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("%s has %d stale entr(ies); the allowlist may only shrink:\n  %s",
			goStringBaselinePath, len(stale), strings.Join(stale, "\n  "))
	}
}

// TestEveryJobErrorKeyIsReachable is the reverse direction: a jobError.* key no
// call site can emit is a translation nobody reads, and usually a rename applied
// to one side only.
func TestEveryJobErrorKeyIsReachable(t *testing.T) {
	keys := map[string]bool{}
	for msg := range goProgressLiterals(t, true) {
		if key, ok := i18n.KeyForText(msg); ok {
			keys[key] = true
		}
	}
	if len(keys) == 0 {
		t.Fatal("no job error resolved to a key — the scan or the catalogue is broken")
	}
	for _, key := range i18n.Keys() {
		if strings.HasPrefix(key, "jobError.") && !keys[key] {
			t.Errorf("catalogue key %q is a job error no call site can emit (renamed on one side?)", key)
		}
	}
}

// TestStatusTranslatesOwnedStringsAndRelaysDiagnostics proves the wiring on the
// response the page actually reads, not on a helper in isolation.
//
// Two things must both hold, and each has a different failure mode:
//   - a catatalogue AUTHORED string (a job error we wrote) comes back in the
//     request's language, or the failure banner stays English;
//   - a RELAYED diagnostic (a raw sysupgrade line) comes back untouched, or the
//     operator loses the detail they need to fix the router.
//
// It also pins the copy: the snapshot must not translate the job's own stored
// state, or the first German poll would pin German into a job a second reader
// (or a test, or a log export) then sees in the wrong language.
func TestStatusTranslatesOwnedStringsAndRelaysDiagnostics(t *testing.T) {
	const owned = "Router is not running OpenWrt firmware"
	const relayed = "sysupgrade: Reading from '/tmp/openwrt.bin' … Image check failed"

	if _, ok := i18n.KeyForText(owned); !ok {
		t.Fatalf("%q has no catalogue entry, so this test cannot prove the translation path; add that key to en.json (this is the German-operator case)", owned)
	}
	if _, ok := i18n.KeyForText(relayed); ok {
		t.Fatalf("%q is in the catalogue — it is supposed to be a relayed diagnostic, so this test cannot prove the pass-through", relayed)
	}

	jobID := "i18n-progress-" + t.Name()
	job := newJob("192.168.1.1")
	job.Status = "failed"
	job.Error = owned
	job.Steps[0].Status = "failed"
	job.Steps[0].Detail = relayed

	job.mu.Lock()
	storedDetail := job.Steps[0].Detail
	job.mu.Unlock()

	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()
	defer func() {
		jobsMutex.Lock()
		delete(jobs, jobID)
		jobsMutex.Unlock()
	}()

	r := indexRequest(t, "de-DE,de;q=0.9", nil)
	r.Method = http.MethodGet
	r.URL.Path = "/api/status/" + jobID
	w := httptest.NewRecorder()
	handleStatus(w, r)

	var got struct {
		Error string `json:"error"`
		Steps []Step `json:"steps"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode status: %v", err)
	}

	if got.Error == owned {
		t.Errorf("the job error came back as the English literal for a German request: %q", got.Error)
	}
	if got.Error != i18n.T("de", mustKey(t, owned)) {
		t.Errorf("job error = %q, want the German catalogue value %q", got.Error, i18n.T("de", mustKey(t, owned)))
	}
	if len(got.Steps) == 0 || got.Steps[0].Detail != relayed {
		t.Errorf("the relayed diagnostic was altered: %q", got.Steps[0].Detail)
	}

	// The stored job must be untouched: see the copy in handleStatus.
	job.mu.Lock()
	after := job.Steps[0].Detail
	afterErr := job.Error
	job.mu.Unlock()
	if after != storedDetail {
		t.Errorf("handleStatus rewrote the job's stored step detail (%q -> %q): the next request in another language would inherit this one's", storedDetail, after)
	}
	if afterErr != owned {
		t.Errorf("handleStatus rewrote the job's stored error (%q -> %q)", owned, afterErr)
	}
}

func goJobErrorLiteralKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for msg := range goProgressLiterals(t, true) {
		if key, ok := i18n.KeyForText(msg); ok {
			out[key] = true
		}
	}
	return out
}

func mustKey(t *testing.T, english string) string {
	t.Helper()
	key, ok := i18n.KeyForText(english)
	if !ok {
		t.Fatalf("no catalogue key for %q", english)
	}
	return key
}

// goProgressLiterals returns the literal strings a job stores for the page, in
// ONE of the two classes — they are not the same thing and must not be checked
// by the same rule:
//
//	jobErrors=true  → Job.Error: PROSE, the sentence the failure banner shows.
//	                  Required to be in the catalogue, with no allowlist.
//	jobErrors=false → Step.Detail: a short STATUS TOKEN under a step title that
//	                  is already translated. Most carry a runtime value, so they
//	                  are allowed to stay English when declared.
func goProgressLiterals(t *testing.T, jobErrors bool) map[string]token.Position {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("cannot parse the app package: %v", err)
	}

	out := map[string]token.Position{}
	add := func(e ast.Expr) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		msg, err := strconv.Unquote(lit.Value)
		if err != nil || msg == "" {
			return
		}
		out[msg] = fset.Position(lit.Pos())
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					name, ok := callName(node)
					if !ok {
						return true
					}
					// The argument positions differ per helper; a wrong index
					// would silently check the step NUMBER, so each is named.
					//   jobFail(job, step, stepDetail, jobErr)
					//   jobFailAfterRestore(job, client, sta, step, stepDetail, jobErr)
					//   setStep(i, status, detail)
					var idx []int
					switch name {
					case "jobFail":
						idx = []int{2, 3}
					case "jobFailAfterRestore":
						idx = []int{4, 5}
					case "setStep":
						idx = []int{2}
					default:
						return true
					}
					for _, i := range idx {
						if i >= len(node.Args) {
							continue
						}
						// Keep only the class asked for: arg 3/5 is the job
						// error, arg 2/4/2 is the step detail.
						isErr := i == 3 || i == 5
						if isErr != jobErrors {
							continue
						}
						add(node.Args[i])
					}
				case *ast.AssignStmt:
					if !jobErrors {
						return true
					}
					for i, lhs := range node.Lhs {
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok || sel.Sel == nil || sel.Sel.Name != "Error" {
							continue
						}
						if i < len(node.Rhs) {
							add(node.Rhs[i])
						}
					}
				}
				return true
			})
		}
	}
	return out
}

// callName returns the function name of a call, for both `f(...)` and
// `x.f(...)` forms, so a method call cannot slip past the scan.
func callName(call *ast.CallExpr) (string, bool) {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name, true
	case *ast.SelectorExpr:
		if fun.Sel != nil {
			return fun.Sel.Name, true
		}
	}
	return "", false
}

// readGoStringBaseline loads the declared status tokens. A `#` starts a comment,
// so an entry can carry the reason it is allowed to stay English next to it.
func readGoStringBaseline(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(goStringBaselinePath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}
		}
		t.Fatalf("read %s: %v", goStringBaselinePath, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out[trimmed] = true
	}
	return out
}

// goStringBaselineHeader is written at the top of a regenerated allowlist. The
// file is committed, so this text is where a future reader learns what the list
// means before deciding to add a line to it.
const goStringBaselineHeader = `# Step details that stay English, with the class of each.
#
# These are STATUS TOKENS shown under a step title that IS translated — two or
# three words, usually carrying a runtime value (a count, an address, a raw
# tool's output). Translating a token means translating its payload, and a
# half-translated token with a raw tail reads worse than the English one.
#
# They are NOT failure-banner sentences: those are prose the operator reads, and
# they must be in the catalogue (TestJobErrorsAreInTheCatalogue, no allowlist).
#
# This list may only SHRINK. Adding a line is a decision that a string is a
# status token and not prose; removing one is how a token graduates to a
# catalogue key. Regenerate with:
#   UPDATE_GOSTRINGS=1 go test ./internal/app/ -run TestGoStepDetails
#
# One string per line, no quoting, no trailing spaces.

`

// writeGoStringBaseline rewrites the allowlist in a stable, sorted order so a
// regeneration produces a readable diff rather than a reshuffle.
func writeGoStringBaseline(details map[string]token.Position) error {
	msgs := make([]string, 0, len(details))
	for msg := range details {
		if _, ok := i18n.KeyForText(msg); ok {
			continue
		}
		msgs = append(msgs, msg)
	}
	sort.Strings(msgs)

	var b strings.Builder
	b.WriteString(goStringBaselineHeader)
	for _, m := range msgs {
		b.WriteString(m)
		b.WriteString("\n")
	}
	return os.WriteFile(goStringBaselinePath, []byte(b.String()), 0o644)
}
