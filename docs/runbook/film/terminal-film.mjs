import { chromium } from 'playwright';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

// Record terminal walkthroughs of each install path: type the real commands
// with a visible caret, print the output, and narrate over the live terminal
// (a lower-third caption), paced to each line so nothing overlaps.
const OUT = process.env.FILM_DIR || '/tmp/opencode/filmcap';
const VOICE = process.env.FILM_VOICE || 'af_heart';
const TTS = process.env.KOKORO_BIN || 'kokoro-tts';
const ONLY = process.env.ONLY || '';
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

const TAG = fs.readFileSync(path.join(OUT, 'tag.txt'), 'utf8').trim();
const VER = fs.readFileSync(path.join(OUT, 'ver.txt'), 'utf8').trim();

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
<div id="bar">&#9679;&#9679;&#9679;&nbsp; <b>TollGate</b> &mdash; OpenWrt router shell &nbsp;<span class="dim">ssh root@router</span></div>
<div id="term"><span id="body"></span><span id="caret"></span></div>
<div id="cap"></div>
</body></html>`;

const SCEN = [
  {
    id: 's2-manual-apk',
    beats: [
      { say: 'This is the manual install on OpenWrt 25.12 — the apk lane, for when the wizard cannot run. You need a router already flashed with vanilla OpenWrt and given an internet uplink.',
        cap: 'Path B — manual install, .apk on OpenWrt 25.12',
        cmds: [['out', '# router already flashed + uplinked; log in over ssh'], ['cmd', 'ssh root@192.168.1.1']] },
      { say: 'First, resolve the release under test, so you never paste a stale tag.',
        cap: 'Resolve the release under test first',
        cmds: [['cmd', "TAG=$(curl -fsSL 'https://api.github.com/repos/FreedomTechFeed/packages/releases?per_page=30' | python3 -c 'import sys,json;print(max(json.load(sys.stdin), key=lambda r: r[\"published_at\"])[\"tag_name\"])')"],
               ['cmd', 'VER=$(printf \'%s\' "$TAG" | sed -e \'s/^v//\' -e \'s/-/_/g\')'],
               ['out', `TAG=${TAG} VER=${VER}`, 'ok']] },
      { say: 'Download the package and check its sha256. The packages carry no signature, so this checksum is the integrity gate.',
        cap: 'Fetch the file, verify the checksum',
        cmds: [['cmd', 'BASE="https://github.com/FreedomTechFeed/packages/releases/download/$TAG"'],
               ['cmd', 'ARCH=$(sed -n \'s/^DISTRIB_ARCH=//p\' /etc/openwrt_release | tr -d "\\"\'")'],
               ['cmd', 'EXT=apk; PM="apk add --allow-untrusted --force-overwrite"; apk update'],
               ['cmd', 'wget -q --no-check-certificate "$BASE/SHA256SUMS" -O SHA256SUMS'],
               ['cmd', 'wget -q --no-check-certificate "$BASE/$FILE" -O "$FILE"'],
               ['cmd', 'grep " $FILE\\$" SHA256SUMS | sha256sum -c -'],
               ['out', `tollgate-wrt_${VER}_aarch64_cortex-a53.apk: OK`, 'ok']] },
      { say: 'Install the file by path. The install drops your SSH session and moves the wired LAN port onto the management bridge, so reconnect at the new address.',
        cap: 'Install — the session drops, reconnect',
        cmds: [['cmd', '$PM "/tmp/$FILE"'],
               ['out', '# postinst restarts the network; the wired port moves to br-private'],
               ['cmd', 'uci get network.private.ipaddr'],
               ['out', '192.168.2.1/24']] },
      { say: 'Reconnect and reach the board. The router answers at its hostname dot l a n. Log in with the default password, which is TollGate, and change it at first login.',
        cap: 'Reconnect · https://tollgate-<4hex>.lan · default password TollGate',
        cmds: [['cmd', 'ssh root@$(uci get network.private.ipaddr)'],
               ['cmd', 'cat /proc/sys/kernel/hostname'],
               ['out', 'TollGate-7TJZ', 'ok'],
               ['out', '# board: http://tollgate-7tjz.lan:8080  (log in: root / TollGate)']] },
    ],
  },
  {
    id: 's3-manual-ipk',
    beats: [
      { say: 'This is the manual install on OpenWrt 24.10 — the opkg lane. Same steps, but the package manager is opkg and the file is an ipk.',
        cap: 'Path B — manual install, .ipk on OpenWrt 24.10',
        cmds: [['out', '# router flashed with OpenWrt 24.10 + uplinked'], ['cmd', 'ssh root@192.168.1.1']] },
      { say: 'Resolve the release under test the same way.',
        cap: 'Resolve the release under test',
        cmds: [['out', `TAG=${TAG}  VER=${VER}`, 'ok']] },
      { say: 'Fetch the ipk and verify its checksum. There is no feed to add — you install the release file by path.',
        cap: 'Fetch the .ipk, verify the checksum',
        cmds: [['cmd', 'EXT=ipk; PM="opkg install"; opkg update'],
               ['cmd', 'wget -q --no-check-certificate "$BASE/SHA256SUMS" -O SHA256SUMS'],
               ['cmd', 'wget -q --no-check-certificate "$BASE/$FILE" -O "$FILE"'],
               ['cmd', 'grep " $FILE\\$" SHA256SUMS | sha256sum -c -'],
               ['out', `tollgate-wrt_${VER}_aarch64_cortex-a53.ipk: OK`, 'ok']] },
      { say: 'Install it. If the plain install refuses, add the force flags one at a time. The install drops your SSH session.',
        cap: 'Install with opkg (add force flags if needed)',
        cmds: [['cmd', '$PM "/tmp/$FILE"'],
               ['out', '# postinst restarts the network; the wired port moves to br-private']] },
      { say: 'Reconnect, then reach the board at the router hostname dot l a n, or add the tollgate dot l a n alias yourself. Log in with default password TollGate.',
        cap: 'Reconnect · https://tollgate-<4hex>.lan · default password TollGate',
        cmds: [['cmd', 'IP=$(uci -q get network.private.ipaddr); IP=${IP%%/*}'],
               ['cmd', 'echo "$IP tollgate.lan tollgate.local" >> /etc/hosts'],
               ['out', '# board: http://tollgate.lan:8080  (log in: root / TollGate)']] },
    ],
  },
  {
    id: 's4-offline-bundle',
    beats: [
      { say: 'When the router has no internet uplink, use the release offline bundle. Stage the package and its whole dependency closure on the router from a machine that does have internet.',
        cap: 'Path B — offline bundle (no uplink on the router)',
        cmds: [['out', '# on a machine WITH internet:'], ['cmd', `curl -fsSL https://github.com/FreedomTechFeed/packages/releases/download/${TAG}/tollgate-wrt_${VER}_aarch64_cortex-a53-offline.tar.gz | tar -xz`]] },
      { say: 'The bundle unpacks an installer that copies the package and its dependencies to the router and installs offline.',
        cap: 'Unpack and run the offline installer',
        cmds: [['cmd', "./install-offline.sh 192.168.1.1 'ROUTER_PASSWORD'"],
               ['out', '# stages the package + dependency closure, then installs offline'],
               ['out', '# postinst restarts the network; reconnect at the management address', 'ok']] },
    ],
  },
  {
    id: 's5-flash-uplink',
    beats: [
      { say: 'Both manual paths start from vanilla OpenWrt with an uplink. This is the flash step: pick the profile for your device from the official index, never from memory.',
        cap: 'Appendix A.1 — flash vanilla OpenWrt',
        cmds: [['cmd', 'RELEASE=25.12.5; TARGET=mediatek/filogic; PROFILE=glinet_gl-mt3000'],
               ['cmd', 'BASE="https://downloads.openwrt.org/releases/$RELEASE/targets/$TARGET"'],
               ['cmd', 'IMG="openwrt-$RELEASE-${TARGET//\\//-}-$PROFILE-squashfs-sysupgrade.bin"']] },
      { say: 'Download the image and its checksum, and verify the checksum before you flash. A wrong image is a brick.',
        cap: 'Verify the image BEFORE flashing',
        cmds: [['cmd', 'curl -fsSLO "$BASE/sha256sums"; curl -fsSLO "$BASE/$IMG"'],
               ['cmd', 'grep -F "$IMG" sha256sums | sha256sum -c -'],
               ['out', `${'openwrt-25.12.5-mediatek-filogic-glinet_gl-mt3000-squashfs-sysupgrade.bin'}: OK`, 'ok']] },
      { say: 'Pipe the image to the router, verify it landed, then sysupgrade with dash n for a clean slate.',
        cap: 'Pipe the image in, then sysupgrade -n',
        cmds: [['cmd', "ssh root@$IP 'cat > /tmp/sysup.bin' < \"$IMG\""],
               ['cmd', 'ssh root@$IP "sha256sum /tmp/sysup.bin"'],
               ['cmd', 'ssh root@$IP "sysupgrade -n /tmp/sysup.bin"']] },
      { say: 'Finally, give the router an uplink by joining a wireless network as a client, then route it through the WAN zone, and confirm the uplink is up.',
        cap: 'Appendix A.2 — WiFi uplink (repeater)',
        cmds: [['cmd', "RADIO=radio0; SSID='<upstream-ssid>'; KEY='<upstream-key>'"],
               ['cmd', 'uci set wireless.sta=wifi-iface; uci set wireless.sta.mode=sta'],
               ['cmd', 'uci set wireless.sta.ssid="$SSID"; uci set wireless.sta.key="$KEY"'],
               ['cmd', 'uci set wireless.sta.network=wwan; uci commit wireless; wifi reload'],
               ['cmd', 'ip route | grep default ; ping -c2 9.9.9.9'],
               ['out', 'PING 9.9.9.9: 2 packets transmitted, 2 received', 'ok']] },
    ],
  },
  {
    id: 's1-wizard-headless',
    beats: [
      { say: 'If you prefer no browser, the wizard runs headless: the same installer, driven from the command line, installing and verifying in one shot.',
        cap: 'Path A — installer wizard (headless)',
        cmds: [['out', '# no browser: full deploy + verification'], ['cmd', 'bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) \\'], ['cmd', "  192.168.1.1 'ROUTER_PASSWORD' you@wallet.app"]] },
      { say: 'Pass the router address, its current password, and your Lightning address. The installer resolves the newest release, installs it, and checks the result.',
        cap: 'Router IP · password · Lightning address',
        cmds: [['out', '# resolves the feed release → installs → verifies', 'ok'],
               ['out', '# then reach the board at http://tollgate-<4hex>.lan (log in: root / TollGate)']] },
    ],
  },
];

const CSS_OVERLAY = `#cap{position:fixed;left:0;right:0;bottom:0;height:78px;background:rgba(8,12,22,.96);color:#eef3fb;font-family:system-ui;font-size:20px;display:flex;align-items:center;justify-content:center;border-top:3px solid #f97316;text-align:center;padding:0 30px}`;

// Synthesize every beat BEFORE the camera rolls.
const DUR = new Map();
for (const scen of SCEN) {
  if (ONLY && scen.id !== ONLY) continue;
  for (const b of scen.beats) if (!DUR.has(b.say)) DUR.set(b.say, voiceMs(b.say));
}
console.log('measured beats:', DUR.size);

async function record(scen, browser) {
  const videoDir = path.join(OUT, 'video', scen.id);
  fs.rmSync(videoDir, { recursive: true, force: true });
  fs.mkdirSync(videoDir, { recursive: true });
  const ctx = await browser.newContext({ viewport: { width: 1100, height: 700 }, recordVideo: { dir: videoDir, size: { width: 1100, height: 700 } } });
  const page = await ctx.newPage();
  let t0 = Date.now();
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

  for (const beat of scen.beats) {
    startBeat(beat.say);
    await caption(beat.cap);
    const target = Math.max(2600, (DUR.get(beat.say) ?? 0) + 800);
    const t = Date.now();
    for (const [kind, text, cls] of beat.cmds) {
      if (kind === 'cmd') {
        await prompt();
        for (const ch of text) { await append(ch, 'c'); await page.waitForTimeout(16); }
        await page.waitForTimeout(280);
      } else {
        await append('\n' + text, cls || 'o');
        await page.waitForTimeout(450);
      }
    }
    const left = target - (Date.now() - t);
    if (left > 0) await page.waitForTimeout(left);
    endBeat();
  }
  await page.evaluate(() => { document.getElementById('caret').style.display = 'none'; });
  await page.waitForTimeout(600);
  fs.writeFileSync(path.join(OUT, `${scen.id}.narration.json`), JSON.stringify({ entries: timeline }, null, 1));
  await ctx.close();
  const files = fs.readdirSync(videoDir).filter((f) => f.endsWith('.webm'));
  if (files.length) fs.renameSync(path.join(videoDir, files[0]), path.join(OUT, `${scen.id}.webm`));
  console.log(scen.id, 'entries', timeline.length, 'video', fs.existsSync(path.join(OUT, `${scen.id}.webm`)));
}

const browser = await chromium.launch(process.env.CHROME_EXE ? { executablePath: process.env.CHROME_EXE } : {});
for (const scen of SCEN) {
  if (ONLY && scen.id !== ONLY) continue;
  await record(scen, browser);
}
await browser.close();
console.log('terminal films done');
