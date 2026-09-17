package main

import (
	_ "embed"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// wantFeedReleaseTag is the feed release tag the wizard selects by default:
// the main-tip pre-release of tollgate-module-basic-go, whose feed build is
// pinned to upstream commit 373770a. Bumping the code's feedReleaseTagDefault
// without bumping this literal (and the per-arch names in TestFeedAssetURL)
// fails TestFeedReleaseIdentityIsPinned — that is what makes a repin auditable
// instead of a silent literal swap.
const wantFeedReleaseTag = "v0.6.0-alpha2-pre6"

// wantFeedPkgVersion is wantFeedReleaseTag in the feed's PKG_VERSION spelling:
// leading "v" dropped, hyphens turned into underscores (apk-tools 3.x rejects
// hyphens in versions). The installed package reports this as
// "0.6.0_alpha2_pre3-r1" — note it is NOT the string the binary reports about
// itself ("v0.6.0-alpha2-g373770a"; see docs/package-provenance.md).
const wantFeedPkgVersion = "0.6.0_alpha2_pre6"

// wantTollgatePkgURL is the exact release asset pinned by the wizard.
// The v0.6.0-alpha2-pre3 release of FreedomTechFeed/packages ships the
// tollgate-wrt package for the aarch64_cortex-a53 target (feed-primary).
// It is DERIVED from feedAssetURL so it can never drift from the generic
// URL builder — this test pins the derivation, not a literal.
const wantTollgatePkgURL = "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha2-pre6/tollgate-wrt_0.6.0_alpha2_pre6_aarch64_cortex-a53.ipk"

// feedReleasePublishedArches is the arch matrix the pinned feed release
// actually publishes: 7 tuples × {.ipk, .apk} = the 14 assets on
// v0.6.0-alpha2-pre3. This is a TEST FIXTURE describing one release, not a
// runtime allowlist — feedAssetURL stays generic on purpose, so an arch the
// feed adds later works without a code change. It is also deliberately NOT
// the set of tuples normalizeBareArch can emit: "aarch64_generic" is absent
// from the feed matrix, so a router reporting that tuple resolves to a URL
// that 404s rather than silently installing a different arch's package.
var feedReleasePublishedArches = []string{
	"aarch64_cortex-a53",
	"aarch64_cortex-a72",
	"arm_cortex-a7",
	"mips64_octeonplus",
	"mipsel_24kc",
	"mips_24kc",
	"x86_64",
}

// TestTollgatePkgURLPinsExistingAsset pins the package download URL to the
// exact asset that exists on the currently selected feed release. Any
// intentional repin (e.g. moving to a newer pre-release) must update this test
// in the same commit — that is what makes the pin auditable.
func TestTollgatePkgURLPinsExistingAsset(t *testing.T) {
	if tollgatePkgURL != wantTollgatePkgURL {
		t.Errorf("tollgatePkgURL =\n  %q\nwant\n  %q", tollgatePkgURL, wantTollgatePkgURL)
	}
	// The value must be derived from the generic builder, not a hardcoded
	// literal — otherwise the two sources of truth can drift.
	if tollgatePkgURL != feedAssetURL("aarch64_cortex-a53", ".ipk") {
		t.Errorf("tollgatePkgURL = %q, want feedAssetURL(aarch64_cortex-a53, .ipk) = %q",
			tollgatePkgURL, feedAssetURL("aarch64_cortex-a53", ".ipk"))
	}
	if tollgatePkgAPKURL != feedAssetURL("aarch64_cortex-a53", ".apk") {
		t.Errorf("tollgatePkgAPKURL = %q, want feedAssetURL(aarch64_cortex-a53, .apk) = %q",
			tollgatePkgAPKURL, feedAssetURL("aarch64_cortex-a53", ".apk"))
	}
}

// TestFeedReleaseIdentityIsPinned pins the selected release identity: the tag
// literal, the derived package version, and the URL prefix. The package
// version is re-derived two ways — through feedPkgVersionForTag and through an
// independent inline transformation — so a bump that changes the tag without
// changing the version spelling cannot pass.
func TestFeedReleaseIdentityIsPinned(t *testing.T) {
	if feedReleaseTag != wantFeedReleaseTag {
		t.Errorf("feedReleaseTag = %q, want %q (the tag the feed published for main tip)", feedReleaseTag, wantFeedReleaseTag)
	}
	if got := feedPkgVersion(); got != wantFeedPkgVersion {
		t.Errorf("feedPkgVersion() = %q, want %q (tag %q in PKG_VERSION form)", got, wantFeedPkgVersion, feedReleaseTag)
	}
	if got := feedPkgVersionForTag(feedReleaseTag); got != wantFeedPkgVersion {
		t.Errorf("feedPkgVersionForTag(%q) = %q, want %q", feedReleaseTag, got, wantFeedPkgVersion)
	}
	// Independent re-derivation: if feedPkgVersionForTag were ever changed to
	// something other than "strip v, hyphen → underscore", this disagrees.
	wantDerived := strings.ReplaceAll(strings.TrimPrefix(feedReleaseTag, "v"), "-", "_")
	if wantDerived != wantFeedPkgVersion {
		t.Errorf("independent derivation of %q = %q, want %q", feedReleaseTag, wantDerived, wantFeedPkgVersion)
	}
	wantPrefix := "https://github.com/" + feedRepoSlug + "/releases/download/" + wantFeedReleaseTag + "/"
	if got := feedReleaseURLPrefix(); got != wantPrefix {
		t.Errorf("feedReleaseURLPrefix() = %q, want %q", got, wantPrefix)
	}
}

// TestFeedAssetURLShapeMatchesPinnedRelease pins the SHAPE of the feed asset
// URL, not just one literal: the release tag and the package version are the
// same version in two spellings (tag keeps the hyphen, the package version uses
// underscores because apk-tools 3 rejects hyphens — see the feed's
// net/tollgate-wrt/Makefile). Bumping one without the other produces a
// well-formed-looking but wrong asset name that 404s on every deploy, so the
// relationship is asserted rather than trusted.
func TestFeedAssetURLShapeMatchesPinnedRelease(t *testing.T) {
	// The URL must be exactly release-prefix + tollgate-wrt_<version>_<arch><ext>.
	for _, tc := range []struct{ arch, ext string }{
		{"aarch64_cortex-a53", ".ipk"},
		{"aarch64_cortex-a53", ".apk"},
		{"mipsel_24kc", ".ipk"},
		{"mips_24kc", ".apk"},
		{"x86_64", ".ipk"},
	} {
		got := feedAssetURL(tc.arch, tc.ext)
		wantName := "tollgate-wrt_" + wantFeedPkgVersion + "_" + tc.arch + tc.ext
		want := "https://github.com/" + feedRepoSlug + "/releases/download/" + wantFeedReleaseTag + "/" + wantName
		if got != want {
			t.Errorf("feedAssetURL(%q, %q) =\n  %q\nwant\n  %q", tc.arch, tc.ext, got, want)
		}
		if !strings.HasSuffix(got, "_"+tc.arch+tc.ext) {
			t.Errorf("feedAssetURL(%q, %q) = %q: asset name must end with %q", tc.arch, tc.ext, got, "_"+tc.arch+tc.ext)
		}
		if strings.Contains(got, "__") {
			t.Errorf("feedAssetURL(%q, %q) = %q: contains an empty URL component", tc.arch, tc.ext, got)
		}
		if strings.Contains(got, "/"+wantFeedReleaseTag+"/"+wantFeedReleaseTag) {
			t.Errorf("feedAssetURL(%q, %q) = %q: duplicated tag", tc.arch, tc.ext, got)
		}
	}

	// The release prefix must be derived from the same identity, so the
	// builder and the pinned tag can never drift apart.
	if !strings.HasPrefix(feedReleaseURLPrefix(), "https://github.com/"+feedRepoSlug+"/releases/download/") {
		t.Errorf("feedReleaseURLPrefix() = %q: must be the feed repo's release download prefix", feedReleaseURLPrefix())
	}
	if !strings.HasSuffix(feedReleaseURLPrefix(), "/"+feedReleaseTag+"/") {
		t.Errorf("feedReleaseURLPrefix() = %q: must end with the pinned release tag %q", feedReleaseURLPrefix(), feedReleaseTag)
	}
}

// TestFeedPkgVersionForTag pins the tag → PKG_VERSION transformation itself,
// including the two spellings in play today (v0.6.0-alpha2-pre3 is the current
// feed release, v0.6.0-alpha1 the previous one) and the legacy v0.5.0 GitHub
// release, whose version carries no hyphen and no leading "v" once stripped.
func TestFeedPkgVersionForTag(t *testing.T) {
	for _, tc := range []struct{ tag, want string }{
		{"v0.6.0-alpha2-pre3", "0.6.0_alpha2_pre3"},
		{"v0.6.0-alpha1", "0.6.0_alpha1"},
		{"v0.5.0", "0.5.0"},
		{"v0.6.0-alpha2-pre", "0.6.0_alpha2_pre"},
		{" v0.6.0-alpha2-pre3\n", "0.6.0_alpha2_pre3"},
		{"", ""},
	} {
		if got := feedPkgVersionForTag(tc.tag); got != tc.want {
			t.Errorf("feedPkgVersionForTag(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

// TestResolveFeedReleaseTagPrecedence pins the env override: a plausible
// TOLLGATE_FEED_RELEASE_TAG wins, anything else falls back to the default. The
// reject cases matter because the value is interpolated straight into every
// arch's download URL — a typo or a shell fragment must not become a URL.
func TestResolveFeedReleaseTagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, env, want string
	}{
		{"unset falls back to default", "", feedReleaseTagDefault},
		{"whitespace only falls back to default", "   ", feedReleaseTagDefault},
		{"previous pre-release honoured", "v0.6.0-alpha1", "v0.6.0-alpha1"},
		{"newer pre-release honoured", "v0.6.0-alpha3-pre", "v0.6.0-alpha3-pre"},
		{"legacy v0.5.0 tag honoured", "v0.5.0", "v0.5.0"},
		{"surrounding whitespace trimmed", "  v0.6.0-alpha1\n", "v0.6.0-alpha1"},
		{"embedded space rejected", "v0.6.0 alpha1", feedReleaseTagDefault},
		{"shell fragment rejected", "v0.6.0;rm -rf /", feedReleaseTagDefault},
		{"absolute url rejected", "https://evil.example/x", feedReleaseTagDefault},
		{"path traversal rejected", "../../etc/passwd", feedReleaseTagDefault},
		{"leading dot rejected", ".hidden", feedReleaseTagDefault},
		{"newline injection rejected", "v0.6.0-alpha2-pre\nx", feedReleaseTagDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveFeedReleaseTag(func(string) string { return tc.env })
			if got != tc.want {
				t.Errorf("resolveFeedReleaseTag(env=%q) = %q, want %q", tc.env, got, tc.want)
			}
			if !feedReleaseTagRe.MatchString(got) {
				t.Errorf("resolveFeedReleaseTag(env=%q) = %q: not a plausible tag; would produce a malformed download URL", tc.env, got)
			}
		})
	}

	// A nil lookup (defensive) must not panic and must fall back.
	if got := resolveFeedReleaseTag(nil); got != feedReleaseTagDefault {
		t.Errorf("resolveFeedReleaseTag(nil) = %q, want %q", got, feedReleaseTagDefault)
	}
	// The real process environment must resolve to a plausible tag too.
	if got := resolveFeedReleaseTag(os.Getenv); !feedReleaseTagRe.MatchString(got) {
		t.Errorf("resolveFeedReleaseTag(os.Getenv) = %q: not a plausible tag", got)
	}
}

// TestFeedAssetURLHonoursTagOverride proves the override reaches the actual
// download URL (not just the resolver): with the tag switched back to the
// previous release, every derived URL must be the previous release's URL.
func TestFeedAssetURLHonoursTagOverride(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()

	feedReleaseTag = "v0.6.0-alpha1"
	want := "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_x86_64.apk"
	if got := feedAssetURL("x86_64", ".apk"); got != want {
		t.Errorf("feedAssetURL with overridden tag = %q, want %q", got, want)
	}
	if got := feedReleaseURLPrefix(); got != "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/" {
		t.Errorf("feedReleaseURLPrefix() with overridden tag = %q", got)
	}

	// Restoring the tag must restore the derived URLs to the pinned release.
	feedReleaseTag = orig
	if got := feedAssetURL("x86_64", ".apk"); got != "https://github.com/FreedomTechFeed/packages/releases/download/"+wantFeedReleaseTag+"/tollgate-wrt_"+wantFeedPkgVersion+"_x86_64.apk" {
		t.Errorf("feedAssetURL after restore = %q, want the pinned release's asset", got)
	}
}

// ─── Live feed-release checks ────────────────────────────────────────────
//
// The two tests below ask GitHub what the pinned release actually contains.
// They are skipped in -short mode (the unit-run mode) and skipped — loudly,
// never silently — if the anonymous GitHub API is rate-limited. They replace
// guessing about asset names with the release's own asset list.

type feedReleaseAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type feedRelease struct {
	TagName string             `json:"tag_name"`
	Assets  []feedReleaseAsset `json:"assets"`
}

var (
	feedReleaseOnce sync.Once
	feedReleaseData feedRelease
	feedReleaseCode int
	feedReleaseErr  error
)

// fetchPinnedFeedRelease does at most ONE GitHub API request per test binary,
// memoised through sync.Once, so the checks below cannot burn the anonymous
// rate limit once per test.
func fetchPinnedFeedRelease() (feedRelease, int, error) {
	feedReleaseOnce.Do(func() {
		url := "https://api.github.com/repos/" + feedRepoSlug + "/releases/tags/" + feedReleaseTag
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			feedReleaseErr = err
			return
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		// Optional token: the anonymous limit is 60 requests/hour per IP and
		// shared CI runners share it. Never required for the check to be valid.
		if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			feedReleaseErr = err
			return
		}
		defer resp.Body.Close()
		feedReleaseCode = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			return
		}
		feedReleaseErr = json.NewDecoder(resp.Body).Decode(&feedReleaseData)
	})
	return feedReleaseData, feedReleaseCode, feedReleaseErr
}

// requirePinnedFeedRelease returns the pinned release, failing the test if the
// selected tag does not exist on the feed — the exact failure mode of leaving
// a stale pin in place (the wizard downloads a 404 on every arch), and skipping
// (not passing) if GitHub refuses to answer.
func requirePinnedFeedRelease(t *testing.T) feedRelease {
	t.Helper()
	if testing.Short() {
		t.Skip("live GitHub release check skipped: -short mode")
	}
	rel, code, err := fetchPinnedFeedRelease()
	if err != nil {
		t.Fatalf("querying %s release %q: %v", feedRepoSlug, feedReleaseTag, err)
	}
	switch code {
	case http.StatusOK:
		return rel
	case http.StatusNotFound:
		t.Fatalf("STALE PIN: %s has no release tagged %q (HTTP 404) — every arch would download a 404. "+
			"Select a tag the feed actually published (feedReleaseTagDefault / TOLLGATE_FEED_RELEASE_TAG).",
			feedRepoSlug, feedReleaseTag)
	default:
		t.Skipf("GitHub API returned HTTP %d for %s release %q (rate limit or outage); cannot verify asset names here",
			code, feedRepoSlug, feedReleaseTag)
	}
	return rel
}

// TestPinnedFeedReleaseTagExists is the stale-pin guard: it fails the moment
// the selected feed release tag stops existing, which is what a hardcoded pin
// does when the feed moves on. It also asserts the API's own tag_name matches
// the tag we asked for, so a redirect or a renamed tag cannot pass silently.
func TestPinnedFeedReleaseTagExists(t *testing.T) {
	rel := requirePinnedFeedRelease(t)
	if rel.TagName != feedReleaseTag {
		t.Errorf("release tag_name = %q, want %q", rel.TagName, feedReleaseTag)
	}
	if len(rel.Assets) == 0 {
		t.Errorf("release %q exists but publishes 0 assets — nothing to install", feedReleaseTag)
	}
}

// TestFeedReleasePublishesEachDerivedAssetName is the byte-identical-name
// check: every asset name the code derives for every published arch must exist
// on the live release, and every tollgate-wrt asset the release publishes must
// be derivable by the code. Missing names mean a broken download; unexpected
// names mean the naming rule drifted and the tests are no longer describing
// reality.
func TestFeedReleasePublishesEachDerivedAssetName(t *testing.T) {
	rel := requirePinnedFeedRelease(t)

	published := make(map[string]int64, len(rel.Assets))
	for _, a := range rel.Assets {
		published[a.Name] = a.Size
	}

	wantNames := make(map[string]bool, 2*len(feedReleasePublishedArches))
	for _, arch := range feedReleasePublishedArches {
		for _, ext := range []string{".ipk", ".apk"} {
			name := path.Base(feedAssetURL(arch, ext))
			wantNames[name] = true
			size, ok := published[name]
			if !ok {
				t.Errorf("derived asset %q (arch %q, %s) is NOT published on release %q — that arch cannot install",
					name, arch, ext, feedReleaseTag)
				continue
			}
			if size <= 0 {
				t.Errorf("published asset %q has size %d", name, size)
			}
		}
	}

	// Reverse direction: no tollgate-wrt asset may exist that the code cannot
	// derive. Catches a naming-rule change on the feed side.
	publishedPkgs := 0
	for name := range published {
		if !strings.HasPrefix(name, "tollgate-wrt_") {
			continue
		}
		publishedPkgs++
		if !wantNames[name] {
			t.Errorf("release %q publishes %q, which feedAssetURL cannot derive — the naming rule has drifted", feedReleaseTag, name)
		}
	}
	if publishedPkgs != len(feedReleasePublishedArches)*2 {
		t.Errorf("release %q publishes %d tollgate-wrt assets, want %d (feedReleasePublishedArches × {.ipk,.apk}); "+
			"the arch matrix changed — update feedReleasePublishedArches deliberately",
			feedReleaseTag, publishedPkgs, len(feedReleasePublishedArches)*2)
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
//   - pins: name → value for every URL pin whose name ends in "URL" — declared
//     as a string-literal constant OR a var derived from feedAssetURL (the
//     pinned-download-URL naming convention of this file). A pin named
//     differently would escape the registry; keep the convention. For a var
//     derived from feedAssetURL the value is recorded as the resolved
//     feedAssetURL call so the registry still knows the pin exists.
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
		if !ok || (decl.Tok != token.CONST && decl.Tok != token.VAR) {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			name := vs.Names[0].Name
			if !strings.HasSuffix(name, "URL") {
				continue
			}
			// String-literal pin (const or var).
			if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if value, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(value, "https://") {
					pins[name] = value
				}
				continue
			}
			// Var derived from feedAssetURL(...) — record the call so the
			// registry still knows the pin exists (value resolved at runtime).
			if call, ok := vs.Values[0].(*ast.CallExpr); ok {
				if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "feedAssetURL" {
					pins[name] = "feedAssetURL(...)"
				}
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
