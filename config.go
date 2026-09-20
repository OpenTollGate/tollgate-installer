package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ─── Feed identity: module source pin from the feed Makefile ───────────────
//
// The installer's feed constants (arch.go) name the release tag and the package
// version, but NOT the upstream module COMMIT the feed package was built from.
// That pin (PKG_SOURCE_VERSION) lives only in the feed recipe
// (FreedomTechFeed/packages: net/tollgate-wrt/Makefile), so we fetch the
// Makefile at the exact release tag and report the pin alongside the rest of
// the feed identity. The fetch is fail-soft and cached per ref: an offline box
// still serves /api/config, just without the module pin.

const feedMakefilePath = "net/tollgate-wrt/Makefile"

// feedMakefileURL returns the raw URL of the feed recipe at an exact ref (tag).
// Using the TAG ref (not a branch) means the reported pin is the one THAT
// release was built from.
func feedMakefileURL(ref string) string {
	return "https://raw.githubusercontent.com/" + feedRepoSlug + "/" + ref + "/" + feedMakefilePath
}

// feedMakefileInfo is the subset of the feed recipe the installer reports.
type feedMakefileInfo struct {
	Ref           string
	URL           string
	PKGVersion    string
	SourceVersion string // PKG_SOURCE_VERSION (immutable upstream commit)
	SourceSHA7    string
	SourceTag     string // PKG_SOURCE_TAG (human-facing upstream release)
	PKGHash       string
	Error         string
}

var feedMakefileRes = map[string]*regexp.Regexp{
	"pkg_version":    regexp.MustCompile(`(?m)^PKG_VERSION:=\s*(\S+)`),
	"source_version": regexp.MustCompile(`(?m)^PKG_SOURCE_VERSION:=\s*([0-9a-fA-F]{7,40})`),
	"source_tag":     regexp.MustCompile(`(?m)^PKG_SOURCE_TAG:=\s*(\S+)`),
	"pkg_hash":       regexp.MustCompile(`(?m)^PKG_HASH:=\s*([0-9a-fA-F]{64})`),
}

func firstSub(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// parseFeedMakefile extracts the reported fields from a feed recipe. Pure, so
// it is unit-tested against a fixture of the real file.
func parseFeedMakefile(text string) feedMakefileInfo {
	var info feedMakefileInfo
	info.PKGVersion = firstSub(feedMakefileRes["pkg_version"], text)
	info.SourceVersion = firstSub(feedMakefileRes["source_version"], text)
	info.SourceTag = firstSub(feedMakefileRes["source_tag"], text)
	info.PKGHash = firstSub(feedMakefileRes["pkg_hash"], text)
	if len(info.SourceVersion) >= 7 {
		info.SourceSHA7 = info.SourceVersion[:7]
	}
	return info
}

// feedHTTPGet is the (injectable) fetcher. Kept as a var so tests can stub it.
var feedHTTPGet = func(url string) ([]byte, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

var (
	feedPinMu    sync.Mutex
	feedPinCache = map[string]feedMakefileInfo{}
)

// cachedFeedPin returns the pin for ref only if it has already been fetched
// (no network), so latency-sensitive callers can include it when it is cheap.
func cachedFeedPin(ref string) (feedMakefileInfo, bool) {
	feedPinMu.Lock()
	defer feedPinMu.Unlock()
	v, ok := feedPinCache[ref]
	return v, ok
}

// resolveFeedModulePin returns the feed recipe fields for ref, fetching once
// and caching for the process. Fail-soft: on error the Error field is set and
// the rest are empty; it never panics and never blocks indefinitely.
func resolveFeedModulePin(ref string) feedMakefileInfo {
	if v, ok := cachedFeedPin(ref); ok {
		return v
	}
	url := feedMakefileURL(ref)
	info := feedMakefileInfo{Ref: ref, URL: url}
	body, err := feedHTTPGet(url)
	if err != nil {
		info.Error = err.Error()
	} else {
		parsed := parseFeedMakefile(string(body))
		parsed.Ref, parsed.URL = ref, url
		info = parsed
	}
	feedPinMu.Lock()
	feedPinCache[ref] = info
	feedPinMu.Unlock()
	return info
}
