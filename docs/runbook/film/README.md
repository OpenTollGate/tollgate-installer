# Installer runbook films — recording kit

The recorders and helpers behind the narrated films linked from the **install
run book** (`docs/install-runbook.md`). Everything here is deterministic and
offline where it can be; the two router-driven films need the bench routers and
are marked below.

## Pipeline

```
record (Playwright/Chrome -> silent .webm + narration.json)
  -> narrate (Kokoro TTS, mixed over the video)   -> .mp4
  -> publish (Blossom)                            -> URL
```

The narration + publish tooling is the shared **film lane** (not duplicated
here): `tools/film/narrate-film.sh`, `tools/film/kokoro-tts.py`,
`scripts/film-narrate.py`, `scripts/publish-film.py` — see
`docs/film-lane.md` in `physical-router-test-automation` (PRTA). This kit is the
installer-specific concrete lane.

## Dependencies

| Tool | Why | Notes |
|---|---|---|
| `ffmpeg` | mux + `narrate-film.sh` | any recent build |
| `node` + Playwright | the `.mjs` recorders | this kit drives **system Chrome** (`CHROME_EXE`); a bundled headless shell also works |
| `kokoro-tts` | the voice | `kokoro-onnx`, pin `kokoro-v1.0.onnx` sha256 `7d5df8…a6c5`, `voices-v1.0.bin` sha256 `bca610…bf7d` |

## Environment knobs

| Var | Used by | Meaning |
|---|---|---|
| `FILM_DIR` | all | where `tag.txt`/`ver.txt` and outputs live (default `/tmp/opencode/filmcap`) |
| `KOKORO_BIN` / `FILM_VOICE` | all | the TTS binary / voice id (default `af_heart`) |
| `CHROME_EXE` | the browser recorders | e.g. `/usr/bin/google-chrome` |
| `WIZ_URL` | `record-wizard.mjs`, `i2-wizard-gui.mjs` | the installer wizard UI (default `http://127.0.0.1:8799/`) |
| `PORTAL_URL` | `tail-payment.mjs` | the real captive portal splash |
| `BOARD_HOST` / `BOARD_IP` | `tail-admin2.mjs` | the board hostname + its address (Chrome `--host-resolver-rules`) |

`tag.txt` / `ver.txt` pin the **release under test** (e.g. `v0.6.0-rc5` /
`0.6.0_rc5`); the terminal films interpolate them.

## The films

| File | Film | Determinism |
|---|---|---|
| `terminal-film.mjs` | the S1–S5 terminal walkthroughs (headless, `.apk`, `.ipk`, offline, flash) | **scripted** — byte-reproducible, no router |
| `record-wizard.mjs` | the wizard UI walkthrough | needs the wizard UI (`WIZ_URL`) |
| `i1-film.mjs` | I1 — flash+uplink + merged `.apk`/`.ipk` install | **scripted** content mirroring the real captures in `../real/` |
| `rc-fixes.mjs` | **What rc5 + rc28 fix** — bounded mint registration under a hanging mint (#940), the board as the default face (`entry_ui`), the private-subnet recheck (#926), the installer's pending/`password_status` (rc28) | **scripted** content mirroring the real fleet-router captures (2026-10-14, rc5 / `cf7cad34`) |
| `i2-wizard-gui.mjs` | I2 — wizard GUI | needs the wizard UI |
| `i2-headless.mjs` | I2 — headless | scripted |
| `tail-payment.mjs` | tail — captive portal (Cashu + Lightning) | **real router** (`PORTAL_URL`) |
| `tail-admin2.mjs` | tail — admin board (dashboard → wallet → wifi → settings → Drain) | **real router** (`BOARD_HOST`) |

Helpers: `sercap.py` (a robust serial-console transcript for the real flash
takes), `film-check.py` (validates the published artifacts: URLs, caption
coverage, narration overlap), `mock2121.mjs` (a stand-in for the portal API when
no router is available).

### Reproduce a scripted film, start to finish

```sh
export FILM_DIR=$PWD KOKORO_BIN=$(command -v kokoro-tts) CHROME_EXE=/usr/bin/google-chrome
node i1-film.mjs                                   # -> i1-flash-manual.webm + .narration.json
TTS_ENGINE=kokoro narrate-film.sh \
  i1-flash-manual.webm i1-flash-manual.narration.json i1-flash-manual.mp4 af_heart
python3 <PRTA>/scripts/publish-film.py --run . --server https://blossom2.orangesync.tech --glob '*.mp4' --glob '*.vtt'
```

The scripted films need **no router and no network** and reproduce byte-for-byte
given the same `tag.txt`/`ver.txt`. The two **real-router** films need the bench
routers reachable (the portal + the board) and are reproducible given that
hardware; they are documented, not hermetic.

## The reel

`reel/index.html` is the narrated index of every path; edit the URLs and publish
it with the film lane.
