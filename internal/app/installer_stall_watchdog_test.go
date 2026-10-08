package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Live defect 2026-10-05 (rc17, 82cd11d): the deploy sat at step 1
// ("Pre-downloading packages and firmware...") FOREVER after the log line
// "SSH OK. Firmware: ...", with progressTotal 0 and status still "running".
//
// The router had answered the firmware probe and then gone silent. sshRun
// called session.CombinedOutput with no deadline, so the deploy goroutine
// parked there permanently and nothing ever surfaced it. Both halves of the
// fix are pinned here: (A) a remote command can no longer outlive its
// deadline, and (B) a job that stops making progress is failed with a reason
// the operator can act on, instead of spinning.
//
// Forensics: tollgate-development/references/stuck-deploy-hang-forensics.md

// ---- Part A: a bounded remote command ----

// TestRunBoundedReturnsTheResultOfTheFunction: the bound must be invisible to a
// command that answers in time.
func TestRunBoundedReturnsTheResultOfTheFunction(t *testing.T) {
	out, err := runBounded(2*time.Second, "true", func() ([]byte, error) {
		return []byte("glinet,gl-mt3000\n"), nil
	})
	if err != nil {
		t.Fatalf("a command that answers must not error: %v", err)
	}
	if string(out) != "glinet,gl-mt3000\n" {
		t.Fatalf("runBounded lost the command output: %q", out)
	}
}

// TestRunBoundedCarriesTheFunctionError: a genuine transport/Cmd error must not
// be replaced by a timeout error — the deploy's message must stay truthful.
func TestRunBoundedCarriesTheFunctionError(t *testing.T) {
	want := errors.New("ssh: connection reset by peer")
	_, err := runBounded(2*time.Second, "true", func() ([]byte, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("runBounded = %v, want the function's own error %v", err, want)
	}
}

// TestRunBoundedGivesUpOnACallThatNeverReturns is THE regression: the blocking
// call must not be able to park its caller, and the error must name the
// deadline so the deploy can turn it into an operator-readable failure.
func TestRunBoundedGivesUpOnACallThatNeverReturns(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)

	start := time.Now()
	_, err := runBounded(50*time.Millisecond, "cat /tmp/sysinfo/board_name 2>/dev/null", func() ([]byte, error) {
		<-blocked // the wedged router: the read never returns
		return nil, nil
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a call that never returns must time out — a nil error is the rc17 infinite spinner")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runBounded waited %s on a 50ms deadline — the caller was still parked", elapsed)
	}
	if !strings.Contains(err.Error(), "50ms") {
		t.Errorf("the error must name the deadline that expired: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "answering") {
		t.Errorf("the error must say the router stopped answering, not a bare 'timeout': %v", err)
	}
}

// TestSSHCommandTimeoutIsBoundedAndGenerous: the default deadline must exist,
// and must be long enough that a merely slow router is never failed by it.
func TestSSHCommandTimeoutIsBoundedAndGenerous(t *testing.T) {
	if sshCommandTimeout <= 0 {
		t.Fatal("sshCommandTimeout must be a positive deadline — 0 is the unbounded defect")
	}
	if sshCommandTimeout < 10*time.Second {
		t.Errorf("sshCommandTimeout = %s is too tight: a slow opkg/apk install would be failed as a hang", sshCommandTimeout)
	}
	if sshCommandTimeout > time.Minute {
		t.Errorf("sshCommandTimeout = %s is too long for an operator waiting on a wedged router", sshCommandTimeout)
	}
}

// TestSSHRunERefusesANilClient: the old sshRun dereferenced a nil client and
// panicked the whole installer (see the sshRun doc comment). The bounded form
// returns a reason the deploy can put in the job error instead.
func TestSSHRunERefusesANilClient(t *testing.T) {
	_, err := sshRunE(nil, "true")
	if err == nil {
		t.Fatal("a nil client must be an error, not a panic")
	}
	if !strings.Contains(err.Error(), "lost") {
		t.Errorf("the nil-client error must say the connection was lost: %v", err)
	}
}

// ---- Part B: the no-progress watchdog ----

// stalledJob builds a running job whose last observable progress is old.
func stalledJob(age time.Duration, step int) *Job {
	job := newJob("10.117.96.1")
	job.setStep(step, "running", "")
	job.mu.Lock()
	job.lastActivity = time.Now().Add(-age)
	job.mu.Unlock()
	return job
}

// TestFailIfStalledFailsAJobThatMadeNoProgress: the observable end state the
// operator never got on rc17 — a failed job with a reason.
func TestFailIfStalledFailsAJobThatMadeNoProgress(t *testing.T) {
	job := stalledJob(2*time.Minute, 1)

	if !failIfStalled(job, time.Minute) {
		t.Fatal("a job with no progress for longer than the limit must be failed")
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != "failed" {
		t.Fatalf("job.Status = %q, want failed", job.Status)
	}
	if job.Error == "" {
		t.Fatal("a stalled job must carry an operator-readable error")
	}
	// The operator needs to know WHERE it stopped and WHAT was not progressing.
	if !strings.Contains(job.Error, job.Steps[1].Desc) {
		t.Errorf("the error must name the step that stopped progressing (%q): %q", job.Steps[1].Desc, job.Error)
	}
	if !strings.Contains(job.Error, "1m0s") && !strings.Contains(job.Error, "60s") {
		t.Errorf("the error must name how long nothing happened: %q", job.Error)
	}
	if !strings.Contains(strings.ToLower(job.Error), "power-cycle") {
		t.Errorf("the error must carry the recovery action (power-cycle the router): %q", job.Error)
	}
}

// TestFailIfStalledLeavesAWorkingJobAlone: a job that is still making progress
// must never be failed by the watchdog.
func TestFailIfStalledLeavesAWorkingJobAlone(t *testing.T) {
	job := stalledJob(0, 1)
	if failIfStalled(job, time.Minute) {
		t.Fatal("a job that just logged progress must not be failed")
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != "running" || job.Error != "" {
		t.Fatalf("job = %q / %q, want running with no error", job.Status, job.Error)
	}
}

// TestFailIfStalledNeverTouchesATerminalJob: the watchdog reads the job, then
// decides. A deploy that SUCCEEDED (or already failed) in that window must not
// be overwritten by a stale stall verdict — that would turn a good deploy into
// a reported failure.
func TestFailIfStalledNeverTouchesATerminalJob(t *testing.T) {
	for _, terminal := range []string{"done", "failed"} {
		job := stalledJob(2*time.Minute, 1)
		job.mu.Lock()
		job.Status = terminal
		job.mu.Unlock()

		if failIfStalled(job, time.Minute) {
			t.Errorf("a %s job must never be reported as stalled", terminal)
		}
		job.mu.Lock()
		got, errMsg := job.Status, job.Error
		job.mu.Unlock()
		if got != terminal {
			t.Errorf("job.Status = %q, want %q left untouched", got, terminal)
		}
		if errMsg != "" {
			t.Errorf("a terminal job must not gain an error: %q", errMsg)
		}
	}
}

// TestWatchdogFailsAStalledJobEndToEnd drives the actual watcher: this is the
// wiring proof that a parked deploy surfaces as a failure instead of an
// infinite spinner.
func TestWatchdogFailsAStalledJobEndToEnd(t *testing.T) {
	job := newJob("10.117.96.1")
	job.setStep(1, "running", "")

	startJobWatchdog(job, 60*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job.mu.Lock()
		done := job.Status != "running"
		job.mu.Unlock()
		if done {
			job.mu.Lock()
			defer job.mu.Unlock()
			if job.Status != "failed" {
				t.Fatalf("job.Status = %q, want failed", job.Status)
			}
			if job.Error == "" {
				t.Fatal("the watchdog must leave an operator-readable error")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the watchdog never fired: a stalled job stayed 'running' — that is the rc17 infinite spinner")
}
