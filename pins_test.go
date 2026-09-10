package main

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// deployGoSrc embeds deploy.go so the registry test reads the exact source
// that compiles into this package, independent of the test working directory.
//
//go:embed deploy.go
var deployGoSrc string

// expectedPinnedURLConsts is the authoritative registry of pinned download
// URL constants declared in deploy.go. TestDeployGoPinRegistry enforces that
// deploy.go declares exactly this set — a new pin cannot be added without
// registering it here, and every registered pin is exercised live by
// TestPinnedURLsAreLive (add new pins to liveCheckPins too).
var expectedPinnedURLConsts = []string{
	"tollgatePkgURL",
	"tollgatePkgAPKURL",
}

// forbiddenDeployIdentifiers names pins/steps removed by SW4a (Aug 2026)
// because their URLs 404'd. They must not come back:
//
//   - tollgateNftEnforceURL / 20-nds-enforce.nft: never existed as a release
//     asset. The nft enforcement rules ship INSIDE the .ipk since
//     OpenTollGate/tollgate-module-basic-go PR #283 ("fix: NDS fw4/nftables
//     enforcement bridge", merged 2026-07-24) — verified by extracting
//     data.tar.gz from tollgate-wrt_main.56.b528e1d: it contains
//     ./etc/nftables.d/20-nds-enforce.nft and ./etc/nftables.d/30-backend-firewall.nft.
//     The separate overlay download in deploy step 4 was redundant and broken.
//
//   - tollgateOSURL: dead constant (declared but never referenced) whose
//     releases.tollgate.me URL returned 404.
var forbiddenDeployIdentifiers = []string{
	"tollgateNftEnforceURL",
	"tollgateOSURL",
	"20-nds-enforce.nft",
}

// wantTollgatePkgURL is the exact release asset pinned by the wizard.
// The v0.6.0-alpha1 release of FreedomTechFeed/packages ships the
// tollgate-wrt package for the aarch64_cortex-a53 target (feed-primary).
const wantTollgatePkgURL = "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.ipk"

// TestTollgatePkgURLPinsExistingAsset pins the package download URL to the
// exact asset that exists on the v0.6.1-post-merge release. Any intentional
// repin (e.g. switching to an upstream OpenTollGate release) must update this
// test in the same commit — that is what makes the pin auditable.
func TestTollgatePkgURLPinsExistingAsset(t *testing.T) {
	if tollgatePkgURL != wantTollgatePkgURL {
		t.Errorf("tollgatePkgURL =\n  %q\nwant\n  %q", tollgatePkgURL, wantTollgatePkgURL)
	}
}

// liveCheckPins is the map of every pinned URL that TestPinnedURLsAreLive
// exercises with a real HTTP request. TestDeployGoPinRegistry asserts its key
// set equals expectedPinnedURLConsts, so a pin cannot be registered without
// also being live-checked (and vice versa).
var liveCheckPins = map[string]string{
	"tollgatePkgURL":    tollgatePkgURL,
	"tollgatePkgAPKURL": tollgatePkgAPKURL,
}

// parsePinnedURLConsts parses deploy.go (real Go syntax via go/ast, not text
// matching) and returns:
//
//   - pins: name → value for every string-literal constant whose name ends in
//     "URL" and whose value starts with "https://" — the pinned-download-URL
//     naming convention of this file. A pin named differently or served over
//     plain http:// would escape the registry; keep the convention.
//   - codeTokens: every identifier and string-literal value in the file.
//     Comments are NOT part of the AST (the file is parsed without
//     ParseComments), so documentation mentioning a removed pin cannot fail
//     the tripwire, while identifiers and URL string literals cannot hide.
func parsePinnedURLConsts(t *testing.T) (pins map[string]string, codeTokens []string) {
	t.Helper()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "deploy.go", deployGoSrc, 0)
	if err != nil {
		t.Fatalf("parsing deploy.go: %v", err)
	}

	pins = map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			name := vs.Names[0].Name
			if strings.HasSuffix(name, "URL") && strings.HasPrefix(value, "https://") {
				pins[name] = value
			}
		}
		return true
	})

	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			codeTokens = append(codeTokens, x.Name)
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					codeTokens = append(codeTokens, s)
				}
			}
		}
		return true
	})

	return pins, codeTokens
}

// TestDeployGoPinRegistry guards the set of pinned download URLs in deploy.go:
//
//  1. every pinned URL constant declared in deploy.go must be listed in
//     expectedPinnedURLConsts (so it gets a live HTTP 200 check),
//  2. the live-check map must cover exactly that registry (no drift), and
//  3. none of the removed 404 pins/steps (forbiddenDeployIdentifiers) may be
//     reintroduced — as identifiers OR inside string literals (e.g. URLs).
func TestDeployGoPinRegistry(t *testing.T) {
	declared, codeTokens := parsePinnedURLConsts(t)

	// 1. Declared URL constants must exactly match the registry.
	want := map[string]bool{}
	for _, name := range expectedPinnedURLConsts {
		want[name] = true
	}
	declaredSet := keySet(declared)
	if !reflect.DeepEqual(declaredSet, want) {
		t.Errorf("deploy.go declares URL constants %v, but registry expects %v\n"+
			"→ add/remove pins in BOTH expectedPinnedURLConsts and liveCheckPins",
			setNames(declaredSet), setNames(want))
	}

	// 2. The live-check map must cover exactly the registry (no drift).
	live := map[string]bool{}
	for name := range liveCheckPins {
		live[name] = true
	}
	if !reflect.DeepEqual(live, want) {
		t.Errorf("liveCheckPins covers %v, but registry expects %v\n"+
			"→ every registered pin must have a live HTTP check",
			setNames(live), setNames(want))
	}

	// 3. Removed 404 pins/steps must stay removed — scanning identifiers and
	//    string literals (comments excluded by construction).
	for _, tok := range codeTokens {
		for _, f := range forbiddenDeployIdentifiers {
			if strings.Contains(tok, f) {
				t.Errorf("deploy.go still references %q (in code: %q) — this pin/step was removed because it 404s (see SW4a)",
					f, truncate(tok, 100))
			}
		}
	}
}

// TestPinnedURLsAreLive is the regression test for SW4a: every URL the wizard
// pins must return HTTP 200 today. A pin that 404s means every fresh deploy
// breaks at the download step, exactly the outage this test guards against.
//
// It performs real HTTP requests (Range: bytes=0-0 so asset bodies are not
// downloaded). Run with -short to skip network access for offline hacking;
// the CI-parity command is plain `go test ./...`, which runs this live.
func TestPinnedURLsAreLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live URL check skipped: -short mode (CI-parity runs without -short)")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	for name, url := range liveCheckPins {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				t.Fatalf("building request for %s: %v", name, err)
			}
			req.Header.Set("Range", "bytes=0-0") // fetch 1 byte, not the asset
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s = %q: request failed: %v", name, url, err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				t.Errorf("%s = %q: got HTTP %d, want 200 — broken pin, fresh deploys will fail (SW4a regression)",
					name, url, resp.StatusCode)
			}
		})
	}
}

// keySet converts a name→value map to a name set for set comparison.
func keySet(m map[string]string) map[string]bool {
	set := map[string]bool{}
	for name := range m {
		set[name] = true
	}
	return set
}

// setNames returns a deterministic sorted name list for a set map (stable
// test failure output).
func setNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
