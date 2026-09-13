# Package provenance + installed build identification

**Status:** implemented, unit-tested, NOT yet run against a physical router by
this change (see "What is not verified").

## Why

The wizard prefers the **feed-built** `tollgate-wrt` package
(`FreedomTechFeed/packages`, `net/tollgate-wrt/Makefile`) over the pinned
GitHub release asset, so that an installer run is also an end-to-end test of
`tollgate-module-basic-go` **and** the feed's package build. Two things were
missing for that to be provable:

1. **No source reporting.** The install step tried the feed URL and then the
   GitHub release URL, and both produced a working install. The log showed the
   winning URL only as a download path — it never *stated* the source, so a
   feed install and a fallback install were indistinguishable afterwards.
2. **No build identification.** Nothing read the installed version back off the
   router, so "which build am I running?" had no answer.

## What this change adds

### Source labels — `pkgSourceLabel(arch, ext, url)`

| Label | Meaning |
|---|---|
| `FreedomTechFeed/packages release` | The feed-published release asset (primary) |
| `tollgate-module-basic-go GitHub release (fallback)` | The pinned GitHub release asset |
| `router package feed` | Installed from the router's own opkg/apk repositories (last resort) |
| `unrecognised source` | URL matches neither candidate — reported, never guessed |

The install step records the candidate URL **only once the bytes are confirmed
on the router**, then logs:

```
tollgate-wrt source: FreedomTechFeed/packages release — https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha1/tollgate-wrt_0.6.0_alpha1_aarch64_cortex-a53.apk
```

If neither candidate supplies the package the log says so explicitly
(`tollgate-wrt source: none ...`) instead of leaving it to inference.

### Installed build read-back

After a successful install the wizard walks this ladder (first rung that yields
a version wins) and reports it in the log and in the install step's detail:

1. `tollgate version --json` — the installed CLI
   (`src/cli/version.go`) → `version`, `commit`, `build_time`, `go_version`,
   `openwrt_version`. The **commit** is what distinguishes two main-tip builds
   that share a version.
2. `tollgate version` — same fields, human-readable form.
3. `opkg list-installed tollgate-wrt` — package metadata (opkg backends).
4. `apk info -v tollgate-wrt` — package metadata (apk-tools).
5. `apk list --installed tollgate-wrt` — apk-tools 3 spelling.
6. `opkg status tollgate-wrt` — control-block form.

Result, e.g.:

```
Installed tollgate-wrt build: 0.6.0-alpha1 (commit 089e876fedcb)
step install → "tollgate-wrt 0.6.0-alpha1 (commit 089e876fedcb) installed via apk from FreedomTechFeed/packages release"
```

Every rung is optional: an older backend (v0.5.0) or an image whose CLI is not
on `PATH` yields `unknown`, which is logged (never treated as an error). The
`git` placeholders `unknown` / `dev` that a plain `go build` injects are dropped
rather than reported as a build identity.

### Feed URL hardening

`pkgCandidateURLs` now returns **no candidates** for an arch that is not a
plausible OpenWrt tuple (empty, whitespace-padded, containing spaces or
slashes). Previously an empty arch produced a malformed
`.../tollgate-wrt_0.6.0_alpha1_.ipk` URL — a 404 that hid the real fault (arch
detection failed). The caller treats an empty candidate list as a hard failure
and never substitutes another arch's asset.

The feed release tag and package version are now named constants
(`feedReleaseTag` = `v0.6.0-alpha1`, `feedPkgVersion` = `0.6.0_alpha1`) so
`TestFeedAssetURLShapeMatchesPinnedRelease` can assert the two spellings stay in
sync — the feed's Makefile keeps `PKG_SOURCE_VERSION` (hyphen, tags the release)
and `PKG_VERSION` (underscore, apk-legal, names the asset) as the same version.

## Fallback behaviour

Feed URL first; pinned GitHub release second (aarch64 only today); then the
router-side `wget` of the same candidate list; then the router's own package
feeds. A feed outage degrades to the GitHub release, and the deploy log names
which one was used. There is no silent substitution of a different
architecture's asset.

## What is verified

- `go test ./...` green (90 top-level tests, 0 failures) — includes
  `TestPkgSourceLabel`, `TestParseInstalledTollgateBuild`,
  `TestIdentifyInstalledTollgateBuildLadder`, `TestInstallStepDetail`,
  `TestPkgCandidateURLsRejectUnmappedArch`, `TestPkgCandidateURLsAreWellFormed`,
  `TestFeedAssetURLShapeMatchesPinnedRelease`.
- `TestArchAssetsAreLive` and `TestPinnedURLsAreLive` fetch every pinned feed
  URL over the network: all eight feed assets (four arches × ipk/apk) and both
  aarch64 GitHub fallback assets return HTTP 200.
- The feed release `v0.6.0-alpha1` of `FreedomTechFeed/packages` really does
  publish `tollgate-wrt_0.6.0_alpha1_<arch>.{ipk,apk}`; its `.ipk` control file
  reports `Package: tollgate-wrt`, `Version: 0.6.0_alpha1-r1`, which is what the
  read-back ladder reports.

## What is not verified

- No physical-router run from this change: the provenance log lines and the
  read-back output have not been observed on a live deploy.
- `tollgate version` output was confirmed by reading the module source
  (`src/cli/version.go`) and by finding the same strings in the published
  binaries — not by executing the aarch64 build on a router.
