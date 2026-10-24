package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGLModelFromBoard(t *testing.T) {
	cases := map[string]string{
		"glinet,gl-mt6000":   "gl-mt6000",
		"glinet,gl-mt3000":   "gl-mt3000",
		"GLiNet,GL-MT6000":   "gl-mt6000",
		"gl-mt6000":          "gl-mt6000",
		"  glinet,gl-x3000 ": "gl-x3000",
		"":                   "",
		"glinet,":            "",
		"mediatek,mt7621":    "",
		"dlink,covr-x1860":   "",
		// Stock GL.iNet firmware reports the model with an UNDERSCORE
		// (/tmp/sysinfo/board_name = "glinet_gl-mt6000"), which carries no comma.
		// Clean-slating a router that is still on stock firmware is the whole
		// point of the button, so this spelling must resolve.
		"glinet_gl-mt6000":      "gl-mt6000",
		"glinet_gl-mt3000":      "gl-mt3000",
		"GLiNet_GL-MT6000":      "gl-mt6000",
		"glinet_gl-ar300m-lite": "gl-ar300m-lite",
		"foo_bar":               "",
		"glinet_":               "",
		// The guard is the MODEL TABLE, not a "gl-" prefix. A foreign board that
		// merely happens to start with "gl-" must still be refused: the old
		// prefix test handed "gl-mt9999" back as flashable, and only the later
		// glModelMap lookup stopped it. Resolving against the table is what
		// makes the table the single source of truth.
		"fakevendor,gl-mt9999": "",
	}
	for in, want := range cases {
		if got := glModelFromBoard(in); got != want {
			t.Errorf("glModelFromBoard(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGLModelFromBoardAcceptsEveryTableRouter pins the two properties the
// widened guard must hold at once: every board a glModelMap row publishes
// resolves back to that row's own key, and no board outside the table ever
// resolves to anything. The wash of vendor prefixes (§8 of the brief) is data,
// not special cases.
func TestGLModelFromBoardAcceptsTableRouter(t *testing.T) {
	// The COMFAST CF-WR632AX reports "comfast,cf-wr632ax" (OpenWrt
	// mediatek/filogic, SUPPORTED_DEVICES += cf-wr632ax). It is a real
	// flashable router, so the guard must accept it — and its model segment
	// must be a key in the table, or accepting it would ship a 404.
	if got := glModelFromBoard("comfast,cf-wr632ax"); got != "cf-wr632ax" {
		t.Fatalf("glModelFromBoard(comfast,cf-wr632ax) = %q, want cf-wr632ax", got)
	}
	if _, ok := glModelMap["cf-wr632ax"]; !ok {
		t.Fatal("glModelMap has no cf-wr632ax row — accepting the board would ship a 404")
	}

	// Round trip: for every row, the board it publishes must resolve back to
	// that row's own key. A row the guard cannot resolve is a router the
	// wizard can never flash.
	for model, img := range glModelMap {
		for _, board := range []string{img.Board, strings.Replace(img.Board, "_", ",", 1)} {
			if got := glModelFromBoard(board); got != model {
				t.Errorf("glModelFromBoard(%q) = %q, want the table key %q", board, got, model)
			}
		}
	}
}

func TestForceFlashResolvesPinnedImage(t *testing.T) {
	m := glModelFromBoard("glinet,gl-mt6000")
	if m == "" {
		t.Fatal("MT6000 board did not resolve to a model key")
	}
	img, ok := glModelMap[m]
	if !ok {
		t.Fatalf("glModelMap missing %q — force-flash would be refused", m)
	}
	if img.Version != openWrtVersion {
		t.Errorf("image version = %q, want %q", img.Version, openWrtVersion)
	}
	if img.Board != "glinet_gl-mt6000" {
		t.Errorf("board = %q, want glinet_gl-mt6000", img.Board)
	}
}

func TestDeployRequestForceFlashJSON(t *testing.T) {
	var req deployRequest
	if err := json.Unmarshal([]byte(`{"ip":"192.168.1.1","lnurl":"a@b.c","forceFlash":true}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.ForceFlash {
		t.Error("forceFlash did not decode to true")
	}
	// Default must be false (explicit opt-in only).
	var zero deployRequest
	if zero.ForceFlash {
		t.Error("forceFlash must default to false")
	}
}

func TestSysupgradeFatal(t *testing.T) {
	// The exact shape a successful sysupgrade emits: it closes the SSH session,
	// after which ubus reports "Command failed". This must NOT be fatal.
	success := "verifying sysupgrade tar file integrity\n" +
		"Tue Sep 15 07:31:17 UTC 2026 upgrade: Commencing upgrade. Closing all shell sessions.\n" +
		"Command failed: ubus call system sysupgrade { \"prefix\": \"/tmp/root\" }"
	if sysupgradeFatal(success) {
		t.Error("successful sysupgrade output (session closure) was misclassified as fatal")
	}

	fatal := map[string]string{
		"image rejected": "Image check failed:\n  Invalid image type",
		"no space":       "no space left on device",
		"missing binary": "sysupgrade: not found",
	}
	for name, out := range fatal {
		if !sysupgradeFatal(out) {
			t.Errorf("expected fatal for %q case", name)
		}
	}
}
