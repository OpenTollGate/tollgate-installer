// e2e/i18n.spec.mjs — the i18n acceptance gate.
//
// Proves two things the Go tests can only assert on strings:
//   1. the resolved language actually REACHES the page (lang/dir on <html>, the
//      catalogue's own text rendered, not the key names);
//   2. RTL is a LAYOUT property, not just translated text — an Arabic page must
//      measure as right-to-left, which an English page must not.
//
// It also captures one screenshot per locale under e2e/screenshots/, which is
// the artefact the card asks for (en/de/ar at minimum; ar proves RTL).
//
// Run against a wizard already listening on WIZARD_URL (default 8099):
//   WIZARD_URL=http://127.0.0.1:8123 npx playwright test i18n.spec.mjs
//
// NOTE on Accept-Language: page.setExtraHTTPHeaders CANNOT override it — the
// browser builds that header itself and Playwright filters the attempt, so a
// header-based test written that way silently runs in the DEFAULT locale and
// "passes" any assertion about English. The per-context `locale` option is the
// supported lever; it is what sets Accept-Language on the wire.
import { test, expect } from '@playwright/test';
import { mkdirSync } from 'node:fs';

const BASE = process.env.WIZARD_URL || 'http://127.0.0.1:8099';
const SHOT_DIR = process.env.I18N_SHOT_DIR || 'e2e/screenshots';

// The three the card requires. `locale` becomes the Accept-Language the browser
// sends; `lang`/`dir` are what the server must echo back and render.
const LOCALES = [
  { locale: 'en-US', lang: 'en', dir: 'ltr' },
  { locale: 'de-DE', lang: 'de', dir: 'ltr' },
  { locale: 'ar', lang: 'ar', dir: 'rtl' },
];

test.beforeAll(() => {
  mkdirSync(SHOT_DIR, { recursive: true });
});

for (const loc of LOCALES) {
  test.describe(`${loc.lang} page`, () => {
    test.use({ locale: loc.locale });

    test(`the header language reaches the rendered page`, async ({ page }) => {
      await page.goto(BASE);

      // 1. The MARKUP carries the decision — this is the pre-script paint, so an
      //    RTL reader never sees a frame of LTR layout.
      await expect(page.locator('html')).toHaveAttribute('lang', loc.lang);
      await expect(page.locator('html')).toHaveAttribute('dir', loc.dir);

      // 2. The catalogue payload is in the page and non-empty.
      const payload = await page.evaluate(() => ({
        lang: window.__LANG,
        dir: window.__DIR,
        keys: Object.keys(window.__I18N || {}).length,
        title: window.__I18N ? window.__I18N['title'] : null,
      }));
      expect(payload.lang, '__LANG').toBe(loc.lang);
      expect(payload.dir, '__DIR').toBe(loc.dir);
      expect(payload.keys, 'catalogue keys on the page').toBeGreaterThan(100);
      expect(payload.title, 'title in the payload').toBeTruthy();

      // 3. No raw key name leaked into the visible page. t() returns the key
      //    itself for an unknown key (deliberately visible), so a dotted
      //    identifier in the body text is a missing translation.
      const bodyText = await page.locator('body').innerText();
      expect(bodyText, 'unrendered catalogue keys in the page').not.toMatch(
        /\b(?:scan|trust|copy|cleanSlate|credential|common|lang|error)\.[a-zA-Z]+\b/,
      );

      // 4. The document title is the catalogue's, not the English fallback.
      expect(await page.title(), 'translated title').toBe(payload.title);

      await page.screenshot({ path: `${SHOT_DIR}/wizard-${loc.lang}.png`, fullPage: true });
    });
  });
}

test('ar: the layout is mirrored, not only the text', async ({ browser }) => {
  // The check a translation-only change would pass while still being broken:
  // Arabic words inside a left-aligned, LTR-shaped box.
  const arCtx = await browser.newContext({ locale: 'ar' });
  const arPage = await arCtx.newPage();
  await arPage.goto(BASE);

  const ar = await arPage.evaluate(() => {
    const body = getComputedStyle(document.body);
    const label = document.querySelector('label');
    const pwWrap = document.querySelector('.pw-wrap input');
    return {
      direction: getComputedStyle(document.documentElement).direction,
      bodyAlign: body.textAlign,
      labelAlign: label ? getComputedStyle(label).textAlign : null,
      // The password field's reveal button is pinned to the inline-END side.
      // Under RTL that must move to the left; measuring it proves the mirrored
      // rules in the stylesheet were actually applied, not just present.
      pwPaddingLeft: pwWrap ? getComputedStyle(pwWrap).paddingLeft : null,
      pwPaddingRight: pwWrap ? getComputedStyle(pwWrap).paddingRight : null,
    };
  });

  expect(ar.direction, 'document direction').toBe('rtl');
  // text-align alone is satisfied by a stylesheet nobody applied, so measure
  // the computed direction as well as the alignment.
  expect(['right', 'start'], 'body text alignment').toContain(ar.bodyAlign);
  if (ar.pwPaddingLeft && ar.pwPaddingRight) {
    expect(
      parseFloat(ar.pwPaddingLeft),
      'RTL must move the password toggle reserve to the left side',
    ).toBeGreaterThan(parseFloat(ar.pwPaddingRight));
  }
  await arCtx.close();

  // The contrast case: the same measurements on an English page must be LTR, so
  // this test cannot pass against a page that ignores direction entirely.
  const enCtx = await browser.newContext({ locale: 'en-US' });
  const enPage = await enCtx.newPage();
  await enPage.goto(BASE);
  const en = await enPage.evaluate(() => ({
    direction: getComputedStyle(document.documentElement).direction,
    bodyAlign: getComputedStyle(document.body).textAlign,
  }));
  expect(en.direction, 'English document direction').toBe('ltr');
  expect(en.bodyAlign, 'English body alignment').not.toBe('right');
  await enCtx.close();
});

test('a saved choice beats the header, through the browser', async ({ browser }) => {
  // The precedence is unit-tested in Go; this proves the COOKIE the picker
  // writes is the cookie the server reads. A name mismatch would leave the
  // picker looking broken (choose German, get English on reload) with no error
  // anywhere — which is exactly the defect this test exists to catch.
  const ctx = await browser.newContext({ locale: 'en-US' });
  await ctx.addCookies([{ name: 'tollgate_i18n', value: 'de', url: BASE }]);
  const page = await ctx.newPage();
  await page.goto(BASE);

  // The header says English; the saved choice wins.
  await expect(page.locator('html')).toHaveAttribute('lang', 'de');
  // And the picker shows German as the current selection.
  await expect(page.locator('#lang-select')).toHaveValue('de');

  // Switching through the picker persists and survives a reload.
  await page.selectOption('#lang-select', 'ar');
  await expect(page.locator('html')).toHaveAttribute('lang', 'ar');
  await expect(page.locator('html')).toHaveAttribute('dir', 'rtl');
  await page.reload();
  await expect(page.locator('html'), 'saved choice survives a reload').toHaveAttribute('lang', 'ar');
  await ctx.close();
});
