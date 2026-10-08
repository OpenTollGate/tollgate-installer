package app

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// The "first install fails, second succeeds" report: on a COLD run the uplink
// gate probed DNS before dnsmasq had learned the DHCP lease's resolvers (it
// starts at boot with no upstream and only follows one when netifd's hotplug
// fires), failed an upstream that was healthy, and rolled the STA config back.
// The second run — AP now warm, association fast, resolv.conf.auto populated
// sooner — passed the identical probe with no configuration change.
//
// The gate now (a) makes dnsmasq follow the current uplink once, (b) waits,
// bounded, for the precondition (a non-loopback nameserver AND dnsmasq
// resolving) before it believes a DNS verdict, and (c) if routing is still OK
// while only a public resolver answers, applies the repair the project's own
// diagnostic names by hand — LOGGED — and retries once.
//
// These tests drive the real decision functions and the real upstreamOnlineWith
// through an injected SSH runner, so the ordering is pinned without a router.

// ─── resolv.conf parsing ───────────────────────────────────────────

func TestResolvNameserverPresent(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"upstream resolver", "nameserver 10.10.24.1\n", true},
		{"public resolver", "# generated\nnameserver 1.1.1.1\nnameserver 9.9.9.9\n", true},
		{"loopback only is not a resolver", "nameserver 127.0.0.1\n", false},
		{"dnsmasq's own 127.0.0.1 with search line", "search lan\nnameserver 127.0.0.1\n", false},
		{"no-resolver placeholder", "nameserver 0.0.0.0\n", false},
		{"ipv6 loopback", "nameserver ::1\n", false},
		{"empty config", "", false},
		{"comments only", "# nothing yet\n", false},
		{"real resolver after a loopback line", "nameserver 127.0.0.1\nnameserver 10.0.0.1\n", true},
		{"bare nameserver with no address", "nameserver\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolvNameserverPresent(c.out); got != c.want {
				t.Errorf("resolvNameserverPresent(%q) = %v, want %v", c.out, got, c.want)
			}
		})
	}
}

// The precondition is conjunctive: a nameserver must have been handed out AND
// dnsmasq must actually answer. Either alone is not enough to believe a
// failure — that is precisely the cold-start false negative.
func TestUplinkPreconditionMet(t *testing.T) {
	okAnswer := "Name:    github.com\nAddress: 140.82.121.4"
	refused := "** server can't find github.com: REFUSED"
	cases := []struct {
		name                         string
		resolvAuto, resolv, nslookup string
		want                         bool
	}{
		{"resolv.auto populated and dnsmasq answers", "nameserver 10.10.24.1", "", okAnswer, true},
		{"resolv.conf fallback populated and dnsmasq answers", "", "nameserver 192.168.8.1", okAnswer, true},
		{"loopback nameserver does not count", "nameserver 127.0.0.1", "nameserver 0.0.0.0", okAnswer, false},
		{"nameserver present but dnsmasq refused", "nameserver 10.10.24.1", "", refused, false},
		{"nameserver present but no answer at all", "nameserver 10.10.24.1", "", "", false},
		{"dnsmasq answers but no nameserver yet", "", "", okAnswer, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := uplinkPreconditionMet(c.resolvAuto, c.resolv, c.nslookup); got != c.want {
				t.Errorf("uplinkPreconditionMet(auto=%q, resolv=%q, dns=%q) = %v, want %v",
					c.resolvAuto, c.resolv, c.nslookup, got, c.want)
			}
		})
	}
}

// ─── the injected-runner double ────────────────────────────────────

// scriptedRunner answers router commands from a map of command prefixes and
// records every command issued. The cold-start tests need the resolv.conf/dns
// answers to CHANGE between polls, so a per-command callback can be supplied
// via dynamic.
type scriptedRunner struct {
	static  map[string]string
	dynamic func(cmd string) (string, bool)
	seen    []string
}

func (s *scriptedRunner) run(cmd string) string {
	s.seen = append(s.seen, cmd)
	if s.dynamic != nil {
		if out, ok := s.dynamic(cmd); ok {
			return out
		}
	}
	for prefix, out := range s.static {
		if strings.HasPrefix(cmd, prefix) {
			return out
		}
	}
	return ""
}

// count returns how many issued commands had the given prefix.
func (s *scriptedRunner) count(prefix string) int {
	n := 0
	for _, c := range s.seen {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// baseUplinkOutputs is the healthy-but-cold baseline: repairLanDNS finds no LAN
// IP (so it is skipped), and the uplink has a default route and answers ping.
func baseUplinkOutputs() map[string]string {
	return map[string]string{
		"uci -q get network.lan.ipaddr": "",
		"ip -4 -o addr show dev br-lan": "",
		uplinkRouteCmd:                  "default via 10.10.24.1 dev phy1-sta0 src 10.10.24.145\n",
		uplinkPingCmd:                   "1 packets transmitted, 1 packets received, 0% packet loss",
	}
}

// ─── waitForUplinkResolvers ────────────────────────────────────────

func TestWaitForUplinkResolversPollsUntilTheHandOffLands(t *testing.T) {
	polls := 0
	s := &scriptedRunner{}
	s.dynamic = func(cmd string) (string, bool) {
		switch cmd {
		case uplinkResolvAutoCmd:
			polls++
			if polls < 3 {
				return "", true // resolvers not handed out yet
			}
			return "nameserver 10.10.24.1", true
		case uplinkDNSCmd:
			if polls < 3 {
				return "** server can't find github.com: REFUSED", true
			}
			return "Name:    github.com\nAddress: 140.82.121.4", true
		}
		return "", false
	}
	met, obs := waitForUplinkResolvers(s.run, 10, 0)
	if !met {
		t.Fatalf("precondition not reported met after the hand-off landed; observation: %s", obs)
	}
	if polls != 3 {
		t.Errorf("polled %d times, want 3 (stop as soon as the precondition holds)", polls)
	}
}

func TestWaitForUplinkResolversIsBoundedAndSaysWhatNeverArrived(t *testing.T) {
	// resolvers never land: the wait must give up after exactly `attempts`
	// polls — a gate that waits forever hangs the wizard.
	s := &scriptedRunner{static: map[string]string{
		uplinkResolvAutoCmd: "",
		uplinkResolvCmd:     "",
		uplinkDNSCmd:        "** server can't find github.com: REFUSED",
	}}
	met, obs := waitForUplinkResolvers(s.run, 4, 0)
	if met {
		t.Fatal("precondition reported met with an empty resolv.conf and a REFUSED answer")
	}
	if n := s.count(uplinkResolvAutoCmd); n != 4 {
		t.Errorf("polled %d times, want exactly 4 (bounded)", n)
	}
	if !strings.Contains(obs, "nameserver-in-resolv.auto=false") {
		t.Errorf("observation must name the half that never arrived; got %q", obs)
	}
}

// ─── the regression: cold-start ordering ───────────────────────────

// This is the reported bug, driven through the REAL gate. The upstream is
// healthy from the start; only the DHCP hand-off to dnsmasq is late (the first
// 12 DNS probes REFUSE, then it comes good). The gate must WAIT for the
// precondition and end up online — and it must reach that verdict WITHOUT
// rewriting dhcp config, because nothing was wrong with the upstream.
func TestUpstreamOnlineWaitsOutTheColdStartInsteadOfFailing(t *testing.T) {
	s := &scriptedRunner{static: baseUplinkOutputs()}
	dnsCalls := 0
	s.dynamic = func(cmd string) (string, bool) {
		switch cmd {
		case uplinkResolvAutoCmd:
			if dnsCalls < 12 {
				return "", true // no resolvers handed out yet
			}
			return "nameserver 10.10.24.1", true
		case uplinkDNSCmd:
			dnsCalls++
			if dnsCalls <= 12 {
				return "** server can't find github.com: REFUSED", true
			}
			return "Name:    github.com\nAddress: 140.82.121.4", true
		}
		return "", false
	}
	online, diag := upstreamOnlineWith(s.run, 30, 0)
	if !online {
		t.Fatalf("cold-start gate reported the healthy upstream unusable — this is the bug:\n%s", diag)
	}
	if !strings.Contains(diag, "cold-start-wait: met=true") {
		t.Errorf("diagnostic must record that the wait was satisfied; got:\n%s", diag)
	}
	if strings.Contains(diag, "the upstream handed out resolvers it does not serve") {
		t.Errorf("a waited-out cold start must not be reported as an AP serving resolvers it does not own:\n%s", diag)
	}
	if n := s.count("uci -q add_list"); n != 0 {
		t.Errorf("the gate rewrote dhcp config %d time(s) on a cold start that needed no repair", n)
	}
}

// A gate with no wait (the old implementation) fails this shape: it gives up
// after its fixed probe loop while dnsmasq has not yet been told. Assert the
// bounded wait is what carries it — i.e. a wait budget of 0 does NOT rescue the
// same cold router.
func TestUpstreamOnlineColdStartNeedsTheWait(t *testing.T) {
	s := &scriptedRunner{static: baseUplinkOutputs()}
	calls := 0
	s.dynamic = func(cmd string) (string, bool) {
		switch cmd {
		case uplinkResolvAutoCmd:
			return "", true
		case uplinkDNSCmd:
			calls++
			if calls <= 12 {
				return "** server can't find github.com: REFUSED", true
			}
			return "Address: 140.82.121.4", true
		}
		return "", false
	}
	// No wait budget, and the public resolvers are unreachable in this fixture.
	online, _ := upstreamOnlineWith(s.run, 0, 0)
	if online {
		t.Error("with no wait at all the cold router should still fail — the wait is load-bearing")
	}
}

// ─── the logged router-side repair ─────────────────────────────────

func TestUpstreamOnlineAppliesAndLogsRepairThenRetriesOnce(t *testing.T) {
	// Routing + ping are fine, resolv.conf is populated, but dnsmasq's own
	// upstream resolvers REFUSE; only the public resolver 1.1.1.1 answers.
	s := &scriptedRunner{static: baseUplinkOutputs()}
	s.static[uplinkResolvAutoCmd] = "nameserver 10.10.24.1"
	// The repair command begins with a del_list, so count on that marker.
	repaired := func() int { return s.count("uci -q del_list dhcp.@dnsmasq[0].server") }
	s.dynamic = func(cmd string) (string, bool) {
		switch {
		case cmd == uplinkDNSCmd:
			// Before the repair dnsmasq cannot resolve; after it, it can.
			if repaired() == 0 {
				return "** server can't find github.com: REFUSED", true
			}
			return "Name:    github.com\nAddress: 140.82.121.4", true
		case strings.HasPrefix(cmd, "nslookup github.com 1.1.1.1"):
			return "Name:    github.com\nAddress: 140.82.121.4", true
		case strings.HasPrefix(cmd, "nslookup github.com 9.9.9.9"):
			return "** server can't find github.com: REFUSED", true
		}
		return "", false
	}
	online, diag := upstreamOnlineWith(s.run, 4, 0)
	if !online {
		t.Fatalf("repair did not bring the gate online:\n%s", diag)
	}
	wantCmd := "uci -q add_list dhcp.@dnsmasq[0].server='1.1.1.1'"
	if !strings.Contains(diag, "resolver-fallback:") || !strings.Contains(diag, wantCmd) {
		t.Errorf("diagnostic must state the EXACT repair applied; got:\n%s", diag)
	}
	if n := repaired(); n != 1 {
		t.Errorf("repair applied %d times, want exactly once", n)
	}
	if !strings.Contains(diag, "uci commit dhcp") || !strings.Contains(diag, "/etc/init.d/dnsmasq restart") {
		t.Errorf("repair must commit + restart; got:\n%s", diag)
	}
	// Never silent: the change is also logged on the router.
	logged := false
	for _, c := range s.seen {
		if strings.HasPrefix(c, "logger -t tollgate-installer") && strings.Contains(c, "resolver-fallback") {
			logged = true
		}
	}
	if !logged {
		t.Error("the repair was applied without logging it on the router — a silent router reconfiguration")
	}
}

func TestUpstreamOnlineDoesNotRepairWhenPublicDNSIsBlockedToo(t *testing.T) {
	s := &scriptedRunner{static: baseUplinkOutputs()}
	s.static[uplinkResolvAutoCmd] = "nameserver 10.10.24.1"
	s.static[uplinkDNSCmd] = "** server can't find github.com: REFUSED"
	s.dynamic = func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "nslookup github.com 1.1.1.1") ||
			strings.HasPrefix(cmd, "nslookup github.com 9.9.9.9") {
			return "** server can't find github.com: REFUSED", true
		}
		return "", false
	}
	online, diag := upstreamOnlineWith(s.run, 2, 0)
	if online {
		t.Fatal("reported online with every resolver path dead")
	}
	if n := s.count("uci -q add_list"); n != 0 {
		t.Errorf("rewrote dhcp config %d time(s) although no public resolver answers", n)
	}
	if !strings.Contains(diag, "the upstream network blocks DNS") {
		t.Errorf("verdict must say DNS is blocked outright; got:\n%s", diag)
	}
}

// The wait is only meaningful once the uplink itself is up, so a router with no
// default route must not spend 90s waiting on resolvers.
func TestUpstreamOnlineSkipsTheWaitWhenUplinkIsNotUp(t *testing.T) {
	s := &scriptedRunner{static: map[string]string{
		"uci -q get network.lan.ipaddr": "",
		"ip -4 -o addr show dev br-lan": "",
		uplinkRouteCmd:                  "192.168.1.0/24 dev br-lan scope link  src 192.168.1.1",
		uplinkPingCmd:                   "1 packets transmitted, 0 packets received, 100% packet loss",
	}}
	online, diag := upstreamOnlineWith(s.run, 30, 0)
	if online {
		t.Fatal("reported online with no default route and a dead first hop")
	}
	if !strings.Contains(diag, "cold-start-wait: met=false — skipped") {
		t.Errorf("the wait must be skipped (not spun) with no uplink; got:\n%s", diag)
	}
	if n := s.count(uplinkResolvAutoCmd); n != 0 {
		t.Errorf("polled resolv.conf %d time(s) with no uplink to wait for", n)
	}
	if !strings.Contains(diag, "no default route") {
		t.Errorf("verdict must blame the missing default route; got:\n%s", diag)
	}
}

// ─── dnsmasq is made to follow the uplink, exactly once ────────────

func TestReloadDnsmasqOnceUsesReloadWithRestartFallback(t *testing.T) {
	s := &scriptedRunner{static: map[string]string{}}
	reloadDnsmasqOnce(s.run)
	if n := s.count("/etc/init.d/dnsmasq reload"); n != 1 {
		t.Fatalf("dnsmasq reload issued %d time(s), want exactly once", n)
	}
	if len(s.seen) != 1 || !strings.Contains(s.seen[0], "/etc/init.d/dnsmasq restart") {
		t.Errorf("reload must fall back to restart; got %v", s.seen)
	}
}

// ─── end-to-end through a real SSH session (no router, no hardware) ─

// Drives the REAL upstreamOnline code path — every command through a real
// sshRun and a real SSH transport — against a canned router whose DHCP
// hand-off is late. Asserts the gate reloads dnsmasq and waits out the cold
// start rather than failing, and that it issues no config repair.
func TestUpstreamOnlineOverSSHReloadsAndWaitsOutColdStart(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped in -short mode")
	}
	var mu sync.Mutex
	resolvPolls := 0
	router := startCannedRouter(t, func(cmd string) string {
		mu.Lock()
		defer mu.Unlock()
		switch cmd {
		case "uci -q get network.lan.ipaddr", "ip -4 -o addr show dev br-lan":
			return ""
		case uplinkRouteCmd:
			return "default via 10.10.24.1 dev phy1-sta0 src 10.10.24.145\n"
		case uplinkPingCmd:
			return "1 packets transmitted, 1 packets received, 0% packet loss"
		case uplinkResolvAutoCmd:
			resolvPolls++
			if resolvPolls < 2 {
				return "" // cold: the lease's resolvers are not in the file yet
			}
			return "nameserver 10.10.24.1"
		case uplinkDNSCmd:
			if resolvPolls < 2 {
				return "** server can't find github.com: REFUSED"
			}
			return "Name:    github.com\nAddress: 140.82.121.4"
		}
		return ""
	})
	client := router.dial(t)
	defer client.Close()

	online, diag := upstreamOnlineWith(func(cmd string) string { return sshRun(client, cmd) }, 8, 10*time.Millisecond)
	if !online {
		t.Fatalf("gate failed over a real SSH session on a cold-start router:\n%s", diag)
	}
	joined := strings.Join(router.sentCommands(), "\n")
	if !strings.Contains(joined, "/etc/init.d/dnsmasq reload") {
		t.Error("upstreamOnline did not make dnsmasq follow the uplink (no reload issued)")
	}
	if strings.Contains(joined, "uci -q add_list dhcp.@dnsmasq[0].server") {
		t.Error("cold start applied a dhcp repair it did not need")
	}
	if !strings.Contains(diag, "cold-start-wait: met=true") {
		t.Errorf("cold-start wait not recorded in the real-path diagnostic:\n%s", diag)
	}
}
