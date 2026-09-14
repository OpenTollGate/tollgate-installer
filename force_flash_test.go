package main

import (
	"encoding/json"
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
	}
	for in, want := range cases {
		if got := glModelFromBoard(in); got != want {
			t.Errorf("glModelFromBoard(%q) = %q, want %q", in, got, want)
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
