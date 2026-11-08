// @vitest-environment node
import { chromium, type Browser } from '@playwright/test';
import react from '@vitejs/plugin-react';
import { createServer, type ViteDevServer } from 'vite';
import { afterAll, beforeAll, expect, it } from 'vitest';
import { imageCatalogItem, imageDisplayFX, publicImagePricing } from './image-sale.fixture';

let server: ViteDevServer;
let browser: Browser;
let origin: string;

beforeAll(async () => {
  server = await createServer({
    configFile: false, envFile: false, plugins: [react()], logLevel: 'silent',
    server: { host: '127.0.0.1', port: 0, hmr: false },
  });
  await server.listen();
  const address = server.httpServer!.address();
  if (!address || typeof address === 'string') throw new Error('Local test server unavailable');
  origin = `http://127.0.0.1:${address.port}`;
  browser = await chromium.launch({ channel: process.platform === 'win32' ? 'chrome' : undefined, headless: true });
}, 30_000);

afterAll(async () => { await browser?.close(); await server?.close(); });

const cases = [1440, 390, 320].flatMap((width) => [false, true].flatMap((account) =>
  ['zh-CN', 'en'].map((locale) => ({ width, account, locale }))));

it.each(cases)('keeps image prices and terms within the viewport: $width / account=$account / $locale', async ({ width, account, locale }) => {
  const context = await browser.newContext({ viewport: { width, height: 900 }, locale, serviceWorkers: 'block' });
  const unexpected: string[] = [];
  const errors: string[] = [];
  try {
    await context.addInitScript((language) => localStorage.setItem('ztapi.locale', language), locale);
    await context.route('**/*', (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== origin) { unexpected.push(request.url()); return route.abort(); }
      const item = imageCatalogItem();
      const bodies: Record<string, unknown> = {
        '/api/pricing': publicImagePricing,
        '/api/status': { success: true, data: { quota_per_unit: 500_000, usd_exchange_rate: 7.3, display_fx: imageDisplayFX } },
        '/api/display-fx': { success: true, data: imageDisplayFX },
        '/api/spending/alerts': { success: true, data: { items: [], unread_count: 0 } },
        '/api/user/models': { success: true, data: [item.model_name], catalog: [item], display_cny_per_unit: 7.3, display_fx: imageDisplayFX },
        '/api/auth/refresh': { success: true, data: { access_token: 'offline-fixture-only', expires_in: 900,
          user: { id: 7, username: 'fixture', role: 1, group: 'default' } } },
      };
      if (url.pathname in bodies) return route.fulfill({ json: bodies[url.pathname] });
      if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1')) {
        unexpected.push(url.pathname); return route.abort();
      }
      return route.continue();
    });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    page.setDefaultNavigationTimeout(20_000);
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/${account ? 'console/models' : 'models'}`);
    const sale = page.getByRole('list', { name: `zt-image-per-picture ${locale === 'en' ? 'per-image prices' : '按张售价'}` });
    if (!account) {
      await page.getByRole('article', { name: 'zt-image-per-picture' }).locator('summary').click();
    }
    await sale.waitFor({ state: 'visible' });
    for (const currency of [locale === 'en' ? 'CNY' : '人民币', 'U']) {
      await page.getByRole('button', { name: currency, exact: true }).click();
      const layout = await page.evaluate(() => ({
        pageOverflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        clippedPrices: [...document.querySelectorAll('.model-image-sale, .model-image-sale li, .model-image-sale p, .catalog-model summary')]
          .filter((element) => element.scrollWidth > element.clientWidth + 1).length,
      }));
      expect(layout).toEqual({ pageOverflow: false, clippedPrices: 0 });
    }
    expect(unexpected).toEqual([]);
    expect(errors).toEqual([]);
  } finally {
    await context.close();
  }
}, 30_000);
