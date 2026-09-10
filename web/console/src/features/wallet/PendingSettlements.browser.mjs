/* global document:readonly, window:readonly */
import assert from 'node:assert/strict';
import console from 'node:console';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const evidence = path.resolve(process.argv[2] ?? path.join(root, '../../output/playwright/wallet-pending'));
await mkdir(evidence, { recursive: true });
const report = { status: 'running', blockedRequests: [], viewports: [] };
let browser;
let server;
const item = (id) => ({ id, request_id: id === 1 ? `request-${'x'.repeat(120)}` : `request-${id}`,
  model: id === 1 ? `zt-${'m'.repeat(197)}` : 'zt-offline-model', status: id === 1 ? 'reserved' : 'pending',
  reserved_quota: 250_000, created_at: '2026-09-07T00:00:00Z', updated_at: '2026-09-07T00:00:00Z' });

try {
  server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  for (const viewport of [{ width: 1440, height: 1000 }, { width: 390, height: 844 }, { width: 320, height: 568 }]) {
    const context = await browser.newContext({ viewport, serviceWorkers: 'block' });
    let items = [item(1), item(2)];
    let invalid = false;
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url());
      const respond = (data) => route.fulfill({ json: { success: true, data } });
      if (url.origin !== origin) {
        report.blockedRequests.push(url.origin);
        return route.abort();
      }
      switch (url.pathname) {
        case '/api/auth/refresh': return respond({ access_token: 'offline-wallet-browser', expires_in: 900,
          user: { id: 7, username: 'offline-review', role: 1, group: 'default' } });
        case '/api/user/self': return respond({ quota: 1_000_000 });
        case '/api/status': return respond({ quota_per_unit: 100_000 });
        case '/api/user/topup/info': return respond({ enable_usdt_trc20_topup: false,
          usdt_trc20_network: 'tron-mainnet', usdt_trc20_asset: 'USDT', usdt_trc20_min_topup: 10, usdt_trc20_order_ttl_seconds: 600 });
        case '/api/user/topup/self': return respond({ page: 1, page_size: 5, total: 0, items: [] });
        case '/api/user/self/pending-settlements': {
          assert.equal(route.request().method(), 'GET');
          if (invalid) return respond({ items: [] });
          const after = Number(url.searchParams.get('after_id'));
          assert.equal(url.searchParams.get('limit'), '21');
          const selected = items.filter((entry) => entry.id > after).slice(0, 21);
          return respond({ items: selected, next_after_id: selected.at(-1)?.id ?? after });
        }
        default:
          if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/v1/')) {
            report.blockedRequests.push(url.pathname);
            return route.abort();
          }
          return route.continue();
      }
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/console/wallet`);
    const holds = page.getByRole('region', { name: '待核账预留' });
    await expect(holds.getByText('预留中', { exact: true })).toBeVisible();
    await expect(holds.getByText('待核账', { exact: true })).toBeVisible();
    await expect(holds.getByText('$2.50', { exact: true })).toHaveCount(2);
    await expect(page.getByRole('region', { name: '账户余额' }).getByText('$10.00')).toBeVisible();
    const layout = await page.evaluate(() => {
      const issues = [];
      if (document.documentElement.scrollWidth > window.innerWidth + 1) issues.push('page overflow');
      for (const cell of document.querySelectorAll('#pending-settlements-heading ~ *, .wallet-history td')) {
        if (cell.scrollWidth > cell.clientWidth + 1) issues.push(`cell overflow: ${cell.tagName} ${cell.textContent.slice(0, 40)} ${cell.scrollWidth}/${cell.clientWidth}`);
      }
      const overlaps = (elements) => {
        const rects = elements.map((element) => element.getBoundingClientRect());
        for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) {
          if (Math.min(rects[i].right, rects[j].right) - Math.max(rects[i].left, rects[j].left) > 1 &&
            Math.min(rects[i].bottom, rects[j].bottom) - Math.max(rects[i].top, rects[j].top) > 1) issues.push('overlap');
        }
      };
      overlaps([...document.querySelectorAll('.wallet-page > section, .wallet-page > header')]);
      for (const row of document.querySelectorAll('.wallet-history tbody tr')) overlaps([...row.querySelectorAll('td')]);
      return issues;
    });
    const screenshot = path.join(evidence, `wallet-${viewport.width}.png`);
    await page.screenshot({ path: screenshot, fullPage: true });
    assert.deepEqual(layout, []);
    items = Array.from({ length: 21 }, (_, index) => item(index + 1));
    await holds.getByRole('button', { name: '刷新预留记录' }).click();
    await expect(holds.getByText('request-20', { exact: true })).toBeVisible();
    await holds.getByRole('button', { name: '下一页预留记录' }).click();
    await expect(holds.getByText('request-21', { exact: true })).toBeVisible();
    await expect(holds.getByRole('button', { name: '下一页预留记录' })).toBeDisabled();
    invalid = true;
    await holds.getByRole('button', { name: '刷新预留记录' }).click();
    await expect(holds.getByText('预留记录加载失败，请刷新重试。')).toBeVisible();
    await expect(page).toHaveURL(`${origin}/console/wallet`);
    await expect(page.getByRole('region', { name: '账户余额' }).getByText('$10.00')).toBeVisible();
    invalid = false;
    items = [];
    await holds.getByRole('button', { name: '刷新预留记录' }).click();
    await expect(holds.getByText('暂无待核账预留。')).toBeVisible();
    assert.deepEqual(errors, []);
    report.viewports.push({ ...viewport, screenshot, status: 'passed' });
    await context.close();
  }
  assert.deepEqual(report.blockedRequests, []);
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  report.error = error.message;
  process.exitCode = 1;
} finally {
  await browser?.close();
  if (server) await new Promise((resolve) => server.httpServer.close(resolve));
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}
