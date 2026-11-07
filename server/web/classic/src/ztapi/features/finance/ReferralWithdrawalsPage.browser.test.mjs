import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { createServer } from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { referralBrowserLaunchOptions } from './referral-browser-launch-options.mjs';
import { build } from '../../../../../../../node_modules/.pnpm/node_modules/esbuild/lib/main.js';

const root = fileURLToPath(new URL('../../../../../../../', import.meta.url));
const here = path.dirname(fileURLToPath(import.meta.url));
const output = path.resolve(here, 'referral-browser-evidence');
const preview = process.argv.includes('--preview');
await mkdir(output, { recursive: true });
const bundle = await build({
  stdin: { contents: `
    import React from 'react';
    import { createRoot } from 'react-dom/client';
    import { BrowserRouter, useLocation } from 'react-router-dom';
    import AdminShell from '../../layout/AdminShell.jsx';
    import { AdminPermission, hasAdminPermission } from '../../auth/admin-permission-policy.js';
    import { setAdminSession } from '../../auth/admin-session.js';
    import ReferralWithdrawalsPage from './ReferralWithdrawalsPage.jsx';
    import OverviewPage from './OverviewPage.jsx';
    import '../../styles/admin.css';
    const role = Number(new URLSearchParams(location.search).get('role') || 100);
    const user = { id: 7, role, username: 'local-mock-admin' };
    setAdminSession({ access_token: 'LOCAL-ONLY-MOCK', expires_in: 3600, user });
    function App() {
      const { pathname } = useLocation();
      const allowed = hasAdminPermission(role, AdminPermission.referralsRead);
      return <AdminShell user={user} onLogout={() => {}}>{pathname === '/finance/overview'
        ? <OverviewPage /> : !allowed ? <p role='alert'>无权访问此页面</p>
        : <ReferralWithdrawalsPage view={pathname.endsWith('/withdrawals') ? 'withdrawals' : 'commissions'} canWrite={${!preview} && hasAdminPermission(role, AdminPermission.referralsWrite)} />}</AdminShell>;
    }
    createRoot(document.getElementById('root')).render(<BrowserRouter><App /></BrowserRouter>);
  `, resolveDir: here, loader: 'jsx' },
  bundle: true, write: false, outfile: path.resolve(here, 'referral-harness.js'),
  nodePaths: [path.resolve(root, 'node_modules/.pnpm/node_modules')],
  define: { 'process.env.NODE_ENV': '"production"', 'import.meta.env': '{"MODE":"test"}' },
  loader: { '.png': 'dataurl', '.svg': 'dataurl' },
});
const assets = Object.fromEntries(bundle.outputFiles.map((file) => [path.basename(file.path), file.contents]));
const server = createServer((request, response) => {
  const name = new URL(request.url, 'http://127.0.0.1').pathname.slice(1);
  if (name.startsWith('api/')) {
    if (!preview) { response.writeHead(500); response.end('Unmocked API'); return; }
    response.setHeader('Content-Type', 'application/json; charset=utf-8');
    if (request.method !== 'GET') { response.writeHead(403); response.end(JSON.stringify({ success: false, message: 'Local preview is read only' })); return; }
    const data = name.includes('finance-reporting/report') ? report
      : name.endsWith('/readiness') ? { readiness: { ready: false, schema_ready: true, worker_ready: false, finance_ready: false, blocked_reasons: ['local_mock_only', 'worker_not_ready'] }, worker: { enabled: false, running: false, last_heartbeat: 0, last_success: 0, last_error: '' } }
      : name === 'api/admin/referrals' ? { campaign, summary: {}, withdrawals: { items: [row()], total: 1 }, commissions: { items: [], total: 0 } } : null;
    response.writeHead(data ? 200 : 404); response.end(JSON.stringify({ success: !!data, data })); return;
  }
  if (assets[name]) { response.setHeader('Content-Type', name.endsWith('.css') ? 'text/css' : 'application/javascript'); response.end(assets[name]); return; }
  response.setHeader('Content-Type', 'text/html; charset=utf-8');
  response.end('<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/referral-harness.css"><div id="root"></div><script src="/referral-harness.js"></script></html>');
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const origin = `http://127.0.0.1:${server.address().port}`;
const errors = [], external = [], posts = [];
const campaign = { enabled: false, accepting_invites: false, blocked_reasons: ['supplier_cost_unknown', 'Flash_model_mapping_unknown'] };
let mode = 'normal', claimConflict = false, currentStatus = 'requested';
const row = () => ({ id: 12, version: currentStatus === 'requested' ? 3 : 4, amount: '20.000001', fee: '0.123456', net_amount: '19.876545', address: `T${'W'.repeat(33)}`, status: currentStatus, created_at: 1794000000, fee_expires_at: 1794001800, tx_id: currentStatus === 'submitted_unknown' ? 'a'.repeat(64) : '', note: 'local-only browser mock' });
const referral = { supported: true, status: 'available', accrued: '5.000000', refund_reversal: '-1.000000', net_commission_expense: '4.100000', manual_loss: '0.100000', frozen_payable: '9.000000', withdrawal_reserved: '20.000001', paid: '40.000000', platform_net_fee: null };
const report = { month: '2026-09', confirmed: false, period_closed: true, revenue: '20.000000', known_revenue: '20.000000', actual_cost: '15.000000', gross_profit: null, charged_quota: 10500000, quota_per_unit: 500000, receipts_total: '100.000000', pending_requests: 0, issue_count: 1, receipts: [], channels: [], manual_funding: [], issues: [], costs: [], referral };
if (preview) {
  console.log(JSON.stringify({ url: `${origin}/finance/referrals/withdrawals`, pid: process.pid, readonly: true, mockOnly: true }));
  await new Promise(() => {});
}
const browserLaunchOptions = referralBrowserLaunchOptions();
console.log(JSON.stringify({ event: 'browser_launch', platform: process.platform, options: browserLaunchOptions }));
const browser = await chromium.launch(browserLaunchOptions);
const context = await browser.newContext({ serviceWorkers: 'block' });
await context.route('**/*', async (route) => {
  const request = route.request(), url = new URL(request.url());
  if (url.origin !== origin) { external.push(request.url()); await route.abort(); return; }
  if (!url.pathname.startsWith('/api/')) { await route.continue(); return; }
  const send = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify({ success: status < 400, data }) });
  if (request.method() === 'POST') {
    posts.push({ path: url.pathname, body: request.postDataJSON() });
    if (url.pathname.endsWith('/claim') && claimConflict) { currentStatus = 'paying'; await send({}, 409); return; }
    if (url.pathname.endsWith('/quote')) { currentStatus = 'fee_quoted'; await send({ tx_id: 'hash-is-not-payment' }); return; }
    if (url.pathname.endsWith('/submitted')) { currentStatus = 'submitted_unknown'; await send({ tx_id: 'a'.repeat(64) }); return; }
    if (url.pathname.endsWith('/confirm-paid')) { await send({}, 409); return; }
    if (url.pathname.endsWith('/reject')) {
      assert.ok(['requested', 'fee_quoted', 'confirmed', 'manual_review'].includes(currentStatus));
      assert.equal(request.postDataJSON().version, row().version);
      currentStatus = 'rejected'; await send({}); return;
    }
    assert.fail(`Unexpected mutation: ${url.pathname}`);
  }
  if (url.pathname.includes('finance-reporting/report')) { await send(mode === 'legacy' ? { ...report, referral: undefined } : report); return; }
  if (url.pathname.endsWith('/readiness')) { await send({ readiness: { ready: false, schema_ready: true, worker_ready: false, finance_ready: false, price_gate: { allowed: false, blocked_reasons: ['internal/route/model/long-dimension:evidence_unknown'] }, blocked_reasons: ['worker_not_ready', 'finance_capability_unknown'] }, catalog_sha256: 'a'.repeat(64), required_dimension_keys: ['internal/route/model/long-dimension'], worker: { enabled: true, running: false, last_heartbeat: 1794000000, last_success: 0, last_error: 'reconciliation_unavailable' } }); return; }
  if (url.pathname === '/api/admin/referrals') {
    if (mode === 'error') { await send({}, 503); return; }
    const items = mode === 'empty' ? [] : [row()];
    await send({ campaign, summary: {}, withdrawals: { items, total: mode === 'empty' ? 0 : 42 }, commissions: { items: [], total: 0 } }); return;
  }
  assert.fail(`Unmocked API: ${url.pathname}`);
});
try {
  const page = await context.newPage();
  page.on('pageerror', (error) => errors.push(String(error)));
  for (const width of [1440, 390, 320]) {
    for (const lang of ['zh-CN', 'en']) {
      await page.addInitScript((language) => {
        Object.defineProperty(navigator, 'language', { configurable: true, value: language });
        Object.defineProperty(navigator, 'languages', { configurable: true, value: [language] });
      }, lang);
      await page.setViewportSize({ width, height: 960 });
      await page.goto(`${origin}/finance/referrals/withdrawals?lang=${lang}`);
      await expect(page.getByRole('cell', { name: row().address, exact: true })).toBeVisible();
      await expect(page.getByText('上游成本尚未核实', { exact: true })).toBeVisible();
      await expect(page.getByText('Supplier cost is unverified', { exact: true })).toBeVisible();
      await expect(page.getByText('supplier_cost_unknown', { exact: true })).toHaveCount(0);
      await expect(page.getByText('账本结构', { exact: true }).locator('..')).toContainText('通过');
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `page overflow at ${width}`);
      await page.screenshot({ path: path.resolve(output, `withdrawals-${width}-${lang}.png`), fullPage: true });
      await page.getByRole('button', { name: '手续费报价 #12' }).click();
      await expect(page.getByRole('dialog')).toBeVisible();
      const box = await page.getByRole('dialog').boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= width + 1);
      assert.ok(box.y >= 0 && box.y + box.height <= 961);
      await page.screenshot({ path: path.resolve(output, `quote-${width}-${lang}.png`), fullPage: false });
      await page.getByRole('button', { name: '关闭操作' }).click();
      currentStatus = 'manual_review';
      await page.getByRole('button', { name: '刷新推广数据' }).click();
      await expect(page.getByRole('note')).toContainText('先人工核对退款及剩余有效佣金');
      await expect(page.getByRole('button', { name: '手续费报价 #12' })).toHaveCount(0);
      await expect(page.getByRole('button', { name: '独占领取 #12' })).toHaveCount(0);
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `manual review overflow at ${width}`);
      await page.screenshot({ path: path.resolve(output, `manual-review-${width}-${lang}.png`), fullPage: true });
      await page.getByRole('button', { name: '拒绝 #12' }).click();
      await expect(page.getByRole('dialog').getByRole('note')).toContainText('核对后可拒绝释放');
      const reviewBox = await page.getByRole('dialog').boundingBox();
      assert.ok(reviewBox.x >= 0 && reviewBox.x + reviewBox.width <= width + 1);
      assert.ok(reviewBox.y >= 0 && reviewBox.y + reviewBox.height <= 961);
      await page.screenshot({ path: path.resolve(output, `reject-manual-review-${width}-${lang}.png`), fullPage: false });
      await page.getByRole('button', { name: '关闭操作' }).click();
      currentStatus = 'requested';
      await page.getByRole('button', { name: '刷新推广数据' }).click();
      await expect(page.getByRole('button', { name: '手续费报价 #12' })).toBeVisible();
    }
  }
  await page.getByRole('button', { name: '手续费报价 #12' }).click();
  await page.getByLabel('手续费（USDT）').fill('0.000001');
  await page.getByRole('button', { name: '提交报价' }).click();
  await expect(page.getByRole('cell', { name: '等待用户确认', exact: true })).toBeVisible();
  assert.equal(posts.at(-1).body.fee, '0.000001');
  assert.equal(posts.at(-1).body.version, 3);
  currentStatus = 'confirmed'; claimConflict = true;
  await page.getByRole('button', { name: '刷新推广数据' }).click();
  await page.getByRole('button', { name: '独占领取 #12' }).click();
  await page.getByRole('button', { name: '确认领取' }).click();
  await expect(page.getByRole('alert')).toContainText('冲突');
  await expect(page.getByRole('cell', { name: '付款任务已领取', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: '拒绝 #12' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '手续费报价 #12' })).toHaveCount(0);
  await page.getByRole('button', { name: '登记结果未知 #12' }).click();
  await page.getByLabel('交易哈希').fill('a'.repeat(64));
  await page.getByRole('button', { name: '保持占用并登记' }).click();
  await expect(page.getByRole('cell', { name: '已提交，结果未知', exact: true })).toBeVisible();
  await page.getByRole('button', { name: '服务端核验付款 #12' }).click();
  await page.getByRole('button', { name: '请求只读核验' }).click();
  await expect(page.getByRole('alert')).toContainText('保持占用');
  await expect(page.getByRole('cell', { name: '已付款', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '拒绝 #12' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: '手续费报价 #12' })).toHaveCount(0);
  currentStatus = 'manual_review';
  await page.getByRole('button', { name: '刷新推广数据' }).click();
  await page.getByRole('button', { name: '拒绝 #12' }).click();
  await expect(page.getByRole('dialog').getByRole('note')).toContainText('先人工核对退款及剩余有效佣金');
  await page.getByLabel('拒绝理由').fill('已人工核对退款，释放剩余有效佣金');
  await page.getByRole('button', { name: '确认拒绝' }).click();
  await expect(page.getByRole('cell', { name: '已拒绝', exact: true })).toBeVisible();
  assert.deepEqual(posts.at(-1), { path: '/api/admin/referrals/withdrawals/12/reject', body: { version: 4, note: '已人工核对退款，释放剩余有效佣金' } });
  mode = 'empty'; await page.getByRole('button', { name: '刷新推广数据' }).click();
  await expect(page.getByText('暂无提现记录')).toBeVisible();
  mode = 'error'; await page.getByRole('button', { name: '刷新推广数据' }).click();
  await expect(page.getByRole('alert').filter({ hasText: '加载失败' })).toBeVisible();
  mode = 'normal'; await page.getByRole('button', { name: '重试', exact: true }).click();
  await expect(page.getByRole('table', { name: '提现记录' })).toBeVisible();
  for (const role of [2, 3, 10, 999]) {
    await page.goto(`${origin}/finance/referrals/withdrawals?role=${role}`);
    await expect(page.getByRole('alert')).toHaveText('无权访问此页面');
    await expect(page.getByRole('link', { name: '提现审核', exact: true })).toHaveCount(0);
  }
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 960 });
    mode = 'normal'; await page.goto(`${origin}/finance/overview`);
    await expect(page.getByRole('region', { name: '推广佣金' })).toContainText('4.100000 USDT');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await page.screenshot({ path: path.resolve(output, `overview-${width}.png`), fullPage: true });
    mode = 'legacy'; await page.getByRole('button', { name: '刷新财务数据' }).click();
    await expect(page.getByRole('region', { name: '推广佣金' })).toContainText('旧版财务来源');
    await expect(page.getByRole('region', { name: '推广佣金' })).not.toContainText('0.000000');
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(external, []);
  console.log(JSON.stringify({ result: 'PASS', widths: [1440, 390, 320], languageContexts: ['zh-CN', 'en'], posts: posts.map((post) => post.path), externalRequests: external.length, pageErrors: errors.length, output, note: 'Existing classic administrator interface remains Chinese in English browser context.' }));
} finally {
  await browser.close();
  await new Promise((resolve) => server.close(resolve));
}
