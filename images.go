package main

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

// URL returns the full sysupgrade download URL for this image.
func (img openWrtImage) URL() string {
	return fmt.Sprintf(
		"https://downloads.openwrt.org/releases/%s/targets/%s/%s/openwrt-%s-%s-%s-%s-squashfs-sysupgrade.bin",
		img.Version, img.Target, img.Subtarget,
		img.Version, img.Target, img.Subtarget, img.Board,
	)
}

// openWrtVersion is the pinned OpenWrt release for all GL.iNet images.
// Bump this (and re-verify every board name below) when a new stable
// OpenWrt version ships — see the quarterly image-URL check.
const openWrtVersion = "25.12.5"

// glModelMap maps a GL.iNet board name (from /etc/gl-inet-release or
// /tmp/sysinfo/board_name, lowercased) to its OpenWrt sysupgrade image.
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
// operator gets the "Unknown GL.iNet model" error and must flash manually.
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
