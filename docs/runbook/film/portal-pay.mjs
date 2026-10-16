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

// The real testnet e-cash token to spend (minted fresh against the configured
// testnet mint). Read from a file so the token is not baked into the recorder.
const TOKEN = (process.env.PAYMENT_TOKEN || (fs.existsSync(path.join(OUT, 'testnet-token.txt'))
  ? fs.readFileSync(path.join(OUT, 'testnet-token.txt'), 'utf8').trim() : '')).trim();

const S = [
  ['This is the TollGate captive portal — the page a guest sees before they have internet. They pay in e-cash, and the gate opens.',
   async () => { await hover('body'); }],
  ['Choose a mint from the list. These mints are configured on the router — the router decides which ones are accepted.',
   async () => { await click('button:has(.mint-meta-address)', 900); }],
  ['Paste a Cashu token into the box. The portal reads it and shows how much internet it buys.',
   async () => { await pasteSel('#cashu-token', TOKEN); await hover('#cashu-token'); await page.waitForTimeout(1600); }],
  ['Press Purchase. The token is spent with the router, and the session opens for the amount of data it bought.',
   async () => {
     await click('.tollgate-captive-portal-method-submit button.cta', 1500);
     await page.waitForFunction(() => /Payment successful|internet access|Access purchased|You now have/i.test(document.body.innerText), { timeout: 45000 })
       .catch(() => console.log('  WARN: no success state after Purchase'));
     await page.waitForTimeout(1500);
   }],
  ['After the payment the balance page shows how much internet your token grants, and the remaining balance drops as you use it.',
   async () => { await goto2(); }],
];

const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));
const CAPTIONS = [
  'TollGate captive portal — pay to open the gate.',
  'Choose an accepted mint.',
  'Paste a Cashu token.',
  'Press Purchase — the token buys data.',
  'Balance drops as you use the internet.',
];

const videoDir = path.join(OUT, 'video', 'portal-pay');
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
// pasteSel sets the whole value in ONE input event (a paste/drop), so React
// validates once instead of on every character — and bypasses Playwright's
// actionability/stability wait, which the portal's re-render loop blocks (that
// wait stretched the beat to ~40 s).
async function pasteSel(sel, text) {
  const c = await center(sel); if (c) await moveTo(c.x, c.y);
  await page.evaluate(({ sel, text }) => {
    const el = document.querySelector(sel);
    if (!el) return;
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
    setter.call(el, text);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, { sel, text }).catch(() => {});
  await page.waitForTimeout(400);
}
async function goto2() {
  // Navigate the SAME page to the balance view. Opening a second page would
  // make Playwright record a SECOND video, and the recorder saves only the
  // first — truncating the film to the pre-balance port(ion).
  await page.goto(BALANCE, { waitUntil: 'domcontentloaded', timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1300);
  await inject();
  await hover('.brand, h1, body');
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
const ctx = await browser.newContext({ viewport: { width: 1000, height: 840 }, recordVideo: { dir: videoDir, size: { width: 1000, height: 840 } } });
page = await ctx.newPage();
t0 = Date.now();

// Register the client with NoDogSplash FIRST: a guest becomes a tracked
// client only when it hits the portal gateway (NoDogSplash's gateway port),
// which is what a real guest does via the captive redirect. Without this the
// router refuses the purchase with CU107 ("no captive-portal session for this
// device") because its pre-flight cannot find the MAC in NDS.
const GATEWAY = (() => { try { const u = new URL(SPLASH); u.port = (Number(u.port || 2051) - 1).toString(); u.pathname = '/'; u.search = ''; return u.toString(); } catch { return SPLASH.replace(':2051/', ':2050/'); } })();
await page.goto(GATEWAY, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForTimeout(1500);

await page.goto(SPLASH, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForTimeout(2000);
await inject();

// F2: NEVER record a blank page. The old bench portal (10.173.245.1:2050) is
// gone; a dead portal produced a film that was black for most of its length.
// Assert the portal actually rendered before rolling: no #cashu-token / mint
// list means we are not looking at the TollGate splash page.
const portalReady = await page.waitForSelector('#cashu-token, .mint, button', { timeout: 10000 })
  .then(() => true).catch(() => false);
if (!portalReady) {
  throw new Error('the portal did not render (no #cashu-token / mint list) — refusing to record a blank film. Is PORTAL_URL reachable? ' + SPLASH);
}

async function scene(say, shown, action, ms) {
  start(say); await cap(shown);
  const target = Math.max(2400, (ms ?? 0) + 800); const t = Date.now(); cx = 300; cy = 300; await page.mouse.move(300, 300).catch(() => {});
  if (action) await action().catch((e) => console.log('  action-err:', e.message.slice(0, 80)));
  const left = target - (Date.now() - t); if (left > 0) await page.waitForTimeout(left).catch(() => {});
  end();
}
for (let i = 0; i < S.length; i++) await scene(S[i][0], CAPTIONS[i], S[i][1], DURS[i]);

await page.screenshot({ path: path.join(OUT, 'portal-pay.png') });
// No closing fade-to-black: a fade reads as a broken/blank film and made the
// verification tooling trip on a black segment. Hold the final frame instead.
await page.waitForTimeout(1000);
fs.writeFileSync(path.join(OUT, 'portal-pay.narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await ctx.close(); await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'portal-pay.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'portal-pay.webm')));
