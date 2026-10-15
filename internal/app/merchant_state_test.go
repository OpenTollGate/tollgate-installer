package app

import "testing"

// merchantState is the pure decision the step-11 gate is built on: it must
// never call a starting merchant "unknown" (which the gate treats as a hard
// failure) and must never call a real advertisement "starting".
func TestMerchantState(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"advertisement (kind)", `{"kind":10021,"metric":"bytes","pubkey":"abc"}`, merchantHealthy},
		{"advertisement (metric)", `{"metric": "bytes"}`, merchantHealthy},
		{"advertisement (pubkey)", `{"pubkey":"deadbeef"}`, merchantHealthy},
		{"starting code", `{"error":"...","code":"starting","retry_after":5}`, merchantStarting},
		{"starting message", `This TollGate is starting up: the payment API is up, but the wallet and the mint health are still loading. Please retry in a few seconds.`, merchantStarting},
		{"still starting", `tollgate is still starting: the wallet and the mint health are not loaded yet`, merchantStarting},
		{"503 status line only", "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\n\r\n", merchantStarting},
		{"empty", "", merchantUnknown},
		{"garbage", "not json at all", merchantUnknown},
		// The uclient-fetch refusal that shipped the "Setup failed" bug: a probe
		// that passed an unsupported flag captured this usage text instead of the
		// API body. It must classify as unknown — NEVER as healthy — so the
		// regression cannot masquerade as a good merchant.
		{"wget usage error (unsupported flag)", "wget: unrecognized option: S\nUsage: wget [options] <URL>\nOptions:\n\t-4\t\t\t\tUse IPv4 only\n\t-6\t\t\t\tUse IPv6 only\n\t-O <file>\t\t\tRedirect output to file (use \"-\" for stdout)\n", merchantUnknown},
	}
	for _, c := range cases {
		if got := merchantState(c.body); got != c.want {
			t.Errorf("%s: merchantState(%q) = %q, want %q", c.name, c.body, got, c.want)
		}
	}
}

// A job ending "pending" is NOT a failure: it must not carry an Error, the step
// must be the non-red "warn" mark, and the Note must be set so the UI can show
// the amber banner. This is the whole point of the state — a slow merchant must
// not read as a broken install.
func TestJobPendingIsNonFatal(t *testing.T) {
	job := &Job{Status: "running", Steps: make([]Step, 14)}
	job.Steps[11].Status = "running"
	jobPending(job, 11, "merchant starting (wallet/mints still loading)", "the merchant is still starting")
	if job.Status != "pending" {
		t.Fatalf("Status = %q, want pending", job.Status)
	}
	if job.Error != "" {
		t.Errorf("a pending job must not carry an Error, got %q", job.Error)
	}
	if job.Note == "" {
		t.Errorf("a pending job must carry a Note")
	}
	if job.Steps[11].Status != "warn" {
		t.Errorf("step 11 status = %q, want warn", job.Steps[11].Status)
	}
}
