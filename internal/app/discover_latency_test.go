package app

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// discover_latency_test.go is the regression lock for the scan-latency defect
// measured on CobradorWave on 2026-10-05 with NO router attached:
//
//	shipped discoverRouters (sequential)   25.0 s / 29.0 s
//	route-aware candidate filter only       9.5 s
//	same probes, fanned out (16 workers)    5.0 s
//	route-aware + fanned out                4.1 s
//
// The cause was NOT slow routers: it was fixed timeout waste. `ip route get`
// showed 4 of the 6 shipped guesses route VIA THE DEFAULT GATEWAY (their subnets
// are absent here) → the SYN is blackholed → each one burned the full 2 s SSH
// probe plus 3 × 1 s HTTP probes = 5 s, serially. ~80 % of the scan was waiting
// for subnets that are not present.
//
// Three properties are pinned below, each against a host with no router:
//
//  1. a host costs the SLOWEST port probe, not the SUM of its ports;
//  2. a scan costs the slowest candidate WAVE, not the SUM of its candidates;
//  3. the per-port budget is a LAN budget (< 1 s), not the shipped 2 s/1 s.
//
// Plus the ordering contract: the default gateway is always a candidate and is
// ordered before every ARP-derived one, and an UNROUTABLE-by-default-gw
// candidate is DEMOTED (indexed after the directly-connected ones), never
// dropped.

// TestProbeRouterProbesPortsInParallel: a dead host must cost the slowest single
// port probe, not the sum of all four. Serial, this is 4 × the injected delay.
func TestProbeRouterProbesPortsInParallel(t *testing.T) {
	orig := tcpProbeFn
	defer func() { tcpProbeFn = orig }()

	const portDelay = 300 * time.Millisecond
	var mu sync.Mutex
	var budgets []time.Duration
	tcpProbeFn = func(ip string, port int, timeout time.Duration) bool {
		mu.Lock()
		budgets = append(budgets, timeout)
		mu.Unlock()
		// An unreachable port: it burns the whole budget before giving up.
		time.Sleep(portDelay)
		return false
	}

	start := time.Now()
	info := probeRouterWithPassword("203.0.113.9", "")
	elapsed := time.Since(start)

	if info.SSH || info.HTTPPort != 0 {
		t.Errorf("a host that answered nothing must be all-empty, got %+v", info)
	}
	if n := len(budgets); n != 4 {
		t.Errorf("probed %d port(s), want 4 (ssh + 80 + 443 + 8080)", n)
	}
	// Serial would be 4 × 300 ms = 1.2 s. Fanned out, one dead host costs the
	// slowest single probe.
	if elapsed > 800*time.Millisecond {
		t.Errorf("probing one dead host took %v — the per-port probes are still SERIAL (four ports must cost the slowest probe, not their sum)", elapsed)
	}
}

// TestProbeBudgetIsLANAppropriate pins the budget handed to every probe: the
// shipped 2 s SSH + 1 s/port is what turned "no router here" into a 25-29 s
// wait. A directly connected LAN host answers in milliseconds or not at all.
func TestProbeBudgetIsLANAppropriate(t *testing.T) {
	orig := tcpProbeFn
	defer func() { tcpProbeFn = orig }()

	var mu sync.Mutex
	seen := map[time.Duration]bool{}
	tcpProbeFn = func(ip string, port int, timeout time.Duration) bool {
		mu.Lock()
		seen[timeout] = true
		mu.Unlock()
		return false
	}
	probeRouterWithPassword("203.0.113.9", "")

	if len(seen) == 0 {
		t.Fatal("no probe ran at all")
	}
	for budget := range seen {
		if budget > 800*time.Millisecond {
			t.Errorf("a port probe was given a %v budget — a dead LAN host must cost < 1 s, not the shipped 2 s/1 s", budget)
		}
	}
	if probeTimeout > 800*time.Millisecond {
		t.Errorf("probeTimeout = %v, want <= 800ms", probeTimeout)
	}
}

// TestScanNetworkBoundedTimeWithUnreachableCandidates is the end-to-end latency
// lock: 30 ARP neighbours the host cannot reach on-link (the "shared WiFi" shape
// where the ARP table is full of silent strangers) plus the shipped guesses,
// every probe burning its whole budget. Serially that is the measured 25-29 s;
// fanned out the scan costs the slowest wave.
func TestScanNetworkBoundedTimeWithUnreachableCandidates(t *testing.T) {
	origNet, origProbe, origIfaces := collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn
	defer func() {
		collectHostNetworkFn, probeCandidateFn, collectInterfaceStatesFn = origNet, origProbe, origIfaces
	}()

	n := hostNetwork{
		Gateways: []gatewayRoute{{IP: "10.47.41.1", Iface: "enp1s0"}},
		Addrs:    []localAddr{{Iface: "wlan0", CIDR: "192.168.2.33/24"}},
		Common:   append([]string{}, commonIPs...),
	}
	for i := 0; i < 30; i++ {
		// Neighbours on a subnet this host does NOT hold: routed via the
		// default gateway, so each one costs a full probe budget.
		n.ARP = append(n.ARP, arpEntry{
			IP:    fmt.Sprintf("172.16.9.%d", i+2),
			MAC:   fmt.Sprintf("02:11:22:33:44:%02x", i),
			Iface: "wlan0",
		})
	}
	collectHostNetworkFn = func() hostNetwork { return n }
	collectInterfaceStatesFn = func() []ifaceState {
		return []ifaceState{{Name: "wlan0", Up: true, Carrier: "1", IPv4: []string{"192.168.2.33/24"}}}
	}

	const probeDelay = 150 * time.Millisecond
	probeCandidateFn = func(ip string) RouterInfo {
		time.Sleep(probeDelay) // an unreachable subnet: no answer, full budget gone
		return RouterInfo{Vendor: "unknown", Model: "unknown", Firmware: "unknown"}
	}

	start := time.Now()
	res := scanNetwork()
	elapsed := time.Since(start)

	if len(res.Routers) != 0 {
		t.Fatalf("routers = %+v, want none (nothing answered)", res.Routers)
	}
	if res.Diagnostics == nil {
		t.Fatal("a scan that found nothing must produce the diagnostic block")
	}
	if len(res.Probes) < 30 {
		t.Fatalf("probes = %d, want every candidate probed (the ARP neighbours must not be dropped)", len(res.Probes))
	}
	serial := time.Duration(len(res.Probes)) * probeDelay
	if elapsed >= serial/2 {
		t.Errorf("scanning %d unreachable candidates took %v — serial would be %v and a fanned-out scan must cost the slowest wave (~%v × ceil(n/%d))",
			len(res.Probes), elapsed, serial, probeDelay, scanWorkers)
	}
}

// TestDefaultGatewayIsCandidateAndFirst pins the ordering the operator depends
// on: the address the OS itself calls "the router" is always probed, and it is
// probed before any neighbour-table entry.
func TestDefaultGatewayIsCandidateAndFirst(t *testing.T) {
	n := hostNetwork{
		Gateways: []gatewayRoute{{IP: "192.168.2.1", Iface: "wlp58s0"}},
		Addrs:    []localAddr{{Iface: "wlp58s0", CIDR: "192.168.2.33/24"}},
		Common:   []string{"192.168.1.1", "192.168.8.1"},
		ARP: []arpEntry{
			{IP: "192.168.2.34", MAC: "d4:f3:2d:d2:18:74", Iface: "wlp58s0"},
			{IP: "192.168.2.48", MAC: "74:bf:c0:ac:ae:b4", Iface: "wlp58s0"},
		},
	}
	got := hostCandidates(n)
	if len(got) == 0 {
		t.Fatal("no candidates at all — the default gateway was lost")
	}
	if got[0].IP != "192.168.2.1" || got[0].Source != sourceGateway {
		t.Errorf("first candidate = %+v, want the default-route gateway 192.168.2.1", got[0])
	}
	gwIdx, arpIdx := -1, -1
	for i, c := range got {
		if c.IP == "192.168.2.1" {
			gwIdx = i
		}
		if c.Source == sourceARP && arpIdx == -1 {
			arpIdx = i
		}
	}
	if gwIdx == -1 {
		t.Fatal("the default gateway is not in the candidate set at all")
	}
	if arpIdx != -1 && arpIdx < gwIdx {
		t.Errorf("an ARP-derived candidate is ordered before the default gateway: %v", candIPs(got))
	}
}

// TestUnroutableCandidateIsDemotedNotDropped pins the demotion rule: a candidate
// whose route would go `via <default gateway>` (the host holds no subnet
// containing it) is indexed AFTER every directly-connected candidate — but it is
// still probed, because a router in factory configuration can be reachable
// through a route the host does not own.
func TestUnroutableCandidateIsDemotedNotDropped(t *testing.T) {
	// This host holds 10.47.41.0/24 only, so `ip route get 192.168.1.1` answers
	// "via 10.47.41.1": the subnet is absent, the SYN is blackholed.
	n := hostNetwork{
		Gateways: []gatewayRoute{{IP: "10.47.41.1", Iface: "enp1s0"}},
		Addrs:    []localAddr{{Iface: "enp1s0", CIDR: "10.47.41.23/24"}},
		Common:   []string{"192.168.0.1", "192.168.1.1"},
		ARP:      []arpEntry{{IP: "10.47.41.55", MAC: "02:11:22:33:44:55", Iface: "enp1s0"}},
	}
	got := hostCandidates(n)

	byIP := map[string]candidate{}
	lastDirect, firstDemoted := -1, -1
	for i, c := range got {
		byIP[c.IP] = c
		if c.Demoted {
			if firstDemoted == -1 {
				firstDemoted = i
			}
			continue
		}
		lastDirect = i
	}

	for _, ip := range []string{"192.168.0.1", "192.168.1.1"} {
		c, ok := byIP[ip]
		if !ok {
			t.Fatalf("candidate %s was DROPPED — an unroutable candidate must be demoted, never removed (got %v)", ip, candIPs(got))
		}
		if !c.Demoted {
			t.Errorf("%s routes via the default gateway and must be demoted: %+v", ip, c)
		}
	}
	if firstDemoted == -1 {
		t.Fatalf("nothing was demoted at all: %v", candIPs(got))
	}
	if firstDemoted < lastDirect {
		t.Errorf("candidate order = %v — a demoted (unroutable) address is indexed before a directly-connected one", candIPs(got))
	}
	for _, ip := range []string{"10.47.41.1", "10.47.41.254", "10.47.41.55"} {
		c, ok := byIP[ip]
		if !ok {
			t.Errorf("directly-connected candidate %s is missing: %v", ip, candIPs(got))
			continue
		}
		if c.Demoted {
			t.Errorf("%s is on a subnet we hold and must not be demoted: %+v", ip, c)
		}
	}
}
