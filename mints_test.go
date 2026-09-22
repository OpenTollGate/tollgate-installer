package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// wantProdMintURLs is the fixed production mint set that every real customer
// deployment must ship — and the ONLY set when test_mints is off.
var wantProdMintURLs = []string{
	"https://mint.coinos.io",
	"https://mint.minibits.cash/Bitcoin",
	"https://mint.lnserver.com",
	"https://mint.macadamia.cash",
	"https://mint.westernbtc.com",
	"https://kashu.me",
	"https://mint.cubabitcoin.org",
}

func mintURLs(t *testing.T, raw string) []string {
	t.Helper()
	var mints []mintCfg
	if err := json.Unmarshal([]byte(raw), &mints); err != nil {
		t.Fatalf("default mints payload is not valid JSON: %v\n%s", err, raw)
	}
	urls := make([]string, 0, len(mints))
	for _, m := range mints {
		urls = append(urls, m.URL)
	}
	return urls
}

// TestDeployPayloadTestMintsDefaultOff is the core opt-in guarantee: a deploy
// payload WITHOUT the test_mints field (or with it false) must produce a mint
// config containing EXACTLY the 7 production mints and ZERO testnut entries.
func TestDeployPayloadTestMintsDefaultOff(t *testing.T) {
	payloads := []string{
		// field absent entirely (legacy UI payloads)
		`{"ip":"192.168.1.1","password":"pw","lnurl":"test@wallet.app"}`,
		// field explicitly false
		`{"ip":"192.168.1.1","password":"pw","lnurl":"test@wallet.app","test_mints":false}`,
		// field false alongside other advanced fields
		`{"ip":"192.168.1.1","password":"pw","lnurl":"test@wallet.app","test_mints":false,"mint":"https://mint.example.com","devSplit":10,"margin":5}`,
	}
	for i, p := range payloads {
		var req deployRequest
		if err := json.Unmarshal([]byte(p), &req); err != nil {
			t.Fatalf("payload %d: decode: %v", i, err)
		}
		if req.TestMints {
			t.Errorf("payload %d: TestMints = true, want false (opt-in flag must default to false)", i)
		}
		got := mintURLs(t, defaultMintsJSON(req.TestMints))
		if len(got) != 7 {
			t.Errorf("payload %d: default mints count = %d, want exactly 7 (got %v)", i, len(got), got)
		}
		for _, u := range got {
			if strings.Contains(u, "testnut") {
				t.Errorf("payload %d: testnut mint %q present with test_mints off — testnut must be opt-in only", i, u)
			}
		}
		if !reflect.DeepEqual(got, wantProdMintURLs) {
			t.Errorf("payload %d: mints = %v, want %v", i, got, wantProdMintURLs)
		}
	}
}

// TestDeployPayloadTestMintsOptIn: test_mints=true appends BOTH testnut
// entries after the 7 production mints (9 total, order preserved).
func TestDeployPayloadTestMintsOptIn(t *testing.T) {
	var req deployRequest
	if err := json.Unmarshal([]byte(`{"ip":"192.168.1.1","password":"pw","lnurl":"test@wallet.app","test_mints":true}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.TestMints {
		t.Fatal("TestMints = false, want true when payload sets test_mints:true")
	}
	got := mintURLs(t, defaultMintsJSON(req.TestMints))
	if len(got) != 9 {
		t.Fatalf("mints count = %d, want 9 (7 production + 2 testnut); got %v", len(got), got)
	}
	want := append(append([]string{}, wantProdMintURLs...),
		"https://nofee.testnut.cashu.space",
		"https://testnut.cashu.space")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mints = %v, want %v", got, want)
	}
	testnut := 0
	for _, u := range got {
		if strings.Contains(u, "testnut.cashu.space") {
			testnut++
		}
	}
	if testnut != 2 {
		t.Errorf("testnut mint count = %d, want both testnut entries", testnut)
	}
}

// TestDefaultMintsJSONFieldValues pins the per-mint numeric config so the
// testnut entries stay zero-fee/no-payout and production mints keep their
// top-up thresholds.
func TestDefaultMintsJSONFieldValues(t *testing.T) {
	for _, include := range []bool{false, true} {
		var mints []mintCfg
		if err := json.Unmarshal([]byte(defaultMintsJSON(include)), &mints); err != nil {
			t.Fatalf("include=%v: %v", include, err)
		}
		for _, m := range mints {
			if m.PriceUnit != "sats" || m.PricePerStep != 1 || m.MinPurchaseSteps != 0 {
				t.Errorf("mint %s: unexpected pricing fields %+v", m.URL, m)
			}
			if strings.Contains(m.URL, "testnut") {
				if m.MinBalance != 0 || m.PayoutIntervalSeconds != 999999 || m.MinPayoutAmount != 999999 || m.BalanceTolerancePercent != 0 {
					t.Errorf("testnut mint %s: must stay zero-balance/no-payout, got %+v", m.URL, m)
				}
			} else {
				if m.MinBalance != 64 || m.BalanceTolerancePercent != 10 || m.PayoutIntervalSeconds != 60 || m.MinPayoutAmount != 128 {
					t.Errorf("production mint %s: unexpected thresholds, got %+v", m.URL, m)
				}
			}
		}
	}
}

// TestMintConfigJqMerge runs the EXACT jq filter that deploy step 7 builds
// against sample config.json states (via local jq when available) to prove:
//   - the merge actually works — the old filter errored with "Cannot iterate
//     over null" and never wrote mints at all;
//   - a config missing accepted_mints is handled;
//   - merging is idempotent (re-running adds nothing);
//   - an empty custom mint does NOT append a {"url":""} entry;
//   - a custom mint matching a default URL is not duplicated;
//   - margin/profit_share owner+developer factors land correctly.
func TestMintConfigJqMerge(t *testing.T) {
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not installed — skipping end-to-end jq filter test")
	}
	extractURLs := func(cfgJSON string) []string {
		t.Helper()
		var cfg struct {
			AcceptedMints []mintCfg `json:"accepted_mints"`
		}
		if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
			t.Fatalf("merged config not valid JSON: %v\n%s", err, cfgJSON)
		}
		urls := make([]string, 0, len(cfg.AcceptedMints))
		for _, m := range cfg.AcceptedMints {
			urls = append(urls, m.URL)
		}
		return urls
	}

	// The operator mint must cross as a shell-SINGLE-QUOTED jq --arg. We run
	// the filter through the exact configJqCmd builder, but the jq invocation
	// in that builder targets /etc/tollgate/config.json on a real router.
	// Instead we drive jq directly here with the SAME host-computed values
	// ($m/$of/$df/$dm) plus the operator mint as --arg mu.
	runJq := func(t *testing.T, config, mintArg string, includeTestnut bool) string {
		t.Helper()
		args := []string{
			"--argjson", "m", "5",
			"--argjson", "of", "0.9000",
			"--argjson", "df", "0.1000",
			"--argjson", "dm", defaultMintsJSON(includeTestnut),
			"--arg", "mu", mintArg,
			configJqFilter(),
		}
		cmd := exec.Command(jqPath, args...)
		cmd.Stdin = strings.NewReader(config)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("jq failed: %v\nstderr: %s", err, ee.Stderr)
			}
			t.Fatalf("jq failed to start: %v", err)
		}
		return string(out)
	}

	baseConfig := `{"margin":0,"profit_share":[{"identity":"owner","factor":1},{"identity":"developer","factor":0}],"accepted_mints":[{"url":"https://mint.coinos.io","min_balance":64}]}`
	// A FRESH router: no accepted_mints key at all. The old filter threw
	// "Cannot iterate over null" here; the fixed filter must succeed.
	emptyMintsConfig := `{"margin":0,"profit_share":[{"identity":"owner","factor":1},{"identity":"developer","factor":0}]}`

	cases := []struct {
		name       string
		config     string
		mintArg    string
		testnut    bool
		want       []string
		wantMargin float64
		wantOwnerF float64
		wantDevF   float64
	}{
		{
			name:       "empty custom mint adds only defaults, existing coinos kept, no dup",
			config:     baseConfig,
			mintArg:    "",
			want:       wantProdMintURLs,
			wantMargin: 5, wantOwnerF: 0.9, wantDevF: 0.1,
		},
		{
			name:       "custom mint appended once",
			config:     baseConfig,
			mintArg:    "https://mint.example.com",
			want:       append(append([]string{"https://mint.coinos.io"}, "https://mint.example.com"), wantProdMintURLs[1:]...),
			wantMargin: 5, wantOwnerF: 0.9, wantDevF: 0.1,
		},
		{
			name:    "custom mint matching a default URL is not duplicated",
			config:  baseConfig,
			mintArg: "https://kashu.me",
			// $mu=kashu is appended (not yet present in base config), then the
			// $dm merge skips it ($have already includes it): same SET as the
			// production list, but kashu sits at its appended slot.
			want: []string{"https://mint.coinos.io", "https://kashu.me",
				"https://mint.minibits.cash/Bitcoin", "https://mint.lnserver.com",
				"https://mint.macadamia.cash", "https://mint.westernbtc.com",
				"https://mint.cubabitcoin.org"},
			wantMargin: 5, wantOwnerF: 0.9, wantDevF: 0.1,
		},
		{
			// FRESH router with no accepted_mints: must NOT throw.
			name:       "fresh config without accepted_mints gets defaults",
			config:     emptyMintsConfig,
			mintArg:    "",
			want:       wantProdMintURLs,
			wantMargin: 5, wantOwnerF: 0.9, wantDevF: 0.1,
		},
		{
			name:    "testnut opt-in payload adds both testnut mints",
			config:  baseConfig,
			mintArg: "",
			testnut: true,
			want: append(append([]string{}, wantProdMintURLs...),
				"https://nofee.testnut.cashu.space",
				"https://testnut.cashu.space"),
			wantMargin: 5, wantOwnerF: 0.9, wantDevF: 0.1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runJq(t, tc.config, tc.mintArg, tc.testnut)
			got := extractURLs(out)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("merged mints = %v\nwant           = %v", got, tc.want)
			}
			// Idempotency: run the same merge over the merged output again —
			// mint list must not change.
			out2 := runJq(t, out, tc.mintArg, tc.testnut)
			got2 := extractURLs(out2)
			if !reflect.DeepEqual(got2, got) {
				t.Errorf("merge not idempotent:\nfirst  = %v\nsecond = %v", got, got2)
			}
			// Margin / profit_share written correctly.
			var cfg struct {
				Margin      float64 `json:"margin"`
				ProfitShare []struct {
					Identity string  `json:"identity"`
					Factor   float64 `json:"factor"`
				} `json:"profit_share"`
			}
			if err := json.Unmarshal([]byte(out), &cfg); err != nil {
				t.Fatalf("parse merged: %v", err)
			}
			if cfg.Margin != tc.wantMargin {
				t.Errorf("margin = %v, want %v", cfg.Margin, tc.wantMargin)
			}
			for _, ps := range cfg.ProfitShare {
				switch ps.Identity {
				case "owner":
					if ps.Factor != tc.wantOwnerF {
						t.Errorf("owner factor = %v, want %v", ps.Factor, tc.wantOwnerF)
					}
				case "developer":
					if ps.Factor != tc.wantDevF {
						t.Errorf("developer factor = %v, want %v", ps.Factor, tc.wantDevF)
					}
				}
			}
		})
	}
}

// TestConfigJqCmdQuotesOperatorMint is the quoted-$mu regression: the cfgCmd
// must embed the operator mint as a shell-SINGLE-QUOTED jq --arg so an
// empty/spacey mint cannot consume the filter or corrupt the command.
func TestConfigJqCmdQuotesOperatorMint(t *testing.T) {
	cmd := configJqCmd(5, "0.9000", "0.1000", "", false)
	// Empty mint must still be single-quoted: `--arg mu ''`.
	if !strings.Contains(cmd, "--arg mu '' ") {
		t.Errorf("empty mint must be single-quoted (--arg mu ''):\n%s", cmd)
	}
	// A mint with a space/shell char must be fully single-quoted.
	spacey := "https://mint.example.com/foo bar"
	cmd2 := configJqCmd(5, "0.9000", "0.1000", spacey, false)
	if !strings.Contains(cmd2, "--arg mu 'https://mint.example.com/foo bar' ") {
		t.Errorf("spacey mint must be single-quoted verbatim:\n%s", cmd2)
	}
	// Embedded single quote must be escaped so it cannot break out.
	quote := "https://mint.example.com/x'y"
	cmd3 := configJqCmd(5, "0.9000", "0.1000", quote, false)
	if strings.Contains(cmd3, "--arg mu 'https://mint.example.com/x'y' ") {
		t.Errorf("mint with a single quote must be escaped (not raw-interpolated):\n%s", cmd3)
	}
	if !strings.Contains(cmd3, "'\\''") {
		t.Errorf("mint with a single quote must escape via '\\'' sequence:\n%s", cmd3)
	}
	// The filter must be present (not consumed as an --arg value).
	for _, c := range []string{cmd, cmd2, cmd3} {
		if !strings.Contains(c, ".accepted_mints") {
			t.Errorf("cfgCmd must contain the jq filter program:\n%s", c)
		}
	}
}

// TestPasswdCommandUsesBase64Carrier asserts the root password command does
// NOT contain the raw password literal — it cross as a base64 carrier, so the
// plaintext is never visible in process argv on the router.
func TestPasswdCommandUsesBase64Carrier(t *testing.T) {
	const pw = "hunter2$Secret"
	cmd := passwdCommand(pw)
	if strings.Contains(cmd, pw) {
		t.Errorf("passwd command must NOT contain the raw password literal:\n%s", cmd)
	}
	// The decoded base64 of the embedded carrier must equal the password, and
	// the command must pipe it via printf into passwd.
	b64 := base64.StdEncoding.EncodeToString([]byte(pw))
	if !strings.Contains(cmd, b64) {
		t.Errorf("passwd command must embed the base64 carrier %q:\n%s", b64, cmd)
	}
	if !strings.Contains(cmd, "| passwd root") {
		t.Errorf("passwd command must pipe into `passwd root`:\n%s", cmd)
	}
}

// TestSecuritySurface regressions for the deploy-surface exposure:
// loopback-only bind and no CORS wildcard.
func TestSecuritySurface(t *testing.T) {
	t.Run("bind defaults to loopback", func(t *testing.T) {
		if got := listenAddress("", "8099"); got != "127.0.0.1:8099" {
			t.Errorf("default bind = %q, want %q (loopback-only)", got, "127.0.0.1:8099")
		}
		if got := listenAddress(defaultBindHost, "8099"); got != "127.0.0.1:8099" {
			t.Errorf("loopback bind = %q, want %q", got, "127.0.0.1:8099")
		}
	})
	t.Run("CORS does not emit wildcard and allowlists loopback", func(t *testing.T) {
		// The wildcard must never be generated anywhere.
		if s := corsMiddleware(http.NotFoundHandler()); s == nil {
			t.Fatal("corsMiddleware should return a handler")
		}
		// Build the middleware and drive a request to inspect headers.
		req := httptest.NewRequest("GET", "http://127.0.0.1:8099/api/scan", nil)
		req.Header.Set("Origin", "http://127.0.0.1:8099")
		rec := httptest.NewRecorder()
		corsMiddleware(http.NotFoundHandler()).ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8099" {
			t.Errorf("loopback origin ACAO = %q, want echoed %q", got, "http://127.0.0.1:8099")
		}
		if strings.Contains(rec.Header().Get("Access-Control-Allow-Origin"), "*") {
			t.Errorf("ACAO must not be a wildcard, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
		}

		// A foreign origin must get NO ACAO header at all.
		req2 := httptest.NewRequest("GET", "http://127.0.0.1:8099/api/scan", nil)
		req2.Header.Set("Origin", "https://evil.example.com")
		rec2 := httptest.NewRecorder()
		corsMiddleware(http.NotFoundHandler()).ServeHTTP(rec2, req2)
		if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("foreign origin ACAO = %q, want empty (browser must block cross-origin reads)", got)
		}
	})
	t.Run("corsAllowedOrigin allowlist", func(t *testing.T) {
		for _, ok := range []string{
			"http://localhost:8099",
			"http://127.0.0.1:8099",
			"http://localhost:9999",
			"https://localhost:8099",
		} {
			if !corsAllowedOrigin(ok) {
				t.Errorf("corsAllowedOrigin(%q) = false, want true", ok)
			}
		}
		for _, bad := range []string{
			"",
			"https://evil.example.com",
			"http://example.com",
			"null",
			"not-a-url",
		} {
			if corsAllowedOrigin(bad) {
				t.Errorf("corsAllowedOrigin(%q) = true, want false", bad)
			}
		}
	})
}

// TestStaSetupScriptUsesBase64Carriers asserts the WiFi STA passphrase and
// SSID cross as base64 carriers — not string-interpolated into the UCI shell
// commands — so a password/SSID can never inject shell.
func TestStaSetupScriptUsesBase64Carriers(t *testing.T) {
	const ssid = "MyNet`; rm -rf / #"
	const key = "Pass'word$; id #"
	script := staSetupScript(ssid, key, "")
	if strings.Contains(script, ssid) {
		t.Errorf("STA script must not contain the raw SSID (injection-prone):\n%s", script)
	}
	if strings.Contains(script, key) {
		t.Errorf("STA script must not contain the raw wifi key (injection-prone):\n%s", script)
	}
	if !strings.Contains(script, "sta_ssid=$(echo "+base64.StdEncoding.EncodeToString([]byte(ssid))+" | base64 -d)") {
		t.Errorf("STA script must decode the SSID base64 carrier:\n%s", script)
	}
	if !strings.Contains(script, "sta_key=$(echo "+base64.StdEncoding.EncodeToString([]byte(key))+" | base64 -d)") {
		t.Errorf("STA script must decode the wifi key base64 carrier:\n%s", script)
	}
	// The uci writes must use the shell variables, never raw literals.
	if !strings.Contains(script, ".ssid=\"$sta_ssid\"") || !strings.Contains(script, ".key=\"$sta_key\"") {
		t.Errorf("STA script must set ssid/key from shell variables:\n%s", script)
	}
}
