package app

import (
	"runtime"
	"strings"
	"testing"
)

// These tests pin the tester-side uplink diagnosis for card t_27e348b5: the
// wizard must not blame the router when the only thing that changed is the
// TESTER's own wifi association.

func TestUplinkKindByPrefix(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"wlp59s0", uplinkKindWifi},
		{"wlan0", uplinkKindWifi},
		{"wlp3s0", uplinkKindWifi},
		{"ath0", uplinkKindWifi},
		{"ra0", uplinkKindWifi},
		{"enx00e04c36043e", uplinkKindEthernet},
		{"eth0", uplinkKindEthernet},
		{"eno1", uplinkKindEthernet},
		{"usb0", uplinkKindEthernet},
		{"", uplinkKindUnknown},
		{"zzz0", uplinkKindUnknown},
	}
	for _, c := range cases {
		if got := uplinkKind(c.name); got != c.want {
			t.Errorf("uplinkKind(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// A WIRED tester must get the ordinary router-side message: the notice exists
// only for the wifi case, and emitting it on ethernet would send an operator
// chasing their own radio for a router fault.
func TestTesterUplinkNoticeSilentWhenNotWifi(t *testing.T) {
	for _, kind := range []string{uplinkKindEthernet, uplinkKindUnknown, ""} {
		if got := testerUplinkNotice(kind, "eth0"); got != "" {
			t.Errorf("testerUplinkNotice(%q, eth0) = %q, want empty", kind, got)
		}
	}
}

// The wifi notice must name the tester's OWN connection and an action, and it
// must never claim the install failed (that is the whole defect).
func TestTesterUplinkNoticeNamesTesterAndAction(t *testing.T) {
	got := testerUplinkNotice(uplinkKindWifi, "wlp59s0")
	if got == "" {
		t.Fatal("testerUplinkNotice(wifi, wlp59s0) is empty, want a notice")
	}
	for _, want := range []string{"wlp59s0", "YOUR association", "rejoin the router's wifi", "ethernet"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "installation failed") {
		t.Errorf("notice must not report an install failure:\n%s", got)
	}
	if !strings.Contains(got, "instead of failing") {
		t.Errorf("notice must say the deploy may not have failed:\n%s", got)
	}
}

// The notice must degrade gracefully when the interface name is unavailable.
func TestTesterUplinkNoticeWithoutIfaceName(t *testing.T) {
	got := testerUplinkNotice(uplinkKindWifi, "")
	if got == "" || strings.Contains(got, "()") {
		t.Errorf("testerUplinkNotice(wifi, \"\") = %q, want a notice with no empty parens", got)
	}
}

// The classification must prefer the kernel's answers over the name: a name
// that looks like a LAN uplink but is down, virtual, point-to-point or loopback
// is NOT a plausible uplink, while an unrecognised-but-real interface still is
// (reported unknown).
func TestClassifyHostInterfaceEvidence(t *testing.T) {
	cases := []struct {
		name                  string
		isUp, loop, virt, p2p bool
		wantKind              string
		wantPlausible         bool
	}{
		{"wlp59s0", true, false, false, false, uplinkKindWifi, true},
		{"wlp59s0", false, false, false, false, uplinkKindUnknown, false},
		{"wlp59s0", true, true, false, false, uplinkKindUnknown, false},
		{"wlp59s0", true, false, true, false, uplinkKindUnknown, false},
		{"wlp59s0", true, false, false, true, uplinkKindUnknown, false},
		{"fips0", true, false, true, false, uplinkKindUnknown, false},
		{"enx00e04c36043e", true, false, false, false, uplinkKindEthernet, true},
		{"zzz0", true, false, false, false, uplinkKindUnknown, true},
	}
	for _, c := range cases {
		kind, plausible := classifyHostInterface(c.name, c.isUp, c.loop, c.virt, c.p2p)
		if kind != c.wantKind || plausible != c.wantPlausible {
			t.Errorf("classifyHostInterface(%q, up=%v, lo=%v, virt=%v, p2p=%v) = (%q,%v), want (%q,%v)",
				c.name, c.isUp, c.loop, c.virt, c.p2p, kind, plausible, c.wantKind, c.wantPlausible)
		}
	}
}

// macOS names its wifi en0 while the name alone says ethernet. Without this the
// Apple-laptop case would be misdiagnosed as a wired tester.
func TestClassifyDarwinWifiIsNotEthernet(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only naming rule")
	}
	kind, ok := classifyHostInterface("en0", true, false, false, false)
	if !ok || kind != uplinkKindWifi {
		t.Fatalf("classifyHostInterface(en0) = (%q,%v), want (wifi,true)", kind, ok)
	}
}

// ifaceHoldsAddr must not report a match for an interface that holds nothing:
// the default-route interface is only a usable answer when it has an address.
func TestIfaceHoldsAddr(t *testing.T) {
	addrs := []localAddr{
		{Iface: "lo", CIDR: "127.0.0.1/8"},
		{Iface: "wlp59s0", CIDR: "10.2.124.120/16"},
	}
	if !ifaceHoldsAddr(addrs, "wlp59s0") {
		t.Error("ifaceHoldsAddr(addrs, wlp59s0) = false, want true")
	}
	if ifaceHoldsAddr(addrs, "enx00e04c36043e") {
		t.Error("ifaceHoldsAddr(addrs, enx…) = true, want false")
	}
}

// testerUplinkKind must never return a virtual interface, and on a host with
// only loopback it must answer unknown rather than guessing. This runs against
// the real machine state deliberately: it is the guard that keeps the notice
// from firing on a wrong interface.
func TestTesterUplinkKindLiveStateIsConsistent(t *testing.T) {
	kind, ifname := testerUplinkKind()
	if ifname != "" && isVirtualIface(ifname) {
		t.Fatalf("testerUplinkKind returned a virtual interface %q", ifname)
	}
	if kind == uplinkKindUnknown && ifname != "" {
		t.Fatalf("testerUplinkKind returned unknown with interface %q", ifname)
	}
	if kind == uplinkKindWifi && runtime.GOOS == "darwin" && ifname == "en0" {
		t.Log("darwin wifi classification")
	}
}
