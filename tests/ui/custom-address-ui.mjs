// tests/ui/custom-address-ui.mjs
//
// Playwright E2E gate for the "custom router address" progressive-disclosure
// change: the typed-address field moves behind an explicit dropdown choice
// instead of competing with the scan result on every screen.
//
// Deliverables asserted:
//
//   Act 1  With candidates found, the dropdown is the happy path: the manual
//          address card is HIDDEN, and a final option ("Enter a different
//          address...") reveals it on selection and focuses the input.
//          Switching back to a discovered router hides it again.
//          WHY: an always-visible address box competes with the scan result and
//          most operators do not know the router's IP. Behind a choice it costs
//          nothing until it is wanted.
//   Act 2  A scan that finds NOTHING still shows the address field with no
//          dropdown interaction at all. This is the trap the design has to
//          avoid: the dropdown is populated from scan results, so gating the
//          field behind an option in an EMPTY dropdown would strand the
//          operator with no way forward — exactly the 10.153.97.1 case, where
//          the router was alive on a subnet the scan could not guess.
//   Act 3  The sentinel is never an address. While "custom" is selected no
//          request may carry the sentinel as an ip; once a real router is
//          selected the requests carry the real address again. Every consumer
//          reads selectedRouterIP(), never the raw <select>.
//
// What is real: the binary under test, its HTTP API, its page, the DOM.
// What is stubbed: /api/scan, /api/identify and /api/prestage, because a LAN
// with — and without — a router cannot exist on the machine running this.
//
// Usage: node tests/ui/custom-address-ui.mjs [outdir]
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const REPO = path.resolve(new URL('../..', import.meta.url).pathname);
const OUT = path.resolve(process.argv[2] || path.join(REPO, 'dist', 'ui-custom-address'));
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

// Two plausible results: one identified TollGate router, one unverified host.
const ROUTERS = [
  {
    ip: '192.168.1.1', name: 'GL-MT3000', vendor: 'OpenWrt', model: 'glinet,gl-mt3000',
    firmware: 'OpenWrt 25.12.5', mac: '94:83:c4:47:1b:ac', ssh_open: true,
    http_port: 80, source: 'default-route gateway', identified: true,
  },
  {
    ip: '192.168.2.40', name: 'Router', vendor: 'unknown', model: 'unknown',
    firmware: 'unknown', mac: '94:83:c4:74:d3:03', ssh_open: false,
    http_port: 80, source: 'ARP/neighbour table', identified: false,
    note: 'unverified host on your LAN — answers :80',
  },
];

const DIAG_EMPTY = {
  summary: '4 address(es) were probed and answered nothing.',
  interfaces: [{ name: 'enp0s31f6', state: 'up', carrier: '1', ipv4: ['192.168.2.43/24'] }],
  gateways: ['192.168.2.1'],
  probes: [{ ip: '192.168.2.1', source: 'default-route gateway', ssh: false, httpPort: 0 }],
  sources: ['default-route gateway: 1 address(es)'],
  remediation: ['1. Use the LAN port.', '2. Give it time.'],
};

const CUSTOM = '__custom__';

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

function stubScan(ctx, routers) {
  return ctx.route('**/api/scan', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ routers, diagnostics: routers.length ? null : DIAG_EMPTY }),
    }));
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'ui-custom-'));
  const { proc: inst, base } = await startInstaller(work);
  log(`installer under test: ${base}`);

  const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });
  const fail = (m) => { throw new Error(m); };
  const results = [];

  try {
    // ── Act 1: candidates found -> dropdown is the happy path ───────────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      await stubScan(ctx, ROUTERS);
      const page = await ctx.newPage();
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await waitFor(async () => !(await page.locator('#select-view').isHidden()), 20000, 'the router dropdown');

      // The manual card must NOT be competing with the scan result.
      if (await page.locator('#manual-view').isVisible()) {
        fail('the manual address card is still shown when the scan found routers');
      }

      // ...but it must be reachable: a final sentinel option.
      const opts = await page.$$eval('#router-select option', (os) => os.map((o) => ({ v: o.value, t: o.textContent })));
      if (opts.length !== ROUTERS.length + 1) {
        fail(`expected ${ROUTERS.length} routers + 1 custom option, got ${opts.length}: ${JSON.stringify(opts)}`);
      }
      const last = opts[opts.length - 1];
      if (last.v !== CUSTOM) fail(`the last dropdown option is not the custom sentinel (got ${JSON.stringify(last)})`);
      if (!/different address/i.test(last.t)) fail(`the sentinel does not read as an address escape hatch: ${JSON.stringify(last.t)}`);

      // Selecting it reveals and focuses the field.
      await page.selectOption('#router-select', CUSTOM);
      await waitFor(async () => (await page.locator('#manual-view').isVisible()), 5000, 'the manual card to appear on the custom choice');
      const focused = await page.evaluate(() => document.activeElement && document.activeElement.id);
      if (focused !== 'manual-ip') fail(`the address input was not focused on the custom choice (focus was ${focused})`);
      if (!(await page.locator('#manual-ip').isVisible())) fail('the address input is not visible while custom is selected');

      // Switching back to a real router hides it again.
      await page.selectOption('#router-select', ROUTERS[0].ip);
      await waitFor(async () => (await page.locator('#manual-view').isHidden()), 5000, 'the manual card to hide on a real router');
      results.push({ act: 'custom option reveals the field; real router hides it', ok: true });
      log('ACT 1 OK — with candidates the field is hidden, and the dropdown sentinel reveals + focuses it');
      await ctx.close();
    }

    // ── Act 2: NOTHING found -> the field must not be stranded behind an
    //           option in an empty dropdown ─────────────────────────────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      await stubScan(ctx, []);
      const page = await ctx.newPage();
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await waitFor(async () => (await page.locator('#scan-detail').innerText()).length > 0, 20000, 'the diagnostic block');

      if (await page.locator('#manual-view').isHidden()) {
        fail('a scan that found nothing left the address field hidden — the operator is stranded (this is the 10.153.97.1 case)');
      }
      if (!(await page.locator('#manual-ip').isVisible())) fail('the address input is not visible after a fruitless scan');
      results.push({ act: 'fruitless scan still exposes the field without a click', ok: true });
      log('ACT 2 OK — a scan that finds nothing still shows the address field: no stranding');
      await ctx.close();
    }

    // ── Act 3: the sentinel is never an address ─────────────────────────────
    {
      const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
      await stubScan(ctx, ROUTERS);
      const sent = [];
      const record = (route, body) => { sent.push(body); return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }); };
      await ctx.route('**/api/identify', (route) => {
        let body = {};
        try { body = JSON.parse(route.request().postData() || '{}'); } catch { /* ignore */ }
        // identify must still answer with a router shape for the real address.
        if (body.ip && body.ip !== CUSTOM) {
          sent.push(body);
          return route.fulfill({
            status: 200, contentType: 'application/json',
            body: JSON.stringify({ ip: body.ip, name: 'GL-MT3000', vendor: 'OpenWrt', firmware: 'OpenWrt 25.12.5', mac: '94:83:c4:47:1b:ac', ssh_open: true, identified: true }),
          });
        }
        return record(route, body);
      });
      await ctx.route('**/api/prestage', (route) => {
        let body = {};
        try { body = JSON.parse(route.request().postData() || '{}'); } catch { /* ignore */ }
        return record(route, body);
      });

      const page = await ctx.newPage();
      await page.goto(base, { waitUntil: 'domcontentloaded' });
      await waitFor(async () => !(await page.locator('#select-view').isHidden()), 20000, 'the router dropdown');

      // Select custom, give it a password, wait out every debounce.
      await page.selectOption('#router-select', CUSTOM);
      await page.fill('#password', 'hunter2');
      await page.waitForTimeout(2500);

      const leaked = sent.filter((b) => b && b.ip === CUSTOM);
      if (leaked.length) fail(`${leaked.length} request(s) carried the sentinel as an ip: ${JSON.stringify(leaked)}`);

      // Now pick a real router: requests must carry the real address.
      await page.selectOption('#router-select', ROUTERS[0].ip);
      await page.waitForTimeout(2500);
      const real = sent.filter((b) => b && b.ip === ROUTERS[0].ip);
      if (!real.length) fail(`no request carried the real address ${ROUTERS[0].ip} after selecting it: ${JSON.stringify(sent)}`);
      results.push({ act: 'sentinel never reaches the API; real address does', ok: true });
      log('ACT 3 OK — the sentinel never reaches the API; selecting a router restores the real address');
      await ctx.close();
    }

    fs.writeFileSync(path.join(OUT, 'custom-address-ui.log'), LOG.join('\n') + '\n');
    console.log('\nE2E RESULT: ' + JSON.stringify(results, null, 2));
  } finally {
    await browser.close();
    inst.kill();
  }
}

main().then(() => process.exit(0)).catch((e) => { console.error('E2E FAILED: ' + e.message); console.error(LOG.join('\n')); process.exit(1); });
