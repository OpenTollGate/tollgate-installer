package app

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// RED suite for the self-inflicted-transport-loss defect.
//
// Observed on a GL-MT3000 (2026-10-05): step 10 restarts tollgate-wrt on the
// router, whose uci-defaults derive network.private from network.lan and reload
// networking. That tears down the very SSH session the deploy is driving over.
// sshRun is nil-safe, so the death was swallowed, step 10 was reported DONE, and
// the failure only surfaced at the health check two steps later as "lost the SSH
// transport" — pointing the operator at a router that was never at fault.
//
// The fix must (a) prove the session after the restart and (b) re-acquire it
// within a bounded window before any later step runs.

// --- the retry policy, tested without SSH -----------------------------------

func TestReacquireLoopGivesUpWithinTheDeadline(t *testing.T) {
	var probes int
	start := time.Now()
	attempts, ok := reacquireLoop(120*time.Millisecond, 20*time.Millisecond, func() bool {
		probes++
		return false
	})
	elapsed := time.Since(start)

	if ok {
		t.Fatal("reacquireLoop reported success although the probe never succeeded")
	}
	if attempts < 2 {
		t.Fatalf("expected a retry, got %d attempt(s)", attempts)
	}
	if attempts != probes {
		t.Fatalf("returned attempts %d != probes actually made %d", attempts, probes)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("loop overran its deadline: %s", elapsed)
	}
}

func TestReacquireLoopStopsAtTheFirstSuccess(t *testing.T) {
	var probes int
	attempts, ok := reacquireLoop(5*time.Second, time.Millisecond, func() bool {
		probes++
		return probes == 3
	})
	if !ok || attempts != 3 {
		t.Fatalf("expected success on attempt 3, got ok=%v attempts=%d", ok, attempts)
	}
	if probes != 3 {
		t.Fatalf("probe kept running after success: %d probes", probes)
	}
}

// --- seams ------------------------------------------------------------------

// withTransportSeams swaps the SSH seams for the duration of a test.
func withTransportSeams(t *testing.T,
	alive func(*ssh.Client) error,
	dial func(string, string) *ssh.Client) {
	t.Helper()
	prevAlive, prevDial, prevClose := transportAliveFn, transportDialFn, transportCloseFn
	prevTimeout, prevInterval := deployReacquireTimeout, deployReacquireInterval
	transportAliveFn = alive
	transportDialFn = dial
	transportCloseFn = func(*ssh.Client) {}
	deployReacquireTimeout, deployReacquireInterval = 60*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		transportAliveFn, transportDialFn, transportCloseFn = prevAlive, prevDial, prevClose
		deployReacquireTimeout, deployReacquireInterval = prevTimeout, prevInterval
	})
}

// --- reacquireTransport -----------------------------------------------------

func TestReacquireTransportRetriesUntilTheRouterIsBack(t *testing.T) {
	back := new(ssh.Client)
	var dials int
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(string, string) *ssh.Client {
			dials++
			if dials < 3 {
				return nil // still down
			}
			return back
		})

	got := reacquireTransport(&Job{}, "10.117.96.1", "pw", time.Second, time.Millisecond)
	if got != back {
		t.Fatal("reacquireTransport did not return the recovered client")
	}
	if dials != 3 {
		t.Fatalf("expected 3 dials, got %d", dials)
	}
}

func TestReacquireTransportGivesUpAndReturnsNil(t *testing.T) {
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(string, string) *ssh.Client { return nil })

	if got := reacquireTransport(&Job{}, "10.117.96.1", "pw", 60*time.Millisecond, 10*time.Millisecond); got != nil {
		t.Fatal("reacquireTransport must return nil when the router never comes back")
	}
}

// A client that connects but whose session is already dead must not count as
// recovered — otherwise the deploy resumes onto a corpse.
func TestReacquireTransportRejectsAConnectedButDeadSession(t *testing.T) {
	withTransportSeams(t,
		func(*ssh.Client) error { return errors.New("read: connection reset by peer") },
		func(string, string) *ssh.Client { return new(ssh.Client) })

	if got := reacquireTransport(&Job{}, "10.117.96.1", "pw", 60*time.Millisecond, 10*time.Millisecond); got != nil {
		t.Fatal("a connected-but-dead session must not be accepted")
	}
}

// --- ensureTransportAfterRestart -------------------------------------------

func TestEnsureTransportAfterRestartKeepsALiveClient(t *testing.T) {
	live := new(ssh.Client)
	var dials int
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(string, string) *ssh.Client { dials++; return nil })

	got, err := ensureTransportAfterRestart(&Job{}, live, "10.117.96.1", "pw")
	if err != nil {
		t.Fatalf("a live transport must not be an error, got %v", err)
	}
	if got != live {
		t.Fatal("a live transport must be returned unchanged")
	}
	if dials != 0 {
		t.Fatalf("must not dial while the transport is alive (dialed %d times)", dials)
	}
}

func TestEnsureTransportAfterRestartReacquiresADeadTransport(t *testing.T) {
	replacement := new(ssh.Client)
	var aliveCalls int
	withTransportSeams(t,
		func(*ssh.Client) error {
			aliveCalls++
			if aliveCalls == 1 {
				return errors.New("read: connection timed out") // the session that the restart killed
			}
			return nil // the re-acquired one is healthy
		},
		func(string, string) *ssh.Client { return replacement })

	got, err := ensureTransportAfterRestart(&Job{}, new(ssh.Client), "10.117.96.1", "pw")
	if err != nil {
		t.Fatalf("a successful re-acquire must not error, got %v", err)
	}
	if got != replacement {
		t.Fatal("expected the re-acquired client back")
	}
}

func TestEnsureTransportAfterRestartSurfacesFailureToReacquire(t *testing.T) {
	boom := errors.New("read: connection timed out")
	withTransportSeams(t,
		func(*ssh.Client) error { return boom },
		func(string, string) *ssh.Client { return nil })

	got, err := ensureTransportAfterRestart(&Job{}, new(ssh.Client), "10.117.96.1", "pw")
	if got != nil {
		t.Fatal("a failed re-acquire must not return a client")
	}
	if err == nil {
		t.Fatal("a failed re-acquire must return the original transport error")
	}
}

// --- the wiring -------------------------------------------------------------

// The restart step must re-establish the transport, so a restart that kills the
// session is recovered in step 10 instead of being reported as a failed install
// at the health gate.
func TestDeployReacquiresAfterTheServiceRestart(t *testing.T) {
	src, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("read deploy.go: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "ensureTransportAfterRestart(job, client, req.IP, req.Password)") {
		t.Fatal("deploy.go must re-establish the transport after the service restart")
	}
	// The restart's own output must not be read through the error-swallowing
	// sshRun: the deploy has to see that the command died.
	if !strings.Contains(body, `sshRunE(client, strings.Join([]string{`) {
		t.Fatal("the service restart must use the error-returning sshRunE, not nil-safe sshRun")
	}
}
