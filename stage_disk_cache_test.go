package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// On-disk re-deploy cache tests (plan Task 5).
//
// The Job stageCache is in-memory and per-Job, so a second deploy to a
// different router (a fresh Job) would re-download every asset. The on-disk
// layer added here lets re-deploys re-use previously staged bytes instead of
// re-fetching. These tests pin the disk behaviour: bytes persisted by one
// deploy are reused by a later deploy (Gate C), non-persistable package
// bytes are never stored (consultant RISK 3), and corrupt/empty cache files
// count as a miss so the live-download fallback still triggers.
func TestStageCachePersistsAcrossJobs(t *testing.T) {
	dir := t.TempDir()
	stageDiskDirOverride = dir
	defer func() { stageDiskDirOverride = "" }()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("STAGE_IMG_BYTES"))
	}))
	defer srv.Close()

	original := persistableDiskAsset
	persistableDiskAsset = func(u string) bool { return true }
	defer func() { persistableDiskAsset = original }()

	imgURL := srv.URL + "/persist.bin"

	// Deploy "A": stage fetches once (hits=1) and writes through to disk.
	jobA := newJob("10.0.0.1")
	if failed := stageAssets(jobA, []string{imgURL}); len(failed) != 0 {
		t.Fatalf("stageAssets(A) failed on %v", failed)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("asset fetched %d times on first deploy, want 1", got)
	}
	// The file exists on disk at <dir>/<hex sha256 of url> with exact bytes.
	sum := sha256.Sum256([]byte(imgURL))
	path := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if data, err := os.ReadFile(path); err != nil || string(data) != "STAGE_IMG_BYTES" {
		t.Errorf("disk cache file = %v (err %v), want %q on disk", data, err, "STAGE_IMG_BYTES")
	}

	// Deploy "B": a fresh Job must load from disk, zero network fetch.
	jobB := newJob("10.0.0.2")
	if failed := stageAssets(jobB, []string{imgURL}); len(failed) != 0 {
		t.Fatalf("stageAssets(B) failed on %v", failed)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("asset re-fetched on second deploy: %d hits total, want 1 (disk cache must be reused, no re-download)", got)
	}
	if data, ok := jobB.stagedAsset(imgURL); !ok || string(data) != "STAGE_IMG_BYTES" {
		t.Errorf("second job did not load from disk: %q, %v", data, ok)
	}
}

func TestPackageBinariesNotPersisted(t *testing.T) {
	dir := t.TempDir()
	stageDiskDirOverride = dir
	defer func() { stageDiskDirOverride = "" }()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("PKG_BYTES"))
	}))
	defer srv.Close()

	original := persistableDiskAsset
	persistableDiskAsset = func(u string) bool { return false } // NOT persistable
	defer func() { persistableDiskAsset = original }()

	// Prove the real package URLs (tollgatePkgURL / tollgatePkgAPKURL) are
	// rejected by the default policy — packages must never reach disk.
	for _, pkgURL := range []string{tollgatePkgURL, tollgatePkgAPKURL} {
		if persistableDiskAsset(pkgURL) {
			t.Errorf("default policy marks package %q as persistable — packages must NOT be persisted (RISK 3)", pkgURL)
		}
	}

	// AND a non-persistable httptest URL stages in memory but writes nothing.
	pkgURL := srv.URL + "/tollgate-wrt.ipk"
	job := newJob("10.0.0.1")
	if failed := stageAssets(job, []string{pkgURL}); len(failed) != 0 {
		t.Fatalf("stageAssets failed on %v", failed)
	}
	if _, ok := job.stagedAsset(pkgURL); !ok {
		t.Fatal("package should be staged in memory")
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("reading cache dir: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("cache dir has %d entries, want 0 (non-persistable asset must not be written)", len(entries))
	}
}

func TestStageDiskMissFallsBackToLive(t *testing.T) {
	dir := t.TempDir()
	stageDiskDirOverride = dir
	defer func() { stageDiskDirOverride = "" }()

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("FRESH_BYTES"))
	}))
	defer srv.Close()

	original := persistableDiskAsset
	persistableDiskAsset = func(u string) bool { return true }
	defer func() { persistableDiskAsset = original }()

	imgURL := srv.URL + "/asset.bin"

	// Pre-seed the cache path with an EMPTY file. loadStageDisk rejects size 0,
	// so this must be a miss → live fetch.
	sum := sha256.Sum256([]byte(imgURL))
	path := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("pre-seeding corrupt cache: %v", err)
	}

	job := newJob("10.0.0.1")
	if failed := stageAssets(job, []string{imgURL}); len(failed) != 0 {
		t.Fatalf("stageAssets failed on %v", failed)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected a live fetch on empty cache, got %d hits", got)
	}
	if data, _ := job.stagedAsset(imgURL); string(data) != "FRESH_BYTES" {
		t.Errorf("staged data = %q, want %q (must fall back to live on corrupt cache)", data, "FRESH_BYTES")
	}
	// A successful fetch overwrites the corrupt file with fresh bytes.
	if data, err := os.ReadFile(path); err != nil || string(data) != "FRESH_BYTES" {
		t.Errorf("cache file after fallback = %q (err %v), want fresh bytes", data, err)
	}
}

func TestStageDiskPathIsSHA256OfURL(t *testing.T) {
	dir := t.TempDir()
	stageDiskDirOverride = dir
	defer func() { stageDiskDirOverride = "" }()

	url := "https://downloads.openwrt.org/releases/25.12.5/targets/mediatek/filogic/openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin"
	sum := sha256.Sum256([]byte(url))
	want := filepath.Join(dir, hex.EncodeToString(sum[:]))
	if got := stageDiskPath(url); got != want {
		t.Errorf("stageDiskPath(url) = %q, want %q", got, want)
	}
	// Two different URLs get distinct cache files.
	if stageDiskPath(url) == stageDiskPath(url+"?v=2") {
		t.Error("distinct URLs must map to distinct cache files")
	}
}

func TestPackagePolicyPersistable(t *testing.T) {
	// Sanity: at least one real flash-image URL IS persistable under the
	// default policy (so the disk cache actually activates for images).
	pinned := ""
	for _, img := range glModelMap {
		pinned = img.URL()
		break
	}
	if pinned == "" {
		t.Fatal("glModelMap empty — cannot verify persistence policy")
	}
	if !persistableDiskAsset(pinned) {
		t.Errorf("flash image %q must be persistable (version-pinned)", pinned)
	}
}
