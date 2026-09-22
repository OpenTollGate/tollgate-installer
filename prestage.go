package main

import (
	"fmt"
	"strings"
)

// Selection-time pre-download (/api/prestage). Kept in its own file so the
// deploy step-index guard (which parses deploy.go and expects exactly one
// setStep(i,"running") per deploySteps() entry) is not confused by this
// shorter, separate step list.

// prestageSteps is the step list for a selection-time pre-download job
// (/api/prestage). Deliberately short: it only verifies SSH and stages assets,
// and never changes the router.
func prestageSteps() []Step {
	return []Step{
		{Name: "verify", Desc: "Verifying SSH access to router...", Status: "pending"},
		{Name: "stage", Desc: "Pre-downloading packages and firmware...", Status: "pending"},
	}
}

// prestageRequest is the JSON body for /api/prestage (selection-time
// pre-download). It carries only what is needed to decide WHICH assets to
// fetch; the router is never modified.
type prestageRequest struct {
	IP       string `json:"ip"`
	Password string `json:"password"`
	// ForceFlash changes which flash image is staged (a stock GL.iNet image vs
	// the router's own pinned release) when the router is already OpenWrt.
	ForceFlash bool `json:"forceFlash"`
}

// runPreStageJob is the /api/prestage worker: it probes the router's firmware
// state and arch, then stages the deploy assets into this job's cache. A later
// /api/deploy that passes this job's id (prestageJobId) reuses the cached
// bytes, so the download starts the moment the router is selected and the
// deploy does not repeat it. Read-only with respect to the router.
func runPreStageJob(job *Job, req prestageRequest) {
	client := sshConnect(req.IP, req.Password)
	if client == nil && req.Password != "" {
		client = sshConnect(req.IP, "")
	}
	if client == nil {
		jobFail(job, 0, "Cannot connect to router via SSH", "Cannot connect to router via SSH")
		return
	}
	defer client.Close()

	job.setStep(0, "running", "")
	fwOut := strings.TrimSpace(sshRun(client, "cat /etc/openwrt_release 2>/dev/null || echo 'not openwrt'"))
	isStockGL := false
	var glModel string
	if fwOut == "not openwrt" || fwOut == "" {
		glOut := strings.TrimSpace(sshRun(client, "cat /etc/gl-inet-release 2>/dev/null || echo ''"))
		if glOut == "" {
			jobFail(job, 0, "Router is not running OpenWrt", "Router is not running OpenWrt firmware — cannot pre-download deploy assets")
			return
		}
		isStockGL = true
		glModel, _ = parseGLInetRelease(glOut)
		glModel = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(glModel), " ", "-"))
		if glModel == "" {
			glModel = glModelFromBoard(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
		}
	} else {
		glModel = glModelFromBoard(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
	}
	job.setStep(0, "done", "")

	job.setStep(1, "running", "")
	pkgMgr, arch := "", ""
	if !isStockGL {
		pkgMgr = strings.TrimSpace(sshRun(client, "command -v apk >/dev/null 2>&1 && echo apk || echo opkg"))
		arch = detectArch(client)
	}
	urls := stageAssetURLsForArch(arch, isStockGL, glModel, pkgMgr)
	if len(urls) == 0 {
		job.setStep(1, "done", "nothing to stage")
	} else {
		job.addLog(fmt.Sprintf("Pre-downloading %d deploy asset(s)...", len(urls)))
		failed := stageAssets(job, urls)
		staged := len(urls) - len(failed)
		job.setStep(1, "done", fmt.Sprintf("%d/%d asset(s) ready", staged, len(urls)))
	}

	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()
}
