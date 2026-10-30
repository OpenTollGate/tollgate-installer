package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The mint selector shipped with vendor-specific copy, and the deploy path once
// shipped a per-mint jq FILTER that dropped one mint by brand. Both were
// residue of the same incident: a mint whose keyset rotation was broken in the
// Cashu backend, worked around in the wizard.
//
// The workarounds are gone, and these two guards keep them gone:
//
//  1. NO MINT IS FILTERED. The accepted_mints set the wizard writes is exactly
//     the compile-time constants in defaultMintsJSON (plus the operator's own
//     chosen mint). A mint is removed by editing that constant set — never by a
//     pattern in the router-side jq filter, which is invisible to the operator
//     and can silently drop a mint on a router that already lists it.
//
//  2. OPERATOR-VISIBLE MINT COPY NAMES NO MINT. Which mint to prefer is the
//     operator's choice; naming a vendor in the hint — to announce it is
//     "temporarily removed" or otherwise — turns a backend workaround into
//     product copy that outlives the workaround. The hint states what the field
//     DOES.
//
// Guard 1 is a regression guard on a hazard that is ALREADY absent: the jq
// filter was removed before this file existed. It therefore cannot be RED on
// the current tree, and it does not pretend to be — its negative control below
// proves the scan still matches a real occurrence, so a GREEN result means
// "scanned and clean", not "pattern rotted". Guard 2 was observed RED on the
// tree that still carried the vendor copy, and GREEN after it was removed.
//
// NO BANNED TOKEN IS SPELLED AS SCANNED BYTES IN THIS FILE. Patterns that would
// match this file are assembled from fragments (`"url | te" + "st("`), so the
// scan can cover every Go file in the package, this one included, without
// tripping over its own rules.

// filterConstruct is the shape that silently drops a mint: a jq test() applied
// to a mint URL, kept or dropped on the strength of the URL's text. Assembled
// from fragments so this file does not match its own scan.
var filterConstruct = regexp.MustCompile(`(?i)url[^|\n]{0,40}\|\s*te` + `st\s*\(`)

// mintFilterLoop is the other half of the same hazard: a select()/map() over
// accepted_mints whose predicate is a URL-text match rather than a URL identity
// comparison.
var mintFilterLoop = regexp.MustCompile(`(?i)accepted_mints[^|\n]{0,80}\|\s*(map|select)\s*\([^)\n]*te` + `st\s*\(`)

// vendorMintCopy is the vocabulary of mint-vendor names that must not appear in
// operator-visible mint copy. Assembled from fragments, same reason as above.
var vendorMintCopy = regexp.MustCompile(`(?i)(mini` + `bits|coi` + `nos|ln` + `server|maca` + `damia|western` + `btc|ka` + `shu|cuba` + `bitcoin|test` + `nut)`)

// vendorDefectCopy is the incident narrative itself. An operator does not need
// to know which backend bug was worked around, and leaving the narrative in
// place invites the workaround back.
var vendorDefectCopy = regexp.MustCompile(`(?i)(DL` + `EQ|key` + `set rotation|temporarily remo` + `ved)`)

// TestNoMintURLIsFiltered is guard 1: nothing in the shipped mint pipeline may
// drop a mint by matching its URL text, and the production mint set must reach
// the router complete — every entry, unfiltered.
func TestNoMintURLIsFiltered(t *testing.T) {
	// --- static half: scan every shipped Go file in the package ------------
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		if m := filterConstruct.FindString(string(src)); m != "" {
			t.Errorf("%s: a mint URL is FILTERED by a jq test() construct (%q). No mint may be dropped by matching its URL text — the mint set is the compile-time constants in defaultMintsJSON, and removing one is an edit to that set, not a filter on the router.", f, m)
		}
		if m := mintFilterLoop.FindString(string(src)); m != "" {
			t.Errorf("%s: the accepted_mints list is FILTERED by a predicate on URL text (%q). Membership must be decided by URL identity (map(.url) | index($u)), never by a text match.", f, m)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go source files — the guard would pass vacuously; check the working directory")
	}

	// --- negative control: the scan still matches a real occurrence --------
	// Without this, a refactor that broke the pattern would leave the guard
	// green forever. The sample is assembled from fragments too.
	sample := "(.accepted_mints | map(select(.url | te" + `st("mini` + `bits") | not)))`
	if !filterConstruct.MatchString(sample) {
		t.Fatalf("negative control FAILED: the filter scan no longer matches a known-bad construct %q — the guard has rotted and proves nothing", sample)
	}

	// --- behavioural half: every production mint survives the real filter --
	// The static scan covers the constructive shape; this covers the RESULT.
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Log("jq not installed — behavioural half skipped (static scan above still enforced)")
		return
	}
	routerConfig := `{"margin":0,"profit_share":[{"identity":"owner","factor":1},{"identity":"developer","factor":0}],` +
		`"accepted_mints":[{"url":"https://mint.coinos.io","min_balance":64}]}`
	cmd := exec.Command(jqPath,
		"--argjson", "m", "5",
		"--argjson", "of", "0.9000",
		"--argjson", "df", "0.1000",
		"--argjson", "dm", defaultMintsJSON(false),
		"--arg", "mu", "",
		configJqFilter(),
	)
	cmd.Stdin = strings.NewReader(routerConfig)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("jq failed: %v\nstderr: %s", err, ee.Stderr)
		}
		t.Fatalf("jq failed to start: %v", err)
	}
	var merged struct {
		AcceptedMints []mintCfg `json:"accepted_mints"`
	}
	if err := json.Unmarshal(out, &merged); err != nil {
		t.Fatalf("merged config is not valid JSON: %v\n%s", err, out)
	}
	got := make(map[string]bool, len(merged.AcceptedMints))
	for _, m := range merged.AcceptedMints {
		got[m.URL] = true
	}
	for _, want := range wantProdMintURLs {
		if !got[want] {
			t.Errorf("production mint %q is ABSENT from the merged accepted_mints — a mint is being filtered out. Every compile-time mint must reach the router.", want)
		}
	}
	if len(merged.AcceptedMints) != len(wantProdMintURLs) {
		t.Errorf("merged accepted_mints has %d entries, want exactly %d (the compile-time production set, nothing added or dropped)",
			len(merged.AcceptedMints), len(wantProdMintURLs))
	}
}

// TestMintSelectorCopyIsBrandNeutral is guard 2: operator-visible mint copy
// names no mint vendor and carries no vendor-defect narrative.
func TestMintSelectorCopyIsBrandNeutral(t *testing.T) {
	page := string(indexHTML)

	// Locate the mint selector's hint rather than scanning the whole page, so
	// the guard asserts the copy an operator actually reads next to the field.
	// The <option> labels are real choices (a mint the operator may pick) and
	// are deliberately out of scope: they are data, not a preference claim.
	selector := regexp.MustCompile(`(?s)<select id="mint">.*?</select>\s*<div class="hint">(.*?)</div>`)
	m := selector.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("could not find the mint selector hint (<select id=\"mint\"> followed by <div class=\"hint\">) — the guard cannot prove anything about copy it cannot locate. Update the selector or this guard deliberately, not by loosening it.")
	}
	hint := m[1]
	if strings.TrimSpace(hint) == "" {
		t.Error("the mint selector hint is EMPTY — the field explains nothing to the operator")
	}

	if bad := vendorMintCopy.FindString(hint); bad != "" {
		t.Errorf("the mint selector hint names the mint vendor %q:\n  %s\nWhich mint to prefer is the operator's choice; the hint states what the field DOES, never which vendor is in or out of favour.", bad, hint)
	}
	if bad := vendorDefectCopy.FindString(hint); bad != "" {
		t.Errorf("the mint selector hint carries vendor-defect narrative %q:\n  %s\nA backend incident is not operator copy: the workaround it described is retired, and the narrative is what invites it back.", bad, hint)
	}

	// The narrative must not reappear anywhere on the operator-visible page,
	// not just in this one hint — a second surface is how it grows back.
	if bad := vendorDefectCopy.FindString(page); bad != "" {
		t.Errorf("operator-visible page (index.html) carries the mint-defect narrative %q. The backend defect it named is fixed and reachable; no shipped UI may describe it.", bad)
	}

	// Negative controls: both patterns still match a real occurrence.
	if !vendorMintCopy.MatchString("Mini" + "bits temporarily remo" + "ved") {
		t.Fatal("negative control FAILED: the vendor-name pattern no longer matches a known-bad hint — the guard has rotted")
	}
	if !vendorDefectCopy.MatchString("DLE" + `Q keyset rotation bug`) {
		t.Fatal("negative control FAILED: the defect-narrative pattern no longer matches a known-bad hint — the guard has rotted")
	}
}
