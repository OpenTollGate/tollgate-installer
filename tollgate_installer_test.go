package main

import (
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello world", 5, "hello..."},
		{"hello", 5, "hello"},
		{"hello world", 0, "..."}, // Edge case: maxLen = 0
		{"", 5, ""},               // Edge case: empty string
		{"a very long string that needs truncating", 10, "a very lon..."},
		{"string with trailing spaces   ", 20, "string with trailing..."}, // len=27 after trim, so truncated
		{"short", 10, "short"}, // len < maxLen
	}

	for _, test := range tests {
		got := truncate(test.input, test.maxLen)
		if got != test.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", test.input, test.maxLen, got, test.expected)
		}
	}
}

func TestNewJob(t *testing.T) {
	job := newJob("192.168.1.1")

	if job.IP != "192.168.1.1" {
		t.Errorf("newJob IP = %q, want %q", job.IP, "192.168.1.1")
	}
	if job.Status != "running" {
		t.Errorf("newJob Status = %q, want %q", job.Status, "running")
	}
	if job.Step != 0 {
		t.Errorf("newJob Step = %d, want 0", job.Step)
	}
	if len(job.Steps) == 0 {
		t.Error("newJob Steps should not be empty")
	}
}

func TestAddLog(t *testing.T) {
	job := newJob("192.168.1.1")
	initialLen := len(job.Log)

	job.addLog("test message")

	if len(job.Log) != initialLen+1 {
		t.Errorf("addLog: log count = %d, want %d", len(job.Log), initialLen+1)
	}

	logEntry := job.Log[len(job.Log)-1]
	if logEntry.Msg != "test message" {
		t.Errorf("addLog: last message = %q, want %q", logEntry.Msg, "test message")
	}
	if logEntry.Time <= 0 {
		t.Errorf("addLog: time = %f, want > 0", logEntry.Time)
	}
}

func TestSetStep(t *testing.T) {
	job := newJob("192.168.1.1")

	// Test setting a valid step
	job.setStep(0, "done", "test detail")
	if job.Step != 0 {
		t.Errorf("setStep: Step = %d, want 0", job.Step)
	}
	if job.Steps[0].Status != "done" {
		t.Errorf("setStep: Status = %q, want %q", job.Steps[0].Status, "done")
	}
	if job.Steps[0].Detail != "test detail" {
		t.Errorf("setStep: Detail = %q, want %q", job.Steps[0].Detail, "test detail")
	}

	// Test setting step out of bounds
	job.setStep(999, "running", "")
	if job.Step != 0 { // Should remain unchanged
		t.Errorf("setStep: out of bounds should not change Step, got %d", job.Step)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()

	writeError(w, 400, "test error")

	resp := w.Result()
	if resp.StatusCode != 400 {
		t.Errorf("writeError: status code = %d, want 400", resp.StatusCode)
	}

	var respJSON map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&respJSON); err != nil {
		t.Fatalf("writeError: failed to decode JSON: %v", err)
	}

	if respJSON["error"] != "test error" {
		t.Errorf("writeError: error message = %q, want %q", respJSON["error"], "test error")
	}
}

func TestTcpProbe(t *testing.T) {
	// Test with a non-routable IP (should return false)
	result := tcpProbe("192.0.2.1", 80, 100*time.Millisecond)
	if result {
		t.Error("tcpProbe: non-routable IP should return false")
	}

	// Test with invalid port
	result = tcpProbe("192.168.1.1", 99999, 100*time.Millisecond)
	if result {
		t.Error("tcpProbe: invalid port should return false")
	}
}

func TestWaitForRouterAfterFlashTimeout(t *testing.T) {
	// With a non-routable original IP and a short timeout, the polling loop
	// must give up and return an error (not hang, not panic).
	ip, client, err := waitForRouterAfterFlash("192.0.2.1", "pw", 300*time.Millisecond)
	if err == nil {
		t.Fatal("waitForRouterAfterFlash: expected timeout error, got nil")
	}
	if ip != "" {
		t.Errorf("waitForRouterAfterFlash: ip = %q, want empty on timeout", ip)
	}
	if client != nil {
		t.Error("waitForRouterAfterFlash: client should be nil on timeout")
	}
}

func TestJobMutexSafety(t *testing.T) {
	job := newJob("192.168.1.1")

	// Test concurrent access to job
	var wg sync.WaitGroup
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			job.addLog("concurrent message")
			job.setStep(id%3, "running", "")
			done <- true
		}(i)
	}

	wg.Wait()
	close(done)

	// Should have 10 log entries
	if len(job.Log) != 10 {
		t.Errorf("concurrent addLog: expected 10 entries, got %d", len(job.Log))
	}

	// Should not have panicked or had race conditions
}
