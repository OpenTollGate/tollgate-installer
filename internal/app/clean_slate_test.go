package app

import (
	"strings"
	"testing"
)

func TestCleanSlateImageURLForEachRelease(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"25.12.5", "https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin"},
		{"24.10.8", "https://downloads.openwrt.org/releases/24.10.8/targets/mediatek/filogic/openwrt-24.10.8-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin"},
	} {
		img, err := cleanSlateImage("gl-mt3000", tc.version)
		if err != nil {
			t.Fatal(err)
		}
		if got := img.URL(); got != tc.want {
			t.Errorf("URL = %q, want %q", got, tc.want)
		}
	}
}

func TestCleanSlateRejectsUnmappedBoardBeforeFlash(t *testing.T) {
	if _, err := cleanSlateImage("gl-not-a-real-board", "25.12.5"); err == nil || !strings.Contains(err.Error(), "Unknown GL.iNet model") {
		t.Fatalf("error = %v, want unknown-model refusal", err)
	}
}

func TestCleanSlateConfirmationMustMatchDetectedBoard(t *testing.T) {
	for _, confirmation := range []string{"", "gl-mt3000", "glinet_gl-mt6000"} {
		if err := validateCleanSlateConfirmation("glinet_gl-mt3000", confirmation); err == nil {
			t.Errorf("confirmation %q was accepted", confirmation)
		}
	}
	if err := validateCleanSlateConfirmation("glinet_gl-mt3000", "glinet_gl-mt3000"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanSlateHashMismatchAbortsBeforePush(t *testing.T) {
	pushed := false
	_, err := verifyCleanSlateImage([]byte("image"), "deadbeef", func([]byte) { pushed = true })
	if err == nil || !strings.Contains(err.Error(), "expected sha256 deadbeef") || !strings.Contains(err.Error(), "actual sha256") {
		t.Fatalf("error = %v, want expected and actual hashes", err)
	}
	if pushed {
		t.Fatal("image was pushed after hash mismatch")
	}
}

func TestCleanSlateDoesNotInstallPackage(t *testing.T) {
	if strings.Contains(cleanSlateFlashCommand, "tollgate-wrt") || strings.Contains(cleanSlateFlashCommand, "opkg install") || strings.Contains(cleanSlateFlashCommand, "apk add") {
		t.Fatalf("clean-slate flash command installs a package: %q", cleanSlateFlashCommand)
	}
	if !strings.Contains(cleanSlateFlashCommand, "sysupgrade -n") {
		t.Fatalf("clean-slate flash command = %q, want sysupgrade -n", cleanSlateFlashCommand)
	}
}
