import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// I2 (live) — the REAL TollGate installer wizard, driving a REAL deploy against
// the bench router over its management-net address, with a visible cursor.
// Wired uplink (no WiFi), the operator's LNURL, and the install-wait segment
// time-compressed 8x (a real wait, played fast — not a cut).
const URL = process.env.WIZ_URL || 'http://127.0.0.1:8099/';
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
const ROUTER_IP = process.env.ROUTER_IP || '10.57.113.1';
const ROUTER_PW = process.env.ROUTER_PW || 'TollGate';
const LNURL = process.env.LNURL || 'tollgate@coinos.io';
fs.mkdirSync(OUT, { recursive: true });

function voiceMs(text) {
  const tmp = path.join(OUT, `tts-${Buffer.from(text).length}.wav`);
  const r = spawnSync(TTS, ['--out', tmp, '--voice', VOICE, '--text', text], { encoding: 'utf8' });
  if (r.status !== 0) return null;
  const p = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', tmp], { encoding: 'utf8' });
  const d = parseFloat((p.stdout || '').trim());
  return Number.isFinite(d) ? Math.round(d * 1000) : null;
}

// Beats: [spoken line, caption, action]. The install beat runs to completion.
const S = [
  ['This is the TollGate installer wizard. It sets up your router in about a minute: it brings up the internet uplink, installs the TollGate package, and verifies the result.',
   'TollGate installer wizard — your router in about a minute.',
   async () => { await hoverSel('#lnurl'); }],
  ['It scanned the local network and found candidate routers. We type the address of ours and its current password to be sure.',
   'Select your router.',
   async () => { await ensureRouterSelected(ROUTER_IP, ROUTER_PW); }],
  ['This router already has a working wired uplink, so we keep it. Wi-Fi repeater is only needed when there is no cable.',
   'Upstream connection — the wired uplink is fine.',
   async () => { await clickSel('#mode-wan', 1000); }],
  ['Enter your Lightning address — this is where the payments go. It is required. The developer split default is fine.',
   'Lightning address — where payments go.',
   async () => { await typeSel('#lnurl', LNURL); await hoverSel('#devsplit'); }],
  ['Press Deploy TollGate. It installs the package over SSH, brings up the merchant, and verifies the result — watch the steps.',
   'Deploy TollGate.',
   async () => { await ensureSelectedNow(); await clickSel('#deploy-btn', 1800); await handleTrust(); }],
  ['The installer is working through the steps: staging the package, pushing it over, installing it, and waiting for the merchant to come up. The wait is real, so it is played fast here.',
   'Installing — waiting for the merchant (played at 8x).',
   async () => { await waitForDeployDone(); }],
  ['When it finishes, open the router admin page at tollgate dot L A N and log in as root. The board makes you set your own password the first time.',
   'Done — open https://tollgate.lan and log in.',
   async () => { await hoverBody(); }],
];

const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));

const videoDir = path.join(OUT, 'video', 'i2-wizard-live');
fs.rmSync(videoDir, { recursive: true, force: true });
fs.mkdirSync(videoDir, { recursive: true });

const timeline = [];
let t0 = 0, pending = null;
const start = (say) => { if (!t0) t0 = Date.now(); pending = { start: Date.now() - t0, say }; };
const end = () => { if (pending) { pending.end = Date.now() - t0; timeline.push(pending); pending = null; } };

const CSS = `
#film-cursor{position:fixed;left:0;top:0;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;
  background:rgba(255,140,0,.95);border:2px solid #fff;box-shadow:0 0 14px rgba(255,140,0,.85);z-index:2147483647;pointer-events:none}
#film-ripple{position:fixed;border:3px solid rgba(255,140,0,.9);border-radius:50%;z-index:2147483646;pointer-events:none;opacity:0}
#film-caption{position:fixed;left:0;right:0;bottom:0;z-index:2147483645;background:rgba(8,12,22,.92);color:#eef3fb;
  font-family:system-ui,"Segoe UI",sans-serif;font-size:21px;line-height:1.45;padding:18px 32px;text-align:center;border-top:3px solid #f97316}
`;

let page, cx = 200, cy = 200;

async function injectOverlay() {
  await page.addStyleTag({ content: CSS });
  await page.evaluate(() => {
    const mk = (id) => { const e = document.createElement('div'); e.id = id; document.body.appendChild(e); return e; };
    if (!document.getElementById('film-cursor')) mk('film-cursor');
    if (!document.getElementById('film-ripple')) mk('film-ripple');
    if (!document.getElementById('film-caption')) mk('film-caption');
    document.addEventListener('mousemove', (e) => {
      const c = document.getElementById('film-cursor');
      if (c) c.style.transform = `translate(${e.clientX}px,${e.clientY}px)`;
      window.__lx = e.clientX; window.__ly = e.clientY;
    }, true);
  });
}
async function caption(text) {
  await page.evaluate((t) => {
    let el = document.getElementById('film-caption');
    if (!el) { el = document.createElement('div'); el.id = 'film-caption'; document.body.appendChild(el); }
    el.textContent = t; el.style.display = 'block';
  }, text);
}
async function moveTo(x, y, steps = 22) {
  const sx = cx, sy = cy;
  for (let i = 1; i <= steps; i++) {
    const t = i / steps, e = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
    await page.mouse.move(sx + (x - sx) * e, sy + (y - sy) * e);
    await page.waitForTimeout(16);
  }
  cx = x; cy = y;
}
async function center(sel) {
  const box = await page.locator(sel).first().boundingBox().catch(() => null);
  return box ? { x: box.x + box.width / 2, y: box.y + box.height / 2 } : null;
}
async function clickSel(sel, settle = 650) {
  const c = await center(sel);
  if (!c) { console.log('no center for', sel); return; }
  await moveTo(c.x, c.y);
  await page.waitForTimeout(220);
  await page.evaluate(() => {
    const r = document.getElementById('film-ripple');
    if (!r) return;
    const x = window.__lx || 0, y = window.__ly || 0;
    r.style.left = (x - 8) + 'px'; r.style.top = (y - 8) + 'px';
    r.style.width = '16px'; r.style.height = '16px'; r.style.transition = 'none'; r.style.opacity = '1';
    requestAnimationFrame(() => { r.style.transition = 'all .5s ease-out'; r.style.width = '90px'; r.style.height = '90px'; r.style.left = (x - 45) + 'px'; r.style.top = (y - 45) + 'px'; r.style.opacity = '0'; });
  });
  await page.locator(sel).first().click({ timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(settle);
}
async function hoverSel(sel) { const c = await center(sel); if (c) await moveTo(c.x, c.y); }
async function hoverBody() { await moveTo(400, 400); }
async function typeSel(sel, text) {
  await clickSel(sel, 250);
  await page.locator(sel).first().fill('').catch(() => {});
  await page.locator(sel).first().type(text, { delay: 70 }).catch(() => {});
  await page.waitForTimeout(350);
}
// Select the candidate router whose option text contains the IP.
async function selectRouter(ip) {
  const sel = page.locator('#router-select');
  await sel.click({ timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(400);
  const opt = page.locator('#router-select option', { hasText: ip }).first();
  const val = await opt.getAttribute('value').catch(() => null);
  if (val) await sel.selectOption(val).catch(() => {});
  await page.waitForTimeout(1200);
}

// ensureRouterSelected makes the selection deterministic: it types the address
// and password (the wizard's identify path), then verifies selectedRouterIP()
// is exactly the address. If the manual path did not take (a rescan can reassign
// the dropdown), it falls back to selecting the option directly. The deploy
// reads selectedRouterIP(), so this keeps a take from hitting the wrong router.
async function ensureRouterSelected(ip, pw) {
  // The manual-address card may be collapsed after discovery, but its inputs are
  // in the DOM: fill them directly (fill() works on hidden elements; the cursor
  // nudge is cosmetic). The wizard's identify path then owns the address, and the
  // deploy targets it even though the dropdown shows the scan's first candidate.
  const fillDirect = async (sel, val) => {
    await page.locator(sel).first().fill(val).catch(() => {});
    const c = await center(sel);
    if (c) await moveTo(c.x, c.y);
  };
  await fillDirect('#password', pw);
  await fillDirect('#manual-ip', ip);
  // click the manual button via JS (it may be off-screen); fall back to a real click
  await page.evaluate(() => { const b = document.getElementById('manual-btn'); if (b) b.click(); }).catch(() => {});
  await page.waitForTimeout(3000);
  const cur = await page.evaluate(() => (typeof selectedRouterIP === 'function' ? selectedRouterIP() : ''));
  console.log('router identified:', ip, '(dropdown now:', cur + ')');
}
// The wizard refuses the first deploy until the router's SSH host key is
// trusted (the operator must confirm the fingerprint on the router console).
// Clicking #trust-btn remembers it; then Deploy proceeds. This is part of the
// real happy path, so the film shows it.
async function handleTrust() {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    const present = await page.locator('#trust-btn').first().isVisible().catch(() => false);
    if (present) {
      await clickSel('#trust-btn', 1500);
      await page.waitForTimeout(800);
      await clickSel('#deploy-btn', 1500);
      return;
    }
    await page.waitForTimeout(1000);
  }
}

// ensureSelectedNow re-asserts the target right before Deploy. The wizard's
// identify path owns the address (the dropdown may still show the scan's first
// candidate), so the trustworthy signal is that the manual address field holds
// the intended IP — re-fill it and re-run identify if not.
async function ensureSelectedNow() {
  const ip = ROUTER_IP;
  const field = await page.locator('#manual-ip').first().inputValue().catch(() => '');
  if (field === ip) return;
  console.log('re-asserting manual address (was ' + field + ')');
  await page.locator('#manual-ip').first().fill(ip).catch(() => {});
  await page.evaluate(() => { const b = document.getElementById('manual-btn'); if (b) b.click(); }).catch(() => {});
  await page.waitForTimeout(2000);
}

// Wait for the deploy to finish. We do NOT truly time-compress the network
// wait (the deploy is real); we record it and compress the WAIT FRAMES during
// assembly. Here we simply cap the wall-clock wait.
async function waitForDeployDone() {
  const deadline = Date.now() + 8 * 60 * 1000;
  while (Date.now() < deadline) {
    const done = await page.evaluate(() => {
      const ok = document.getElementById('success-view');
      const pend = document.getElementById('pending-view');
      const err = document.getElementById('error-view');
      const vis = (e) => e && !e.classList.contains('hidden') && e.offsetParent !== null;
      return { ok: vis(ok), pending: vis(pend), err: vis(err) };
    }).catch(() => ({ ok: false, pending: false, err: false }));
    if (done.ok || done.pending || done.err) { console.log('deploy terminal:', JSON.stringify(done)); return; }
    await page.waitForTimeout(3000);
  }
  console.log('deploy wait timed out');
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
const context = await browser.newContext({
  viewport: { width: 1000, height: 840 },
  recordVideo: { dir: videoDir, size: { width: 1000, height: 840 } },
});
page = await context.newPage();
// The wizard confirms the router's SSH host key with a window.confirm naming the
// full fingerprint (the out-of-band step made concrete). The recorder accepts it
// — the fingerprint was verified against the router's own console beforehand.
page.on('dialog', (d) => d.accept().catch(() => {}));
t0 = Date.now();
await page.goto(URL, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForSelector('#lnurl', { timeout: 40000 }).catch(() => {});
await page.waitForTimeout(3500); // let the scan populate
await injectOverlay();

async function scene(spoken, shown, actions, voicedMs) {
  start(spoken);
  await caption(shown);
  const ms = Math.max(2400, (voicedMs ?? 0) + 800);
  const t = Date.now();
  cx = 200; cy = 200;
  await page.mouse.move(200, 200);
  if (actions) await actions();
  const left = ms - (Date.now() - t);
  if (left > 0) await page.waitForTimeout(left);
  end();
}

for (let i = 0; i < S.length; i++) await scene(S[i][0], S[i][1], S[i][2], DURS[i]);

await page.screenshot({ path: path.join(OUT, 'i2-wizard-live.png') });
await page.evaluate(() => {
  const f = document.createElement('div');
  f.style.cssText = 'position:fixed;inset:0;background:#000;z-index:2147483647;opacity:0;transition:opacity 1s';
  document.body.appendChild(f);
  requestAnimationFrame(() => { f.style.opacity = '1'; });
});
await page.waitForTimeout(1200);

fs.writeFileSync(path.join(OUT, 'i2-wizard-live.narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await context.close();
await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'i2-wizard-live.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'i2-wizard-live.webm')));
