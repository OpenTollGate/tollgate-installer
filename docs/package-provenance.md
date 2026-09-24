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
tollgate-wrt source: FreedomTechFeed/packages release — https://github.com/FreedomTechFeed/packages/releases/download/v0.6.0-alpha2-pre3/tollgate-wrt_0.6.0_alpha2_pre3_aarch64_cortex-a53.apk
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
Installed tollgate-wrt build: v0.6.0-alpha2-g373770a (commit 373770a)
step install → "tollgate-wrt v0.6.0-alpha2-g373770a (commit 373770a) installed via apk from FreedomTechFeed/packages release"
```

On the current main-tip package the CLI rung reports the version string with the
**source commit appended** (`v0.6.0-alpha2-g373770a`) while the package-metadata
rungs report the apk version (`0.6.0_alpha2_pre3-r1`) — see "Version string vs
source commit" below.

Every rung is optional: an older backend (v0.5.0) or an image whose CLI is not
on `PATH` yields `unknown`, which is logged (never treated as an error). The
`git` placeholders `unknown` / `dev` that a plain `go build` injects are dropped
rather than reported as a build identity.

### Feed URL hardening

`pkgCandidateURLs` returns **no candidates** for an arch that is not a
plausible OpenWrt tuple (empty, whitespace-padded, containing spaces or
slashes). Previously an empty arch produced a malformed
`.../tollgate-wrt_0.6.0_alpha2_pre_.ipk` URL — a 404 that hid the real fault (arch
detection failed). The caller treats an empty candidate list as a hard failure
and never substitutes another arch's asset.

The **package version is derived from the release tag**, never written out
separately (see "Selected feed release" below), so the two spellings the feed's
Makefile keeps — `PKG_SOURCE_VERSION` (hyphen, tags the release) and
`PKG_VERSION` (underscore, apk-legal, names the asset) — cannot drift.

## Selected feed release (current pin)

| | |
|---|---|
| Feed repo | `FreedomTechFeed/packages` (`net/tollgate-wrt/Makefile`) |
| Selected tag | **`v0.6.0-alpha2-pre3`** — the main-tip pre-release |
| Package version | **`0.6.0_alpha2_pre3`** (tag with the leading `v` dropped and `-` → `_`), installed as `0.6.0_alpha2_pre3-r1` |
| Source commit | **`373770a`** of `tollgate-module-basic-go` (the feed build is SHA-pinned to it) |
| Assets | 7 arches × {`.ipk`, `.apk`} = 14: `aarch64_cortex-a53`, `aarch64_cortex-a72`, `arm_cortex-a7`, `mips64_octeonplus`, `mipsel_24kc`, `mips_24kc`, `x86_64` |
| Naming | `tollgate-wrt_0.6.0_alpha2_pre_<arch>.<ext>` |

There is exactly **one** version literal in the code —
`feedReleaseTagDefault = "v0.6.0-alpha2-pre3"` — and one conversion rule,
`feedPkgVersionForTag` (strip the leading `v`, `-` → `_`). `feedAssetURL` builds
every URL as
`<feed repo release download>/<effective tag>/tollgate-wrt_<derived version>_<arch><ext>`,
so bumping the tag moves the URL, the asset name and the version together.

### Overriding the tag

Pre-releases (and rollbacks) can be exercised without rebuilding the wizard:

```
TOLLGATE_FEED_RELEASE_TAG=v0.6.0-alpha1 ./tollgate-installer
```

The override is honoured only when it is a plausible tag (no spaces, no
slashes, no leading dot); anything else falls back to the default, so a typo
can never turn into a malformed download URL for every arch. It is read once at
startup, so set it before launching the wizard.

### What happens when the pin goes stale

`TestPinnedFeedReleaseTagExists` queries
`api.github.com/repos/FreedomTechFeed/packages/releases/tags/<selected tag>` and
**fails** on HTTP 404, because a stale pin means every arch downloads a 404.
`TestFeedReleasePublishesEachDerivedAssetName` goes further: it fetches the
release's own asset list and asserts every name the code derives exists, and
that no `tollgate-wrt_` asset exists that the code cannot derive. Both tests are
skipped in `-short` mode, and are skipped (never silently passed) if the
anonymous GitHub API rate-limits the request.

### Version string vs source commit

Two different strings describe the same build, and neither substitutes for the
other:

| String | Where it comes from | Value on this release |
|---|---|---|
| Package version | The feed's `PKG_VERSION`, the release tag in apk-legal spelling | `0.6.0_alpha2_pre3` (installed as `0.6.0_alpha2_pre3-r1`) |
| Binary version string | Compiled into the installed binary | `v0.6.0-alpha2-g373770a` |

The binary's string embeds the **source commit** (`-g373770a`) and does **not**
equal the tag-derived package version. That is expected: the version string
identifies the release line, while the **commit identifies the build**. Two
different main-tip builds can carry the same version string and different
commits, so the commit — not the version string — is what pins a main-tip
artifact.

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
- For the **current** pin (`v0.6.0-alpha2-pre3`), the release's asset list was
  queried live and the code's derived names were compared against it — see
  "Verification of the v0.6.0-alpha2-pre repin" below.

## Verification of the `v0.6.0-alpha2-pre3` repin

Verified by this change:

- The release exists and publishes 14 assets: `gh api
  repos/FreedomTechFeed/packages/releases/tags/v0.6.0-alpha2-pre` returns
  `tag_name: v0.6.0-alpha2-pre` with 7 arches × {`.ipk`, `.apk`}.
- `feedAssetURL(arch, ext)` reproduces the release's own asset names
  byte-for-byte for every arch — the derivation needed **no** change: the tag's
  hyphens become underscores exactly as the feed's `PKG_VERSION` does. Asserted
  against the live release by
  `TestFeedReleasePublishesEachDerivedAssetName`, and pinned offline (all 14
  names) by `TestFeedAssetURL`.
- The `x86_64` `.ipk` was downloaded from the release and its payload inspected:
  `Version: 0.6.0_alpha2_pre-r1` in the control file, and the version string
  `v0.6.0-alpha2-g373770a` present in the shipped `usr/bin/tollgate-wrt` binary.
  This is how the two strings in the table above were established; it is a
  file-level check on one arch, not a router run.

Not verified by this change:

- No install on a physical router: no wizard run downloaded these assets onto a
  device, and the provenance log lines and read-back output were not observed
  on a live deploy.
- `tollgate version --json` was not observed returning a version payload on the
  downloaded x86_64 CLI on this laptop (that binary needs the router's musl
  runtime), so the CLI rung's exact output on a router remains unconfirmed; the
  version string above was read from the binary file itself.
- The other six arches were verified by asset *listing* only, not by download.

## What is not verified

- No physical-router run from this change: the provenance log lines and the
  read-back output have not been observed on a live deploy.
- `tollgate version` output was confirmed by reading the module source
  (`src/cli/version.go`) and by finding the same strings in the published
  binaries — not by executing the aarch64 build on a router.

---

# Package integrity verification (audit C2-I-03)

**Status:** implemented, unit-tested, and verified against the LIVE pinned
release (see the evidence below). Not yet observed on a physical router.

## Why

Nothing in the install path checked that the bytes about to be installed were
the bytes the release published. `deploy.go` downloaded the package and handed
it straight to a package manager whose own verification is deliberately
disabled for this path:

```
opkg install --force-downgrade --force-reinstall --force-overwrite --force-depends /tmp/tollgate-wrt.ipk
apk  add    --allow-untrusted --force-overwrite                                       /tmp/tollgate-wrt.apk
```

The only `crypto/sha256` call in the repository hashed the URL *string*, to name
a cache file. So a truncated download, an HTML error page saved as "the
package", a substituted file left in the staging cache, or a mirror/redirect
serving different bytes all installed as root — and the step still rendered
green, because the post-install check only compared version strings.

## What runs now — `pkgverify.go`

Immediately before the package is pushed to the router (and again on the
router's own `wget` path, hashing the file on the router), the bytes are checked:

1. **Structural gate (always, no network):** the payload must be at least
   100 KiB and carry the format's magic — gzip (`1f 8b`) for `.ipk`, the
   apk-tools 3 ADB magic (`ADBd`) for `.apk`. This rejects a truncated transfer
   and an error page saved as the package before any lookup happens.
2. **Published-digest gate**, trying three sources in order:
   1. `<release>/SHA256SUMS` — the release manifest, once the feed publishes it;
   2. `<asset-URL>.sha256` — a per-asset sidecar;
   3. the GitHub release API's per-asset `digest`
      (`GET /api.github.com/repos/<owner>/<repo>/releases/tags/<tag>`).

   Source 3 is what makes verification real **today**: the feed release
   `v0.6.0-alpha2-pre9` carries 14 assets and no `SHA256SUMS`, but GitHub
   publishes a `sha256:` digest for every asset.

   Sources 1 and 2 are **same-origin** with the package (fetched from the asset
   URL's own host), so they are not an independent anchor: a host or MITM that
   serves altered bytes also serves the digest that matches them. A match from
   those two is therefore reported `NOT VERIFIED`. Source 3 is served by a
   different origin (`api.github.com`) over its own connection, so it survives a
   substituted mirror/redirect — it is the only source that can produce a green
   verdict. (Cold cross-family review, 2026-09-23, finding 2.)

## Policy — fail closed, never a silent pass

| Condition | Result |
|---|---|
| Independent published digest matches the bytes | step 6 renders **done** with `[sha256 verified]` |
| Published digest differs | **deploy FAILS** (`jobFail`, step 6 = failed), naming the asset, both digests and which source the expected digest came from |
| Bytes cannot be the expected format | **deploy FAILS**, naming the reason |
| No digest published anywhere | step 6 renders **warn** with `NOT VERIFIED`; logged as a WARNING |
| Only a SAME-ORIGIN digest (manifest or sidecar) matches | step 6 renders **warn**, marked not verified — self-consistency is not authenticity |
| Installed from the ROUTER's own package feeds (last-resort path; the bytes never pass through the wizard) | step 6 renders **warn**, marked not verified — the gate never saw those bytes |
| No digest published, and `TOLLGATE_REQUIRE_PACKAGE_DIGEST=1` | **deploy FAILS** |

The warn rows exist so that "we could not check what we installed" is never
presented as an unqualified success. Green is reserved for a verdict that
verified the bytes against an independent published digest; nothing else — not
even a verdict that was never computed (the zero value) — can render green.

### What this does and does not defend against

Catches: a partial/corrupted download; a wrong or substituted file at rest in
the staging cache; a redirect or mirror serving different bytes **when the
expected digest came from the GitHub release API** (the independent source — the
substituted host cannot serve that digest); a swapped `/tmp/tollgate-wrt.*` on
the router. Each becomes a failed deploy.

Does **not** catch, and says so in the step detail: a same-origin digest source
(`SHA256SUMS` / `.sha256` fetched from the asset's own host) on a custom feed
host — a host serving altered bytes serves the matching digest too, so that case
is reported `NOT VERIFIED` rather than green; and the last-resort router-feed
install, where the bytes never pass through this process at all (also `NOT
VERIFIED`). Nor a compromise of the GitHub release itself, where the digest and
the bytes would be replaced together. Closing those needs the digest to come
from a signed manifest published by the feed with a key pinned in the binary
(audit finding C3-05: publish `SHA256SUMS` plus the apk signing public key as
release assets). The verifier already prefers that manifest the moment it
exists — no code change will be needed, though a signature check (rather than a
parse-only manifest) still has to be added to make it an independent anchor.

## Evidence

```
# live: the pinned release asset, verified and then tampered with
$ go test -count=1 -run TestLivePackageDigestRejectsCorruptedBytes -v .
--- PASS: TestLivePackageDigestRejectsCorruptedBytes (4.05s)
    verified tollgate-wrt_0.6.0_alpha2_pre9_aarch64_cortex-a53.ipk (8789585 bytes)
      against GitHub release API asset digest: sha256 23add42c18…b2a3c49
    corrupted copy rejected as expected: sha256 MISMATCH … expected 23add42c18…b2a3c49
      (from the GitHub release API asset digest) but the bytes hash to 4655ecddb2…6dc018
      — refusing to install altered or corrupted package bytes
```

`TestCheckPackageBytesPolicy` additionally pins that a tampered payload makes
`checkPackageBytes` return a fatal error and an `ERROR … MISMATCH` log line,
and that an unverifiable payload is fatal under
`TOLLGATE_REQUIRE_PACKAGE_DIGEST=1`.
