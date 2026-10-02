package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// C2-I-02 (pre-release security audit 2026-09-23), second half: the download
// candidate list must be TAG-CONSISTENT.
//
// `pkgCandidateURLs` used to append the pinned GitHub fallback (a v0.5.0 asset)
// after the feed URL for aarch64, so a 404 on the requested release's asset — a
// pinned tag whose assets are not published yet, a partially published release
// — fell through to a months-old build with no error and no version gap. The
// tests below pin that a candidate list never pairs a <tag> request with an
// asset from a different <tag>, at every level: URL builder, opt-in wrapper and
// the pre-stage list.

// releaseTagFromURL returns the release tag a GitHub release-asset URL names:
//
//	https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>  →  <tag>
//
// "" when the URL carries no release path. Test-only helper: the assertions
// below read the tag out of the URL the installer would actually fetch rather
// than trusting a hand-written expectation, so a future pin change cannot make
// them vacuous.
func releaseTagFromURL(url string) string {
	const marker = "/releases/download/"
	i := strings.Index(url, marker)
	if i < 0 {
		return ""
	}
	rest := url[i+len(marker):]
	j := strings.Index(rest, "/")
	if j <= 0 {
		return ""
	}
	return rest[:j]
}

// TestPkgCandidateURLsNeverCrossTags is the table test for the defect: for
// several effective feed tags — including a tag with no published assets at all
// — and every arch/extension, NO candidate URL may name a release other than
// the effective tag, and no candidate may be the pinned GitHub fallback.
func TestPkgCandidateURLsNeverCrossTags(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()

	arches := append([]string{"aarch64_cortex-a53"}, feedReleasePublishedArches...)
	tags := []string{
		wantFeedReleaseTag,          // the pinned release
		"v0.6.0-alpha2-pre9",        // a previous release of the same feed
		"v0.6.0-alpha2-pre999",      // nonexistent: the card's repro tag
		"v0.5.0",                    // the tag the pinned fallback asset belongs to
		"v9.9.9-does-not-exist-any", // arbitrary future tag
	}
	fallbackIPK := githubFallbackURL("aarch64_cortex-a53", ".ipk")
	fallbackAPK := githubFallbackURL("aarch64_cortex-a53", ".apk")
	if fallbackIPK == "" || fallbackAPK == "" {
		t.Fatal("fixture stale: no pinned GitHub fallback for aarch64_cortex-a53")
	}

	for _, tag := range tags {
		feedReleaseTag = tag
		for _, arch := range arches {
			for _, ext := range []string{".ipk", ".apk"} {
				got := pkgCandidateURLs(arch, ext)
				if len(got) != 1 {
					t.Errorf("pkgCandidateURLs(%q, %q) with feedReleaseTag=%s = %v, want exactly the feed URL (the list must not cross tags)",
						arch, ext, tag, got)
				}
				for _, u := range got {
					if rt := releaseTagFromURL(u); rt != tag {
						t.Errorf("candidate %q names release %q, but the effective tag is %q — a <tag> request must never be paired with another tag's asset",
							u, rt, tag)
					}
					if u == fallbackIPK || u == fallbackAPK {
						t.Errorf("candidate %q is the pinned GitHub fallback for a %s request — the fallback may only be offered by explicit opt-in",
							u, tag)
					}
					// The asset name must carry the version derived from the
					// SAME tag the URL path names, so the two spellings cannot
					// drift apart within one candidate.
					if want := "tollgate-wrt_" + feedPkgVersionForTag(tag) + "_" + arch + ext; !strings.HasSuffix(u, want) {
						t.Errorf("candidate %q does not end with %q", u, want)
					}
				}
			}
		}
	}

	// Restoring the tag must restore the derived candidate to the pinned
	// release (no captured/stale tag anywhere).
	feedReleaseTag = orig
	if got := pkgCandidateURLs("aarch64_cortex-a53", ".ipk"); len(got) != 1 || releaseTagFromURL(got[0]) != orig {
		t.Errorf("after restoring feedReleaseTag=%s: candidates = %v", orig, got)
	}
}

// TestPkgCandidateURLsWithFallbackOptInIsExplicit pins the only path that may
// return an asset from a different release, and the refusal that governs it.
func TestPkgCandidateURLsWithFallbackOptInIsExplicit(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()

	const arch = "aarch64_cortex-a53"
	fbIPK := githubFallbackURL(arch, ".ipk")
	fbTag := releaseTagFromURL(fbIPK)
	fbVer := githubFallbackPkgVersion(arch, ".ipk")
	if fbTag == "" || fbVer == "" {
		t.Fatal("fixture stale: pinned fallback URL is not a release asset URL")
	}

	// Not opted in: the list stays tag-consistent and the refusal explains why,
	// naming the requested tag, the arch and the version it refuses to install.
	list, err := pkgCandidateURLsWithFallback(arch, ".ipk", false)
	if err == nil {
		t.Fatalf("pkgCandidateURLsWithFallback(allowFallback=false) = (%v, nil), want a refusal", list)
	}
	for _, u := range list {
		if releaseTagFromURL(u) != feedReleaseTag {
			t.Errorf("refused list still carries a foreign tag: %v", list)
		}
		if u == fbIPK {
			t.Errorf("refused list still carries the fallback asset: %v", list)
		}
	}
	for _, want := range []string{feedReleaseTag, feedPkgVersion(), arch, fbVer, "allow-fallback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%s", want, err)
		}
	}

	// Opted in: the fallback is appended, and it is a DIFFERENT release than
	// the requested tag — that is exactly why the opt-in is required.
	list, err = pkgCandidateURLsWithFallback(arch, ".ipk", true)
	if err != nil {
		t.Fatalf("pkgCandidateURLsWithFallback(allowFallback=true) = %v", err)
	}
	if len(list) != 2 || list[0] != feedAssetURL(arch, ".ipk") || list[1] != fbIPK {
		t.Fatalf("opted-in candidates = %v, want [feed, fallback]", list)
	}
	if releaseTagFromURL(list[1]) == feedReleaseTag {
		t.Errorf("fixture broken: the fallback names the requested tag %s — it must be a different release for the opt-in to mean anything", feedReleaseTag)
	}

	// Opted in for an arch without a fallback: nothing is added, no error.
	if l, err := pkgCandidateURLsWithFallback("mipsel_24kc", ".ipk", true); err != nil || len(l) != 1 {
		t.Errorf("pkgCandidateURLsWithFallback(mipsel_24kc, allow=true) = (%v, %v), want the feed list and nil", l, err)
	}
}

// TestStageAssetURLsForArchIsTagConsistent pins the pre-stage list: a package
// from another release must not be prefetched behind an operator's back, and the
// opt-in must still reach it when it IS given.
func TestStageAssetURLsForArchIsTagConsistent(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()

	const arch = "aarch64_cortex-a53"
	fbIPK := githubFallbackURL(arch, ".ipk")

	for _, tag := range []string{wantFeedReleaseTag, "v0.6.0-alpha2-pre999"} {
		feedReleaseTag = tag

		// Default (no opt-in): feed assets for the effective tag only.
		t.Setenv(githubFallbackEnv, "")
		urls, refusal := stageAssetURLsForArch(arch, false, "", "opkg")
		if len(urls) != 1 {
			t.Errorf("stageAssetURLsForArch(%q) with tag %s = %v, want only the feed asset", arch, tag, urls)
		}
		for _, u := range urls {
			if releaseTagFromURL(u) != tag {
				t.Errorf("staged URL %q names another release than %s", u, tag)
			}
			if u == fbIPK {
				t.Errorf("staged the older GitHub fallback without an opt-in: %v", urls)
			}
		}
		// The suppression must be reported to the caller, not swallowed
		// (`candidates, _ :=`), so the pre-stage path can log why no package
		// from another release was cached (review finding 3). It is reported
		// for every suppressed arch: pre-staging runs before any download, so
		// the report cannot depend on the requested asset being unavailable.
		if refusal == nil {
			t.Errorf("stageAssetURLsForArch(%q) with tag %s suppressed the fallback without reporting it", arch, tag)
		} else {
			for _, want := range []string{tag, arch, "0.5.0"} {
				if !strings.Contains(refusal.Error(), want) {
					t.Errorf("suppression report does not name %q:\n%v", want, refusal)
				}
			}
			if !strings.Contains(refusal.Error(), "allow-fallback") {
				t.Errorf("suppression report does not point at the opt-in:\n%v", refusal)
			}
			// It must NOT claim the requested release is undownloadable: that
			// is only knowable after a download attempt (deploy step 6).
			if strings.Contains(refusal.Error(), "is not downloadable") {
				t.Errorf("pre-stage report claims the requested release is not downloadable:\n%v", refusal)
			}
		}
		// Package manager unknown: both formats, still tag-consistent.
		both, _ := stageAssetURLsForArch(arch, false, "", "")
		for _, u := range both {
			if releaseTagFromURL(u) != tag {
				t.Errorf("staged URL %q names another release than %s", u, tag)
			}
		}
	}

	// Opted in: the fallback is staged too, so a feed outage the operator
	// accepted still works from the cache.
	feedReleaseTag = wantFeedReleaseTag
	t.Setenv(githubFallbackEnv, "1")
	urls, _ := stageAssetURLsForArch(arch, false, "", "opkg")
	found := false
	for _, u := range urls {
		if u == fbIPK {
			found = true
		}
	}
	if !found {
		t.Errorf("stageAssetURLsForArch with %s=1 = %v, want the fallback asset staged", githubFallbackEnv, urls)
	}

	// An arch with no pinned fallback suppresses nothing, so there is nothing
	// to report — only the a53 entry exists in the map.
	if _, refusal := stageAssetURLsForArch("mipsel_24kc", false, "", "opkg"); refusal != nil {
		t.Errorf("stageAssetURLsForArch(mipsel_24kc) reported a suppression with no fallback: %v", refusal)
	}
}

// TestMissingAssetRefusalNamesTagAndArch is the fail-loudly side of the same
// defect, at the unit level: for a tag whose assets do not exist, the candidate
// selection must produce a refusal that names the requested tag AND the arch,
// and must hand the caller a tag-consistent list to fail with. The process-level
// proof (non-zero exit, nothing installed) is scripts/test-missing-asset-fails-loudly.sh.
func TestMissingAssetRefusalNamesTagAndArch(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()

	const arch = "aarch64_cortex-a53"
	feedReleaseTag = "v0.6.0-alpha2-pre999" // nonexistent by construction

	list, err := pkgCandidateURLsWithFallback(arch, ".ipk", false)
	if err == nil {
		t.Fatalf("a missing asset for %s produced no refusal: %v", feedReleaseTag, list)
	}
	msg := err.Error()
	for _, want := range []string{feedReleaseTag, arch, "v0.6.0-alpha2-pre999", "0.5.0"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not name %q:\n%s", want, msg)
		}
	}
	// The wording itself (case-insensitive: the message starts a sentence with
	// "Refusing").
	lower := strings.ToLower(msg)
	for _, want := range []string{"refusing", "older", "not downloadable"} {
		if !strings.Contains(lower, want) {
			t.Errorf("refusal does not contain %q:\n%s", want, msg)
		}
	}
	// The list the caller fails with still points at the requested tag, so the
	// step-6 message can only ever name the release the operator asked for.
	for _, u := range list {
		if releaseTagFromURL(u) != feedReleaseTag {
			t.Errorf("refusal list carries %q, not the requested tag %s", u, feedReleaseTag)
		}
	}
}

// TestStep6FallbackDecisionLogIsPreDownloadWording is the STATIC guard for the
// e23633e follow-up (C2-I-02): deploy step 6's candidate-selection line runs
// BEFORE any download has been attempted, so it may not publish the list-level
// refusal wording — githubFallbackSelection's "requested release … is not
// downloadable" — which is only knowable once every candidate has actually
// failed. e23633e removed that wording from the pre-stage report; step 6 still
// published it eagerly, so a healthy non-opted-in aarch64 deploy logged "is not
// downloadable" while its feed asset downloaded and installed cleanly.
//
// The guard pins the shape that keeps the two apart: the selection line goes
// through fallbackSelectionLogLine (pre-download wording), while the FAIL-loud
// detail keeps the list-level refusal. Re-inlining `fallbackRefusal.Error()`
// into the selection log would otherwise only show up in a healthy deploy's
// operator log, where no test reads it.
func TestStep6FallbackDecisionLogIsPreDownloadWording(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "deploy.go", deployGoSrc, 0)
	if err != nil {
		t.Fatalf("parsing deploy.go: %v", err)
	}

	run := funcBodyText(t, fset, f, "runDeployment")
	if !strings.Contains(run, "fallbackSelectionLogLine(") {
		t.Errorf("deploy step 6 no longer logs its fallback decision through fallbackSelectionLogLine " +
			"— the selection-time line must be built from pre-download wording")
	}
	if strings.Contains(run, "fallbackRefusal.Error()") {
		t.Errorf("deploy step 6 logs fallbackRefusal.Error() at candidate-selection time: that text " +
			"asserts the requested release \"is not downloadable\" before anything has been " +
			"downloaded — the defect this card fixes")
	}

	selection := funcBodyText(t, fset, f, "fallbackSelectionLogLine")
	if !strings.Contains(selection, "fallbackSuppressedReason(") {
		t.Errorf("fallbackSelectionLogLine no longer builds the selection-time line from " +
			"fallbackSuppressedReason — the pre-download wording has lost its only source")
	}
	if strings.Contains(selection, "refusal.Error()") {
		t.Errorf("fallbackSelectionLogLine publishes the list-level refusal text verbatim: that is " +
			"the \"is not downloadable\" claim, only established after a download fails")
	}

	// The other half of the split: fail-loud time DOES publish the list-level
	// refusal (tag, arch, older version, opt-in) in the step detail.
	fail := funcBodyText(t, fset, f, "refuseMissingRequestedRelease")
	if !strings.Contains(fail, "refusal.Error()") {
		t.Errorf("refuseMissingRequestedRelease no longer publishes the refusal detail: the " +
			"fail-loud path must keep naming the requested tag and why it refused")
	}
}

// TestStep6SelectionLogKeepsThePreDownloadWording pins acceptance criterion 1 of
// the C2-I-02 follow-up at the unit level: on the DEFAULT path (no
// --allow-fallback, no TOLLGATE_ALLOW_GITHUB_FALLBACK=1) for an arch with a
// pinned fallback, the line step 6 logs at candidate-selection time must not
// assert that the requested release "is not downloadable". Nothing has been
// downloaded when that line is emitted, and on this path the feed asset is
// normally downloaded and installed cleanly immediately afterwards — which is
// exactly how a healthy aarch64 deploy came to print that claim.
//
// The wording is asserted on BOTH strings, so the fix cannot be "delete the
// sentence": the list-level refusal must keep it, because that is what the
// FAIL-loud gate publishes once a download really has failed (criterion 2).
func TestStep6SelectionLogKeepsThePreDownloadWording(t *testing.T) {
	orig := feedReleaseTag
	defer func() { feedReleaseTag = orig }()
	t.Setenv(githubFallbackEnv, "") // the default: no opt-in

	const arch = pkgFallbackTestArch // aarch64_cortex-a53, the arch with a pinned fallback
	fbURL := githubFallbackURL(arch, ".ipk")
	if fbURL == "" {
		t.Fatalf("no GitHub fallback pinned for %s/.ipk — fixture is stale", arch)
	}

	candidates, refusal := pkgCandidateURLsWithFallback(arch, ".ipk", githubFallbackAllowed())
	if refusal == nil || len(candidates) == 0 {
		t.Fatalf("pkgCandidateURLsWithFallback(%s, .ipk, no opt-in) = (%v, %v), want the feed list plus a refusal",
			arch, candidates, refusal)
	}
	// The premise the line is asserted on: this is the HEALTHY path — the
	// requested release's own asset is the only candidate, and it is what the
	// deploy goes on to download.
	if len(candidates) != 1 || releaseTagFromURL(candidates[0]) != feedReleaseTag {
		t.Fatalf("candidates = %v, want only the feed asset for the requested tag %s", candidates, feedReleaseTag)
	}

	line := fallbackSelectionLogLine(arch, ".ipk", candidates, refusal)
	if line == "" {
		t.Fatal("step 6 logs nothing about a withheld fallback: the operator cannot tell why the older asset was not tried")
	}
	if strings.Contains(line, "is not downloadable") {
		t.Errorf("step 6's selection line claims the requested release is not downloadable before any download was attempted:\n%s", line)
	}
	for _, want := range []string{feedReleaseTag, arch, "0.5.0", "allow-fallback", fbURL} {
		if !strings.Contains(line, want) {
			t.Errorf("selection line does not name %q:\n%s", want, line)
		}
	}

	// Criterion 2: the refusal the fail-loud gate publishes still carries the
	// list-level wording and all four facts.
	detail := refusal.Error()
	for _, want := range []string{feedReleaseTag, arch, "0.5.0", "allow-fallback"} {
		if !strings.Contains(detail, want) {
			t.Errorf("refusal detail does not name %q:\n%s", want, detail)
		}
	}
	if !strings.Contains(strings.ToLower(detail), "not downloadable") {
		t.Errorf("the list-level refusal lost its \"not downloadable\" wording — it is the text the fail-loud path is reviewed for:\n%s", detail)
	}

	// Opted in: the line names the version that WILL be installed instead of
	// the requested one, and still makes no availability claim.
	opted, err := pkgCandidateURLsWithFallback(arch, ".ipk", true)
	if err != nil || len(opted) < 2 {
		t.Fatalf("opted-in candidates = %v, %v; want the feed asset plus the fallback", opted, err)
	}
	optLine := fallbackSelectionLogLine(arch, ".ipk", opted, nil)
	for _, want := range []string{"0.5.0", fbURL, "opt-in"} {
		if !strings.Contains(optLine, want) {
			t.Errorf("opted-in selection line does not name %q:\n%s", want, optLine)
		}
	}
	if strings.Contains(optLine, "is not downloadable") {
		t.Errorf("opted-in selection line makes an availability claim:\n%s", optLine)
	}

	// An arch with no pinned fallback withholds nothing, so there is nothing to
	// report — and no line may claim otherwise.
	plain, plainRefusal := pkgCandidateURLsWithFallback("mipsel_24kc", ".ipk", false)
	if plainRefusal != nil {
		t.Fatalf("no-fallback arch produced a refusal: %v", plainRefusal)
	}
	if got := fallbackSelectionLogLine("mipsel_24kc", ".ipk", plain, plainRefusal); got != "" {
		t.Errorf("step 6 logs %q for an arch with no pinned fallback, want no line at all", got)
	}
}
