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
      const fixture = { ...managedPublicPricing, data: [...managedPublicPricing.data, ...(count === 37 ? embeddings : [])] };
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
      await expect(page.getByText(`${count} 个公开模型`, { exact: true })).toBeVisible();
      await expect(page.locator('.catalog-table tbody tr')).toHaveCount(count);
      for (const model of fixture.data) await expect(page.getByText(model.model_name, { exact: true })).toHaveCount(1);
      for (const vendor of new Set(fixture.data.map((model) => model.vendor_name))) await expect(page.getByRole('heading', { name: vendor, exact: true })).toBeVisible();
      await expect(page.getByText('当前没有可展示的公开模型。', { exact: true })).toHaveCount(0);
      await expect(page.getByText('按规则计费', { exact: true })).toHaveCount(0);
      const claude = page.locator('tr').filter({ has: page.getByText('zt-claude-haiku-4.5', { exact: true }) });
      await expect(claude.getByRole('listitem')).toHaveCount(6);
      await expect(claude.getByText('$1.3 / 1M tokens', { exact: true })).toBeVisible();
      await expect(claude.getByText('$0.13 / 1M tokens', { exact: true })).toBeVisible();
      await page.waitForLoadState('networkidle');
      const layout = await page.evaluate(() => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        brokenImages: [...document.images].filter((img) => !img.complete || !img.naturalWidth).length,
        scrollableTables: [...document.querySelectorAll('.console-table-wrap')].filter((el) => el.scrollWidth > el.clientWidth).length,
      }));
      assert.equal(layout.overflow, false);
      assert.equal(layout.brokenImages, 0);
      let screenshot = path.join(evidence, `public-models-${count}-${width}.png`);
      await page.screenshot({ path: screenshot, fullPage: true });
      report.screenshots.push(screenshot);
      if (count === 37) {
        for (const [name, price] of [['zt-text-embedding-ada-002', '$0.13 / 1M tokens'], ['zt-text-embedding-3-small', '$0.026 / 1M tokens']]) {
          const row = page.locator('tr').filter({ has: page.getByText(name, { exact: true }) });
          await expect(row.getByRole('listitem')).toHaveCount(1);
          await expect(row.getByText('输入', { exact: true })).toBeVisible();
          await expect(row.getByText('输出', { exact: true })).toHaveCount(0);
          await expect(row).not.toContainText('$0 /');
          const priceCell = row.getByText(price, { exact: true });
          await priceCell.scrollIntoViewIfNeeded();
          await priceCell.evaluate((element) => {
            const scroller = element.closest('.console-table-wrap');
            scroller.scrollLeft += element.getBoundingClientRect().left - scroller.getBoundingClientRect().left - 16;
          });
          await expect(priceCell).toBeInViewport();
          const visibleText = await priceCell.evaluate((element) => {
            const range = document.createRange();
            range.selectNodeContents(element);
            const text = range.getBoundingClientRect();
            const scroller = element.closest('.console-table-wrap').getBoundingClientRect();
            return text.left >= Math.max(0, scroller.left) && text.right <= Math.min(window.innerWidth, scroller.right);
          });
          assert.equal(visibleText, true, 'complete embedding price must be visible after horizontal scrolling');
        }
        screenshot = path.join(evidence, `public-embedding-prices-${width}.png`);
        await page.screenshot({ path: screenshot });
        report.screenshots.push(screenshot);
      }
      if (width < 500) {
        assert.equal(layout.scrollableTables, 7);
        const table = page.getByRole('region', { name: 'OpenAI 模型价格表格', exact: true });
        await table.evaluate((el) => { el.scrollLeft = el.scrollWidth; });
        assert.ok(await table.evaluate((el) => el.scrollLeft > 0));
        await table.evaluate((el) => { el.scrollLeft = 0; });
        assert.equal(await table.evaluate((el) => el.scrollLeft), 0);
      }
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
