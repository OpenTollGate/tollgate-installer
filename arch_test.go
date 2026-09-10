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
// unknown arch or an arch with no published asset in the requested format.
func TestSelectPkgURL(t *testing.T) {
	// Every known arch resolves to a FEED URL (FreedomTechFeed/packages).
	for _, arch := range []string{"aarch64_cortex-a53", "mipsel_24kc", "mips_24kc", "x86_64"} {
		if url, ext, ok := selectPkgURL(arch, "opkg"); !ok {
			t.Errorf("selectPkgURL(%q, opkg): ok=%v, want true", arch, ok)
		} else if ext != ".ipk" {
			t.Errorf("selectPkgURL(%q, opkg): ext=%q, want .ipk", arch, ext)
		} else if url != tollgateArchAssets[arch].IPK {
			t.Errorf("selectPkgURL(%q, opkg): url=%q, want %q", arch, url, tollgateArchAssets[arch].IPK)
		}

		if url, ext, ok := selectPkgURL(arch, "apk"); !ok {
			t.Errorf("selectPkgURL(%q, apk): ok=%v, want true", arch, ok)
		} else if ext != ".apk" {
			t.Errorf("selectPkgURL(%q, apk): ext=%q, want .apk", arch, ext)
		} else if url != tollgateArchAssets[arch].APK {
			t.Errorf("selectPkgURL(%q, apk): url=%q, want %q", arch, url, tollgateArchAssets[arch].APK)
		}
	}

	// Unknown archs must fail, including the empty string.
	for _, arch := range []string{"sparc", "riscv64", ""} {
		if _, _, ok := selectPkgURL(arch, "opkg"); ok {
			t.Errorf("selectPkgURL(%q, opkg) = ok=true, want false (unknown arch)", arch)
		}
	}
}

// TestArchAssetsMatchDetectedArch ties arch detection to asset selection:
// every key in tollgateArchAssets must be a canonical tuple (so a value
// returned by detectArch's normalizeBareArch is always selectable), and every
// known arch must carry BOTH the .ipk and .apk FEED URLs (FreedomTechFeed/
// packages) for tollgate-wrt. The GitHub fallback map must also carry the
// aarch64_cortex-a53 assets.
func TestArchAssetsMatchDetectedArch(t *testing.T) {
	// Every map key must be a canonical tuple so selectPkgURL can look up any
	// arch that detectArch might return.
	for tuple := range tollgateArchAssets {
		if normalizeBareArch(tuple) != tuple {
			t.Errorf("tollgateArchAssets key %q is not a canonical tuple (normalizeBareArch(%q)=%q)", tuple, tuple, normalizeBareArch(tuple))
		}
	}

	// Every known arch must carry both feed formats.
	for _, arch := range []string{"aarch64_cortex-a53", "mipsel_24kc", "mips_24kc", "x86_64"} {
		ipk := tollgateArchAssets[arch].IPK
		apk := tollgateArchAssets[arch].APK
		if ipk == "" || !strings.HasPrefix(ipk, "https://github.com/FreedomTechFeed/packages/") || !strings.HasSuffix(ipk, ".ipk") {
			t.Errorf("%s .ipk feed URL missing or malformed: %q", arch, ipk)
		}
		if !strings.Contains(ipk, "tollgate-wrt") {
			t.Errorf("%s .ipk must reference tollgate-wrt: %q", arch, ipk)
		}
		if apk == "" || !strings.HasPrefix(apk, "https://github.com/FreedomTechFeed/packages/") || !strings.HasSuffix(apk, ".apk") {
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
// assets in tollgateArchAssets (feed) AND tollgateGithubFallback (fallback) —
// the per-arch replacement for the old tollgatePkgURL pin that was live-checked
// in pins_test.go. Every asset a fresh deploy might download (feed primary or
// GitHub fallback) is probed with a 1-byte Range request. Run with -short to
// skip network access; the CI-parity command is plain `go test ./...`.
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

	for arch, asset := range tollgateArchAssets {
		for name, url := range map[string]string{"IPK": asset.IPK, "APK": asset.APK} {
			if url == "" {
				continue // no published asset yet — not checked
			}
			t.Run("feed_"+arch+"_"+name, func(t *testing.T) {
				probe("feed "+arch+" "+name, url)
			})
		}
	}
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
