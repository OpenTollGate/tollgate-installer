import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// I2 (part 2) — the headless installer: same wizard, driven from the command
// line (no browser). Terminal style, matching the I1 look.
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
fs.mkdirSync(OUT, { recursive: true });

function voiceMs(text) {
  const tmp = path.join(OUT, `tts-${Math.abs(hash(text))}.wav`);
  const r = spawnSync(TTS, ['--out', tmp, '--voice', VOICE, '--text', text], { encoding: 'utf8' });
  if (r.status !== 0) return null;
  const p = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', tmp], { encoding: 'utf8' });
  const d = parseFloat((p.stdout || '').trim());
  return Number.isFinite(d) ? Math.round(d * 1000) : null;
}
function hash(s) { let h = 0; for (const c of s) h = (h * 31 + c.charCodeAt(0)) | 0; return h; }

const HTML = `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;background:#0b0f14;font-family:"DejaVu Sans Mono",Menlo,Consolas,monospace;color:#d7e0ea}
#bar{position:fixed;top:0;left:0;right:0;height:36px;background:#161b22;display:flex;align-items:center;padding:0 16px;color:#8b98a6;font-size:14px;gap:8px}
#bar b{color:#e6edf3}
#term{position:fixed;top:36px;left:0;right:0;bottom:78px;overflow:hidden;padding:20px 24px;font-size:17px;line-height:1.55;white-space:pre-wrap;word-break:break-word}
.p{color:#5b8ef2}.c{color:#e6edf3}.o{color:#9fb0c3}.ok{color:#3fb950}.dim{color:#6b7887}
#caret{display:inline-block;width:10px;height:18px;background:#e6edf3;vertical-align:-3px;animation:bl 1s steps(1) infinite}
@keyframes bl{50%{opacity:0}}
#cap{position:fixed;left:0;right:0;bottom:0;height:78px;background:rgba(8,12,22,.96);color:#eef3fb;font-family:system-ui,"Segoe UI",sans-serif;font-size:20px;display:flex;align-items:center;justify-content:center;border-top:3px solid #f97316;text-align:center;padding:0 30px}
</style></head><body>
<div id="bar">&#9679;&#9679;&#9679;&nbsp; <b>TollGate</b> &mdash; headless installer &nbsp;<span class="dim">no browser</span></div>
<div id="term"><span id="body"></span><span id="caret"></span></div>
<div id="cap"></div>
</body></html>`;

const SCEN = {
  id: 'i2-headless',
  beats: [
    { say: 'If you prefer no browser, the wizard runs headless: the same installer, driven from the command line, in one shot.',
      cap: 'Path A — installer wizard (headless)',
      cmds: [['out', '# no browser: download the installer and deploy'],
             ['cmd', 'bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \\'],
             ['cmd', "  192.168.1.1 'ROUTER_PASSWORD' you@wallet.app"]] },
    { say: 'Pass the router address, its current password, and your Lightning address. It resolves the newest release, installs it, then verifies hostname, ports, DNS, the captive portal, and the health ad.',
      cap: 'Router IP · password · Lightning address — then it verifies',
      cmds: [['out', '# resolves the feed release → installs → verifies', 'ok'],
             ['out', '# deploy complete — hostname, ports, DNS, LNURL, portal OK', 'ok'],
             ['out', '# board: http://tollgate-<4hex>.lan:8080   (log in: root / TollGate)', 'ok']] },
  ],
};

const DUR = new Map();
for (const b of SCEN.beats) if (!DUR.has(b.say)) DUR.set(b.say, voiceMs(b.say));
console.log('measured beats:', DUR.size, [...DUR.values()]);

async function record(browser) {
  const videoDir = path.join(OUT, 'video', SCEN.id);
  fs.rmSync(videoDir, { recursive: true, force: true });
  fs.mkdirSync(videoDir, { recursive: true });
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 700 }, recordVideo: { dir: videoDir, size: { width: 1100, height: 700 } } });
  const page = await ctx.newPage();
  let t0;
  let pending = null;
  const timeline = [];
  const startBeat = (say) => { pending = { start: Date.now() - t0, say }; };
  const endBeat = () => { if (pending) { pending.end = Date.now() - t0; timeline.push(pending); pending = null; } };

  await page.setContent(HTML, { waitUntil: 'load' });
  t0 = Date.now();
  const append = (t, cls) => page.evaluate(({ t, cls }) => {
    const b = document.getElementById('body');
    const s = document.createElement('span'); s.className = cls || ''; s.textContent = t; b.appendChild(s);
    const term = document.getElementById('term'); term.scrollTop = term.scrollHeight;
  }, { t, cls });
  const prompt = () => append('\nuser@laptop:~$ ', 'p');
  const caption = (t) => page.evaluate((x) => { document.getElementById('cap').textContent = x; }, t);

  for (const beat of SCEN.beats) {
    startBeat(beat.say);
    await caption(beat.cap);
    const target = Math.max(3000, (DUR.get(beat.say) ?? 0) + 900);
    const t = Date.now();
    for (const [kind, text, cls] of beat.cmds) {
      if (kind === 'cmd') {
        await prompt();
        for (const ch of text) { await append(ch, 'c'); await page.waitForTimeout(12); }
        await page.waitForTimeout(260);
      } else {
        await append('\n' + text, cls || 'o');
        await page.waitForTimeout(420);
      }
    }
    const left = target - (Date.now() - t);
    if (left > 0) await page.waitForTimeout(left);
    endBeat();
  }
  await page.evaluate(() => { document.getElementById('caret').style.display = 'none'; });
  await page.waitForTimeout(600);
  fs.writeFileSync(path.join(OUT, `${SCEN.id}.narration.json`), JSON.stringify({ entries: timeline }, null, 1));
  await ctx.close();
  const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
  if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, `${SCEN.id}.webm`));
  console.log(SCEN.id, 'entries', timeline.length, 'video', fs.existsSync(path.join(OUT, `${SCEN.id}.webm`)));
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
await record(browser);
await browser.close();
console.log('i2 headless done');
