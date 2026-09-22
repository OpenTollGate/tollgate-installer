package main

import (
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type RouterInfo struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Vendor   string `json:"vendor"`
	Model    string `json:"model"`
	Firmware string `json:"firmware"`
	// Name is a friendly display label (e.g. "GL-MT3000") derived from
	// model/vendor/OUI. The UI shows it instead of a bare IP so the operator
	// can tell devices apart. Best-effort: never affects routing or deploy.
	Name     string `json:"name"`
	SSH      bool   `json:"ssh_open"`
	HTTPPort int    `json:"http_port,omitempty"`
}

// discoverRouters scans the local subnet for OpenWrt routers.
func discoverRouters() []RouterInfo {
	var found []RouterInfo
	seen := make(map[string]bool)

	// 1. Scan ARP table for known router MACs
	arpEntries := readARPTable()

	// 2. Always check common router gateway IPs.
	// Include 192.168.21.1 (GL.iNet default WiFi-AP subnet seen on Felix's
	// T14Gen5) so it's tried early, but still AFTER the classic wired subnets.
	commonIPs := []string{
		"192.168.1.1", "192.168.8.1", "192.168.0.1", "192.168.2.1",
		"10.47.41.1", "192.168.21.1",
	}

	// Merge: common router IPs FIRST, then ARP entries.
	// On some laptops (e.g. Felix's T14Gen5) the router shows up in the ARP
	// table as 192.168.21.1 (the WiFi AP address) and would be listed before
	// the wired gateway 192.168.1.1. By checking commonIPs first we ensure
	// the wired/LAN address — which is what the wizard needs for SSH — is
	// probed before any WiFi-AP address from the ARP table.
	var candidates []string
	for _, ip := range commonIPs {
		if !seen[ip] {
			seen[ip] = true
			candidates = append(candidates, ip)
		}
	}
	for _, e := range arpEntries {
		if !seen[e.IP] {
			seen[e.IP] = true
			candidates = append(candidates, e.IP)
		}
	}

	for _, ip := range candidates {
		info := probeRouter(ip)
		if info.SSH || info.HTTPPort > 0 {
			// Enrich with ARP MAC if available
			for _, a := range arpEntries {
				if a.IP == ip && info.MAC == "" {
					info.MAC = a.MAC
				}
			}
			// Recompute the friendly name now the MAC is known (the OUI
			// fallback in friendlyRouterName needs it).
			info.Name = friendlyRouterName(info)
			found = append(found, info)
		}
	}
	return found
}

type arpEntry struct {
	IP  string
	MAC string
}

func readARPTable() []arpEntry {
	out, err := exec.Command("arp", "-a").Output()
	if err != nil {
		// Try ip neigh as fallback
		out, err = exec.Command("ip", "neigh").Output()
		if err != nil {
			return nil
		}
	}
	var entries []arpEntry
	lines := strings.Split(string(out), "\n")
	macRe := regexp.MustCompile(`([0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}`)
	ipRe := regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)`)
	for _, line := range lines {
		macMatch := macRe.FindString(line)
		ipMatch := ipRe.FindString(line)
		if macMatch != "" && ipMatch != "" {
			entries = append(entries, arpEntry{IP: ipMatch, MAC: macMatch})
		}
	}
	return entries
}

// probeRouter probes ip with passwordless SSH (the LAN scan path).
func probeRouter(ip string) RouterInfo { return probeRouterWithPassword(ip, "") }

// probeRouterWithPassword probes ip, using password when non-empty so a
// password-protected router can still be identified (the wizard calls this
// after the operator enters the root password, via /api/identify). The
// friendly Name is computed here; discoverRouters recomputes it once the MAC
// has been enriched so the OUI fallback can apply.
func probeRouterWithPassword(ip, password string) RouterInfo {
	info := RouterInfo{IP: ip, Vendor: "unknown", Model: "unknown", Firmware: "unknown"}

	// Check SSH port 22
	info.SSH = tcpProbe(ip, 22, 2*time.Second)

	// Check HTTP ports
	for _, port := range []int{80, 443, 8080} {
		if tcpProbe(ip, port, 1*time.Second) {
			info.HTTPPort = port
			break
		}
	}

	// Try SSH-based identification (password when supplied, else passwordless)
	if info.SSH {
		if fw, vendor, model := sshIdentify(ip, password); fw != "" {
			info.Firmware = fw
			info.Vendor = vendor
			info.Model = model
		}
	}

	info.Name = friendlyRouterName(info)
	return info
}

func tcpProbe(ip string, port int, timeout time.Duration) bool {
	// net.JoinHostPort correctly brackets IPv6 addresses per RFC 3986.
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// parseGLInetRelease parses the contents of /etc/gl-inet-release from stock
// GL.iNet firmware. The format is NOT guaranteed to be stable: it may be
// key=value lines (model=..., version=...) OR a single bare product string.
// Handle both defensively. Returns the model and version (version may be
// empty if the file carries no version line).
func parseGLInetRelease(glOut string) (model, version string) {
	lines := strings.Split(glOut, "\n")

	// First pass: extract model/version from key=value lines.
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.Contains(low, "model") || strings.Contains(low, "product") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) > 1 {
				model = strings.Trim(parts[1], " 	\"'")
			}
		}
		if strings.Contains(low, "version") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) > 1 {
				version = strings.Trim(parts[1], " 	\"'")
			}
		}
	}

	// If no model was found via key=value, fall back to a bare product string:
	// the first non-empty line that is not itself a key=value line. This covers
	// the single-product-string format (e.g. "GL-MT3000") and mixed formats.
	if model == "" {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "=") {
				continue
			}
			model = line
			break
		}
	}
	return model, version
}

// sshIdentify tries passwordless SSH to read firmware info.
func sshIdentify(ip, password string) (firmware, vendor, model string) {
	client := sshConnect(ip, password)
	if client == nil {
		return
	}
	defer client.Close()

	out := sshRun(client, "cat /etc/openwrt_release 2>/dev/null")
	if strings.Contains(out, "OpenWrt") || strings.Contains(out, "openwrt") {
		vendor = "OpenWrt"
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "DISTRIB_DESCRIPTION") {
				parts := strings.SplitN(line, "'", 2)
				if len(parts) > 1 {
					firmware = strings.Trim(parts[1], "'")
				}
			}
		}
		if firmware == "" {
			firmware = "OpenWrt"
		}
	}

	// If not OpenWrt, check for stock GL.iNet firmware. The /etc/gl-inet-release
	// format is NOT verified against a real device (no GL.iNet reachable from
	// this network at implementation time) — the parser is defensive and handles
	// both key=value lines and a bare product string.
	if vendor == "" {
		glOut := sshRun(client, "cat /etc/gl-inet-release 2>/dev/null || echo ''")
		glOut = strings.TrimSpace(glOut)
		if glOut != "" {
			vendor = "GL.iNet"
			firmware = "stock"
			glModel, glVersion := parseGLInetRelease(glOut)
			if glModel != "" {
				model = glModel
			}
			if glVersion != "" {
				firmware = "GL.iNet " + glVersion
			}
			// Normalize: lowercase, spaces -> dashes (e.g. "GL-MT3000" -> "gl-mt3000").
			model = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(model), " ", "-"))
		}
	}

	// Try to get model from OpenWrt sysinfo (only if not already identified
	// from a GL.iNet release file, which takes precedence).
	if model == "" {
		modelOut := sshRun(client, "cat /tmp/sysinfo/board_name 2>/dev/null || cat /tmp/sysinfo/model 2>/dev/null")
		modelOut = strings.TrimSpace(modelOut)
		if modelOut != "" {
			model = modelOut
		}
	}

	return
}

// prettyModel turns an OpenWrt board_name / GL release model into a display
// name: "glinet,gl-mt3000" -> "GL-MT3000", "gl-mt3000" -> "GL-MT3000"; other
// strings are returned unchanged.
func prettyModel(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, ","); i >= 0 {
		model = strings.TrimSpace(model[i+1:])
	}
	if model == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(model), "GL-") {
		return strings.ToUpper(model)
	}
	return model
}

// friendlyRouterName builds a human label for a discovered device: the
// prettified model when known, else the vendor, else an OUI-derived vendor,
// else "Router". Never returns the raw "unknown"/"unknown unknown" pair.
func friendlyRouterName(info RouterInfo) string {
	model := strings.TrimSpace(info.Model)
	vendor := strings.TrimSpace(info.Vendor)
	if model != "" && model != "unknown" {
		if p := prettyModel(model); p != "" {
			return p
		}
	}
	if vendor != "" && vendor != "unknown" {
		return vendor + " router"
	}
	if v := ouiVendor(info.MAC); v != "" {
		return v + " device"
	}
	return "Router"
}

// ouiVendors is a small best-effort MAC-prefix table used only to make the
// dropdown friendlier when SSH identification is unavailable. A miss (or a
// wrong guess) only affects the display label — never routing or deploy.
var ouiVendors = map[string]string{
	"94:83:C4": "GL.iNet",
	"E4:95:6E": "GL.iNet",
	"52:54:00": "QEMU",
	"00:1C:42": "Parallels",
}

// ouiVendor returns the vendor for a MAC's OUI (first three octets), or "".
func ouiVendor(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	if len(mac) < 8 {
		return ""
	}
	return ouiVendors[mac[:8]]
}
