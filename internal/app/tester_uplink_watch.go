package app

import (
	"net"
	"os/exec"
	"strings"
	"time"
)

// The tester's OWN uplink, watched across a deploy (card t_27e348b5).
//
// A deploy that runs from a machine on WIFI can lose its own radio association
// while the router is perfectly healthy: the deploy reconfigures the router's
// wireless mid-run (step 5 STA, step 7 branding, step 10 service restart), the
// tester's AP disappears or its SSID rotates, and the tester's own stack drops
// off the LAN. The SSH session dies with it.
//
// Before this file, every such loss went through failTransportLost, whose text
// is about the ROUTER ("find it on its new LAN address, try the OTHER ethernet
// port") and whose verdict is "failed" — so a completed install was reported as
// a failed one, and the operator was pointed at hardware that was never at
// fault. This file supplies the missing half: read the tester's uplink BEFORE
// and AFTER, decide whether the evidence blames the tester or the router, and —
// when it blames the tester — end the job NON-fatally with the rejoin action.

// testerUplinkState is one point-in-time read of the machine running the
// installer. CIDR is the first IPv4 address ON Iface, because that is what
// changes when a laptop roams between SSIDs even though the interface name
// stays the same (live: wlp59s0 moved from 192.168.1.0/24 to 10.2.124.0/16
// mid-session, same iface, different subnet).
type testerUplinkState struct {
	Kind  string
	Iface string
	CIDR  string
}

// readTesterUplink reads the tester's current uplink. It never guesses: an
// unreadable host degrades to the zero state (Kind "unknown", no iface), and
// every caller treats that as "no evidence" rather than inventing a verdict.
func readTesterUplink() testerUplinkState {
	kind, ifname := testerUplinkKind()
	st := testerUplinkState{Kind: kind, Iface: ifname}
	if ifname == "" {
		return st
	}
	for _, a := range localIPv4Addrs() {
		if a.Iface == ifname {
			st.CIDR = a.CIDR
			break
		}
	}
	return st
}

// testerUplinkNow is the seam the deploy path calls. A var so a test can drive
// the before/after comparison without touching this machine's interfaces.
var testerUplinkNow = readTesterUplink

// routerDialableFn answers "can this machine still open a TCP connection to the
// router's SSH port?". It is the second half of the evidence: a dead session
// plus an unreachable router is consistent with EITHER side failing, so this
// probe alone never decides — the tester's own uplink state does.
var routerDialableFn = func(ip string) bool {
	if strings.TrimSpace(ip) == "" {
		return false
	}
	return tcpProbe(ip, 22, 5*time.Second)
}

// The three verdicts a lost transport can earn.
const (
	// transportLossTesterSide means the evidence points at THIS machine's own
	// connectivity: it was on wifi when the deploy started, and that path is
	// now gone or on a different subnet. The router's state is UNKNOWN, so the
	// deploy must not report an install failure.
	transportLossTesterSide = "tester-side"
	// transportLossRouterSide means the tester's own uplink is intact (same
	// interface, same subnet, and the router still answers on :22) while the
	// session died: the router was re-addressed, rebooted, or vanished.
	transportLossRouterSide = "router-side"
	// transportLossUnknown means the evidence does not decide. The deploy keeps
	// today's failure verdict but appends the tester-side hint.
	transportLossUnknown = "unknown"
)

// transportLossInput is the evidence a lost transport is judged on.
type transportLossInput struct {
	Before         testerUplinkState
	After          testerUplinkState
	RouterIP       string
	RouterDialable bool
}

// classifyTransportLoss decides who to blame for a dead SSH session, from the
// tester's uplink before and after plus a fresh reachability probe.
//
// The asymmetry is deliberate. A "tester-side" verdict converts a hard failure
// into a non-fatal pending state, so it demands POSITIVE evidence that this
// machine's own path changed — it is never inferred from the router being
// unreachable, which is equally consistent with a dead router. Conversely a
// wired tester is never blamed for its own link here: the whole story is a
// radio that the deploy reconfigured, and the wifi notice is required to stay
// silent on ethernet (TestTesterUplinkNoticeSilentWhenNotWifi).
func classifyTransportLoss(in transportLossInput) string {
	if in.Before.Kind != uplinkKindWifi {
		return transportLossRouterSide
	}
	// The router answers on :22 from here, so this machine's path is alive and
	// the death is the router's (a killed session, a reboot, a re-address).
	if in.RouterDialable {
		return transportLossRouterSide
	}
	// The interface the deploy was using is gone entirely.
	if in.After.Iface == "" {
		return transportLossTesterSide
	}
	// Same machine, different path: the radio moved to another interface.
	if in.After.Iface != in.Before.Iface {
		return transportLossTesterSide
	}
	// The interface lost its address: the association dropped.
	if in.Before.CIDR != "" && in.After.CIDR == "" {
		return transportLossTesterSide
	}
	// The interface keeps its name but sits on a different subnet: this is the
	// live roam (192.168.1.0/24 -> 10.2.124.0/16 on the same wlp59s0).
	if in.Before.CIDR != "" && in.After.CIDR != "" && !sameSubnet(in.Before.CIDR, in.After.CIDR) {
		return transportLossTesterSide
	}
	// Still attached to the same subnet the router was on, and the router does
	// not answer: that is the router's problem, not the tester's link.
	if in.After.CIDR != "" && ipInCIDR(in.RouterIP, in.After.CIDR) {
		return transportLossRouterSide
	}
	// Enough to rule the tester out, not enough to rule the router in.
	return transportLossUnknown
}

// sameSubnet reports whether two CIDR addresses describe the same network.
func sameSubnet(a, b string) bool {
	_, an, errA := net.ParseCIDR(strings.TrimSpace(a))
	_, bn, errB := net.ParseCIDR(strings.TrimSpace(b))
	if errA != nil || errB != nil || an == nil || bn == nil {
		return false
	}
	return an.String() == bn.String()
}

// ipInCIDR reports whether ip falls inside cidr.
func ipInCIDR(ip, cidr string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if parsed == nil || err != nil || network == nil {
		return false
	}
	return network.Contains(parsed)
}

// testerSideHeading is the amber pending view's title when the deploy stopped
// because THIS machine lost its own connection. It states the two facts the
// operator needs (their link dropped; the install is not being called a
// failure) and it never says the install failed.
func testerSideHeading() string {
	return "Your wifi dropped — this is not an install failure"
}

// testerSideNoteInput is everything the recovery note needs. Kept as a struct
// so the note is a pure function of facts, and its wording is pinned by a test.
type testerSideNoteInput struct {
	RouterIP     string
	Before       testerUplinkState
	After        testerUplinkState
	CaptiveSSID  string
	PrivateSSID  string
	RejoinTried  bool
	RejoinDetail string

	// InstallRan is false when the deploy died before it changed anything on the
	// router (step 0). The note must then not offer a Re-check for an install
	// that never started.
	InstallRan bool
}

// testerSideRecoveryNote is the operator-facing message for a deploy that lost
// the TESTER's own uplink. It must be actionable without any router access:
// name what changed on this machine, name the SSIDs to rejoin, and say what to
// do next. It must never claim the install failed.
func testerSideRecoveryNote(in testerSideNoteInput) string {
	var b strings.Builder
	b.WriteString("The installer stopped because THIS machine lost its network connection to the router — not because the install reported a problem. It could not reach the router to verify the result.\n\n")
	b.WriteString("What changed on this machine:\n")
	b.WriteString("- uplink when the deploy started: " + describeUplinkState(in.Before) + "\n")
	b.WriteString("- uplink when the connection was lost: " + describeUplinkState(in.After) + "\n")
	if strings.TrimSpace(in.RouterIP) != "" {
		b.WriteString("- router address the deploy was using: " + strings.TrimSpace(in.RouterIP) + "\n")
	}
	b.WriteString("\nA deploy reconfigures the router's wireless mid-run, so a machine joined over wifi can be dropped by the very step that is meant to succeed.\n\n")

	// Requirement (3): reconnect if credentials allow, otherwise say plainly
	// which SSID to join.
	if in.RejoinTried {
		b.WriteString("Reconnect attempt: " + strings.TrimSpace(in.RejoinDetail) + "\n\n")
	} else if strings.TrimSpace(in.RejoinDetail) != "" {
		b.WriteString("Reconnect: " + strings.TrimSpace(in.RejoinDetail) + "\n\n")
	}

	b.WriteString("What to do:\n")
	if ssids := joinSSIDs(in.PrivateSSID, in.CaptiveSSID); ssids != "" {
		b.WriteString("- Join the router's wifi: " + ssids + ". The private SSID's password is TollGate unless you chose your own; the open SSID needs no password.\n")
	} else {
		b.WriteString("- Rejoin the router's wifi (the deploy may have changed its SSIDs — the open one starts with !TollGate- and the private one is <nym>-<code>).\n")
	}
	if in.InstallRan {
		b.WriteString("- Then press Re-check: it re-probes the router without re-running the deploy.\n")
	} else {
		b.WriteString("- Then re-run the wizard: the deploy stopped before it changed anything on the router.\n")
	}
	b.WriteString("- To avoid this entirely, run the wizard from a machine on an ethernet cable.\n\n")
	if in.InstallRan {
		b.WriteString("The install is NOT being reported as failed: the installer lost the tester's own link and could not verify the router.")
	} else {
		b.WriteString("This is not an install failure: nothing was installed, and the installer lost the tester's own link before it could start.")
	}
	return b.String()
}

// describeUplinkState renders one point-in-time uplink read for the note.
func describeUplinkState(st testerUplinkState) string {
	kind := st.Kind
	if kind == "" {
		kind = uplinkKindUnknown
	}
	where := st.Iface
	if where == "" {
		where = "no interface"
	}
	if st.CIDR != "" {
		return kind + " via " + where + " (" + st.CIDR + ")"
	}
	return kind + " via " + where
}

// joinSSIDs renders the SSIDs a tester can rejoin, most useful first. It skips
// empty names and never repeats one, so the sentence cannot read "join X or X".
func joinSSIDs(private, captive string) string {
	seen := map[string]bool{}
	var parts []string
	for _, s := range []string{private, captive} {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	return strings.Join(parts, " or ")
}

// rejoinSavedWiFi re-associates this machine to ssid using ONLY an existing
// saved network profile, and never passes a passphrase on a command line (that
// would put the secret in `ps` for every local user). It reports whether a
// reconnect was actually attempted and what happened, so the note can be
// truthful about it either way.
//
// It is best-effort by design: no network manager, or no saved profile, is a
// normal outcome and becomes a plain instruction to the operator.
func rejoinSavedWiFi(ssid string) (attempted bool, detail string) {
	ssid = strings.TrimSpace(ssid)
	if ssid == "" {
		return false, "no router SSID is known yet, so nothing could be rejoined automatically."
	}
	if p, err := exec.LookPath("nmcli"); err == nil {
		if saved, ok := nmcliHasProfile(p, ssid); ok {
			out, runErr := exec.Command(p, "--wait", "20", "connection", "up", "id", saved).CombinedOutput()
			if runErr != nil {
				return true, "tried NetworkManager's saved profile \"" + saved + "\" but it did not come up (" + truncate(strings.TrimSpace(string(out)), 120) + ")."
			}
			return true, "rejoined the saved NetworkManager profile \"" + saved + "\"."
		}
		return false, "this machine has no saved NetworkManager profile for " + ssid + ", so it was not rejoined automatically."
	}
	if p, err := exec.LookPath("wpa_cli"); err == nil {
		if id, ok := wpaCliNetworkID(p, ssid); ok {
			exec.Command(p, "select_network", id).Run()
			out, runErr := exec.Command(p, "reassociate").CombinedOutput()
			if runErr != nil {
				return true, "tried wpa_supplicant's saved network " + id + " (" + ssid + ") but it did not reassociate (" + truncate(strings.TrimSpace(string(out)), 120) + ")."
			}
			return true, "reassociated wpa_supplicant to its saved network " + id + " (" + ssid + ")."
		}
		return false, "this machine has no saved wpa_supplicant network for " + ssid + ", so it was not rejoined automatically."
	}
	return false, "no NetworkManager or wpa_supplicant control tool was found, so the wifi was not rejoined automatically."
}

// nmcliHasProfile reports whether NetworkManager already has a connection
// profile whose NAME is exactly ssid. Only a saved profile can be brought up
// without a passphrase, which is what "if credentials allow" means here.
func nmcliHasProfile(nmcli, ssid string) (string, bool) {
	out, err := exec.Command(nmcli, "-t", "-f", "NAME", "connection", "show").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if name == ssid {
			return name, true
		}
	}
	return "", false
}

// wpaCliNetworkID finds an existing wpa_supplicant network block by SSID.
// `list_networks` prints: id / ssid / bssid / flags.
func wpaCliNetworkID(wpaCli, ssid string) (string, bool) {
	out, err := exec.Command(wpaCli, "list_networks").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		if strings.TrimSpace(f[1]) == ssid {
			return strings.TrimSpace(f[0]), true
		}
	}
	return "", false
}

// testerWiFiRejoinFn is the seam the deploy calls, so the decision to rejoin
// and the note it produces can be tested without a radio.
var testerWiFiRejoinFn = rejoinSavedWiFi
