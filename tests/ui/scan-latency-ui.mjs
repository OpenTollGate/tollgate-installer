// tests/ui/scan-latency-ui.mjs
//
// Playwright E2E gate for the scan-latency / undiagnosable-scan change.
//
// Drives the ACTUAL wizard page served by the ACTUAL installer binary in real
// Chrome and asserts the two operator-facing deliverables:
//
//   Act 1  A scan that finds nothing shows the DIAGNOSTIC BLOCK: every interface
//          with its state, the default gateway, every address that was probed
//          with what it answered, and the remediation list — including the
//          conditions the operator cannot otherwise see ("no link",
//          "link up, NO IPv4"). The one sentence it replaces
//          ("Scan failed. Check that the wizard can reach your network.") named
//          none of it.
//   Act 2  The SAME screen always offers the manual address field, and a typed
//          address is identified and selected so Deploy unlocks WITHOUT
//          discovery ever finding anything.
//   Act 3  A scan whose fetch never returns does not hang the wizard forever:
//          the watchdog fires, the block explains it, and the manual field is
//          still there.
//
// What is real: the binary under test, its HTTP API, its page, the DOM.
// What is stubbed: /api/scan and /api/identify per case, because a LAN with —
// and without — a router cannot exist on the machine running this.
//
// HOSTILE-CONTENT CHECK: the diagnostic payload used in Act 1 carries markup in
// the strings a neighbouring device controls (interface names, notes). The page
// holds the router's password, so none of it may reach the HTML parser. The test
// asserts the rendered block contains the payload verbatim as visible text and
// that no <svg>/<img>/<b> element was created inside it.
//
// Usage: node tests/ui/scan-latency-ui.mjs [outdir]
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const REPO = path.resolve(new URL('../..', import.meta.url).pathname);
const OUT = path.resolve(process.argv[2] || path.join(REPO, 'dist', 'ui-scan-latency'));
const GO = '/usr/local/go/bin';
const LOG = [];

function log(...a) {
  const line = a.join(' ');
  LOG.push(line);
  console.log(line);
}
function sh(cmd, args, opts = {}) {
  return execFileSync(cmd, args, { encoding: 'utf8', ...opts });
}
function waitFor(pred, ms, what) {
  const t0 = Date.now();
  return new Promise((resolve, reject) => {
    const tick = async () => {
      let v;
      try { v = await pred(); } catch { v = false; }
      if (v) return resolve(v);
      if (Date.now() - t0 > ms) return reject(new Error(`timed out waiting for ${what}`));
      setTimeout(tick, 100);
    };
    tick();
  });
}

// The diagnostic payload the server produces when nothing answers. Interface
// names are host-controlled; the notes are shaped by whatever answered on the
// LAN, so they carry markup here on purpose.
const DIAG = {
  summary: '4 address(es) were probed and answered nothing that identifies as a TollGate/OpenWrt router.',
  interfaces: [
    { name: 'wlp58s0', state: 'up', carrier: '1', ipv4: ['192.168.2.33/24'] },
    { name: 'enp0s31f6', state: 'down', carrier: '0', problem: 'no link (carrier=0): nothing is attached to this port' },
    { name: 'wwp0s20f0u6i12', state: 'up', carrier: '1', problem: 'link up, NO IPv4: no DHCP lease and no static address' },
    { name: 'docker0', state: 'up', carrier: '1', ipv4: ['172.17.0.1/16'], virtual: true },
    { name: '<img src=x onerror=fetch("//evil.example")>', state: 'up', carrier: '1' },
  ],
  gateways: ['192.168.2.1'],
  probes: [
    { ip: '192.168.2.1', source: 'default-route gateway', detail: 'dev wlp58s0', ssh: false, httpPort: 0 },
    { ip: '192.168.2.254', source: 'local subnet address', detail: 'wlp58s0 192.168.2.33/24', ssh: false, httpPort: 0 },
    { ip: '192.168.1.1', source: 'well-known router address', ssh: false, httpPort: 0, demoted: true, note: '<b>unverified host on your LAN</b>' },
    { ip: '192.168.2.48', source: 'ARP/neighbour table', detail: '74:bf:c0:ac:ae:b4', ssh: false, httpPort: 80, note: 'unverified host on your LAN — answers :80' },
  ],
  sources: ['default-route gateway: 1 address(es)', 'local subnet address: 1 address(es)', 'well-known router address: 1 address(es)', 'ARP/neighbour table: 1 address(es)'],
  remediation: [
    '1. Use the LAN port: on the GL-MT3000 the 2.5 GbE port is WAN and the 1 GbE port is LAN — plugged into WAN you get no DHCP lease and no route.',
    '2. Give it time: after a flash or a reboot a router needs 60-120 s before it answers.',
    '3. Force a fresh lease on the wired interface: dhclient -v <interface>.',
    '4. Vanilla OpenWrt answers on :22 only — there is no LuCI web UI on :80 or :8080 until it is installed.',
    "5. Or skip discovery entirely: bash <(curl -fsSL https://raw.githubusercontent.com/OpenTollGate/tollgate-installer/main/install-and-test.sh) <ROUTER-IP> '' <you@wallet.app>",
  ],
  manualHint: "Enter your router's address in the box above.",
  text: 'FULL BLOCK',
};

async function startInstaller(binDir) {
  const bin = path.join(binDir, 'installer-ui');
  log('building the installer under test ...');
  sh(path.join(GO, 'go'), ['build', '-o', bin, '.'], { cwd: REPO, env: { ...process.env, PATH: `${GO}:${process.env.PATH}` } });
  const httpPort = String(30000 + Math.floor(Math.random() * 20000));
  const store = path.join(binDir, 'known-hosts');
  const proc = spawn(bin, ['-port', httpPort, '-ssh-port', '2299'], {
    env: { ...process.env, TOLLGATE_KNOWN_HOSTS: store },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', (b) => log('[installer] ' + b.toString().trim()));
  proc.stderr.on('data', (b) => log('[installer] ' + b.toString().trim()));
  const base = 'http://127.0.0.1:' + httpPort;
  await waitFor(async () => {
    const r = await fetch(base + '/api/config').catch(() => null);
    return r && r.status === 200;
  }, 30000, 'the installer to serve /api/config');
  return { proc, base };
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'ui-scan-'));
  const { proc: inst, base } = await startInstaller(work);
  log(`installer under test: ${base}`);

  const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  const fail = (m) => { throw new Error(m); };
  const results = [];

  try {
    // ── Act 1: a scan that finds nothing shows the diagnostic block ──────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      let scanStub = 'empty';
      await ctx.route('**/api/scan', async (route) => {
        if (scanStub === 'empty') {
          return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ routers: [], diagnostics: DIAG, failure: 'FULL BLOCK' }) });
        }
        // 'hang': never fulfil — the client watchdog must fire.
        return new Promise(() => {});
      });
      const page = await ctx.newPage();
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#scan-detail', { state: 'attached', timeout: 15000 });
      await waitFor(async () => (await page.locator('#scan-detail').innerText()).includes('install-and-test.sh'), 15000, 'the diagnostic block to render');

      const text = await page.locator('#scan-detail').innerText();
      const mustMention = [
        'wlp58s0', '192.168.2.33/24', 'enp0s31f6', 'carrier=0', 'no link',
        'link up, NO IPv4', 'docker0', 'virtual: not scanned',
        '192.168.2.1', 'default-route gateway', '192.168.2.254', '192.168.1.1',
        'well-known router address', 'ARP/neighbour table', '192.168.2.48',
        'not on your local subnet',                  // the demotion is visible
        'What to try, in this order', '2.5 GbE', '60-120 s', 'dhclient', ':22 only',
        'install-and-test.sh', '<ROUTER-IP>',        // the copy-pasteable headless form
      ];
      for (const m of mustMention) {
        if (!text.includes(m)) fail(`the failure screen does not mention ${JSON.stringify(m)}:\n${text}`);
      }
      if (text.includes('Scan failed. Check that the wizard can reach your network.')) {
        fail('the dead-end one-liner is still what the operator is shown');
      }

      // Hostile content must arrive as TEXT: no element was created from it.
      const hostile = '<img src=x onerror=fetch("//evil.example")>';
      if (!text.includes(hostile)) fail('the hostile interface name was not rendered as text at all');
      const parsed = await page.evaluate(() => {
        const box = document.getElementById('scan-detail');
        return { imgs: box.querySelectorAll('img,svg,b,script').length };
      });
      if (parsed.imgs !== 0) fail(`hostile content was parsed into ${parsed.imgs} element(s) inside the diagnostic block (this page holds the router password)`);

      // The manual address field is on the SAME screen.
      if (await page.locator('#manual-view').isHidden()) fail('the manual address card is hidden on the failure screen');
      if (!(await page.locator('#manual-ip').isVisible())) fail('the manual address input is not visible');
      results.push({ act: 'diagnostic block + manual field on a failed scan', ok: true });
      log('ACT 1 OK — the failure screen names interfaces, gateway, probes and next steps; manual field present; hostile text inert');
      await ctx.close();
    }

    // ── Act 2: a typed address bypasses discovery and unlocks Deploy ─────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      await ctx.route('**/api/scan', (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ routers: [], diagnostics: DIAG }) }));
      const identified = [];
      await ctx.route('**/api/identify', async (route) => {
        const body = JSON.parse(route.request().postData() || '{}');
        identified.push(body.ip);
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            ip: body.ip, name: 'GL-MT3000', vendor: 'OpenWrt', model: 'glinet,gl-mt3000',
            firmware: 'OpenWrt 25.12.5', mac: '94:83:c4:aa:bb:cc',
            ssh_open: true, identified: true, note: 'TollGate/OpenWrt router (identified)',
            source: 'typed by the operator',
          }),
        });
      });
      const page = await ctx.newPage();
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await page.waitForSelector('#manual-ip', { timeout: 15000 });

      // A malformed address must be refused locally — a typo must not become a
      // deploy against a different device.
      await page.fill('#manual-ip', '192.168.1');
      await page.click('#manual-btn');
      await page.waitForTimeout(300);
      if (identified.length !== 0) fail('a malformed address was sent to the installer anyway');
      if (await page.locator('#select-view').isVisible()) fail('a malformed address opened the deploy screen');

      await page.fill('#manual-ip', '127.0.0.1');
      await page.click('#manual-btn');
      await waitFor(async () => (await page.locator('#select-view').getAttribute('class') || '').indexOf('hidden') === -1, 15000, 'the deploy screen to open from the typed address');
      if (identified[0] !== '127.0.0.1') fail(`the typed address was not the one identified: ${JSON.stringify(identified)}`);
      const selected = await page.locator('#router-select').inputValue();
      if (selected !== '127.0.0.1') fail(`the typed address is not selected in the router dropdown (got ${selected})`);
      const label = await page.locator('#router-select option:checked').innerText();
      if (!/GL-MT3000/.test(label)) fail(`the identified router label was not shown: ${label}`);

      // Deploy unlocks with the typed address and a Lightning address, even
      // though discovery found NOTHING.
      await page.fill('#password', 'hunter2');
      await page.fill('#lnurl', 'you@wallet.app');
      await page.waitForTimeout(400);
      if (!(await page.isEnabled('#deploy-btn'))) fail('Deploy stayed disabled after a typed address + Lightning address');
      results.push({ act: 'typed address -> identify -> Deploy unlocked', ok: true });
      log('ACT 2 OK — a typed address bypassed discovery, was identified, selected, and unlocked Deploy');
      await ctx.close();
    }

    // ── Act 3: a scan that never returns does not hang the wizard ────────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      await ctx.route('**/api/scan', () => new Promise(() => {})); // never answers
      const page = await ctx.newPage();
      // The watchdog is 20 s by design and is asserted in the page source; here
      // we really wait it out rather than adding a test-only hook to the page.
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await waitFor(async () => {
        const t = await page.locator('#scan-status').innerText();
        return /did not finish in time/i.test(t);
      }, 35000, 'the client watchdog to fire and say so');
      const text = await page.locator('#scan-detail').innerText();
      if (!/did not answer within/i.test(text)) fail(`the watchdog block does not explain the timeout:\n${text}`);
      if (!/Use this address/.test(text)) fail('the watchdog block does not point at the manual address field');
      if (await page.locator('#manual-view').isHidden()) fail('the manual address card is hidden after a timed-out scan');
      results.push({ act: 'client watchdog on a hung scan', ok: true });
      log('ACT 3 OK — a hung scan surfaced the watchdog state, the explanation, and the manual field');
      await ctx.close();
    }

    fs.writeFileSync(path.join(OUT, 'scan-latency-ui.log'), LOG.join('\n') + '\n');
    console.log('\nE2E RESULT: ' + JSON.stringify(results, null, 2));
  } finally {
    await browser.close();
    inst.kill();
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error('E2E FAILED: ' + e.message); console.error(LOG.join('\n')); process.exit(1); });
