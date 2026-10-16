package app

import (
	"strings"
	"testing"
)

// The wizard's flash refusals are the last thing an operator sees before giving
// up, so their wording is part of the product. It used to say "Unknown GL.iNet
// model" in both the clean-slate and the force-flash path — wrong the moment a
// real supported router is a COMFAST (CF-WR632AX) or anything else that is not
// GL.iNet. The wording must be vendor-neutral, keep the actionable tail, and
// (the bug that was fixed before) actually NAME the model the router reported.
func TestFlashRefusalsAreVendorNeutralAndNameTheModel(t *testing.T) {
	// Vendor-neutral: no GL.iNet-specific claim in any operator-facing refusal.
	// The COMFAST rows matter most — a COMFAST board the table does not carry
	// must still be refused clearly, and told so in COMFAST-neutral words.
	for _, board := range []string{"comfast,cf-wr632ax-v2", "comfast,cf-rt305ax", "dlink,covr-x1860", "tplink,archer-c7"} {
		_, err := cleanSlateImage(board, "25.12.5")
		if err == nil {
			t.Errorf("%s should have been refused", board)
			continue
		}
		msg := err.Error()
		if strings.Contains(msg, "GL.iNet") || strings.Contains(msg, "GL-iNet") {
			t.Errorf("clean-slate refusal for %q is still GL.iNet-specific: %q", board, msg)
		}
		if !strings.Contains(msg, "flash manually") {
			t.Errorf("clean-slate refusal for %q lost its actionable tail: %q", board, msg)
		}
		if !strings.Contains(msg, board) {
			t.Errorf("clean-slate refusal for %q does not name what the router reported: %q", board, msg)
		}
	}

	// The force-flash path carries the same contract.
	summary, detail := flashModelUnsupportedError("cf-wr632ax")
	if strings.Contains(summary, "GL.iNet") || strings.Contains(detail, "GL.iNet") {
		t.Errorf("force-flash refusal is still GL.iNet-specific: %q / %q", summary, detail)
	}
	if !strings.Contains(summary, "cf-wr632ax") || !strings.Contains(detail, "cf-wr632ax") {
		t.Errorf("force-flash refusal does not name the reported model: %q / %q", summary, detail)
	}
	if !strings.Contains(detail, "flash manually") {
		t.Errorf("force-flash refusal lost its actionable tail: %q", detail)
	}

	// The unreadable-board refusal must not claim a vendor it cannot know.
	if s, d := flashModelUnknownError(); strings.Contains(s, "GL.iNet") || strings.Contains(d, "GL.iNet") {
		t.Errorf("unreadable-board refusal is still GL.iNet-specific: %q / %q", s, d)
	}
}
