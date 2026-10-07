package app

import (
	"fmt"
	"net/http"
	"strings"
)

// discoverRoutersFn is the LAN-discovery seam. The SSH-dependent handlers call it
// when a dial fails, to answer the one question the plain failure leaves open: is
// the router answering somewhere else? A package variable, like the package's
// other injectable funcs (feedHTTPGet, wirelessRollback, persistableDiskAsset), so
// a test can script the LAN instead of owning one.
var discoverRoutersFn = discoverRouters

// sshFailureIsAMovedAddress reports whether the recorded failure for ip is the
// class a RENUMBERED router produces: a reachability failure (refused, timed out,
// no route). A host-key refusal or an authentication failure is deliberately NOT
// this class — the router answered, so its address is right, and going looking for
// it elsewhere would bury the real problem (an untrusted key, a wrong password)
// under a scan.
func sshFailureIsAMovedAddress(ip string) bool {
	if lastHostKeyRefusal(ip) != "" {
		return false
	}
	err, _ := lastSSHConnectError(ip)
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return false
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "network is unreachable"):
		return true
	}
	return false
}

// movedRouterMessage answers "the router is not at the address you gave me — is it
// still on this LAN?" and returns (suffix, address, found).
//
// The MAC is what makes this safe. A deploy that renumbers a router keeps its
// hardware address, so a candidate carrying the SAME MAC is the SAME router, and
// the message can say so and offer it. With a MAC in hand that matches nothing,
// nothing is offered: some router answering elsewhere is not "your router moved",
// and pointing the operator at another device is how a wrong deploy starts.
//
// Only with NO MAC (an address typed by hand) does a single answering candidate
// qualify, and then the sentence is deliberately weaker — "a router answers at" —
// because that is all that was measured.
func movedRouterMessage(want, mac string) (string, string, bool) {
	var candidates []RouterInfo
	for _, r := range discoverRoutersFn() {
		if r.IP == "" || r.IP == want {
			continue
		}
		candidates = append(candidates, r)
	}

	if mac != "" {
		var matching []RouterInfo
		for _, r := range candidates {
			if strings.EqualFold(r.MAC, mac) {
				matching = append(matching, r)
			}
		}
		if len(matching) != 1 {
			return "", "", false
		}
		where := matching[0].IP
		// The deploy that renumbers the management address is the common cause.
		return fmt.Sprintf(" — the router you were talking to now answers at %s (same MAC %s), which is where the install moved its management address: rescan and select it", where, matching[0].MAC), where, true
	}

	if len(candidates) == 1 {
		where := candidates[0].IP
		return fmt.Sprintf(" — a router answers at %s. If the install moved this router's management address, rescan and select it", where), where, true
	}
	return "", "", false
}

// writeRouterDialFailure answers a failed SSH dial on an SSH-dependent endpoint
// (WiFi scan, WiFi test, identity probe). sshConnectFailureMessage already names
// the class of failure; when that class can mean the address moved, one bounded
// LAN probe is worth it, because "check the router's address" without saying where
// the router is sends the operator hunting for a device that is right there
// (measured 2026-10-07: 10.60.32.1 held, router answering at 192.168.1.1).
//
// The status stays 502 and the `error` string stays the primary field, so an older
// index.html is unaffected; the address rides along in suggestedIp for a UI that
// can offer it.
func writeRouterDialFailure(w http.ResponseWriter, ip, mac, fallback string) {
	msg := sshConnectFailureMessage(ip, fallback)
	if sshFailureIsAMovedAddress(ip) {
		if suffix, moved, ok := movedRouterMessage(ip, mac); ok {
			writeErrorExtra(w, 502, msg+suffix, map[string]string{"suggestedIp": moved})
			return
		}
	}
	writeError(w, 502, msg)
}
