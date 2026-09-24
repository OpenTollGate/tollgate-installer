package main

import (
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ─── the nil client must fail the job, never panic (BLOCK 2 of the #52 review) ─
//
// #52 made fixSubnetCollisions able to return nil (the move severed the only
// management path) and then handed that nil to code that dereferences it:
//
//   - configureSTA's call had NO nil check, so the nil flowed into
//     upstreamOnline -> repairLanDNS -> sshRun -> client.NewSession() -> panic;
//   - the two call sites that DID check returned while `client`/`*pclient` was
//     nil, so runDeployment's deferred client.Close() dereferenced it;
//   - those same two sites failed the step with setStep(n,"failed") + return,
//     which never sets job.Status, so the wizard spun on "running" forever;
//   - runDeployment runs in a goroutine (main.go) and the repo has no recover(),
//     so one panic took the whole installer process down mid-deploy.
//
// These tests drive the REAL functions (fixSubnetCollisions, moveLocalSubnet,
// adoptRelocatedClient, closeSSHClient, guardDeploymentPanic) through a nil
// client, and the relocation logic through an in-process SSH server that answers
// the deploy's router queries with fixed output — so the "the move silently did
// not happen" and "the move lost the router" paths are exercised end to end
// without a router.

// cannedExit is the exit-status payload an SSH server sends for a finished
// exec (RFC 4254 §6.10; the field is uint32).
type cannedExit struct{ Status uint32 }

// cannedRouter is a minimal in-process SSH server: it accepts sessions, answers
// each `exec` request with output chosen by a callback, logs every command it
// was sent, and reports success. It lets the tests above run the real
// relocation code against "a router" instead of a stub at the ssh.Client level.
type cannedRouter struct {
	ln        net.Listener
	hostKey   ssh.Signer
	respond   func(cmd string) string
	mu        sync.Mutex
	commands  []string
	accepting bool
}

func startCannedRouter(t *testing.T, respond func(cmd string) string) *cannedRouter {
	t.Helper()
	signer := hostKeySigner(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := &cannedRouter{ln: ln, hostKey: signer, respond: respond, accepting: true}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(signer)
	go r.serve(cfg)
	t.Cleanup(func() { ln.Close() })
	return r
}

func (r *cannedRouter) serve(cfg *ssh.ServerConfig) {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		go func() {
			sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
			if err != nil {
				conn.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				for ch := range chans {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "only session channels")
						continue
					}
					channel, chReqs, err := ch.Accept()
					if err != nil {
						continue
					}
					go r.serveSession(channel, chReqs)
				}
			}()
			sconn.Wait()
		}()
	}
}

func (r *cannedRouter) serveSession(channel ssh.Channel, reqs <-chan *ssh.Request) {
	defer channel.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		// exec payload: uint32 length + the command string.
		var cmd string
		if len(req.Payload) >= 4 {
			n := int(binary.BigEndian.Uint32(req.Payload[:4]))
			if 4+n <= len(req.Payload) {
				cmd = string(req.Payload[4 : 4+n])
			}
		}
		req.Reply(true, nil)
		r.mu.Lock()
		r.commands = append(r.commands, cmd)
		r.mu.Unlock()
		channel.Write([]byte(r.respond(cmd)))
		channel.SendRequest("exit-status", false, ssh.Marshal(cannedExit{}))
		// One exec per session, then CLOSE the channel: the client's
		// Session.Wait() blocks on the channel's request stream ending, not on
		// the exit-status request, so leaving it open would hang sshRun forever.
		return
	}
}

// sentCommands returns every command the canned router was asked to run.
func (r *cannedRouter) sentCommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.commands))
	copy(out, r.commands)
	return out
}

// dial connects to the canned router the way the installer would (a real
// *ssh.Client, so every sshRun/Close on it is real code).
func (r *cannedRouter) dial(t *testing.T) *ssh.Client {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.FixedHostKey(r.hostKey.PublicKey()),
		Timeout:         5 * time.Second,
	}
	c, err := ssh.Dial("tcp", r.ln.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("dialing the canned router: %v", err)
	}
	return c
}

// ─── canned router presets ────────────────────────────────────────────────────

// collisionRouter answers like an OpenWrt router whose br-lan sits inside the
// upstream subnet, with the deploy connection inside that subnet AND holding a
// DHCP lease — the case relocationIsSafe allows (#52's guard).
func collisionRouter(t *testing.T, brLan, brPrivate, src, lease string) *cannedRouter {
	t.Helper()
	return startCannedRouter(t, func(cmd string) string {
		switch {
		case strings.Contains(cmd, "ip route show default"):
			return "192.168.1.1\n"
		case strings.Contains(cmd, "ubus call network.interface.wwan"):
			return "192.168.1.0/24\n" // upstream collides with a 192.168.1.0/24 br-lan
		case strings.Contains(cmd, "dev br-lan"):
			return brLan
		case strings.Contains(cmd, "dev br-private"):
			return brPrivate
		case strings.Contains(cmd, "SSH_CONNECTION"):
			return src + " 192.168.1.1 40000 22\n"
		case strings.Contains(cmd, "dhcp.leases"):
			return lease
		default:
			return ""
		}
	})
}

// stubReconnect replaces the post-relocation dial for the duration of a test.
// dial receives the 1-based attempt number and the address being dialled, and
// returns the client that answer represents (nil = nothing answered). The
// dialled addresses are returned, in order.
func stubReconnect(t *testing.T, dial func(attempt int, addr string) *ssh.Client) *[]string {
	t.Helper()
	dials := &[]string{}
	old := moveReconnect
	moveReconnect = func(ip, password string, attempts int, delay time.Duration) *ssh.Client {
		*dials = append(*dials, ip)
		return dial(len(*dials), ip)
	}
	t.Cleanup(func() { moveReconnect = old })
	return dials
}

// jobLogMatching returns every log line containing substr.
func jobLogMatching(job *Job, substr string) []string {
	job.mu.Lock()
	defer job.mu.Unlock()
	var out []string
	for _, e := range job.Log {
		if strings.Contains(e.Msg, substr) {
			out = append(out, e.Msg)
		}
	}
	return out
}

// jobSnapshot reads the fields the wizard's status endpoint shows.
func jobSnapshot(job *Job) (status, errMsg, step5 string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.Status, job.Error, job.Steps[5].Status
}

// panicked reports whether fn panicked — the positive control for the nil-client
// tests: the nil really is fatal to a bare dereference, so the guards below are
// what stands between the installer and the process-killing panic of BLOCK 2.
func panicked(fn func()) (yes bool) {
	defer func() {
		if recover() != nil {
			yes = true
		}
	}()
	fn()
	return false
}

// TestNilClientPanicsABareDereference is the positive control for every test
// below: the nil client #52 can produce really does panic sshRun (and therefore
// repairLanDNS/upstreamOnline, which is the reviewer's verified cascade), so a
// green result below cannot be an artifact of nil being harmless here.
func TestNilClientPanicsABareDereference(t *testing.T) {
	if !panicked(func() { sshRun(nil, "echo hi") }) {
		t.Fatal("sshRun(nil, …) did not panic — the nil-client hazard of BLOCK 2 is not being reproduced, so the guards below prove nothing")
	}
}

// TestRelocationFailureFailsTheJobAndNeverDereferencesTheNilClient drives the
// step-5/10.5 call-site sequence through a REAL relocation that loses the router:
// fixSubnetCollisions returns nil, adoptRelocatedClient must fail the job (Status
// "failed", not "running") and hand back a nil to be closed safely — where the
// old code wrote nils into client/*pclient and the deferred Close() panicked.
func TestRelocationFailureFailsTheJobAndNeverDereferencesTheNilClient(t *testing.T) {
	router := collisionRouter(t,
		"192.168.1.1/24\n", // br-lan: collides with the upstream
		"",                 // br-private: no address yet
		"192.168.1.50",     // the deploy connection sits inside the moving subnet…
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.1.50 laptop *\n") // …and holds a lease
	// The move takes, but nothing answers afterwards: the router is gone from
	// this machine (the reviewer's "connect a client to the router's LAN" case).
	stubReconnect(t, func(int, string) *ssh.Client { return nil })

	job := newJob("192.168.1.1")
	client := router.dial(t)
	defer closeSSHClient(client)

	var nc *ssh.Client
	if panicked(func() { nc = fixSubnetCollisions(job, client, "192.168.1.1", "pw") }) {
		t.Fatal("fixSubnetCollisions panicked — the deploy would have died with the whole installer process (BLOCK 2)")
	}
	if nc != nil {
		t.Fatalf("fixSubnetCollisions returned a client %v even though nothing answered after the move — the deploy would grind on a dead connection", nc)
	}

	// The relocation really did run, and the chain it sent carries BLOCK 1's fix.
	moved := false
	for _, c := range router.sentCommands() {
		if strings.Contains(c, "uci set network.lan.ipaddr=") {
			moved = true
			if !strings.Contains(c, "uci -q delete network.lan.gateway || true") {
				t.Errorf("the relocation chain sent to the router does not guard the gateway delete:\n%s", c)
			}
		}
	}
	if !moved {
		t.Fatal("no relocation chain reached the router — the test did not exercise the move path")
	}

	// The call sites consume that nil through adoptRelocatedClient.
	if panicked(func() { nc, _ = adoptRelocatedClient(job, nc, 5) }) {
		t.Fatal("adoptRelocatedClient panicked on the nil client")
	}
	if nc != nil {
		t.Error("adoptRelocatedClient returned a non-nil client for a nil relocation result")
	}
	status, errMsg, step5 := jobSnapshot(job)
	if status != "failed" {
		t.Errorf("job.Status = %q, want \"failed\" — setStep+return (which #52 used) left it \"running\" and the wizard spun forever", status)
	}
	if errMsg == "" {
		t.Error("job.Error is empty — the wizard UI would show no reason for the stopped deploy")
	}
	if step5 != "failed" {
		t.Errorf("step 5 status = %q, want \"failed\"", step5)
	}
	// runDeployment's deferred cleanup, on the nil the call site is left holding.
	if panicked(func() { closeSSHClient(nc) }) {
		t.Fatal("closeSSHClient panicked on nil — that is exactly the deferred client.Close() dereference that killed the installer (BLOCK 2)")
	}
	if panicked(func() { closeSSHClient(nil) }) {
		t.Fatal("closeSSHClient(nil) panicked")
	}
}

// TestSemanticallyNilClientEntryFailsInsteadOfPanicking pins the other half of
// the nil contract: even a nil handed straight INTO the call sites (re-run of a
// step whose session is gone) must produce a failed job, not a panic. It drives
// fixSubnetCollisions + adoptRelocatedClient with nil and no router at all.
func TestSemanticallyNilClientEntryFailsInsteadOfPanicking(t *testing.T) {
	job := newJob("192.168.1.1")
	job.setStep(10, "running", "")

	var nc *ssh.Client
	if panicked(func() { nc = fixSubnetCollisions(job, nil, "192.168.1.1", "pw") }) {
		t.Fatal("fixSubnetCollisions panicked on a nil client instead of reporting no session")
	}
	if nc != nil {
		t.Error("fixSubnetCollisions(nil client) returned a non-nil client")
	}
	if len(jobLogMatching(job, "no live SSH session")) == 0 {
		t.Error("fixSubnetCollisions(nil client) logged no reason — a silent nil return is exactly the failure the reviewer objected to")
	}
	nc, ok := adoptRelocatedClient(job, nc, 10)
	if ok || nc != nil {
		t.Fatalf("adoptRelocatedClient(nil, step 10) = (%v, %v), want (nil, false)", nc, ok)
	}
	job.mu.Lock()
	status, step10 := job.Status, job.Steps[10].Status
	job.mu.Unlock()
	if status != "failed" || step10 != "failed" {
		t.Errorf("after the nil-client relocation: job.Status=%q (want failed), step 10 status=%q (want failed)", status, step10)
	}
}

// TestSecondRelocationFallbackDialsTheAddressTheRouterActuallyAnsweredOn pins
// BLOCK 2's third item: the reviewer's concrete cascade is TWO moves in one
// deploy — br-lan moves to 10.x.y.1, then br-private's reconnect fails and its
// fallback dials the pre-move address (dead) instead of 10.x.y.1. The
// fallback must dial the address the router is on NOW.
func TestSecondRelocationFallbackDialsTheAddressTheRouterActuallyAnsweredOn(t *testing.T) {
	router := collisionRouter(t,
		"192.168.1.1/24\n", // br-lan collides
		"192.168.1.2/24\n", // br-private collides too (no guard: it is not the management path)
		"192.168.1.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.1.50 laptop *\n")
	// First relocation answers on its new address; every later dial fails, so the
	// second move's FALLBACK is observable.
	dials := stubReconnect(t, func(attempt int, addr string) *ssh.Client {
		if attempt == 1 {
			return router.dial(t) // br-lan's move took: the router answers on its new address
		}
		return nil
	})

	job := newJob("192.168.1.1")
	var nc *ssh.Client
	if panicked(func() { nc = fixSubnetCollisions(job, router.dial(t), "192.168.1.1", "pw") }) {
		t.Fatal("fixSubnetCollisions panicked during a two-move relocation")
	}
	if nc != nil {
		t.Fatalf("expected the second move to lose the router (nil client), got %v", nc)
	}
	if len(*dials) != 3 {
		t.Fatalf("expected 3 dials (br-lan new address, br-private new address, br-private fallback), got %v", *dials)
	}
	answeredOn := (*dials)[0] // br-lan's new address answered
	if answeredOn == "192.168.1.1" {
		t.Fatalf("the first move dialled the original address %q — the test cannot distinguish the fix", answeredOn)
	}
	if (*dials)[2] != answeredOn {
		t.Errorf("br-private's fallback dialled %q, want the address the router actually answered on (%q). "+
			"Dialling the pre-deploy address %q is dead: the first move killed it (BLOCK 2, item 3)",
			(*dials)[2], answeredOn, "192.168.1.1")
	}
	if logs := jobLogMatching(job, "Reconnected to router on "); len(logs) != 1 || !strings.HasSuffix(logs[0], answeredOn) {
		t.Errorf("log lines reporting the reconnect = %q, want exactly one naming the address that answered (%q)", logs, answeredOn)
	}

	// And the deploy still fails cleanly at the call site: nil in, failed job out.
	nc, ok := adoptRelocatedClient(job, nc, 5)
	if ok || nc != nil {
		t.Fatalf("adoptRelocatedClient after the failed second move = (%v, %v), want (nil, false)", nc, ok)
	}
	job.mu.Lock()
	status := job.Status
	job.mu.Unlock()
	if status != "failed" {
		t.Errorf("job.Status = %q after the relocation lost the router, want \"failed\"", status)
	}
}

// TestReconnectFallbackReportsTheAddressThatAnswered pins the review's INFO item
// at deploy.go:1664: when the fallback reconnected on the ORIGINAL address (the
// move silently did not happen), the log said "Reconnected to router on <newIP>"
// — a false claim, and after this PR the fallback-success case is precisely the
// "move did not take" signal.
func TestReconnectFallbackReportsTheAddressThatAnswered(t *testing.T) {
	router := collisionRouter(t, "192.168.1.1/24\n", "", "192.168.1.50",
		"1700000000 aa:bb:cc:dd:ee:ff 192.168.1.50 laptop *\n")
	dials := stubReconnect(t, func(attempt int, addr string) *ssh.Client {
		if attempt == 1 { // the new address never answers…
			return nil
		}
		return router.dial(t) // …the pre-move address does
	})
	client := router.dial(t)
	defer closeSSHClient(client)

	job := newJob("192.168.1.1")
	nc, answered := moveLocalSubnet(job, client, "192.168.1.1", "pw", "br-lan", "lan", "lan", "test move")
	if nc == nil {
		t.Fatal("moveLocalSubnet returned no client although the fallback address answered")
	}
	defer closeSSHClient(nc)
	if len(*dials) != 2 {
		t.Fatalf("expected a dial on the new address then a fallback dial, got %v", *dials)
	}
	newIP := (*dials)[0]
	if answered != "192.168.1.1" {
		t.Errorf("moveLocalSubnet reported %q as the address that answered, want the fallback address 192.168.1.1", answered)
	}
	logs := jobLogMatching(job, "Reconnected to router on ")
	if len(logs) != 1 {
		t.Fatalf("expected exactly one \"Reconnected to router on …\" line, got %q", logs)
	}
	if strings.HasSuffix(logs[0], newIP) {
		t.Errorf("the log claimed %q, but the router never moved (the move silently no-opped and the fallback reconnected on the ORIGINAL address) — the message must name the address that actually answered", logs[0])
	}
	if !strings.HasSuffix(logs[0], "192.168.1.1") {
		t.Errorf("the log does not name the address that answered: %q", logs[0])
	}
}

// TestGuardDeploymentPanicFailsTheJobInsteadOfKillingTheProcess pins the
// top-level recover (BLOCK 2's last item): a panic in the deploy goroutine must
// NOT escape and kill the wizard process, and must NOT be swallowed either — the
// job ends up failed, with the panic in its log.
func TestGuardDeploymentPanicFailsTheJobInsteadOfKillingTheProcess(t *testing.T) {
	job := newJob("192.168.1.1")
	job.setStep(7, "running", "")

	escaped := panicked(func() {
		guardDeploymentPanic(job, func() { panic("nil pointer dereference (simulated)") })
	})
	if escaped {
		t.Fatal("a panic escaped guardDeploymentPanic — in the deploy goroutine that kills the whole installer process")
	}
	job.mu.Lock()
	status, errMsg, step7 := job.Status, job.Error, job.Steps[7].Status
	job.mu.Unlock()
	if status != "failed" {
		t.Errorf("job.Status = %q after a contained panic, want \"failed\"", status)
	}
	if !strings.Contains(errMsg, "unexpected internal error") || !strings.Contains(errMsg, "nil pointer dereference") {
		t.Errorf("job.Error = %q, want it to name the contained error (never a silent swallow)", errMsg)
	}
	if step7 != "failed" {
		t.Errorf("the step the deploy died on (7) is %q, want \"failed\"", step7)
	}
	if logs := jobLogMatching(job, "PANIC"); len(logs) == 0 {
		t.Error("the panic was not logged — the operator has nothing to report")
	}
	// A deploy that does NOT panic must be left completely alone.
	ok := newJob("192.168.1.1")
	guardDeploymentPanic(ok, func() { ok.setStep(3, "running", "") })
	ok.mu.Lock()
	defer ok.mu.Unlock()
	if ok.Status != "running" || ok.Error != "" {
		t.Errorf("guardDeploymentPanic changed a healthy run: Status=%q Error=%q", ok.Status, ok.Error)
	}
}

// TestJobFailSetsFailedStatus pins the RISK item: the new failure paths must use
// jobFail, whose whole point is setting job.Status — the setStep+return shape
// #52 shipped leaves the wizard spinning on "running" with no error shown.
func TestJobFailSetsFailedStatus(t *testing.T) {
	job := newJob("192.168.1.1")
	job.setStep(5, "running", "")
	jobFail(job, 5, "subnet relocation severed the connection", "actionable detail")
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != "failed" {
		t.Errorf("jobFail left job.Status = %q, want \"failed\"", job.Status)
	}
	if job.Error != "actionable detail" {
		t.Errorf("jobFail set job.Error = %q, want the actionable detail", job.Error)
	}
	if job.Steps[5].Status != "failed" {
		t.Errorf("jobFail left step 5 = %q, want \"failed\"", job.Steps[5].Status)
	}
}

// TestDeferredDeployCleanupIsNilSafe is the second static half of BLOCK 2.
// runDeployment cannot be driven without a router (it dials before anything
// else), so its deferred cleanup is pinned at the source: closing a bare `client`
// there dereferences the nil a lost relocation hands back — the reviewer's
// verified "defer + early return after a nil check" panic.
func TestDeferredDeployCleanupIsNilSafe(t *testing.T) {
	if !strings.Contains(deployGoSrc, "defer func() { closeSSHClient(client) }()") {
		t.Error("runDeployment's deferred cleanup no longer goes through closeSSHClient — a nil client (the relocation lost the router) would panic the whole installer")
	}
	if strings.Contains(deployGoSrc, "defer func() { client.Close() }()") {
		t.Error("runDeployment's deferred cleanup dereferences client directly instead of through closeSSHClient")
	}
}

// TestEveryFixSubnetCollisionsCallSiteAdoptsTheResult is the static half of the
// BLOCK 2 guard: it is not enough for the nil handling to exist — every call site
// must go through it. Parses the real embedded deploy.go source.
func TestEveryFixSubnetCollisionsCallSiteAdoptsTheResult(t *testing.T) {
	sites := 0
	for _, raw := range strings.Split(deployGoSrc, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.Contains(line, "fixSubnetCollisions(") || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "func fixSubnetCollisions(") {
			continue
		}
		sites++
		if !strings.Contains(line, "adoptRelocatedClient(job, fixSubnetCollisions(") {
			t.Errorf("fixSubnetCollisions call site does not adopt the result through adoptRelocatedClient (nil client => panic / Status stuck on \"running\"):\n%s", line)
		}
	}
	if sites != 3 {
		t.Errorf("found %d fixSubnetCollisions call sites in deploy.go, want 3 (step 5, step 10.5, configureSTA) — a new one must use adoptRelocatedClient too", sites)
	}
}
