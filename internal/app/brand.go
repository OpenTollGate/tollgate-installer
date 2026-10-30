package app

// Brand support for the installer.
//
// WHY THIS LIVE-CYCLES THROUGH THE UPSTREAM INSTALLER RATHER THAN A WRAPPER
//
// The installer mints every branded name from a literal (`deploy.go`: SSID
// "!TollGate-"+code, hostname "tollgate-"+code, gatewaydomainname 'tollgate.lan') and
// reads NO brand input at all. So a white-label deploy is impossible from outside: a
// wrapper can export whatever it likes and the router still comes out TollGate-branded.
//
// Rather than fork the naming logic into a wrapper repo (the drift this cleanup exists
// to remove), the brand becomes a first-class INPUT here, and the wrapper's only job is
// to supply it. The router-side writer is deliberately narrow: it writes ONE token, and
// the module's load_brand() owns every derived name.
//
// NAMING OWNERSHIP
//
//	installer   validates a single token and writes it (this file)
//	module      reads the token and derives hostname / SSID / NDS gatewayname
//
// The installer never composes a display spelling from a literal, and never writes an
// SSID or hostname in the brand path.

import (
	"regexp"
	"strings"
)

// brandTokenPattern is the same rule the module's load_brand() enforces: one
// alphanumeric token. Enforced on BOTH sides on purpose — the installer must never
// hand the router something load_brand() would coerce away, because that would look
// like a successful brand that silently became the default.
var brandTokenPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// defaultBrand is this project's own brand name. It is the value load_brand() coerces
// to, and the value the installer treats as "no brand supplied" so that an unbranded
// deploy stays byte-identical to today.
const defaultBrand = "tollgate"

// sanitizeBrandToken lowercases and validates a brand token, returning "" when the
// input is not a usable brand (empty, punctuation, whitespace, a path, shell
// metacharacters, non-ASCII). "" means "no brand", which every caller treats as "leave
// the default alone" — never as an error, because an absent brand is the normal case
// for every upstream user.
func sanitizeBrandToken(in string) string {
	t := strings.ToLower(strings.TrimSpace(in))
	if t == "" || !brandTokenPattern.MatchString(t) {
		return ""
	}
	if t == defaultBrand {
		// The default is spelled "no brand" so the unbranded path stays byte-identical.
		return ""
	}
	return t
}

// brandDisplaySpelling is the SSID-facing form of a token: first letter capitalised,
// the same rule load_brand() applies to BRAND_HOSTNAME. Kept here so the installer and
// the module agree on what a brand looks like without sharing code.
func brandDisplaySpelling(token string) string {
	if token == "" {
		return "TollGate"
	}
	return strings.ToUpper(token[:1]) + token[1:]
}

// brandedDeviceIdentity builds the identity with a brand applied. An empty token
// reproduces the historical literal exactly, so the unbranded path cannot drift.
func brandedDeviceIdentity(code, token string) deviceIdentity {
	t := sanitizeBrandToken(token)
	if t == "" {
		return deviceIdentity{
			Code:        code,
			Hostname:    "tollgate-" + code,
			SSID:        "!TollGate-" + code,
			PrivateSSID: privateSSIDPrefix + "-" + code,
		}
	}
	return deviceIdentity{
		Code:        code,
		Hostname:    t + "-" + code,
		SSID:        "!" + brandDisplaySpelling(t) + "-" + code,
		PrivateSSID: privateSSIDPrefix + "-" + code,
	}
}

// brandTokenCommands returns the router-side commands that record the brand token, or
// nil when there is no brand to record.
//
// It writes ONLY the token file. Every derived name belongs to the module's
// load_brand(), so this must not contain an SSID, hostname or gatewayname — asserted by
// TestBrandTokenWriterIsEmittedBeforePackaging.
//
// It is emitted at the PRE-PACKAGE step (see deploy step 4), not with the branding
// commands at step 7: the module's load_brand() is one-shot and runs in the package
// postinst, so a step-7 write would be inert.
func brandTokenCommands(token string) []string {
	t := sanitizeBrandToken(token)
	if t == "" {
		return nil
	}
	return []string{
		"mkdir -p /etc/tollgate",
		"printf '%s' " + shellQuoteSingle(t) + " > /etc/tollgate/brand",
	}
}
