package main

import (
	"strings"
	"testing"
)

// TestParseARPTable covers readARPTable's line parser against the two output
// shapes the installer can actually see:
//
//   - macOS / BSD `arp -a`:
//     ? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 ifscope [ethernet]
//     BSD `arp` prints MAC octets UNPADDED when they are < 0x10, e.g.
//     `1:0:5e:0:0:fb`. Those lines must still yield a MAC.
//   - Linux `ip neigh` (the fallback when `arp` is missing):
//     192.168.1.1 dev eth0 lladdr e8:8f:6f:df:9e:11 REACHABLE
//   - Linux net-tools `arp -a`:
//     gateway (192.168.1.1) at e8:8f:6f:df:9e:11 [ether] on eth0
//
// FIXTURE PROVENANCE: these rows are DOCUMENTED-SHAPE fixtures written from
// the formats above — they are NOT captured from a real Mac (no macOS host was
// available). The padded macOS rows use the exact line supplied in the task
// that requested this test. Entries with no resolvable MAC (`(incomplete)`,
// `<incomplete>`, `FAILED`) and IPv6-only neighbours must be skipped, never
// emitted as a half pair.
func TestParseARPTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // "ip mac", in order
	}{
		{
			name: "bsd arp -a, padded mac",
			in:   "? (192.168.2.1) at e8:8f:6f:df:9e:11 on en0 ifscope [ethernet]\n",
			want: []string{"192.168.2.1 e8:8f:6f:df:9e:11"},
		},
		{
			name: "bsd arp -a, task-supplied line",
			in:   "? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 ifscope [ethernet]\n",
			want: []string{"192.168.1.1 a8:a0:92:a5:39:7a"},
		},
		{
			name: "bsd arp -a, reverse-resolved hostname",
			in:   "mac-mini.lan (192.168.1.10) at 1c:2a:b3:c4:d5:e6 on en0 ifscope [ethernet]\n",
			want: []string{"192.168.1.10 1c:2a:b3:c4:d5:e6"},
		},
		{
			name: "bsd arp -a, no ifscope qualifier",
			in:   "? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 [ethernet]\n",
			want: []string{"192.168.1.1 a8:a0:92:a5:39:7a"},
		},
		{
			// BSD arp prints each octet with %x: multicast/MACs with a
			// leading zero octet come out unpadded. Real routers commonly
			// have such MACs (e.g. 0a:11:22:...).
			name: "bsd arp -a, unpadded multicast mac",
			in:   "? (224.0.0.251) at 1:0:5e:0:0:fb on en0 ifscope permanent [ethernet]\n",
			want: []string{"224.0.0.251 1:0:5e:0:0:fb"},
		},
		{
			name: "bsd arp -a, unpadded unicast mac",
			in:   "? (169.254.10.3) at 0:1a:2b:3c:4d:5e on en0 ifscope [ethernet]\n",
			want: []string{"169.254.10.3 0:1a:2b:3c:4d:5e"},
		},
		{
			name: "bsd arp -a, hyphen-separated mac",
			in:   "? (192.168.8.1) at 94-83-c4-aa-bb-cc on en0 ifscope [ethernet]\n",
			want: []string{"192.168.8.1 94-83-c4-aa-bb-cc"},
		},
		{
			name: "bsd arp -a, incomplete entry is skipped",
			in:   "? (192.168.1.48) at (incomplete) on en0 ifscope [ethernet]\n",
			want: nil,
		},
		{
			name: "ip neigh, reachable",
			in:   "192.168.2.1 dev eth0 lladdr e8:8f:6f:df:9e:11 REACHABLE\n",
			want: []string{"192.168.2.1 e8:8f:6f:df:9e:11"},
		},
		{
			name: "ip neigh, stale with uppercase mac",
			in:   "192.168.1.1 dev wlan0 lladdr A8:A0:92:A5:39:7A STALE\n",
			want: []string{"192.168.1.1 A8:A0:92:A5:39:7A"},
		},
		{
			name: "ip neigh, FAILED has no mac",
			in:   "192.168.1.9 dev eth0 FAILED\n",
			want: nil,
		},
		{
			name: "ip neigh, DELAY",
			in:   "192.168.1.1 dev eth0 lladdr e8:8f:6f:df:9e:11 DELAY\n",
			want: []string{"192.168.1.1 e8:8f:6f:df:9e:11"},
		},
		{
			// IPv6 neighbours carry MACs too, but the scan probes IPv4 only —
			// they must be dropped rather than paired with a bogus IP.
			name: "ip neigh, ipv6 neighbour is skipped",
			in:   "fe80::1 dev eth0 lladdr 00:11:22:33:44:55 router REACHABLE\n",
			want: nil,
		},
		{
			name: "net-tools arp -a",
			in:   "gateway (192.168.1.1) at e8:8f:6f:df:9e:11 [ether] on eth0\n",
			want: []string{"192.168.1.1 e8:8f:6f:df:9e:11"},
		},
		{
			name: "net-tools arp -a, incomplete",
			in:   "? (192.168.1.7) at <incomplete> on eth0\n",
			want: nil,
		},
		{
			name: "empty output",
			in:   "",
			want: nil,
		},
		{
			name: "arp error text",
			in:   "arp: unknown host\n",
			want: nil,
		},
		{
			name: "mixed multi-line block keeps order",
			in: strings.Join([]string{
				"? (192.168.1.1) at a8:a0:92:a5:39:7a on en0 ifscope [ethernet]",
				"? (192.168.1.48) at (incomplete) on en0 ifscope [ethernet]",
				"192.168.1.10 dev eth0 lladdr 1c:2a:b3:c4:d5:e6 REACHABLE",
				"? (224.0.0.251) at 1:0:5e:0:0:fb on en0 ifscope permanent [ethernet]",
			}, "\n") + "\n",
			want: []string{
				"192.168.1.1 a8:a0:92:a5:39:7a",
				"192.168.1.10 1c:2a:b3:c4:d5:e6",
				"224.0.0.251 1:0:5e:0:0:fb",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := []string{}
			for _, e := range parseARPTable(tc.in) {
				got = append(got, e.IP+" "+e.MAC)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseARPTable(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
