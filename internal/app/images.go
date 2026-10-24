package app

import (
	"fmt"
	"time"
)

// openWrtImage describes the OpenWrt sysupgrade image parameters for a
// GL.iNet board. URL() builds the standard OpenWrt download URL:
//
//	https://downloads.openwrt.org/releases/{version}/targets/{target}/{subtarget}/openwrt-{version}-{target}-{subtarget}-{board}-squashfs-sysupgrade.bin
//
// The latest stable OpenWrt version is updated when the image URL changes.
type openWrtImage struct {
	Target    string // e.g. "mediatek"
	Subtarget string // e.g. "filogic"
	Board     string // e.g. "glinet_gl-mt3000"
	Version   string // e.g. "25.12.5"
}

// glModelReleases is the per-model OpenWrt release allowlist for boards that do
// NOT ship on every release the wizard supports — a router added to OpenWrt
// after an older supported branch was cut has no image there. CF-WR632AX is the
// case in point: it has a 25.12.5 sysupgrade image but NO 24.10.8 one (the
// device landed after the 24.10 branch), so a request on 24.10.8 must be
// refused up front rather than produce a URL that 404s mid-deploy.
//
// A model ABSENT from this map is assumed to ship on every supported release —
// the historical behaviour, kept as the default so the existing rows are
// unchanged and only a board with a known gap carries an entry. Keys must be
// glModelMap keys (guarded by TestGlModelMapVersionAllowlistsAreSelfConsistent).
var glModelReleases = map[string][]string{
	"cf-wr632ax": {"25.12.5"},
}

// modelSupportsRelease reports whether the model has an OpenWrt sysupgrade
// image on the given release. An absent allowlist means every supported release.
func modelSupportsRelease(model, version string) bool {
	allow, ok := glModelReleases[model]
	if !ok {
		return true
	}
	for _, v := range allow {
		if v == version {
			return true
		}
	}
	return false
}

// URL returns the full sysupgrade download URL for this image.
func (img openWrtImage) URL() string {
	return fmt.Sprintf(
		"https://downloads.openwrt.org/releases/%s/targets/%s/%s/openwrt-%s-%s-%s-%s-squashfs-sysupgrade.bin",
		img.Version, img.Target, img.Subtarget,
		img.Version, img.Target, img.Subtarget, img.Board,
	)
}

// openWrtVersion is the pinned OpenWrt release for every image in the map.
// Bump this (and re-verify every board name below) when a new stable
// OpenWrt version ships — see the quarterly image-URL check.
//
// Not every board ships on every supported release: a router added to OpenWrt
// after a release branch was cut has no image there. Such a board pins its own
// Version list in glModelMap (see cf-wr632ax) instead of inheriting this one.
const openWrtVersion = "25.12.5"

// glModelMap maps a supported router's board name to its OpenWrt sysupgrade
// image. Keys are the bare lowercase model — the segment after the vendor
// comma in the OpenWrt board_name (/tmp/sysinfo/board_name), e.g. "gl-mt6000"
// for "glinet,gl-mt6000" and "cf-wr632ax" for "comfast,cf-wr632ax".
//
// This table is the AUTHORITY glModelFromBoard resolves a router's board
// against: a model is flashable exactly when it has a row here, so adding a
// row is the whole of "support the device" and an unknown board (whatever its
// vendor prefix) is refused. Any new row must also satisfy the URL-format and
// liveness tests in images_test.go.
//
// Board names, targets, and subtargets were VERIFIED against the live
// downloads.openwrt.org 25.12.5 build tree (2026-09-08). The plan's original
// draft had several wrong targets/boards — corrected here:
//
//   - gl-ar750s  → ath79/nand, board glinet_gl-ar750s-nor (NOT ath79/generic)
//   - gl-ar300m  → ath79/nand, board glinet_gl-ar300m-nor (NOT ath79/generic)
//   - gl-xe300   → ath79/nand, board glinet_gl-xe300 (NOT ramips/mt7621)
//   - gl-mt1300  → ramips/mt7621, board glinet_gl-mt1300 (NOT mediatek/mt7623)
//   - gl-ax1800  → qualcommax/ipq60xx, board glinet_gl-ax1800 (NOT ipq807x)
//   - gl-axt1800 → qualcommax/ipq60xx, board glinet_gl-axt1800 (NOT ipq807x)
//
// The plan also listed gl-b1300, gl-sft1200, and gl-mv1000 — none of these
// ship a 25.12.5 sysupgrade image (verified across every target), so they
// are intentionally OMITTED: including them would produce 404 URLs and fail
// the liveness test. If a real device reports one of these models, the
// operator gets the "Unsupported router model" error and must flash manually.
//
// Keys are lowercase model names (GL.iNet board names are lowercase).
var glModelMap = map[string]openWrtImage{
	// mediatek/filogic
	"gl-mt2500":   {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-mt2500", Version: openWrtVersion},
	"gl-mt3000":   {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-mt3000", Version: openWrtVersion},
	"gl-mt3600be": {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-mt3600be", Version: openWrtVersion},
	"gl-mt6000":   {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-mt6000", Version: openWrtVersion},
	"gl-x3000":    {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-x3000", Version: openWrtVersion},
	"gl-xe3000":   {Target: "mediatek", Subtarget: "filogic", Board: "glinet_gl-xe3000", Version: openWrtVersion},
	// COMFAST CF-WR632AX: same aarch64_cortex-a53 / mediatek-filogic target as
	// the GL-MT3000/MT6000 above. The map KEY is the bare model the router
	// reports ("comfast,cf-wr632ax"); the Board is the UNDERCORE form because
	// that is the image-filename segment. Verified 2026-10-16: the 25.12.5
	// sysupgrade image is HTTP 200, the 24.10.8 one is HTTP 404 (the device was
	// added to OpenWrt after the 24.10 branch), so its release allowlist names
	// 25.12.5 only.
	"cf-wr632ax": {Target: "mediatek", Subtarget: "filogic", Board: "comfast_cf-wr632ax", Version: openWrtVersion},

	// ath79/generic
	"gl-ar150":       {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-ar150", Version: openWrtVersion},
	"gl-ar300m-lite": {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-ar300m-lite", Version: openWrtVersion},
	"gl-ar300m16":    {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-ar300m16", Version: openWrtVersion},
	"gl-ar750":       {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-ar750", Version: openWrtVersion},
	"gl-mifi":        {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-mifi", Version: openWrtVersion},
	"gl-usb150":      {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-usb150", Version: openWrtVersion},
	"gl-x300b":       {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-x300b", Version: openWrtVersion},
	"gl-x750":        {Target: "ath79", Subtarget: "generic", Board: "glinet_gl-x750", Version: openWrtVersion},

	// ath79/nand
	"gl-ar300m-nand": {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-ar300m-nand", Version: openWrtVersion},
	"gl-ar300m-nor":  {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-ar300m-nor", Version: openWrtVersion},
	"gl-ar750s-nor":  {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-ar750s-nor", Version: openWrtVersion},
	"gl-e750":        {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-e750", Version: openWrtVersion},
	"gl-s200-nor":    {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-s200-nor", Version: openWrtVersion},
	"gl-x1200-nor":   {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-x1200-nor", Version: openWrtVersion},
	"gl-xe300":       {Target: "ath79", Subtarget: "nand", Board: "glinet_gl-xe300", Version: openWrtVersion},

	// ramips/mt7621
	"gl-mt1300": {Target: "ramips", Subtarget: "mt7621", Board: "glinet_gl-mt1300", Version: openWrtVersion},

	// qualcommax/ipq60xx
	"gl-ax1800":  {Target: "qualcommax", Subtarget: "ipq60xx", Board: "glinet_gl-ax1800", Version: openWrtVersion},
	"gl-axt1800": {Target: "qualcommax", Subtarget: "ipq60xx", Board: "glinet_gl-axt1800", Version: openWrtVersion},
}

// flashImageBytes returns the OpenWrt sysupgrade image bytes for imageURL,
// preferring the Job's stageCache — populated by the PreStage step under the
// exact asset URL — so a pre-staged flash performs ZERO network fetches (the
// whole point of staging: the laptop may have lost its internet by flash
// time). On a cache miss the image is downloaded live with downloadWithRetry
// (3 attempts, exponential backoff, definitive-4xx short-circuit) — the image
// is the largest, most failure-prone download in the deploy, so it must never
// use the bare httpGetFile path. The acquisition path is logged so the
// operator can see whether the flash ran offline.
func flashImageBytes(job *Job, imageURL string) ([]byte, error) {
	if data, ok := job.stagedAsset(imageURL); ok {
		job.addLog("Using staged OpenWrt image from cache (no download)")
		return data, nil
	}
	job.addLog("Downloading: " + imageURL)
	data, err := downloadWithRetry(imageURL, 3, 2*time.Second)
	if err == nil {
		job.addLog(fmt.Sprintf("Downloaded %d KB", len(data)/1024))
	}
	return data, err
}
