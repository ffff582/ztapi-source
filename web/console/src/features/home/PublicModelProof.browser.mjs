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
const dist = path.resolve(process.argv[2]);
const evidence = path.resolve(process.argv[3]);
await mkdir(evidence, { recursive: true });
const fixtureSource = await readFile(new URL('./public-pricing.fixture.ts', import.meta.url), 'utf8');
const fixtureCode = ts.transpileModule(fixtureSource, { compilerOptions: { module: ts.ModuleKind.ESNext } }).outputText;
const { managedPublicPricing } = await import(`data:text/javascript;base64,${Buffer.from(fixtureCode).toString('base64')}`);
const report = { status: 'running', fixtureOnly: true, dist, catalogCount: managedPublicPricing.data.length,
  externalRequests: [], unexpectedRequests: [], browserErrors: [], screenshots: [], viewports: [] };
let browser, server;
try {
  server = await preview({ root, configFile: false, build: { outDir: dist }, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const width of [1440, 390, 320]) {
    const context = await browser.newContext({ viewport: { width, height: width === 1440 ? 1000 : 844 }, serviceWorkers: 'block' });
    await context.route('**/*', (route) => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) { report.externalRequests.push(url.origin); return route.abort(); }
      if (url.pathname === '/api/auth/refresh') return route.fulfill({ status: 401, json: { success: false, message: 'Unauthenticated' } });
      if (url.pathname === '/api/pricing') return route.fulfill({ json: managedPublicPricing });
      if (url.pathname === '/api/status') return route.fulfill({ json: { success: true, data: { quota_per_unit: 250000 } } });
      if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/')) {
        report.unexpectedRequests.push(url.pathname); return route.abort();
      }
      return route.continue();
    });
    const page = await context.newPage();
    page.on('pageerror', (error) => report.browserErrors.push(error.message));
    page.on('console', (message) => {
      if (message.type() === 'error' && !message.text().includes('401')) report.browserErrors.push(message.text());
    });
    await page.goto(origin);
    await expect(page.getByText('35 个实时公开模型', { exact: true })).toBeVisible();
    await expect(page.getByText('模型目录正在配置，开放后将在这里展示实时价格。', { exact: true })).toHaveCount(0);
    const proof = page.locator('.model-proof');
    await expect(proof.locator('article')).toHaveCount(3);
    for (const [name, price] of [['zt-claude-haiku-4.5', '$1.3 / 1M tokens'], ['zt-gpt-4.1', '$2.6 / 1M tokens']]) {
      const card = proof.locator('article').filter({ has: page.getByText(name, { exact: true }) });
      await expect(card.getByText(price, { exact: true })).toBeVisible();
      await expect(card.getByText('按规则计费', { exact: true })).toHaveCount(0);
    }
    await page.waitForLoadState('networkidle');
    const layout = await page.evaluate(() => {
      const cards = [...document.querySelectorAll('.model-proof__grid article')];
      const overlaps = [];
      for (let i = 0; i < cards.length; i++) for (let j = i + 1; j < cards.length; j++) {
        const a = cards[i].getBoundingClientRect(), b = cards[j].getBoundingClientRect();
        if (Math.min(a.right, b.right) > Math.max(a.left, b.left) + 1 && Math.min(a.bottom, b.bottom) > Math.max(a.top, b.top) + 1) overlaps.push(`${i}/${j}`);
      }
      return { overflow: document.documentElement.scrollWidth > window.innerWidth + 1, overlaps,
        brokenImages: [...document.images].filter((img) => !img.complete || !img.naturalWidth).length };
    });
    assert.deepEqual(layout, { overflow: false, overlaps: [], brokenImages: 0 });
    for (const name of ['home', 'public-catalog']) {
      const screenshot = path.join(evidence, `${name}-${width}.png`);
      const clip = name === 'home' ? undefined : await proof.boundingBox();
      if (name !== 'home') assert.ok(clip);
      await page.screenshot({ path: screenshot, fullPage: true, ...(clip ? { clip } : {}) });
      report.screenshots.push(screenshot);
    }
    report.viewports.push({ width, status: 'passed', layout });
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
