package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	}, "noderunner-private")

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

	id := fallbackDeviceIdentity("noderunner-private")
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

func TestDeviceIdentityScriptAdoptsConfiguredPrefix(t *testing.T) {
	prefixFile := filepath.Join(t.TempDir(), "ssid-prefixes")
	if err := os.WriteFile(prefixFile, []byte("# custom skins\nCustomSkin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, statePath, _ := uciStubEnv(t, []string{
		"system.@system[0].hostname=CustomSkin-ABCD",
		"wireless.private_radio0.ssid=noderunner-private-ABCD",
	})
	env = append(env,
		"TOLLGATE_SSID_PREFIXES_FILE="+prefixFile,
		"PRIVATE_NYM=noderunner-private",
	)

	id, out := runIdentityScript(t, env, statePath)
	if id.Code != "ABCD" {
		t.Fatalf("code = %q, want ABCD (resolver output:\n%s)", id.Code, out)
	}
	if id.Source != "hostname" {
		t.Errorf("source = %q, want hostname", id.Source)
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

func TestAcceptedMintsPayloadReplacesTheList(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not installed")
	}
	accepted := []mintCfg{
		{URL: "https://mint-a.example", PricePerStep: 21, PriceUnit: "sat", PurchaseMinSteps: 1},
		{URL: "https://mint-b.example", PricePerStep: 21, PriceUnit: "sat", PurchaseMinSteps: 1},
	}
	payload, _ := json.Marshal(accepted)
	price := 21
	filter := configJqReplaceFilter() +
		" | .metric=\"milliseconds\" | .step_size=$ss" +
		" | .accepted_mints |= map(.price_per_step=$pp)"
	args := []string{
		"--argjson", "m", "5",
		"--argjson", "of", "0.9000",
		"--argjson", "df", "0.1000",
		"--argjson", "ss", "60000",
		"--argjson", "dm", string(payload),
		"--argjson", "pp", strconv.Itoa(price),
		"--arg", "mu", "",
		filter,
	}
	config := `{"accepted_mints":[{"url":"https://old.example"}],"profit_share":[{"identity":"owner","factor":0},{"identity":"developer","factor":0}]}`
	cmd := exec.Command(jq, args...)
	cmd.Stdin = strings.NewReader(config)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jq failed: %v", err)
	}
	var doc struct {
		AcceptedMints []mintCfg `json:"accepted_mints"`
		StepSize      int       `json:"step_size"`
		Metric        string    `json:"metric"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("merged config is not JSON: %v\n%s", err, out)
	}
	if len(doc.AcceptedMints) != 2 {
		t.Fatalf("accepted_mints = %d, want 2 (replace, not merge): %s", len(doc.AcceptedMints), out)
	}
	if doc.AcceptedMints[0].URL != "https://mint-a.example" {
		t.Errorf("first mint = %q", doc.AcceptedMints[0].URL)
	}
	for _, m := range doc.AcceptedMints {
		if m.PricePerStep != 21 {
			t.Errorf("mint %s price = %d, want 21", m.URL, m.PricePerStep)
		}
	}
	if doc.StepSize != 60_000 || doc.Metric != "milliseconds" {
		t.Errorf("step_size/metric = %d/%q", doc.StepSize, doc.Metric)
	}
}

func TestEffectivePrivateSSID(t *testing.T) {
	withPrivateNym(t, "node-default")
	if got := effectivePrivateSSID("noderunner-private"); got != "noderunner-private" {
		t.Errorf("request prefix = %q, want noderunner-private", got)
	}
	if got := effectivePrivateSSID("bad prefix"); got != "node-default" {
		t.Errorf("invalid prefix = %q, want the process default", got)
	}
	if got := effectivePrivateSSID(""); got != "node-default" {
		t.Errorf("empty prefix = %q, want the process default", got)
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
