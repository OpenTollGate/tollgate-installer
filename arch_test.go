package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestNormalizeBareArch verifies normalizeBareArch maps bare CPU arch strings
// (as reported by `apk --print-arch` or `uname -m`) to canonical OpenWrt arch
// tuples, and passes canonical tuples through unchanged. A bare "aarch64" must
// become "aarch64_cortex-a53" — this is the exact bug the wizard previously
// papered over by hardcoding aarch64_cortex-a53 in every download URL.
func TestNormalizeBareArch(t *testing.T) {
	cases := map[string]string{
		// bare names from apk --print-arch / uname -m
		"aarch64": "aarch64_cortex-a53",
		"mipsel":  "mipsel_24kc",
		"mips":    "mips_24kc",
		"x86_64":  "x86_64",
		"amd64":   "x86_64",
		// whitespace + case are tolerated
		"  AARCH64  ": "aarch64_cortex-a53",
		"Mipsel":      "mipsel_24kc",
		// canonical tuples pass through unchanged
		"aarch64_cortex-a53": "aarch64_cortex-a53",
		"mipsel_24kc":        "mipsel_24kc",
		"mips_24kc":          "mips_24kc",
		// unknowns normalize to ""
		"":        "",
		"sparc":   "",
		"armv7l":  "",
		"riscv64": "",
	}

	for in, want := range cases {
		if got := normalizeBareArch(in); got != want {
			t.Errorf("normalizeBareArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSelectPkgURL verifies selectPkgURL returns the correct download URL and
// extension for the requested package manager, and FAILS (ok=false) for an
// empty/undetectable arch. The URL is DERIVED generically from the tuple via
// feedAssetURL, so ANY arch the feed publishes resolves — no hardcoded map.
func TestSelectPkgURL(t *testing.T) {
	// Every known arch resolves to a FEED URL (FreedomTechFeed/packages),
	// derived from the tuple — including arches NOT in the original 4-tuple map.
	for _, arch := range []string{"aarch64_cortex-a53", "mipsel_24kc", "mips_24kc", "x86_64", "arm_cortex-a7", "riscv64"} {
		if url, ext, ok := selectPkgURL(arch, "opkg"); !ok {
			t.Errorf("selectPkgURL(%q, opkg): ok=%v, want true", arch, ok)
		} else if ext != ".ipk" {
			t.Errorf("selectPkgURL(%q, opkg): ext=%q, want .ipk", arch, ext)
		} else if url != feedAssetURL(arch, ".ipk") {
			t.Errorf("selectPkgURL(%q, opkg): url=%q, want %q", arch, url, feedAssetURL(arch, ".ipk"))
		}

		if url, ext, ok := selectPkgURL(arch, "apk"); !ok {
			t.Errorf("selectPkgURL(%q, apk): ok=%v, want true", arch, ok)
		} else if ext != ".apk" {
			t.Errorf("selectPkgURL(%q, apk): ext=%q, want .apk", arch, ext)
		} else if url != feedAssetURL(arch, ".apk") {
			t.Errorf("selectPkgURL(%q, apk): url=%q, want %q", arch, url, feedAssetURL(arch, ".apk"))
		}
	}

	// Empty/undetectable arch must fail.
	if _, _, ok := selectPkgURL("", "opkg"); ok {
		t.Errorf("selectPkgURL(\"\", opkg) = ok=true, want false (undetectable arch)")
	}
}

// TestFeedAssetURL verifies the generic feed URL builder produces the exact
// deterministic URL for a canonical tuple, and that it is generic — the tuple
// is interpolated, not looked up.
func TestFeedAssetURL(t *testing.T) {
	cases := map[string]struct{ ext, want string }{
		"aarch64_cortex-a53": {".ipk", "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.ipk"},
		"mipsel_24kc":        {".ipk", "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_mipsel_24kc.ipk"},
		"x86_64":             {".apk", "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_x86_64.apk"},
		"arm_cortex-a7":      {".ipk", "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_arm_cortex-a7.ipk"},
	}
	for arch, tc := range cases {
		if got := feedAssetURL(arch, tc.ext); got != tc.want {
			t.Errorf("feedAssetURL(%q, %q) = %q, want %q", arch, tc.ext, got, tc.want)
		}
	}
}

// TestPkgCandidateURLs verifies the ordered download candidates: the generic
// feed URL first, then the GitHub release fallback for aarch64 (the only arch
// with published GitHub assets). Non-aarch64 arches get only the feed URL.
func TestPkgCandidateURLs(t *testing.T) {
	// aarch64: feed first, then GitHub fallback.
	got := pkgCandidateURLs("aarch64_cortex-a53", ".ipk")
	want := []string{
		feedAssetURL("aarch64_cortex-a53", ".ipk"),
		tollgateGithubFallback["aarch64_cortex-a53"].IPK,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pkgCandidateURLs(aarch64, .ipk) = %v, want %v", got, want)
	}

	// Non-aarch64: feed only (no GitHub fallback exists).
	for _, arch := range []string{"mipsel_24kc", "mips_24kc", "x86_64", "arm_cortex-a7"} {
		got := pkgCandidateURLs(arch, ".ipk")
		if len(got) != 1 || got[0] != feedAssetURL(arch, ".ipk") {
			t.Errorf("pkgCandidateURLs(%q, .ipk) = %v, want [%q]", arch, got, feedAssetURL(arch, ".ipk"))
		}
	}
}

// TestArchAssetsMatchDetectedArch ties arch detection to asset selection:
// every canonical tuple that normalizeBareArch can produce must resolve to a
// feed URL via selectPkgURL (the generic builder), and the GitHub fallback
// must carry the aarch64_cortex-a53 assets.
func TestArchAssetsMatchDetectedArch(t *testing.T) {
	// Every canonical tuple must resolve to a feed URL (both formats).
	for _, arch := range []string{"aarch64_cortex-a53", "mipsel_24kc", "mips_24kc", "x86_64"} {
		ipk, _, ok := selectPkgURL(arch, "opkg")
		if !ok || !strings.HasPrefix(ipk, "https://github.com/FreedomTechFeed/packages/") || !strings.HasSuffix(ipk, ".ipk") {
			t.Errorf("%s .ipk feed URL missing or malformed: %q", arch, ipk)
		}
		if !strings.Contains(ipk, "tollgate-wrt") {
			t.Errorf("%s .ipk must reference tollgate-wrt: %q", arch, ipk)
		}
		apk, _, ok := selectPkgURL(arch, "apk")
		if !ok || !strings.HasPrefix(apk, "https://github.com/FreedomTechFeed/packages/") || !strings.HasSuffix(apk, ".apk") {
			t.Errorf("%s .apk feed URL missing or malformed: %q", arch, apk)
		}
		if !strings.Contains(apk, "tollgate-wrt") {
			t.Errorf("%s .apk must reference tollgate-wrt: %q", arch, apk)
		}
	}

	// The GitHub fallback must carry the aarch64 assets (both formats).
	gh := tollgateGithubFallback["aarch64_cortex-a53"]
	if gh.IPK == "" || !strings.HasPrefix(gh.IPK, "https://github.com/") || !strings.HasSuffix(gh.IPK, ".ipk") {
		t.Errorf("aarch64_cortex-a53 GitHub .ipk fallback missing or malformed: %q", gh.IPK)
	}
	if gh.APK == "" || !strings.HasPrefix(gh.APK, "https://github.com/") || !strings.HasSuffix(gh.APK, ".apk") {
		t.Errorf("aarch64_cortex-a53 GitHub .apk fallback missing or malformed: %q", gh.APK)
	}
}

// TestDetectArchPrecedence drills the precedence ladder of detectArchFrom:
// DISTRIB_ARCH wins; then opkg print-architecture; then ubus board; then the
// bare apk --print-arch (normalized); then uname -m (normalized, last resort).
// It also verifies a router that answers nothing yields "" (no hardcoded
// default ever).
func TestDetectArchPrecedence(t *testing.T) {
	cases := []struct {
		name string
		// outputs maps the command prefix to the output it produces.
		outputs map[string]string
		want    string
	}{
		{
			name: "distrib_arch authoritative",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "DISTRIB_ID='OpenWrt'\nDISTRIB_ARCH=\"aarch64_cortex-a53\"\n",
				"opkg print-architecture":                "arch all 1\narch noarch 10\narch aarch64_cortex-a53 100\n",
			},
			want: "aarch64_cortex-a53",
		},
		{
			name: "opkg print-architecture first arch line",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "", // no DISTRIB_ARCH key
				"opkg print-architecture":                "arch all 1\narch noarch 10\narch mipsel_24kc 100\n",
			},
			want: "mipsel_24kc",
		},
		{
			name: "ubus board architecture json",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "", // empty
				"opkg print-architecture":                "",
				"ubus call system board":                 `{"architecture":"mips_24kc","board_name":"bananapi"}`,
			},
			want: "mips_24kc",
		},
		{
			name: "ubus board bare arch normalized",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "",
				"opkg print-architecture":                "",
				"ubus call system board":                 `{"architecture":"aarch64"}`,
			},
			want: "aarch64_cortex-a53",
		},
		{
			name: "bare apk arch normalized",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "",
				"opkg print-architecture":                "",
				"ubus call system board":                 "",
				"apk --print-arch":                       "aarch64\n",
			},
			want: "aarch64_cortex-a53",
		},
		{
			name: "uname -m coarse last resort normalized",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "",
				"opkg print-architecture":                "",
				"ubus call system board":                 "",
				"apk --print-arch":                       "", // apk absent on OpenWrt 24.x
				"uname -m":                               "mips\n",
			},
			want: "mips_24kc",
		},
		{
			name: "nothing detected returns empty",
			outputs: map[string]string{
				"grep DISTRIB_ARCH /etc/openwrt_release": "",
				"opkg print-architecture":                "",
				"ubus call system board":                 "",
				"apk --print-arch":                       "",
				"uname -m":                               "",
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			get := func(cmd string) string {
				for prefix, out := range tc.outputs {
					if strings.HasPrefix(cmd, prefix) {
						return out
					}
				}
				return ""
			}
			if got := detectArchFrom(get); got != tc.want {
				t.Errorf("detectArchFrom = %q, want %q", got, tc.want)
			}
		})
	}

	// A nil runner must yield "" too (defensive — detectArch guards this).
	if got := detectArchFrom(nil); got != "" {
		t.Errorf("detectArchFrom(nil) = %q, want \"\"", got)
	}

	// detectArch with a nil client must also yield "" (the SSH wrapper's
	// defensive nil guard).
	if got := detectArch(nil); got != "" {
		t.Errorf("detectArch(nil) = %q, want \"\"", got)
	}
}

// TestDownloadBaseURL verifies the nodogsplash/jq package-repository base URL
// is threaded with the router's detected arch tuple, so the packages resolve
// for ANY router — not just aarch64_cortex-a53.
func TestDownloadBaseURL(t *testing.T) {
	cases := map[string]string{
		"aarch64_cortex-a53": "https://downloads.openwrt.org/releases/24.10.4/packages/aarch64_cortex-a53/",
		"mipsel_24kc":        "https://downloads.openwrt.org/releases/24.10.4/packages/mipsel_24kc/",
		"mips_24kc":          "https://downloads.openwrt.org/releases/24.10.4/packages/mips_24kc/",
		"x86_64":             "https://downloads.openwrt.org/releases/24.10.4/packages/x86_64/",
	}
	for in, want := range cases {
		if got := downloadBaseURL(in); got != want {
			t.Errorf("downloadBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestArchAssetsAreLive is the live HTTP 200 guard for the tollgate-wrt
// assets the deploy may download — the generic feed URLs for every canonical
// tuple (via feedAssetURL) AND the GitHub fallback (tollgateGithubFallback).
// Every asset a fresh deploy might download (feed primary or GitHub fallback)
// is probed with a 1-byte Range request. Run with -short to skip network
// access; the CI-parity command is plain `go test ./...`.
func TestArchAssetsAreLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live URL check skipped: -short mode (CI-parity runs without -short)")
	}

	client := &http.Client{Timeout: 30 * time.Second}

	probe := func(source, url string) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("building request for %s %q: %v", source, url, err)
		}
		req.Header.Set("Range", "bytes=0-0") // fetch 1 byte, not the asset
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("%s = %q: request failed: %v", source, url, err)
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			t.Errorf("%s = %q: got HTTP %d, want 200 — broken asset, fresh deploys will fail",
				source, url, resp.StatusCode)
		}
	}

	// Feed URLs for every canonical tuple the feed publishes (both formats).
	for _, arch := range []string{"aarch64_cortex-a53", "mipsel_24kc", "mips_24kc", "x86_64"} {
		for name, ext := range map[string]string{"IPK": ".ipk", "APK": ".apk"} {
			url := feedAssetURL(arch, ext)
			t.Run("feed_"+arch+"_"+name, func(t *testing.T) {
				probe("feed "+arch+" "+name, url)
			})
		}
	}
	// GitHub fallback assets.
	for arch, asset := range tollgateGithubFallback {
		for name, url := range map[string]string{"IPK": asset.IPK, "APK": asset.APK} {
			if url == "" {
				continue
			}
			t.Run("fallback_"+arch+"_"+name, func(t *testing.T) {
				probe("gh-fallback "+arch+" "+name, url)
			})
		}
	}
}
