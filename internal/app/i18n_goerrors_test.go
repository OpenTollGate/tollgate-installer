package app

// Guards for the Go-side error surface.
//
// The handlers write an English sentence into a JSON `error` field, which the
// page renders verbatim (showError(data.error)). That makes those literals a
// user-facing surface like any other — a German operator whose deploy is
// rejected saw "IP required" in an otherwise German wizard, which is exactly the
// leak this card is about.
//
// writeError translates them by looking the ENGLISH SENTENCE up in the
// catalogue (i18n.KeyForText), so the literal in Go IS the contract. These tests
// fail when that contract drifts: a rewritten sentence that no longer matches its
// catalogue entry would silently fall back to English forever, and an English
// page would look perfectly fine.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenTollGate/tollgate-installer/internal/i18n"
)

// TestEveryHandledErrorStringIsInTheCatalogue walks the real call sites and
// requires each literal message to resolve to a catalogue key.
func TestEveryHandledErrorStringIsInTheCatalogue(t *testing.T) {
	lits := goErrorLiterals(t)
	if len(lits) == 0 {
		t.Fatal("found no literal messages passed to writeError/writeErrorExtra — this guard is not scanning anything")
	}
	for msg, pos := range lits {
		if _, ok := i18n.KeyForText(msg); !ok {
			t.Errorf("%s: the error string %q has no catalogue entry, so a non-English wizard renders it in English.\n"+
				"Add a key for it to internal/i18n/locales/en.json (the key's value must equal this string exactly).",
				pos, msg)
		}
	}
	t.Logf("checked %d literal error messages", len(lits))
}

// goErrorLiterals returns every literal string passed as the MESSAGE to
// writeError/writeErrorExtra in the non-test sources, keyed by text, with
// positions for diagnostics.
//
// It parses the AST rather than grepping, because a regex over source cannot tell
// a user-facing message from a variable name or a comment, and the point of this
// guard is to be trustworthy at 40+ call sites.
//
// A COMPUTED message (err.Error(), a variable, a concatenation) is deliberately
// not returned: it is a relayed diagnostic carrying upstream detail, and
// translating it would replace that detail with a guess. localizeMessage leaves
// those verbatim, so there is nothing to check.
func goErrorLiterals(t *testing.T) map[string]token.Position {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("cannot parse the app package: %v", err)
	}

	out := map[string]token.Position{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				fn, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				// The message argument differs: writeError carries the request.
				//   writeError(w, r, code, msg)      -> 3
				//   writeErrorExtra(w, code, msg, x) -> 2
				idx := -1
				switch fn.Name {
				case "writeError":
					idx = 3
				case "writeErrorExtra":
					idx = 2
				default:
					return true
				}
				if len(call.Args) <= idx {
					return true
				}
				lit, ok := call.Args[idx].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				msg, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				out[msg] = fset.Position(lit.Pos())
				return true
			})
		}
	}
	return out
}

// goErrorLiteralKeys returns the catalogue keys those literals resolve to. It is
// the reference TestEveryCatalogueKeyIsRendered needs for the error.* family:
// those keys are never named in index.html, because the server resolves them from
// the sentence a handler wrote.
func goErrorLiteralKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for msg := range goErrorLiterals(t) {
		if key, ok := i18n.KeyForText(msg); ok {
			out[key] = true
		}
	}
	return out
}

// TestErrorStringsActuallyTranslate is the counterpart no AST walk can give:
// that a resolved key renders in the target language rather than echoing the
// English text back. Without it, KeyForText could return a key whose German
// value is still the English sentence and every other guard would pass.
func TestErrorStringsActuallyTranslate(t *testing.T) {
	for _, tc := range []struct{ english, lang, want string }{
		{"IP required", "de", "IP-Adresse erforderlich"},
		{"IP required", "ar", "عنوان IP مطلوب"},
		{"invalid JSON", "de", "Ungültiges JSON"},
		{"could not connect to router", "de", "Verbindung zum Router nicht möglich"},
	} {
		key, ok := i18n.KeyForText(tc.english)
		if !ok {
			t.Errorf("KeyForText(%q) found no key", tc.english)
			continue
		}
		if got := i18n.T(tc.lang, key); got != tc.want {
			t.Errorf("T(%s, %s) = %q, want %q", tc.lang, key, got, tc.want)
		}
		if got := i18n.T("en", key); got != tc.english {
			t.Errorf("the English value of %s is %q, want the Go literal %q — they are the contract", key, got, tc.english)
		}
	}
}

// TestLocalizeMessageLeavesDiagnosticsAlone pins the other half of the rule: a
// message we merely RELAY must survive unchanged. Translating a wrapped upstream
// error would drop the detail the operator needs to fix the router, and
// returning an empty string would blank the banner.
func TestLocalizeMessageLeavesDiagnosticsAlone(t *testing.T) {
	r := indexRequest(t, "de-DE,de;q=0.9", nil)

	if got := localizeMessage(r, "IP required"); got != "IP-Adresse erforderlich" {
		t.Errorf("our own message was not translated: %q", got)
	}

	for _, relayed := range []string{
		"failed to download OpenWrt sha256sums: dial tcp 1.2.3.4:443: connect: connection refused",
		"/etc/dropbear/dropbear_ed25519_host_key.pub",
		"ssh: handshake failed: knownhosts: key mismatch",
	} {
		if got := localizeMessage(r, relayed); got != relayed {
			t.Errorf("a relayed diagnostic was altered:\n  in:  %q\n  out: %q", relayed, got)
		}
	}

	// A nil request (a handler reached through a path with no request) must not
	// panic: it degrades to the English text.
	if got := localizeMessage(nil, "IP required"); got != "IP required" {
		t.Errorf("localizeMessage(nil, …) = %q, want the message unchanged", got)
	}
}
