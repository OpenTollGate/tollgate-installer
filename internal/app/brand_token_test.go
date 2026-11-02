package app

import (
	"strings"
	"testing"
)

// The installer mints every branded name from a LITERAL today:
//
//	deploy.go:780  SSID:        "!TollGate-" + code
//	deploy.go:779  Hostname:    "tollgate-" + code
//	deploy.go:77   uci -q set nodogsplash.@nodogsplash[0].gatewaydomainname='tollgate.lan'
//
// and it reads no brand input at all (its whole env surface is GITHUB_TOKEN, PATH and
// TOLLGATE_CREDENTIAL_FILE). So a white-label deploy is impossible: the wrapper can
// export whatever it likes and the router still comes out branded TollGate. That is
// the defect these tests pin.
//
// Two things are asserted, and they are deliberately separable:
//
//	(A) DE-BRAND IS UNCHANGED. With no brand supplied, every derived name must stay
//	    byte-identical to today. A branded feature that silently changes what an
//	    unbranded router does breaks upstream users to serve ours.
//	(B) A SUPPLIED BRAND IS USED. The token must reach the hostname, the captive SSID
//	    and the display spelling, and must be validated before use.

// (A) No brand => today's exact bytes. This is the regression guard.
func TestDeviceIdentityWithoutABrandIsUnchanged(t *testing.T) {
	id := brandedDeviceIdentity("OQ3Q", "")
	if id.Hostname != "tollgate-OQ3Q" {
		t.Errorf("hostname changed for the unbranded case: got %q, want %q", id.Hostname, "tollgate-OQ3Q")
	}
	if id.SSID != "!TollGate-OQ3Q" {
		t.Errorf("captive SSID changed for the unbranded case: got %q, want %q", id.SSID, "!TollGate-OQ3Q")
	}
}

// (A) The default must also hold when the brand is the default's own name.
func TestDeviceIdentityWithTheDefaultBrandMatchesTheUnbrandedCase(t *testing.T) {
	bare := brandedDeviceIdentity("OQ3Q", "")
	def := brandedDeviceIdentity("OQ3Q", "tollgate")
	if bare != def {
		t.Errorf("brand 'tollgate' must be byte-identical to no brand:\n bare=%+v\n def =%+v", bare, def)
	}
}

// (B) A supplied brand reaches the hostname (lowercased) and the SSID (display case).
func TestDeviceIdentityUsesASuppliedBrand(t *testing.T) {
	id := brandedDeviceIdentity("OQ3Q", "net4sats")
	if id.Hostname != "net4sats-OQ3Q" {
		t.Errorf("hostname did not take the brand: got %q, want %q", id.Hostname, "net4sats-OQ3Q")
	}
	// The captive SSID keeps the display spelling and the leading '!' so guest SSIDs
	// still sort first in a scan list (the pinned #706 contract).
	if id.SSID != "!Net4sats-OQ3Q" {
		t.Errorf("captive SSID did not take the brand: got %q, want %q", id.SSID, "!Net4sats-OQ3Q")
	}
}

// (B) The brand is an input from outside, so it must be validated before it becomes a
// hostname or an SSID. One alphanumeric token only, exactly like the module's
// load_brand() coercion — an invalid token falls back to the default rather than
// producing a broken hostname.
func TestBrandTokenValidation(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"net4sats", "net4sats", "plain lowercase token"},
		{"Net4sats", "net4sats", "lowercased for a hostname"},
		{"net4sats9", "net4sats9", "digits are allowed"},
		{"", "", "empty stays empty (means: no brand)"},
		{"bad token", "", "whitespace is not a brand"},
		{"bad-token", "", "punctuation is not a brand"},
		{"bad_token", "", "underscore is not a brand"},
		{"../etc/passwd", "", "a path is not a brand"},
		{"net4sats;rm -rf /", "", "shell metacharacters are not a brand"},
		{"nët4sats", "", "non-ASCII is not a brand"},
	}
	for _, c := range cases {
		if got := sanitizeBrandToken(c.in); got != c.want {
			t.Errorf("sanitizeBrandToken(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

// (B) The token must be written to the router at the PRE-PACKAGE step, so the module's
// one-shot load_brand() reads it when the package installs. Writing it only in the
// branding step (7) is inert — that is why this is a separate command list.
func TestBrandTokenWriterIsEmittedBeforePackaging(t *testing.T) {
	cmds := brandTokenCommands("net4sats")
	joined := strings.Join(cmds, "\n")
	if joined == "" {
		t.Fatal("no brand-token writer emitted; the module would fall back to the default")
	}
	if !strings.Contains(joined, "/etc/tollgate/brand") {
		t.Errorf("the writer does not mention the brand file:\n%s", joined)
	}
	if !strings.Contains(joined, "net4sats") {
		t.Errorf("the writer does not carry the token:\n%s", joined)
	}
	// It must be a single token write, not a name composer: no SSID, no hostname.
	for _, forbidden := range []string{"hostname", "ssid", "gatewayname"} {
		if strings.Contains(strings.ToLower(joined), forbidden) {
			t.Errorf("the brand-token writer composes %q — it must only supply the token:\n%s", forbidden, joined)
		}
	}
}

// (A) No brand => no writer at all. An absent brand must leave no record behind, so an
// unbranded router stays byte-identical and a later upgrade can still introduce one.
func TestNoBrandTokenWriterWithoutABrand(t *testing.T) {
	if cmds := brandTokenCommands(""); len(cmds) != 0 {
		t.Errorf("emitted a brand writer with no brand: %v", cmds)
	}
	if cmds := brandTokenCommands("tollgate"); len(cmds) != 0 {
		t.Errorf("emitted a brand writer for the default brand: %v", cmds)
	}
}

func TestOperatorTokenDefaultsAndBrandInput(t *testing.T) {
	prev := deviceIdentityDefaultOperator
	t.Cleanup(func() { deviceIdentityDefaultOperator = prev })

	id := brandedDeviceIdentity("OQ3Q", "")
	if id.PrivateSSID != "Private-c08r4d0r-OQ3Q" {
		t.Errorf("default private SSID = %q, want Private-c08r4d0r-OQ3Q", id.PrivateSSID)
	}

	SetOperatorToken("Archipelago")
	id = brandedDeviceIdentity("OQ3Q", "")
	if id.PrivateSSID != "Private-Archipelago-OQ3Q" {
		t.Errorf("brand-input private SSID = %q, want Private-Archipelago-OQ3Q", id.PrivateSSID)
	}
}

func TestOperatorTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"Archipelago", "Archipelago"},
		{"operator_2", "operator_2"},
		{"bad token", ""},
		{"bad/token", ""},
		{"'", ""},
		{"", ""},
	} {
		if got := sanitizeOperatorToken(tc.in); got != tc.want {
			t.Errorf("sanitizeOperatorToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
