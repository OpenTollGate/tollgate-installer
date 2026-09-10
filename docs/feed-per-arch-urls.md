# Feed per-arch tollgate-wrt URLs (Phase 3)

**Branch:** `feat/feed-per-arch-urls`
**Status:** implemented, tested, E2E-verified against bench GL-MT6000, awaiting review

## Problem

Phase 1 (`feat/auto-detect-arch`) made the wizard detect the router's CPU arch
and select a per-arch tollgate-wrt asset. But the only published assets were
the GitHub `tollgate-module-basic-go` release URLs, and only for
`aarch64_cortex-a53`. A `mipsel_24kc` or `x86_64` router could detect its arch
correctly but had no asset to download — `selectPkgURL` returned `ok=false`
and the deploy failed.

Phase 2 (`feat/feed-publish-per-arch`) made the FreedomTechFeed/packages feed
publish per-arch tollgate-wrt packages at deterministic stable URLs for all
four canonical tuples (`aarch64_cortex-a53`, `mipsel_24kc`, `mips_24kc`,
`x86_64`), in both `.apk` (OpenWrt 25+) and `.ipk` (OpenWrt <=24.x).

This change (Phase 3) points the wizard's arch→URL selection at those feed
URLs, keeping the GitHub release URLs as a fallback.

## Approach

### `tollgateArchAssets` — now the FEED source

The map is keyed by canonical OpenWrt arch tuple and now carries the
FreedomTechFeed/packages release assets for every known arch:

```go
var tollgateArchAssets = map[string]struct{ IPK, APK string }{
    "aarch64_cortex-a53": {
        IPK: "…/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.ipk",
        APK: "…/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.apk",
    },
    "mipsel_24kc": { … },
    "mips_24kc":   { … },
    "x86_64":      { … },
}
```

### `tollgateGithubFallback` — the GitHub fallback

The old GitHub `tollgate-module-basic-go` release URLs moved into a separate
map, `tollgateGithubFallback`, preserved for arches the feed does not publish
yet (or a feed outage). Only `aarch64_cortex-a53` has GitHub release assets
today.

### `selectPkgURL` — feed first, GitHub fallback

`selectPkgURL(arch, pkgMgr)` now:

1. Consults `tollgateArchAssets` (the feed) first — returns the feed URL if
   the arch/format is published.
2. Falls back to `tollgateGithubFallback` only when the feed has no asset for
   that arch/format.
3. Returns `ok=false` (hard deploy failure) when neither source has the asset.

The fail-loudly contract is unchanged: an unknown arch or an arch with no
published asset in the requested format stops the deploy — it never silently
substitutes `aarch64_cortex-a53`.

## Why feed-first

The feed is the canonical source of per-arch tollgate-wrt packages (Phase 2).
Pointing the wizard at it means:

- Every known arch resolves to a real, published binary — no more
  `ok=false` for `mipsel_24kc` / `mips_24kc` / `x86_64`.
- The bench GL-MT6000 (`aarch64_cortex-a53`) downloads from the feed, proving
  "test the feed via the installer" end-to-end.
- The GitHub release remains as a safety net, so a feed outage or a
  not-yet-published arch still has a fallback path.

## Verification

- `go test ./...` full suite green, zero failures, coverage ≥80%.
- `TestSelectPkgURLFeedPrimary` asserts every known arch resolves to a feed URL
  in both formats.
- `TestSelectPkgURLFeedFallback` asserts the GitHub fallback map is preserved.
- `TestArchAssetsAreLive` live-checks every feed URL returns HTTP 200.
- Wizard E2E against the bench GL-MT6000: deploy log shows the arch was
  DETECTED (not hardcoded) and the package came from the FEED's per-arch URL.
