package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStageAssetURLsForArch(t *testing.T) {
	// Unknown arch falls back to the pinned aarch64 assets (historical behaviour).
	if got := stageAssetURLsForArch("", false, "", "opkg"); len(got) != 1 || got[0] != tollgatePkgURL {
		t.Errorf("unknown arch, opkg = %v, want [tollgatePkgURL]", got)
	}
	if got := stageAssetURLsForArch("", false, "", "apk"); len(got) != 1 || got[0] != tollgatePkgAPKURL {
		t.Errorf("unknown arch, apk = %v, want [tollgatePkgAPKURL]", got)
	}

	// A real arch is derived per-arch: the first candidate is the feed URL.
	if got := stageAssetURLsForArch("aarch64_cortex-a53", false, "", "opkg"); len(got) == 0 || got[0] != tollgatePkgURL {
		t.Errorf("aarch64 opkg = %v, want first == tollgatePkgURL", got)
	}
	x86 := feedAssetURL("x86_64", ".apk")
	if got := stageAssetURLsForArch("x86_64", false, "", "apk"); len(got) == 0 || got[0] != x86 {
		t.Errorf("x86_64 apk = %v, want first == %s", got, x86)
	}

	// Stock GL: the flash image plus both package formats.
	img, ok := glModelMap["gl-mt3000"]
	if !ok {
		t.Fatal("gl-mt3000 missing from glModelMap")
	}
	got := stageAssetURLsForArch("", true, "gl-mt3000", "")
	if len(got) == 0 || got[0] != img.URL() {
		t.Errorf("stock gl-mt3000 = %v, want first == image URL %s", got, img.URL())
	}
}

func TestAdoptStageCache(t *testing.T) {
	src := newJob("192.168.1.1")
	dst := newJob("192.168.1.1")
	src.stageAsset("https://example.test/a.ipk", []byte("AAA"))
	src.stageAsset("https://example.test/b.apk", []byte("BBB"))
	// Zero-length payloads are not cached.
	src.stageAsset("https://example.test/empty", nil)

	if n := adoptStageCache(dst, src); n != 2 {
		t.Errorf("adoptStageCache moved %d assets, want 2", n)
	}
	if data, ok := dst.stagedAsset("https://example.test/a.ipk"); !ok || string(data) != "AAA" {
		t.Errorf("dst missing adopted asset a.ipk (ok=%v)", ok)
	}
	if _, ok := dst.stagedAsset("https://example.test/empty"); ok {
		t.Error("empty asset must not be adopted")
	}

	// nil safety.
	if n := adoptStageCache(nil, src); n != 0 {
		t.Errorf("adoptStageCache(nil, src) = %d, want 0", n)
	}
	if n := adoptStageCache(dst, nil); n != 0 {
		t.Errorf("adoptStageCache(dst, nil) = %d, want 0", n)
	}
}

func TestHandlePreStageValidation(t *testing.T) {
	// Method guard.
	rr := httptest.NewRecorder()
	handlePreStage(rr, httptest.NewRequest(http.MethodGet, "/api/prestage", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr.Code)
	}
	// Missing IP.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/prestage", strings.NewReader(`{"password":"x"}`))
	handlePreStage(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing ip status = %d, want 400", rr.Code)
	}
	// Invalid JSON.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/prestage", strings.NewReader(`not json`))
	handlePreStage(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid json status = %d, want 400", rr.Code)
	}
}

func TestHandleWifiTestValidation(t *testing.T) {
	rr := httptest.NewRecorder()
	handleWifiTest(rr, httptest.NewRequest(http.MethodGet, "/api/wifi-test", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr.Code)
	}
	// ip present, ssid missing → 400.
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/wifi-test", strings.NewReader(`{"ip":"192.168.1.1"}`))
	handleWifiTest(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing ssid status = %d, want 400", rr.Code)
	}
}

func TestHandleIdentifyValidation(t *testing.T) {
	rr := httptest.NewRecorder()
	handleIdentify(rr, httptest.NewRequest(http.MethodGet, "/api/identify", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr.Code)
	}
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/identify", strings.NewReader(`{}`))
	handleIdentify(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing ip status = %d, want 400", rr.Code)
	}
}

func TestFriendlyRouterName(t *testing.T) {
	cases := []struct {
		info RouterInfo
		want string
	}{
		{RouterInfo{Vendor: "OpenWrt", Model: "glinet,gl-mt3000"}, "GL-MT3000"},
		{RouterInfo{Vendor: "GL.iNet", Model: "gl-mt3000"}, "GL-MT3000"},
		{RouterInfo{Vendor: "OpenWrt", Model: "unknown"}, "OpenWrt router"},
		{RouterInfo{Vendor: "unknown", Model: "unknown", MAC: "94:83:C4:11:22:33"}, "GL.iNet device"},
		{RouterInfo{Vendor: "unknown", Model: "unknown", MAC: ""}, "Router"},
	}
	for _, tc := range cases {
		if got := friendlyRouterName(tc.info); got != tc.want {
			t.Errorf("friendlyRouterName(%+v) = %q, want %q", tc.info, got, tc.want)
		}
	}
}

func TestPrettyModel(t *testing.T) {
	cases := map[string]string{
		"glinet,gl-mt3000": "GL-MT3000",
		"gl-mt3000":        "GL-MT3000",
		"":                 "",
		"someboard":        "someboard",
	}
	for in, want := range cases {
		if got := prettyModel(in); got != want {
			t.Errorf("prettyModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOUIVendor(t *testing.T) {
	if got := ouiVendor("94:83:c4:11:22:33"); got != "GL.iNet" {
		t.Errorf("ouiVendor gl-inet = %q, want GL.iNet", got)
	}
	if got := ouiVendor("94-83-C4-11-22-33"); got != "GL.iNet" {
		t.Errorf("ouiVendor dash format = %q, want GL.iNet", got)
	}
	if got := ouiVendor(""); got != "" {
		t.Errorf("ouiVendor empty = %q, want empty", got)
	}
}

func TestPrestageJobIDRoundTrip(t *testing.T) {
	out, err := json.Marshal(deployRequest{IP: "192.168.1.1", PrestageJobID: "123"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"prestageJobId":"123"`) {
		t.Errorf("marshal output %s missing lower-case prestageJobId", out)
	}
	var back deployRequest
	if err := json.Unmarshal([]byte(`{"prestageJobId":"abc"}`), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.PrestageJobID != "abc" {
		t.Errorf("PrestageJobID = %q, want abc", back.PrestageJobID)
	}
}

func TestBandParsers(t *testing.T) {
	if got := bandFromGHz("Mode: Master  Channel: 36 (5 GHz)"); got != "5" {
		t.Errorf("bandFromGHz 5 = %q", got)
	}
	if got := bandFromGHz("Channel: 6 (2.4 GHz)"); got != "2.4" {
		t.Errorf("bandFromGHz 2.4 = %q", got)
	}
	if got := bandFromGHz("Channel: 5 (6 GHz)"); got != "6" {
		t.Errorf("bandFromGHz 6 = %q", got)
	}
	if got := bandFromGHz("no band here"); got != "" {
		t.Errorf("bandFromGHz none = %q, want empty", got)
	}
	for freq, want := range map[int]string{2412: "2.4", 5180: "5", 5955: "6", 0: ""} {
		if got := bandFromFreq(freq); got != want {
			t.Errorf("bandFromFreq(%d) = %q, want %q", freq, got, want)
		}
	}
	if got := normalizeBand(" 2.4 "); got != "2.4" {
		t.Errorf("normalizeBand = %q", got)
	}
	if got := normalizeBand(`5"; rm -rf /`); got != "" {
		t.Errorf("normalizeBand hostile = %q, want empty", got)
	}
}

func TestParseIwinfoScanBand(t *testing.T) {
	out := `wl0-sha0   ESSID: "Home-2G"
          Mode: Master  Channel: 6 (2.4 GHz)
          Signal: -45 dBm  Quality: 70/70
          Encryption: WPA2 PSK (CCMP)
wl1-sha0   ESSID: "Home-5G"
          Mode: Master  Channel: 36 (5 GHz)
          Signal: -60 dBm  Quality: 40/70
          Encryption: WPA2 PSK (CCMP)`
	byName := map[string]string{}
	for _, s := range parseIwinfoScan(out) {
		byName[s.Name] = s.Band
	}
	if byName["Home-2G"] != "2.4" {
		t.Errorf("Home-2G band = %q, want 2.4", byName["Home-2G"])
	}
	if byName["Home-5G"] != "5" {
		t.Errorf("Home-5G band = %q, want 5", byName["Home-5G"])
	}
}

func TestParseIwScanBand(t *testing.T) {
	out := `BSS aa:bb:cc:dd:ee:01 on wlan0
	freq: 2412
	SSID: N2
BSS aa:bb:cc:dd:ee:02 on wlan1
	freq: 5180
	SSID: N5`
	byName := map[string]string{}
	for _, s := range parseIwScan(out) {
		byName[s.Name] = s.Band
	}
	if byName["N2"] != "2.4" {
		t.Errorf("N2 band = %q, want 2.4", byName["N2"])
	}
	if byName["N5"] != "5" {
		t.Errorf("N5 band = %q, want 5", byName["N5"])
	}
}

func TestAdLooksHealthy(t *testing.T) {
	if !adLooksHealthy(`{"metric":"bytes","step_size":22020096}`) {
		t.Error("an advertisement with metric must look healthy")
	}
	if !adLooksHealthy(`{"kind":10021}`) {
		t.Error("an advertisement with kind must look healthy")
	}
	if adLooksHealthy("") {
		t.Error("an empty body must NOT look healthy")
	}
	if adLooksHealthy("not found") {
		t.Error("an error body must NOT look healthy")
	}
}

func TestHandleStatusExposesProgress(t *testing.T) {
	job := newPreStageJob("192.168.1.1")
	job.setProgress(1, 3, "downloading")
	jobsMutex.Lock()
	jobs["test-progress"] = job
	jobsMutex.Unlock()
	defer func() {
		jobsMutex.Lock()
		delete(jobs, "test-progress")
		jobsMutex.Unlock()
	}()

	rr := httptest.NewRecorder()
	handleStatus(rr, httptest.NewRequest(http.MethodGet, "/api/status/test-progress", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	var body struct {
		ProgressCurrent int    `json:"progressCurrent"`
		ProgressTotal   int    `json:"progressTotal"`
		ProgressLabel   string `json:"progressLabel"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ProgressCurrent != 1 || body.ProgressTotal != 3 || body.ProgressLabel != "downloading" {
		t.Errorf("progress = %+v, want {1 3 downloading}", body)
	}
}

// TestBandFromChannelRegression reproduces the reported bug: iwinfo printed
// "Channel: 36" WITHOUT a "(5 GHz)" token, so the band came back empty and a
// 5 GHz SSID was configured on the 2.4 GHz radio (radio0) and never
// associated. The band must now be inferred from the channel number.
func TestBandFromChannelRegression(t *testing.T) {
	if got := bandFromChannel(36); got != "5" {
		t.Errorf("bandFromChannel(36) = %q, want 5", got)
	}
	if got := bandFromChannel(6); got != "2.4" {
		t.Errorf("bandFromChannel(6) = %q, want 2.4", got)
	}
	if got := bandFromChannel(200); got != "" {
		t.Errorf("bandFromChannel(200) = %q, want empty", got)
	}

	out := `wl1-sha0   ESSID: "EnterSSID-5GHz"
          Mode: Master  Channel: 36
          Signal: -60 dBm  Quality: 40/70
          Encryption: WPA PSK (CCMP)`
	got := parseIwinfoScan(out)
	if len(got) == 0 {
		t.Fatal("parseIwinfoScan returned no SSIDs")
	}
	if got[0].Name != "EnterSSID-5GHz" || got[0].Band != "5" {
		t.Errorf("parsed %+v, want name EnterSSID-5GHz band 5", got[0])
	}
}

// TestSubnetsOverlap pins the collision check used to relocate our local
// subnets away from the upstream. It must use the REAL prefixes (an upstream
// 10.47.0.0/16 collides with our 10.47.41.0/24 and 10.47.42.0/24) while not
// flagging unrelated private ranges.
func TestSubnetsOverlap(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"192.168.1.1/24", "192.168.1.23/24", true},
		{"192.168.1.1/24", "192.168.2.23/24", false},
		{"10.47.41.1/24", "10.47.0.1/16", true},  // upstream /16 contains our LAN /24
		{"10.47.42.1/24", "10.47.41.1/16", true}, // and our private /24
		{"192.168.1.1/24", "10.0.0.0/8", false},
		{"", "192.168.1.1/24", false},
		{"garbage", "192.168.1.1/24", false},
	}
	for _, c := range cases {
		if got := subnetsOverlap(c.a, c.b); got != c.want {
			t.Errorf("subnetsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestRandomPrivateLANIP pins the relocation target: inside 10.0.0.0/8, in
// 10.x.y.1 form, with no zero octets.
func TestRandomPrivateLANIP(t *testing.T) {
	for i := 0; i < 200; i++ {
		ip := randomPrivateLANIP()
		v4 := net.ParseIP(ip)
		if v4 == nil || v4.To4() == nil {
			t.Fatalf("randomPrivateLANIP returned invalid IP %q", ip)
		}
		if !strings.HasPrefix(ip, "10.") || !strings.HasSuffix(ip, ".1") {
			t.Fatalf("randomPrivateLANIP %q is not in 10.x.y.1 form", ip)
		}
		oct := strings.Split(ip, ".")
		if len(oct) != 4 || oct[1] == "0" || oct[2] == "0" {
			t.Fatalf("randomPrivateLANIP %q has a zero octet", ip)
		}
	}
}
