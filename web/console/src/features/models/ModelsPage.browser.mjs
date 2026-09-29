/* global document:readonly, window:readonly */
import assert from 'node:assert/strict';
import { Buffer } from 'node:buffer';
import console from 'node:console';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';
import ts from 'typescript';

const root = fileURLToPath(new URL('../../../', import.meta.url));
assert.ok(process.argv[2] && process.argv[3], 'provide separate built dist and evidence paths');
const dist = path.resolve(process.argv[2]), evidence = path.resolve(process.argv[3]);
await mkdir(evidence, { recursive: true });
const fixtureSource = await readFile(new URL('../home/public-pricing.fixture.ts', import.meta.url), 'utf8');
const fixtureCode = ts.transpileModule(fixtureSource, { compilerOptions: { module: ts.ModuleKind.ESNext } }).outputText;
const { managedPublicPricing } = await import(`data:text/javascript;base64,${Buffer.from(fixtureCode).toString('base64')}`);
const pricedFixtureModels = managedPublicPricing.data.map((model) => model.model_name === 'zt-gpt-4.1'
  ? { ...model, official_usd: Object.fromEntries(Object.entries(model.sale_usd).map(([key, value]) => [key, (Number(value) / 0.8).toFixed(10)])) }
  : model);
const embeddings = [['zt-text-embedding-ada-002', '0.1300000000'], ['zt-text-embedding-3-small', '0.0260000000']].map(([name, price]) => ({
  ...managedPublicPricing.data[0], model_name: name, provider_family: 'openai', vendor_name: 'OpenAI',
  supported_endpoint_types: ['embeddings'], input_price_per_million: price, output_price_per_million: '0.0000000000',
  billing_dimensions: ['input_tokens'], sale_usd: { input_tokens: price }, billing_rule: 'input_only',
}));
const report = { status: 'running', fixtureOnly: true, dist, externalRequests: [], unexpectedRequests: [], browserErrors: [], screenshots: [], cases: [] };
let browser, server;
try {
  server = await preview({ root, configFile: false, build: { outDir: dist }, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const width of [1440, 390, 320]) {
    for (const count of [35, 37]) {
      const fixture = { ...managedPublicPricing, data: [...pricedFixtureModels, ...(count === 37 ? embeddings : [])] };
      const context = await browser.newContext({ viewport: { width, height: width === 1440 ? 1000 : 844 }, serviceWorkers: 'block' });
      await context.route('**/*', (route) => {
        const url = new URL(route.request().url());
        if (url.origin !== origin) { report.externalRequests.push(url.origin); return route.abort(); }
        if (url.pathname === '/api/auth/refresh') return route.fulfill({ status: 401, json: { success: false, message: 'Unauthenticated' } });
        if (url.pathname === '/api/pricing') return route.fulfill({ json: fixture });
        if (url.pathname === '/api/status') return route.fulfill({ json: { success: true, data: { quota_per_unit: 250000 } } });
        if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/')) { report.unexpectedRequests.push(url.pathname); return route.abort(); }
        return route.continue();
      });
      const page = await context.newPage();
      page.on('pageerror', (error) => report.browserErrors.push(error.message));
      page.on('console', (message) => { if (message.type() === 'error' && !message.text().includes('401')) report.browserErrors.push(message.text()); });
      await page.goto(`${origin}/models`);
      await expect(page.getByText(`显示 ${count} / ${count}`, { exact: true })).toBeVisible();
      await expect(page.locator('.catalog-model')).toHaveCount(count);
      for (const model of fixture.data) await expect(page.getByText(model.model_name, { exact: true })).toHaveCount(1);
      for (const vendor of new Set(fixture.data.map((model) => model.vendor_name))) await expect(page.getByRole('heading', { name: vendor, exact: true })).toBeVisible();
      await expect(page.getByText('当前没有可展示的公开模型。', { exact: true })).toHaveCount(0);
      await expect(page.getByText('按规则计费', { exact: true })).toHaveCount(0);
      await expect(page.locator('.catalog-model').filter({ has: page.getByText('zt-gpt-4.1', { exact: true }) }).getByText('官方 8 折', { exact: true })).toBeVisible();
      const claude = page.locator('.catalog-model').filter({ has: page.getByText('zt-claude-haiku-4.5', { exact: true }) });
      await claude.locator('summary').click();
      await expect(claude.getByRole('listitem')).toHaveCount(6);
      await expect(claude.locator('.catalog-model__price-list').getByText('1.3 U / 1M tokens', { exact: true })).toBeVisible();
      await expect(claude.getByText('0.13 U / 1M tokens', { exact: true })).toBeVisible();
      if (width < 700) {
        await expect(claude.locator('.catalog-model__sale small').first()).toBeVisible();
        await expect(claude.locator('.catalog-model__official small').first()).toBeVisible();
        const detailScreenshot = path.join(evidence, `public-model-detail-${width}.png`);
        await claude.screenshot({ path: detailScreenshot });
        report.screenshots.push(detailScreenshot);
      }
      await page.waitForLoadState('networkidle');
      const layout = await page.evaluate(() => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        brokenImages: [...document.images].filter((img) => !img.complete || !img.naturalWidth).length,
        clippedSummaries: [...document.querySelectorAll('.catalog-model summary')].filter((el) => el.scrollWidth > el.clientWidth + 1).length,
        overflowElements: [...document.querySelectorAll('body *')].filter((el) => el.getBoundingClientRect().right > window.innerWidth + 1).slice(0, 6).map((el) => `${el.tagName.toLowerCase()}.${el.className?.baseVal ?? el.className}`),
      }));
      let screenshot = path.join(evidence, `public-models-${count}-${width}.png`);
      await page.screenshot({ path: screenshot, fullPage: true });
      report.screenshots.push(screenshot);
      await page.evaluate(() => window.scrollTo(0, 0));
      await page.screenshot({ path: path.join(evidence, `public-models-first-screen-${count}-${width}.png`) });
      report.lastLayout = { count, width, ...layout };
      assert.equal(layout.overflow, false);
      assert.equal(layout.brokenImages, 0);
      assert.equal(layout.clippedSummaries, 0);
      if (count === 37) {
        for (const [name, price] of [['zt-text-embedding-ada-002', '0.13 U / 1M tokens'], ['zt-text-embedding-3-small', '0.026 U / 1M tokens']]) {
          const row = page.locator('.catalog-model').filter({ has: page.getByText(name, { exact: true }) });
          await row.locator('summary').click();
          await expect(row.getByRole('listitem')).toHaveCount(1);
          await expect(row.locator('.catalog-model__price-list').getByText('输入', { exact: true })).toBeVisible();
          await expect(row.locator('.catalog-model__price-list').getByText('输出', { exact: true })).toHaveCount(0);
          await expect(row).not.toContainText('0 U /');
          const priceCell = row.locator('.catalog-model__price-list').getByText(price, { exact: true });
          await expect(priceCell).toBeInViewport();
        }
        screenshot = path.join(evidence, `public-embedding-prices-${width}.png`);
        await page.screenshot({ path: screenshot });
        report.screenshots.push(screenshot);
      }
      await page.getByRole('button', { name: '向量模型' }).click();
      await expect(page.locator('.catalog-model')).toHaveCount(count === 37 ? 2 : 0);
      await page.getByRole('button', { name: '全部模型' }).click();
      await expect(page.locator('.catalog-model')).toHaveCount(count);
      report.cases.push({ count, width, status: 'passed', layout });
      await context.close();
    }
  }
  assert.deepEqual(report.externalRequests, []);
  assert.deepEqual(report.unexpectedRequests, []);
  assert.deepEqual(report.browserErrors, []);
  report.status = 'passed';
} catch (error) {
  report.status = 'failed'; report.error = error.message; process.exitCode = 1;
} finally {
  await browser?.close();
  if (server) await new Promise((resolve) => server.httpServer.close(resolve));
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}
