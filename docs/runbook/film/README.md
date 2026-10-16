# Installer runbook films — recording kit

The recorders and helpers behind the narrated films linked from the **install
run book** (`docs/install-runbook.md`). Three films, all happy-path, all
**router-driven** (they run against a real box on its management net).

## Pipeline

```
record (Playwright/Chrome -> silent .webm + narration.json)
  -> [optional] compress-windows.py (speed up a long wait, remap the timeline)
  -> narrate (Kokoro TTS, mixed over the video)   -> .mp4
  -> verify (film-verify.py: blackdetect + OCR)   -> pass/fail
  -> publish (Blossom)                            -> URL
```

## The three films

| File | Film | Needs |
|---|---|---|
| `i2-wizard-live.mjs` | **install** — the real installer wizard driving a **real deploy** (router picked from the dropdown, LNURL `tollgate@coinos.io`, wired uplink) | the installer binary (`:8099`) + a router on its management net |
| `portal-pay.mjs` | **captive portal** — Cashu-only payment with a real e-cash token; the gate opens | a real guest client (on `br-lan`, so NoDogSplash has a session) |
| `configui.mjs` | **configUI** — board login → dashboard → **wallet (balance)** → **Drain** → wifi → settings → LuCI | the board (`https://<mgmt-ip>/`) |

Helpers: `sercap.py` (serial-console transcript), `compress-windows.py`
(time-compress a long segment and remap `narration.json`), `film-verify.py`
(content gate: no black frames, no error strings on screen, required evidence
present), `reel/index.html` (the narrated index).

## Environment knobs

| Var | Used by | Meaning |
|---|---|---|
| `FILM_DIR` | all | where outputs live |
| `KOKORO_BIN` / `FILM_VOICE` | all | TTS binary / voice (default `af_heart`) |
| `CHROME_EXE` | all | system Chrome path (e.g. `/usr/bin/google-chrome`) |
| `WIZ_URL` | `i2-wizard-live.mjs` | the installer wizard UI (default `http://127.0.0.1:8099/`) |
| `ROUTER_IP` / `ROUTER_PW` | `i2-wizard-live.mjs` | the target router's management address + current root password |
| `LNURL` | `i2-wizard-live.mjs` | the Lightning address to enter |
| `PORTAL_URL` | `portal-pay.mjs` | the captive portal splash (e.g. `http://<mgmt-ip>:2051/splash.html`) |
| `BOARD_HOST` / `BOARD_IP` / `BOARD_PW_NEW` | `configui.mjs` | the board hostname, its address, and the first-run password to set |

## Rules (learned the hard way)

- **Read the recorder before wiring hardware.** If it never does
  `page.goto(<router>)` it needs no router.
- **Run router-driven films on a host on the router's LAN/management side**
  (for the fleet, the gateway). Never SSH-tunnel ports to a WAN-side laptop and
  never add temporary firewall accepts. See the PRTA
  [bench-lab runbook](https://github.com/OpenTollGate/physical-router-test-automation/blob/main/docs/RUNBOOK-bench-lab.md#reaching-a-tollgate-installed-box-post-install-address).
- **Never record a fade-to-black ending** and never concat two clips (the seam
  is black) — both trip `film-verify.py` and read as a broken film.
- **React controlled inputs** (`#password` on the board, the token box on the
  portal) must be set with the **native setter + an input event**, or a
  `.fill()`/`.type()` leaves them empty on camera.
- **The captive portal needs a real guest session.** Register the client with
  NoDogSplash first (load the gateway port), or the purchase is refused with
  `#CU107`.
- **Run `film-verify.py` before publishing.** It is the gate that would have
  caught a film showing `#LN004`, a black stretch, or a placeholder address.

## Verify

```sh
python3 docs/runbook/film/film-verify.py <film.mp4> [...]
# or: make -C docs/runbook/film film-verify FILMS="a.mp4 b.mp4"
```
