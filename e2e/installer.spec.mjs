// e2e/installer.spec.mjs - Playwright E2E gate for tollgate-installer.
// Guards the class of defect that cost 2026-09-28: host-key type blindness
// and the stale feed default. Run against a wizard already listening on 8099.
import { test, expect } from '@playwright/test';
import { execFileSync } from 'node:child_process';

const BASE = process.env.WIZARD_URL || 'http://127.0.0.1:8099';

test('wizard UI loads and renders the trust surface', async ({ page }) => {
  await page.goto(BASE);
  await expect(page).toHaveTitle(/TollGate Installer/);
  // The page must render the build info. NOTE: the "[installer build tag]"
  // label lives in the LAUNCHER (install-and-test.sh); the binary's own
  // embedded index.html says "installer build" (PR #62). Assert structurally
  // here so this smoke test is valid against any published binary.
  await expect(page.locator('#build-info')).toBeVisible();
  await expect(page.locator('#build-info')).not.toHaveText(/^\s*$/);
});

test('feed resolution is not silently a stale default', async ({ request }) => {
  const cfg = await (await request.get(BASE + '/api/config')).json();
  expect(cfg.feed_release_tag, 'feed_release_tag present').toBeTruthy();
  expect(cfg.installer_commit, 'installer_commit present').toBeTruthy();
  // RED today: the binary falls back to a baked-in obsolete tag
  // (v0.6.0-alpha2-pre9) with no signal that it was never resolved.
  expect(
    cfg.feed_source,
    'config must declare how the feed release was resolved (default vs explicit)',
  ).toBeTruthy();
});

test('sync guard: negotiation reproduces the dual-key bug', () => {
  // Non-vacuous by construction: if the fixture stops serving BOTH key types
  // this throws, so the suite can never silently become meaningless.
  const out = execFileSync('go', ['run', './e2e-probe', '127.0.0.1:2222'], {
    encoding: 'utf8',
    env: { ...process.env, GOFLAGS: '-mod=mod' },
  });
  expect(out).toContain('REPRODUCED');
});
