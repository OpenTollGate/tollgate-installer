package app

import "testing"

func TestClamp(t *testing.T) {
	cases := []struct {
		n, lo, hi, want int
	}{
		// dev split range (0-50)
		{n: 10, lo: 0, hi: 50, want: 10}, // default, unchanged
		{n: 0, lo: 0, hi: 50, want: 0},   // lower bound
		{n: 50, lo: 0, hi: 50, want: 50}, // upper bound
		{n: -5, lo: 0, hi: 50, want: 0},  // below → clamp to lo
		{n: 99, lo: 0, hi: 50, want: 50}, // above → clamp to hi
		// margin range (0-100)
		{n: 0, lo: 0, hi: 100, want: 0}, // default margin
		{n: 100, lo: 0, hi: 100, want: 100},
		{n: 250, lo: 0, hi: 100, want: 100},
	}
	for _, c := range cases {
		got := clamp(c.n, c.lo, c.hi)
		if got != c.want {
			t.Errorf("clamp(%d, %d, %d) = %d, want %d", c.n, c.lo, c.hi, got, c.want)
		}
	}
}
