package app

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RouterInfo struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Firmware string `json:"firmware"`
	// Name is a friendly display label (e.g. "GL-MT3000") derived from
	// model/vendor/OUI. The UI shows it instead of a bare IP so the operator
	// can tell devices apart. Best-effort: never affects routing or deploy.
	Name     string `json:"name"`
	SSH      bool   `json:"ssh_open"`
	HTTPPort int    `json:"http_port,omitempty"`
	// SSHRefusal explains why SSH identification could not run: the router's
	// host-key refusal (fingerprint plus the exact trust instruction) when there
	// was one. sshIdentify returns silently — its only other channel is the
	// binary's stderr, which a browser-first operator (curl|bash: the launcher
	// redirects it into a log file) never sees — so /api/scan and /api/identify
	// carry it and the UI renders it next to the router (RISK item of the #41
	// review).
	SSHRefusal string `json:"ssh_refusal,omitempty"`
	// SSHFingerprint is the OpenSSH SHA256 fingerprint the router presented when
	// it was refused, kept as its own field so the browser wizard can offer to
	// trust exactly that key (POST /api/trust-host-key) instead of trying to
	// parse it back out of the prose in SSHRefusal. Empty when no key was seen.
	SSHFingerprint string `json:"ssh_fingerprint,omitempty"`
	// Source records WHERE this address came from: a default-route gateway, a
	// subnet address derived from a local interface, a well-known router
	// address, the neighbour table, or the operator typing it. Strictly
	// evidence about the candidate, never about the device.
	Source string `json:"source,omitempty"`
	// Identified is true only when the device said what it is (SSH
	// identification, i.e. OpenWrt or stock GL.iNet firmware). A device that
	// merely answered a probed port is NOT a router and must never be
	// presented as one — see Note.
	Identified bool `json:"identified"`
	// Note is the operator-facing classification label. Identified devices get
	// a "<vendor> router (identified)" label; everything else gets an
	// "unverified host on your LAN — answers :22" style label and sorts after
	// the identified ones. It is never empty for a scanned candidate.
	Note string `json:"note,omitempty"`
}

// sshProbePort is the TCP port that decides whether a host answers SSH. It is a
// variable only so the router-trust tests can aim this real discovery path at an
// in-process SSH server (see sshDialPort in ssh.go); production always uses 22.
var sshProbePort = 22

// probeTimeout is the per-PORT TCP budget for one host on a directly connected
// LAN. A neighbouring host answers a SYN in single-digit milliseconds; anything
// that has not answered inside this window is not going to. The shipped value
// was 2 s for SSH plus 1 s per HTTP port, probed SERIALLY, so a candidate with no
// router behind it cost up to 5 s of the operator's wait — and 4 of the 6
// shipped guesses route via the default gateway on an ordinary laptop, so the
// SYN is blackholed and every one of them burned the full budget. Measured on
// CobradorWave (2026-10-05, no router attached): /api/scan took 25.0 s and
// 29.0 s before this change.
var probeTimeout = 800 * time.Millisecond

// scanWorkers bounds how many candidates are probed at once. The scan must cost
// the SLOWEST probe, not the sum of all of them: with the serial loop the
// measured 25-29 s scan is dominated by timeout waste on subnets the host does
// not hold.
const scanWorkers = 16

// tcpProbeFn is the TCP reachability check. A variable so the port fan-out can
// be measured (and a dead LAN host simulated) without a network.
var tcpProbeFn = tcpProbe

// ─── candidate discovery ──────────────────────────────────────

// Candidate provenance. The wizard's failure block names these, so the operator
// can see which KIND of evidence produced each address that was probed.
const (
	sourceGateway = "default-route gateway"
	sourceSubnet  = "local subnet address"
	sourceCommon  = "well-known router address"
	sourceARP     = "ARP/neighbour table"
	sourceManual  = "typed by the operator"
)

// commonIPs are the addresses shipped as guesses in the hope that one of them
// is a router in its factory configuration. They are the WEAKEST evidence the
// scan has — on a host that holds none of these subnets the route for each one
// goes via the default gateway, so the probe is blackholed and costs the full
// per-port budget. They are still probed (a factory-default router may be
// reachable through a route the host does not own), only after everything the
// host's own routing table proves is local.
var commonIPs = []string{
	"192.168.1.1", "192.168.8.1", "192.168.0.1", "192.168.2.1",
	"10.47.41.1", "192.168.21.1",
}

// candidate is one address the scan will probe, with the evidence that put it
// on the list.
type candidate struct {
	IP     string `json:"ip"`
	Source string `json:"source"`
	// Detail names the interface (and CIDR, or MAC) behind the candidate so the
	// failure block can be read like a routing table.
	Detail string `json:"detail,omitempty"`
	// Demoted is true when the host holds NO subnet containing this address,
	// i.e. `ip route get <ip>` would answer `via <default gateway>`. That is
	// exactly the case that costs a full probe budget for nothing, so these
	// candidates are probed LAST. They are never dropped: a router in factory
	// configuration can be reachable through a route the host does not own.
	Demoted bool `json:"demoted,omitempty"`
}

// localAddr is one IPv4 address the host holds, as "iface + CIDR".
type localAddr struct {
	Iface string
	CIDR  string
}

// gatewayRoute is one default route: the gateway address and the interface the
// OS would send traffic through.
type gatewayRoute struct {
	IP    string
	Iface string
}

// hostNetwork is the raw host state the candidate list is built from. Passing it
// in (instead of reading the host inside) is what makes hostCandidates pure and
// table-testable.
type hostNetwork struct {
	Gateways []gatewayRoute
	Addrs    []localAddr
	Common   []string
	ARP      []arpEntry
}

// hostCandidates returns the addresses to probe, in priority order:
//
//  1. every default-route gateway (what the OS itself calls "the router"),
//  2. the .1 and .254 of every on-link subnet the host holds an IPv4 on,
//  3. every neighbouring host the ARP/neighbour table places on those subnets,
//  4. everything else — the well-known guesses, and neighbours the host would
//     have to reach THROUGH the default gateway. These are DEMOTED (probed
//     last) but never dropped.
//
// Addresses are deduped, keeping the highest-priority source, and the host's own
// addresses are never probed. Pure: no shelling out, no network.
func hostCandidates(n hostNetwork) []candidate {
	seen := make(map[string]bool)
	var direct, deferred []candidate
	add := func(ip, source, detail string, demoted bool) {
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
		c := candidate{IP: ip, Source: source, Detail: detail, Demoted: demoted}
		if demoted {
			deferred = append(deferred, c)
			return
		}
		direct = append(direct, c)
	}

	// The host's own addressing: its addresses are never probed (a laptop
	// running sshd at the .1 of its own subnet answered :22 and was reported as
	// a found "router"), and the subnets it holds define what is on-link.
	own := map[string]bool{}
	var onLink []*net.IPNet
	for _, a := range n.Addrs {
		ip, ipnet, err := net.ParseCIDR(a.CIDR)
		if err != nil {
			continue
		}
		own[ip.String()] = true
		if !isVirtualIface(a.Iface) {
			onLink = append(onLink, ipnet)
		}
	}
	directlyConnected := func(ip string) bool {
		p := net.ParseIP(ip)
		if p == nil {
			return false
		}
		for _, nw := range onLink {
			if nw.Contains(p) {
				return true
			}
		}
		return false
	}

	// 1. Default-route gateways, in routing-table order.
	for _, g := range n.Gateways {
		if own[g.IP] {
			continue
		}
		add(g.IP, sourceGateway, "dev "+g.Iface, false)
	}

	// 2. The .1/.254 of every non-virtual subnet the host holds.
	for _, a := range n.Addrs {
		if isVirtualIface(a.Iface) {
			continue
		}
		for _, c := range subnetCandidates(a) {
			if own[c.IP] {
				continue
			}
			add(c.IP, c.Source, c.Detail, false)
		}
	}

	// 3. Well-known guesses. Demoted when the host holds no subnet containing
	// them: their route goes via the default gateway.
	for _, ip := range n.Common {
		if own[ip] {
			continue
		}
		add(ip, sourceCommon, "", !directlyConnected(ip))
	}

	// 4. Neighbour-table entries. An entry on a container/tunnel interface
	// cannot be the operator's LAN router; an entry whose interface the parser
	// could not determine is KEPT (never silently drop the operator's router).
	for _, e := range n.ARP {
		if own[e.IP] {
			continue
		}
		if e.Iface != "" && isVirtualIface(e.Iface) {
			continue
		}
		add(e.IP, sourceARP, e.MAC, !directlyConnected(e.IP))
	}

	out := make([]candidate, 0, len(direct)+len(deferred))
	out = append(out, direct...)
	out = append(out, deferred...)
	return out
}

// subnetCandidates returns the .1 and .254 of a's subnet, omitting addresses
// outside the prefix and the address the host itself holds.
func subnetCandidates(a localAddr) []candidate {
	ip, ipnet, err := net.ParseCIDR(a.CIDR)
	if err != nil {
		return nil
	}
	v4 := ip.To4()
	if v4 == nil || ipnet.IP.To4() == nil {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	// A /31 or /32 has no host addresses, and a /0 is not an interface address.
	if bits != 32 || ones == 0 || ones >= 31 {
		return nil
	}
	mask := binary.BigEndian.Uint32(ipnet.Mask)
	net32 := binary.BigEndian.Uint32(v4) & mask
	last := net32 | ^mask // broadcast address
	host := binary.BigEndian.Uint32(v4)

	detail := a.Iface + " " + a.CIDR
	var out []candidate
	for _, want := range []uint32{net32 + 1, net32 + 254} {
		if want >= last || want == host {
			continue
		}
		out = append(out, candidate{
			IP:     net.IPv4(byte(want>>24), byte(want>>16), byte(want>>8), byte(want)).String(),
			Source: sourceSubnet,
			Detail: detail,
		})
	}
	return out
}

// isVirtualIface reports whether an interface is a container/loopback/tunnel
// device that cannot be the operator's LAN. Derived from there would be
// container bridges and mesh tunnels belonging to other software.
func isVirtualIface(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, exact := range []string{"lo", "sit0", "gre0", "ip6tnl0", "dummy0", "mon0"} {
		if n == exact {
			return true
		}
	}
	for _, prefix := range []string{
		"docker", "veth", "virbr", "vnet", "br-", "tun", "tap", "wg",
		"fips", "wt", "tailscale", "zt", "ppp", "lxc", "vmnet",
	} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// collectHostNetwork reads the host state hostCandidates needs.
func collectHostNetwork() hostNetwork {
	return hostNetwork{
		Gateways: defaultGateways(),
		Addrs:    localIPv4Addrs(),
		Common:   commonIPs,
		ARP:      readARPTable(),
	}
}

// localIPv4Addrs returns every IPv4 address the host holds, with interface and
// CIDR, including loopback and virtual interfaces (callers filter).
func localIPv4Addrs() []localAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []localAddr
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			out = append(out, localAddr{Iface: ifc.Name, CIDR: ipnet.String()})
		}
	}
	return out
}

// defaultGateways returns every IPv4 default-route gateway the host has, in
// routing-table order, whatever the platform.
func defaultGateways() []gatewayRoute {
	switch runtime.GOOS {
	case "windows":
		if out, err := exec.Command("route", "print", "-4").Output(); err == nil {
			return parseWindowsRouteDefault(string(out))
		}
	case "darwin", "freebsd", "openbsd", "netbsd", "dragonfly":
		if out, err := exec.Command("netstat", "-rn", "-f", "inet").Output(); err == nil {
			return parseNetstatDefault(string(out))
		}
	default:
		// `ip -4 route show default` is the modern Linux source; a box without
		// iproute2 (busybox, a minimal container) still has netstat.
		if out, err := exec.Command("ip", "-4", "route", "show", "default").Output(); err == nil {
			return parseIPRouteDefault(string(out))
		}
		if out, err := exec.Command("netstat", "-rn", "-f", "inet").Output(); err == nil {
			return parseNetstatDefault(string(out))
		}
	}
	return nil
}

// parseIPRouteDefault parses `ip -4 route show default` (Linux). Only routes
// with an IPv4 gateway are usable: `default dev ppp0 scope link` has none, and a
// link-local IPv6 gateway (`via fe80::1`) says nothing about an IPv4 router.
func parseIPRouteDefault(out string) []gatewayRoute {
	var res []gatewayRoute
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "default" && fields[0] != "0.0.0.0/0") {
			continue
		}
		gw, iface := "", ""
		for i, f := range fields {
			switch f {
			case "via":
				if i+1 < len(fields) {
					gw = fields[i+1]
				}
			case "dev":
				if i+1 < len(fields) {
					iface = fields[i+1]
				}
			}
		}
		ip := net.ParseIP(gw)
		if ip == nil || ip.To4() == nil {
			continue
		}
		res = append(res, gatewayRoute{IP: gw, Iface: iface})
	}
	return res
}

// parseNetstatDefault parses the IPv4 routing table of `netstat -rn -f inet`
// (macOS/BSD) and of Linux netstat. A resolved "link#5" BSD gateway is not an
// address and is skipped, and header lines are not mistaken for routes.
func parseNetstatDefault(out string) []gatewayRoute {
	var res []gatewayRoute
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		gw, iface := "", ""
		switch {
		case fields[0] == "default":
			// BSD: Destination Gateway Flags [Expire] Netif
			gw = fields[1]
			if len(fields) >= 4 {
				iface = fields[3]
			}
		case fields[0] == "0.0.0.0" || fields[0] == "0.0.0.0/0":
			// Linux netstat: Destination Gateway Genmask Flags ... Iface
			gw = fields[1]
			iface = fields[len(fields)-1]
		default:
			continue
		}
		ip := net.ParseIP(gw)
		if ip == nil || ip.To4() == nil {
			continue
		}
		res = append(res, gatewayRoute{IP: gw, Iface: iface})
	}
	return res
}

// parseWindowsRouteDefault parses `route print -4`'s Active Routes table: the
// all-zero destination AND mask identify a default route, and "On-link" (a
// directly attached network) is not a gateway address.
func parseWindowsRouteDefault(out string) []gatewayRoute {
	var res []gatewayRoute
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "0.0.0.0" || fields[1] != "0.0.0.0" {
			continue
		}
		if fields[2] == "On-link" {
			continue
		}
		ip := net.ParseIP(fields[2])
		if ip == nil || ip.To4() == nil {
			continue
		}
		res = append(res, gatewayRoute{IP: fields[2], Iface: fields[3]})
	}
	return res
}

// ─── discovery seams ──────────────────────────────────────────

// The seams below exist so the scan can be driven end to end — classification,
// ordering, probes, diagnostics — with a frozen host state and frozen probe
// answers, without touching the network. Production never reassigns them.
var (
	collectHostNetworkFn     = collectHostNetwork
	collectInterfaceStatesFn = collectInterfaceStates
	probeCandidateFn         = probeRouter
)

// probeRouterWithPasswordFn is the identification entry point, a variable so a
// test can drive /api/identify without dialling a real router.
var probeRouterWithPasswordFn = probeRouterWithPassword

// scanNetworkFn is the scan entry point used by /api/scan; a variable so the
// handler's wire contract can be tested without probing the network.
var scanNetworkFn = scanNetwork

// ─── scan ─────────────────────────────────────────────────────

// ScanResult is everything one scan produced: the devices found, every address
// that was probed (with its provenance), and — when nothing was found — the
// diagnostic block explaining why.
type ScanResult struct {
	Routers     []RouterInfo
	Probes      []candidateProbe
	Diagnostics *ScanDiagnostics
}

// discoverRouters scans for routers and keeps the historical contract: found
// devices, identified ones first.
func discoverRouters() []RouterInfo { return scanNetwork().Routers }

// scanNetwork probes every candidate, classifies what answered, and builds the
// failure block when nothing did.
//
// The candidates are fanned out (see probeCandidates), so the scan costs the
// slowest single probe rather than the sum of every dead address: the measured
// 25-29 s serial scan becomes a bounded few seconds even on a host with no
// router attached. Candidate ORDER is still meaningful for the results list
// (route-derived evidence first), and is preserved by indexing the fan-out.
func scanNetwork() ScanResult {
	n := collectHostNetworkFn()
	cands := hostCandidates(n)

	arpMAC := make(map[string]string, len(n.ARP))
	for _, e := range n.ARP {
		if e.MAC != "" {
			arpMAC[e.IP] = e.MAC
		}
	}

	infos := probeCandidates(cands)

	found := []RouterInfo{}
	probes := []candidateProbe{}
	for i, c := range cands {
		info := infos[i]
		info.IP = c.IP
		if info.MAC == "" {
			info.MAC = arpMAC[c.IP]
		}
		info.Source = c.Source
		info.Identified, info.Note = classifyProbed(info)
		info.Name = friendlyRouterName(info)
		probes = append(probes, candidateProbe{
			IP:         c.IP,
			Source:     c.Source,
			Detail:     c.Detail,
			Demoted:    c.Demoted,
			SSH:        info.SSH,
			HTTPPort:   info.HTTPPort,
			Identified: info.Identified,
			Note:       info.Note,
		})
		if info.SSH || info.HTTPPort > 0 {
			found = append(found, info)
		}
	}
	sortIdentifiedFirst(found)

	res := ScanResult{Routers: found, Probes: probes}
	if len(found) == 0 {
		d := buildScanDiagnostics(collectInterfaceStatesFn(), n.Gateways, probes)
		res.Diagnostics = &d
	}
	return res
}

// probeCandidates probes every candidate concurrently, bounded by scanWorkers,
// and returns the answers in candidate order (so the caller's priority order is
// preserved even though the probes complete out of order).
func probeCandidates(cands []candidate) []RouterInfo {
	out := make([]RouterInfo, len(cands))
	if len(cands) == 0 {
		return out
	}
	sem := make(chan struct{}, scanWorkers)
	var wg sync.WaitGroup
	for i := range cands {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = probeCandidateFn(cands[i].IP)
		}(i)
	}
	wg.Wait()
	return out
}

// classifyProbed decides what a probed address actually is, and returns the
// label the operator sees. Only a device that identified itself (SSH gave us a
// firmware string) is a router; a machine that merely answers :22 or :80 is an
// unverified host and must not be offered as a deploy target.
func classifyProbed(info RouterInfo) (identified bool, note string) {
	vendor := strings.TrimSpace(info.Vendor)
	if vendor != "" && vendor != "unknown" {
		if vendor == "OpenWrt" {
			return true, "TollGate/OpenWrt router (identified)"
		}
		return true, vendor + " router (identified)"
	}
	var answers []string
	if info.SSH {
		answers = append(answers, ":"+strconv.Itoa(sshProbePort))
	}
	if info.HTTPPort > 0 {
		answers = append(answers, ":"+strconv.Itoa(info.HTTPPort))
	}
	if len(answers) == 0 {
		return false, "did not answer"
	}
	return false, "unverified host on your LAN — answers " + strings.Join(answers, " and ")
}

// sortIdentifiedFirst orders identified devices before unverified hosts,
// keeping the candidate priority order inside each group (stable sort).
func sortIdentifiedFirst(list []RouterInfo) {
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Identified && !list[j].Identified
	})
}

// ─── actionable failure ───────────────────────────────────────

// ifaceState is one interface as the host reports it: enough to tell "no link"
// (carrier 0, cable in the wrong port) from "link up, NO IPv4" (the router is
// not handing out DHCP).
type ifaceState struct {
	Name    string
	Up      bool
	Carrier string // "1", "0", or "?" when the platform does not say
	IPv4    []string
	Virtual bool
}

// ifaceReport is the rendered form of one interface in the failure block.
type ifaceReport struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Carrier string   `json:"carrier"`
	IPv4    []string `json:"ipv4,omitempty"`
	Problem string   `json:"problem,omitempty"`
	Virtual bool     `json:"virtual,omitempty"`
}

// candidateProbe is what one probed address answered, with its provenance.
type candidateProbe struct {
	IP         string `json:"ip"`
	Source     string `json:"source"`
	Detail     string `json:"detail,omitempty"`
	Demoted    bool   `json:"demoted,omitempty"`
	SSH        bool   `json:"ssh"`
	HTTPPort   int    `json:"httpPort,omitempty"`
	Identified bool   `json:"identified"`
	Note       string `json:"note,omitempty"`
}

// ScanDiagnostics is the failure block: interface state, the addresses that were
// probed and what each answered, and what to do next, in order.
type ScanDiagnostics struct {
	Summary     string           `json:"summary"`
	Interfaces  []ifaceReport    `json:"interfaces"`
	Gateways    []string         `json:"gateways"`
	Probes      []candidateProbe `json:"probes"`
	Sources     []string         `json:"sources"`
	Remediation []string         `json:"remediation"`
	ManualHint  string           `json:"manualHint"`
	Text        string           `json:"text"`
}

// collectInterfaceStates returns every non-loopback interface with its state,
// carrier and IPv4 addresses, in the order the kernel reports them.
func collectInterfaceStates() []ifaceState {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []ifaceState
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		st := ifaceState{
			Name:    ifc.Name,
			Up:      ifc.Flags&net.FlagUp != 0,
			Carrier: readCarrier(ifc.Name),
			Virtual: isVirtualIface(ifc.Name),
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			// Still report the interface: an interface whose addresses cannot
			// be read is precisely what the operator needs to see.
			out = append(out, st)
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			st.IPv4 = append(st.IPv4, ipnet.String())
		}
		out = append(out, st)
	}
	return out
}

// readCarrier reads the Linux carrier flag for an interface ("1"/"0"); on a
// platform that does not expose it the answer is "?" rather than a guess.
func readCarrier(name string) string {
	b, err := os.ReadFile("/sys/class/net/" + name + "/carrier")
	if err != nil {
		return "?"
	}
	s := strings.TrimSpace(string(b))
	if s == "1" || s == "0" {
		return s
	}
	return "?"
}

// interfaceProblem names the condition on one interface, or "" when the
// interface looks usable.
func interfaceProblem(s ifaceState) string {
	if s.Carrier == "0" {
		return "no link (carrier=0): nothing is attached to this port"
	}
	if !s.Up {
		return "administratively down: the interface is disabled"
	}
	if len(s.IPv4) == 0 {
		return "link up, NO IPv4: no DHCP lease and no static address"
	}
	return ""
}

// buildScanDiagnostics assembles the failure block. Pure: everything it reports
// is passed in, so the block is table-testable without a network.
func buildScanDiagnostics(ifaces []ifaceState, gateways []gatewayRoute, probes []candidateProbe) ScanDiagnostics {
	d := ScanDiagnostics{
		Summary: fmt.Sprintf("%d address(es) were probed and answered nothing that identifies as a TollGate/OpenWrt router.",
			len(probes)),
		Probes: probes,
	}
	for _, s := range ifaces {
		state := "down"
		if s.Up {
			state = "up"
		}
		d.Interfaces = append(d.Interfaces, ifaceReport{
			Name:    s.Name,
			State:   state,
			Carrier: s.Carrier,
			IPv4:    s.IPv4,
			Problem: interfaceProblem(s),
			Virtual: s.Virtual,
		})
	}
	for _, g := range gateways {
		d.Gateways = append(d.Gateways, g.IP)
	}
	d.Sources = sourceSummary(probes)
	d.Remediation = remediationSteps(gateways)
	d.ManualHint = "Enter your router's address in the box above — the wizard identifies it directly, bypassing discovery entirely."
	d.Text = formatScanFailure(d)
	return d
}

// sourceSummary counts how many probed addresses each evidence class
// contributed, in priority order, so the operator can read the block like the
// list of sources the wizard actually consulted.
func sourceSummary(probes []candidateProbe) []string {
	var out []string
	for _, class := range []string{sourceGateway, sourceSubnet, sourceCommon, sourceARP, sourceManual} {
		n := 0
		for _, p := range probes {
			if p.Source == class {
				n++
			}
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%s: %d address(es)", class, n))
		}
	}
	return out
}

// remediationSteps is the ordered list of things to try, ending with the
// headless form that always works. A default route owned by a tunnel interface
// is called out in addition to the list, because that is a case where the
// operator's laptop is not on the LAN at all.
func remediationSteps(gateways []gatewayRoute) []string {
	steps := []string{
		"1. Use the LAN port: on the GL-MT3000 the 2.5 GbE port is WAN and the 1 GbE port is LAN — plugged into WAN you get no DHCP lease and no route.",
		"2. Give it time: after a flash or a reboot a router needs 60-120 s before it answers.",
		"3. Force a fresh lease on the wired interface: dhclient -v <interface>.",
		"4. Vanilla OpenWrt answers on :22 only — there is no LuCI web UI on :80 or :8080 until it is installed.",
		"5. Or skip discovery entirely: bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER-IP> '' <you@wallet.app>",
	}
	for _, g := range gateways {
		if isVirtualIface(g.Iface) {
			steps = append(steps, fmt.Sprintf(
				"Note: the default route is owned by the tunnel interface %s (via %s) — the router on your LAN is only reachable on its own subnet.",
				g.Iface, g.IP))
			break
		}
	}
	return steps
}

// formatScanFailure renders the failure block as copy-pasteable plain text, for
// the UI, the deploy log and the headless path. Pure. It names every interface
// (with the condition the operator cannot see), every address that was probed
// and what it answered, and what to do next — the one-sentence message it
// replaces named nothing.
func formatScanFailure(d ScanDiagnostics) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Router scan: %s\n", d.Summary)

	b.WriteString("\nInterfaces:\n")
	for _, r := range d.Interfaces {
		line := fmt.Sprintf("  %-18s %-4s carrier=%s", r.Name, r.State, r.Carrier)
		if len(r.IPv4) > 0 {
			line += " " + strings.Join(r.IPv4, " ")
		}
		if r.Virtual {
			line += " [virtual: not scanned]"
		}
		if r.Problem != "" {
			line += " — " + r.Problem
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\nDefault gateway(s) from the routing table:\n")
	if len(d.Gateways) == 0 {
		b.WriteString("  (none — this host has no default route, so it is not on any LAN)\n")
	}
	for _, g := range d.Gateways {
		fmt.Fprintf(&b, "  %s\n", g)
	}

	fmt.Fprintf(&b, "\nAddresses probed: %d\n", len(d.Probes))
	for _, p := range d.Probes {
		line := fmt.Sprintf("  %-15s [%s]", p.IP, p.Source)
		if p.Detail != "" {
			line += " " + p.Detail
		}
		line += fmt.Sprintf("  ssh=%s  http=%s", yesNo(p.SSH), portOrNone(p.HTTPPort))
		if p.Demoted {
			line += "  (not on your local subnet: routed via the default gateway)"
		}
		if p.Note != "" {
			line += " — " + p.Note
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\nCandidate sources:\n")
	for _, s := range d.Sources {
		fmt.Fprintf(&b, "  %s\n", s)
	}

	b.WriteString("\nWhat to try, in this order:\n")
	for _, s := range d.Remediation {
		fmt.Fprintf(&b, "  %s\n", s)
	}

	if d.ManualHint != "" {
		fmt.Fprintf(&b, "\n%s\n", d.ManualHint)
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func portOrNone(p int) string {
	if p <= 0 {
		return "none"
	}
	return strconv.Itoa(p)
}

// ─── neighbour table ─────────────────────────────────────────

type arpEntry struct {
	IP    string
	MAC   string
	Iface string
}

func readARPTable() []arpEntry {
	out, err := exec.Command("arp", "-a").Output()
	if err != nil {
		// Try ip neigh as fallback
		out, err = exec.Command("ip", "neigh").Output()
		if err != nil {
			return nil
		}
	}
	return parseARPTable(string(out))
}

// ARP line classification. The three shapes parsed here are:
//
//	BSD/macOS  arp -a :  ? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 ifscope [ethernet]
//	Linux      arp -a :  gateway (192.168.1.1) at e8:8f:6f:df:9e:11 [ether] on eth0
//	Linux    ip neigh :  192.168.1.1 dev eth0 lladdr e8:8f:6f:df:9e:11 REACHABLE
var (
	// IPv4 in the parenthesised column both BSD and net-tools print.
	arpParenIPv4Re = regexp.MustCompile(`\((\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\)`)
	// Any dotted quad on the line (covers `ip neigh`, which prints the
	// address as a bare first field).
	arpIPv4Re = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
	// A hardware address field. Groups are 1-2 hex digits because BSD `arp`
	// prints each octet with %x — `0a:1b:...` comes out as `a:1b:...` and
	// multicast addresses look like `1:0:5e:0:0:fb`. Anchored, so only a
	// whole field qualifies.
	arpMACRe = regexp.MustCompile(`^([0-9a-fA-F]{1,2}[:-]){5}[0-9a-fA-F]{1,2}$`)
)

// parseARPTable extracts IP/MAC/interface triples from `arp -a` (BSD/macOS and
// net-tools) or `ip neigh` output. Split out of readARPTable so the parser is
// testable without shelling out.
//
// The MAC is taken positionally (the field after `lladdr`, else after `at`)
// rather than by scanning the whole line, so unroutable entries
// (`at (incomplete)`, `<incomplete>`, `FAILED`) and IPv6-only neighbours are
// dropped instead of being paired with an unrelated address. The interface
// (`dev <if>` / `on <if>`) travels with the entry because the candidate filter
// drops neighbours that belong to container or tunnel interfaces; an entry whose
// interface cannot be determined keeps iface "" and is treated as a real LAN
// neighbour.
func parseARPTable(out string) []arpEntry {
	var entries []arpEntry
	for _, line := range strings.Split(out, "\n") {
		mac := tokenAfter(line, "lladdr")
		if mac == "" {
			mac = tokenAfter(line, "at")
		}
		if !arpMACRe.MatchString(mac) {
			continue
		}
		ip := ""
		if m := arpParenIPv4Re.FindStringSubmatch(line); m != nil {
			ip = m[1]
		} else if m := arpIPv4Re.FindString(line); m != "" {
			ip = m
		}
		if ip == "" {
			continue
		}
		iface := tokenAfter(line, "dev")
		if iface == "" {
			iface = tokenAfter(line, "on")
		}
		entries = append(entries, arpEntry{IP: ip, MAC: mac, Iface: iface})
	}
	return entries
}

// tokenAfter returns the whitespace-delimited field following the first
// case-insensitive occurrence of kw, or "" when kw is absent or trailing.
func tokenAfter(line, kw string) string {
	fields := strings.Fields(line)
	for i, f := range fields {
		if strings.EqualFold(f, kw) && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// probeRouter probes ip with passwordless SSH (the LAN scan path).
func probeRouter(ip string) RouterInfo { return probeRouterWithPasswordFn(ip, "") }

// probeRouterWithPassword probes ip, using password when non-empty so a
// password-protected router can still be identified (the wizard calls this
// after the operator enters the root password, via /api/identify). The
// friendly Name is computed here; scanNetwork recomputes it once the MAC has
// been enriched and the device classified.
//
// Every port is probed CONCURRENTLY with probeTimeout: on a LAN a host either
// answers immediately or not at all, so the four probes must cost the slowest
// one (~800 ms) and not their sum (which was 5 s per dead candidate and is where
// the measured 25-29 s scan went).
func probeRouterWithPassword(ip, password string) RouterInfo {
	info := RouterInfo{IP: ip, Vendor: "unknown", Model: "unknown", Firmware: "unknown"}

	ports := []int{sshProbePort, 80, 443, 8080}
	open := make([]bool, len(ports))
	var wg sync.WaitGroup
	for i, port := range ports {
		wg.Add(1)
		go func(i, port int) {
			defer wg.Done()
			open[i] = tcpProbeFn(ip, port, probeTimeout)
		}(i, port)
	}
	wg.Wait()

	info.SSH = open[0]
	// HTTPPort reports the most operator-meaningful port that answered, in the
	// same preference order the serial scan used (80, then 443, then 8080).
	for i, port := range []int{80, 443, 8080} {
		if open[i+1] {
			info.HTTPPort = port
			break
		}
	}

	// Try SSH-based identification (password when supplied, else passwordless)
	if info.SSH {
		if fw, vendor, model := sshIdentify(ip, password); fw != "" {
			info.Firmware = fw
			info.Vendor = vendor
			info.Model = model
		} else if refusal := lastHostKeyRefusal(ip); refusal != "" {
			// The connect was refused: say so instead of rendering a router whose
			// firmware is simply "unknown". The fingerprint travels as its own
			// field so the UI can act on it.
			info.SSHRefusal = refusal
			info.SSHFingerprint = lastHostKeyFingerprint(ip)
		}
	}

	info.Name = friendlyRouterName(info)
	return info
}

func tcpProbe(ip string, port int, timeout time.Duration) bool {
	// net.JoinHostPort correctly brackets IPv6 addresses per RFC 3986.
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// parseGLInetRelease parses the contents of /etc/gl-inet-release from stock
// GL.iNet firmware. The format is NOT guaranteed to be stable: it may be
// key=value lines (model=..., version=...) OR a single bare product string.
// Handle both defensively. Returns the model and version (version may be
// empty if the file carries no version line).
func parseGLInetRelease(glOut string) (model, version string) {
	lines := strings.Split(glOut, "\n")

	// First pass: extract model/version from key=value lines.
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "model") || strings.Contains(low, "product") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) > 1 {
				model = strings.Trim(parts[1], " \t\"'")
			}
		}
		if strings.Contains(low, "version") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) > 1 {
				version = strings.Trim(parts[1], " \t\"'")
			}
		}
	}

	// If no model was found via key=value, fall back to a bare product string:
	// the first non-empty line that is not itself a key=value line. This covers
	// the single-product-string format (e.g. "GL-MT3000") and mixed formats.
	if model == "" {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "=") {
				continue
			}
			model = line
			break
		}
	}
	return model, version
}

// sshIdentify tries passwordless SSH to read firmware info. A connect that never
// happened (no client) returns empty values; the reason is recorded against ip by
// the connect helper (see hostkey.go), so the caller can surface a host-key
// refusal with lastHostKeyRefusal/sshConnectFailureMessage instead of losing it.
func sshIdentify(ip, password string) (firmware, vendor, model string) {
	client := sshConnect(ip, password)
	if client == nil {
		return
	}
	defer client.Close()

	out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null")
	if strings.Contains(out, "OpenWrt") || strings.Contains(out, "openwrt") {
		vendor = "OpenWrt"
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "DISTRIB_DESCRIPTION") {
				parts := strings.SplitN(line, "'", 2)
				if len(parts) > 1 {
					firmware = strings.Trim(parts[1], "'")
				}
			}
		}
		if firmware == "" {
			firmware = "OpenWrt"
		}
	}

	// If not OpenWrt, check for stock GL.iNet firmware. The /etc/gl-inet-release
	// format is NOT verified against a real device (no GL.iNet reachable from
	// this network at implementation time) — the parser is defensive and handles
	// both key=value lines and a bare product string.
	if vendor == "" {
		glOut := sshRun(client, "cat /etc/gl-inet-release 2>/dev/null || echo ''")
		glOut = strings.TrimSpace(glOut)
		if glOut != "" {
			vendor = "GL.iNet"
			firmware = "stock"
			glModel, glVersion := parseGLInetRelease(glOut)
			if glModel != "" {
				model = glModel
			}
			if glVersion != "" {
				firmware = "GL.iNet " + glVersion
			}
			// Normalize: lowercase, spaces -> dashes (e.g. "GL-MT3000" -> "gl-mt3000").
			model = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(model), " ", "-"))
		}
	}

	// Try to get model from OpenWrt sysinfo (only if not already identified
	// from a GL.iNet release file, which takes precedence).
	if model == "" {
		modelOut := sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null || cat /tmp/sysinfo/model 2>/dev/null")
		modelOut = strings.TrimSpace(modelOut)
		if modelOut != "" {
			model = modelOut
		}
	}

	return
}

// prettyModel turns an OpenWrt board_name / GL release model into a display
// name: "glinet,gl-mt3000" -> "GL-MT3000", "gl-mt3000" -> "GL-MT3000"; other
// strings are returned unchanged.
func prettyModel(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, ","); i >= 0 {
		model = strings.TrimSpace(model[i+1:])
	}
	if model == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(model), "GL-") {
		return strings.ToUpper(model)
	}
	return model
}

// friendlyRouterName builds a human label for a discovered device: the
// prettified model when known, else the vendor, else an OUI-derived vendor,
// else "Router". Never returns the raw "unknown"/"unknown unknown" pair.
func friendlyRouterName(info RouterInfo) string {
	model := strings.TrimSpace(info.Model)
	vendor := strings.TrimSpace(info.Vendor)
	if model != "" && model != "unknown" {
		if p := prettyModel(model); p != "" {
			return p
		}
	}
	if vendor != "" && vendor != "unknown" {
		return vendor + " router"
	}
	if v := ouiVendor(info.MAC); v != "" {
		return v + " device"
	}
	return "Router"
}

// ouiVendors is a small best-effort MAC-prefix table used only to make the
// dropdown friendlier when SSH identification is unavailable. A miss (or a
// wrong guess) only affects the display label — never routing or deploy.
var ouiVendors = map[string]string{
	"94:83:C4": "GL.iNet",
	"E4:95:6E": "GL.iNet",
	"52:54:00": "QEMU",
	"00:1C:42": "Parallels",
}

// ouiVendor returns the vendor for a MAC's OUI (first three octets), or "".
func ouiVendor(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	if len(mac) < 8 {
		return ""
	}
	return ouiVendors[mac[:8]]
}
