// tests/ui/password-status-ui.mjs
//
// Drives the ACTUAL wizard in Chrome against the in-process fixture router, to
// prove the operator-facing password behaviour end to end:
//
//   Act 1  A router that needs NO password (a fresh box): the wizard must NOT
//          ask for a password — the field is hidden and a "needs no password"
//          note is shown.
//   Act 2  A router that DOES need a password: the field is shown, the RIGHT
//          password reads accepted (green), and a WRONG one reads refused (red).
//
// This is the mirror of internal/app's passwordVerdict tests at the UI layer:
// the verdict is measured in isolation, so a wrong password can never read
// "accepted" via a fallback connect (a fresh router accepts an empty password,
// so a connect-outcome verdict would have said accepted for anything).
//
// What is real: the binary under test, its HTTP API, the SSH handshake against
// the fixture router, the trust store, and every line of the page.
// What is stubbed: /api/scan only (there is no LAN with a router on it here).
//
// Usage: node tests/ui/password-status-ui.mjs [outdir]
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const REPO = path.resolve(new URL('../..', import.meta.url).pathname);
const OUT = path.resolve(process.argv[2] || path.join(REPO, 'dist', 'ui-video'));
const GO = process.env.GO_BIN || 'go';
const LOG = [];
const log = (...a) => { const l = a.join(' '); LOG.push(l); console.log(l); };
const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { encoding: 'utf8', ...opts });

function waitFor(pred, ms, what) {
  const t0 = Date.now();
  return new Promise((resolve, reject) => {
    const tick = async () => {
      let v; try { v = await pred(); } catch { v = false; }
      if (v) return resolve(v);
      if (Date.now() - t0 > ms) return reject(new Error(`timed out waiting for ${what}`));
      setTimeout(tick, 100);
    };
    tick();
  });
}

async function startFakeRouter(binDir, password) {
  const bin = path.join(binDir, 'fakerouter');
  sh(GO, ['build', '-o', bin, './internal/fakerouter'], { cwd: REPO });
  const proc = spawn(bin, ['-password', password], { stdio: ['ignore', 'pipe', 'pipe'] });
  return new Promise((resolve, reject) => {
    let out = '';
    const onData = (b) => {
      out += b.toString();
      const port = /FAKEROUTER_PORT=(\d+)/.exec(out);
      const fp = /FAKEROUTER_FINGERPRINT=(SHA256:\S+)/.exec(out);
      if (port && fp) { proc.stdout.off('data', onData); resolve({ proc, port: port[1], fingerprint: fp[1] }); }
    };
    proc.stdout.on('data', onData);
    proc.stderr.on('data', (b) => process.stderr.write('[fakerouter] ' + b));
    setTimeout(() => reject(new Error('fakerouter did not report its port/fingerprint')), 30000);
  });
}

async function startInstaller(binDir, sshPort, fingerprint) {
  const bin = path.join(binDir, 'installer-ui');
  sh(GO, ['build', '-o', bin, '.'], { cwd: REPO });
  const httpPort = String(20000 + Math.floor(Math.random() * 20000));
  const proc = spawn(bin, ['-port', httpPort, '-ssh-port', sshPort, '-trust-host-key', fingerprint],
    { stdio: ['ignore', 'pipe', 'pipe'] });
  proc.stderr.on('data', (b) => log('[installer] ' + b.toString().trim()));
  const base = 'http://127.0.0.1:' + httpPort;
  await waitFor(async () => {
    try { return (await fetch(base + '/api/config')).status === 200; } catch { return false; }
  }, 30000, 'the installer to serve /api/config');
  return { proc, base };
}

async function openWizard(context, base) {
  const page = await context.newPage();
  await page.route('**/api/scan', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ routers: [{ ip: '127.0.0.1', name: 'Fixture Router', vendor: 'OpenWrt', model: 'test', firmware: 'OpenWrt 24.10', mac: '00:11:22:33:44:55' }] }),
    }));
  await page.goto(base, { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('#select-view:not(.hidden)', { timeout: 20000 });
  await page.selectOption('#router-select', '127.0.0.1');
  return page;
}

const fail = (m) => { throw new Error(m); };
const isHidden = async (page, sel) => (await page.locator(sel).getAttribute('class') || '').split(/\s+/).includes('hidden');

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'pw-ui-'));
  const results = [];
  let videoPath = '';

  const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--no-sandbox', '--disable-dev-shm-usage'] });

  // ---- Act 1: a passwordless router must not be asked for a password --------
  {
    const { proc: fake, port, fingerprint } = await startFakeRouter(work, '');
    const { proc: inst, base } = await startInstaller(work, port, fingerprint);
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 }, recordVideo: { dir: OUT, size: { width: 1280, height: 800 } } });
    const page = await openWizard(ctx, base);
    // Selecting triggers refreshRouterName -> /api/identify -> password_status
    await waitFor(() => isHidden(page, '#password-field'), 20000, 'the password field to hide on a passwordless router')
      .catch(() => fail('a router that needs NO password still showed the password field (the wizard asked for a password it does not need)'));
    if (await isHidden(page, '#password-not-required')) fail('the "needs no password" note stayed hidden on a passwordless router');
    results.push('Act 1: passwordless router -> the password field is hidden, the "needs no password" note is shown');
    log('Act 1 OK');
    videoPath = await (async () => { await page.waitForTimeout(1500); const v = page.video(); await page.close(); await ctx.close(); fake.kill(); inst.kill(); return v.path(); })();
  }

  // ---- Act 2: a protected router: right -> accepted, wrong -> refused --------
  {
    const { proc: fake, port, fingerprint } = await startFakeRouter(work, 'hunter2');
    const { proc: inst, base } = await startInstaller(work, port, fingerprint);
    const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
    const page = await openWizard(ctx, base);
    await waitFor(async () => !(await isHidden(page, '#password-field')), 20000, 'the password field to show on a protected router')
      .catch(() => fail('a router that REQUIRES a password hid the password field'));

    // Wrong first: must read refused (the regression: a connect-outcome verdict
    // read accepted, because the fresh-router empty-password fallback would bind).
    await page.fill('#password', 'not-the-password');
    await waitFor(async () => /Wrong router password/i.test(await page.locator('#password-status').innerText()), 20000,
      'the wrong password to read refused')
      .catch(async () => fail(`a WRONG password did not read refused; field says: ${(await page.locator('#password-status').innerText()).trim()}`));
    results.push('Act 2: wrong password -> refused (red)');

    await page.fill('#password', 'hunter2');
    await waitFor(async () => /Router password accepted/i.test(await page.locator('#password-status').innerText()), 20000,
      'the right password to read accepted')
      .catch(async () => fail(`the RIGHT password did not read accepted; field says: ${(await page.locator('#password-status').innerText()).trim()}`));
    results.push('Act 2: right password -> accepted (green)');
    log('Act 2 OK');
    await page.waitForTimeout(1500);
    await page.close();
    await ctx.close();
    fake.kill();
    inst.kill();
  }

  await browser.close();
  fs.writeFileSync(path.join(OUT, 'password-status-ui.log'), LOG.join('\n') + '\n');
  console.log('\n===== PASS =====');
  for (const r of results) console.log(' • ' + r);
  console.log('VIDEO=' + videoPath);
}

main().catch((e) => { console.error('\n===== FAIL ====='); console.error(e && e.stack ? e.stack : e); process.exit(1); });
