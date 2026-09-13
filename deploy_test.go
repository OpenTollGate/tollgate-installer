package main

import (
	"strings"
	"testing"
)

// TestDownloadURLsPointToTollgateRepo verifies that the tollgate package
// download URL used by the wizard targets the tollgate-module-basic-go project
// in the OpenTollGate org.
//
// This guards against accidentally reverting to a stale or wrong repo, and
// documents the Endo-handover strategy: OpenTollGate is the single source of
// truth for tollgate-wrt releases.
//
// (SW4a) The nft-enforce overlay URL is gone — those rules ship inside the
// ipk — and the full set of pinned URL constants, plus a live HTTP 200 check
// for each, is enforced in pins_test.go.
func TestDownloadURLsPointToTollgateRepo(t *testing.T) {
	cases := map[string]string{
		"tollgatePkgURL": tollgatePkgURL,
	}

	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			// 1. Must not be empty.
			if url == "" {
				t.Fatalf("%s is empty", name)
			}

			// 2. Must be a GitHub URL.
			if !strings.HasPrefix(url, "https://github.com/") {
				t.Errorf("%s = %q: must be an https://github.com URL", name, url)
			}

			// 3. Must reference the feed repo (FreedomTechFeed/packages) — the
			//    per-arch tollgate-wrt source (feat/feed-per-arch-urls). The
			//    GitHub tollgate-module-basic-go release is the fallback only.
			ownerOk := strings.Contains(url, "FreedomTechFeed/")
			repoOk := strings.Contains(url, "packages")
			if !ownerOk {
				t.Errorf("%s = %q: owner must be FreedomTechFeed (feed-primary)", name, url)
			}
			if !repoOk {
				t.Errorf("%s = %q: must reference the feed packages repo", name, url)
			}

			// 4. Must be a release download URL, not e.g. a branch archive.
			if !strings.Contains(url, "/releases/download/") {
				t.Errorf("%s = %q: must be a GitHub release download URL", name, url)
			}
		})
	}
}

// TestTollgatePkgURLAssetName verifies the .ipk asset exists in the URL and
// targets the aarch64_cortex-a53 architecture the routers use. This catches
// typos introduced when bumping release tags/asset names (the v0.6.1-post-merge
// asset is named differently from the v0.5.0-e2e-test one).
func TestTollgatePkgURLAssetName(t *testing.T) {
	// Must end with the .ipk extension.
	if !strings.HasSuffix(tollgatePkgURL, ".ipk") {
		t.Errorf("tollgatePkgURL = %q: must end with .ipk", tollgatePkgURL)
	}
	// Must target the router's architecture.
	if !strings.Contains(tollgatePkgURL, "aarch64_cortex-a53") {
		t.Errorf("tollgatePkgURL = %q: must target aarch64_cortex-a53", tollgatePkgURL)
	}
	// Must reference the tollgate-wrt binary, not some other asset.
	if !strings.Contains(tollgatePkgURL, "tollgate-wrt") {
		t.Errorf("tollgatePkgURL = %q: must reference the tollgate-wrt binary", tollgatePkgURL)
	}
}

// TestPkgSourceLabel verifies the install step can state WHICH source supplied
// the tollgate-wrt package. The feed release and the GitHub release both
// install, so a wrong or missing label is exactly the "silent substitution"
// this guards against: an installer run that was supposed to exercise
// tollgate-module-basic-go + FreedomTechFeed/packages must not be reported the
// same way as one that fell back to the pinned GitHub asset.
func TestPkgSourceLabel(t *testing.T) {
	const arch = "aarch64_cortex-a53"
	feedIPK := feedAssetURL(arch, ".ipk")
	ghIPK := githubFallbackURL(arch, ".ipk")
	if ghIPK == "" {
		t.Fatal("githubFallbackURL(aarch64_cortex-a53, .ipk) is empty — the fallback pin is missing")
	}

	cases := []struct {
		name      string
		arch, ext string
		url       string
		want      string
	}{
		{"feed release (ipk)", arch, ".ipk", feedIPK, pkgSourceFeedRelease},
		{"feed release (apk)", arch, ".apk", feedAssetURL(arch, ".apk"), pkgSourceFeedRelease},
		{"pinned GitHub fallback", arch, ".ipk", ghIPK, pkgSourceGitHubRelease},
		{"github fallback apk", arch, ".apk", githubFallbackURL(arch, ".apk"), pkgSourceGitHubRelease},
		{"nothing supplied the package", arch, ".ipk", "", ""},
		// An unknown URL is reported as unrecognised, never guessed as the feed.
		{"unknown URL", arch, ".ipk", "https://example.com/tollgate-wrt.ipk", pkgSourceUnrecognised},
		// The GitHub URL is not a fallback for an arch it does not cover.
		{"github asset on an arch with no fallback", "mipsel_24kc", ".ipk", ghIPK, pkgSourceUnrecognised},
		// The feed URL of a DIFFERENT arch is not this arch's feed URL.
		{"feed URL of another arch", "mipsel_24kc", ".ipk", feedIPK, pkgSourceUnrecognised},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pkgSourceLabel(tc.arch, tc.ext, tc.url); got != tc.want {
				t.Errorf("pkgSourceLabel(%q, %q, %q) = %q, want %q", tc.arch, tc.ext, tc.url, got, tc.want)
			}
		})
	}

	// The two real sources must be distinguishable labels — collapse them and
	// the operator can no longer tell feed from fallback.
	if pkgSourceFeedRelease == pkgSourceGitHubRelease {
		t.Error("pkgSourceFeedRelease and pkgSourceGitHubRelease are the same label")
	}
	for _, l := range []string{pkgSourceFeedRelease, pkgSourceGitHubRelease, pkgSourceRouterFeed, pkgSourceUnrecognised} {
		if strings.TrimSpace(l) == "" {
			t.Error("a package-source label is empty")
		}
	}
}

// TestParseInstalledTollgateBuild covers the readback formats the router can
// actually produce. These strings are the real shapes from
// tollgate-module-basic-go's CLI (src/cli/version.go), opkg, and apk-tools.
func TestParseInstalledTollgateBuild(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// `tollgate version --json` (the version endpoint payload).
			name: "cli json",
			in:   `{"success":true,"message":"TollGate Version","data":{"version":"0.6.0-alpha1","commit":"089e876fedcba9876543210fedcba9876543210","build_time":"2026-09-09T18:12:00Z","go_version":"go1.24.0","openwrt_version":"OpenWrt 25.12.0"}}`,
			want: "0.6.0-alpha1 (commit 089e876fedcb)",
		},
		{
			name: "cli json short commit",
			in:   `{"data":{"version":"0.6.0-alpha1","commit":"089e876"}}`,
			want: "0.6.0-alpha1 (commit 089e876)",
		},
		{
			// The main-tip pre-release build: the version string carries the
			// tag-derived version PLUS "-g" and the source commit
			// (0.6.0-alpha2 + g089e876), which is the spelling the shipped
			// v0.6.0-alpha2-pre package's tollgate-wrt binary reports. Note it
			// does NOT equal the package version 0.6.0_alpha2_pre — the commit
			// is what identifies the build (see docs/package-provenance.md).
			name: "cli json main-tip pre-release build",
			in:   `{"data":{"version":"v0.6.0-alpha2-g089e876","commit":"089e876"}}`,
			want: "v0.6.0-alpha2-g089e876 (commit 089e876)",
		},
		{
			// A plain `go build` leaves the placeholder commit — the version is
			// still reported, the placeholder is not passed off as a build id.
			name: "cli json placeholder commit",
			in:   `{"data":{"version":"0.6.0-alpha1","commit":"unknown"}}`,
			want: "0.6.0-alpha1",
		},
		{
			name: "cli json dev version unknown commit",
			in:   `{"data":{"version":"dev","commit":"unknown"}}`,
			want: "dev",
		},
		{
			// `tollgate version` human-readable form (GetFormattedVersionInfo).
			name: "cli human readable",
			in: `TollGate Version
version: 0.6.0-alpha1
commit: 089e876fedcba9876543210fedcba9876543210
build_time: 2026-09-09T18:12:00Z
go_version: go1.24.0
openwrt_version: OpenWrt 25.12.0`,
			want: "0.6.0-alpha1 (commit 089e876fedcb)",
		},
		{
			name: "cli human readable placeholder commit",
			in: `TollGate Version
version: v0.5.0
commit: unknown
go_version: go1.22.0`,
			want: "v0.5.0",
		},
		{
			// opkg list-installed, feed build (0.6.0 alpha1, PKG_VERSION form).
			name: "opkg list-installed feed build",
			in:   "tollgate-wrt - 0.6.0_alpha1-r1",
			want: "0.6.0_alpha1-r1",
		},
		{
			// opkg list-installed, the CURRENT feed build (main-tip
			// pre-release v0.6.0-alpha2-pre): the same PKG_VERSION spelling
			// with the package revision the published .ipk carries. The tag's
			// hyphens are underscores here, exactly as in the asset name.
			name: "opkg list-installed current feed build",
			in:   "tollgate-wrt - 0.6.0_alpha2_pre-r1",
			want: "0.6.0_alpha2_pre-r1",
		},
		{
			// opkg list-installed, the pinned GitHub release v0.5.0 (its control
			// file carries the leading "v").
			name: "opkg list-installed v0.5.0",
			in:   "tollgate-wrt - v0.5.0",
			want: "v0.5.0",
		},
		{
			// apk -v / apk list --installed (apk-tools 3).
			name: "apk info -v",
			in:   "tollgate-wrt-0.6.0_alpha1-r1",
			want: "0.6.0_alpha1-r1",
		},
		{
			name: "apk info -v current feed build",
			in:   "tollgate-wrt-0.6.0_alpha2_pre-r1",
			want: "0.6.0_alpha2_pre-r1",
		},
		{
			name: "apk list --installed row",
			in:   "tollgate-wrt-0.6.0_alpha1-r1 aarch64_cortex-a53 {feeds} (installed)",
			want: "0.6.0_alpha1-r1",
		},
		{
			// opkg status / opkg control-block form.
			name: "opkg status control block",
			in: `Package: tollgate-wrt
Version: 0.6.0_alpha1-r1
Architecture: aarch64_cortex-a53
Depends: libc, nodogsplash, jq`,
			want: "0.6.0_alpha1-r1",
		},
		{
			// Nothing usable: no version may be invented.
			name: "empty output",
			in:   "",
			want: "",
		},
		{
			name: "unrelated output",
			in:   "ash: tollgate: not found",
			want: "",
		},
		{
			name: "package name without a version",
			in:   "tollgate-wrt",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseInstalledTollgateBuild(tc.in); got != tc.want {
				t.Errorf("parseInstalledTollgateBuild(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIdentifyInstalledTollgateBuildLadder verifies the readback ladder: the
// first rung that yields an identifiable version wins, and a backend that
// cannot answer any rung degrades to "" instead of failing (AGENTS.md: the
// wizard must keep working with older backends such as v0.5.0).
func TestIdentifyInstalledTollgateBuildLadder(t *testing.T) {
	const cliJSON = `{"data":{"version":"0.6.0-alpha1","commit":"089e876fedcba9876543210"}}`

	// 1. CLI answers: used immediately, ladder stops at rung 1.
	var asked []string
	got := identifyInstalledTollgateBuild(func(cmd string) string {
		asked = append(asked, cmd)
		if strings.Contains(cmd, "tollgate version --json") {
			return cliJSON
		}
		return ""
	})
	if got != "0.6.0-alpha1 (commit 089e876fedcb)" {
		t.Errorf("ladder with CLI answering = %q, want %q", got, "0.6.0-alpha1 (commit 089e876fedcb)")
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "tollgate version --json") {
		t.Errorf("ladder probes = %v, want only the first rung when it answers", asked)
	}

	// 2. No CLI (older backend) but apk metadata answers: falls through and
	//    reports the package version rather than giving up.
	got = identifyInstalledTollgateBuild(func(cmd string) string {
		if strings.Contains(cmd, "apk info -v") {
			return "tollgate-wrt-0.6.0_alpha1-r1"
		}
		return ""
	})
	if got != "0.6.0_alpha1-r1" {
		t.Errorf("ladder with apk metadata only = %q, want %q", got, "0.6.0_alpha1-r1")
	}

	// 3. v0.5.0-style opkg backend: opkg metadata answers.
	got = identifyInstalledTollgateBuild(func(cmd string) string {
		if strings.Contains(cmd, "opkg list-installed") {
			return "tollgate-wrt - v0.5.0"
		}
		return ""
	})
	if got != "v0.5.0" {
		t.Errorf("ladder with opkg metadata only = %q, want %q", got, "v0.5.0")
	}

	// 4. Nothing answers at all: "" (deploy must not fail on this).
	got = identifyInstalledTollgateBuild(func(cmd string) string { return "" })
	if got != "" {
		t.Errorf("ladder with no output = %q, want %q", got, "")
	}

	// 5. Noise on every rung: still "" — no version is invented from junk.
	got = identifyInstalledTollgateBuild(func(cmd string) string { return "sh: not found" })
	if got != "" {
		t.Errorf("ladder with junk output = %q, want %q", got, "")
	}

	// 6. A nil runner is safe (never panics).
	if got := identifyInstalledTollgateBuild(nil); got != "" {
		t.Errorf("ladder with nil runner = %q, want %q", got, "")
	}
}

// TestInstallStepDetail pins the install step's detail line: it must name the
// installed build when the router reported one, and must name the source so
// the operator can tell a feed install from a GitHub-fallback install.
func TestInstallStepDetail(t *testing.T) {
	cases := []struct {
		name               string
		build, pkgMgr, src string
		want               string
	}{
		{
			name:  "feed build identified",
			build: "0.6.0-alpha1 (commit 089e876fedcb)", pkgMgr: "apk", src: pkgSourceFeedRelease,
			want: "tollgate-wrt 0.6.0-alpha1 (commit 089e876fedcb) installed via apk from FreedomTechFeed/packages release",
		},
		{
			name:  "github fallback identified",
			build: "v0.5.0", pkgMgr: "opkg", src: pkgSourceGitHubRelease,
			want: "tollgate-wrt v0.5.0 installed via opkg from tollgate-module-basic-go GitHub release (fallback)",
		},
		{
			name:  "older backend cannot report a version",
			build: "", pkgMgr: "opkg", src: pkgSourceFeedRelease,
			want: "tollgate-wrt installed via opkg from FreedomTechFeed/packages release",
		},
		{
			name:  "router package feed",
			build: "0.6.0_alpha1-r1", pkgMgr: "apk", src: pkgSourceRouterFeed,
			want: "tollgate-wrt 0.6.0_alpha1-r1 installed via apk from router package feed",
		},
		{
			name:  "no source known",
			build: "", pkgMgr: "opkg", src: "",
			want: "tollgate-wrt installed via opkg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := installStepDetail(tc.build, tc.pkgMgr, tc.src); got != tc.want {
				t.Errorf("installStepDetail(%q, %q, %q) = %q, want %q", tc.build, tc.pkgMgr, tc.src, got, tc.want)
			}
		})
	}
}
