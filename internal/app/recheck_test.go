package app

import (
	"os"
	"strings"
	"testing"
)

func TestRecheckCandidatesUsesRequestJobAndDiscovery(t *testing.T) {
	withRouterCandidates(t, []string{"10.95.98.1", "192.168.2.1"})
	got := strings.Join(recheckCandidates("192.168.1.1", "192.168.1.1"), ",")
	want := "192.168.1.1,10.95.98.1,192.168.2.1"
	if got != want {
		t.Fatalf("recheckCandidates = %q, want %q", got, want)
	}
}

func TestRecheckCredentialPrefersRequestThenDeployThenGenerated(t *testing.T) {
	job := &Job{
		deployPassword:      "deploy",
		pendingRootPassword: "pending",
		generatedPassword:   "generated",
	}
	if got := recheckCredential("typed", job); got != "typed" {
		t.Fatalf("explicit credential = %q, want typed", got)
	}
	if got := recheckCredential("", job); got != "deploy" {
		t.Fatalf("stored deploy credential = %q, want deploy", got)
	}
	job.deployPassword = ""
	if got := recheckCredential("", job); got != "pending" {
		t.Fatalf("pending credential = %q, want pending", got)
	}
	job.pendingRootPassword = ""
	if got := recheckCredential("", job); got != "generated" {
		t.Fatalf("generated credential = %q, want generated", got)
	}
}

// The pending Re-check must not reuse the pre-move address alone: it has to
// carry the job id (for the stored credential) and fall back to a fresh scan.
func TestRecheckHandlerUsesJobAndDiscovery(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"JobID    string `json:\"job_id\"`",
		"recheckCredential(req.Password, job)",
		"recheckCandidates(req.IP, jobIP)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("handleRecheck wiring missing %q", want)
		}
	}
	ui, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(ui), "job_id: currentJobId") {
		t.Error("Re-check button must send the current job id")
	}
}
