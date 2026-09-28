package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"
)

// /api/trust-host-key is the browser wizard's trust action for the router's SSH
// host key (the C2-I-01 refusal in hostkey.go). Without it a browser-first
// operator who hits a freshly re-keyed router is dead-ended: the wizard shows the
// fingerprint and tells them to relaunch with --trust-host-key, but offers no
// way to act on what it just showed. This endpoint closes that loop.
//
// It is an affirmation, not a discovery step: it verifies that the address
// really presents the fingerprint the operator supplied, then remembers it. The
// out-of-band check (reading the fingerprint on the router's console) is the
// operator's responsibility and is what the confirm dialog in index.html asks
// them to do — this endpoint cannot tell a verified fingerprint from a
// hurriedly-clicked one, so it must never be reached without that dialog.

// trustHostKeyRequest is the JSON body for /api/trust-host-key.
type trustHostKeyRequest struct {
	IP          string `json:"ip"`
	Fingerprint string `json:"fingerprint"`
}

// looksLikeFingerprint reports whether s has the shape of an OpenSSH SHA256
// fingerprint we are willing to act on: a non-empty "SHA256:" prefix and a
// base64 body. Shape only — the value still has to match the key the router
// presents before anything is trusted.
func looksLikeFingerprint(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	body := strings.TrimPrefix(s, "SHA256:")
	if body == "" {
		return false
	}
	for _, r := range body {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '+' || r == '/' || r == '=':
		default:
			return false
		}
	}
	return true
}

// handleTrustHostKey pins the router's SSH host key for one address on the
// operator's explicit instruction. A malformed or mismatched fingerprint is
// refused and leaves the trust store untouched; no credential is ever offered.
func handleTrustHostKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	var req trustHostKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.IP) == "" {
		writeError(w, 400, "IP required")
		return
	}
	if !looksLikeFingerprint(req.Fingerprint) {
		writeError(w, 400, "a fingerprint is required, e.g. SHA256:AbCdEf…")
		return
	}

	key, err := trustHostKeyForHost(req.IP, req.Fingerprint)
	if err != nil {
		// The refusal text names the fingerprint the router actually presents,
		// so the operator can correct the value and retry without relaunching.
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"trusted":     true,
		"fingerprint": ssh.FingerprintSHA256(key),
		"store":       knownHostsPath(),
	})
}
