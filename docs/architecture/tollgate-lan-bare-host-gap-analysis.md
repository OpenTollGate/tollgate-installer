# docs: gap analysis — bare `tollgate.lan` (no scheme) lands in a web search

Analysis for **t_808829c3**. **No behaviour change in this PR** — it records what is
already decided, what is already implemented, and what remains unverified, so the fix is
chosen deliberately instead of re-litigating an ADR.

The report: users type `tollgate.lan`, get a search engine, but `https://tollgate.lan`
reaches the config UI correctly.

## What I established (code-as-read, not executed)

### 1. DNS already resolves it — this is not a DNS problem

`internal/app/deploy.go` already sets up every layer:

| layer | line | what it does |
| --- | --- | --- |
| client hosts file | `816-817` | `echo '<ip> tollgate.lan tollgate.local' >> /etc/hosts` |
| dnsmasq address | `843-847` | purges stale `tollgate.lan` entries, then `/tollgate.lan/<ip>` |
| DHCP option 6 | `849-851` | pushes the router as DNS server, so `.lan` resolves on clients |
| network domain | `856` | `network.lan.domain='lan'` |
| nodogsplash | `859-861` | `gatewaydomainname='tollgate.lan'` |

So a bare `tollgate.lan` **does** resolve to the router. The failure is in what the
browser does with the *answer*, not in getting one.

### 2. The browser-side cause

When a browser gets a resolved IP for a single-label-ish host and the **HTTP guess fails**,
Chrome and Firefox fall back to searching. `tollgate.lan` is served over **HTTPS on :443**.
If nothing answers on plain **:80**, the browser's first guess (`http://tollgate.lan`) gets
a connection refused, and depending on the browser/version and saved history the search
fallback wins.

That is consistent with the report exactly: `https://tollgate.lan` works (nothing to
guess), bare `tollgate.lan` searches (the guess is `http://`, which fails).

### 3. The fix is NOT "turn on the redirect" — there is a decided ADR

`docs/architecture/uhttpd-redirect-https-ownership-decision.md` (Status: **Decided**
2026-09-21, hardened 2026-09-26) governs `uhttpd.main.redirect_https`. It is a **derived**
value with **one rule, two writers**:

```sh
if [ -n "$cert" ] && cert_covers_router "$cert"; then
    uci set uhttpd.main.redirect_https='1'
else
    uci set uhttpd.main.redirect_https='0'
fi
```

Redirection is permitted **only** when a certificate actually covers the address the
browser used. The ADR records why, with measurements:

- **2026-09-21 (pre13):** the redirect was on and **nothing was listening on :443**. The
  operator was locked out of LuCI.
- **2026-09-26 (pre17, GL-MT3000 bench):** :443 was live but behind the OpenWrt **image
  placeholder** cert (`CN=OpenWrt`, `SAN DNS:OpenWrt`) — readable, non-empty, and covering
  neither the hostname nor the LAN IP. Every admin login began with a hard certificate
  error.

So the rule is "fail closed". **A PR that sets `redirect_https='1'` to fix the bare-host
case would re-open exactly the incident the ADR was written to close.** I am not doing that.

### 4. What is already right, and what is the real gap

`:443` is served only when the operator has provisioned a **real** identity —
`99-tollgate-setup` drives `tollgate ssl apply` (`provision_tls_identity`) rather than
inheriting the placeholder. So on a correctly provisioned router, https works and the bare
host is the only broken entry point.

The remaining gap is therefore narrow:

- **nothing answers on :80** for a router whose :443 is healthy, so the browser's first
  guess fails;
- and the ADR's rule deliberately does not open :80→:443 unless the cert validates the
  host used — which for a **bare** `tollgate.lan` is `tollgate.lan`, a name the issued
  certificate may or may not carry as a SAN.

## Candidate solutions, with costs

**A. Serve a bare :80 redirect stub, gated on cert coverage of the *hostname alias*.**
Closest to existing design: it reuses the ADR's predicate, so it cannot redirect a browser
at an identity that does not validate it. Needs the cert to carry `tollgate.lan` as a SAN —
which `cert_covers_router` already checks per the ADR ("this router's hostname, its
`<hostname>.lan` alias, or its LAN IP"). **Preferred.** It is the ADR's own rule applied to
the one entry point that has no handler.

**B. Extend the certificate to always include `tollgate.lan`.**
Then A is satisfied by construction. Cheapest to reason about, and makes the bare host work
on every provisioned router.

**C. HSTS / preload.**
Would not help: HSTS requires the browser to have *already* reached the host over https at
least once, and it does not prevent the initial search fallback. **Reject.**

**D. Captive-portal intercept of the bare host.**
nodogsplash already answers for the portal. Encoding "this exact host means the admin UI"
into the portal is a bigger behavioural change than A and would put the admin board behind
the portal's logic. **Not preferred.**

**E. Make the *browser* not search.**
Not ours to control. **Reject.**

## What this PR does and does not claim

- **Does:** records the analysis, the ADR constraint, the candidate costs, and the
  recommended path (A, with B to make it unconditional).
- **Does not:** fix the behaviour, and **does not claim it is verified** — the card's
  acceptance criterion is a **Playwright run proving a bare `tollgate.lan` entry reaches the
  config UI**. That has not been done. Per the instruction not to consider this fixed until
  Playwright proves it, this PR is explicitly **not** that proof.

## The Playwright gate (the actual acceptance, for the follow-up)

The test must, in a real browser:

1. Navigate to **`tollgate.lan`** with **no scheme** — not `https://tollgate.lan`, not
   `http://tollgate.lan`.
2. Assert the final URL is the config UI on the router, and that no search-engine URL was
   ever in the redirect chain (that is the distinguishing assertion — asserting "we ended up
   on the config UI" alone would pass even if the browser had briefly searched).
3. Run it on a router with a provisioned cert, and again on one **without** — the second
   must fail closed to a plain-HTTP error page rather than a redirect to a certificate the
   browser cannot validate.

Record the browser build, since the search fallback differs between Chrome and Firefox and
between versions.

## Next step

Pick A or A+B. Then the Playwright lane goes to `physical-router-test-automation` so it is
reproducible, with the harness note that the bare-host entry needs a real browser — `curl`
cannot observe a search fallback, so a curl-based test would pass while the user's problem
remains.
