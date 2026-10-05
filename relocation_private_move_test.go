package main

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// ─── a move of a NON-management bridge must not move the deploy session ───────
//
// Operator report 2026-10-05 (live deploy, GL-MiFi/MT3000-class, OpenWrt 25.12.5):
//
//	Subnet collision: br-private=192.168.2.1/24 overlaps upstream 192.168.2.35/24
//	  (gw 192.168.2.1) — moving br-private to 10.117.96.1/24
//	<nothing for a long time>
//	Setup failed: A colliding local subnet was moved, but the router could not be
//	  reached from this machine afterwards...
//
// moveLocalSubnet dialled the address it had just given the RELOCATED bridge.
// For br-private that address is unroutable from the operator's machine BY
// CONSTRUCTION: the laptop holds 192.168.2.x (WiFi uplink) and 192.168.1.x
// (Ethernet to br-lan), while 10.117.96.1/24 only exists inside the router, so
// the dial left via the default gateway and blackholed — up to ~65s of silent
// retry, and after the (documented-harmful) `/etc/init.d/network restart`
// fallback in the move chain had already taken br-lan's IPv4 down, the fallback
// dial to the management address could not answer either.
//
// The invariant these tests pin: the deploy session lives on the MANAGEMENT
// path (br-lan). Relocating br-private must leave that session exactly where it
// is, and must never be reported as if the session had moved.
//
// The br-private move test is written against the existing fakes (cannedRouter
// + stubReconnect); the moveLocalSubnetCommands chain it sends is asserted at
// the SSH boundary, because the harmful fallback is an ABSOLUTE path
// (/etc/init.d/network restart) that a PATH stub cannot intercept.

// jobLogAll returns every log message the job recorded.
func jobLogAll(job *Job) []string {
	job.mu.Lock()
	defer job.mu.Unlock()
	out := make([]string, 0, len(job.Log))
	for _, e := range job.Log {
		out = append(out, e.Msg)
	}
	return out
}

// relocationMovedAddress returns the address moveLocalSubnet gave ifname, read
// from the job log's "moving <ifname> to <addr>/24" line.
func relocationMovedAddress(t *testing.T, job *Job, ifname string) string {
	t.Helper()
	re := regexp.MustCompile(`moving ` + regexp.QuoteMeta(ifname) + ` to (\d+\.\d+\.\d+\.\d+)/24`)
	for _, msg := range jobLogAll(job) {
		if m := re.FindStringSubmatch(msg); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no \"moving %s to <addr>/24\" line in the job log — the move never ran", ifname)
	return ""
}

// requireLogLineContaining fails unless some log line contains every substr.
func requireLogLineContaining(t *testing.T, job *Job, why string, substrs ...string) {
	t.Helper()
	for _, msg := range jobLogAll(job) {
		ok := true
		for _, s := range substrs {
			if !strings.Contains(msg, s) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("%s: no log line contains all of %q. Log:\n%s", why, substrs, strings.Join(jobLogAll(job), "\n"))
}

// TestPrivateBridgeMoveNeverDialsTheRelocatedBridgeAddress is the operator's
// case: br-lan is fine, br-private collides with the upstream, and the address
// br-private is moved to is NOT reachable from the client. The deploy session
// must stay on the management address — which means no reconnect at all — and
// the private move must be reported as its own result.
func TestPrivateBridgeMoveNeverDialsTheRelocatedBridgeAddress(t *testing.T) {
	router := collisionRouter(t,
		"192.168.7.1/24\n", // br-lan: no collision with the upstream
		"192.168.1.2/24\n", // br-private: collides with the 192.168.1.0/24 upstream
		"192.168.7.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.7.50 laptop *\n")
	dials := stubReconnect(t, func(int, string) *ssh.Client { return nil })

	job := newJob("192.168.7.1")
	client := router.dial(t)
	nc := fixSubnetCollisions(job, client, "192.168.7.1", "pw")
	defer closeSSHClient(nc)

	newPrivateIP := relocationMovedAddress(t, job, "br-private")

	// (i) The relocated address is never dialled — it cannot be routed to from a
	// client sitting on br-lan, and dialling it is what produced the 65s hang.
	// Only the management address may ever be dialled.
	for _, d := range *dials {
		if d == newPrivateIP {
			t.Errorf("the deploy dialled %q — the address it just gave the relocated br-private bridge. That subnet exists only inside the router: from this machine the dial leaves via the default gateway and blackholes (the operator's reported hang)", d)
		}
		if d != "192.168.7.1" {
			t.Errorf("the deploy dialled %q; the session lives on the management path 192.168.7.1, and a non-management move must not dial anything else", d)
		}
	}
	if len(*dials) != 0 {
		t.Errorf("expected no reconnect attempt after moving a non-management bridge (the SSH session is on br-lan and was not touched), got dials %v", *dials)
	}

	// The move must not have cost the session.
	if nc == nil {
		t.Fatal("fixSubnetCollisions returned no client after a br-private move — moving a bridge that does not carry the deploy session must not sever it")
	}
	job.mu.Lock()
	status := job.Status
	job.mu.Unlock()
	if status == "failed" {
		t.Errorf("the job was failed by a br-private-only relocation (log: %s)", strings.Join(jobLogAll(job), " | "))
	}

	// (ii) Liveness is asserted on the UNCHANGED management address.
	requireLogLineContaining(t, job, "the session was not reported as still live on the management address",
		"br-private", newPrivateIP, "192.168.7.1")

	// (iii) The private move is reported as its own result — never as a session
	// move, and never naming the new private subnet as something to reconnect to.
	for _, msg := range jobLogAll(job) {
		if strings.Contains(msg, "Reconnected to router on "+newPrivateIP) {
			t.Errorf("the deploy claims it reconnected on the relocated private address %s — the session never moved there: %q", newPrivateIP, msg)
		}
		if strings.Contains(msg, "Reconnected to router on") && strings.Contains(msg, "br-private") {
			t.Errorf("a br-private move must not report a reconnect of the deploy session: %q", msg)
		}
	}

	// The chain that reached the router must not contain the documented-harmful
	// full network restart, and must apply the move non-disruptively.
	var movedChain string
	for _, c := range router.sentCommands() {
		if strings.Contains(c, "uci set network.private.ipaddr=") {
			movedChain = c
		}
	}
	if movedChain == "" {
		t.Fatal("no br-private relocation chain reached the router — the test did not exercise the move path")
	}
	if strings.Contains(movedChain, "network restart") {
		t.Errorf("the relocation chain for a NON-management bridge still contains a full network restart — the repo's own comment records that it leaves br-lan without an IPv4 address, which is how this deploy lost the router on both addresses:\n%s", movedChain)
	}
}

// TestPrivateBridgeMoveReconnectsOnlyOnTheManagementAddressWhenItDies pins the
// other half: IF the private move did take the management path down anyway, the
// only address worth dialling is the management address — never the relocated
// private one.
func TestPrivateBridgeMoveReconnectsOnlyOnTheManagementAddressWhenItDies(t *testing.T) {
	router := collisionRouter(t,
		"192.168.7.1/24\n",
		"192.168.1.2/24\n",
		"192.168.7.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.7.50 laptop *\n")

	// The management session stops answering after the private move…
	old := relocationManagementAlive
	relocationManagementAlive = func(*ssh.Client) error {
		return errors.New("ssh exec did not answer within 8s — the transport is dead (simulated)")
	}
	t.Cleanup(func() { relocationManagementAlive = old })

	// …and the router answers again on the management address only.
	dials := stubReconnect(t, func(int, string) *ssh.Client { return router.dial(t) })

	job := newJob("192.168.7.1")
	client := router.dial(t)
	nc := fixSubnetCollisions(job, client, "192.168.7.1", "pw")
	defer closeSSHClient(nc)

	newPrivateIP := relocationMovedAddress(t, job, "br-private")
	if nc == nil {
		t.Fatal("fixSubnetCollisions returned no client although the management address answered again")
	}
	for _, d := range *dials {
		if d != "192.168.7.1" {
			t.Errorf("dialled %q after a non-management move; only the management address 192.168.7.1 can answer from this machine", d)
		}
	}
	if len(*dials) == 0 {
		t.Error("the management session was reported dead but nothing was dialled — the deploy would continue against a dead connection")
	}
	// The recovery must NOT name the new private subnet as the place to connect.
	for _, msg := range jobLogAll(job) {
		if strings.Contains(msg, newPrivateIP) && strings.Contains(msg, "connect a client") {
			t.Errorf("the failure guidance points the operator at the relocated private subnet %s, which no client on br-lan can reach: %q", newPrivateIP, msg)
		}
	}
	requireLogLineContaining(t, job, "the reconnect was not reported on the management address", "Reconnected to router on 192.168.7.1")
}

// TestPrivateMoveFailureNamesTheBridgeThatMoved pins the operator-facing text:
// when a br-private move really does lose the router, the job error must name
// br-private (the bridge that moved) and the recovery that applies, and must NOT
// repeat the old advice to connect to "the new subnet" — which, for a private
// bridge, is an address the operator's machine can never reach.
func TestPrivateMoveFailureNamesTheBridgeThatMoved(t *testing.T) {
	router := collisionRouter(t,
		"192.168.7.1/24\n",
		"192.168.1.2/24\n",
		"192.168.7.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.7.50 laptop *\n")

	oldAlive := relocationManagementAlive
	relocationManagementAlive = func(*ssh.Client) error { return errors.New("transport dead (simulated)") }
	t.Cleanup(func() { relocationManagementAlive = oldAlive })
	stubReconnect(t, func(int, string) *ssh.Client { return nil })

	job := newJob("192.168.7.1")
	job.setStep(10, "running", "")
	nc, ok := adoptRelocatedClient(job, fixSubnetCollisions(job, router.dial(t), "192.168.7.1", "pw"), 10)
	if ok || nc != nil {
		t.Fatalf("adoptRelocatedClient = (%v, %v), want (nil, false) after the private move lost the router", nc, ok)
	}

	job.mu.Lock()
	errMsg := job.Error
	job.mu.Unlock()
	if errMsg == "" {
		t.Fatal("the job carries no error message — the operator has nothing to act on")
	}
	if !strings.Contains(errMsg, "br-private") {
		t.Errorf("the failure text does not name the bridge that actually moved (br-private): %q", errMsg)
	}
	if strings.Contains(errMsg, "it now hands out addresses on the new subnet") {
		t.Errorf("the failure text still tells the operator to connect to the new subnet — that is br-lan's text, and br-lan did not move: %q", errMsg)
	}
	if !strings.Contains(errMsg, "power-cycle") {
		t.Errorf("the failure text does not name the recovery for a management bridge that lost its address (power-cycle the router): %q", errMsg)
	}
}

// TestRelocationReconnectIsBoundedAndLoggedProgressPerAttempt pins the second
// half of the operator-visible defect: the retry window was up to ~65s with ZERO
// log output, so a retry was indistinguishable from a hang. Every attempt must
// announce itself and be counted, and the window must stay bounded.
func TestRelocationReconnectIsBoundedAndLoggedProgressPerAttempt(t *testing.T) {
	router := collisionRouter(t,
		"192.168.1.1/24\n", // br-lan collides, and the deploy connection follows it
		"",
		"192.168.1.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.1.50 laptop *\n")
	dials := stubReconnect(t, func(int, string) *ssh.Client { return nil })

	job := newJob("192.168.1.1")
	client := router.dial(t)
	nc := fixSubnetCollisions(job, client, "192.168.1.1", "pw")
	defer closeSSHClient(nc)

	if nc != nil {
		t.Fatalf("expected no client (nothing answered), got %v", nc)
	}
	// Bounded: the two retry windows of a management move (new address, then the
	// management address) are 5 + 3 attempts. Never an unbounded loop.
	if len(*dials) == 0 || len(*dials) > 8 {
		t.Errorf("the reconnect window made %d attempts (%v) — want a bounded 1..8", len(*dials), *dials)
	}
	progress := 0
	for _, msg := range jobLogAll(job) {
		if strings.Contains(msg, "attempt ") {
			progress++
		}
	}
	if progress != len(*dials) {
		t.Errorf("logged %d progress lines for %d connection attempts — a retry must never be indistinguishable from a hang. Log:\n%s",
			progress, len(*dials), strings.Join(jobLogAll(job), "\n"))
	}
	// Each dialled address must be named by a progress line, so the operator can
	// see WHICH address is being retried.
	for _, d := range *dials {
		requireLogLineContaining(t, job, "no progress line names the address being dialled", "attempt ", d)
	}
	// And giving up is logged too, never silent.
	requireLogLineContaining(t, job, "the deploy gave up on reconnect without saying so", "No answer from 192.168.1.1")
}
