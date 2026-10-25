package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// This file emits the EXACT /api/status payload the wizard serves when a deploy
// loses the TESTER's own wifi association, so the Playwright case in
// physical-router-test-automation can drive a real browser against real wording
// instead of hand-typed copy (card t_27e348b5, acceptance).
//
// Why a fixture emitter rather than a JS literal: the defect was an incorrect
// MESSAGE and an incorrect VERDICT produced by this package. A spec that pasted
// the expected sentence into itself would keep passing after the Go code
// regressed to "failed". Here the payload is produced by the shipped code path
// (handleTransportLoss -> jobPending -> the handleStatus snapshot shape), so a
// regression changes the file the browser reads and the spec fails.
//
// Set TG_TESTER_DROP_FIXTURE=/path/to/out.json to write it. Unset: the test is a
// no-op, so the ordinary `go test ./...` run is unaffected.

// testerDropScenario names the two shapes the e2e needs. The control case is the
// ROUTER-side loss: it must still report a failure, which is what stops the
// browser test from being a vacuous "it never says failed" assertion.
const (
	testerDropTesterSide     = "tester-side"
	testerDropRouterSideFail = "router-side-fail"
)

// statusSnapshot mirrors the anonymous struct handleStatus serialises. Kept
// deliberately explicit (not the Job struct) because the JSON keys are the
// browser contract: note_title is what the pending view renders as its heading.
type statusSnapshot struct {
	IP            string     `json:"ip"`
	Status        string     `json:"status"`
	Step          int        `json:"step"`
	Steps         []Step     `json:"steps"`
	Log           []LogEntry `json:"log"`
	Error         string     `json:"error,omitempty"`
	Note          string     `json:"note,omitempty"`
	NoteTitle     string     `json:"note_title,omitempty"`
	ProgressLabel string     `json:"progressLabel,omitempty"`
}

// snapshotOfJob reproduces handleStatus's payload for a finished job. It reads
// the same fields under the same lock, so it cannot drift from the endpoint
// without the browser fixture changing too.
func snapshotOfJob(job *Job) statusSnapshot {
	job.mu.Lock()
	defer job.mu.Unlock()
	return statusSnapshot{
		IP:        job.IP,
		Status:    job.Status,
		Step:      job.Step,
		Steps:     job.Steps,
		Log:       job.Log,
		Error:     job.Error,
		Note:      job.Note,
		NoteTitle: job.NoteTitle,
	}
}

// TestEmitTesterDropFixtures writes the e2e fixtures when asked. It also pins
// that the tester-side payload is genuinely non-fatal, right here, so the fixture
// can never be emitted from a failing job.
func TestEmitTesterDropFixtures(t *testing.T) {
	dir := os.Getenv("TG_TESTER_DROP_FIXTURE")
	if dir == "" {
		t.Skip("TG_TESTER_DROP_FIXTURE not set — fixture emission is opt-in")
	}

	// Case 1: the card's scenario. A laptop on wifi drives the deploy; the
	// deploy reconfigures the router's wireless; the laptop roams to another
	// subnet and the SSH session dies at the health gate.
	before := testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "192.168.1.20/24"}
	after := testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "10.2.124.120/16"}

	prevNow, prevDial, prevRejoin := testerUplinkNow, routerDialableFn, testerWiFiRejoinFn
	testerUplinkNow = func() testerUplinkState { return after }
	routerDialableFn = func(string) bool { return false }
	testerWiFiRejoinFn = func(ssid string) (bool, string) {
		if ssid == "c08r4d0r-AB12" {
			return false, "this machine has no saved NetworkManager profile for c08r4d0r-AB12, so it was not rejoined automatically."
		}
		return false, "no saved profile for " + ssid
	}
	t.Cleanup(func() { testerUplinkNow, routerDialableFn, testerWiFiRejoinFn = prevNow, prevDial, prevRejoin })

	dropJob := &Job{IP: "192.168.1.1", Status: "running", Steps: deploySteps()}
	for i := 0; i <= 10; i++ {
		dropJob.Steps[i].Status = "done"
		dropJob.Steps[i].Detail = "ok"
	}
	dropJob.setStep(11, "running", "")
	handleTransportLoss(dropJob, 11, "192.168.1.1",
		errText("ssh: session run error: connection reset by peer"),
		before, true, rejoinTargets{Private: "c08r4d0r-AB12", Captive: "!TollGate-AB12"})

	if got := dropJob.Status; got != "pending" {
		t.Fatalf("fixture emitted from a %q job — the tester-side case MUST be non-fatal", got)
	}
	writeFixture(t, filepath.Join(dir, testerDropTesterSide+".json"), snapshotOfJob(dropJob))

	// Case 2 (control): the ROUTER stopped answering while this machine's link
	// stayed put. This must remain a failure — otherwise the browser test only
	// proves "the word failed is absent", not that a real fault is still
	// reported.
	testerUplinkNow = func() testerUplinkState { return before }
	routerDialableFn = func(string) bool { return true }
	failJob := &Job{IP: "192.168.1.1", Status: "running", Steps: deploySteps()}
	for i := 0; i <= 10; i++ {
		failJob.Steps[i].Status = "done"
		failJob.Steps[i].Detail = "ok"
	}
	failJob.setStep(11, "running", "")
	handleTransportLoss(failJob, 11, "192.168.1.1",
		errText("ssh: session run error: connection reset by peer"),
		before, true, rejoinTargets{Private: "c08r4d0r-AB12", Captive: "!TollGate-AB12"})

	if got := failJob.Status; got != "failed" {
		t.Fatalf("control fixture must be a failure, got %q", got)
	}
	writeFixture(t, filepath.Join(dir, testerDropRouterSideFail+".json"), snapshotOfJob(failJob))
}

// errText is a tiny error so this file needs no fmt/errors import churn, and
// does not collide with the package's existing errString helper.
type errText string

func (e errText) Error() string { return string(e) }

func writeFixture(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	t.Logf("wrote %s", path)
}
