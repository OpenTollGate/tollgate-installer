package app

import (
	"testing"
)

func TestSshRunFunction(t *testing.T) {
	// This tests sshRun indirectly by testing the functions that call it
	// We can't easily test sshRun directly without actual SSH connections

	// Test that sshRun is called indirectly through sshIdentify
	// This is a simple smoke test to ensure the function exists and doesn't crash
	// when called with empty credentials

	firmware, vendor, model := sshIdentify("192.168.1.1", "")
	// Result will be empty strings, but the function should not panic
	if len(firmware) != 0 || len(vendor) != 0 || len(model) != 0 {
		t.Errorf("sshIdentify with empty credentials should return empty strings, got firmware=%q, vendor=%q, model=%q", firmware, vendor, model)
	}
}

func TestDiscoverRoutersNoNetwork(t *testing.T) {
	// Test discoverRouters with no network connectivity
	// This will test the basic structure without actual network calls

	routers := discoverRouters()
	// Should return empty array or routers with minimal info when no network
	// The important thing is that the function doesn't crash
	if routers == nil {
		t.Error("discoverRouters should not return nil")
	}

	// Verify each router has the expected structure
	for _, router := range routers {
		if router.IP == "" {
			t.Error("Router should have non-empty IP")
		}
		if router.Vendor == "" {
			t.Error("Router should have vendor info")
		}
	}
}

func TestEmptyJobOperations(t *testing.T) {
	// Test edge cases with empty/invalid job data

	job := newJob("")
	if job.IP != "" {
		t.Errorf("Job with empty IP should have IP = '', got %q", job.IP)
	}

	job.setStep(0, "done", "")
	if job.Step != 0 {
		t.Errorf("Job step should be 0, got %d", job.Step)
	}

	// Test setting step out of bounds
	job.setStep(999, "done", "")
	// Should not crash, should not change step out of bounds
	if job.Step > 0 {
		t.Errorf("Setting step out of bounds should not change current step, got %d", job.Step)
	}
}
