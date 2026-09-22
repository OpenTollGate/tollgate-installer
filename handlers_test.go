package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandleStatus(t *testing.T) {
	// First create a test job
	jobID := "test-job-123"
	job := newJob("192.168.1.1")
	job.Status = "done"
	job.Step = 2
	job.Steps[0].Status = "done"
	job.Steps[0].Detail = "test detail"
	job.addLog("test log message")

	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()

	req := httptest.NewRequest("GET", "/api/status/"+jobID, nil)
	w := httptest.NewRecorder()

	handleStatus(w, req)

	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("handleStatus: status code = %d, want 200", resp.StatusCode)
		return
	}

	var respJSON map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("handleStatus: failed to decode JSON: %v", err)
	}

	// Check if the fields are present and have correct types (lowercase!)
	ipVal, ok := respJSON["ip"].(string)
	if !ok || ipVal != "192.168.1.1" {
		t.Errorf("handleStatus: ip = %v (%T), want 192.168.1.1 (string)", respJSON["ip"], respJSON["ip"])
	}

	statusVal, ok := respJSON["status"].(string)
	if !ok || statusVal != "done" {
		t.Errorf("handleStatus: status = %v (%T), want done (string)", respJSON["status"], respJSON["status"])
	}

	stepVal, ok := respJSON["step"].(float64)
	if !ok || stepVal != 2 {
		t.Errorf("handleStatus: step = %v (%T), want 2 (float64)", respJSON["step"], respJSON["step"])
	}

	// Test non-existent job
	req2 := httptest.NewRequest("GET", "/api/status/nonexistent", nil)
	w2 := httptest.NewRecorder()

	handleStatus(w2, req2)

	resp2 := w2.Result()
	if resp2.StatusCode != 404 {
		t.Errorf("handleStatus: nonexistent job status = %d, want 404", resp2.StatusCode)
	}
}

func TestHandleDeploy(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "invalid JSON",
			body:           "invalid json {",
			expectedStatus: 400,
			expectedError:  "invalid JSON",
		},
		{
			name:           "missing IP",
			body:           `{"ip": "", "password": "secretpw", "lnurl": "test@wallet.app"}`,
			expectedStatus: 400,
			expectedError:  "IP required",
		},
		{
			// Fresh-reset OpenWrt routers ship with an empty root password —
			// the deploy API must accept an empty password (see sshConnect).
			name:           "empty password allowed (fresh reset)",
			body:           `{"ip": "192.168.1.1", "password": "", "lnurl": "test@wallet.app", "mode": "wan"}`,
			expectedStatus: 200,
			expectedError:  "",
		},
		{
			name:           "invalid lightning address",
			body:           `{"ip": "192.168.1.1", "password": "password", "lnurl": "invalid"}`,
			expectedStatus: 400,
			expectedError:  "a valid Lightning address is required",
		},
		{
			name:           "valid request",
			body:           `{"ip": "192.168.1.1", "password": "password", "lnurl": "test@wallet.app", "mode": "wan"}`,
			expectedStatus: 200,
			expectedError:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/deploy", strings.NewReader(tt.body))
			w := httptest.NewRecorder()

			handleDeploy(w, req)

			resp := w.Result()
			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("%s: status code = %d, want %d", tt.name, resp.StatusCode, tt.expectedStatus)
				return
			}

			if tt.expectedError != "" {
				var respJSON map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
					t.Fatalf("%s: failed to decode JSON: %v", tt.name, err)
				}
				if respJSON["error"] != tt.expectedError {
					t.Errorf("%s: error = %q, want %q", tt.name, respJSON["error"], tt.expectedError)
				}
			} else {
				var respJSON map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
					t.Fatalf("%s: failed to decode JSON: %v", tt.name, err)
				}
				if _, ok := respJSON["job_id"]; !ok {
					t.Errorf("%s: response should have job_id", tt.name)
				}
			}
		})
	}
}

func TestDeployRequestPreStageRoundTrip(t *testing.T) {
	// Task 4: the UI checkbox sends preStage in the deploy POST body; the
	// field must survive JSON decode into deployRequest so runDeployment
	// can act on it (req.PreStage → runPreStage). Pin both directions plus
	// the default (absent → false).
	body := `{"ip":"192.168.1.1","password":"pw","lnurl":"a@b.c","mode":"sta","preStage":true}`
	var req deployRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode with preStage: %v", err)
	}
	if !req.PreStage {
		t.Error("preStage=true in JSON body decoded to false")
	}

	// Absent field must default to false (checkbox unchecked / WAN mode).
	// NOTE: unmarshal into a FRESH struct — Go's json.Unmarshal does not
	// zero absent fields, so reusing req would leak PreStage=true above.
	bodyNoFlag := `{"ip":"192.168.1.1","password":"pw","lnurl":"a@b.c","mode":"wan"}`
	var reqNoFlag deployRequest
	if err := json.Unmarshal([]byte(bodyNoFlag), &reqNoFlag); err != nil {
		t.Fatalf("decode without preStage: %v", err)
	}
	if reqNoFlag.PreStage {
		t.Error("absent preStage decoded to true — WAN deploy would wrongly pre-stage")
	}

	// Marshal round-trip: the wire name is the lower-case preStage.
	out, err := json.Marshal(deployRequest{IP: "192.168.1.1", PreStage: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"preStage":true`) {
		t.Errorf("marshal output %s missing lower-case preStage:true", out)
	}
}

func TestMainFunctionStructure(t *testing.T) {
	// Test that the main function sets up routes correctly
	// This is a basic structural test since we can't easily run the main function

	// The wizard must default to LOOPBACK binding (127.0.0.1), not a
	// wildcard ":<port>" — the deploy API drives a root SSH session on the
	// router, so exposing it on all interfaces would let any LAN host drive
	// deploys. This guards the security fix (previously ":8099").
	if got := listenAddress(*listenBind, "8099"); got != "127.0.0.1:8099" {
		t.Errorf("listenAddress(bind=%q, 8099) = %q, want %q (loopback default)", *listenBind, got, "127.0.0.1:8099")
	}

	// Check that the default listen port is properly set. listenAddr itself is
	// only assigned inside main() (":8099" from the flag default), so it is
	// always "" in a test binary — assert the flag default, the real source of
	// truth for the listen address.
	if *listenPort != "8099" {
		t.Errorf("listenPort default = %q, want %q", *listenPort, "8099")
	}

	// An empty --bind must still resolve to loopback.
	if got := listenAddress("", "8099"); got != "127.0.0.1:8099" {
		t.Errorf("listenAddress(\"\", 8099) = %q, want loopback default %q", got, "127.0.0.1:8099")
	}

	// Verify the regex patterns are compiled
	if lnAddrRe == nil {
		t.Error("lnAddrRe should be compiled")
	}
	if lnurlRe == nil {
		t.Error("lnurlRe should be compiled")
	}
}

func TestJobStructFields(t *testing.T) {
	job := newJob("192.168.1.1")

	// Test that all expected fields are present and initialized
	if job.IP == "" {
		t.Error("Job.IP should not be empty")
	}
	if job.Steps == nil {
		t.Error("Job.Steps should not be nil")
	}
	if job.Log == nil {
		t.Error("Job.Log should not be nil")
	}
	// Mutex is a sync.RWMutex, check it's not zero value by trying to lock it
	job.mu.Lock()
	defer job.mu.Unlock()

	// If we get here without panic, the mutex is properly initialized
}

func TestDeployStepStruct(t *testing.T) {
	step := Step{Name: "test", Desc: "test description", Status: "pending"}

	if step.Name != "test" {
		t.Errorf("Step.Name = %q, want %q", step.Name, "test")
	}
	if step.Desc != "test description" {
		t.Errorf("Step.Desc = %q, want %q", step.Desc, "test description")
	}
	if step.Status != "pending" {
		t.Errorf("Step.Status = %q, want %q", step.Status, "pending")
	}
}

func TestLogEntryStruct(t *testing.T) {
	entry := LogEntry{Time: float64(time.Now().Unix()), Msg: "test log"}

	if entry.Msg != "test log" {
		t.Errorf("LogEntry.Msg = %q, want %q", entry.Msg, "test log")
	}
	if entry.Time <= 0 {
		t.Errorf("LogEntry.Time = %f, want > 0", entry.Time)
	}
}

func TestConcurrentJobOperations(t *testing.T) {
	job := newJob("192.168.1.1")

	var wg sync.WaitGroup

	// Test concurrent addLog operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			job.addLog("concurrent log message")
		}(i)
	}

	wg.Wait()

	// Should have exactly 10 log entries
	if len(job.Log) != 10 {
		t.Errorf("Expected 10 log entries, got %d", len(job.Log))
	}

	// Test concurrent setStep operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(stepNum int) {
			defer wg.Done()
			job.setStep(stepNum%3, "done", "test detail")
		}(i)
	}

	wg.Wait()

	// Should not have panicked, should have some steps updated
	// The exact step count depends on timing, but no crash is the key
	if len(job.Steps) == 0 {
		t.Error("Steps should not be empty after concurrent operations")
	}
}
