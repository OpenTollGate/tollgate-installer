package app

import "testing"

// discover_candidates_test.go pins the candidate builder for the scan.
//
// The defect this file is written against (measured on CobradorWave,
// 2026-09-25): discover.go built its candidate list from exactly two sources —
// six hard-coded addresses and whatever sat in the ARP table — so the address
// the host's own routing table proves is the router (its default gateway, and
// the .1/.254 of every subnet the host holds an IPv4 on) was probed only by
// accident, and a container bridge or a stranger's laptop in the ARP table was
// probed as if it were a router.
//
// Everything here is a table test over hostCandidates(), a PURE function of the
// host's network state, so the expected order is asserted without touching the
// network or shelling out.

// candIPs renders a candidate list as bare addresses, in order.
func candIPs(cs []candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.IP)
	}
	return out
}

func assertCandidates(t *testing.T, got []candidate, want []string) {
	t.Helper()
	gotIPs := candIPs(got)
	if len(gotIPs) != len(want) {
		t.Fatalf("candidate count = %d (%v), want %d (%v)", len(gotIPs), gotIPs, len(want), want)
	}
	for i := range want {
		if gotIPs[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q (full: %v)", i, gotIPs[i], want[i], gotIPs)
		}
	}
}

// TestHostCandidatesPriorityOrder pins the documented priority order and the
// dedupe. The list is built in two blocks: everything the host can reach without
// leaving a subnet it holds (gateway, then the .1/.254 of each on-link subnet,
// then the neighbours ON those subnets), and then — DEMOTED, but kept — every
// address the host would have to reach THROUGH the default gateway. A candidate
// reachable from several sources keeps its HIGHEST-priority position and source.
func TestHostCandidatesPriorityOrder(t *testing.T) {
	n := hostNetwork{
		Gateways: []gatewayRoute{{IP: "10.47.41.1", Iface: "enp1s0"}},
		Addrs:    []localAddr{{Iface: "enp1s0", CIDR: "192.168.77.23/24"}},
		Common:   []string{"192.168.1.1", "192.168.8.1"},
		ARP: []arpEntry{
			{IP: "192.168.77.9", MAC: "aa:bb:cc:dd:ee:ff", Iface: "enp1s0"},
			// Also a common IP: must NOT be duplicated and must keep the
			// earlier (route-derived) source.
			{IP: "192.168.1.1", MAC: "aa:bb:cc:dd:ee:00", Iface: "enp1s0"},
		},
	}
	got := hostCandidates(n)
	assertCandidates(t, got, []string{
		"10.47.41.1",     // 1. default-route gateway
		"192.168.77.1",   // 2. subnet .1 of enp1s0 192.168.77.23/24
		"192.168.77.254", // 2. subnet .254
		"192.168.77.9",   // 3. a neighbour ON our subnet (reachable without a router)
		"192.168.1.1",    // 4. common (ARP duplicate dropped) — DEMOTED
		"192.168.8.1",    // 4. common — DEMOTED
	})

	byIP := map[string]candidate{}
	for _, c := range got {
		byIP[c.IP] = c
	}
	if c := byIP["10.47.41.1"]; c.Source != sourceGateway || c.Detail == "" {
		t.Errorf("gateway candidate = %+v, want source %q and a non-empty detail naming the interface", c, sourceGateway)
	}
	if c := byIP["192.168.77.254"]; c.Source != sourceSubnet || c.Detail == "" {
		t.Errorf("subnet candidate = %+v, want source %q and a non-empty detail naming the interface + CIDR", c, sourceSubnet)
	}
	if c := byIP["192.168.8.1"]; c.Source != sourceCommon {
		t.Errorf("common candidate = %+v, want source %q", c, sourceCommon)
	}
	if c := byIP["192.168.77.9"]; c.Source != sourceARP {
		t.Errorf("arp candidate = %+v, want source %q", c, sourceARP)
	}
	if c := byIP["192.168.1.1"]; c.Source != sourceCommon {
		t.Errorf("192.168.1.1 = %+v, want the first (common) source, not the ARP duplicate", c)
	}
	if c := byIP["192.168.77.9"]; c.Demoted {
		t.Errorf("a neighbour on a subnet we hold must not be demoted: %+v", c)
	}
}

// TestHostCandidatesGatewayOutranksCommonIP is the core blind-spot fix: the
// gateway the OS itself selected is strictly better evidence than a guess from
// the hard-coded list, so it must be probed first even when it is also a
// well-known address.
func TestHostCandidatesGatewayOutranksCommonIP(t *testing.T) {
	n := hostNetwork{
		Gateways: []gatewayRoute{{IP: "192.168.2.1", Iface: "wlp58s0"}},
		Common:   []string{"192.168.1.1", "192.168.2.1"},
	}
	got := hostCandidates(n)
	assertCandidates(t, got, []string{"192.168.2.1", "192.168.1.1"})
	if got[0].Source != sourceGateway {
		t.Errorf("first candidate source = %q, want %q", got[0].Source, sourceGateway)
	}
}

// TestHostCandidatesEveryDefaultGateway covers multi-uplink hosts: every
// default route is probed, in routing-table order.
func TestHostCandidatesEveryDefaultGateway(t *testing.T) {
	n := hostNetwork{
		Gateways: []gatewayRoute{
			{IP: "192.168.2.1", Iface: "wlp58s0"},
			{IP: "10.8.8.1", Iface: "wwan0"},
		},
	}
	assertCandidates(t, hostCandidates(n), []string{"192.168.2.1", "10.8.8.1"})
}

// TestHostCandidatesSkipsVirtualInterfaces pins the exclusion list: container
// and tunnel interfaces must contribute no subnet candidates — on this host
// those are docker0 172.17.0.1/16, two br-* bridges 172.18/172.20.0.1/16, four
// veth pairs, wt0 100.90.101.9/16 (mesh) and fips0.
func TestHostCandidatesSkipsVirtualInterfaces(t *testing.T) {
	n := hostNetwork{
		Addrs: []localAddr{
			{Iface: "wlan0", CIDR: "192.168.2.33/24"},
			{Iface: "docker0", CIDR: "172.17.0.1/16"},
			{Iface: "br-6a3ff369d2db", CIDR: "172.18.0.1/16"},
			{Iface: "br-e65f4ba1d5c1", CIDR: "172.20.0.1/16"},
			{Iface: "vethaaf4ae7", CIDR: "169.254.7.7/16"},
			{Iface: "wt0", CIDR: "100.90.101.9/16"},
			{Iface: "fips0", CIDR: "10.60.1.1/24"},
			{Iface: "tun0", CIDR: "10.9.9.1/24"},
			{Iface: "tap0", CIDR: "10.9.10.1/24"},
			{Iface: "wg0", CIDR: "10.8.8.1/24"},
			{Iface: "lo", CIDR: "127.0.0.1/8"},
		},
	}
	assertCandidates(t, hostCandidates(n), []string{"192.168.2.1", "192.168.2.254"})
}

// TestHostCandidatesNeverProbesItself: a host that holds the .1 of its own
// subnet must not offer that address as a router. On a laptop that runs sshd,
// probing ourselves answers :22 and would be reported as a found device — the
// same false-positive class as a stranger's machine in the ARP table. The
// subnet's .254 is still a legitimate candidate.
func TestHostCandidatesNeverProbesItself(t *testing.T) {
	n := hostNetwork{
		Addrs:  []localAddr{{Iface: "eth0", CIDR: "192.168.2.1/24"}},
		Common: []string{"192.168.2.1", "192.168.2.254"},
		ARP:    []arpEntry{{IP: "192.168.2.1", MAC: "aa:bb:cc:dd:ee:ff", Iface: "eth0"}},
	}
	got := hostCandidates(n)
	for _, c := range got {
		if c.IP == "192.168.2.1" {
			t.Errorf("the host's own address was offered as a candidate: %+v", c)
		}
	}
	assertCandidates(t, got, []string{"192.168.2.254"})
}

// TestHostCandidatesDropsARPEntriesOnVirtualInterfaces: the container bridges
// that produced 172.18.0.254 / 172.20.0.2 in the measured evidence are dropped
// by the interface that owns the neighbour entry. An entry whose interface the
// parser could not determine is KEPT — the operator's router must never be
// silently dropped.
func TestHostCandidatesDropsARPEntriesOnVirtualInterfaces(t *testing.T) {
	n := hostNetwork{
		Addrs: []localAddr{{Iface: "wlan0", CIDR: "192.168.2.33/24"}},
		ARP: []arpEntry{
			{IP: "172.18.0.254", MAC: "02:42:ac:12:00:fe", Iface: "br-6a3ff369d2db"},
			{IP: "172.20.0.2", MAC: "26:56:c8:1b:95:c2", Iface: "br-e65f4ba1d5c1"},
			{IP: "192.168.2.48", MAC: "74:bf:c0:ac:ae:b4", Iface: "wlan0"},
			{IP: "192.168.2.34", MAC: "d4:f3:2d:d2:18:74"},                 // iface unknown
			{IP: "192.168.2.33", MAC: "aa:bb:cc:dd:ee:ff", Iface: "wlan0"}, // our own address
		},
	}
	assertCandidates(t, hostCandidates(n), []string{
		"192.168.2.1", // subnet .1 of wlan0
		"192.168.2.254",
		"192.168.2.48",
		"192.168.2.34",
	})
}

// TestSubnetAddresses covers the .1/.254 derivation for real prefixes. The
// address is derived from the REAL network mask, and the address the host
// itself holds is never emitted.
func TestSubnetAddresses(t *testing.T) {
	cases := []struct {
		name string
		in   localAddr
		want []string
	}{
		{"slash24", localAddr{Iface: "eth0", CIDR: "10.47.41.23/24"}, []string{"10.47.41.1", "10.47.41.254"}},
		{"slash16", localAddr{Iface: "eth0", CIDR: "10.47.41.23/16"}, []string{"10.47.0.1", "10.47.0.254"}},
		{"slash30 keeps only .1", localAddr{Iface: "eth0", CIDR: "10.0.0.2/30"}, []string{"10.0.0.1"}},
		{"slash31 has no host addresses", localAddr{Iface: "eth0", CIDR: "10.0.0.0/31"}, nil},
		{"slash32 has no host addresses", localAddr{Iface: "eth0", CIDR: "10.0.0.1/32"}, nil},
		{"host IS .1", localAddr{Iface: "eth0", CIDR: "192.168.2.1/24"}, []string{"192.168.2.254"}},
		{"host IS .254", localAddr{Iface: "eth0", CIDR: "192.168.2.254/24"}, []string{"192.168.2.1"}},
		{"garbage", localAddr{Iface: "eth0", CIDR: "not-a-cidr"}, nil},
		{"ipv6 only", localAddr{Iface: "eth0", CIDR: "fe80::1/64"}, nil},
		{"empty", localAddr{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertCandidates(t, subnetCandidates(tc.in), tc.want)
		})
	}
}

// TestIsVirtualIface pins the exclusion predicate. A miss here (classifying a
// real LAN interface as virtual) hides the operator's router; a false positive
// (classifying a container bridge as real) probes a subnet that cannot hold it.
func TestIsVirtualIface(t *testing.T) {
	virtual := []string{
		"lo", "docker0", "br-6a3ff369d2db", "vethaaf4ae7", "virbr0", "vnet0",
		"tun0", "tap0", "wg0", "fips0", "wt0", "tailscale0", "ztabcdefg",
		"sit0", "gre0", "ip6tnl0", "ppp0", "dummy0", "lxcbr0", "vmnet1", "mon0",
	}
	real := []string{"eth0", "enp0s31f6", "en0", "wlan0", "wlp58s0", "wlan1", "wwan0", "br0", "usb0"}
	for _, name := range virtual {
		if !isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = false, want true", name)
		}
	}
	for _, name := range real {
		if isVirtualIface(name) {
			t.Errorf("isVirtualIface(%q) = true, want false (%q is a real interface)", name, name)
		}
	}
}

// TestParseIPRouteDefault covers `ip -4 route show default` on Linux, including
// the multiple default routes of a multi-uplink host and the two shapes that
// carry no usable IPv4 gateway.
func TestParseIPRouteDefault(t *testing.T) {
	out := `default via 192.168.2.1 dev wlp58s0 proto dhcp src 192.168.2.33 metric 100
default via 10.8.8.1 dev wwan0 proto dhcp src 10.8.8.5 metric 600
192.168.2.0/24 dev wlp58s0 proto kernel scope link src 192.168.2.33
default dev ppp0 scope link
default via fe80::1 dev eth0 proto ra metric 100
0.0.0.0/0 via 192.168.5.1 dev eth1 proto dhcp metric 50
`
	got := parseIPRouteDefault(out)
	want := []gatewayRoute{
		{IP: "192.168.2.1", Iface: "wlp58s0"},
		{IP: "10.8.8.1", Iface: "wwan0"},
		{IP: "192.168.5.1", Iface: "eth1"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseIPRouteDefault = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseNetstatDefault covers macOS/BSD `netstat -rn -f inet` and the
// Linux netstat table shape. A resolved "link#5" gateway is not an IPv4
// address and must be skipped, header lines must not be mistaken for routes.
func TestParseNetstatDefault(t *testing.T) {
	bsd := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.2.1        UGScg          en0
default            fe80::1%en0        UGcIg          en0
127                127.0.0.1          UCS            lo0
192.168.2          link#5             UCS            en0
`
	got := parseNetstatDefault(bsd)
	if len(got) != 1 || got[0].IP != "192.168.2.1" {
		t.Errorf("parseNetstatDefault(bsd) = %+v, want one entry 192.168.2.1", got)
	}

	linux := `Kernel IP routing table
Destination     Gateway         Genmask         Flags Metric Ref    Use Iface
0.0.0.0         192.168.2.1     0.0.0.0         UG    600    0        0 wlp58s0
192.168.2.0     0.0.0.0         255.255.255.0   U     600    0        0 wlp58s0
`
	got = parseNetstatDefault(linux)
	if len(got) != 1 || got[0].IP != "192.168.2.1" || got[0].Iface != "wlp58s0" {
		t.Errorf("parseNetstatDefault(linux) = %+v, want one entry 192.168.2.1 on wlp58s0", got)
	}
}

// TestParseWindowsRouteDefault covers `route print -4`'s Active Routes table:
// the all-zero destination AND mask identify a default route, and "On-link"
// (a directly attached network) is not a gateway address.
func TestParseWindowsRouteDefault(t *testing.T) {
	out := `===========================================================================
Interface List
 12...aa bb cc dd ee ff ......Intel(R) Ethernet
===========================================================================
IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.100     25
        127.0.0.0        255.0.0.0         On-link         127.0.0.1    331
     192.168.1.0    255.255.255.0         On-link     192.168.1.100    281
===========================================================================
`
	got := parseWindowsRouteDefault(out)
	if len(got) != 1 || got[0].IP != "192.168.1.1" {
		t.Errorf("parseWindowsRouteDefault = %+v, want one entry 192.168.1.1", got)
	}
}

// TestParseARPTableIface pins the interface column the candidate filter needs:
// `dev <iface>` (ip neigh), `on <iface>` (BSD and net-tools arp). Losing it
// would let container-bridge neighbours back into the candidate list, so it is
// asserted on every shape the parser claims to handle.
func TestParseARPTableIface(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ip neigh", "192.168.2.1 dev wlp58s0 lladdr e8:8f:6f:df:9e:11 REACHABLE\n", "wlp58s0"},
		{"bsd arp", "? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 ifscope [ethernet]\n", "en0"},
		{"net-tools arp", "gateway (192.168.1.1) at e8:8f:6f:df:9e:11 [ether] on eth0\n", "eth0"},
		{"no iface column", "192.168.2.9 lladdr aa:bb:cc:dd:ee:ff REACHABLE\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := parseARPTable(tc.in)
			if len(entries) != 1 {
				t.Fatalf("parseARPTable(%q) = %+v, want exactly one entry", tc.in, entries)
			}
			if entries[0].Iface != tc.want {
				t.Errorf("Iface = %q, want %q", entries[0].Iface, tc.want)
			}
		})
	}
}
