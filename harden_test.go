package main

import (
	"strings"
	"testing"
)

// TestApkInstallFailed pins the signatures that mean "the upgrade did NOT
// happen". This is what stops a failed apk install from passing as success just
// because the OLD binary (and its OLD portal) is still on disk.
func TestApkInstallFailed(t *testing.T) {
	bad := []string{
		"ERROR: unable to select packages:\n  nodogsplash (no such package)",
		"Transaction failed: conflicting dependencies",
		"ERROR: failed to install tollgate-wrt",
		"conflicting dependencies: jq",
	}
	for _, s := range bad {
		if !apkInstallFailed(s) {
			t.Errorf("apkInstallFailed(%q) = false, want true", s)
		}
	}
	good := []string{
		"OK: 46.5 MiB in 203 packages",
		"(1/1) Installing tollgate-wrt (0.6.0_alpha2_pre4-r1)",
		"Executing tollgate-wrt-0.6.0_alpha2_pre4-r1.post-upgrade",
	}
	for _, s := range good {
		if apkInstallFailed(s) {
			t.Errorf("apkInstallFailed(%q) = true, want false", s)
		}
	}
}

// TestParsePkgVersionFromApkDB pins parsing of /lib/apk/db/installed stanzas.
func TestParsePkgVersionFromApkDB(t *testing.T) {
	db := "P:busybox\nV:1.37.0-r1\n\nP:tollgate-wrt\nV:0.6.0_alpha2_pre4-r1\nA:aarch64_cortex-a53\n\nP:jq\nV:1.8.1-r1\n"
	if got := parsePkgVersionFromApkDB(db, "tollgate-wrt"); got != "0.6.0_alpha2_pre4-r1" {
		t.Errorf("parsePkgVersionFromApkDB = %q, want 0.6.0_alpha2_pre4-r1", got)
	}
	if got := parsePkgVersionFromApkDB(db, "nope"); got != "" {
		t.Errorf("parsePkgVersionFromApkDB(absent) = %q, want empty", got)
	}
	// Substring package names must not match (tollgate-wrt vs tollgate).
	if got := parsePkgVersionFromApkDB(db, "tollgate"); got != "" {
		t.Errorf("parsePkgVersionFromApkDB(tollgate) = %q, want empty", got)
	}
}

// TestParsePkgVersionFromOpkg pins parsing of `opkg list-installed` output.
func TestParsePkgVersionFromOpkg(t *testing.T) {
	out := "jq - 1.8.1-r1\ntollgate-wrt - 0.6.0_alpha2_pre4-r1\nnodogsplash - 5.0.2-r1\n"
	if got := parsePkgVersionFromOpkg(out, "tollgate-wrt"); got != "0.6.0_alpha2_pre4-r1" {
		t.Errorf("parsePkgVersionFromOpkg = %q, want 0.6.0_alpha2_pre4-r1", got)
	}
	if got := parsePkgVersionFromOpkg(out, "missing"); got != "" {
		t.Errorf("parsePkgVersionFromOpkg(absent) = %q, want empty", got)
	}
}

// TestPortalMissingAssets pins extraction of the step-8 probe marker.
func TestPortalMissingAssets(t *testing.T) {
	got := portalMissingAssets("MISSING_ASSETS: /assets/a.js /assets/b.css")
	if len(got) != 2 || got[0] != "/assets/a.js" || got[1] != "/assets/b.css" {
		t.Errorf("portalMissingAssets = %v, want the two paths", got)
	}
	if portalMissingAssets("OK:8") != nil {
		t.Errorf("portalMissingAssets(OK) should be nil")
	}
	if portalMissingAssets("MISSING_ASSETS:") != nil {
		t.Errorf("portalMissingAssets(empty marker) should be nil")
	}
}

// TestFeedPkgVersionPrefixMatchesInstalled documents why the install step
// compares with HasPrefix: the feed PKG_VERSION has no "release" suffix while
// the installed package does ("0.6.0_alpha2_pre4" vs "0.6.0_alpha2_pre4-r1").
func TestFeedPkgVersionPrefixMatchesInstalled(t *testing.T) {
	want := feedPkgVersion() // derived from feedReleaseTagDefault (pre4)
	if want == "" {
		t.Fatal("feedPkgVersion() returned empty")
	}
	if !strings.HasPrefix("0.6.0_alpha2_pre4-r1", want) {
		t.Errorf("installed version does not start with %q", want)
	}
	if strings.HasPrefix("0.6.0_alpha2_pre3-r1", want) {
		t.Errorf("pre3 must NOT match the pre4 prefix %q", want)
	}
}
