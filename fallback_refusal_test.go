package main

import (
	"strings"
	"testing"
)

// C2-I-02 (pre-release security audit, 2026-09-23): "Wrong/older version can be
// installed and reported as success".
//
// Two defects, both pinned below:
//
//  1. pkgCandidateURLs offers the GitHub fallback — today a v0.5.0 asset, while
//     the requested feed release is v0.6.0-alpha2-pre9 — as a second download
//     attempt with no opt-in and no warning. The selection must instead refuse
//     unless the operator has EXPLICITLY allowed the downgrade, naming both the
//     requested tag/version and the version the fallback would install.
//  2. The post-install version assertion was gated on the SOURCE URL
//     (`strings.Contains(pkgSourceURL, "/releases/download/"+feedReleaseTag+"/")`,
//     deploy.go:645), so for the fallback URL it could not fire at all: there was
//     no comparison, no failure, and the step still rendered "done"/green. The
//     assertion must fire for EVERY source we downloaded.

const pkgFallbackTestArch = "aarch64_cortex-a53"

// TestGithubFallbackSelectionRequiresOptIn pins the fail-loudly contract: the
// fallback is NOT selectable unless explicitly allowed, and the refusal names
// what was requested AND what the fallback would install instead.
func TestGithubFallbackSelectionRequiresOptIn(t *testing.T) {
	fb := githubFallbackURL(pkgFallbackTestArch, ".ipk")
	if fb == "" {
		t.Fatalf("no GitHub fallback pinned for %s/.ipk — fixture is stale", pkgFallbackTestArch)
	}
	fbVer := githubFallbackPkgVersion(pkgFallbackTestArch, ".ipk")
	if fbVer == "" || fbVer == feedPkgVersion() {
		t.Fatalf("fixture broken: fallback version %q vs requested %q — the fallback must be a DIFFERENT release for this test to mean anything",
			fbVer, feedPkgVersion())
	}

	// NOT opted in: refuse, and name both versions.
	got, err := githubFallbackSelection(pkgFallbackTestArch, ".ipk", false)
	if err == nil {
		t.Fatalf("githubFallbackSelection(allowFallback=false) = (%q, nil), want an error refusing the silent downgrade", got)
	}
	if got != "" {
		t.Errorf("githubFallbackSelection(allowFallback=false) returned URL %q alongside the error; must return no URL", got)
	}
	msg := err.Error()
	for _, want := range []string{feedReleaseTag, feedPkgVersion(), fbVer} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message does not name %q:\n%s", want, msg)
		}
	}
	if !strings.Contains(msg, "allow-fallback") {
		t.Errorf("refusal message does not tell the operator how to opt in (--allow-fallback):\n%s", msg)
	}

	// Explicitly opted in: the URL is returned, no error.
	got, err = githubFallbackSelection(pkgFallbackTestArch, ".ipk", true)
	if err != nil {
		t.Fatalf("githubFallbackSelection(allowFallback=true) = %v, want the fallback URL", err)
	}
	if got != fb {
		t.Errorf("githubFallbackSelection(allowFallback=true) = %q, want %q", got, fb)
	}
}

// TestGithubFallbackSelectionWithoutFallback: an arch with no pinned fallback
// has nothing to refuse and nothing to select — ("", nil) in both modes, so the
// caller's feed-only path is unchanged.
func TestGithubFallbackSelectionWithoutFallback(t *testing.T) {
	const arch = "mipsel_24kc"
	if githubFallbackURL(arch, ".ipk") != "" {
		t.Fatalf("fixture stale: %s now has a pinned fallback", arch)
	}
	for _, allow := range []bool{false, true} {
		got, err := githubFallbackSelection(arch, ".ipk", allow)
		if got != "" || err != nil {
			t.Errorf("githubFallbackSelection(%q, allowFallback=%v) = (%q, %v), want (\"\", nil)",
				arch, allow, got, err)
		}
	}
}

// TestGithubFallbackPkgVersion pins the version spelling the fallback asset
// names, derived from the URL via feedPkgVersionForTag (the single tag→version
// mapping point) rather than from a second hardcoded literal.
func TestGithubFallbackPkgVersion(t *testing.T) {
	if got, want := githubFallbackPkgVersion(pkgFallbackTestArch, ".ipk"), "0.5.0"; got != want {
		t.Errorf("githubFallbackPkgVersion(aarch64_cortex-a53, .ipk) = %q, want %q", got, want)
	}
	if got, want := githubFallbackPkgVersion(pkgFallbackTestArch, ".apk"), "0.5.0"; got != want {
		t.Errorf("githubFallbackPkgVersion(aarch64_cortex-a53, .apk) = %q, want %q", got, want)
	}
	if got := githubFallbackPkgVersion("mipsel_24kc", ".ipk"); got != "" {
		t.Errorf("githubFallbackPkgVersion(mipsel_24kc, .ipk) = %q, want \"\"", got)
	}
	for _, tc := range []struct{ in, want string }{
		{"https://github.com/o/r/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk", "0.5.0"},
		{"https://github.com/o/r/releases/download/v0.6.0-alpha2-pre9/tollgate-wrt_0.6.0_alpha2_pre9_x86_64.apk", "0.6.0_alpha2_pre9"},
		{"https://example.com/tollgate-wrt.ipk", ""},
		{"", ""},
	} {
		if got := pkgVersionFromReleaseURL(tc.in); got != tc.want {
			t.Errorf("pkgVersionFromReleaseURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestPkgVersionVerdict pins the post-install assertion for EVERY source. The
// defect was that the comparison only ran when the source URL was the feed URL;
// a fallback install was never compared, so a downgrade passed as success.
func TestPkgVersionVerdict(t *testing.T) {
	const arch = pkgFallbackTestArch
	feedIPK := feedAssetURL(arch, ".ipk")
	fbIPK := githubFallbackURL(arch, ".ipk")
	requested := feedPkgVersion()                   // 0.6.0_alpha2_pre9
	fbVer := githubFallbackPkgVersion(arch, ".ipk") // 0.5.0
	if fbVer == "" || fbVer == requested {
		t.Fatalf("fixture broken: fallback %q vs requested %q", fbVer, requested)
	}

	cases := []struct {
		name            string
		installed       string
		sourceURL       string
		wantFatalSubstr string // "" => no fatal failure
		wantWarnSubstr  string // "" => no downgrade warning
	}{
		{
			name:      "feed source, requested version installed",
			installed: requested + "-r1", sourceURL: feedIPK,
		},
		{
			name:      "feed source, older version installed — the upgrade did not take effect",
			installed: fbVer, sourceURL: feedIPK,
			wantFatalSubstr: "did not take effect",
		},
		{
			name:      "FALLBACK source (opted in), fallback version installed — no longer silent",
			installed: fbVer, sourceURL: fbIPK,
			wantWarnSubstr: "NOT the requested release",
		},
		{
			// The readback a REAL opted-in fallback install produces: the
			// pinned v0.5.0 .ipk carries `Version: v0.5.0` in its control file
			// (deploy_test.go pins the router readback "tollgate-wrt - v0.5.0").
			// Without normalizing the leading "v" this fataled with "the
			// upgrade did not take effect" — on the exact recovery route the
			// refusal message tells the operator to re-run with.
			name:      "FALLBACK source (opted in), REAL v-prefixed readback — must not fatal",
			installed: "v" + fbVer, sourceURL: fbIPK,
			wantWarnSubstr: "NOT the requested release",
		},
		{
			name:      "fallback source, unexpected version installed — still fatal",
			installed: "0.4.0", sourceURL: fbIPK,
			wantFatalSubstr: "did not take effect",
		},
		{
			name:      "nothing was pushed (router feed path), older version installed",
			installed: fbVer, sourceURL: "",
			wantWarnSubstr: "NOT the requested release",
		},
		{
			name:      "no version read back — nothing to assert",
			installed: "", sourceURL: feedIPK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fatal, warn := pkgVersionVerdict(tc.installed, tc.sourceURL, arch, ".ipk")
			if tc.wantFatalSubstr == "" && fatal != "" {
				t.Errorf("pkgVersionVerdict(%q, %q) fatal = %q, want no fatal", tc.installed, tc.sourceURL, fatal)
			}
			if tc.wantFatalSubstr != "" {
				if fatal == "" {
					t.Fatalf("pkgVersionVerdict(%q, %q) fatal = \"\", want a failure containing %q",
						tc.installed, tc.sourceURL, tc.wantFatalSubstr)
				}
				if !strings.Contains(fatal, tc.wantFatalSubstr) {
					t.Errorf("fatal = %q, want it to contain %q", fatal, tc.wantFatalSubstr)
				}
				if !strings.Contains(fatal, tc.installed) {
					t.Errorf("fatal = %q, want it to name the installed version %q", fatal, tc.installed)
				}
			}
			if tc.wantWarnSubstr == "" && warn != "" {
				t.Errorf("pkgVersionVerdict(%q, %q) warn = %q, want no warning", tc.installed, tc.sourceURL, warn)
			}
			if tc.wantWarnSubstr != "" {
				if !strings.Contains(warn, tc.wantWarnSubstr) {
					t.Errorf("warn = %q, want it to contain %q", warn, tc.wantWarnSubstr)
				}
				if !strings.Contains(warn, requested) || !strings.Contains(warn, feedReleaseTag) {
					t.Errorf("warn = %q, want it to name the requested release %s (%s)", warn, feedReleaseTag, requested)
				}
			}
		})
	}
}

// TestPkgVersionVerdictNormalisesTheLeadingV pins that the same package reported
// in either spelling — the feed's derived `0.6.0_alpha4_pre15-r1` and the pinned
// GitHub release's `v0.5.0` — yields the SAME verdict for the source that
// supplied it. Both spellings are real (deploy_test.go pins both readbacks), and
// a spelling difference may never turn a correct install into a failure.
func TestPkgVersionVerdictNormalisesTheLeadingV(t *testing.T) {
	const arch, ext = pkgFallbackTestArch, ".ipk"
	cases := []struct {
		name  string
		src   string
		plain string // the version the source names, in the source's own spelling
	}{
		{"requested release from the feed", feedAssetURL(arch, ext), feedPkgVersion()},
		{"opted-in fallback downgrade", githubFallbackURL(arch, ext), githubFallbackPkgVersion(arch, ext)},
	}
	for _, c := range cases {
		if c.src == "" || c.plain == "" {
			t.Fatalf("%s: fixture stale (src=%q plain=%q)", c.name, c.src, c.plain)
		}
		fatalPlain, warnPlain := pkgVersionVerdict(c.plain, c.src, arch, ext)
		fatalV, warnV := pkgVersionVerdict("v"+c.plain, c.src, arch, ext)
		if fatalPlain != fatalV {
			t.Errorf("%s: the spelling changes the FATAL verdict (%q vs %q)", c.name, fatalPlain, fatalV)
		}
		// The warning TEXT quotes the readback, so it legitimately differs; what
		// must not differ is WHETHER the downgrade warning fires.
		if (warnPlain == "") != (warnV == "") {
			t.Errorf("%s: the spelling changes whether a downgrade warning fires (%q vs %q)", c.name, warnPlain, warnV)
		}
		if fatalV != "" {
			t.Errorf("%s: fatal = %q for the version its own source provides — a correct install must not fail",
				c.name, fatalV)
		}
	}
}

// TestGithubFallbackOptInPolicy pins the default: OFF. The fallback is a
// downgrade path, so nothing but an explicit opt-in may enable it.
func TestGithubFallbackOptInPolicy(t *testing.T) {
	if *allowFallback {
		t.Errorf("--allow-fallback defaults to true; the fallback must be OFF unless the operator asks for it")
	}
	t.Setenv(githubFallbackEnv, "1")
	if !githubFallbackAllowed() {
		t.Errorf("githubFallbackAllowed() = false with %s=1", githubFallbackEnv)
	}
	t.Setenv(githubFallbackEnv, "")
	if githubFallbackAllowed() {
		t.Errorf("githubFallbackAllowed() = true with %s empty and the flag off", githubFallbackEnv)
	}
}
