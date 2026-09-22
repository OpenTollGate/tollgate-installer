package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDownloadWithRetrySuccess verifies a successful download returns the body
// on the first attempt (no retries needed).
func TestDownloadWithRetrySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()

	data, err := downloadWithRetry(srv.URL, 3, time.Millisecond)
	if err != nil {
		t.Fatalf("downloadWithRetry: unexpected error: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Errorf("downloadWithRetry: got %q, want %q", data, "image-bytes")
	}
}

// TestDownloadWithRetryTransientRetries verifies that a transient failure
// (connection refused) is retried and eventually succeeds.
func TestDownloadWithRetryTransientRetries(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Simulate a transient 500 error on the first two attempts.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	data, err := downloadWithRetry(srv.URL, 3, time.Millisecond)
	if err != nil {
		t.Fatalf("downloadWithRetry: unexpected error after retries: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("downloadWithRetry: got %q, want %q", data, "ok")
	}
	if attempts != 3 {
		t.Errorf("downloadWithRetry: expected 3 attempts, got %d", attempts)
	}
}

// TestDownloadWithRetryDefinitiveNoRetry verifies a 404 is NOT retried — it
// fails immediately (a broken image pin should not waste time).
func TestDownloadWithRetryDefinitiveNoRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := downloadWithRetry(srv.URL, 3, time.Millisecond)
	if err == nil {
		t.Fatal("downloadWithRetry: expected error for 404, got nil")
	}
	if attempts != 1 {
		t.Errorf("downloadWithRetry: 404 should not be retried, got %d attempts", attempts)
	}
}

// TestDownloadWithRetryExhausted verifies that when all attempts fail with a
// transient error, the last error is returned.
func TestDownloadWithRetryExhausted(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := downloadWithRetry(srv.URL, 3, time.Millisecond)
	if err == nil {
		t.Fatal("downloadWithRetry: expected error after exhausting retries, got nil")
	}
	if attempts != 3 {
		t.Errorf("downloadWithRetry: expected 3 attempts, got %d", attempts)
	}
}

// TestIsDefinitiveHTTPError verifies the 4xx-vs-transient classification.
func TestIsDefinitiveHTTPError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errString("HTTP 404 Not Found"), true},
		{errString("HTTP 403 Forbidden"), true},
		{errString("HTTP 410 Gone"), true},
		{errString("HTTP 500 Internal Server Error"), false},
		{errString("HTTP 503 Service Unavailable"), false},
		{errString("connection refused"), false},
		{errString("Get https://x: dial tcp: i/o timeout"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isDefinitiveHTTPError(c.err); got != c.want {
			t.Errorf("isDefinitiveHTTPError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// TestParseSysupgradeError verifies common sysupgrade failure modes map to
// actionable messages.
func TestParseSysupgradeError(t *testing.T) {
	cases := []struct {
		out  string
		want string
	}{
		{"Image check failed: wrong image", "rejected the image"},
		{"invalid image", "rejected the image"},
		{"sysupgrade: not found", "not available"},
		{"no space left on device", "storage is full"},
		{"connection refused", "SSH connection dropped"},
		{"some random output", "sysupgrade output"},
	}
	for _, c := range cases {
		got := parseSysupgradeError(c.out)
		if !strings.Contains(got, c.want) {
			t.Errorf("parseSysupgradeError(%q) = %q, want it to contain %q", c.out, got, c.want)
		}
	}
}

// TestScanSubnetForOpenWrtMalformedIP verifies a malformed base IP returns ""
// without panicking.
func TestScanSubnetForOpenWrtMalformedIP(t *testing.T) {
	if got := scanSubnetForOpenWrt("not-an-ip", "pw", time.Millisecond); got != "" {
		t.Errorf("scanSubnetForOpenWrt(malformed) = %q, want empty", got)
	}
}

// errString is a minimal error implementation for table-driven tests.
type errString string

func (e errString) Error() string { return string(e) }
