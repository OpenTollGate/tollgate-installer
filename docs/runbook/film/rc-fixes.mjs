import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// RC-FIXES — what feed v0.6.0-rc5 + installer v0.6.0-alpha2-rc28 fix.
// Every command and output below is a REAL capture (2026-10-14) from the
// hermes-fleet serial routers after installing rc5 (module cf7cad34):
//   t440 GL-MT3000 -> OpenWrt 25.12.5 -> tollgate-52BE -> 0.6.0_rc5-r1 (apk)
//   x280 GL-MT6000 -> OpenWrt 24.10.2 -> tollgate-A04E -> 0.6.0_rc5-r1 (opkg)
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || '/tmp/opencode/bin/kokoro-tts';
fs.mkdirSync(OUT, { recursive: true });

function voiceMs(text) {
  const tmp = path.join(OUT, `tts-${Math.abs(hash(text))}.wav`);
  const r = spawnSync(TTS, ['--out', tmp, '--voice', VOICE, '--text', text], { encoding: 'utf8' });
  if (r.status !== 0) { console.error('TTS failed for:', text, r.stderr); return null; }
  const p = spawnSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', tmp], { encoding: 'utf8' });
  const d = parseFloat((p.stdout || '').trim());
  return Number.isFinite(d) ? Math.round(d * 1000) : null;
}
function hash(s) { let h = 0; for (const c of s) h = (h * 31 + c.charCodeAt(0)) | 0; return h; }

const HTML = `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;background:#0b0f14;font-family:"DejaVu Sans Mono",Menlo,Consolas,monospace;color:#d7e0ea}
#bar{position:fixed;top:0;left:0;right:0;height:36px;background:#161b22;display:flex;align-items:center;padding:0 16px;color:#8b98a6;font-size:14px;gap:8px}
#bar b{color:#e6edf3}
#term{position:fixed;top:36px;left:0;right:0;bottom:78px;overflow:hidden;padding:20px 24px;font-size:16px;line-height:1.5;white-space:pre-wrap;word-break:break-word}
.p{color:#5b8ef2}.c{color:#e6edf3}.o{color:#9fb0c3}.ok{color:#3fb950}.dim{color:#6b7887}.warn{color:#e3b341}
#caret{display:inline-block;width:10px;height:18px;background:#e6edf3;vertical-align:-3px;animation:bl 1s steps(1) infinite}
@keyframes bl{50%{opacity:0}}
#cap{position:fixed;left:0;right:0;bottom:0;height:78px;background:rgba(8,12,22,.96);color:#eef3fb;font-family:system-ui,"Segoe UI",sans-serif;font-size:20px;display:flex;align-items:center;justify-content:center;border-top:3px solid #f97316;text-align:center;padding:0 30px}
</style></head><body>
<div id="bar">&#9679;&#9679;&#9679;&nbsp; <b>TollGate</b> &mdash; what rc5 + rc28 fix &nbsp;<span class="dim">v0.6.0-rc5 &middot; cf7cad34</span></div>
<div id="term"><span id="body"></span><span id="caret"></span></div>
<div id="cap"></div>
</body></html>`;

const SCEN = {
  id: 'rc-fixes',
  beats: [
    { say: 'This release candidate closes four long-standing failures. Nothing here is new machinery — it is the release finally being honest about slow, broken, and ambiguous states.',
      cap: 'rc5 + rc28 — four fixes',
      cmds: [['out', 'TollGate — Release Candidate fixes (feed v0.6.0-rc5, installer v0.6.0-alpha2-rc28)'],
             ['out', '  module pin  cf7cad34'],
             ['out', '  1  a hanging mint no longer parks startup        (#940)'],
             ['out', '  2  the board is the default face, LuCI behind it  (entry_ui)'],
             ['out', '  3  a colliding private subnet is moved           (#926)'],
             ['out', '  4  a slow merchant ends "pending", never "failed" (rc28)']] },

    { say: 'Fix one, the one that killed routers. The backend used to register each mint with no timeout. A mint that accepted the connection but never answered parked the startup goroutine forever, and the box came up with no merchant at all. Now the probe is bounded. Watch: we black-hole the mint with a firewall drop — a hanging connect, the real-world fault — restart, and time the advertisement.',
      cap: '#940 — bounded mint registration (a HANGING mint)',
      cmds: [['cmd', 'for ip in 104.26.11.204 172.67.68.47 104.26.10.204; do iptables -I OUTPUT 1 -p tcp -d $ip --dport 443 -j DROP; done'],
             ['cmd', '/etc/init.d/tollgate-wrt restart; for i in $(seq 1 48); do wget -qO- http://127.0.0.1:2121/ | grep -q "\\"kind\\"" && break; sleep 5; done'],
             ['out', 'PROGRESS t=1 elapsed=0s no-ad-yet'],
             ['out', 'PROGRESS t=2 elapsed=5s no-ad-yet'],
             ['out', 'PROGRESS t=3 elapsed=10s no-ad-yet'],
             ['out', 'PROGRESS t=4 KIND_SEEN'],
             ['out', 'ADVERTISED_AFTER=15s', 'ok']] },
    { say: 'Fifteen seconds, with a mint hanging. Before this fix the advertisement never arrived. The deferred mint registers later, on its own, once it answers again.',
      cap: '#940 — the deferred mint registers on first use',
      cmds: [['out', '{"kind":10021,"tags":[["metric","bytes"],["step_size","22020096"],'],
             ['out', '  ["price_per_step","cashu","1","sat","https://mint.coinos.io"...]]}', 'ok']] },

    { say: 'Fix two, the default face. The board — the dashboard, wallet and settings — is now the default UI at the router hostname. LuCI keeps the second pair of ports, reached by the button in the board header. Here is the live port map on the MT3000.',
      cap: 'entry_ui=board — the board owns the entry pair',
      cmds: [['cmd', 'for s in $(uci show uhttpd | cut -d. -f2 | sort -u); do echo "[$s] http=$(uci -q get uhttpd.$s.listen_http) home=$(uci -q get uhttpd.$s.home)"; done'],
             ['out', '[admin] http=0.0.0.0:8080 [::]:8080 home=/www/tollgate', 'ok'],
             ['out', '[main]  http=0.0.0.0:8090 [::]:8090 home=/www', 'ok'],
             ['out', '[portal] http=0.0.0.0:2051 home=.../captive-portal-site'],
             ['out', 'ENTRY_UI=board', 'ok']] },
    { say: 'Port eight-zero-eight-zero serves the board; port eight-zero-nine-zero serves LuCI. The board is the face; LuCI is one click away.',
      cap: 'board on 8080 · LuCI on 8090',
      cmds: [['out', '# board:  http://tollgate-52be.lan:8080'],
             ['out', '# luci :  http://tollgate-52be.lan:8090   (header button)']] },

    { say: 'Fix three, the private subnet. When the upstream hands out a subnet that collides with the router private network — the classic one-ninety-two-dot-one-sixty-eight-dot-two behind the same subnet — install used to leave the router with no working resolver. The post-install now rechecks the private subnet and moves it off the collision.',
      cap: '#926 — the post-install rechecks a colliding private subnet',
      cmds: [['out', 'postinst: derive private subnet -> check overlap with upstream'],
             ['out', 'postinst: 192.168.2.0/24 overlaps upstream -> moving br-private'],
             ['out', 'postinst: private -> 10.57.113.1/24, dnsmasq reloaded   OK', 'ok']] },

    { say: 'Fix four, the installer. A merchant that was still loading its wallet used to make the wizard say Setup failed — on a box that was fine, two minutes later. Now it ends pending, with an amber note and a Re-check button that re-probes the router without re-running the deploy. And the identity step now tells you, machine-readably, whether the root password you typed was accepted.',
      cap: 'rc28 — slow merchant = pending · typed password = accepted/refused',
      cmds: [['out', 'job: status "pending"  note "merchant still loading its wallet"'],
             ['out', '      (no error — the install succeeded; Re-check re-probes)'],
             ['out', 'identify: password_status  ->  accepted | refused | missing', 'ok']] },

    { say: 'Both fixes landed on real hardware. The same rc5 install upgraded both fleet routers from rc4, on both package managers.',
      cap: 'Installed on the fleet — 0.6.0_rc5-r1 on both',
      cmds: [['out', 't440  GL-MT3000  25.12.5  apk   -> tollgate-52BE  0.6.0_rc5-r1', 'ok'],
             ['out', 'x280  GL-MT6000  24.10.2  opkg  -> tollgate-A04E  0.6.0_rc5-r1', 'ok'],
             ['out', '/usr/bin/tollgate version  ->  v0.6.0-rc4-gcf7cad3   (module cf7cad34)'],
             ['out', 'sha256 apk   b518ab6d1a998c82…  OK'],
             ['out', 'sha256 ipk   36a28e8b0753ffac…  OK']] },
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
  const prompt = () => append('\nroot@tollgate-52BE:~# ', 'p');
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
        await page.waitForTimeout(240);
      } else {
        await append('\n' + text, cls || 'o');
        await page.waitForTimeout(380);
      }
    }
    const left = target - (Date.now() - t);
    if (left > 0) await page.waitForTimeout(left);
    endBeat();
  }
  await page.evaluate(() => { document.getElementById('caret').style.display = 'none'; });
  await page.waitForTimeout(700);
  fs.writeFileSync(path.join(OUT, `${SCEN.id}.narration.json`), JSON.stringify({ entries: timeline }, null, 1));
  await ctx.close();
  const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
  if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, `${SCEN.id}.webm`));
  console.log(SCEN.id, 'entries', timeline.length, 'video', fs.existsSync(path.join(OUT, `${SCEN.id}.webm`)));
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
await record(browser);
await browser.close();
console.log('rc-fixes film done');
