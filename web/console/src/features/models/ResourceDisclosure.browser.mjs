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
const base = managedPublicPricing.data[0];
const mixed = {
  ...base, model_name: 'zt-mock-mixed', input_price_per_million: '6', output_price_per_million: '24',
  billing_dimensions: ['input_tokens', 'output_tokens', 'cache_read'], billing_rule: 'multi_dimension',
  sale_usd: { input_tokens: '6', output_tokens: '24', cache_read: '1' },
  official_usd: { input_tokens: '10', output_tokens: '20' },
  official_reference: { url: 'https://example.com/pricing', checked_at: '2026-11-07', kind: 'official_snapshot', note: '固定高峰基准；非高峰官网价格减半。' },
};
const quoted = {
  ...mixed, model_name: 'zt-mock-quoted',
  official_reference: { ...mixed.official_reference, kind: 'supplier_quoted_official', note: '报价快照，不是当前官网独立核验。' },
};
const fixture = { ...managedPublicPricing, data: [mixed, quoted] };
const report = { status: 'running', fixtureOnly: true, externalRequests: [], unexpectedRequests: [], browserErrors: [], screenshots: [], cases: [] };
let browser, server;
try {
  server = await preview({ root, configFile: false, build: { outDir: dist }, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const width of [1440, 390, 320]) {
    const context = await browser.newContext({ locale: 'zh-CN', viewport: { width, height: width === 1440 ? 1000 : 844 }, serviceWorkers: 'block' });
    await context.route('**/*', (route) => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) { report.externalRequests.push(url.href); return route.abort(); }
      if (url.pathname === '/api/auth/refresh') return route.fulfill({ status: 401, json: { success: false } });
      if (url.pathname === '/api/pricing') return route.fulfill({ json: fixture });
      if (url.pathname === '/api/status') return route.fulfill({ json: { success: true, data: { quota_per_unit: 250000 } } });
      if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1')) { report.unexpectedRequests.push(url.pathname); return route.abort(); }
      return route.continue();
    });
    const page = await context.newPage();
    page.on('pageerror', (error) => report.browserErrors.push(error.message));
    page.on('console', (message) => { if (message.type() === 'error' && !message.text().includes('401')) report.browserErrors.push(message.text()); });
    const capture = async (name) => {
      const layout = await page.evaluate(() => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        brokenImages: [...document.images].filter((img) => !img.complete || !img.naturalWidth).length,
        clipped: [...document.querySelectorAll('.resource-disclosure, .official-price-reference, .catalog-model summary')]
          .filter((el) => el.scrollWidth > el.clientWidth + 1).length,
      }));
      assert.deepEqual(layout, { overflow: false, brokenImages: 0, clipped: 0 });
      const screenshot = path.join(evidence, `${name}-${width}.png`);
      await page.screenshot({ path: screenshot, fullPage: name === 'resource-docs' });
      report.screenshots.push(screenshot);
      report.cases.push({ name, width, status: 'passed', layout });
    };
    await page.goto(origin);
    const notice = page.getByRole('note', { name: '资源来源说明' });
    await expect(notice).toBeInViewport();
    const headerBox = await page.locator('.public-header').boundingBox();
    const noticeBox = await notice.boundingBox();
    assert.ok(headerBox && noticeBox && noticeBox.y >= headerBox.y + headerBox.height, 'disclosure must not sit underneath the fixed public header');
    await expect(notice).toContainText('部分模型使用上游反代资源，并非全部为原厂 API 直连');
    await expect(page.getByText('综合优惠约 20%', { exact: true })).toHaveCount(0);
    await page.waitForLoadState('networkidle');
    await capture('homepage-disclosure');
    await notice.getByRole('link', { name: '了解资源来源与价格说明' }).click();
    await expect(page.getByRole('heading', { level: 1, name: '资源来源与价格说明' })).toBeVisible();
    await expect(page.getByRole('article')).toContainText('ZTAPI 不主动将所选型号替换为其他型号或静默降级');
    await capture('resource-docs');
    await page.getByRole('link', { name: '查看当前模型与价格', exact: true }).click();
    await expect(page.getByRole('note', { name: '资源来源说明' })).toBeInViewport();
    await expect(page.getByRole('article')).toHaveCount(2);
    const mixedRow = page.getByRole('article', { name: mixed.model_name });
    await mixedRow.locator('summary').click();
    await expect(mixedRow.getByText('高于官网 20%', { exact: true })).toBeVisible();
    await expect(mixedRow.getByText('低于官网 40%', { exact: true })).toBeVisible();
    await expect(mixedRow.locator('.catalog-model__official').filter({ hasText: '暂无官网参考' })).toBeVisible();
    await expect(mixedRow.getByRole('link', { name: '官网价格参考', exact: true })).toHaveAttribute('href', 'https://example.com/pricing');
    await expect(mixedRow.getByText('2026-11-07', { exact: true })).toBeVisible();
    await expect(mixedRow.locator('.catalog-model__discount')).toHaveCount(0);
    await mixedRow.scrollIntoViewIfNeeded();
    await capture('mixed-official-comparisons');
    const quotedRow = page.getByRole('article', { name: quoted.model_name });
    await quotedRow.locator('summary').click();
    await expect(quotedRow.getByRole('link', { name: '报价表官网基准', exact: true })).toBeVisible();
    await expect(quotedRow.getByText('高于报价基准 20%', { exact: true })).toBeVisible();
    await expect(quotedRow).toContainText('供应商报价基准，不代表当前官网价格已独立核验。');
    await quotedRow.scrollIntoViewIfNeeded();
    await capture('supplier-quoted-reference');
    await page.getByRole('note', { name: '资源来源说明' }).getByRole('link').click();
    await expect(page).toHaveURL(`${origin}/docs/resource-disclosure`);
    await page.getByRole('button', { name: '切换到英文' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Resource sources and pricing' })).toBeVisible();
    await capture('resource-docs-english');
    await context.close();
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
