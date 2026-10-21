package app

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// The install-time twin of the step-10 restart recovery. The package postinst
// runs `/etc/init.d/network restart`, 99-tollgate-setup derives network.private
// (lan/24 ±1) and moves the wired port onto br-private, so the step-6 session
// dies. The deploy must find the router at its new address and re-acquire it,
// not stall at step 6.

func withRouterCandidates(t *testing.T, cands []string) {
	t.Helper()
	prev := routerCandidatesFn
	routerCandidatesFn = func() []string { return cands }
	t.Cleanup(func() { routerCandidatesFn = prev })
}

func TestReacquireAfterInstallRestartFindsTheRouterAtItsNewAddress(t *testing.T) {
	replacement := new(ssh.Client)
	var dialed []string
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(ip, _ string) *ssh.Client {
			dialed = append(dialed, ip)
			if ip == "192.168.2.1" {
				return replacement
			}
			return nil
		})
	withRouterCandidates(t, []string{"192.168.2.1"})

	got, ip := reacquireAfterInstallRestart(&Job{}, "192.168.1.1", "pw")
	if got != replacement {
		t.Fatal("expected the re-acquired client back")
	}
	if ip != "192.168.2.1" {
		t.Fatalf("expected the router's new address 192.168.2.1, got %q", ip)
	}
	if len(dialed) == 0 || dialed[0] != "192.168.2.1" {
		t.Fatalf("the discovered address must be tried first, dialed=%v", dialed)
	}
}

func TestReacquireAfterInstallRestartFallsBackToTheOldAddress(t *testing.T) {
	replacement := new(ssh.Client)
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(ip, _ string) *ssh.Client {
			if ip == "192.168.1.1" {
				return replacement
			}
			return nil
		})
	withRouterCandidates(t, nil) // the scan found nothing

	got, ip := reacquireAfterInstallRestart(&Job{}, "192.168.1.1", "pw")
	if got != replacement || ip != "192.168.1.1" {
		t.Fatalf("expected the old address as a last resort, got ip=%q", ip)
	}
}

func TestReacquireAfterInstallRestartReturnsNilWhenTheRouterNeverComesBack(t *testing.T) {
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(string, string) *ssh.Client { return nil })
	withRouterCandidates(t, []string{"192.168.2.1"})

	if got, ip := reacquireAfterInstallRestart(&Job{}, "192.168.1.1", "pw"); got != nil || ip != "" {
		t.Fatal("a router that never comes back must return (nil, \"\")")
	}
}

// Wiring: the install step must recover from the postinst's network restart,
// which is the step this defect fired at.
func TestDeployRecoversFromTheInstallNetworkRestart(t *testing.T) {
	src, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("read deploy.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "reacquireAfterInstallRestart(job, req.IP, req.Password") {
		t.Fatal("deploy.go must re-acquire the transport after the package install (its postinst restarts networking)")
	}
	if !strings.Contains(body, "transportAliveFn(client)") {
		t.Fatal("deploy.go must prove the session survived the install before the verification checks")
	}
}

// The captured br-private address is the exact destination; a fresh LAN scan
// cannot see it when the host has not re-leased.
func TestInstallRestartCandidatesPreferTheCapturedPrivateAddress(t *testing.T) {
	got := installRestartCandidates("10.187.186.1/24", "192.168.1.1/24", "192.168.1.1")
	want := "10.187.186.1,192.168.2.1,192.168.0.1,192.168.1.1"
	if strings.Join(got, ",") != want {
		t.Fatalf("installRestartCandidates = %v, want %s", got, want)
	}
}

func TestReacquireAfterInstallRestartTriesTheCapturedPrivateFirst(t *testing.T) {
	replacement := new(ssh.Client)
	var dialed []string
	withTransportSeams(t,
		func(*ssh.Client) error { return nil },
		func(ip, _ string) *ssh.Client {
			dialed = append(dialed, ip)
			if ip == "10.187.186.1" {
				return replacement
			}
			return nil
		})
	withRouterCandidates(t, []string{"192.168.2.1"})

	got, ip := reacquireAfterInstallRestart(&Job{}, "192.168.1.1", "pw", "10.187.186.1")
	if got != replacement || ip != "10.187.186.1" {
		t.Fatalf("reacquire = (%v, %q), want the captured private address", got, ip)
	}
	if len(dialed) == 0 || dialed[0] != "10.187.186.1" {
		t.Fatalf("captured address must be dialled first, dialed=%v", dialed)
	}
}

// Wiring: the install step must capture the pre-restart private address and
// must end pending, not failed, when only the transport moved.
func TestDeployTransportLossAfterInstallIsPending(t *testing.T) {
	src, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatalf("read deploy.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "installRestartCandidates(preInstallPrivate, preInstallLan, req.IP)") {
		t.Error("deploy.go must capture the pre-restart private address as a re-acquire candidate")
	}
	if !strings.Contains(body, "jobPending(job, 6, \"installed; router moved to its management network\"") {
		t.Error("deploy.go must end pending, not failed, when the install ran but the router moved")
	}
	if strings.Contains(body, "failTransportLost(job, 6, req.IP, aliveErr)") {
		t.Error("the post-install transport loss must not call failTransportLost")
	}
}

func TestJobPendingCarriesTheSkippedDetail(t *testing.T) {
	job := &Job{Status: "running", Steps: []Step{
		{Name: "install", Status: "running"},
		{Name: "brand", Status: "pending"},
	}}
	jobPending(job, 0, "installed; router moved", "note", "not reached — the router moved to its management network")
	if job.Status != "pending" {
		t.Fatalf("Job.Status = %q, want pending", job.Status)
	}
	if job.Steps[1].Detail != "not reached — the router moved to its management network" {
		t.Fatalf("skipped detail = %q", job.Steps[1].Detail)
	}
}
