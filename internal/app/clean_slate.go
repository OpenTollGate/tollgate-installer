package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const cleanSlateFlashCommand = "sysupgrade -n /tmp/openwrt-sysupgrade.bin 2>&1"

var cleanSlateDownload = httpGetFile

func cleanSlateImage(model, version string) (openWrtImage, error) {
	if version != "25.12.5" && version != "24.10.8" {
		return openWrtImage{}, fmt.Errorf("unsupported OpenWrt release %q", version)
	}
	reported := strings.TrimSpace(model) // the caller's spelling, for the message
	model = glModelFromBoard(model)
	if model == "" {
		// Name what the router actually reported: the old message interpolated
		// the variable AFTER it had been emptied by the mapping, so every
		// operator saw "Unknown GL.iNet model: " with no model in it.
		return openWrtImage{}, fmt.Errorf("Unknown GL.iNet model: %s. Please update the model table in images.go or flash manually.", reported)
	}
	base, ok := glModelMap[model]
	if !ok {
		return openWrtImage{}, fmt.Errorf("Unknown GL.iNet model: %s. Please update the model table in images.go or flash manually.", model)
	}
	base.Version = version
	return base, nil
}

func validateCleanSlateConfirmation(board, confirmation string) error {
	if strings.TrimSpace(confirmation) != board {
		return fmt.Errorf("confirmation must exactly match detected board %q", board)
	}
	return nil
}

func verifyCleanSlateImage(data []byte, expected string, push func([]byte)) (string, error) {
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(strings.TrimSpace(expected), actual) {
		return actual, fmt.Errorf("OpenWrt image sha256 mismatch: expected sha256 %s, actual sha256 %s", expected, actual)
	}
	if push != nil {
		push(data)
	}
	return actual, nil
}

func cleanSlateSHA256(sumData []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(sumData), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[1], "*") == filename {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("sha256sums has no entry for %s", filename)
}

// cleanSlateDetectedBoard reads the router's OpenWrt board name and resolves it
// to the sysupgrade image for the REQUESTED OpenWrt release.
//
// It returns the OpenWrt board string — the identity the operator must type to
// confirm — and the resolved image. Callers MUST use the returned image and
// must NOT feed the board string back into cleanSlateImage: that string is an
// OpenWrt identity ("glinet_gl-mt3000"), not a glModelMap key ("gl-mt3000"), so
// re-parsing it as a model key failed the whole inspect step for EVERY router.
func cleanSlateDetectedBoard(client *ssh.Client, version string) (string, openWrtImage, error) {
	raw := strings.TrimSpace(sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null"))
	img, err := cleanSlateImage(raw, version)
	if err != nil {
		return "", openWrtImage{}, err
	}
	return img.Board, img, nil
}

func cleanSlateInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req cleanSlateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.IP == "" {
		writeError(w, http.StatusBadRequest, "IP required")
		return
	}
	client := sshConnect(req.IP, req.Password)
	if client == nil {
		writeError(w, http.StatusBadGateway, "could not connect to router")
		return
	}
	defer client.Close()
	board, img, err := cleanSlateDetectedBoard(client, req.Version)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sumData, err := cleanSlateDownload(cleanSlateSumsURL(img))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to download OpenWrt sha256sums: "+err.Error())
		return
	}
	expected, err := cleanSlateSHA256(sumData, imgFilename(img))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"board": board, "image_url": img.URL(), "sha256": expected})
}

type cleanSlateRequest struct {
	IP           string `json:"ip"`
	Password     string `json:"password"`
	Version      string `json:"version"`
	Confirmation string `json:"confirmation"`
}

func imgFilename(img openWrtImage) string {
	url := img.URL()
	return url[strings.LastIndex(url, "/")+1:]
}

func cleanSlateSumsURL(img openWrtImage) string {
	return fmt.Sprintf("https://downloads.openwrt.org/releases/%s/targets/%s/%s/sha256sums", img.Version, img.Target, img.Subtarget)
}

func cleanSlateFlash(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req cleanSlateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.IP == "" || req.Version == "" {
		writeError(w, http.StatusBadRequest, "IP and version required")
		return
	}
	// Validate identity and typed confirmation synchronously, before creating a job.
	client := sshConnect(req.IP, req.Password)
	if client == nil {
		writeError(w, http.StatusBadGateway, "could not connect to router")
		return
	}
	board, _, err := cleanSlateDetectedBoard(client, req.Version)
	client.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCleanSlateConfirmation(board, req.Confirmation); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	jobID, err := newJobID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot generate a job id")
		return
	}
	job := newCleanSlateJob(req.IP)
	jobsMutex.Lock()
	jobs[jobID] = job
	jobsMutex.Unlock()
	go runCleanSlate(job, req)
	startJobWatchdog(job, jobStallTimeout)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"job_id": jobID})
}

func runCleanSlate(job *Job, req cleanSlateRequest) {
	client := sshConnect(req.IP, req.Password)
	if client == nil {
		jobFail(job, 0, "router connection failed", "could not connect to router")
		return
	}
	defer client.Close()
	job.setStep(0, "running", "resolving board")
	board, img, err := cleanSlateDetectedBoard(client, req.Version)
	if err != nil {
		jobFail(job, 0, "identity refused", err.Error())
		return
	}
	if err := validateCleanSlateConfirmation(board, req.Confirmation); err != nil {
		jobFail(job, 0, "confirmation refused", err.Error())
		return
	}
	job.setStep(0, "done", board)
	job.setStep(1, "running", "downloading and verifying image")
	data, err := cleanSlateDownload(img.URL())
	if err != nil {
		jobFail(job, 1, "download failed", err.Error())
		return
	}
	sumData, err := cleanSlateDownload(cleanSlateSumsURL(img))
	if err != nil {
		jobFail(job, 1, "checksum download failed", err.Error())
		return
	}
	expected, err := cleanSlateSHA256(sumData, imgFilename(img))
	if err != nil {
		jobFail(job, 1, "checksum unavailable", err.Error())
		return
	}
	if _, err := verifyCleanSlateImage(data, expected, nil); err != nil {
		jobFail(job, 1, "integrity check failed", err.Error())
		return
	}
	job.addLog("Verified OpenWrt image sha256: " + expected)
	job.setStep(1, "done", "sha256 verified")
	job.setStep(2, "running", "flashing vanilla OpenWrt")
	pushOut := sshUploadPipe(client, data, "cat > /tmp/openwrt-sysupgrade.bin && echo PUSH_OK")
	if !strings.Contains(pushOut, "PUSH_OK") {
		jobFail(job, 2, "image push failed", "failed to push OpenWrt image to router")
		return
	}
	upgradeOut := sshRun(client, cleanSlateFlashCommand)
	// A successful `sysupgrade -n` kills the SSH session mid-command, after
	// which ubus prints "Command failed: ubus call system sysupgrade". Matching
	// bare "failed"/"error" substrings therefore reports a GOOD upgrade as a
	// failure — exactly what the deploy path did until it was fixed for a real
	// MT6000. Use the same shared predicate here.
	if sysupgradeFatal(upgradeOut) {
		jobFail(job, 2, "sysupgrade failed", parseSysupgradeError(upgradeOut))
		return
	}
	job.addLog("Router rebooting; waiting for vanilla OpenWrt to return")
	client.Close()
	newIP, newClient, err := waitForRouterAfterFlash(req.IP, req.Password, 3*time.Minute)
	if err != nil {
		jobFail(job, 2, "router did not return", err.Error())
		return
	}
	newClient.Close()
	job.setStep(2, "done", "OpenWrt "+img.Version+" flashed on "+board+"; router back at "+newIP)
	job.mu.Lock()
	job.Status = "done"
	job.mu.Unlock()
}

func newCleanSlateJob(ip string) *Job {
	return &Job{IP: ip, Status: "running", Steps: []Step{{Name: "identity", Desc: "Confirm router identity", Status: "pending"}, {Name: "image", Desc: "Verify OpenWrt image", Status: "pending"}, {Name: "flash", Desc: "Flash vanilla OpenWrt", Status: "pending"}}, Log: []LogEntry{}, stageCache: map[string][]byte{}, lastActivity: time.Now()}
}
