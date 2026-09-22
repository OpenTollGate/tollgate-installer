package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Cache-consumer tests for the PreStage feature (plan Task 3): the flash step
// (flashImageBytes) and the install step (stagedOrLiveBytes) must READ the
// Job's stageCache FIRST and only fall back to a live download on a cache
// miss. These tests pin the two behaviours Task 3's verification calls for:
//   - cache holds the asset → the fetch helper performs ZERO network requests
//   - cache miss → live download happens (single httpGetFile for small
//     packages; downloadWithRetry semantics for the flash image, including
//     the definitive-4xx short-circuit)
//   - cache-miss + live-fail returns an error, which is exactly the condition
//     under which runDeployment's install step triggers the unchanged
//     router-side wget → feed fallback chain

// TestFlashImageBytesCacheHitNoNetwork is Task 3's core flash verification:
// with the image URL staged, flashImageBytes must serve the staged bytes and
// the live server must never be hit.
func TestFlashImageBytesCacheHitNoNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("LIVE_IMAGE_BYTES"))
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	job.stageAsset(srv.URL, []byte("STAGED_IMAGE_BYTES"))

	data, err := flashImageBytes(job, srv.URL)
	if err != nil {
		t.Fatalf("flashImageBytes: unexpected error: %v", err)
	}
	if string(data) != "STAGED_IMAGE_BYTES" {
		t.Errorf("flashImageBytes returned %q, want staged bytes %q", data, "STAGED_IMAGE_BYTES")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("live server hit %d times while the image was staged — a pre-staged flash must perform zero network fetches", got)
	}
}

// TestFlashImageBytesCacheMissDownloadsLive verifies the flash fallback: no
// staged bytes → the image is downloaded live exactly once.
func TestFlashImageBytesCacheMissDownloadsLive(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("LIVE_IMAGE_BYTES"))
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	data, err := flashImageBytes(job, srv.URL)
	if err != nil {
		t.Fatalf("flashImageBytes: unexpected error: %v", err)
	}
	if string(data) != "LIVE_IMAGE_BYTES" {
		t.Errorf("flashImageBytes returned %q, want live bytes %q", data, "LIVE_IMAGE_BYTES")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("live server hit %d times on a cache miss, want exactly 1", got)
	}
}

// TestFlashImageBytesCacheMiss404ShortCircuits verifies that on a cache miss
// with a definitive 4xx the flash path fails immediately (downloadWithRetry
// semantics) instead of burning 3 attempts on a broken URL.
func TestFlashImageBytesCacheMiss404ShortCircuits(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	if _, err := flashImageBytes(job, srv.URL); err == nil {
		t.Fatal("flashImageBytes: expected an error for a 404 image URL, got nil")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("404 image URL fetched %d times, want 1 (definitive 4xx must short-circuit)", got)
	}
}

// TestStagedOrLiveBytesCacheHitNoNetwork is the install-step counterpart of
// the flash cache-hit test: a staged package URL is served with zero network
// fetches and reports fromCache=true.
func TestStagedOrLiveBytesCacheHitNoNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("LIVE_PKG_BYTES"))
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	job.stageAsset(srv.URL, []byte("STAGED_PKG_BYTES"))

	data, fromCache, err := stagedOrLiveBytes(job, "tollgate-wrt .ipk", srv.URL)
	if err != nil {
		t.Fatalf("stagedOrLiveBytes: unexpected error: %v", err)
	}
	if !fromCache {
		t.Error("stagedOrLiveBytes reported fromCache=false for a staged URL")
	}
	if string(data) != "STAGED_PKG_BYTES" {
		t.Errorf("stagedOrLiveBytes returned %q, want staged bytes %q", data, "STAGED_PKG_BYTES")
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("live server hit %d times while the package was staged — a pre-staged install must perform zero network fetches", got)
	}
}

// TestStagedOrLiveBytesCacheMissFetchesLive verifies the install fallback: a
// cache miss performs exactly one live httpGetFile and reports fromCache=false
// (the signal the install step uses to log the real acquisition path).
func TestStagedOrLiveBytesCacheMissFetchesLive(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("LIVE_PKG_BYTES"))
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	data, fromCache, err := stagedOrLiveBytes(job, "tollgate-wrt .apk", srv.URL)
	if err != nil {
		t.Fatalf("stagedOrLiveBytes: unexpected error: %v", err)
	}
	if fromCache {
		t.Error("stagedOrLiveBytes reported fromCache=true on a cache miss")
	}
	if string(data) != "LIVE_PKG_BYTES" {
		t.Errorf("stagedOrLiveBytes returned %q, want live bytes %q", data, "LIVE_PKG_BYTES")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("live server hit %d times on a cache miss, want exactly 1", got)
	}
}

// TestStagedOrLiveBytesCacheMiss404ReturnsError verifies that a cache-miss +
// live-fail produces an error (not silent empty bytes). That error is exactly
// the condition under which the install step's UNCHANGED fallback chain
// (router-side wget, then feed install) still triggers.
func TestStagedOrLiveBytesCacheMiss404ReturnsError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	data, fromCache, err := stagedOrLiveBytes(job, "jq .ipk", srv.URL)
	if err == nil {
		t.Fatal("stagedOrLiveBytes: expected an error for a 404 package URL, got nil")
	}
	if fromCache {
		t.Error("stagedOrLiveBytes reported fromCache=true when the fetch failed")
	}
	if len(data) != 0 {
		t.Errorf("stagedOrLiveBytes returned %d bytes alongside an error, want none", len(data))
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("404 package URL fetched %d times, want 1 (no retry on small packages)", got)
	}
}

// TestStagedOrLiveBytesIsExactURLKeyed guards the cache-key contract: the
// lookup must be by the exact URL, so a URL with a different query or a
// trailing-slash variant must NOT hit an entry staged under the plain URL
// (and a pre-staged deploy must not silently re-download under a variant
// spelling that misses).
func TestStagedOrLiveBytesIsExactURLKeyed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("LIVE_BYTES"))
	}))
	defer srv.Close()

	job := newJob("192.168.1.1")
	base := srv.URL + "/pkg.ipk"
	job.stageAsset(base, []byte("STAGED_BYTES"))

	for name, miss := range map[string]string{
		"query":    base + "?cache=1", // same path, different URL
		"trailing": base + "/",        // trailing slash
	} {
		data, fromCache, err := stagedOrLiveBytes(job, "pkg", miss)
		if fromCache {
			t.Errorf("%s variant %q must be a cache miss (exact-URL keying)", name, miss)
		}
		if err != nil {
			t.Errorf("%s variant %q: unexpected fetch error: %v", name, miss, err)
		}
		if string(data) == "STAGED_BYTES" {
			t.Errorf("%s variant %q served staged bytes — lookups must be exact-URL keyed", name, miss)
		}
	}

	data, fromCache, err := stagedOrLiveBytes(job, "pkg", base)
	if err != nil || !fromCache || string(data) != "STAGED_BYTES" {
		t.Errorf("exact URL lookup: data=%q fromCache=%v err=%v, want a staged cache hit", data, fromCache, err)
	}
}

// TestStageAndConsumeKeysAlign guards the producer→consumer URL contract that
// makes the offline deploy work. stageCache is keyed by the exact asset URL:
// the PreStage producer (stageAssetURLs) and the Task-3 consumers
// (flashImageBytes on img.URL(); stagedOrLiveBytes on tollgatePkgURL /
// tollgatePkgAPKURL) must agree on every URL, or a staged deploy would
// silently miss the cache and fall back to a live download. For every model a
// stock-GL deploy can flash, staging must emit the flash-step image URL plus
// BOTH package formats (the post-reboot package manager is unknown until the
// fresh OpenWrt boots); for an already-OpenWrt router the exact format the
// install step will look up must be staged.
func TestStageAndConsumeKeysAlign(t *testing.T) {
	if len(glModelMap) == 0 {
		t.Fatal("glModelMap empty — cannot verify key alignment")
	}
	for model, img := range glModelMap {
		urls := stageAssetURLs(true, model, "")
		imgURL := img.URL()
		var sawImg, sawIPK, sawAPK bool
		for _, u := range urls {
			sawImg = sawImg || u == imgURL
			sawIPK = sawIPK || u == tollgatePkgURL
			sawAPK = sawAPK || u == tollgatePkgAPKURL
		}
		if !sawImg {
			t.Errorf("%s: stageAssetURLs must stage the flash-step lookup key %q", model, imgURL)
		}
		if !sawIPK || !sawAPK {
			t.Errorf("%s: stageAssetURLs must stage both package formats (ipk=%v apk=%v) — install step picks one by pkgMgr after reboot", model, sawIPK, sawAPK)
		}
	}

	// Already-OpenWrt: the staged URL must be the exact one the install step
	// looks up once it probes the live package manager.
	if got := stageAssetURLs(false, "", "opkg"); len(got) != 1 || got[0] != tollgatePkgURL {
		t.Errorf("stageAssetURLs(opkg) = %v, want exactly [tollgatePkgURL] (the install-step opkg lookup key)", got)
	}
	if got := stageAssetURLs(false, "", "apk"); len(got) != 1 || got[0] != tollgatePkgAPKURL {
		t.Errorf("stageAssetURLs(apk) = %v, want exactly [tollgatePkgAPKURL] (the install-step apk lookup key)", got)
	}
}

// TestConsumersLoggedPaths ensures the two acquisition paths produce distinct,
// operator-visible log lines (so a deploy that ran from the cache is auditable
// in the job log without reading code).
func TestConsumersLoggedPaths(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("BYTES"))
	}))
	defer srv.Close()

	// Cache hit: the log must say "staged"/"cache", never "Downloading".
	hitJob := newJob("192.168.1.1")
	hitJob.stageAsset(srv.URL, []byte("BYTES"))
	if _, err := flashImageBytes(hitJob, srv.URL); err != nil {
		t.Fatalf("flashImageBytes: %v", err)
	}
	if _, fromCache, err := stagedOrLiveBytes(hitJob, "pkg", srv.URL); err != nil || !fromCache {
		t.Fatalf("stagedOrLiveBytes: err=%v fromCache=%v", err, fromCache)
	}
	var sawDownloading bool
	for _, entry := range hitJob.Log {
		if strings.Contains(entry.Msg, "Downloading") {
			sawDownloading = true
		}
		if !strings.Contains(entry.Msg, "cache") && !strings.Contains(entry.Msg, "staged") {
			t.Errorf("cache-hit log line %q does not mention the cache/staged source", entry.Msg)
		}
	}
	if sawDownloading {
		t.Error("cache-hit run logged a \"Downloading\" line — staged assets must not claim a download")
	}

	// Cache miss: the log must show the live "Downloading" line.
	missJob := newJob("192.168.1.1")
	if _, err := flashImageBytes(missJob, srv.URL); err != nil {
		t.Fatalf("flashImageBytes (miss): %v", err)
	}
	if _, fromCache, err := stagedOrLiveBytes(missJob, "pkg", srv.URL); err != nil || fromCache {
		t.Fatalf("stagedOrLiveBytes (miss): err=%v fromCache=%v", err, fromCache)
	}
	var downloadLines int
	for _, entry := range missJob.Log {
		if strings.Contains(entry.Msg, "Downloading") {
			downloadLines++
		}
	}
	if downloadLines != 2 {
		t.Errorf("cache-miss run logged %d \"Downloading\" lines, want 2 (one per consumer)", downloadLines)
	}
}
