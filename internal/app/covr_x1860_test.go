package app

import "testing"

// The COVR-X1860 (D-Link) is a real club device: the feed publishes
// tollgate-wrt_<ver>_mipsel_24kc-ramips-mt7621.{apk,ipk} for it and OpenWrt
// 25.12.5 ships openwrt-25.12.5-ramips-mt7621-dlink_covr-x1860-a1-squashfs-sysupgrade.bin.
// Yet the wizard could not flash it: glModelFromBoard refused every non-"gl-"
// board, so ForceFlash died with "Unknown GL.iNet model" and the device was
// invisible (TARGETS.md §2 — "a published asset with no wizard entry is
// invisible"). These pin that a non-GL vendor resolves by table lookup.
func TestNonGLVendorBoardsResolveByTableLookup(t *testing.T) {
	cases := map[string]string{
		// OpenWrt's own spelling for the D-Link COVR-X1860 A1.
		"dlink,covr-x1860-a1":    "dlink_covr-x1860-a1",
		"DLINK,COVR-X1860-A1":    "dlink_covr-x1860-a1",
		"  dlink,covr-x1860-a1 ": "dlink_covr-x1860-a1",
		// Already a table key.
		"dlink_covr-x1860-a1": "dlink_covr-x1860-a1",
		// GL.iNet must keep resolving to the BARE model (no regression).
		"glinet,gl-mt6000": "gl-mt6000",
		"glinet_gl-mt6000": "gl-mt6000",
	}
	for in, want := range cases {
		if got := glModelFromBoard(in); got != want {
			t.Errorf("glModelFromBoard(%q) = %q, want %q", in, got, want)
		}
	}
}

// Resolution is decided by the table, so a board that is NOT a row must never
// be handed back as if it were flashable — the callers only check against "".
func TestUnknownVendorBoardsStillRefuse(t *testing.T) {
	for _, in := range []string{
		"mediatek,mt7621",
		"dlink,covr-x1860",    // close to the real board, but not a row
		"tplink,archer-c7",    // not shipped by the feed
		"dlink,covr-x1870-a1", // a sibling device with no table row yet
		"glinet,gl-unknown-model",
	} {
		if got := glModelFromBoard(in); got != "" {
			t.Errorf("glModelFromBoard(%q) = %q, want \"\" (no row exists, so flashing must refuse)", in, got)
		}
	}
}

// The image URL the table builds for the COVR-X1860 must be the real OpenWrt
// release asset. This is the exact string the flash step downloads, so a typo
// in Target/Subtarget/Board would 404 only at flash time on real hardware.
func TestCOVRX1860ImageURLMatchesOpenWrtRelease(t *testing.T) {
	m := glModelFromBoard("dlink,covr-x1860-a1")
	img, ok := glModelMap[m]
	if !ok {
		t.Fatalf("glModelMap missing %q — force-flash would be refused", m)
	}
	want := "https://downloads.openwrt.org/releases/" + openWrtVersion +
		"/targets/ramips/mt7621/openwrt-" + openWrtVersion +
		"-ramips-mt7621-dlink_covr-x1860-a1-squashfs-sysupgrade.bin"
	if got := img.URL(); got != want {
		t.Errorf("COVR-X1860 image URL = %q, want %q", got, want)
	}
}

// The clean-slate inspect chain feeds the table's OWN board string back in, so
// every row must round-trip for both supported releases. The COVR-X1860 is the
// first non-GL row, which is what made the earlier gl-prefix assumption visible.
func TestCOVRX1860CleanSlateRoundTrip(t *testing.T) {
	for _, version := range []string{"25.12.5", "24.10.8"} {
		got, err := cleanSlateImage("dlink_covr-x1860-a1", version)
		if err != nil {
			t.Fatalf("cleanSlateImage on %s: %v", version, err)
		}
		if got.Board != "dlink_covr-x1860-a1" || got.Target != "ramips" || got.Subtarget != "mt7621" {
			t.Errorf("cleanSlateImage(%s) = %+v, want the dlink_covr-x1860-a1 ramips/mt7621 row", version, got)
		}
		if got.Version != version {
			t.Errorf("image version = %q, want %q", got.Version, version)
		}
	}
}
