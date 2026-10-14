import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// Tail (admin) v2 — the REAL TollGate board at its hostname entry
// (https://tollgate-52be.lan/), i.e. the default face. First-run password set,
// then dashboard -> wallet -> wifi -> settings (dev split) -> wallet Drain, and
// the LuCI cross-link button called out.
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
const HOST = process.env.BOARD_HOST || 'tollgate-52be.lan';
const IP = process.env.BOARD_IP || '192.168.2.4';
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
  ['Log in as root. The install-time admin password is generated and never printed, so the board has you choose your own on first use; from then on you sign in with it.',
   async () => { await typeSel('#password', 'TollGate'); await clickText('Sign In'); await page.waitForTimeout(2800); }],
  ['The dashboard is the router at a glance: system, network, and the live session.',
   async () => { await route('dashboard'); }],
  ['The Wallet holds what the router earns — the balance in sats and the Cashu mint it accepts. Drain withdraws the balance as a Cashu token.',
   async () => { await route('wallet'); await hover('button:has-text("Drain")'); }],
  ['Wi-Fi shows the guest and private networks.',
   async () => { await route('wifi'); }],
  ['Settings holds the pricing and the profit split — the margin factor, the price per step, and the accepted mints. Change these to your own defaults.',
   async () => { await route('settings'); await hover('#field-margin'); }],
  ['And if you need the full OpenWrt options, the board has a button for LuCI right in the header.',
   async () => { await hover('a:has-text("LuCI")'); await hover('button:has-text("LuCI")'); }],
];

const DURS = S.map(([say]) => voiceMs(say));
console.log('measured lines (ms):', DURS.join(', '));
const CAPTIONS = [
  'https://tollgate-<4hex>.lan/ — the board, the default face.',
  'Log in as root (password chosen on first run).',
  'Dashboard — system, network, session.',
  'Wallet — earned sats, the mint, and Drain.',
  'Wi-Fi — guest and private networks.',
  'Settings — pricing + profit split.',
  'Need more? The board links to LuCI.',
];

const videoDir = path.join(OUT, 'video', 'tail-admin2');
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

await page.screenshot({ path: path.join(OUT, 'tail-admin2.png') });
await page.evaluate(() => { const f = document.createElement('div'); f.style.cssText = 'position:fixed;inset:0;background:#000;z-index:2147483647;opacity:0;transition:opacity 1s'; document.body.appendChild(f); requestAnimationFrame(() => { f.style.opacity = '1'; }); }).catch(() => {});
await page.waitForTimeout(1000);
fs.writeFileSync(path.join(OUT, 'tail-admin2.narration.json'), JSON.stringify({ entries: timeline }, null, 1));
await ctx.close(); await browser.close();
const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, 'tail-admin2.webm'));
console.log('entries', timeline.length, 'video', fs.existsSync(path.join(OUT, 'tail-admin2.webm')));
