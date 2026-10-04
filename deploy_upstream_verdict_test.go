package main

import (
	"strings"
	"testing"
)

// The installer's upstream gate was reporting `default-route=true` on routers
// that had none: it trusted `ip route show default`, which is NOT filtered by
// OpenWrt's busybox ip — it returns the whole route table, so any router with
// any route looks like it has a default route. This is the same trap the
// READ-ONLY diagnostic script hit; it pinged a "gateway" literally named
// "br-lan".
func TestDefaultRouteDetectionIgnoresUnfilteredBusyboxOutput(t *testing.T) {
	withDefault := "default via 10.10.24.1 dev phy1-sta0 src 10.10.24.145\n" +
		"10.10.24.0/21 dev phy1-sta0 scope link  src 10.10.24.145\n" +
		"192.168.1.0/24 dev br-lan scope link  src 192.168.1.1"
	if !defaultRoutePresent(withDefault) {
		t.Errorf("a real default route was not detected:\n%s", withDefault)
	}
	withoutDefault := "192.168.1.0/24 dev br-lan scope link  src 192.168.1.1"
	if defaultRoutePresent(withoutDefault) {
		t.Errorf("LAN-only route table reported as having a default route:\n%s", withoutDefault)
	}
	if defaultRoutePresent("") {
		t.Error("empty route table reported as having a default route")
	}
	// A default route that is not the first line must still be found.
	if !defaultRoutePresent(withoutDefault + "\ndefault via 192.168.2.1 dev eth0") {
		t.Error("default route on a later line was missed")
	}
}

// A REFUSED or empty answer must never count as resolution.
func TestDNSAnswerOKRejectsRefusalsAndEmptiness(t *testing.T) {
	ok := "Name:    github.com\nAddress: 140.82.121.4"
	if !dnsAnswerOK(ok) {
		t.Error("a real answer was rejected")
	}
	bad := []string{
		"** server can't find github.com: REFUSED",
		"** server can't find github.com: NXDOMAIN",
		";; connection timed out; no servers could be reached",
		"",
		"Address: 127.0.0.1", // dnsmasq answering for itself is not resolution
	}
	for _, b := range bad {
		if dnsAnswerOK(b) {
			t.Errorf("counted as resolution: %q", b)
		}
	}
}

// The message must name which side owns the failure: router, the network it
// joined, or a fixable resolver misconfiguration.
func TestUpstreamVerdictNamesTheOwner(t *testing.T) {
	cases := []struct {
		name                                  string
		pingOK, dnsOK, defaultRoute, publicOK bool
		want                                  []string
	}{
		{"no uplink at all", false, false, false, false,
			[]string{"no default route", "not on an upstream"}},
		{"associated but first hop dead", false, false, true, false,
			[]string{"does not answer", "not DNS"}},
		{"healthy", true, true, true, true,
			[]string{"usable"}},
		{"dead handed-out resolvers, public works", true, false, true, true,
			[]string{"does not serve", "fixable"}},
		{"network blocks all DNS", true, false, true, false,
			[]string{"blocks DNS", "nothing to fix on the router"}},
	}
	for _, c := range cases {
		got := upstreamVerdict(c.pingOK, c.dnsOK, c.defaultRoute, c.publicOK)
		if !strings.HasPrefix(got, "verdict: ") {
			t.Errorf("%s: verdict must be labelled, got %q", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: verdict %q does not contain %q", c.name, got, w)
			}
		}
	}
}
