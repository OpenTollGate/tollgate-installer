package app

import (
	"os"
	"strings"
	"testing"
)

// The SSH mux blocks inside client.NewSession() with NO timeout on a half-dead
// transport — a router that re-addressed itself mid-deploy. Bounding only the
// command (or only session.Run) left the deploy parked until the stall watchdog
// fired: the observed step-6 hang right after "Package installed", where the
// install's postinst restarted networking and the next probe blocked in
// NewSession. Both the command path and the transport probe must bound the WHOLE
// exchange.
func TestSSHNewSessionAndProbeAreBounded(t *testing.T) {
	src, err := os.ReadFile("ssh.go")
	if err != nil {
		t.Fatalf("read ssh.go: %v", err)
	}
	body := string(src)
	fnBody := func(name string) string {
		i := strings.Index(body, "func "+name+"(")
		if i < 0 {
			t.Fatalf("ssh.go: no func %s", name)
		}
		rest := body[i:]
		if j := strings.Index(rest, "\nfunc "); j >= 0 {
			return rest[:j]
		}
		return rest
	}

	// sshRunE: the session must be created INSIDE the bounded exchange.
	fn := fnBody("sshRunE")
	rb := strings.Index(fn, "runBounded(")
	ns := strings.Index(fn, "client.NewSession()")
	if rb < 0 || ns < 0 || ns < rb {
		t.Fatalf("sshRunE must create the session INSIDE runBounded (runBounded at byte %d, NewSession at %d)", rb, ns)
	}

	// sshTransportAlive: bound the whole probe, not just session.Run.
	probe := fnBody("sshTransportAlive")
	if !strings.Contains(probe, "go func()") || !strings.Contains(probe, "time.After(") {
		t.Fatalf("sshTransportAlive must bound the whole probe (a goroutine + a timeout), NewSession included")
	}
}
