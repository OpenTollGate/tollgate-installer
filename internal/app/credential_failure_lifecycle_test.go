package app

// Failure-path half of the credential lockout fix on
// OpenTollGate/tollgate-installer#46 (merged head acf8a64c; re-review
// t_d84d4928, finding 2): "the failure path drops a generated credential the
// deploy already set".
//
// The one-shot credential serve lives in handleStatus / main.go and its
// success-path contract is pinned by TestStatusServesGeneratedPasswordExactlyOnce
// (credential_lifecycle_test.go) — this file EXTENDS that one mechanism to the
// failure case rather than adding a second serve path, and pins the failure-side
// contract the same way. A deploy can fail AFTER step 4 already generated and SET
// a root password on the router (step 6's package download, the health probe, the
// STA reconnect...); while the value was served only for job.Status == "done",
// that router kept a credential whose only channel never fired — the operator was
// locked out of the router the wizard had just changed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatusServesGeneratedPasswordOnFailureExactlyOnce pins the endpoint
// contract for a FAILED job: the credential is served on the first read of the
// job in a TERMINAL state, never again, and the server drops it after that read.
// Mid-run reads still must not consume it (the UI polls every second).
func TestStatusServesGeneratedPasswordOnFailureExactlyOnce(t *testing.T) {
	const (
		jobID = "failed-one-shot-credential-job"
		pw    = "Sup3rSecretRootPw1234"
	)
	job := newJob("192.168.8.1")
	job.setGeneratedPassword(pw)

	registerStatusJob(t, jobID, job)

	// Running: nothing served, and nothing consumed.
	if got := readStatus(t, jobID)["generated_password"]; got != nil && got != "" {
		t.Fatalf("generated_password served while the job was still running (%v) — the UI's 1 s polling would consume it before any final view", got)
	}

	// The deploy fails AFTER the credential was set on the router.
	job.mu.Lock()
	job.Status = "failed"
	job.Error = "step 6: could not download the tollgate-wrt package"
	job.mu.Unlock()

	first := readStatus(t, jobID)
	if first["status"] != "failed" {
		t.Fatalf("status = %v, want failed", first["status"])
	}
	if first["generated_password"] != pw {
		t.Fatalf("a FAILED deploy that had already generated and SET a root password did not surrender it (got %v): the router keeps the credential and the operator never sees it — a lockout",
			first["generated_password"])
	}
	for poll := 2; poll <= 4; poll++ {
		if got := readStatus(t, jobID)["generated_password"]; got != nil && got != "" {
			t.Fatalf("read %d returned the credential again (%v) — the endpoint must serve it at most once", poll, got)
		}
	}
	job.mu.Lock()
	held := job.generatedPassword
	served := job.generatedPasswordServed
	job.mu.Unlock()
	if held != "" {
		t.Fatalf("the server still holds the credential (%q) after the one-shot read — it must be dropped", held)
	}
	if !served {
		t.Fatal("the one-shot flag was not set on the failure read; a later read would serve the credential again")
	}
}

// TestStatusDoesNotServeACredentialOnAFailureThatNeverSet implements the other
// side of the same rule: a deploy that failed BEFORE step 4 (or failed without
// having generated anything) has no credential to surrender, and the failure
// view must not be handed an empty-but-present field to render.
func TestStatusDoesNotServeACredentialOnAFailureThatNeverSet(t *testing.T) {
	const jobID = "failed-before-credential-job"
	job := newJob("192.168.8.1")
	job.mu.Lock()
	job.Status = "failed"
	job.Error = "Cannot connect to router via SSH"
	job.mu.Unlock()
	registerStatusJob(t, jobID, job)

	if got := readStatus(t, jobID)["generated_password"]; got != nil && got != "" {
		t.Fatalf("a failure that never set a credential served generated_password = %v", got)
	}
}

// registerStatusJob publishes a job under jobID for the duration of the test.
func registerStatusJob(t *testing.T, jobID string, job *Job) {
	t.Helper()
	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()
	t.Cleanup(func() {
		jobsMutex.Lock()
		delete(jobs, jobID)
		jobsMutex.Unlock()
	})
}

// readStatus performs one GET on /api/status/<jobID> and returns the decoded
// body.
func readStatus(t *testing.T, jobID string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status/"+jobID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("handleStatus status = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status body: %v", err)
	}
	return body
}
