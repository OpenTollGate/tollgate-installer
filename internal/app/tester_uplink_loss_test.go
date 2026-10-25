package app

import (
	"errors"
	"strings"
	"testing"
)

// These tests pin card t_27e348b5's four requirements:
//
//	(i)   detect the tester's uplink kind (wifi vs ethernet) before/after,
//	(ii)  distinguish "router unreachable from the tester" from "router
//	      unhealthy", and never report the former as an install failure,
//	(iii) reconnect the tester when a saved profile allows, or say which SSID
//	      to join,
//	(iv)  never emit an install-failure verdict when the only evidence is the
//	      tester's own disconnection.
//
// Every case here is router-free: the uplink reads and the reachability probe
// are injected, so a laptop roaming between SSIDs can be replayed deterministically
// on any host (the live roam this card was filed from was wlp59s0 moving from
// 192.168.1.0/24 to 10.2.124.0/16).

// wifiOn is the machine the card was filed from: a laptop joined over wifi.
var wifiOn = testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "192.168.1.20/24"}

func TestClassifyTransportLossBlamesTheTesterWhenTheRadioRoams(t *testing.T) {
	// The live shape: same interface name, DIFFERENT subnet, router no longer
	// answering. The only thing that changed is this machine's own path.
	after := testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "10.2.124.120/16"}
	got := classifyTransportLoss(transportLossInput{
		Before: wifiOn, After: after, RouterIP: "192.168.1.1", RouterDialable: false,
	})
	if got != transportLossTesterSide {
		t.Fatalf("a roam to another subnet must be blamed on the tester, got %q", got)
	}
}

func TestClassifyTransportLossBlamesTheTesterWhenTheAssociationIsGone(t *testing.T) {
	cases := map[string]testerUplinkState{
		"no interface left at all":        {Kind: uplinkKindUnknown},
		"association dropped (addr gone)": {Kind: uplinkKindWifi, Iface: "wlp59s0"},
		"radio came up on another iface":  {Kind: uplinkKindWifi, Iface: "wlan0", CIDR: "192.168.1.20/24"},
	}
	for name, after := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyTransportLoss(transportLossInput{
				Before: wifiOn, After: after, RouterIP: "192.168.1.1", RouterDialable: false,
			})
			if got != transportLossTesterSide {
				t.Fatalf("got %q, want tester-side", got)
			}
		})
	}
}

// Requirement (ii): the same dead session with the tester's link intact is the
// ROUTER's problem, and must keep today's failure verdict. A test that only ever
// produced "tester-side" would be a rubber stamp.
func TestClassifyTransportLossBlamesTheRouterWhenTheTesterLinkIsIntact(t *testing.T) {
	t.Run("router still answers on :22", func(t *testing.T) {
		got := classifyTransportLoss(transportLossInput{
			Before: wifiOn, After: wifiOn, RouterIP: "192.168.1.1", RouterDialable: true,
		})
		if got != transportLossRouterSide {
			t.Fatalf("got %q, want router-side", got)
		}
	})
	t.Run("same subnet, router silent", func(t *testing.T) {
		got := classifyTransportLoss(transportLossInput{
			Before: wifiOn, After: wifiOn, RouterIP: "192.168.1.1", RouterDialable: false,
		})
		if got != transportLossRouterSide {
			t.Fatalf("got %q, want router-side", got)
		}
	})
	t.Run("ethernet tester is never blamed for its own link", func(t *testing.T) {
		wired := testerUplinkState{Kind: uplinkKindEthernet, Iface: "enx00e04c36043e", CIDR: "192.168.1.30/24"}
		got := classifyTransportLoss(transportLossInput{
			Before: wired, After: testerUplinkState{Kind: uplinkKindUnknown}, RouterIP: "192.168.1.1",
		})
		if got != transportLossRouterSide {
			t.Fatalf("a wired tester must keep the router-side verdict, got %q", got)
		}
	})
}

// The evidence may be genuinely inconclusive: the tester is on wifi, keeps its
// address, is on a subnet the router is NOT in, and the router does not answer.
// That is neither side proven, so the verdict must say so instead of converting
// a real failure into a reassuring one.
func TestClassifyTransportLossSaysUnknownRatherThanGuessing(t *testing.T) {
	after := testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "10.0.0.5/8"}
	got := classifyTransportLoss(transportLossInput{
		Before: after, After: after, RouterIP: "192.168.1.1", RouterDialable: false,
	})
	if got != transportLossUnknown {
		t.Fatalf("got %q, want unknown", got)
	}
}

// Requirement (iv), the card's headline: a wifi tester's own disconnection must
// leave the job NON-fatal, with an actionable note and no install-failure text.
func TestTesterSideLossEndsPendingNotFailed(t *testing.T) {
	stubTransportLoss(t,
		testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "10.2.124.120/16"},
		false,
		func(string) (bool, string) {
			return false, "no saved NetworkManager profile for !TollGate-AB12, so it was not rejoined automatically."
		},
	)

	job := &Job{Status: "running", Steps: deploySteps()}
	job.setStep(10, "running", "")
	handleTransportLoss(job, 10, "192.168.1.1", errors.New("connection reset by peer"),
		wifiOn, true, rejoinTargets{Private: "c08r4d0r-AB12", Captive: "!TollGate-AB12"})

	if job.Status != "pending" {
		t.Fatalf("Job.Status = %q, want pending — a lost tester link is not a failed install", job.Status)
	}
	if job.Note == "" {
		t.Fatal("a pending job must carry an operator note")
	}
	if job.Error != "" {
		t.Errorf("a tester-side loss must not set a failure error, got %q", job.Error)
	}
	low := strings.ToLower(job.Note)
	if strings.Contains(low, "installation failed") || strings.Contains(low, "setup failed") {
		t.Errorf("the note must not claim the install failed:\n%s", job.Note)
	}
	for _, want := range []string{
		"!TollGate-AB12",     // which SSID to join (requirement iii)
		"wlp59s0",            // which interface changed (requirement i)
		"192.168.1.20/24",    // before
		"10.2.124.120/16",    // after
		"Re-check",           // the action that does not re-deploy
		"not a router fault", // the step detail must not blame the router
	} {
		blob := job.Note + "\n" + job.Steps[10].Detail + "\n" + job.NoteTitle
		if !strings.Contains(blob, want) {
			t.Errorf("pending output missing %q:\n%s", want, blob)
		}
	}
	if !strings.Contains(job.NoteTitle, "not an install failure") {
		t.Errorf("the pending heading must say this is not an install failure, got %q", job.NoteTitle)
	}
	// The step list must be consistent with the terminal state (the same
	// contract failTransportLost keeps).
	for i, s := range job.Steps {
		if s.Status == "pending" || s.Status == "running" {
			t.Errorf("step %d (%s) still reads %q", i, s.Name, s.Status)
		}
	}
}

// A tester-side loss BEFORE anything was installed must not offer a Re-check for
// an install that never ran.
func TestTesterSideLossBeforeInstallSaysNothingWasInstalled(t *testing.T) {
	stubTransportLoss(t, testerUplinkState{Kind: uplinkKindUnknown}, false,
		func(string) (bool, string) { return false, "no tool" })

	job := &Job{Status: "running", Steps: deploySteps()}
	handleTransportLoss(job, 0, "192.168.1.1", errors.New("dial tcp: no route to host"),
		wifiOn, false, rejoinTargets{})

	if job.Status != "pending" {
		t.Fatalf("Job.Status = %q, want pending", job.Status)
	}
	if strings.Contains(job.Note, "press Re-check") {
		t.Errorf("a loss before the install must not offer a Re-check:\n%s", job.Note)
	}
	if !strings.Contains(job.Note, "nothing was installed") {
		t.Errorf("the note must say nothing was installed:\n%s", job.Note)
	}
}

// Requirement (iii): when a saved profile exists the wizard reconnects and says
// what it did. The attempt is injected — no real radio, no real passphrase.
func TestTesterSideLossAttemptsAReconnectWhenAProfileExists(t *testing.T) {
	var tried []string
	stubTransportLoss(t, testerUplinkState{Kind: uplinkKindWifi}, false,
		func(ssid string) (bool, string) {
			tried = append(tried, ssid)
			if ssid == "c08r4d0r-AB12" {
				return true, "rejoined the saved NetworkManager profile \"c08r4d0r-AB12\"."
			}
			return false, "no saved profile for " + ssid
		})

	job := &Job{Status: "running", Steps: deploySteps()}
	handleTransportLoss(job, 11, "192.168.1.1", errors.New("ssh: session run error"),
		wifiOn, true, rejoinTargets{Private: "c08r4d0r-AB12", Captive: "!TollGate-AB12"})

	if len(tried) == 0 || tried[0] != "c08r4d0r-AB12" {
		t.Fatalf("the private SSID must be tried first, tried=%v", tried)
	}
	if !strings.Contains(job.Note, "Reconnect attempt") || !strings.Contains(job.Note, "rejoined the saved NetworkManager profile") {
		t.Errorf("the note must report the reconnect truthfully:\n%s", job.Note)
	}
}

// The router-side verdict must still fail the deploy (the router really did stop
// answering) AND must carry the wifi notice, so a wifi operator is told about
// their own association even when the router is the likelier cause.
func TestRouterSideLossStillFailsAndKeepsTheRouterAdvice(t *testing.T) {
	stubTransportLoss(t, wifiOn, true, nil)

	job := &Job{Status: "running", Steps: deploySteps()}
	handleTransportLoss(job, 11, "192.168.1.1", errors.New("connection reset by peer"),
		wifiOn, true, rejoinTargets{})

	if job.Status != "failed" {
		t.Fatalf("Job.Status = %q, want failed — the router stopped answering", job.Status)
	}
	if !strings.Contains(job.Error, "OTHER ethernet port") {
		t.Errorf("the existing router-side advice must survive:\n%s", job.Error)
	}
	if !strings.Contains(job.Error, "YOUR association") {
		t.Errorf("a wifi tester must additionally be told about their own association:\n%s", job.Error)
	}
}

// A WIRED tester keeps today's failure text byte-for-byte: no wifi notice is
// appended, because there is no radio to blame.
func TestRouterSideLossOnEthernetAppendsNoWifiNotice(t *testing.T) {
	wired := testerUplinkState{Kind: uplinkKindEthernet, Iface: "enx00e04c36043e", CIDR: "192.168.1.30/24"}
	stubTransportLoss(t, wired, false, nil)

	job := &Job{Status: "running", Steps: deploySteps()}
	handleTransportLoss(job, 11, "192.168.1.1", errors.New("connection reset by peer"),
		wired, true, rejoinTargets{})

	if job.Status != "failed" {
		t.Fatalf("Job.Status = %q, want failed", job.Status)
	}
	if strings.Contains(job.Error, "YOUR association") {
		t.Errorf("no tester-side notice belongs on a wired tester's failure:\n%s", job.Error)
	}
}

// The note is a pure function of facts; pin its shape including the SSID-less
// case (before step 7 the deploy does not know the router's SSIDs yet).
func TestTesterSideRecoveryNoteWithoutSSIDsStillNamesTheAction(t *testing.T) {
	note := testerSideRecoveryNote(testerSideNoteInput{
		RouterIP: "192.168.1.1", Before: wifiOn,
		After:      testerUplinkState{Kind: uplinkKindWifi, Iface: "wlp59s0", CIDR: "10.2.124.120/16"},
		InstallRan: true,
	})
	if !strings.Contains(note, "!TollGate-") {
		t.Errorf("with no known SSID the note must describe the SSID shape:\n%s", note)
	}
	if strings.Contains(strings.ToLower(note), "installation failed") {
		t.Errorf("the note must never say installation failed:\n%s", note)
	}
}

// stubTransportLoss installs the two seams a verdict reads, and restores them.
func stubTransportLoss(t *testing.T, after testerUplinkState, dialable bool, rejoin func(string) (bool, string)) {
	t.Helper()
	prevNow, prevDial, prevRejoin := testerUplinkNow, routerDialableFn, testerWiFiRejoinFn
	testerUplinkNow = func() testerUplinkState { return after }
	routerDialableFn = func(string) bool { return dialable }
	if rejoin != nil {
		testerWiFiRejoinFn = rejoin
	} else {
		testerWiFiRejoinFn = func(string) (bool, string) { return false, "not attempted in this test" }
	}
	t.Cleanup(func() {
		testerUplinkNow, routerDialableFn, testerWiFiRejoinFn = prevNow, prevDial, prevRejoin
	})
}
