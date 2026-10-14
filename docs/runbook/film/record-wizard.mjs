import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// Record the real installer wizard, driving its controls with a visible
// cursor. Narration runs over the live UI (a lower-third caption) and is paced
// to each line's synthesized length, so the frame stays on screen while it is
// spoken and a line never crosses into the next one.
const URL = process.env.WIZ_URL || 'http://127.0.0.1:8799/';
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || 'kokoro-tts';
fs.mkdirSync(OUT, { recursive: true });

function voiceMs(text) {
  const tmp = path.join(OUT, `tts-${Buffer.from(text).length}.wav`);
  const r = spawnSync(TTS, ['--out', tmp, '--voice', VOICE, '--text', text], { encoding: 'utf8' });
  if (r.status !== 0) return null;
  const p = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', tmp], { encoding: 'utf8' });
  const d = parseFloat((p.stdout || '').trim());
  return Number.isFinite(d) ? Math.round(d * 1000) : null;
}

// Scenes: [spoken (also the caption text), optional action]. The spoken line
// says "L A N" so the voice spells it out; the caption keeps "tollgate.lan".
const S = [
  ['This is the TollGate installer. It sets up your router in about a minute: it flashes OpenWrt, brings up the internet uplink, installs the TollGate package, and verifies the result.',
   async () => { await hoverSel('#lnurl'); }],
  ['It found your router on the local network. Pick it from the list to continue.',
   async () => { await clickSel('#router-select'); }],
  ['Choose how the router reaches the internet: Wi-Fi repeater to join a wireless network, or the Ethernet cable.',
   async () => { await clickSel('#mode-sta', 1100); await clickSel('#mode-wan'); }],
  ['Enter your Lightning address. This is where payments go, and it is required.',
   async () => { await typeSel('#lnurl', 'you@wallet.app'); }],
  ['Press Deploy TollGate. It flashes the router, brings up the uplink, installs the package, and checks it.',
   async () => { await hoverSel('#deploy-btn'); }],
  ["When it finishes, open the router's admin page at tollgate dot L A N. It asks you to log in. The password is TollGate by default, and the board makes you change it the first time.",
   async () => { await hoverSel('#lnurl'); }],
];

// Synthesize every line BEFORE the camera rolls, or the synth latency lands in
// the video as dead frames.
const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));

const CAPTIONS = [
  'TollGate installer — sets up your router in about a minute.',
  'Select your router.',
  'Upstream connection — Wi-Fi repeater or Ethernet cable.',
  'Lightning address — required.',
  'Deploy TollGate.',
  'https://tollgate.lan — log in; default password TollGate.',
];

const videoDir = path.join(OUT, 'video');
fs.rmSync(videoDir, { recursive: true, force: true });
fs.mkdirSync(videoDir, { recursive: true });

const timeline = [];
let t0 = 0;
let pending = null;
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
  if (!c) return;
  await moveTo(c.x, c.y);
  await page.waitForTimeout(220);
  await page.evaluate(() => {
    const r = document.getElementById('film-ripple');
    if (!r) return;
    const x = window.__lx || 0, y = window.__ly || 0;
    r.style.left = (x - 8) + 'px'; r.style.top = (y - 8) + 'px'; r.style.width = '16px'; r.style.height = '16px'; r.style.transition = 'none'; r.style.opacity = '1';
    requestAnimationFrame(() => { r.style.transition = 'all .5s ease-out'; r.style.width = '90px'; r.style.height = '90px'; r.style.left = (x - 45) + 'px'; r.style.top = (y - 45) + 'px'; r.style.opacity = '0'; });
  });
  await page.locator(sel).first().click({ timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(settle);
}
async function hoverSel(sel) { const c = await center(sel); if (c) await moveTo(c.x, c.y); }
async function typeSel(sel, text) {
  await clickSel(sel, 250);
  await page.locator(sel).first().fill('').catch(() => {});
  await page.locator(sel).first().type(text, { delay: 70 }).catch(() => {});
  await page.waitForTimeout(350);
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
const context = await browser.newContext({
  viewport: { width: 1000, height: 840 },
  recordVideo: { dir: videoDir, size: { width: 1000, height: 840 } },
});
page = await context.newPage();
t0 = Date.now();
await page.goto(URL, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForSelector('#lnurl', { timeout: 40000 }).catch(() => {});
await page.waitForTimeout(1500);
await injectOverlay();

async function scene(spoken, shown, actions, voicedMs) {
  start(spoken);
  await caption(shown);
  const ms = Math.max(2200, (voicedMs ?? 0) + 700);
  const t = Date.now();
  cx = 200; cy = 200;
  await page.mouse.move(200, 200);
  if (actions) await actions();
  const left = ms - (Date.now() - t);
  if (left > 0) await page.waitForTimeout(left);
  end();
}

for (let i = 0; i < S.length; i++) {
  await scene(S[i][0], CAPTIONS[i], S[i][1], DURS[i]);
}

await page.screenshot({ path: path.join(OUT, 'wizard.png') });
await page.evaluate(() => {
  const f = document.createElement('div');
  f.style.cssText = 'position:fixed;inset:0;background:#000;z-index:2147483647;opacity:0;transition:opacity 1s';
  document.body.appendChild(f);
  requestAnimationFrame(() => { f.style.opacity = '1'; });
});
await page.waitForTimeout(1200);

fs.writeFileSync(path.join(OUT, 'narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await context.close();
await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'wizard.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'wizard.webm')));
