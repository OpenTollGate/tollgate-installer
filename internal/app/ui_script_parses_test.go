package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ui_script_parses_test.go closes a gap the other page tests share: they assert
// on the page's TEXT, and text cannot tell a valid edit from one that breaks the
// <script> tag. A broken script tag takes the entire wizard down — every button,
// Deploy included — and every string assertion above would still pass.
//
// This is not hypothetical: three rounds of fixes have now edited this page's
// JavaScript, and the Go tests that guard them are all substring checks.
//
// Skipped when node is unavailable, so a host without it reports a skip rather
// than a failure: the check is a strong signal, not a build dependency.

// jsScriptBlocks returns the body of every inline <script> element.
func jsScriptBlocks(src string) []string {
	var out []string
	rest := src
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		gt := strings.Index(rest, ">")
		if gt < 0 {
			return out
		}
		body := rest[gt+1:]
		end := strings.Index(body, "</script>")
		if end < 0 {
			return out
		}
		if strings.TrimSpace(body[:end]) != "" {
			out = append(out, body[:end])
		}
		rest = body[end:]
	}
}

func TestInstallerUIScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed: cannot syntax-check the page's JavaScript")
	}
	blocks := jsScriptBlocks(string(indexHTML))
	if len(blocks) == 0 {
		t.Fatalf("index.html has no inline <script> block to check")
	}
	dir := t.TempDir()
	for i, b := range blocks {
		f := filepath.Join(dir, fmt.Sprintf("block%d.js", i))
		if err := os.WriteFile(f, []byte(b), 0o600); err != nil {
			t.Fatalf("write block %d: %v", i, err)
		}
		if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
			t.Fatalf("index.html script block %d does not parse — the wizard would be dead on load (no button would work):\n%s", i, out)
		}
	}
	t.Logf("%d inline script block(s) parse cleanly", len(blocks))
}
