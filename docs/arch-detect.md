# Router CPU Architecture Auto-Detection

**Branch:** `feat/auto-detect-arch`
**Status:** implemented, tested, awaiting review

## Problem

The wizard hardcoded the OpenWrt CPU architecture `aarch64_cortex-a53` in
every package download URL:

- `tollgatePkgURL` / `tollgatePkgURLApk` constants in `deploy.go`
  (tollgate-wrt `.ipk`/`.apk` release assets)
- the nodogsplash + jq `baseURL` on the OpenWrt package repository
  (`downloads.openwrt.org/.../packages/aarch64_cortex-a53/`)

This is only correct for GL-MT3000-class ARM64 routers. On any other target
the wrong binary simply does not execute — a `mipsel_24kc` binary will not run
on ARM. That silent failure is the bug this change removes.

## Approach

`deploy.go` now calls `detectArch(client)` once, at the start of step 4, and
every download URL is derived from the detected architecture.

### Precedence ladder (`detectArch`)

The architecture is resolved in this order — first non-empty match wins:

| # | Source | Command | Output shape | Notes |
|---|--------|---------|--------------|-------|
| 1 | `/etc/openwrt_release` | `grep DISTRIB_ARCH` | exact tuple, e.g. `aarch64_cortex-a53` | Authoritative; shipped by every OpenWrt build. |
| 2 | `opkg print-architecture` | — | `arch <name> <pri>` lines | Pseudo-archs `all` / `noarch` are skipped; first real tuple wins. |
| 3 | `ubus call system board` | — | JSON `{"architecture": "..."}` | Rarely present on stock OpenWrt; value normalized via `normalizeBareArch`. |
| 4 | `apk --print-arch` | — | bare, e.g. `aarch64` | Normalized via `normalizeBareArch`. |
| 5 | `uname -m` | — | bare, e.g. `mips`, `x86_64` | Coarse, last resort; always normalized. |

If every source yields nothing, `detectArch` returns `""` and step 4 **fails
loudly**:

```
Could not determine router CPU architecture
```

There is **no hardcoded default**. A wrong architecture guess is the exact bug
being removed, so an undetectable router stops deployment rather than
downloading an unexecutable binary.

### `normalizeBareArch`

Maps a bare CPU name (from `apk --print-arch` or `uname -m`) to the canonical
OpenWrt arch tuple that appears in package-feed paths and release asset names.
The critical mapping:

| Bare | Tuple |
|------|-------|
| `aarch64` / `arm64` | `aarch64_cortex-a53` |
| `mipsel` | `mipsel_24kc` |
| `mips` | `mips_24kc` |
| `x86_64` / `amd64` | `x86_64` |

Canonical tuples pass through unchanged; unknown names return `""` (never a
guessed default).

> **Why the map matters:** `apk --print-arch` on a GL-MT3000 returns the bare
> `aarch64`, NOT `aarch64_cortex-a53`. A naive implementation that used the
> raw value would produce a URL pointing at a nonexistent package directory.

### Per-arch asset selection (`tollgateArchAssets` + `selectPkgURL`)

The two hardcoded URL constants became a map keyed by canonical tuple:

```go
var tollgateArchAssets = map[string]struct{ IPK, APK string }{
    "aarch64_cortex-a53": {
        IPK: "…/v0.7.0-alpha10/tollgate-wrt_…_aarch64_cortex-a53.ipk",
        APK: "…/v0.6.1-post-merge/tollgate-wrt_main.56…_aarch64_cortex-a53.apk",
    },
    "mipsel_24kc": {},
    "mips_24kc":   {},
    "x86_64":      {},
}
```

- Only `aarch64_cortex-a53` has published release assets today; the other
  entries record the naming convention for future releases.
- `selectPkgURL(arch, pkgMgr)` returns the URL + extension (`.ipk` / `.apk`),
  or `ok=false` when the arch is unknown OR has no published asset in the
  requested format. The caller treats `ok=false` as `"Unsupported CPU arch
  <arch>"` — it never substitutes the aarch64 fallback.
- The nodogsplash/jq base URL becomes
  `downloads.openwrt.org/releases/24.10.4/packages/<arch>/`, where `<arch>` is
  the detected tuple.

### Pin registry

`pins_test.go` (`TestDeployGoPinRegistry`) auto-discovers `*URL` string
constants in `deploy.go` and requires live HTTP-200 coverage. Since the two
tollgate URLs moved from constants into the `tollgateArchAssets` map
(arch.go), the registry was updated to track only `configwizURL`; the tollgate
assets are live-checked by `TestArchAssetsAreLive` in `arch_test.go` instead.

## Tests

- `TestNormalizeBareArch` — bare→tuple mapping, case/whitespace tolerance,
  canonical passthrough, unknown→`""`.
- `TestSelectPkgURL` — correct `.ipk`/`.apk` per arch; `ok=false` for unknown
  archs and for archs with no published asset.
- `TestArchAssetsMatchDetectedArch` — every map key is a canonical tuple; the
  aarch64 fallback carries both formats.
- `TestDetectArchPrecedence` — drills the 5-source precedence ladder, including
  the pseudo-arch skip and the nothing-detected→`""` case.
- `TestArchAssetsAreLive` — live HTTP-200 check of the published tollgate
  assets (the per-arch replacement for the old `tollgatePkgURL` pin).
- `TestTollgatePkgURLPinsExistingAsset` — pins the aarch64 `.ipk` asset against
  `wantTollgatePkgURL`; updated to read from the map.

## Verification

```sh
go build ./...      # passes
go test ./...       # full suite passes (incl. live URL checks)
go test -cover ./... # coverage target ≥ 80%
```

## Future work

When the tollgate project cuts assets for other architectures, publish them on
the release and add the URL under the matching tuple in
`tollgateArchAssets` (and in `TestArchAssetsMatchDetectedArch`) — no
selection-logic changes required.
