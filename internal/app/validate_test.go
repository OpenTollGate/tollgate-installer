package app

import "testing"

func TestValidLightningAddress(t *testing.T) {
	cases := map[string]bool{
		// valid — email-shaped Lightning addresses
		"you@wallet.app":            true,
		"alice@walletofsatoshi.com": true,
		"bob@sub.domain.co":         true,
		"node1@42.42.42.42.xdd":     true,
		"  padded@wallet.app  ":     true, // surrounding whitespace trimmed
		// valid — raw LNURL (bech32-encoded, lowercase data after lnurl1 separator)
		"lnurl1dp68gurn8ghj7um5wfnz7rrfh":         true,
		"lnurl1dp68gurn8ghj7um5wfnz7um5wfnz7rrfh": true,
		// invalid — required field, must be rejected
		"":                     false, // empty (the MVP-required gate)
		"   ":                  false, // whitespace only
		"noatsign":             false,
		"@nodomain.com":        false, // empty localpart
		"nolocalpart@":         false, // empty domain
		"user@domain":          false, // no TLD / dot
		"has space@domain.com": false, // space in localpart
		"user@domain.c om":     false, // space in domain
		"user@@domain.com":     false, // double @
		// invalid — malformed LNURL shapes
		"lnurl1":                     false, // prefix only, no bech32 data
		"lnurl1xyz":                  false, // too short (< 6 data chars)
		"lnurl1Abcdef":               false, // uppercase not in bech32 charset
		"LNURL1dp68gurn8ghj7um5w":    false, // prefix must be lowercase
		"lnurl2dp68gurn8ghj7um5wfnz": false, // wrong separator version
	}
	for addr, want := range cases {
		got := validLightningAddress(addr)
		if got != want {
			t.Errorf("validLightningAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}
