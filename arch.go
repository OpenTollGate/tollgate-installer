package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

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
	feedReleaseTagDefault = "v0.6.0-alpha2-pre4"
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
)

// feedReleaseTagRe matches a plausible GitHub release tag (v0.6.0-alpha2-pre3,
// v0.6.0-alpha1, 0.5.0). It is deliberately permissive about WHICH tag — the
// feed is the authority on what it published, and
// TestPinnedFeedReleaseTagExists fails if the selected tag does not exist —
// but strict about the SHAPE, so a typo, a stray space, or a shell fragment
// can never be interpolated into a download URL.
var feedReleaseTagRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// feedReleaseTag is the effective release tag: feedReleaseTagDefault, or the
// TOLLGATE_FEED_RELEASE_TAG override when it is set to a plausible tag. It is
// resolved once at startup (the PreStage pins in deploy.go are derived from it
// at init time), so set the override before starting the wizard, not per
// deploy.
var feedReleaseTag = resolveFeedReleaseTag(os.Getenv)

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
// pkgCandidateURLs consults this map only to append a second download attempt
// after the feed URL.
var tollgateGithubFallback = map[string]struct{ IPK, APK string }{
	"aarch64_cortex-a53": {
		IPK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk",
		APK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.apk",
	},
}

// githubFallbackURL returns the GitHub release fallback URL for a canonical
// arch tuple and package extension, or "" if none exists. Only
// aarch64_cortex-a53 has published GitHub release assets today; used as a
// second download attempt when the feed URL 404s (feed outage).
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

// pkgArchTupleRe matches a plausible canonical OpenWrt arch tuple: at least one
// alphanumeric, then letters/digits/underscore/hyphen (e.g. aarch64_cortex-a53,
// mipsel_24kc, arm_cortex-a7). It is deliberately permissive about WHICH tuple
// (the feed is the authority on what it publishes) but strict about the SHAPE,
// so an unmapped, empty, or whitespace/injection-bearing arch can never be
// interpolated into a download URL.
var pkgArchTupleRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// pkgCandidateURLs returns the ordered download URLs to try for a canonical
// arch tuple and package extension: the feed URL (derived generically from the
// tuple) first, then the GitHub release fallback (aarch64 only) if one exists.
// The first URL that yields bytes wins.
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
	urls := []string{feedAssetURL(arch, ext)}
	if fb := githubFallbackURL(arch, ext); fb != "" && fb != urls[0] {
		urls = append(urls, fb)
	}
	return urls
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
