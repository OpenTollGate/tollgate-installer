// tests/ui/deploy-trust-ui.mjs
//
// Drives the ACTUAL user-facing wizard in Chrome via Playwright and records it,
// to prove the one-click flow end to end from the operator's side:
//
//   Act 1  Deploy -> the confirmation appears ONCE, naming the fingerprint the
//          router actually presents. Declining it must NOT deploy.
//   Act 2  Deploy -> confirm -> the install proceeds (no separate Trust errand,
//          no second button press).
//   Act 3  Deploy again -> NO prompt: the key was remembered.
//
// What is real: the binary under test, its HTTP API, the SSH handshake against
// the fixture router, the trust store, and every line of the page.
// What is stubbed: /api/scan only, because a LAN with a router on it cannot exist
// on the machine running this — the wizard populates its router dropdown from
// that call. The refusal and the fingerprint are NOT stubbed; they come from the
// real server dialling the real fixture.
//
// Usage: node tests/ui/deploy-trust-ui.mjs <outdir>
import { chromium } from 'playwright';
import { spawn, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const REPO = path.resolve(new URL('../..', import.meta.url).pathname);
const OUT = path.resolve(process.argv[2] || path.join(REPO, 'dist', 'ui-video'));
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
    const tick = () => {
      let v;
      try { v = pred(); } catch { v = false; }
      if (v) return resolve(v);
      if (Date.now() - t0 > ms) return reject(new Error(`timed out waiting for ${what}`));
      setTimeout(tick, 100);
    };
    tick();
  });
}

async function get(base, p) {
  const r = await fetch(base + p);
  return { status: r.status, body: await r.json().catch(() => null) };
}

// --- start the fixture router ------------------------------------------------
function startFakeRouter(binDir) {
  const bin = path.join(binDir, 'fakerouter');
  log('building the fixture router ...');
  sh(path.join(GO, 'go'), ['build', '-o', bin, './internal/fakerouter'], { cwd: REPO, env: { ...process.env, PATH: `${GO}:${process.env.PATH}` } });
  const proc = spawn(bin, ['-password', 'hunter2'], { stdio: ['ignore', 'pipe', 'pipe'] });
  return new Promise((resolve, reject) => {
    let out = '';
    const onData = (b) => {
      out += b.toString();
      const port = /FAKEROUTER_PORT=(\d+)/.exec(out);
      const fp = /FAKEROUTER_FINGERPRINT=(SHA256:\S+)/.exec(out);
      if (port && fp) {
        proc.stdout.off('data', onData);
        proc.stdout.on('data', (b) => log('[fakerouter] ' + b.toString().trim()));
        resolve({ proc, port: port[1], fingerprint: fp[1] });
      }
    };
    proc.stdout.on('data', onData);
    proc.stderr.on('data', (b) => process.stderr.write('[fakerouter] ' + b));
    setTimeout(() => reject(new Error('fakerouter did not report its port/fingerprint')), 30000);
  });
}

// --- start the installer under test ------------------------------------------
async function startInstaller(binDir, sshPort, store) {
  const bin = path.join(binDir, 'installer-ui');
  log('building the installer under test ...');
  sh(path.join(GO, 'go'), ['build', '-o', bin, '.'], { cwd: REPO, env: { ...process.env, PATH: `${GO}:${process.env.PATH}` } });
  const httpPort = String(20000 + Math.floor(Math.random() * 20000));
  const proc = spawn(bin, ['-port', httpPort, '-ssh-port', sshPort], {
    env: { ...process.env, TOLLGATE_KNOWN_HOSTS: store },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', (b) => log('[installer] ' + b.toString().trim()));
  proc.stderr.on('data', (b) => log('[installer] ' + b.toString().trim()));
  const base = 'http://127.0.0.1:' + httpPort;
  await waitFor(async () => (await get(base, '/api/config')).status === 200, 30000, 'the installer to serve /api/config');
  return { proc, base };
}

async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'ui-e2e-'));
  const store = path.join(work, 'known-hosts');

  const { proc: fake, port: sshPort, fingerprint } = await startFakeRouter(work);
  log(`fixture router: port ${sshPort}, presents ${fingerprint}`);
  const { proc: inst, base } = await startInstaller(work, sshPort, store);
  log(`installer under test: ${base} (trust store: ${store})`);

  const browser = await chromium.launch({
    channel: 'chrome',
    headless: true,
    args: ['--no-sandbox', '--disable-dev-shm-usage'],
  });
  const context = await browser.newContext({
    viewport: { width: 1280, height: 800 },
    recordVideo: { dir: OUT, size: { width: 1280, height: 800 } },
  });

  // The only stub: there is no LAN with a router on it here.
  await context.route('**/api/scan', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        routers: [{ ip: '127.0.0.1', name: 'Fixture Router', vendor: 'OpenWrt', model: 'test', firmware: 'OpenWrt 24.10', mac: '00:11:22:33:44:55' }],
      }),
    }));

  const dialogs = [];
  let dialogMode = 'decline';

  async function openWizard(page) {
    page.on('dialog', async (d) => {
      dialogs.push({ message: d.message(), mode: dialogMode });
      log(`DIALOG (${dialogMode}): ${d.message().split('\n')[0]}`);
      if (dialogMode === 'accept') await d.accept();
      else await d.dismiss();
    });
    await page.goto(base, { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#select-view:not(.hidden)', { timeout: 20000 });
    await page.selectOption('#router-select', '127.0.0.1');
    await page.fill('#password', 'hunter2');
    await page.fill('#lnurl', 'you@wallet.app');
    await page.waitForTimeout(600);
    const enabled = await page.isEnabled('#deploy-btn');
    if (!enabled) throw new Error('Deploy stayed disabled after filling the router and the Lightning address');
  }

  const fail = (m) => { throw new Error(m); };
  const results = [];

  // ---------------- Act 1: the prompt appears once; declining stops it --------
  let page = await context.newPage();
  await openWizard(page);
  dialogMode = 'decline';
  await page.click('#deploy-btn');
  await waitFor(() => dialogs.length > 0, 15000, 'the trust confirmation to appear');
  const d1 = dialogs[0].message;
  if (!d1.includes(fingerprint)) fail(`the confirmation did not name the fingerprint the router presents (${fingerprint})`);
  if (!/remembered/i.test(d1)) fail('the confirmation does not tell the operator the key is remembered');
  await page.waitForTimeout(1500);
  const deployedAfterDecline = !(await page.locator('#deploy-view').getAttribute('class')).includes('hidden');
  const errorShown = !(await page.locator('#error-view').getAttribute('class')).includes('hidden');
  if (deployedAfterDecline) fail('declining the confirmation still started a deploy');
  // Declining must EXPLAIN itself. This caught a real bug: the page called an
  // undefined showError(), so the cancellation threw a ReferenceError and the
  // operator saw a button that did nothing.
  if (!errorShown) fail('declining the confirmation gave the operator NO feedback: the error view stayed hidden (a failure path that shows nothing)');
  const cancelDetail = (await page.locator('#error-detail').innerText()).trim();
  if (!/not confirmed/i.test(cancelDetail)) fail(`the cancellation message does not explain itself: "${cancelDetail}"`);
  results.push(`Act 1: one prompt, named ${fingerprint.slice(0, 22)}…; declined -> no deploy, and the reason is on screen ("${cancelDetail}")`);
  log(`Act 1 OK: prompt named the real fingerprint; declined -> deploy-view hidden (${!deployedAfterDecline}), error shown (${errorShown}): "${cancelDetail}"`);
  await page.close();

  // ---------------- Act 2: confirm once -> the install proceeds --------------
  page = await context.newPage();
  await openWizard(page);
  dialogMode = 'accept';
  const before = dialogs.length;
  await page.click('#deploy-btn');
  await waitFor(() => dialogs.length > before, 15000, 'the trust confirmation');
  await page.waitForSelector('#deploy-view:not(.hidden)', { timeout: 20000 });
  const log1 = await page.locator('#deploy-view').innerText();
  results.push('Act 2: confirmed once -> the installer started the install (no separate Trust button, no second Deploy press)');
  log(`Act 2 OK: deploy view visible; first lines: ${log1.split('\n').slice(0, 3).join(' | ')}`);
  await page.waitForTimeout(4000);
  const log2 = await page.locator('#deploy-view').innerText();
  log(`Act 2 progress: ${log2.split('\n').length} log line(s)`);
  const video2 = page.video();
  await page.close();
  const videoPath = await video2.path();
  log('video: ' + videoPath);

  // ---------------- Act 3: asked once, ever ---------------------------------
  page = await context.newPage();
  await openWizard(page);
  dialogMode = 'accept';
  const before3 = dialogs.length;
  await page.click('#deploy-btn');
  await page.waitForTimeout(6000);
  if (dialogs.length > before3) fail(`a NEW installer run was asked to trust the same router again: ${dialogs[dialogs.length - 1].message.split('\n')[0]}`);
  const deployed3 = !(await page.locator('#deploy-view').getAttribute('class')).includes('hidden');
  if (!deployed3) fail('with the key remembered, Deploy did not proceed');
  results.push('Act 3: same router, fresh page -> NO prompt, Deploy proceeds straight through');
  log('Act 3 OK: no re-prompt; the key was remembered');
  await page.close();

  await context.close();
  await browser.close();
  fake.kill();
  inst.kill();

  fs.writeFileSync(path.join(OUT, 'ui-e2e.log'), LOG.join('\n') + '\n');
  console.log('\n===== PASS =====');
  for (const r of results) console.log(' • ' + r);
  console.log('VIDEO=' + videoPath);
}

main().catch((e) => {
  console.error('\n===== FAIL =====');
  console.error(e && e.stack ? e.stack : e);
  process.exit(1);
});
