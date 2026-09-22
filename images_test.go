package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestOpenWrtImageURLFormat verifies the URL() method produces the standard
// OpenWrt sysupgrade download URL shape.
func TestOpenWrtImageURLFormat(t *testing.T) {
	img := openWrtImage{
		Target:    "mediatek",
		Subtarget: "filogic",
		Board:     "glinet_gl-mt3000",
		Version:   "25.12.5",
	}
	want := "https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin"
	if got := img.URL(); got != want {
		t.Errorf("URL() =\n  %q\nwant\n  %q", got, want)
	}
}

// TestGlModelMapURLsWellFormed asserts every entry in glModelMap produces a
// well-formed OpenWrt sysupgrade URL: correct scheme/host, the version and
// target/subtarget/board all present, and the squashfs-sysupgrade suffix.
func TestGlModelMapURLsWellFormed(t *testing.T) {
	if len(glModelMap) == 0 {
		t.Fatal("glModelMap is empty")
	}
	for model, img := range glModelMap {
		url := img.URL()
		if !strings.HasPrefix(url, "https://downloads.openwrt.org/releases/") {
			t.Errorf("%s: URL %q missing downloads.openwrt.org prefix", model, url)
		}
		if !strings.HasSuffix(url, "-squashfs-sysupgrade.bin") {
			t.Errorf("%s: URL %q must end in -squashfs-sysupgrade.bin", model, url)
		}
		// The board name must appear in the URL (it is the distinguishing part).
		if !strings.Contains(url, img.Board) {
			t.Errorf("%s: URL %q does not contain board %q", model, url, img.Board)
		}
		// Version must be the pinned constant.
		if img.Version != openWrtVersion {
			t.Errorf("%s: Version %q != pinned openWrtVersion %q", model, img.Version, openWrtVersion)
		}
		// No empty fields.
		if img.Target == "" || img.Subtarget == "" || img.Board == "" {
			t.Errorf("%s: empty target/subtarget/board in %+v", model, img)
		}
	}
}

// TestGlModelMapKnownModels pins the exact URL for the models the deploy
// wizard is most likely to encounter (the ones named in the plan).
func TestGlModelMapKnownModels(t *testing.T) {
	cases := map[string]string{
		"gl-mt3000": "https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin",
		"gl-mt6000": "https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt6000-squashfs-sysupgrade.bin",
		"gl-ar750":  "https://downloads.openwrt.org/releases/25.12.5/targets/ath79/generic/openwrt-25.12.5-ath79-generic-glinet_gl-ar750-squashfs-sysupgrade.bin",
		"gl-mt1300": "https://downloads.openwrt.org/releases/25.12.5/targets/ramips/mt7621/openwrt-25.12.5-ramips-mt7621-glinet_gl-mt1300-squashfs-sysupgrade.bin",
		"gl-ax1800": "https://downloads.openwrt.org/releases/25.12.5/targets/qualcommax/ipq60xx/openwrt-25.12.5-qualcommax-ipq60xx-glinet_gl-ax1800-squashfs-sysupgrade.bin",
	}
	for model, want := range cases {
		img, ok := glModelMap[model]
		if !ok {
			t.Errorf("glModelMap missing expected model %q", model)
			continue
		}
		if got := img.URL(); got != want {
			t.Errorf("%s: URL() =\n  %q\nwant\n  %q", model, got, want)
		}
	}
}

// TestGlModelMapUnknownModelReturnsZeroValue verifies that looking up a model
// not in the map returns the zero value (not a panic), so the caller can
// distinguish "known model" from "unknown model" via the ok idiom.
func TestGlModelMapUnknownModelReturnsZeroValue(t *testing.T) {
	img, ok := glModelMap["gl-nonexistent-model"]
	if ok {
		t.Fatalf("expected unknown model lookup to return ok=false, got %+v", img)
	}
	if img != (openWrtImage{}) {
		t.Errorf("expected zero-value openWrtImage, got %+v", img)
	}
	// The zero value's URL() must not panic and must be malformed (empty fields).
	_ = img.URL()
}

// TestGlModelMapNoEmptyURLs guards against a model whose URL would be
// malformed because a field is empty — a 404 waiting to happen. It checks
// the path portion (after the scheme://host) for empty segments.
func TestGlModelMapNoEmptyURLs(t *testing.T) {
	for model, img := range glModelMap {
		url := img.URL()
		// Strip the scheme://host prefix, then look for empty path segments.
		rest := strings.TrimPrefix(url, "https://downloads.openwrt.org/")
		if strings.Contains(rest, "//") {
			t.Errorf("%s: URL %q contains empty path segment (empty field)", model, url)
		}
	}
}

// TestGlModelMapUniqueBoards ensures no two models map to the same board
// (which would mean a duplicate/typo in the table).
func TestGlModelMapUniqueBoards(t *testing.T) {
	seen := map[string]string{}
	for model, img := range glModelMap {
		key := fmt.Sprintf("%s/%s/%s", img.Target, img.Subtarget, img.Board)
		if prev, dup := seen[key]; dup {
			t.Errorf("models %q and %q both map to %q", prev, model, key)
		}
		seen[key] = model
	}
}

// TestGlModelMapURLsAreLive is the liveness regression test for the OpenWrt
// sysupgrade image URLs built by glModelMap. Every model the wizard can flash
// must resolve to a real image on downloads.openwrt.org today — a 404 here
// means a fresh GL.iNet flash breaks at the download step.
//
// It performs real HTTP requests (Range: bytes=0-0 so image bodies are not
// downloaded). Run with -short to skip network access for offline hacking;
// the CI-parity command is plain `go test ./...`, which runs this live.
//
// NOTE: images.go declares NO *URL constants (URLs are built via the URL()
// method), so these are intentionally NOT registered in pins_test.go's
// expectedPinnedURLConsts/liveCheckPins — that registry parses only deploy.go.
// This test is the dedicated liveness guard for the image map.
func TestGlModelMapURLsAreLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live URL check skipped: -short mode (CI-parity runs without -short)")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	for model, img := range glModelMap {
		url := img.URL()
		t.Run(model, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				t.Fatalf("building request for %s: %v", model, err)
			}
			req.Header.Set("Range", "bytes=0-0") // fetch 1 byte, not the image
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s = %q: request failed: %v", model, url, err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
				t.Errorf("%s = %q: got HTTP %d, want 200 — broken image pin, GL.iNet flash will fail",
					model, url, resp.StatusCode)
			}
		})
	}
}
