package app

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
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

// ─── the flash verdict must use the shared predicate ─────────────────────────
//
// A successful `sysupgrade -n` closes the SSH session mid-command, after which
// ubus prints "Command failed: ubus call system sysupgrade". This path used to
// match bare "failed"/"error" substrings and would therefore report a GOOD
// upgrade as a failure — the identical defect the deploy path was fixed for
// after it was observed on a real MT6000 (24.10.1 -> 25.12.5). The verdict must
// be the shared, tested sysupgradeFatal predicate.
func TestCleanSlateFlashVerdictUsesSharedSysupgradePredicate(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "clean_slate.go", nil, 0)
	if err != nil {
		t.Fatalf("parse clean_slate.go: %v", err)
	}
	var sawFatalCall bool
	var naive []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "sysupgradeFatal" {
				sawFatalCall = true
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name != "Contains" {
				return true
			}
			for _, a := range call.Args {
				lit, ok := a.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if v := strings.Trim(lit.Value, "\""); v == "failed" || v == "error" {
					naive = append(naive, v)
				}
			}
		}
		return true
	})
	if !sawFatalCall {
		t.Error("runCleanSlate does not route the flash verdict through the shared sysupgradeFatal predicate")
	}
	if len(naive) > 0 {
		t.Errorf("clean_slate.go still treats a bare %v substring as a flash failure", naive)
	}
}

// ─── the inspect chain, which is what actually unlocks the flash button ───────
//
// `cleanSlateInfo` resolves the router's board name and hands the result to the
// UI, which sets window._cleanSlateBoard; the flash button stays disabled until
// that happened AND the operator typed the board name. The chain used to feed
// the OpenWrt board string ("glinet_gl-mt3000") back into cleanSlateImage, which
// treated it as a glModelMap key ("gl-mt3000") — so the inspect step answered
// 400 "Unknown GL.iNet model: " for EVERY router, on every release, and the
// button could never be unlocked at all. These tests pin the chain end to end.

// Every row of the model table must survive the round trip the wizard performs:
// the board name the table publishes is the string the router reports and the
// string the operator is asked to type, so it must resolve back to its own row
// for both supported releases — not just the models we happened to try by hand.
func TestCleanSlateInspectResolvesEverySupportedRouter(t *testing.T) {
	if len(glModelMap) == 0 {
		t.Fatal("model table is empty")
	}
	for model, want := range glModelMap {
		for _, version := range []string{"25.12.5", "24.10.8"} {
			got, err := cleanSlateImage(want.Board, version)
			if err != nil {
				t.Errorf("model %s: inspect would refuse board %q on %s: %v", model, want.Board, version, err)
				continue
			}
			if got.Board != want.Board || got.Target != want.Target || got.Subtarget != want.Subtarget {
				t.Errorf("model %s: board %q resolved to %+v, want the same table row", model, want.Board, got)
			}
			if got.Version != version {
				t.Errorf("model %s: image version = %q, want %q", model, got.Version, version)
			}
		}
	}
}

// The detector must resolve against the release the operator CHOSE. It used to
// hardcode openWrtVersion and let the caller re-resolve, which is how the board
// string ended up being treated as a model key in the first place.
func TestCleanSlateDetectedBoardResolvesTheRequestedRelease(t *testing.T) {
	router := startCannedRouter(t, func(cmd string) string {
		if strings.Contains(cmd, "/tmp/sysinfo/board_name") {
			return "glinet,gl-mt3000\n"
		}
		return ""
	})
	client := router.dial(t)
	defer client.Close()

	for _, version := range []string{"25.12.5", "24.10.8"} {
		board, img, err := cleanSlateDetectedBoard(client, version)
		if err != nil {
			t.Fatalf("release %s: %v", version, err)
		}
		if board != "glinet_gl-mt3000" {
			t.Errorf("release %s: board = %q, want glinet_gl-mt3000", version, board)
		}
		if !strings.Contains(img.URL(), "/releases/"+version+"/") {
			t.Errorf("release %s: the flash would download the wrong release: %s", version, img.URL())
		}
	}
}

// The operator's exact call: POST /api/clean-slate-info against a real (canned)
// SSH session. It must answer 200 with the board name and the published
// checksum — the response the UI needs to set window._cleanSlateBoard.
func TestCleanSlateInfoEndpointUnlocksTheFlashButton(t *testing.T) {
	knownHostsStore(t) // isolate the trust store, no ambient pin
	router := startCannedRouter(t, func(cmd string) string {
		if strings.Contains(cmd, "/tmp/sysinfo/board_name") {
			return "glinet,gl-mt3000\n"
		}
		return ""
	})
	// The operator trusts this router out of band, so its fingerprint is pinned.
	withTrustedFingerprint(t, ssh.FingerprintSHA256(router.hostKey.PublicKey()))
	_, port, err := net.SplitHostPort(router.ln.Addr().String())
	if err != nil {
		t.Fatalf("canned router address: %v", err)
	}
	oldPort := sshDialPort
	sshDialPort = port
	t.Cleanup(func() { sshDialPort = oldPort })

	// downloads.openwrt.org is stubbed: this test must stay offline.
	want := strings.Repeat("ab", 32)
	oldDownload := cleanSlateDownload
	cleanSlateDownload = func(url string) ([]byte, error) {
		if !strings.Contains(url, "/releases/25.12.5/") || !strings.HasSuffix(url, "/sha256sums") {
			t.Errorf("unexpected fetch: %s", url)
		}
		return []byte(want + "  openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin\n"), nil
	}
	t.Cleanup(func() { cleanSlateDownload = oldDownload })

	body, _ := json.Marshal(cleanSlateRequest{IP: "127.0.0.1", Version: "25.12.5"})
	w := httptest.NewRecorder()
	cleanSlateInfo(w, httptest.NewRequest(http.MethodPost, "/api/clean-slate-info", bytes.NewReader(body)))

	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/clean-slate-info = HTTP %d: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response: %v", err)
	}
	if got["board"] != "glinet_gl-mt3000" {
		t.Errorf("board = %q, want glinet_gl-mt3000 — this is the string the operator types to unlock the flash", got["board"])
	}
	if got["sha256"] != want {
		t.Errorf("sha256 = %q, want %q", got["sha256"], want)
	}
	if !strings.Contains(got["image_url"], "/releases/25.12.5/") ||
		!strings.HasSuffix(got["image_url"], "glinet_gl-mt3000-squashfs-sysupgrade.bin") {
		t.Errorf("image_url = %q", got["image_url"])
	}
}

// The refusal must name what the router actually reported. The old message
// interpolated the variable AFTER the mapping had emptied it, so every operator
// saw "Unknown GL.iNet model: " with no model in it.
func TestCleanSlateUnknownModelNamesTheReportedBoard(t *testing.T) {
	_, err := cleanSlateImage("tplink,archer-c7", "25.12.5")
	if err == nil || !strings.Contains(err.Error(), "tplink,archer-c7") {
		t.Fatalf("error = %v, want the reported board named so the operator knows what was seen", err)
	}
}
