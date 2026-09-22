package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sampleFeedMakefile mirrors the fields the installer reports from the real
// feed recipe (FreedomTechFeed/packages net/tollgate-wrt/Makefile).
const sampleFeedMakefile = `# net/tollgate-wrt/Makefile (fixture)
PKG_VERSION:=0.6.0_alpha2_pre8
PKG_SOURCE_VERSION:=66c1f70797b308502b596327ad77d99764158f8f
PKG_SOURCE_TAG:=v0.6.0-alpha2
PKG_RELEASE:=1
PKG_HASH:=270d16a2ccda3c2aca531d0c7c0397144aa63d45b929dbfee2ce475057d523ad
`

func TestParseFeedMakefile(t *testing.T) {
	got := parseFeedMakefile(sampleFeedMakefile)
	if got.PKGVersion != "0.6.0_alpha2_pre8" {
		t.Errorf("PKGVersion = %q", got.PKGVersion)
	}
	if got.SourceVersion != "66c1f70797b308502b596327ad77d99764158f8f" {
		t.Errorf("SourceVersion = %q", got.SourceVersion)
	}
	if got.SourceSHA7 != "66c1f70" {
		t.Errorf("SourceSHA7 = %q", got.SourceSHA7)
	}
	if got.SourceTag != "v0.6.0-alpha2" {
		t.Errorf("SourceTag = %q", got.SourceTag)
	}
	if got.PKGHash != "270d16a2ccda3c2aca531d0c7c0397144aa63d45b929dbfee2ce475057d523ad" {
		t.Errorf("PKGHash = %q", got.PKGHash)
	}
}

// The pin is fetched at the exact RELEASE TAG (not a branch) so the reported
// pin is the one that release was built from.
func TestFeedMakefileURLUsesExactTagRef(t *testing.T) {
	u := feedMakefileURL("v0.6.0-alpha2-pre9")
	want := "https://raw.githubusercontent.com/" + feedRepoSlug +
		"/v0.6.0-alpha2-pre9/net/tollgate-wrt/Makefile"
	if u != want {
		t.Errorf("feedMakefileURL = %q, want %q", u, want)
	}
}

func clearFeedPinCache() {
	feedPinMu.Lock()
	feedPinCache = map[string]feedMakefileInfo{}
	feedPinMu.Unlock()
}

func TestHandleConfig(t *testing.T) {
	orig := feedHTTPGet
	defer func() { feedHTTPGet = orig }()
	feedHTTPGet = func(url string) ([]byte, error) { return []byte(sampleFeedMakefile), nil }
	clearFeedPinCache()
	defer clearFeedPinCache()

	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handleConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got configResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FeedRepo != feedRepoSlug {
		t.Errorf("FeedRepo = %q, want %q", got.FeedRepo, feedRepoSlug)
	}
	if got.FeedReleaseTag != feedReleaseTag {
		t.Errorf("FeedReleaseTag = %q, want %q", got.FeedReleaseTag, feedReleaseTag)
	}
	if got.FeedPkgVersion != feedPkgVersion() {
		t.Errorf("FeedPkgVersion = %q, want %q", got.FeedPkgVersion, feedPkgVersion())
	}
	if got.FeedModulePin != "66c1f70797b308502b596327ad77d99764158f8f" {
		t.Errorf("FeedModulePin = %q", got.FeedModulePin)
	}
	if got.FeedModulePin7 != "66c1f70" {
		t.Errorf("FeedModulePin7 = %q", got.FeedModulePin7)
	}
	if got.FeedModuleTag != "v0.6.0-alpha2" {
		t.Errorf("FeedModuleTag = %q", got.FeedModuleTag)
	}
	if !strings.Contains(got.FeedReleaseURL, feedReleaseTag) {
		t.Errorf("FeedReleaseURL = %q should contain tag %q", got.FeedReleaseURL, feedReleaseTag)
	}
	if got.FeedPinError != "" {
		t.Errorf("FeedPinError = %q, want empty", got.FeedPinError)
	}
}

// A failed Makefile fetch must not fail /api/config: the identity still comes
// back, only the module pin is empty with an error note.
func TestHandleConfigPinFetchFailsSoft(t *testing.T) {
	orig := feedHTTPGet
	defer func() { feedHTTPGet = orig }()
	feedHTTPGet = func(url string) ([]byte, error) { return nil, errStubFetch }
	clearFeedPinCache()
	defer clearFeedPinCache()

	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	handleConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the pin fetch fails", w.Code)
	}
	var got configResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.FeedPinError == "" {
		t.Error("expected FeedPinError to be set on fetch failure")
	}
	if got.FeedModulePin != "" {
		t.Errorf("FeedModulePin = %q, want empty on failure", got.FeedModulePin)
	}
	if got.FeedReleaseTag != feedReleaseTag {
		t.Errorf("identity should still be returned, got tag %q", got.FeedReleaseTag)
	}
}

func TestHandleConfigMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/config", nil)
	w := httptest.NewRecorder()
	handleConfig(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", w.Code)
	}
}

var errStubFetch = errors.New("stub fetch failure")
