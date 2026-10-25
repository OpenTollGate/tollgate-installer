package app

import (
	"net"
	"runtime"
	"strings"
)

// Uplink kinds for the machine RUNNING the installer. This is the tester's own
// uplink, not the router's: the wizard drives the router over the LAN the
// tester is attached to, so this is the path the deploy session lives on.
const (
	uplinkKindWifi     = "wifi"
	uplinkKindEthernet = "ethernet"
	uplinkKindUnknown  = "unknown"
)

// uplinkKind classifies an interface name without touching the kernel. Used as
// the first pass by classifyHostInterface; on darwin the kernel flags win over
// the name (en0 is wifi on every Apple laptop, but the name says ethernet).
func uplinkKind(ifname string) string {
	n := strings.ToLower(strings.TrimSpace(ifname))
	if n == "" {
		return uplinkKindUnknown
	}
	for _, p := range []string{"wl", "ath", "ra", "wifi"} {
		if strings.HasPrefix(n, p) {
			return uplinkKindWifi
		}
	}
	for _, p := range []string{"en", "eth", "usb", "lan"} {
		if strings.HasPrefix(n, p) {
			return uplinkKindEthernet
		}
	}
	return uplinkKindUnknown
}

// classifyHostInterface classifies ONE interface, preferring the kernel's own
// answers over the name. It takes its evidence as arguments rather than reading
// the live interface, so the classification is testable without a network stack.
//
// plausible=false means "not a candidate uplink at all" (loopback, virtual,
// down, or point-to-point) — the caller must skip it rather than report unknown.
func classifyHostInterface(name string, isUp, isLoopback, isVirtual, isPointToPoint bool) (kind string, plausible bool) {
	if isLoopback || isVirtual || !isUp || isPointToPoint {
		return uplinkKindUnknown, false
	}
	if runtime.GOOS == "darwin" && strings.EqualFold(strings.TrimSpace(name), "en0") {
		return uplinkKindWifi, true
	}
	return uplinkKind(name), true
}

// testerUplinkKind reports the kind of uplink this installer process is most
// likely using to reach the router, plus the interface it settled on.
//
// It prefers the default-route interface (that is the path the router is
// reached through), then any addressed, non-virtual interface. Returns unknown
// when nothing qualifies — callers must then say NOTHING rather than guess, since
// a wrong "you are on wifi" message is worse than silence.
func testerUplinkKind() (kind, ifname string) {
	ifaces := hostInterfaces()
	addrs := localIPv4Addrs()

	// The default-route interface first, when it holds an address.
	for _, g := range defaultGateways() {
		if g.Iface == "" || isVirtualIface(g.Iface) || !ifaceHoldsAddr(addrs, g.Iface) {
			continue
		}
		if k, ok := classifyIface(ifaces, g.Iface); ok {
			return k, g.Iface
		}
	}
	for _, a := range addrs {
		if a.Iface == "" || isVirtualIface(a.Iface) {
			continue
		}
		if ip, _, err := net.ParseCIDR(a.CIDR); err != nil || ip.IsLoopback() {
			continue
		}
		if k, ok := classifyIface(ifaces, a.Iface); ok {
			return k, a.Iface
		}
	}
	return uplinkKindUnknown, ""
}

// ifaceFacts is the per-interface evidence classifyIface needs. Kept as data so
// the interface read is one seam (hostInterfaces) and the classification is
// pure.
type ifaceFacts struct {
	Name         string
	Up           bool
	Loopback     bool
	PointToPoint bool
}

// hostInterfaces reads the local interface facts. Real failures return nil, and
// every caller degrades to "unknown" rather than inventing a verdict.
func hostInterfaces() []ifaceFacts {
	list, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]ifaceFacts, 0, len(list))
	for _, ifc := range list {
		out = append(out, ifaceFacts{
			Name:         ifc.Name,
			Up:           ifc.Flags&net.FlagUp != 0,
			Loopback:     ifc.Flags&net.FlagLoopback != 0,
			PointToPoint: ifc.Flags&net.FlagPointToPoint != 0,
		})
	}
	return out
}

// classifyIface finds ifname in the facts and classifies it; a name the kernel
// did not report is classified by name, treated as up.
func classifyIface(facts []ifaceFacts, ifname string) (string, bool) {
	for _, f := range facts {
		if f.Name != ifname {
			continue
		}
		return classifyHostInterface(f.Name, f.Up, f.Loopback, isVirtualIface(f.Name), f.PointToPoint)
	}
	return classifyHostInterface(ifname, true, false, isVirtualIface(ifname), false)
}

// ifaceHoldsAddr reports whether any local address belongs to ifname.
func ifaceHoldsAddr(addrs []localAddr, ifname string) bool {
	for _, a := range addrs {
		if a.Iface == ifname {
			return true
		}
	}
	return false
}

// testerUplinkNotice returns the line the wizard should show when a failure may
// be the tester's OWN disconnection, or "" when the uplink is wired (or unknown)
// and the ordinary router-side message is correct.
//
// The rule the card states: never emit "installation failed" when the only
// evidence is the tester's own disconnection. A wifi tester must be given the
// likelier cause and the exact next action instead.
func testerUplinkNotice(kind, ifname string) string {
	if kind != uplinkKindWifi {
		return ""
	}
	where := "your wifi"
	if ifname != "" {
		where = "your wifi (" + ifname + ")"
	}
	return "This machine reaches the router over " + where + ", and the deploy changes the router's wireless " +
		"configuration mid-run. If the run stopped while the radios were being reconfigured, the deploy may have " +
		"dropped YOUR association instead of failing: rejoin the router's wifi and open the wizard again — the " +
		"install is usually complete. To avoid it entirely, run the wizard from a machine on an ethernet cable."
}
