/* global document:readonly, window:readonly */
import assert from 'node:assert/strict';
import console from 'node:console';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';

const root = fileURLToPath(new URL('../../../', import.meta.url));
assert.ok(process.argv[2] && process.argv[3], 'provide built dist and separate evidence paths');
const dist = path.resolve(process.argv[2]), evidence = path.resolve(process.argv[3]);
await mkdir(evidence, { recursive: true });
const fixture = JSON.parse(await readFile(new URL('./live-pricing-20261107.fixture.json', import.meta.url), 'utf8'));
const benchmark = JSON.parse(await readFile(new URL('../../../../../server/model/ztapi_official_comparison_20261107.json', import.meta.url), 'utf8'));
for (const item of fixture.data) {
  const original = benchmark.models.find((entry) => entry.public_name === item.model_name);
  if (item.official_reference && original) Object.assign(item.official_reference, original.reference);
}
const sourceCommit = '1111111111111111111111111111111111111111';
const source = { success: true, data: { license: 'AGPL-3.0-or-later', source_commit: sourceCommit,
  source_repository: 'https://github.com/ffff582/ztapi-source', source_url: `https://github.com/ffff582/ztapi-source/tree/${sourceCommit}` } };
const report = { status: 'running', fixtureOnly: true, paidCalls: 0, externalRequests: [], unexpectedRequests: [], browserErrors: [], cases: [], screenshots: [] };
let browser, server;
try {
  server = await preview({ root, configFile: false, build: { outDir: dist }, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const width of [1440, 390, 320]) {
    let rate = 7.3, sourceAvailable = true;
    const context = await browser.newContext({ locale: 'zh-CN', viewport: { width, height: width === 1440 ? 1000 : 844 }, serviceWorkers: 'block' });
    await context.route('**/*', (route) => {
      const request = route.request(), url = new URL(request.url());
      if (url.origin !== origin) { report.externalRequests.push(url.href); return route.abort(); }
      if (url.pathname === '/api/auth/refresh') return route.fulfill({ status: 401, json: { success: false } });
      if (url.pathname === '/api/pricing') return route.fulfill({ json: fixture });
      if (url.pathname === '/api/status') return route.fulfill({ json: { success: true, data: { quota_per_unit: 500000, usd_exchange_rate: rate } } });
      if (url.pathname === '/.well-known/source') {
        assert.equal(request.method(), 'GET');
        assert.equal(request.headers().authorization, undefined);
        assert.equal(request.headers().cookie, undefined);
        return sourceAvailable ? route.fulfill({ json: source }) : route.fulfill({ status: 503, json: { success: false } });
      }
      if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1')) { report.unexpectedRequests.push(url.pathname); return route.abort(); }
      return route.continue();
    });
    const page = await context.newPage();
    page.on('pageerror', (error) => report.browserErrors.push(error.message));
    page.on('console', (message) => {
      if (message.type() === 'error' && !message.text().includes('401') && !message.text().includes('503')) report.browserErrors.push(message.text());
    });
    const capture = async (name) => {
      await page.waitForLoadState('networkidle');
      const layout = await page.evaluate(() => ({
        overflow: document.documentElement.scrollWidth > window.innerWidth + 1,
        brokenImages: [...document.images].filter((image) => !image.complete || !image.naturalWidth).length,
        clipped: [...document.querySelectorAll('.price-currency, .price-currency__switch, .docs-source-offer, .catalog-model__metric')].filter((element) => element.scrollWidth > element.clientWidth + 1).length,
      }));
      assert.deepEqual(layout, { overflow: false, brokenImages: 0, clipped: 0 });
      const file = path.join(evidence, `${name}-${width}.png`);
      await page.screenshot({ path: file });
      report.cases.push({ name, width, status: 'passed', layout }); report.screenshots.push(file);
    };

    await page.goto(`${origin}/models`);
    await expect(page.getByRole('article')).toHaveCount(16);
    await expect(page.getByRole('button', { name: '人民币', exact: true })).toHaveAttribute('aria-pressed', 'true');
    const opus = page.getByRole('article', { name: 'zt-claude-opus-5', exact: true });
    await expect(opus.getByText('18.2829 元 / 1M tokens', { exact: true }).first()).toBeVisible();
    await expect(page.getByText(/1U = 7.3 元/)).toBeVisible();
    await capture('catalog-cny');
    for (const article of await page.getByRole('article').all()) {
      await article.locator('summary').click();
      await expect(article.locator('details')).toHaveAttribute('open', '');
    }
    await expect(page.locator('.catalog-model__price-list > li')).toHaveCount(78);
    const pro = page.getByRole('article', { name: 'zt-deepseek-v4-pro', exact: true });
    await expect(pro.locator('.catalog-model__official').filter({ hasText: '9 元 / 1M tokens' })).toBeVisible();
    await expect(pro.getByText('高于官网 31.38%', { exact: true }).first()).toBeVisible();
    const glm = page.getByRole('article', { name: 'zt-glm-5', exact: true });
    await expect(glm.locator('.catalog-model__official').filter({ hasText: '4 元 / 1M tokens' })).toBeVisible();
    await pro.scrollIntoViewIfNeeded(); await capture('native-cny-reference');
    await page.getByRole('button', { name: 'U', exact: true }).click();
    await expect(opus.getByText('2.5045 U / 1M tokens', { exact: true }).first()).toBeVisible();
    await opus.scrollIntoViewIfNeeded(); await capture('catalog-u');
    await page.getByRole('button', { name: '人民币', exact: true }).click();
    await page.getByRole('button', { name: '切换到英文' }).click();
    await expect(page.getByRole('button', { name: 'CNY', exact: true })).toHaveAttribute('aria-pressed', 'true');
    await expect(opus.getByText('18.2829 CNY / 1M tokens', { exact: true }).first()).toBeVisible();
    await capture('catalog-cny-english');
    await page.getByRole('button', { name: 'Switch to Chinese' }).click();
    rate = 0;
    await page.reload();
    await expect(page.getByRole('article')).toHaveCount(16);
    await expect(page.getByRole('button', { name: '人民币', exact: true })).toBeDisabled();
    await expect(page.getByText('人民币参考汇率暂不可用，显示 U 原价。')).toBeVisible();
    await capture('catalog-unavailable-fx');
    rate = 7.3;

    await page.goto(`${origin}/`);
    await expect(page.getByText('16 个实时公开模型', { exact: true })).toBeVisible();
    await page.locator('.model-proof').scrollIntoViewIfNeeded();
    await expect(page.locator('.model-proof__grid')).toContainText('元 / 1M tokens');
    await capture('homepage-cny');
    await page.goto(`${origin}/docs/resource-disclosure`);
    await expect(page.getByRole('link', { name: '前端源码', exact: true })).toHaveCount(0);
    await page.locator('.docs-footer').getByRole('link', { name: '开源许可', exact: true }).click();
    await expect(page.getByRole('heading', { level: 1, name: '开源许可', exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: '获取当前版本对应源码' })).toHaveAttribute('href', source.data.source_url);
    await expect(page.getByText('Frontend design and development by New API contributors.', { exact: true })).toBeVisible();
    await capture('license-current-source');
    sourceAvailable = false;
    await page.reload();
    await expect(page.getByRole('alert')).toContainText('当前版本对应源码暂不可用');
    await expect(page.getByRole('link', { name: '获取当前版本对应源码' })).toHaveCount(0);
    await capture('license-failure');
    sourceAvailable = true;
    await page.getByRole('button', { name: '重新加载对应源码' }).click();
    await expect(page.getByRole('link', { name: '获取当前版本对应源码' })).toHaveAttribute('href', source.data.source_url);
    await page.getByRole('button', { name: '切换到英文' }).click();
    await expect(page.getByRole('heading', { level: 1, name: 'Open-source license', exact: true })).toBeVisible();
    await capture('license-retry-english');
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
