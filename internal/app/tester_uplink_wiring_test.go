package app

import (
	"os"
	"strings"
	"testing"
)

// Wiring pin: the verdict must actually be REACHED. A correct classifier that no
// deploy path calls leaves the defect exactly where it was, so this reads
// deploy.go and requires every transport-loss site to go through
// handleTransportLoss. It fails against the pre-fix file, which called
// failTransportLost directly at all five sites — that RED run was observed
// before the wiring landed (see the card's evidence).
func TestDeployTransportLossSitesUseTheVerdict(t *testing.T) {
	src, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("read deploy.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "failTransportLost(job,") {
		t.Error("a deploy path still calls failTransportLost directly — it cannot tell a tester-side loss from a router-side one; route it through handleTransportLoss")
	}
	for _, step := range []string{"0", "10", "11"} {
		if !strings.Contains(body, "handleTransportLoss(job, "+step+",") {
			t.Errorf("no handleTransportLoss call at step %s — that site still blames the router for a tester-side loss", step)
		}
	}
	// The uplink must be read BEFORE anything changes on the router, otherwise a
	// loss cannot be attributed: the "after" read would already be the new path.
	if !strings.Contains(body, "uplinkBefore := testerUplinkNow()") {
		t.Error("deploy.go must snapshot the tester's uplink before touching the router")
	}
	// The snapshot must precede the first remote command.
	if iBefore, iFirstSSH := strings.Index(body, "uplinkBefore := testerUplinkNow()"), strings.Index(body, "fwOut := sshRun(client,"); iBefore < 0 || iFirstSSH < 0 || iBefore > iFirstSSH {
		t.Error("the uplink snapshot must be taken before the first remote command")
	}
}
