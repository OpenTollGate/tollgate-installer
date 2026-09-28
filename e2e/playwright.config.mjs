// e2e/playwright.config.mjs
// Use the SYSTEM Google Chrome: this host's ubuntu26.04 build has no bundled
// chromium download for Playwright 1.59.x ("does not support chromium on
// ubuntu26.04-x64"). channel:'chrome' drives /usr/bin/google-chrome instead.
export default {
  testDir: '.',
  timeout: 60_000,
  fullyParallel: false,
  reporter: [['list']],
  use: { channel: 'chrome', headless: true },
};
