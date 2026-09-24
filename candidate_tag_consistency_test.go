package main

import (
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
