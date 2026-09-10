package main

import (
	"encoding/json"
	"fmt"
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
// and lets TestArchAssetsMatchDetectedArch assert every tollgateArchAssets key
// is a selectable tuple. Add a tuple here only when it is a real OpenWrt feed
// target; an unknown bare name maps to "" (never a guessed default).
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

// tollgateArchAssets is the per-arch map of tollgate-wrt download assets, keyed
// by the canonical OpenWrt arch tuple. IPK is used on OpenWrt <=24.x (opkg), APK
// on OpenWrt 25+ (apk).
//
// Phase 3 (feat/feed-per-arch-urls): the PRIMARY source for every known arch is
// the FreedomTechFeed/packages release assets — the feed publishes all four
// canonical tuples (aarch64_cortex-a53, mipsel_24kc, mips_24kc, x86_64) in both
// .apk and .ipk at deterministic stable URLs (see the feed's
// docs/per-arch-release-assets.md). The GitHub tollgate-module-basic-go release
// URLs are preserved as a FALLBACK in tollgateGithubFallback for arches the feed
// does not publish yet (or a feed outage). selectPkgURL consults the feed first
// and falls back to GitHub only when the feed has no asset for the requested
// arch/format.
var tollgateArchAssets = map[string]struct{ IPK, APK string }{
	// Feed-published per-arch assets (FreedomTechFeed/packages release v0.6.0-alpha1).
	"aarch64_cortex-a53": {
		IPK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.ipk",
		APK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.apk",
	},
	"mipsel_24kc": {
		IPK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_mipsel_24kc.ipk",
		APK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_mipsel_24kc.apk",
	},
	"mips_24kc": {
		IPK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_mips_24kc.ipk",
		APK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_mips_24kc.apk",
	},
	"x86_64": {
		IPK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_x86_64.ipk",
		APK: "https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_x86_64.apk",
	},
}

// tollgateGithubFallback is the GitHub tollgate-module-basic-go release assets,
// kept as a FALLBACK for arches the feed does not publish yet (or a feed
// outage). Only aarch64_cortex-a53 has published GitHub release assets today.
// selectPkgURL consults this map only when tollgateArchAssets has no asset for
// the requested arch/format.
var tollgateGithubFallback = map[string]struct{ IPK, APK string }{
	"aarch64_cortex-a53": {
		IPK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.ipk",
		APK: "https://github.com/OpenTollGate/tollgate-module-basic-go/releases/download/v0.5.0/tollgate-wrt_v0.5.0_aarch64_cortex-a53.apk",
	},
}

// selectPkgURL returns the tollgate-wrt download URL and file extension for the
// given canonical arch tuple and package manager. pkgMgr must be "opkg" or
// "apk".
//
// It returns ok=false when the arch is unknown OR neither the feed nor the
// GitHub fallback has a published asset in the requested format. The caller
// must treat ok=false as a hard deploy failure — NEVER silently substitute the
// aarch64 fallback.
func selectPkgURL(arch, pkgMgr string) (url, ext string, ok bool) {
	// PRIMARY: the feed's per-arch stable URL.
	if asset, exists := tollgateArchAssets[arch]; exists {
		if pkgMgr == "apk" && asset.APK != "" {
			return asset.APK, ".apk", true
		}
		if pkgMgr == "opkg" && asset.IPK != "" {
			return asset.IPK, ".ipk", true
		}
	}
	// FALLBACK: GitHub release assets for arches the feed does not publish yet.
	if asset, exists := tollgateGithubFallback[arch]; exists {
		if pkgMgr == "apk" && asset.APK != "" {
			return asset.APK, ".apk", true
		}
		if pkgMgr == "opkg" && asset.IPK != "" {
			return asset.IPK, ".ipk", true
		}
	}
	return "", "", false
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
