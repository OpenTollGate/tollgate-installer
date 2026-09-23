package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ─── Router CPU architecture auto-detection ─────────────────────────────
//
// The wizard used to hardcode "aarch64_cortex-a53" in every package download
// URL. That is wrong for any other router — a mipsel_24kc binary, for example,
// will not execute on an ARM router, and on an x86_64 gl-gate it would be the
// same class of silent failure. detectArch reads the target's real CPU
// architecture at deploy time and the deployment then selects the matching
// tollgate-wrt asset. See docs/arch-detect.md for the full reasoning and the
// precedence ladder.

// bareArchToTuple normalizes a BARE CPU architecture name (as reported by
// `apk --print-arch` or `uname -m`) to the canonical OpenWrt arch tuple that
// appears in package feed path names and in tollgate-wrt release assets.
//
// `apk --print-arch` on a GL-MT3000 prints the bare "aarch64", not the
// "aarch64_cortex-a53" tuple the download URLs (and the OpenWrt package
// repository layout) use. Mapping that is the whole point of this function — a
// missing entry here is the bug this code deletes.
var bareArchToTuple = map[string]string{
	"aarch64": "aarch64_cortex-a53",
	"arm64":   "aarch64_cortex-a53",
	"mipsel":  "mipsel_24kc",
	"mips":    "mips_24kc",
	"x86_64":  "x86_64",
	"amd64":   "x86_64",
}

// canonicalArchTuples is the set of canonical OpenWrt arch tuples that
// normalizes to themselves. This lets canonical tuples pass through unchanged
// and lets TestArchAssetsMatchDetectedArch assert every canonical tuple
// resolves to a selectable feed URL. Add a tuple here only when it is a real
// OpenWrt feed target; an unknown bare name maps to "" (never a guessed
// default).
var canonicalArchTuples = map[string]bool{
	"aarch64_cortex-a53": true,
	"mipsel_24kc":        true,
	"mips_24kc":          true,
	"x86_64":             true,
}

// normalizeBareArch maps a BARE architecture string to a canonical OpenWrt
// tuple, or returns "" if it cannot be mapped. It tolerates surrounding
// whitespace and case; canonical tuples (e.g. "aarch64_cortex-a53") pass
// through unchanged. It NEVER invents a default.
func normalizeBareArch(b string) string {
	b = strings.ToLower(strings.TrimSpace(b))
	if b == "" {
		return ""
	}
	if canonicalArchTuples[b] {
		return b
	}
	if tuple, ok := bareArchToTuple[b]; ok {
		return tuple
	}
	return ""
}

// ─── Feed release identity ──────────────────────────────────────────────
//
// The feed-built tollgate-wrt package is published by FreedomTechFeed/packages
// (net/tollgate-wrt/Makefile). Its Makefile keeps TWO version spellings, and
// the URL/asset name below must match both:
//
//	release TAG := v0.6.0-alpha2-pre4   (hyphenated)        → download URL
//	PKG_VERSION := 0.6.0_alpha2_pre4    (apk-legal underscore) → asset NAME
//
// PKG_SOURCE_VERSION in the feed is the upstream module COMMIT (373770a), not
// this tag — but every ARCH asset URL is named by the tag/PKG_VERSION pair above.
//
// The split is forced by apk-tools 3.x, which rejects hyphens in versions.
// feedPkgVersionForTag DERIVES the second spelling from the first, so the two
// cannot drift: there is exactly ONE version literal (feedReleaseTagDefault)
// and one conversion rule. Every asset URL is built from the effective tag,
// which keeps them in sync by construction rather than by convention.
//
// The effective tag is feedReleaseTagDefault unless TOLLGATE_FEED_RELEASE_TAG
// overrides it (see feedReleaseTagEnv) — the override exists so a pre-release
// or a rollback can be exercised without rebuilding the wizard.
const (
	// feedRepoSlug is the repo whose CI builds and publishes the feed packages.
	feedRepoSlug = "FreedomTechFeed/packages"
	// feedReleaseTagDefault is the feed release tag selected by default. pre4
	// ships the vendored, working captive portal (the pre3 package had no
	// /assets bundles); its source pin is still module main's tip, 373770a.
	feedReleaseTagDefault = "v0.6.0-alpha2-pre9"
	// feedReleaseTagEnv is the environment variable that overrides
	// feedReleaseTagDefault. Set it to select another published release tag
	// (e.g. a newer pre-release, or an older tag to reproduce an old build)
	// without rebuilding the wizard:
	//
	//	TOLLGATE_FEED_RELEASE_TAG=v0.6.0-alpha1 ./tollgate-installer
	//
	// An empty or implausible value is ignored in favour of the default, so a
	// typo cannot turn into a malformed download URL for every arch.
	feedReleaseTagEnv = "TOLLGATE_FEED_RELEASE_TAG"
	// feedChannelEnv selects the newest feed release in a CHANNEL instead of a
	// fixed tag. It is consulted only when feedReleaseTagEnv is unset, and it
	// is OFF by default: no channel env means no network at startup and the
	// compiled feedReleaseTagDefault is used, keeping builds reproducible. The
	// curl|bash launcher resolves the tag itself and passes the exact tag, so
	// this is for a directly-run binary:
	//
	//	TOLLGATE_FEED_CHANNEL=alpha ./tollgate-installer   # newest pre-release
	//
	// alpha = newest tag matching (pre|alpha|beta|rc); beta = (beta|rc);
	// stable = no pre-release marker; any/latest = newest overall.
	feedChannelEnv = "TOLLGATE_FEED_CHANNEL"
)

// feedReleaseTagRe matches a plausible GitHub release tag (v0.6.0-alpha2-pre3,
// v0.6.0-alpha1, 0.5.0). It is deliberately permissive about WHICH tag — the
// feed is the authority on what it published, and
// TestPinnedFeedReleaseTagExists fails if the selected tag does not exist —
// but strict about the SHAPE, so a typo, a stray space, or a shell fragment
// can never be interpolated into a download URL.
var feedReleaseTagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// feedReleaseTag is the effective release tag: the TOLLGATE_FEED_RELEASE_TAG
// override, else the newest release in TOLLGATE_FEED_CHANNEL, else
// feedReleaseTagDefault. It is resolved once at startup (the PreStage pins in
// deploy.go are derived from it at init time), so set the override/channel
// before starting the wizard, not per deploy. With neither env set there is no
// network call and the compiled default applies, keeping builds reproducible.
var feedReleaseTag = resolveFeedTagWithChannel(os.Getenv, githubResolveChannel)

// resolveFeedReleaseTag returns the effective feed release tag. Unset, empty,
// whitespace-only, and implausibly-shaped overrides all fall back to the
// default: a bad override must never produce a URL that 404s on every arch.
// Injectable so the precedence is unit-testable without touching real env.
func resolveFeedReleaseTag(getenv func(string) string) string {
	if getenv == nil {
		return feedReleaseTagDefault
	}
	if tag := strings.TrimSpace(getenv(feedReleaseTagEnv)); tag != "" && feedReleaseTagRe.MatchString(tag) {
		return tag
	}
	return feedReleaseTagDefault
}

// feedChannelRe matches a plausible channel name. Strict about the SHAPE so a
// stray value can never be interpolated into an API query.
var feedChannelRe = regexp.MustCompile(`^(alpha|beta|stable|any|latest)$`)

// feedReleaseRef is the subset of a GitHub release object the channel resolver
// needs. PublishedAt is preferred over CreatedAt for ordering (a release can be
// created as a draft long before it is published; GitHub's own "latest" flag is
// stale for this feed, so we sort ourselves).
type feedReleaseRef struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	CreatedAt   string `json:"created_at"`
}

// selectFeedTagForChannel returns the newest release tag in channel, or "" if
// none match. Pure, so the channel semantics are unit-tested without network.
func selectFeedTagForChannel(rels []feedReleaseRef, channel string) string {
	channel = strings.ToLower(strings.TrimSpace(channel))
	isPre := func(t string) bool {
		return strings.Contains(strings.ToLower(t), "pre") ||
			strings.Contains(strings.ToLower(t), "alpha") ||
			strings.Contains(strings.ToLower(t), "beta") ||
			strings.Contains(strings.ToLower(t), "rc")
	}
	ok := func(t string) bool {
		switch channel {
		case "", "any", "latest":
			return true
		case "alpha":
			return isPre(t)
		case "beta":
			lt := strings.ToLower(t)
			return strings.Contains(lt, "beta") || strings.Contains(lt, "rc")
		case "stable":
			return !isPre(t)
		default:
			return false
		}
	}
	var matches []feedReleaseRef
	for _, r := range rels {
		if r.TagName != "" && feedReleaseTagRe.MatchString(r.TagName) && ok(r.TagName) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return ""
	}
	sort.SliceStable(matches, func(i, j int) bool {
		ki, kj := matches[i].PublishedAt, matches[j].PublishedAt
		if ki == "" {
			ki = matches[i].CreatedAt
		}
		if kj == "" {
			kj = matches[j].CreatedAt
		}
		return ki > kj // newest first
	})
	return matches[0].TagName
}

// githubResolveChannel fetches the feed repo's releases and returns the newest
// tag in channel, or "" on any failure (network, rate limit, bad payload). The
// caller falls back to the compiled default, so a resolver failure never breaks
// a deploy — it only means the pin is used instead of the channel tip.
func githubResolveChannel(channel string) string {
	if !feedChannelRe.MatchString(strings.ToLower(strings.TrimSpace(channel))) {
		return ""
	}
	url := "https://api.github.com/repos/" + feedRepoSlug + "/releases?per_page=100"
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var rels []feedReleaseRef
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rels); err != nil {
		return ""
	}
	return selectFeedTagForChannel(rels, channel)
}

// resolveFeedTagWithChannel is the full precedence used at startup:
//
//	TOLLGATE_FEED_RELEASE_TAG (explicit) > TOLLGATE_FEED_CHANNEL > compiled default
//
// Injecting getenv and fetch keeps it pure for tests: with no channel env set
// it never calls fetch, so unit tests stay network-free and deterministic.
func resolveFeedTagWithChannel(getenv func(string) string, fetch func(string) string) string {
	if getenv == nil {
		return feedReleaseTagDefault
	}
	if tag := strings.TrimSpace(getenv(feedReleaseTagEnv)); tag != "" && feedReleaseTagRe.MatchString(tag) {
		return tag
	}
	if ch := strings.ToLower(strings.TrimSpace(getenv(feedChannelEnv))); feedChannelRe.MatchString(ch) {
		if fetch != nil {
			if tag := fetch(ch); tag != "" && feedReleaseTagRe.MatchString(tag) {
				return tag
			}
		}
	}
	return feedReleaseTagDefault
}

// feedPkgVersionForTag converts a release tag into the PKG_VERSION spelling the
// feed uses in asset names and in installed package metadata: the leading "v"
// is dropped and every hyphen becomes an underscore (apk-tools 3.x rejects
// hyphens in versions).
//
//		v0.6.0-alpha2-pre3 → 0.6.0_alpha2_pre3   (installed as 0.6.0_alpha2_pre3-r1)
//	v0.6.0-alpha1    → 0.6.0_alpha1
//
// This is the ONLY place the tag spelling and the package-version spelling are
// related, so an asset URL can never name a version its tag does not describe.
func feedPkgVersionForTag(tag string) string {
	return strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(tag), "v"), "-", "_")
}

// feedPkgVersion is the effective package version: the derived spelling of the
// effective release tag. It is what appears in asset names on the feed release
// and in the `Version:` field of the installed package.
func feedPkgVersion() string { return feedPkgVersionForTag(feedReleaseTag) }

// feedReleaseURLPrefix is the fixed prefix of every feed asset URL, derived
// from the effective tag (and therefore from the same single literal).
func feedReleaseURLPrefix() string {
	return "https://github.com/" + feedRepoSlug + "/releases/download/" + feedReleaseTag + "/"
}

// feedAssetURL builds the deterministic tollgate-wrt download URL for a
// canonical OpenWrt arch tuple and file extension. The feed publishes every
// arch it builds at a stable, predictable URL:
//
//	https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha2-pre3/tollgate-wrt_0.6.0_alpha2_pre3_<arch>.<ext>
//
// Because the URL is DERIVED from the tuple rather than looked up in a
// hardcoded map, ANY arch the feed publishes resolves without a code change —
// a new feed target (e.g. arm_cortex-a7) works the moment the feed ships it.
// A detected-but-unpublished arch yields a URL that 404s at download time,
// which is an honest, actionable failure rather than a hardcoded "unsupported"
// list that must be maintained as the feed grows.
//
// Callers must pass a real tuple: pkgCandidateURLs refuses an unmapped arch,
// so this function is never reached with an empty arch (which would otherwise
// build a malformed "..._tollgate-wrt_0.6.0_alpha2_pre_.ipk" URL).
//
// The arch and ext are appended verbatim, which is what the feed's asset names
// do too (arm_cortex-a7, mipsel_24kc, mips64_octeonplus …). That equality is
// asserted against the live release by
// TestFeedReleasePublishesEachDerivedAssetName.
func feedAssetURL(arch, ext string) string {
	return feedReleaseURLPrefix() + "tollgate-wrt_" + feedPkgVersion() + "_" + arch + ext
}

// tollgateGithubFallback is the GitHub tollgate-module-basic-go release assets,
// kept as a FALLBACK for arches the feed does not publish yet (or a feed
// outage). Only aarch64_cortex-a53 has published GitHub release assets today.
//
// These assets belong to a DIFFERENT (older) release than the effective feed
// tag, so they are NOT part of pkgCandidateURLs: the download candidate list is
// tag-consistent and a 404 is never answered with another release's build.
// The map is consulted only by githubFallbackSelection, i.e. after an explicit
// operator opt-in — see pkgCandidateURLsWithFallback (C2-I-02).
var tollgateGithubFallback = map[string]struct{ IPK, APK string }{
	"aarch64_cortex-a53": {
		IPK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk",
		APK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.apk",
	},
}

// githubFallbackURL returns the GitHub release fallback URL for a canonical
// arch tuple and package extension, or "" if none exists. Only
// aarch64_cortex-a53 has published GitHub release assets today. It is a
// different, OLDER release than feedReleaseTag, so it only ever reaches a
// download candidate list through pkgCandidateURLsWithFallback after an
// explicit opt-in — never as a silent second attempt.
func githubFallbackURL(arch, ext string) string {
	asset, ok := tollgateGithubFallback[arch]
	if !ok {
		return ""
	}
	if ext == ".apk" {
		return asset.APK
	}
	return asset.IPK
}

// githubFallbackEnv is the environment variable that opts a run into the GitHub
// release fallback — the same explicit opt-in as --allow-fallback. It exists
// because the curl|bash launcher starts the wizard for the operator and does not
// forward CLI flags:
//
//	TOLLGATE_ALLOW_GITHUB_FALLBACK=1 ./tollgate-installer
const githubFallbackEnv = "TOLLGATE_ALLOW_GITHUB_FALLBACK"

// githubFallbackAllowed reports whether the operator EXPLICITLY opted into the
// GitHub release fallback: the --allow-fallback flag, or a truthy
// TOLLGATE_ALLOW_GITHUB_FALLBACK. OFF by default. The fallback asset is a
// different, OLDER release than the requested one, so taking it without asking
// is a silent downgrade — see githubFallbackSelection.
func githubFallbackAllowed() bool {
	if allowFallback != nil && *allowFallback {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(githubFallbackEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// pkgVersionFromReleaseURL extracts the package-version spelling named by a
// GitHub release asset URL:
//
//	https://github.com/<owner>/<repo>/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk
//	  → 0.5.0
//
// It goes through feedPkgVersionForTag, the single place the tag spelling and
// the package-version spelling are related, so a fallback version can never
// drift from the asset it names. "" when the URL carries no release path.
func pkgVersionFromReleaseURL(url string) string {
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
	return feedPkgVersionForTag(rest[:j])
}

// githubFallbackPkgVersion is the package version the GitHub fallback asset for
// an arch would install (0.5.0 for the current pin — while the effective feed
// release is feedReleaseTag). "" when the arch has no fallback.
func githubFallbackPkgVersion(arch, ext string) string {
	return pkgVersionFromReleaseURL(githubFallbackURL(arch, ext))
}

// githubFallbackSelection returns the GitHub fallback URL to download for an
// arch — but ONLY when the operator explicitly allowed it. Without that opt-in
// it returns an error naming BOTH the requested release/tag and the version the
// fallback would install instead, so the caller can stop and say so rather than
// quietly shipping an older package and reporting success.
//
// The fallback exists for a feed outage; it is NOT a drop-in substitute, because
// it is pinned to a different (older) release than the effective feed tag — for
// aarch64_cortex-a53 the feed DOES publish, so this entry is only ever a
// downgrade path. ("", nil) when the arch has no fallback at all: nothing to
// select and nothing to refuse.
func githubFallbackSelection(arch, ext string, allowFallback bool) (string, error) {
	fb := githubFallbackURL(arch, ext)
	if fb == "" {
		return "", nil
	}
	if allowFallback {
		return fb, nil
	}
	fbVer := githubFallbackPkgVersion(arch, ext)
	return "", fmt.Errorf(
		"requested release %s (%s) is not downloadable for %s, and the GitHub fallback asset is %s "+
			"(a different, OLDER package). Refusing to downgrade silently — re-run with --allow-fallback "+
			"or %s=1 to accept %s, or fix the feed/tag and retry. Fallback URL: %s",
		feedReleaseTag, feedPkgVersion(), arch, fbVer, githubFallbackEnv, fbVer, fb)
}

// expectedPkgVersionForSource is the package version named by the source URL
// that actually supplied the tollgate-wrt bytes: the requested feed release for
// the feed asset, the pinned fallback release for the GitHub fallback asset.
// "" for a URL that is neither — notably the router-feed last-resort path, where
// nothing was pushed and there is no URL to name.
func expectedPkgVersionForSource(sourceURL, arch, ext string) string {
	if sourceURL == "" {
		return ""
	}
	if sourceURL == feedAssetURL(arch, ext) {
		return feedPkgVersion()
	}
	if fb := githubFallbackURL(arch, ext); fb != "" && sourceURL == fb {
		return githubFallbackPkgVersion(arch, ext)
	}
	return ""
}

// pkgVersionVerdict classifies the post-install version readback against the
// source that supplied the package. It replaces a check that was gated on the
// source URL (`strings.Contains(pkgSourceURL, "/releases/download/"+feedReleaseTag+"/")`,
// deploy.go:645), which meant a GitHub-fallback install was NEVER compared to
// anything: no comparison, no failure, and the step still rendered green.
//
//	fatal — the installed version does not match the version the SUPPLYING
//	        source names: the upgrade did not take effect (a no-op install, or
//	        a stale package in /tmp). The step MUST fail.
//	warn  — the installed version is not the REQUESTED release (an explicitly
//	        opted-in fallback, or an install from the router's own feeds). The
//	        step MUST NOT render as an unqualified success.
//
// Both are "" when there is nothing to assert (no version was read back) or
// when the installed version is exactly the requested release.
func pkgVersionVerdict(installed, sourceURL, arch, ext string) (fatal, warn string) {
	if installed == "" {
		return "", ""
	}
	if want := expectedPkgVersionForSource(sourceURL, arch, ext); want != "" && !strings.HasPrefix(installed, want) {
		return fmt.Sprintf("installed package is %s but the source that supplied it (%s) provides %s — "+
			"the upgrade did not take effect (previous package/files still present)",
			installed, pkgSourceLabel(arch, ext, sourceURL), want), ""
	}
	if !strings.HasPrefix(installed, feedPkgVersion()) {
		return "", fmt.Sprintf("installed package is %s, NOT the requested release %s (%s) — "+
			"the router is running a different, older build than the one this deploy asked for",
			installed, feedReleaseTag, feedPkgVersion())
	}
	return "", ""
}

// pkgArchTupleRe matches a plausible canonical OpenWrt arch tuple: at least one
// alphanumeric, then letters/digits/underscore/hyphen (e.g. aarch64_cortex-a53,
// mipsel_24kc, arm_cortex-a7). It is deliberately permissive about WHICH tuple
// (the feed is the authority on what it publishes) but strict about the SHAPE,
// so an unmapped, empty, or whitespace/injection-bearing arch can never be
// interpolated into a download URL.
var pkgArchTupleRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// pkgCandidateURLs returns the ordered download URLs to try for a canonical
// arch tuple and package extension: the feed URL derived generically from the
// tuple for the EFFECTIVE tag. The first URL that yields bytes wins.
//
// The list is TAG-CONSISTENT: every URL it returns names the same release as
// feedReleaseTag, so a missing asset can never be answered by downloading a
// different (older) build. The GitHub fallback asset belongs to a different
// release and is therefore NOT here — offering it is an explicit operator
// decision, see pkgCandidateURLsWithFallback. (Before this, a 404 on the feed
// URL silently fell through to the pinned v0.5.0 asset: C2-I-02.)
//
// An UNMAPPED arch (empty, whitespace, or anything that is not tuple-shaped)
// yields NO candidates. Returning a URL built from a bad arch would be worse
// than returning nothing: it produces a well-formed-looking but wrong
// "..._tollgate-wrt_0.6.0_alpha2_pre_.ipk" URL that 404s, and it would mask the
// real problem (arch detection failed). The caller treats an empty list as a
// hard failure and never substitutes another arch's asset.
func pkgCandidateURLs(arch, ext string) []string {
	if !pkgArchTupleRe.MatchString(arch) {
		return nil
	}
	return []string{feedAssetURL(arch, ext)}
}

// pkgCandidateURLsWithFallback is pkgCandidateURLs plus, ONLY when allowFallback
// is true, the GitHub release fallback asset for the arch. That asset is a
// DIFFERENT, OLDER release than feedReleaseTag, so it is never appended
// silently:
//
//   - allowFallback=false: the tag-consistent feed list is returned together
//     with an error naming the requested tag, the arch and the version the
//     fallback would install instead. The caller MUST stop and fail loudly
//     (deploy.go step 6) — that error is the whole point of this function.
//   - allowFallback=true: [feed URL, fallback URL]. The operator asked for the
//     downgrade, so the caller also reports the version it installs.
//
// An arch with no pinned fallback yields the feed list and a nil error: there
// is nothing to offer and nothing to refuse.
func pkgCandidateURLsWithFallback(arch, ext string, allowFallback bool) ([]string, error) {
	urls := pkgCandidateURLs(arch, ext)
	if len(urls) == 0 {
		return nil, nil
	}
	fb, err := githubFallbackSelection(arch, ext, allowFallback)
	if err != nil {
		return urls, err
	}
	if fb != "" && fb != urls[0] {
		urls = append(urls, fb)
	}
	return urls, nil
}

// selectPkgURL returns the tollgate-wrt download URL and file extension for
// the given canonical arch tuple and package manager. pkgMgr must be "opkg" or
// "apk".
//
// It is GENERIC: the feed URL is derived from the tuple via feedAssetURL, so
// any arch the feed publishes resolves. It returns ok=false only for an
// empty/undetectable arch — the caller must treat that as a hard deploy
// failure and NEVER silently substitute the aarch64 fallback.
func selectPkgURL(arch, pkgMgr string) (url, ext string, ok bool) {
	if arch == "" {
		return "", "", false
	}
	ext = ".ipk"
	if pkgMgr == "apk" {
		ext = ".apk"
	}
	return feedAssetURL(arch, ext), ext, true
}

// distArchRe pulls DISTRIB_ARCH out of /etc/openwrt_release output. The file
// format is shell: DISTRIB_ARCH="aarch64_cortex-a53".
var distArchRe = regexp.MustCompile(`(?m)^DISTRIB_ARCH=["']?([^"'\s]+)`)

// opkgArchRe matches the first "arch <name> <pri>" line in `opkg print-architecture`.
var opkgArchRe = regexp.MustCompile(`(?m)^\s*arch\s+(\S+)\s+\d+`)

// detectArch returns the canonical OpenWrt CPU architecture tuple for the
// target router, probing sources in precedence order. It returns "" (never a
// hardcoded default) if the architecture cannot be determined.
//
// Precedence:
//  1. DISTRIB_ARCH from /etc/openwrt_release  — authoritative, exact-tuple today.
//  2. `opkg print-architecture`               — first "arch <name> <pri>" line.
//  3. `ubus call system board`                — JSON .architecture.
//  4. `apk --print-arch`                      — bare; normalized via normalizeBareArch.
//  5. `uname -m`                              — coarse; last resort, always normalized.
func detectArch(client *ssh.Client) string {
	if client == nil {
		return ""
	}
	return detectArchFrom(func(cmd string) string {
		return sshRun(client, cmd)
	})
}

// detectArchFrom is the pure, testable core of detectArch. get runs a remote
// command and returns its combined output; the precedence ladder below is
// exercised directly by TestDetectArchPrecedence without a live SSH session.
func detectArchFrom(get func(cmd string) string) string {
	if get == nil {
		return ""
	}

	// 1. Authoritative: DISTRIB_ARCH from /etc/openwrt_release.
	if out := get("grep DISTRIB_ARCH /etc/openwrt_release 2>/dev/null; true"); out != "" {
		if m := distArchRe.FindStringSubmatch(out); m != nil {
			arch := strings.TrimSpace(m[1])
			if arch != "" {
				return arch
			}
		}
	}

	// 2. opkg print-architecture — first real "arch <name> <pri>" line.
	// The command lists pseudo-architectures first ("all", "noarch"); the
	// usable CPU tuple is the first line whose name isn't a pseudo-arch.
	if out := get("opkg print-architecture 2>/dev/null; true"); out != "" {
		for _, m := range opkgArchRe.FindAllStringSubmatch(out, -1) {
			arch := strings.TrimSpace(m[1])
			if arch == "" || arch == "all" || arch == "noarch" {
				continue
			}
			return arch
		}
	}

	// 3. ubus call system board — JSON .architecture field. This field is not
	//    present on stock OpenWrt (the call exposes .system and .release.target
	//    instead), so this branch rarely fires — but when it does the value is
	//    a bare CPU name (e.g. "aarch64") that must be normalized to a tuple.
	if out := get("ubus call system board 2>/dev/null; true"); out != "" {
		var board struct {
			Architecture string `json:"architecture"`
		}
		if err := json.Unmarshal([]byte(out), &board); err == nil && board.Architecture != "" {
			if arch := normalizeBareArch(board.Architecture); arch != "" {
				return arch
			}
		}
	}

	// 4. apk --print-arch — bare arch, must be normalized.
	if out := strings.TrimSpace(get("apk --print-arch 2>/dev/null; true")); out != "" {
		if arch := normalizeBareArch(out); arch != "" {
			return arch
		}
	}

	// 5. uname -m — coarse, last resort, always normalized.
	if out := strings.TrimSpace(get("uname -m 2>/dev/null; true")); out != "" {
		if arch := normalizeBareArch(out); arch != "" {
			return arch
		}
	}

	return ""
}

// downloadBaseURL builds the OpenWrt package-repository base URL for an arch
// tuple. The wizard pins release 24.10.4 (the release it was validated on); the
// arch tuple is threaded in so nodogsplash/jq installs resolve for ANY router.
func downloadBaseURL(arch string) string {
	return fmt.Sprintf("https://downloads.openwrt.org/releases/24.10.4/packages/%s/", arch)
}

// ─── Package provenance: WHICH source supplied the package ──────────────
//
// The install step tries the feed URL first and the pinned GitHub release
// second, and both produce a working install. Without an explicit label an
// operator cannot tell which one was used — yet the entire reason for
// preferring the feed build is to exercise tollgate-module-basic-go +
// FreedomTechFeed/packages on a real router. The labels below are written to
// the job log (and the install step's detail) so "which source supplied this
// package?" is answerable afterwards. No silent substitution: a GitHub-release
// install is reported as exactly that.
const (
	// pkgSourceFeedRelease — the feed-published release asset: the primary,
	// intended source.
	pkgSourceFeedRelease = "FreedomTechFeed/packages release"
	// pkgSourceGitHubRelease — the pinned tollgate-module-basic-go GitHub
	// release asset: the explicit fallback (feed outage / arch not published
	// by the feed yet).
	pkgSourceGitHubRelease = "tollgate-module-basic-go GitHub release (fallback)"
	// pkgSourceRouterFeed — package installed from the ROUTER's own configured
	// opkg/apk repositories (last-resort path; nothing was pushed over SSH).
	pkgSourceRouterFeed = "router package feed"
	// pkgSourceUnrecognised — a URL that is neither candidate. Reported rather
	// than guessed, so provenance is never asserted wrongly.
	pkgSourceUnrecognised = "unrecognised source"
)

// pkgSourceLabel classifies the download URL that actually supplied the
// tollgate-wrt bytes. url == "" (nothing supplied the package) returns "".
func pkgSourceLabel(arch, ext, url string) string {
	if url == "" {
		return ""
	}
	if url == feedAssetURL(arch, ext) {
		return pkgSourceFeedRelease
	}
	if fb := githubFallbackURL(arch, ext); fb != "" && url == fb {
		return pkgSourceGitHubRelease
	}
	return pkgSourceUnrecognised
}

// ─── Installed build identification ─────────────────────────────────────
//
// "Which build am I running?" must be answerable after a deploy: an installer
// run that exercised the feed build has to be distinguishable from one that
// silently landed the GitHub-release fallback. After a successful install the
// wizard reads the version back off the router and reports it.
//
// Readback ladder (first command that yields an identifiable version wins):
//
//  1. `tollgate version --json` — the installed CLI (src/cli/version.go in
//     tollgate-module-basic-go) reports {version, commit, build_time,
//     go_version, openwrt_version}. The commit is what tells two main-tip
//     builds sharing a version apart.
//  2. `tollgate version`        — same fields, human-readable multi-line form.
//  3. `opkg list-installed`     — package metadata (opkg backends, <= 24.x).
//  4. `apk info -v`             — package metadata (apk-tools, 25.x+).
//  5. `apk list --installed`    — apk-tools 3 spelling of the same query.
//  6. `opkg status`             — control-block form, last metadata resort.
//
// Every rung is optional: an older backend (v0.5.0) or an image without the CLI
// on PATH yields "" and the caller reports the version as unknown instead of
// failing the deploy.
var (
	// versionJSONRe / commitJSONRe read the `tollgate version --json` payload.
	versionJSONRe = regexp.MustCompile(`"version"[ 	]*:[ 	]*"([^"]+)"`)
	commitJSONRe  = regexp.MustCompile(`"commit"[ 	]*:[ 	]*"([^"]+)"`)
	// versionLineRe / commitLineRe read the human-readable payload.
	versionLineRe = regexp.MustCompile(`(?m)^version:[ 	]*(\S+)`)
	commitLineRe  = regexp.MustCompile(`(?m)^commit:[ 	]*(\S+)`)
	// installedPkgVersionRe reads package-manager output:
	//   opkg: "tollgate-wrt - 0.6.0_alpha2_pre3-r1"  (or "tollgate-wrt - v0.5.0"
	//         for the legacy GitHub-release asset)
	//   apk : "tollgate-wrt-0.6.0_alpha2_pre3-r1"
	installedPkgVersionRe = regexp.MustCompile(`(?m)tollgate-wrt[ 	]*-[ 	]*(v?[0-9][A-Za-z0-9._~+-]*)`)
	// installedPkgStatusRe reads an opkg control/status block.
	installedPkgStatusRe = regexp.MustCompile(`(?m)^Package:[ 	]*tollgate-wrt[ 	]*\r?\nVersion:[ 	]*(\S+)`)
)

// tollgateBuildProbes is the readback ladder, in order. `2>/dev/null` keeps a
// missing command or binary from adding noise on older backends; `; true`
// keeps the exit status clean so sshRun always returns output we can parse.
var tollgateBuildProbes = []string{
	"tollgate version --json 2>/dev/null; true",
	"tollgate version 2>/dev/null; true",
	"opkg list-installed tollgate-wrt 2>/dev/null; true",
	"apk info -v tollgate-wrt 2>/dev/null; true",
	"apk list --installed tollgate-wrt 2>/dev/null; true",
	"opkg status tollgate-wrt 2>/dev/null; true",
}

// identifyInstalledTollgateBuild is the pure core of the readback: it walks the
// ladder with the injected command runner and returns a short identification
// ("v0.6.0-alpha2-g373770a (commit 373770a)" from the CLI on the current
// main-tip build, or "0.6.0_alpha2_pre3-r1" from package metadata), or "" when
// no rung yields a version. Testable without SSH.
func identifyInstalledTollgateBuild(get func(cmd string) string) string {
	if get == nil {
		return ""
	}
	for _, cmd := range tollgateBuildProbes {
		out := get(cmd)
		if out == "" {
			continue
		}
		if id := parseInstalledTollgateBuild(out); id != "" {
			return id
		}
	}
	return ""
}

// readInstalledTollgateBuild is the SSH-backed wrapper around
// identifyInstalledTollgateBuild. Returns "" (never an error) when the build
// cannot be identified — reporting an unknown build must not break a deploy.
func readInstalledTollgateBuild(client *ssh.Client) string {
	if client == nil {
		return ""
	}
	return identifyInstalledTollgateBuild(func(cmd string) string {
		return sshRun(client, cmd)
	})
}

// parseInstalledTollgateBuild extracts a build identification from the output
// of any readback rung, or "" if the output carries no version. It only reads;
// it never invents a version.
func parseInstalledTollgateBuild(out string) string {
	// 1 + 2. `tollgate version` output (JSON first, then human-readable).
	if m := versionJSONRe.FindStringSubmatch(out); m != nil {
		if ver := strings.TrimSpace(m[1]); ver != "" {
			return withCommit(ver, commitJSONRe.FindStringSubmatch(out))
		}
	}
	if m := versionLineRe.FindStringSubmatch(out); m != nil {
		if ver := strings.TrimSpace(m[1]); ver != "" {
			return withCommit(ver, commitLineRe.FindStringSubmatch(out))
		}
	}
	// 3-5. package-metadata output.
	if m := installedPkgVersionRe.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := installedPkgStatusRe.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// withCommit appends the build commit to a version string when the backend
// reported one that carries information ("unknown"/"dev"/"" add nothing).
func withCommit(version string, commitMatch []string) string {
	if len(commitMatch) < 2 {
		return version
	}
	commit := shortCommit(commitMatch[1])
	if commit == "" {
		return version
	}
	return version + " (commit " + commit + ")"
}

// shortCommit normalises a build commit: the module's packaging injects the
// real hash, while a plain `go build` leaves the "unknown"/"dev" placeholder
// (src/cli/version.go) — placeholders are dropped rather than reported as a
// build identity. Long hashes are shortened to 12 chars.
func shortCommit(c string) string {
	c = strings.TrimSpace(c)
	c = strings.TrimSuffix(c, "-dirty")
	if c == "" || c == "unknown" || c == "dev" || c == "none" {
		return ""
	}
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// reportInstalledBuild reads the installed tollgate-wrt build off the router,
// writes it to the job log, and returns it ("" when the router cannot report
// one). Best-effort by design: a backend too old to answer must not fail the
// deploy, so an unknown build is logged as unknown, never treated as an error.
func reportInstalledBuild(job *Job, client *ssh.Client) string {
	build := readInstalledTollgateBuild(client)
	if build != "" {
		job.addLog("Installed tollgate-wrt build: " + build)
	} else {
		job.addLog("Installed tollgate-wrt build: unknown (router reported no version — older backend, or no package DB)")
	}
	return build
}

// installStepDetail renders the install step's detail line: the identified
// build when the router reported one, the package manager used, and the source
// that supplied the package. Pure formatting — the caller passes "" for a
// build the router could not report and/or an empty source.
func installStepDetail(build, pkgMgr, source string) string {
	detail := "tollgate-wrt"
	if build != "" {
		detail += " " + build
	}
	detail += " installed via " + pkgMgr
	if source != "" {
		detail += " from " + source
	}
	return detail
}

// ── post-install verification ───────────────────────────────────────────────
// These exist because "the binary exists" is NOT proof the upgrade happened: a
// failed apk install leaves the OLD binary in place and the step passed anyway
// (2026-09-17 MT3000 field report: pre4 was reported installed while the router
// still served the pre3 portal). Verify the apk output AND the package version.

// apkInstallFailed reports whether apk's output indicates the install/upgrade
// did not happen. apk prints errors to stdout while the shell pipeline
// (`... | tail -5`) masks the exit status. Signatures are deliberately narrow
// to avoid failing on benign warnings.
func apkInstallFailed(out string) bool {
	low := strings.ToLower(out)
	for _, sig := range []string{
		"unable to select packages",
		"transaction failed",
		"conflicting dependencies",
		"failed to install",
	} {
		if strings.Contains(low, sig) {
			return true
		}
	}
	return false
}

// parsePkgVersionFromApkDB extracts the installed version of name from the
// contents of /lib/apk/db/installed (apk-tools 3), whose per-package stanza is
// "P:<name>\nV:<version>". Returns "" when the package is absent.
func parsePkgVersionFromApkDB(db, name string) string {
	want := "P:" + name
	for _, stanza := range strings.Split(db, "\n\n") {
		lines := strings.Split(stanza, "\n")
		if len(lines) == 0 || strings.TrimSpace(lines[0]) != want {
			continue
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "V:") {
				return strings.TrimSpace(strings.TrimPrefix(l, "V:"))
			}
		}
	}
	return ""
}

// parsePkgVersionFromOpkg extracts the installed version of name from
// `opkg list-installed <name>` output ("tollgate-wrt - 0.6.0_alpha2_pre4-r1").
func parsePkgVersionFromOpkg(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && f[0] == name && f[1] == "-" {
			return f[2]
		}
	}
	return ""
}

// readInstalledPkgVersion reads the installed tollgate-wrt PACKAGE version off
// the router (apk on 25+, opkg on <=24.10). Unlike the CLI's own version
// (readInstalledTollgateBuild — the source tag, identical across preN), this
// distinguishes feed releases and is what proves an upgrade took effect.
// Best-effort: "" when the router cannot report one.
func readInstalledPkgVersion(client *ssh.Client) string {
	if client == nil {
		return ""
	}
	if v := parsePkgVersionFromApkDB(
		sshRun(client, "cat /lib/apk/db/installed 2>/dev/null"),
		tollgatePackage,
	); v != "" {
		return v
	}
	return parsePkgVersionFromOpkg(
		sshRun(client, "opkg list-installed "+tollgatePackage+" 2>/dev/null"),
		tollgatePackage,
	)
}

// portalMissingAssets extracts the /assets paths reported missing by the step-8
// portal probe (marker "MISSING_ASSETS:<a> <b> ..."). Returns nil when the
// marker is absent (i.e. nothing known missing).
func portalMissingAssets(out string) []string {
	const marker = "MISSING_ASSETS:"
	i := strings.Index(out, marker)
	if i < 0 {
		return nil
	}
	rest := strings.TrimSpace(out[i+len(marker):])
	if rest == "" {
		return nil
	}
	return strings.Fields(rest)
}
