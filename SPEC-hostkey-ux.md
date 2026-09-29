# SPEC — host-key UX: show who you are trusting in the router dropdown

Repo: `OpenTollGate/tollgate-installer`
Worktree: `~/worktrees/tgi-hostkey-ux` (already created, branch `pr/hostkey-ux`, base `066ae29`)
Upstream PR target: `OpenTollGate/tollgate-installer` `main`; push the branch to the `fork` remote
(`felixfelix-bot/tollgate-installer`) as `pr/hostkey-ux`.

## Why (real incident, 2026-09-28)

An operator burned **four rounds** on `router SSH host key CHANGED`. Root cause: the trust store
keys by **IP:port only**, and the operator's network had **two different devices at
`192.168.1.1`** (their gateway, and the MT3000). The refusal is indistinguishable from a genuine
impersonation attempt, and the only remedy offered is hand-editing `~/.tollgate-known-hosts`. It
blocked the wizard entirely (no router scan, no upstream-WiFi scan).

Operator's request, verbatim: *"Can we add the host key next to the IP address so that the person
selecting the IP address from the drop down menu knows who he is trusting?"*

## Design constraints — DO NOT VIOLATE

1. **Show, don't decide. Never auto-trust.** The fingerprint a peer presents is self-reported; a
   rogue device reports its own. Only the *operator's* comparison against a known/stored value is
   evidence. So the UI must always display the **stored** fingerprint whenever it differs.
2. **Never weaken `routerHostKeyCallback`.** Keep the existing order: explicit pin first, then the
   store, then fail-safe refuse. Do not add an implicit-trust path.
3. **AGENTS.md**: this repo is a thin UI wrapper. No identity-derivation logic. Do not add
   DeriveIPv4/DeriveMAC/BIP39 here.
4. Do not change the meaning of the existing `--trust-host-key` flag or `TOLLGATE_TRUST_HOST_KEY`.
5. `~/.tollgate-known-hosts` must stay valid **OpenSSH known_hosts format** (the code already
   writes it via `knownhosts.Line`, and `verifyStoreResolvesTo` proves it with the real parser).

## What to build

### 1. Probe the presented host key without authenticating (`hostkey_ui.go`, new)

```go
// probeHostKey returns the ED25519/whatever host key `ip` presents, without
// authenticating and without sending any credential. The handshake is aborted
// on purpose: we only want the key.
func probeHostKey(ip string) (ssh.PublicKey, string /*fingerprint*/, error)
```

Use `ssh.Dial` with a `HostKeyCallback` that captures `key` and returns a sentinel error to abort.
Short timeout (~5s). Return `""` fingerprint and an error when unreachable.

### 2. Verdict against the store

```go
type hostKeyVerdict struct {
    Presented string `json:"presented"`           // SHA256:…
    Status    string `json:"status"`              // "trusted" | "new" | "changed" | "unreachable"
    Stored    string `json:"stored,omitempty"`    // the stored fingerprint when status == "changed"
    PeerMAC   string `json:"peer_mac,omitempty"`  // the MAC the operator is really trusting
}
```

Reuse the existing store check. When `knownhosts.New(store)` returns a `*knownhosts.KeyError` with
`len(KeyError.Want) > 0` → `status="changed"` and `Stored` = the fingerprint(s) from `Want`
(`ssh.FingerprintSHA256(w.Key)`). No `Want` → `"new"`. Clean → `"trusted"` — **but only if the
MAC also matches** (see 4); otherwise `"changed"` with a note.

### 3. `RouterInfo` gains a host-key report (`discover.go`)

Add to `RouterInfo`:

```go
HostKey     string `json:"host_key,omitempty"`
HostKeyStat string `json:"host_key_status,omitempty"`
HostKeyPrev string `json:"host_key_stored,omitempty"`
```

Populate in `discoverRouters()` **only for entries where SSH is open** (don't slow the scan down on
hosts with no SSH). Keep it cheap and best-effort: a failure to probe must never drop the router
from the list, and must never fail `/api/scan`.

### 4. MAC-aware verdict — the actual friction killer (`hostkey_mac.go`, new)

Add a sidecar JSON map beside the store: `<store>.mac.json`, shape
`{"94:83:c4:74:d3:04": {"fingerprint": "SHA256:…", "first_seen": "<RFC3339>", "ip_at_first_seen": "192.168.1.1"}}`.

- On a **successful trust** (pin match in `routerHostKeyCallback`, or the new endpoint in §5),
  record/replace the MAC→fingerprint mapping. The MAC comes from the ARP table (`readARPTable()`) —
  it is only ever a *display/attribution* aid, never an authorisation input.
- Verdict logic: if the presented fingerprint matches a stored mapping **for a different MAC** than
  the one currently at this IP → `"changed"`. This is what turns today's mystery into
  *"this is a different device"* instead of an ambiguous impersonation warning.
- Guard it with a mutex like `knownHostsStoreMu`. Tolerate a missing/corrupt sidecar by treating it
  as empty — never fail the deploy because of it.

### 5. New endpoint: explicit, one-click trust (`main.go`)

```
POST /api/hostkey/trust   {"ip": "192.168.1.1", "expect": "SHA256:…"}
```

- Re-probe the key **at action time** and require `sameFingerprint(expect, presented)`; if it
  differs, refuse with 409 and the new fingerprint. (Prevents a TOCTOU where the operator clicks
  Trust for the key they saw, but a different key answers.)
- On match: `rememberHostKey(ip, key)`, record the MAC mapping, clear any recorded refusal
  (`forgetHostKeyRefusal(ip)`), return the fresh verdict.
- This is the programmatic equivalent of the operator passing `--trust-host-key`. It exists so the
  operator no longer has to hand-edit a file.

### 6. UI (`index.html`)

In the router dropdown / selection area, for each router render:

- the friendly name and IP
- **the peer MAC** (labelled, e.g. `device 94:83:c4:74:d3:04`)
- **the fingerprint** (`SHA256:AdXV8yV…`, shortened in the row, full in a `title` attribute)
- a **verdict badge**: `trusted` / `new device` / `CHANGED — stored AdXV8yV…` / `unreachable`
- for `new` and `changed`, a **`[ Trust this key ]` button** calling §5 with the shown `expect`.

Wording rule: for `changed`, always name the **stored** fingerprint next to the presented one, so the
operator has something to compare. Never say "safe" or "verified" — the UI cannot know that.

## Tests (RED first — write the failing test, then the code)

Add `hostkey_ui_test.go` / `hostkey_mac_test.go`:

1. `probeHostKey` against a local `net.Listener` speaking an SSH handshake (reuse whatever pattern
   `hostkey_test.go` already uses to stand up a key) → returns the expected fingerprint.
2. Verdict: empty store → `new`; store containing the presented key → `trusted`; store containing a
   *different* key → `changed` **and** `Stored` carries the old fingerprint.
3. MAC sidecar: record a mapping, then present a **different** fingerprint for a **different** MAC
   at the same IP → `changed`. Same MAC + new fingerprint (genuine reflash) → still `changed`, but
   the reported reason names the MAC as matching, so the operator isn't told it's an impostor.
4. `POST /api/hostkey/trust` with a **wrong** `expect` → 409, and the store is **unchanged**.
5. With the right `expect` → store gains the line, verdict flips to `trusted`, and the line parses
   with the real `knownhosts` parser (`verifyStoreResolvesTo`).
6. Regression: `routerHostKeyCallback` behaviour is unchanged for pin-match, store-match and
   unknown-host (the existing `hostkey_test.go` cases must still pass untouched).

**Hermetic**: no network, no real router. Use `TOLLGATE_KNOWN_HOSTS` (already supported by
`knownHostsPath`) to point every test at a temp store — the operator's real
`~/.tollgate-known-hosts` must never be touched by a test.

## Verify before you finish

```
GOFLAGS=-mod=mod GOPATH=$HOME/gopath-local GOMODCACHE=$HOME/gopath-local/pkg/mod \
GOCACHE=$HOME/.cache/go-build-local go build ./... && \
GOFLAGS=-mod=mod GOPATH=$HOME/gopath-local GOMODCACHE=$HOME/gopath-local/pkg/mod \
GOCACHE=$HOME/.cache/go-build-local go test ./...
```

Local-only caches — never put GOPATH/GOCACHE on a network mount.

Then push `pr/hostkey-ux` to the `fork` remote and open exactly **one** upstream PR against
`OpenTollGate/tollgate-installer:main`, with the RED/GREEN evidence in the body.

## Explicitly OUT of scope

- Changing the refusal thresholds or adding an auto-trust-after-N-attempts path.
- Touching `install-and-test.sh` or the release tag guard (PR #62/#63 territory).
- Any change to the router-side module.
