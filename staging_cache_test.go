package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Staging-cache tests for the PreStage feature (plan Task 1).
//
// The cache lives on the Job struct (guarded by j.mu), keyed by the exact
// asset URL, and is populated by stageAssets. The deploy steps (flash /
// install) consume the cache in a later task; these tests pin the cache
// semantics stageAssets must provide:
//   - stage once, deploy twice → only one fetch per URL (idempotent)
//   - failed fetches are reported and are NOT cached (retryable next run)
//   - empty/nil URL lists are no-ops
//   - the URL-selection helper returns the right asset set per router state

// TestStageAssetsStagesOnceAndReusesCache is the plan's core verification:
// stage once, deploy twice, assert only one fetch. httptest servers count
// requests — a second stageAssets call against the same Job must not hit
// the network again.
func TestStageAssetsStagesOnceAndReusesCache(t *testing.T) {
	var hitsA, hitsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		_, _ = w.Write([]byte("ASSET_A_BYTES"))
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		_, _ = w.Write([]byte("ASSET_B_BYTES"))
	}))
	defer srvB.Close()

	job := newJob("192.168.1.1")
	urls := []string{srvA.URL + "/a.bin", srvB.URL + "/b.bin"}

	// First deploy: both assets fetched exactly once and cached.
	if failed := stageAssets(job, urls); len(failed) != 0 {
		t.Fatalf("first stageAssets failed on %v", failed)
	}
	if got := hitsA.Load(); got != 1 {
		t.Errorf("asset A fetched %d times after first stage, want 1", got)
	}
	if got := hitsB.Load(); got != 1 {
		t.Errorf("asset B fetched %d times after first stage, want 1", got)
	}
	if data, ok := job.stagedAsset(urls[0]); !ok || string(data) != "ASSET_A_BYTES" {
		t.Errorf("stagedAsset(A) = %q, %v; want %q, true", data, ok, "ASSET_A_BYTES")
	}
	if data, ok := job.stagedAsset(urls[1]); !ok || string(data) != "ASSET_B_BYTES" {
		t.Errorf("stagedAsset(B) = %q, %v; want %q, true", data, ok, "ASSET_B_BYTES")
	}

	// Second deploy (same Job): cache hit, zero additional fetches.
	if failed := stageAssets(job, urls); len(failed) != 0 {
		t.Fatalf("second stageAssets failed on %v", failed)
	}
	if got := hitsA.Load(); got != 1 {
		t.Errorf("asset A re-fetched %d times total, want 1 (cache must be reused)", got)
	}
	if got := hitsB.Load(); got != 1 {
		t.Errorf("asset B re-fetched %d times total, want 1 (cache must be reused)", got)
	}
}

// TestStageAssetsPartialFailureNotCached verifies a failed fetch is reported,
// not cached, and is retried (not silently skipped) on the next stage run,
// while a successfully staged URL is never re-fetched.
func TestStageAssetsPartialFailureNotCached(t *testing.T) {
	var goodHits, badHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits.Add(1)
		_, _ = w.Write([]byte("PKG_BYTES"))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.NotFound(w, r)
	}))
	defer bad.Close()

	job := newJob("192.168.1.1")
	urls := []string{bad.URL + "/missing.ipk", good.URL + "/ok.ipk"}

	failed := stageAssets(job, urls)
	if len(failed) != 1 || failed[0] != urls[0] {
		t.Fatalf("failed = %v, want [%q]", failed, urls[0])
	}
	if _, ok := job.stagedAsset(urls[0]); ok {
		t.Error("failed asset must NOT be cached")
	}
	if data, ok := job.stagedAsset(urls[1]); !ok || string(data) != "PKG_BYTES" {
		t.Errorf("good asset not cached correctly: %q, %v", data, ok)
	}

	// Re-stage: good URL must not be re-fetched; bad URL retried (still fails).
	failed = stageAssets(job, urls)
	if len(failed) != 1 {
		t.Fatalf("second stageAssets failed = %v, want exactly the still-bad URL", failed)
	}
	if got := goodHits.Load(); got != 1 {
		t.Errorf("good asset fetched %d times, want 1 (no re-fetch of cached URL)", got)
	}
	if got := badHits.Load(); got != 2 {
		t.Errorf("bad asset fetched %d times, want 2 (retried because not cached)", got)
	}
}

// TestStageAssetsNoOps verifies nil and empty URL lists do nothing and do not
// panic.
func TestStageAssetsNoOps(t *testing.T) {
	job := newJob("192.168.1.1")
	if failed := stageAssets(job, nil); len(failed) != 0 {
		t.Errorf("stageAssets(nil) failed = %v, want empty", failed)
	}
	if failed := stageAssets(job, []string{}); len(failed) != 0 {
		t.Errorf("stageAssets(empty) failed = %v, want empty", failed)
	}
	if len(job.stageCache) != 0 {
		t.Errorf("stageCache = %v, want empty after no-op staging", job.stageCache)
	}
}

// TestStageAssetURLs pins the URL-selection helper: which assets a PreStage
// run should download depends on whether the router will be flashed (stock
// GL.iNet → sysupgrade image + both package formats, since the post-flash
// package manager is only known after reboot) or is already OpenWrt (only
// the format matching its live package manager).
func TestStageAssetURLs(t *testing.T) {
	img, ok := glModelMap["gl-mt3000"]
	if !ok {
		t.Fatal("glModelMap missing gl-mt3000 — test fixture broken")
	}
	wantImageURL := img.URL()

	t.Run("stock GL with known model stages image + both formats", func(t *testing.T) {
		got := stageAssetURLs(true, "gl-mt3000", "")
		want := []string{wantImageURL, tollgatePkgURL, tollgatePkgAPKURL}
		if len(got) != len(want) {
			t.Fatalf("stageAssetURLs = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("stageAssetURLs[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
			}
		}
	})

	t.Run("stock GL unknown model stages both formats, no image", func(t *testing.T) {
		got := stageAssetURLs(true, "gl-bogus-model", "")
		want := []string{tollgatePkgURL, tollgatePkgAPKURL}
		if len(got) != len(want) {
			t.Fatalf("stageAssetURLs = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("stageAssetURLs[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("already-OpenWrt opkg stages ipk only", func(t *testing.T) {
		got := stageAssetURLs(false, "", "opkg")
		want := []string{tollgatePkgURL}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("stageAssetURLs = %v, want %v", got, want)
		}
	})

	t.Run("already-OpenWrt apk stages apk only", func(t *testing.T) {
		got := stageAssetURLs(false, "", "apk")
		want := []string{tollgatePkgAPKURL}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("stageAssetURLs = %v, want %v", got, want)
		}
	})

	t.Run("already-OpenWrt unknown pkgMgr stages both formats", func(t *testing.T) {
		got := stageAssetURLs(false, "", "")
		want := []string{tollgatePkgURL, tollgatePkgAPKURL}
		if len(got) != len(want) {
			t.Fatalf("stageAssetURLs = %v, want %v", got, want)
		}
	})
}

// TestStageCacheIsMutexGuarded runs concurrent reads/writes through the Job
// cache API. With -race this proves stageCache is only touched under j.mu.
func TestStageCacheIsMutexGuarded(t *testing.T) {
	job := newJob("192.168.1.1")
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			url := "https://example.invalid/asset-" + string(rune('a'+n))
			job.stageAsset(url, []byte{byte(n)})
			_, _ = job.stagedAsset(url)
			_, _ = job.stagedAsset("https://example.invalid/missing")
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if len(job.stageCache) != 8 {
		t.Errorf("stageCache len = %d, want 8", len(job.stageCache))
	}
}
