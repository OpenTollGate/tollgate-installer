package main

import (
	"errors"
	"strings"
	"testing"
)

// These four behaviours exist because of a LIVE defect on a real GL-MT3000
// (2026-10-03, rc14): the deploy lost the router mid-run, then reported
// "tollgate-wrt service is NOT listening on :2121 (crash-looping or still
// initializing)" for forty 5-second attempts, with a diagnostics block whose
// every label was empty —
//
//	listening=false ad=""
//	service:
//	proc:
//	date:
//	mints:
//	internet:
//	dns:
//	log:
//	debug:
//
// — and a step list that still read pending/running for steps 6..11 while the
// job had failed at step 11. The operator chased a service fault that did not
// exist; the router had simply left the LAN.

// TestOverlaySpaceVerdictRefusesA16MBDevice: the live 16MB GL-AR300M16 reported
// 8,016 KiB free on /overlay while opkg needed 23,850 KiB. Today the wizard
// rotates/holds the credential and only then finds out — this must be a refusal
// BEFORE step 4.
func TestOverlaySpaceVerdictRefusesA16MBDevice(t *testing.T) {
	ok, msg := overlaySpaceVerdict(8016)
	if ok {
		t.Fatalf("a router that cannot fit the payload must be refused before the credential step; got ok=true (%s)", msg)
	}
	if !strings.Contains(msg, "8016") || !strings.Contains(msg, "25600") {
		t.Errorf("the refusal must name both numbers so the operator can act: %q", msg)
	}
}

func TestOverlaySpaceVerdictAllowsANandClassRouter(t *testing.T) {
	ok, msg := overlaySpaceVerdict(188900) // live GL-MT3000 /overlay: 188.9M available
	if !ok {
		t.Fatalf("a NAND-class router must pass the pre-flight: %s", msg)
	}
}

func TestOverlaySpaceVerdictNeverRefusesOnAMeasurementWeDoNotHave(t *testing.T) {
	ok, msg := overlaySpaceVerdict(-1)
	if !ok {
		t.Fatalf("an unreadable /overlay must not block the deploy: %s", msg)
	}
	if !strings.Contains(msg, "skipped") {
		t.Errorf("an unknown size must say the check was skipped: %q", msg)
	}
}

func TestParseAvailKiB(t *testing.T) {
	in := "Filesystem           1K-blocks      Used Available Use% Mounted on\n" +
		"/dev/ubi0_2             209920     11684    193412   6% /overlay\n"
	if got := parseAvailKiB(in); got != 193412 {
		t.Fatalf("parseAvailKiB = %d, want 193412", got)
	}
	if got := parseAvailKiB(""); got != -1 {
		t.Fatalf("no df output must be -1 (unknown), got %d", got)
	}
	if got := parseAvailKiB("df: /overlay: No such file or directory"); got != -1 {
		t.Fatalf("an error must be -1 (unknown), got %d", got)
	}
}

// TestTransportLostDiagnosticsNeverPrintsAnEmptyLabel pins the exact regression:
// the operator must be told the session died, not shown nine blank fields.
func TestTransportLostDiagnosticsNeverPrintsAnEmptyLabel(t *testing.T) {
	got := transportLostDiagnostics(errors.New("ssh: session run error"))
	if !strings.Contains(got, "transport:") {
		t.Fatalf("a dead transport must be named: %q", got)
	}
	if !strings.Contains(got, "ssh: session run error") {
		t.Errorf("the underlying error must be carried through: %q", got)
	}
	for _, label := range []string{"service:", "proc:", "mints:", "dns:", "log:", "debug:"} {
		if strings.Contains(got, "\n"+label) || strings.HasPrefix(got, label) {
			t.Errorf("a dead-transport block must not print %q — nothing was observed: %q", label, got)
		}
	}
}

// TestDiagFieldSaysUnavailableRatherThanNothing: an empty result from a LIVE
// session is still a result — it must not render as a bare label.
func TestDiagFieldSaysUnavailableRatherThanNothing(t *testing.T) {
	if got := diagField("service", "   ", 200); !strings.Contains(got, "unavailable") {
		t.Fatalf("empty command output must read as unavailable, got %q", got)
	}
	if got := diagField("service", "running\n", 200); got != "service: running" {
		t.Fatalf("diagField = %q, want %q", got, "service: running")
	}
}

// TestFailTransportLostLeavesAConsistentStepList: the live job failed at step 11
// while steps 6..11 still read pending/running.
func TestFailTransportLostLeavesAConsistentStepList(t *testing.T) {
	job := &Job{Steps: deploySteps(), Status: "running"}
	job.setStep(6, "running", "")
	failTransportLost(job, 11, "192.168.1.1", errors.New("connection reset by peer"))
	if job.Status != "failed" {
		t.Fatalf("job.Status = %q, want failed", job.Status)
	}
	if job.Error == "" {
		t.Fatal("a lost router must carry an operator-readable error")
	}
	if !strings.Contains(job.Error, "192.168.1.1") {
		t.Errorf("the error must name the address that stopped answering: %q", job.Error)
	}
	for i, s := range job.Steps {
		if s.Status == "pending" || s.Status == "running" {
			t.Errorf("step %d (%s) still reads %q — the list must match the terminal step", i, s.Name, s.Status)
		}
	}
	if job.Steps[6].Detail == "" {
		t.Error("a step that never ran must say why")
	}
}
