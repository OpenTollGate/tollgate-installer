import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// ConfigUI — the REAL TollGate board (the default face) at its hostname entry.
// dashboard -> wallet (balance earned from the portal payment) -> Drain ->
// wifi -> settings -> the LuCI cross-link button.
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
const HOST = process.env.BOARD_HOST || 'tollgate-9bd4.lan';
const IP = process.env.BOARD_IP || '10.110.223.1';
const PW = process.env.BOARD_PW || 'TollGate';
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
  ['This is the TollGate board — the default face of the router. Open the router hostname, tollgate dash four digits dot L A N, and this is what answers; not LuCI.',
   async () => { await hover('body'); }],
  ['Log in as root. On first use the board has you choose your own password; from then on you sign in with it.',
   async () => { await loginBoard(); }],
  ['The dashboard is the router at a glance: system, network, and the live session.',
   async () => { await route('dashboard'); }],
  ['The Wallet holds what the router earns. This balance is the e-cash a guest just paid through the captive portal — it is held as sats against the accepted mint.',
   async () => { await route('wallet'); await hover('body'); }],
  ['Drain withdraws the balance as a Cashu token you can spend or move elsewhere.',
   async () => { await clickText('Drain All Funds'); await page.waitForTimeout(900); await clickText('Confirm Drain'); await page.waitForTimeout(3000); }],
  ['Wi-Fi shows the guest and private networks.',
   async () => { await route('wifi'); }],
  ['Settings holds the pricing and the profit split — the margin factor, the price per step, and the accepted mints. Change these to your own defaults.',
   async () => { await route('settings'); await hover('body'); }],
  ['And if you need the full OpenWrt options, the board has a button for LuCI right in the header.',
   async () => { await hover('a:has-text("LuCI")'); await hover('button:has-text("LuCI")'); }],
];

const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));
const CAPTIONS = [
  'https://tollgate-<4hex>.lan/ — the board, the default face.',
  'Log in as root.',
  'Dashboard — system, network, session.',
  'Wallet — the sats the guest just paid.',
  'Drain — withdraw the balance as e-cash.',
  'Wi-Fi — guest and private networks.',
  'Settings — pricing + profit split.',
  'Need more? The board links to LuCI.',
];

const videoDir = path.join(OUT, 'video', 'configui');
fs.rmSync(videoDir, { recursive: true, force: true });
fs.mkdirSync(videoDir, { recursive: true });
const timeline = [];
let t0 = 0, pending = null;
const start = (say) => { if (!t0) t0 = Date.now(); pending = { start: Date.now() - t0, say }; };
const end = () => { if (pending) { pending.end = Date.now() - t0; timeline.push(pending); pending = null; } };

const CSS = `
#film-url{position:fixed;left:0;right:0;bottom:78px;height:34px;background:#1b2430;color:#cdd9e5;font:14px ui-monospace,Menlo,Consolas,monospace;display:flex;align-items:center;gap:10px;padding:0 14px;z-index:2147483647}
#film-url b{color:#e6edf3}
#film-cursor{position:fixed;left:0;top:0;width:22px;height:22px;margin:-11px 0 0 -11px;border-radius:50%;background:rgba(255,140,0,.95);border:2px solid #fff;box-shadow:0 0 14px rgba(255,140,0,.85);z-index:2147483647;pointer-events:none}
#film-caption{position:fixed;left:0;right:0;bottom:0;z-index:2147483645;background:rgba(8,12,22,.92);color:#eef3fb;font-family:system-ui,"Segoe UI",sans-serif;font-size:21px;line-height:1.45;padding:18px 32px;text-align:center;border-top:3px solid #f97316}
`;
let page, cx = 300, cy = 300;
async function inject() {
  await page.addStyleTag({ content: CSS }).catch(() => {});
  await page.evaluate((host) => {
    const bar = document.getElementById('film-url') || document.createElement('div');
    bar.id = 'film-url'; bar.innerHTML = '<b>\u25cf \u25cf \u25cf</b> https://' + host + '/';
    document.documentElement.appendChild(bar);
    let c = document.getElementById('film-cursor'); if (!c) { c = document.createElement('div'); c.id = 'film-cursor'; document.documentElement.appendChild(c); }
    let cap = document.getElementById('film-caption'); if (!cap) { cap = document.createElement('div'); cap.id = 'film-caption'; document.documentElement.appendChild(cap); }
    document.addEventListener('mousemove', (e) => { c.style.transform = `translate(${e.clientX}px,${e.clientY}px)`; });
  }, HOST).catch(() => {});
}
async function cap(t) { await page.evaluate((x) => { const e = document.getElementById('film-caption'); if (e) e.textContent = x; }, t).catch(() => {}); }
async function moveTo(x, y, steps = 18) { const sx = cx, sy = cy; for (let i = 1; i <= steps; i++) { const t = i / steps, e = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2; await page.mouse.move(sx + (x - sx) * e, sy + (y - sy) * e).catch(() => {}); await page.waitForTimeout(16); } cx = x; cy = y; }
async function center(sel) { const el = page.locator(sel).first(); const box = await el.boundingBox({ timeout: 1200 }).catch(() => null); return box ? { x: box.x + box.width / 2, y: box.y + box.height / 2 } : null; }
async function hover(sel) { const c = await center(sel); if (c) await moveTo(c.x, c.y); }
async function clickText(txt) { await page.locator(`button:has-text("${txt}")`).first().click({ timeout: 4000 }).catch(() => {}); }
async function fillNative(sel, text) {
  await page.evaluate(({ sel, text }) => {
    const el = document.querySelector(sel); if (!el) return;
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, text);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }, { sel, text }).catch(() => {});
  await page.waitForTimeout(300);
}
// loginBoard: the board is a React app; a Playwright .fill() on the controlled
// password input left it EMPTY on camera ("the router password is required").
// Set the value via the native setter + an input event so React sees it. A
// freshly-installed router also shows a FIRST-RUN "choose your own admin
// password" screen (#new-password) which must be completed before login.
async function loginBoard() {
  await page.waitForTimeout(1500);
  if (await page.locator('#new-password').count()) {
    await fillNative('#new-password', boardPassword());
    await fillNative('#confirm-password', boardPassword());
    await page.waitForTimeout(400);
    await page.locator('button[type="submit"], button:has-text("Set password and continue")').first()
      .click({ timeout: 5000 }).catch(() => {});
    await page.waitForTimeout(3000);
  }
  // now the normal login view
  if (await page.locator('#username').count()) await fillNative('#username', 'root');
  if (await page.locator('#password').count()) await fillNative('#password', boardPassword());
  await page.waitForTimeout(400);
  await page.locator('button[type="submit"], button:has-text("Sign In")').first().click({ timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(2500);
}
function boardPassword() { return process.env.BOARD_PW_NEW || 'TollGate2'; }
async function typeSel(sel, text) { const c = await center(sel); if (c) await moveTo(c.x, c.y); await page.locator(sel).first().fill('').catch(() => {}); await page.locator(sel).first().type(text, { delay: 55 }).catch(() => {}); await page.waitForTimeout(250); }
async function route(r) { await page.evaluate((h) => { location.hash = '#/' + h; }, r).catch(() => {}); await page.waitForTimeout(1100); await inject(); }

const browser = await chromium.launch({ executablePath: '/usr/bin/google-chrome', args: [`--host-resolver-rules=MAP ${HOST} ${IP}`] });
const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1100, height: 800 }, recordVideo: { dir: videoDir, size: { width: 1100, height: 800 } } });
page = await ctx.newPage();
t0 = Date.now();
await page.goto(`https://${HOST}/`, { waitUntil: 'domcontentloaded', timeout: 20000 }).catch(() => {});
await page.waitForTimeout(3000);
await inject();

async function scene(say, shown, action, ms) {
  start(say); await cap(shown);
  const target = Math.max(2600, (ms ?? 0) + 800); const t = Date.now(); cx = 300; cy = 300; await page.mouse.move(300, 300).catch(() => {});
  if (action) await action().catch((e) => console.log('  action-err:', e.message.slice(0, 80)));
  const left = target - (Date.now() - t); if (left > 0) await page.waitForTimeout(left).catch(() => {});
  end();
}
for (let i = 0; i < S.length; i++) await scene(S[i][0], CAPTIONS[i], S[i][1], DURS[i]);

await page.screenshot({ path: path.join(OUT, 'configui.png') });
// No closing fade-to-black (reads as a blank film + trips blackdetect).
await page.waitForTimeout(1000);
fs.writeFileSync(path.join(OUT, 'configui.narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await ctx.close(); await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'configui.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'configui.webm')));
