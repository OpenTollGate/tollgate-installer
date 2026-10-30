package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// withBrand applies a brand for one test and restores the process-wide value.
func withBrand(t *testing.T, brand string) {
	t.Helper()
	prev := brandEnvValue
	SetBrandToken(brand)
	t.Cleanup(func() {
		brandEnvValue = prev
		brandTokenHint = prev
	})
}

// withPrivateNym applies a private-SSID prefix for one test.
func withPrivateNym(t *testing.T, nym string) {
	t.Helper()
	prev := privateSSIDPrefix
	setPrivateSSIDPrefix(nym)
	t.Cleanup(func() { privateSSIDPrefix = prev })
}

func TestApplyStartupInputsFlagWinsOverEnv(t *testing.T) {
	withBrand(t, "")
	withPrivateNym(t, deviceIdentityDefaultNym)

	applyStartupInputs("archipelago", "ignored-brand", "noderunner-private", "ignored-nym")

	if got := brandTokenFromEnv(); got != "archipelago" {
		t.Errorf("brand = %q, want archipelago", got)
	}
	if got := privateSSIDPrefix; got != "noderunner-private" {
		t.Errorf("private nym = %q, want noderunner-private", got)
	}
}

func TestApplyStartupInputsFallsBackToEnv(t *testing.T) {
	withBrand(t, "")
	withPrivateNym(t, deviceIdentityDefaultNym)

	applyStartupInputs("", "archipelago", "", "noderunner-private")

	if got := brandTokenFromEnv(); got != "archipelago" {
		t.Errorf("brand = %q, want archipelago", got)
	}
	if got := privateSSIDPrefix; got != "noderunner-private" {
		t.Errorf("private nym = %q, want noderunner-private", got)
	}
}

func TestUnsafePrivateNymIsIgnored(t *testing.T) {
	withPrivateNym(t, "safe-prefix")
	setPrivateSSIDPrefix("bad prefix; rm -rf /")
	if got := privateSSIDPrefix; got != "safe-prefix" {
		t.Errorf("unsafe prefix changed the value: %q", got)
	}
}

func TestApplyBrandToIdentityArchipelago(t *testing.T) {
	withBrand(t, "archipelago")
	withPrivateNym(t, "noderunner-private")

	id := applyBrandToIdentity(deviceIdentity{
		Code:        "ABCD",
		Hostname:    "tollgate-ABCD",
		SSID:        "!TollGate-ABCD",
		PrivateSSID: "c08r4d0r-ABCD",
	})

	if id.Hostname != "archipelago-ABCD" {
		t.Errorf("hostname = %q, want archipelago-ABCD", id.Hostname)
	}
	if id.SSID != "!Archipelago-ABCD" {
		t.Errorf("captive SSID = %q, want !Archipelago-ABCD", id.SSID)
	}
	if id.Nym != "noderunner-private" {
		t.Errorf("nym = %q, want noderunner-private", id.Nym)
	}
	if id.PrivateSSID != "noderunner-private-ABCD" {
		t.Errorf("private SSID = %q, want noderunner-private-ABCD", id.PrivateSSID)
	}
}

func TestFallbackIdentityIsBranded(t *testing.T) {
	withBrand(t, "archipelago")
	withPrivateNym(t, "noderunner-private")

	id := fallbackDeviceIdentity()
	if !strings.HasPrefix(id.SSID, "!Archipelago-") {
		t.Errorf("fallback SSID = %q, want !Archipelago-*", id.SSID)
	}
	if !strings.HasPrefix(id.Hostname, "archipelago-") {
		t.Errorf("fallback hostname = %q, want archipelago-*", id.Hostname)
	}
	if !strings.HasPrefix(id.PrivateSSID, "noderunner-private-") {
		t.Errorf("fallback private SSID = %q, want noderunner-private-*", id.PrivateSSID)
	}
}

func TestDeviceIdentityScriptAdoptsArchipelagoNames(t *testing.T) {
	env, statePath, _ := uciStubEnv(t, []string{
		"system.@system[0].hostname=Archipelago-ABCD",
		"wireless.private_radio0.ssid=noderunner-private-ABCD",
	})
	env = append(env, "BRAND_TOKEN=archipelago", "PRIVATE_NYM=noderunner-private")

	id, out := runIdentityScript(t, env, statePath)
	if id.Code != "ABCD" {
		t.Fatalf("code = %q (resolver output:\n%s)", id.Code, out)
	}
	if id.Source != "hostname" {
		t.Errorf("code source = %q, want hostname", id.Source)
	}
	if id.Nym != "noderunner-private" {
		t.Errorf("nym = %q, want noderunner-private", id.Nym)
	}
	if id.PrivateSSID != "noderunner-private-ABCD" {
		t.Errorf("private SSID = %q, want noderunner-private-ABCD", id.PrivateSSID)
	}
}

func TestProductionMintsExcludeMinibitsAndLnserver(t *testing.T) {
	got := mintURLs(t, defaultMintsJSON(false))
	if len(got) != 5 {
		t.Fatalf("production mints = %d, want 5: %v", len(got), got)
	}
	for _, u := range got {
		if strings.Contains(u, "minibits") || strings.Contains(u, "lnserver") {
			t.Errorf("production mints still contain %q", u)
		}
	}
}

func TestPkgVersionNewer(t *testing.T) {
	cases := []struct {
		installed, wanted string
		want              bool
	}{
		{"0.6.1", "0.6.0", true},
		{"0.6.0", "0.6.1", false},
		{"0.6.0-r2", "0.6.0-r1", false},  // packaging counter only
		{"0.6.0_rc6-r1", "0.6.0", false}, // pre-release is older
		{"0.6.0", "0.6.0_rc6", true},     // final > same-core rc
		{"0.7.0-dev", "0.6.0", true},
		{"v0.6.0", "0.6.0", false},
		{"not-a-version", "0.6.0", false},
	}
	for _, tc := range cases {
		if got := pkgVersionNewer(tc.installed, tc.wanted); got != tc.want {
			t.Errorf("pkgVersionNewer(%q, %q) = %v, want %v", tc.installed, tc.wanted, got, tc.want)
		}
	}
}

func TestTestnutMintsAreTheVerifiedPair(t *testing.T) {
	got := mintURLs(t, defaultMintsJSON(true))
	joined := strings.Join(got, " ")
	for _, want := range []string{"testnut.cashu.space", "testnut.cashu.exchange"} {
		if !strings.Contains(joined, want) {
			t.Errorf("test mint set missing %s: %v", want, got)
		}
	}
	if strings.Contains(joined, "nofee.testnut.cashu.space") {
		t.Errorf("dead nofee testnut is still present: %v", got)
	}
}

func TestMintConfigUsesTheSchemaKey(t *testing.T) {
	raw := defaultMintsJSON(true)
	if strings.Contains(raw, "min_purchase_steps") {
		t.Errorf("mint config still uses min_purchase_steps:\n%s", raw)
	}
	if !strings.Contains(raw, "purchase_min_steps") {
		t.Errorf("mint config does not use purchase_min_steps:\n%s", raw)
	}
	var mints []mintCfg
	if err := json.Unmarshal([]byte(raw), &mints); err != nil {
		t.Fatalf("mint config is not valid JSON: %v", err)
	}
	for _, m := range mints {
		if m.PurchaseMinSteps < 1 {
			t.Errorf("mint %s: purchase_min_steps = %d, want >= 1", m.URL, m.PurchaseMinSteps)
		}
	}
}
