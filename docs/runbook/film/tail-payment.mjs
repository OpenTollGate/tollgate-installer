import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// Tail (payment) — the REAL TollGate captive portal served by the bench router
// (tollgate-B671). Cashu + Lightning tabs, the mint list, the token box, and the
// balance page.
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
const SPLASH = process.env.PORTAL_URL || 'http://10.173.245.1:2050/splash.html';
const BALANCE = SPLASH.replace('splash.html', 'balance.html');
fs.mkdirSync(OUT, { recursive: true });

function voiceMs(text) {
  const tmp = path.join(OUT, `tts-${Buffer.from(text).length}.wav`);
  const r = spawnSync(TTS, ['--out', tmp, '--voice', VOICE, '--text', text], { encoding: 'utf8' });
  if (r.status !== 0) return null;
  const p = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', tmp], { encoding: 'utf8' });
  const d = parseFloat((p.stdout || '').trim());
  return Number.isFinite(d) ? Math.round(d * 1000) : null;
}

const S = [
  ['This is the TollGate captive portal — the page a guest sees before they have internet. They pay in e-cash or with Lightning, and the gate opens.',
   async () => { await hover('body'); }],
  ['For e-cash, choose a mint from the list. These mints are configured on the router — the router decides which ones are accepted.',
   async () => { await click('.mint, button:nth-of-type(2)', 900); }],
  ['Paste a Cashu token into the box and press Purchase. The token is spent with the router, and the session opens for the amount of data it bought.',
   async () => { await typeSel('#cashu-token', 'cashuBqtesttokenforthedemostring00000000000000000000000000000000'); await hover('#cashu-token'); }],
  ['Prefer Lightning? Switch to the Lightning tab, enter an amount, and pay the invoice with any Lightning wallet.',
   async () => { await click('#tab-lightning', 1200); }],
  ['After the payment the balance page shows how much internet your token grants, and the remaining balance drops as you use it.',
   async () => { await goto2(); }],
];

const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));
const CAPTIONS = [
  'TollGate captive portal — pay to open the gate.',
  'Choose an accepted mint.',
  'Paste a Cashu token → Purchase.',
  'Or pay with Lightning.',
  'Balance drops as you use the internet.',
];

const videoDir = path.join(OUT, 'video', 'tail-payment');
fs.rmSync(videoDir, { recursive: true, force: true });
fs.mkdirSync(videoDir, { recursive: true });
const timeline = [];
let t0 = 0, pending = null;
const start = (say) => { if (!t0) t0 = Date.now(); pending = { start: Date.now() - t0, say }; };
const end = () => { if (pending) { pending.end = Date.now() - t0; timeline.push(pending); pending = null; } };

const CSS = `
#film-cursor{position:fixed;left:0;top:0;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;background:rgba(255,140,0,.95);border:2px solid #fff;box-shadow:0 0 14px rgba(255,140,0,.85);z-index:2147483647;pointer-events:none}
#film-ripple{position:fixed;border:3px solid rgba(255,140,0,.9);border-radius:50%;z-index:2147483646;pointer-events:none;opacity:0}
#film-caption{position:fixed;left:0;right:0;bottom:0;z-index:2147483645;background:rgba(8,12,22,.92);color:#eef3fb;font-family:system-ui,"Segoe UI",sans-serif;font-size:21px;line-height:1.45;padding:18px 32px;text-align:center;border-top:3px solid #f97316}
`;
let page, cx = 300, cy = 300;
async function inject() {
  await page.addStyleTag({ content: CSS });
  await page.evaluate(() => {
    const mk = (id) => { let e = document.getElementById(id); if (!e) { e = document.createElement('div'); e.id = id; document.body.appendChild(e); } return e; };
    mk('film-cursor'); mk('film-ripple'); mk('film-caption');
    document.addEventListener('mousemove', (e) => { const c = document.getElementById('film-cursor'); if (c) c.style.transform = `translate(${e.clientX}px,${e.clientY}px)`; window.__lx = e.clientX; window.__ly = e.clientY; }, true);
  });
}
async function cap(t) { await page.evaluate((x) => { const e = document.getElementById('film-caption'); if (e) e.textContent = x; }, t).catch(() => {}); }
async function moveTo(x, y, steps = 20) { const sx = cx, sy = cy; for (let i = 1; i <= steps; i++) { const t = i / steps, e = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2; await page.mouse.move(sx + (x - sx) * e, sy + (y - sy) * e); await page.waitForTimeout(16); } cx = x; cy = y; }
async function center(sel) { const el = page.locator(sel).first(); const box = await el.boundingBox({ timeout: 1200 }).catch(() => null); return box ? { x: box.x + box.width / 2, y: box.y + box.height / 2 } : null; }
async function click(sel, settle = 600) { const c = await center(sel); if (!c) return; await moveTo(c.x, c.y); await page.waitForTimeout(180); await page.locator(sel).first().click({ timeout: 4000 }).catch(() => {}); await page.waitForTimeout(settle); }
async function hover(sel) { const c = await center(sel); if (c) await moveTo(c.x, c.y); }
async function typeSel(sel, text) { await click(sel, 200); await page.locator(sel).first().fill('').catch(() => {}); await page.locator(sel).first().type(text, { delay: 8 }).catch(() => {}); await page.waitForTimeout(300); }
async function goto2() {
  const np = await page.context().newPage().catch(() => null);
  if (!np) return;
  page = np;
  await page.goto(BALANCE, { waitUntil: 'domcontentloaded', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1300);
  await inject();
  await hover('.brand, h1, body');
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
const ctx = await browser.newContext({ viewport: { width: 1000, height: 840 }, recordVideo: { dir: videoDir, size: { width: 1000, height: 840 } } });
page = await ctx.newPage();
t0 = Date.now();
await page.goto(SPLASH, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForTimeout(2000);
await inject();

async function scene(say, shown, action, ms) {
  start(say); await cap(shown);
  const target = Math.max(2400, (ms ?? 0) + 800); const t = Date.now(); cx = 300; cy = 300; await page.mouse.move(300, 300).catch(() => {});
  if (action) await action().catch((e) => console.log('  action-err:', e.message.slice(0, 80)));
  const left = target - (Date.now() - t); if (left > 0) await page.waitForTimeout(left).catch(() => {});
  end();
}
for (let i = 0; i < S.length; i++) await scene(S[i][0], CAPTIONS[i], S[i][1], DURS[i]);

await page.screenshot({ path: path.join(OUT, 'tail-payment.png') });
await page.evaluate(() => { const f = document.createElement('div'); f.style.cssText = 'position:fixed;inset:0;background:#000;z-index:2147483647;opacity:0;transition:opacity 1s'; document.body.appendChild(f); requestAnimationFrame(() => { f.style.opacity = '1'; }); });
await page.waitForTimeout(1000);
fs.writeFileSync(path.join(OUT, 'tail-payment.narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await ctx.close(); await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'tail-payment.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'tail-payment.webm')));
