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
