// @vitest-environment node
import { chromium, type Browser } from '@playwright/test';
import react from '@vitejs/plugin-react';
import { createServer, type ViteDevServer } from 'vite';
import { afterAll, beforeAll, expect, it } from 'vitest';
import { imageCatalogItem, imageDisplayFX, publicImagePricing, partialImageItem, publicImageItem } from './image-sale.fixture';

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

it.each([1440, 390, 320])('keeps partial image workbench usable at %s without paid calls', async (width) => {
  const context = await browser.newContext({ viewport: { width, height: 900 }, serviceWorkers: 'block' });
  const unexpected: string[] = [];
  const errors: string[] = [];
  try {
    await context.addInitScript(() => localStorage.setItem('ztapi.locale', 'zh-CN'));
    await context.route('**/*', (route) => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) { unexpected.push(url.href); return route.abort(); }
      const item = partialImageItem();
      const bodies: Record<string, unknown> = {
        '/api/user/models': { success: true, data: [item.model_name], catalog: [item] },
        '/api/auth/refresh': { success: true, data: { access_token: 'offline-fixture-only', expires_in: 900, user: { id: 7, username: 'fixture', role: 1, group: 'default' } } },
        '/api/spending/alerts': { success: true, data: { items: [], unread_count: 0 } },
        '/api/token/': { success: true, data: { page: 1, page_size: 100, total: 1, items: [{ id: 1, user_id: 7, status: 1, name: 'fixture', created_time: 1, accessed_time: 0, expired_time: -1, remain_quota: 100, unlimited_quota: true, model_limits_enabled: false, model_limits: '', allow_ips: '', key_prefix: 'sk-zt-offline...' }] } },
      };
      if (url.pathname in bodies) return route.fulfill({ json: bodies[url.pathname] });
      if (/^\/(api|pg|v1)/.test(url.pathname)) { unexpected.push(url.pathname); return route.abort(); }
      return route.continue();
    });
    const page = await context.newPage();
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/console/workbench/image`);
    await page.getByLabel('图片提示词').waitFor();
    expect(await page.getByLabel('图片数量').locator('option').allTextContents()).toEqual(['1']);
    expect(await page.getByLabel('图片分辨率').locator('option').allTextContents()).toEqual(['1K', '2K']);
    await page.getByLabel('图片提示词').fill('中'.repeat(2001));
    await page.getByRole('button', { name: '生成图片', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: '超过 2000' }).waitFor();
    await page.getByLabel('图片分辨率').selectOption('2K');
    const png = await page.evaluate(async () => {
      const canvas = document.createElement('canvas'); canvas.width = canvas.height = 16;
      return Array.from(new Uint8Array(await (await new Promise<Blob>((resolve) => canvas.toBlob((blob) => resolve(blob!)))).arrayBuffer()));
    });
    await page.getByLabel('上传参考图').setInputFiles({ name: 'reference.png', mimeType: 'image/png', buffer: Buffer.from(png) });
    await page.getByAltText('参考图 1').waitFor();
    expect(await page.getByLabel('图片分辨率').locator('option').allTextContents()).toEqual(['1K']);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
    expect(await page.locator('.zt-workbench__config').evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
    await page.screenshot({ path: `output/image-sale-v2-workbench-${width}.png`, fullPage: true });
    expect(unexpected).toEqual([]);
    expect(errors).toEqual([]);
  } finally { await context.close(); }
}, 30_000);

afterAll(async () => { await browser?.close(); await server?.close(); }, 30_000);

const cases = [1440, 390, 320].flatMap((width) => [false, true].flatMap((account) =>
  ['zh-CN', 'en'].flatMap((locale) => [false, true].map((partial) => ({ width, account, locale, partial })))));

it.each(cases)('keeps image prices and terms within the viewport: $width / account=$account / $locale / v2=$partial', async ({ width, account, locale, partial }) => {
  const context = await browser.newContext({ viewport: { width, height: 900 }, locale, serviceWorkers: 'block' });
  const unexpected: string[] = [];
  const errors: string[] = [];
  try {
    await context.addInitScript((language) => localStorage.setItem('ztapi.locale', language), locale);
    await context.route('**/*', (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== origin) { unexpected.push(request.url()); return route.abort(); }
      const item = partial ? partialImageItem() : imageCatalogItem();
      const bodies: Record<string, unknown> = {
        '/api/pricing': partial ? { ...publicImagePricing, data: [publicImageItem(item)] } : publicImagePricing,
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
    expect(await sale.locator('li').count()).toBe(partial ? 2 : 3);
    if (partial) expect(await sale.textContent()).not.toContain('4K');
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
    if (partial && locale === 'zh-CN') await page.screenshot({ path: `output/image-sale-v2-${account ? 'account' : 'public'}-${width}.png`, fullPage: true });
  } finally {
    await context.close();
  }
}, 30_000);
