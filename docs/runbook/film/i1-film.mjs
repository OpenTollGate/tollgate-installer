import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// I1 — flash + uplink prerequisite, then the merged manual install (.apk on
// OpenWrt 25.12 + .ipk on 24.10). Commands and output mirror REAL captures from
// the fleet serial routers (t440 GL-MT3000 -> 25.12.5, x280 GL-MT6000 -> 24.10.2),
// run 2026-10-13 against v0.6.0-rc4.
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

const TAG = fs.readFileSync(path.join(OUT, 'tag.txt'), 'utf8').trim();
const VER = fs.readFileSync(path.join(OUT, 'ver.txt'), 'utf8').trim();

const HTML = `<!doctype html><html><head><meta charset="utf-8"><style>
body{margin:0;background:#0b0f14;font-family:"DejaVu Sans Mono",Menlo,Consolas,monospace;color:#d7e0ea}
#bar{position:fixed;top:0;left:0;right:0;height:36px;background:#161b22;display:flex;align-items:center;padding:0 16px;color:#8b98a6;font-size:14px;gap:8px}
#bar b{color:#e6edf3}
#term{position:fixed;top:36px;left:0;right:0;bottom:78px;overflow:hidden;padding:20px 24px;font-size:17px;line-height:1.55;white-space:pre-wrap;word-break:break-word}
.p{color:#5b8ef2}.c{color:#e6edf3}.o{color:#9fb0c3}.ok{color:#3fb950}.dim{color:#6b7887}.warn{color:#e3b341}
#caret{display:inline-block;width:10px;height:18px;background:#e6edf3;vertical-align:-3px;animation:bl 1s steps(1) infinite}
@keyframes bl{50%{opacity:0}}
#cap{position:fixed;left:0;right:0;bottom:0;height:78px;background:rgba(8,12,22,.96);color:#eef3fb;font-family:system-ui,"Segoe UI",sans-serif;font-size:20px;display:flex;align-items:center;justify-content:center;border-top:3px solid #f97316;text-align:center;padding:0 30px}
</style></head><body>
<div id="bar">&#9679;&#9679;&#9679;&nbsp; <b>TollGate</b> &mdash; manual install &nbsp;<span class="dim">ssh root@router</span></div>
<div id="term"><span id="body"></span><span id="caret"></span></div>
<div id="cap"></div>
</body></html>`;

const SCEN = {
  id: 'i1-flash-manual',
  beats: [
    // ── Act 1: flash + uplink (Appendix A) ────────────────────────────────
    { say: 'Both manual paths start from vanilla OpenWrt with an internet uplink. First pick the profile for your device from the official index — never from memory.',
      cap: 'Appendix A.1 — flash vanilla OpenWrt',
      cmds: [['cmd', 'RELEASE=25.12.5; TARGET=mediatek/filogic; PROFILE=glinet_gl-mt3000'],
             ['cmd', 'BASE="https://downloads.openwrt.org/releases/$RELEASE/targets/$TARGET"'],
             ['cmd', 'IMG="openwrt-$RELEASE-mediatek-filogic-$PROFILE-squashfs-sysupgrade.bin"']] },
    { say: 'Download the image and its checksum, and verify the checksum before you flash. A wrong image is a brick.',
      cap: 'Verify the image BEFORE flashing',
      cmds: [['cmd', 'cd /tmp; wget -q -O $IMG "$BASE/$IMG"; wget -q -O sha256sums "$BASE/sha256sums"'],
             ['cmd', 'grep -F "$IMG" sha256sums | sha256sum -c -'],
             ['out', 'openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin: OK', 'ok']] },
    { say: 'Then sysupgrade with dash-n for a clean slate. The router wipes its configuration, reboots, and comes back as plain OpenWrt.',
      cap: 'sysupgrade -n  (clean slate)',
      cmds: [['cmd', 'sysupgrade -n /tmp/$IMG'],
             ['out', 'verifying sysupgrade tar file integrity'],
             ['out', 'upgrade: Performing system upgrade...'],
             ['out', 'upgrade: Rebooting system...'],
             ['out', 'root@OpenWrt:~#', 'ok']] },
    { say: 'Finally give the router an uplink: join a wireless network as a client, or point a WAN port at a network with DHCP. The package download needs it.',
      cap: 'Appendix A.2 — give the router an uplink',
      cmds: [['cmd', "uci set network.wan.device='lan1'; uci commit network; /etc/init.d/network restart"],
             ['cmd', 'sleep 6; ip route | grep default'],
             ['out', 'default via 192.168.2.1 dev lan1  src 192.168.2.16', 'ok'],
             ['out', 'ping -c1 9.9.9.9  →  1 packets transmitted, 1 received', 'ok']] },

    // ── Act 2: manual install — merged apk + ipk ──────────────────────────
    { say: 'Now the manual install. Both lanes are the same shape: resolve the release, fetch the package, verify it, then install it by path. The only difference is the package manager and the file — apk and dot-a-p-k on OpenWrt 25.12, opkg and dot-i-p-k on 24.10.',
      cap: 'Path B — manual install — .apk (25.12) + .ipk (24.10)',
      cmds: [['cmd', `TAG=${TAG}; VER=${VER}`],
             ['cmd', 'BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"'],
             ['cmd', 'FILE=tollgate-wrt_${VER}_aarch64_cortex-a53.apk   # 25.12']] },
    { say: 'Fetch the package and check its sha256. The release files carry no feed signature, so this checksum is the integrity gate — on both lanes.',
      cap: 'Fetch the file, verify the checksum (both lanes)',
      cmds: [['cmd', 'cd /tmp; wget -q -O $FILE "$BASE/$FILE"; wget -q -O SHA256SUMS "$BASE/SHA256SUMS"'],
             ['cmd', 'grep -F "$FILE" SHA256SUMS | sha256sum -c -'],
             ['out', 'tollgate-wrt_0.6.0_rc4_aarch64_cortex-a53.apk: OK', 'ok']] },
    { say: 'Install on 25.12: apk add, with the allow-untrusted and force-overwrite flags. It pulls twenty-three packages and finishes with tollgate-wrt.',
      cap: '.apk on OpenWrt 25.12 — apk add',
      cmds: [['cmd', 'apk update'],
             ['out', 'OK: 11139 distinct packages available'],
             ['cmd', 'apk add --allow-untrusted --force-overwrite /tmp/$FILE'],
             ['out', '( 1/23) Installing jq (1.8.1-r2)'],
             ['out', ' ...'],
             ['out', '(23/23) Installing tollgate-wrt (0.6.0_rc4-r1)', 'ok']] },
    { say: 'Install on 24.10: opkg install — same package, the ipk, a different manager.',
      cap: '.ipk on OpenWrt 24.10 — opkg install',
      cmds: [['cmd', 'FILE=tollgate-wrt_${VER}_aarch64_cortex-a53.ipk   # 24.10'],
             ['cmd', 'opkg update'],
             ['cmd', 'opkg install /tmp/$FILE'],
             ['out', 'Installing tollgate-wrt (0.6.0_rc4-r1) to root...'],
             ['out', 'Installing nodogsplash (5.0.2-r1) to root...'],
             ['out', 'Configuring tollgate-wrt.', 'ok']] },
    { say: 'The post-install restarts networking and moves the wired LAN ports onto the private bridge, so your SSH session drops — reconnect at the new management address. The hostname becomes tollgate dash four hex.',
      cap: 'Post-install — the wired port moves to br-private',
      cmds: [['out', '  * Restarting network...'],
             ['out', '  br-lan:    port 1(eth1) entered disabled state'],
             ['out', '  br-private: port 1(eth1) entered forwarding state', 'ok'],
             ['out', '  * Reloading remaining services...'],
             ['out', 'root@tollgate-52BE:/tmp#   # 25.12 lane', 'ok'],
             ['out', 'root@tollgate-A04E:/tmp#   # 24.10 lane', 'ok']] },
    { say: 'Reconnect and reach the board at the router hostname dot l a n, at port eight-zero-eight-zero. Log in as root with the default password, TollGate, and change it at first login. In the wizard and headless paths the installer sets that password for you; on a raw manual install, set a root password first or the board stays off.',
      cap: 'Board — https://tollgate-<4hex>.lan:8080 · root / TollGate',
      cmds: [['cmd', 'ssh root@$(uci get network.private.ipaddr)'],
             ['cmd', 'cat /proc/sys/kernel/hostname'],
             ['out', 'tollgate-52BE', 'ok'],
             ['out', '# board: http://tollgate-52be.lan:8080   (log in: root / TollGate)']] },
  ],
};

// Synthesize every beat BEFORE the camera rolls.
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
  const prompt = () => append('\nroot@OpenWrt:~# ', 'p');
  const caption = (t) => page.evaluate((x) => { document.getElementById('cap').textContent = x; }, t);

  for (const beat of SCEN.beats) {
    startBeat(beat.say);
    await caption(beat.cap);
    const target = Math.max(3000, (DUR.get(beat.say) ?? 0) + 900);
    const t = Date.now();
    for (const [kind, text, cls] of beat.cmds) {
      if (kind === 'cmd') {
        await prompt();
        for (const ch of text) { await append(ch, 'c'); await page.waitForTimeout(14); }
        await page.waitForTimeout(260);
      } else {
        await append('\n' + text, cls || 'o');
        await page.waitForTimeout(400);
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
console.log('i1 film done');
