package main

import "testing"

func TestSelectFeedTagForChannel(t *testing.T) {
	rels := []feedReleaseRef{
		{TagName: "v0.6.0-alpha2-pre", PublishedAt: "2026-09-13T19:36:15Z"},
		{TagName: "v0.6.0-alpha2-pre11", PublishedAt: "2026-09-21T09:50:00Z"},
		{TagName: "v0.6.0-alpha2-pre12", PublishedAt: "2026-09-21T11:00:00Z"},
		{TagName: "v0.6.0-alpha1", PublishedAt: "2026-09-08T11:42:45Z"},
		{TagName: "v0.5.0", PublishedAt: "2026-07-03T11:39:09Z"},
	}
	cases := []struct {
		channel string
		want    string
	}{
		{"alpha", "v0.6.0-alpha2-pre12"},
		{"latest", "v0.6.0-alpha2-pre12"},
		{"any", "v0.6.0-alpha2-pre12"},
		{"stable", "v0.5.0"},
		{"beta", ""}, // no beta/rc release in the fixture
		{"bogus", ""},
	}
	for _, tc := range cases {
		if got := selectFeedTagForChannel(rels, tc.channel); got != tc.want {
			t.Errorf("selectFeedTagForChannel(%q) = %q, want %q", tc.channel, got, tc.want)
		}
	}
}

func TestSelectFeedTagForChannel_FallsBackToCreatedAt(t *testing.T) {
	rels := []feedReleaseRef{
		{TagName: "v0.6.0-alpha2-pre10", CreatedAt: "2026-09-20T13:45:21Z"},
		{TagName: "v0.6.0-alpha2-pre11", CreatedAt: "2026-09-20T17:04:09Z"},
	}
	if got := selectFeedTagForChannel(rels, "alpha"); got != "v0.6.0-alpha2-pre11" {
		t.Errorf("created_at ordering: got %q, want v0.6.0-alpha2-pre11", got)
	}
}

func TestResolveFeedTagWithChannel(t *testing.T) {
	calls := 0
	fetch := func(string) string { calls++; return "v0.6.0-alpha2-pre12" }

	// Explicit tag wins and fetch is never consulted.
	calls = 0
	got := resolveFeedTagWithChannel(func(k string) string {
		if k == feedReleaseTagEnv {
			return "v0.6.0-alpha2-pre9"
		}
		return "alpha"
	}, fetch)
	if got != "v0.6.0-alpha2-pre9" {
		t.Errorf("explicit tag: got %q, want v0.6.0-alpha2-pre9", got)
	}
	if calls != 0 {
		t.Errorf("explicit tag must not call fetch (calls=%d)", calls)
	}

	// Channel resolves via fetch.
	calls = 0
	got = resolveFeedTagWithChannel(func(k string) string {
		if k == feedChannelEnv {
			return "alpha"
		}
		return ""
	}, fetch)
	if got != "v0.6.0-alpha2-pre12" {
		t.Errorf("channel resolve: got %q, want v0.6.0-alpha2-pre12", got)
	}
	if calls != 1 {
		t.Errorf("channel resolve should call fetch once (calls=%d)", calls)
	}

	// A resolver failure falls back to the compiled default.
	got = resolveFeedTagWithChannel(func(k string) string {
		if k == feedChannelEnv {
			return "alpha"
		}
		return ""
	}, func(string) string { return "" })
	if got != feedReleaseTagDefault {
		t.Errorf("failed resolve: got %q, want default %q", got, feedReleaseTagDefault)
	}

	// A malformed channel is ignored and fetch is not consulted.
	calls = 0
	got = resolveFeedTagWithChannel(func(k string) string {
		if k == feedChannelEnv {
			return "alpha; rm -rf /"
		}
		return ""
	}, fetch)
	if got != feedReleaseTagDefault {
		t.Errorf("malformed channel: got %q, want default %q", got, feedReleaseTagDefault)
	}
	if calls != 0 {
		t.Errorf("malformed channel must not call fetch (calls=%d)", calls)
	}

	// Unset everything -> default, no network.
	calls = 0
	if got := resolveFeedTagWithChannel(func(string) string { return "" }, fetch); got != feedReleaseTagDefault {
		t.Errorf("unset env: got %q, want default %q", got, feedReleaseTagDefault)
	}
	if calls != 0 {
		t.Errorf("unset env must not call fetch (calls=%d)", calls)
	}

	// nil getenv is safe.
	if got := resolveFeedTagWithChannel(nil, fetch); got != feedReleaseTagDefault {
		t.Errorf("nil getenv: got %q, want default %q", got, feedReleaseTagDefault)
	}
}
