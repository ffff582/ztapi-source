/* global document:readonly, window:readonly, fetch:readonly, AbortSignal:readonly */
import assert from 'node:assert/strict';
import { Buffer } from 'node:buffer';
import { createHash } from 'node:crypto';
import console from 'node:console';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import react from '@vitejs/plugin-react';
import { createServer, preview } from 'vite';
import { parseHarnessOptions, requestDisposition } from './spending-browser-policy.mjs';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const options = parseHarnessOptions(process.argv.slice(2), process.env);
if (options.help) {
  console.log('SpendingPage.browser.mjs [evidence] [--base-url https://published-origin | --static-dir built-assets-directory] [--evidence directory]');
  process.exit(0);
}
const evidence = path.resolve(options.evidence ?? path.join(root, '../../../spending-ui-evidence'));
await mkdir(evidence, { recursive: true });
const report = { status: 'running', mode: options.mode, origin: null, blockedRequests: [], apiRequests: [], staticAssets: [], strippedSetCookies: 0, viewports: [], interactions: [] };
let server;
let browser;
let activePage;
const baseConfig = { enabled: false, daily_spend_u: '', hourly_spend_u: '', low_balance_u: '', input_tokens: 0, telegram: false, version: 0, timezone: 'Asia/Shanghai', telegram_bound: false, telegram_available: true };
const baseOverview = { as_of: '2026-11-02T02:00:00Z', day_start: '2026-11-01T16:00:00Z', timezone: 'Asia/Shanghai', complete: false, confirmed_daily_u: null, confirmed_hourly_u: '0.000001', confirmed_refunds_daily_u: '2.000001', available_balance_u: '12.340000', pending_requests: 3, incomplete_requests: 2 };
const baseAlert = { id: 12, kind: 'daily_spend', created_at: '2026-11-02T02:00:00Z', measured_value: '5.000001', threshold: '5', unit: 'U', request_id: `request-${'x'.repeat(120)}`, model: `model-${'m'.repeat(120)}`, delivery_status: 'not_requested' };
const staticCache = new Map();
async function serveStatic(route) {
  const request = route.request();
  const key = `${request.method()} ${request.url()}`;
  if (!staticCache.has(key)) staticCache.set(key, (async () => {
    // Node fetch has no browser cookie jar. Never forward browser auth, cookies,
    // referrers or redirect responses, even when testing a published origin.
    const response = await fetch(request.url(), { method: request.method(), headers: { Accept: request.headers().accept ?? '*/*' }, redirect: 'manual', signal: AbortSignal.timeout(15_000) });
    assert.ok(response.status >= 200 && response.status < 300, `Static asset returned HTTP ${response.status}; redirects are not followed`);
    const body = Buffer.from(await response.arrayBuffer());
    assert.ok(body.length <= 10 * 1024 * 1024, 'Static asset exceeds acceptance bound');
    const headers = Object.fromEntries(response.headers);
    if (headers['set-cookie']) report.strippedSetCookies += 1;
    for (const name of ['set-cookie', 'set-cookie2', 'content-encoding', 'content-length', 'connection', 'transfer-encoding']) delete headers[name];
    headers['cache-control'] = 'no-store';
    report.staticAssets.push({ path: new URL(request.url()).pathname, bytes: body.length, sha256: createHash('sha256').update(body).digest('hex') });
    return { status: response.status, headers, body };
  })());
  return route.fulfill(await staticCache.get(key));
}

async function relogin(page, origin, owner) {
  await page.getByRole('button', { name: '退出登录', exact: true }).click();
  await expect(page.getByRole('heading', { name: '登录 ZTAPI', exact: true })).toBeVisible();
  await page.getByLabel('账号', { exact: true }).fill(`offline-owner-${owner}`);
  await page.getByLabel('密码', { exact: true }).fill('offline-fixture-password');
  await page.getByRole('button', { name: '登录', exact: true }).click();
  await expect(page).toHaveURL(`${origin}/console`);
  await page.getByRole('link', { name: '消费预警', exact: true }).click();
  await expect(page.getByRole('heading', { name: '消费预警', exact: true })).toBeVisible();
}

async function layoutIssues(page) {
  return page.evaluate(() => {
    const issues = [];
    if (document.documentElement.scrollWidth > window.innerWidth + 1) issues.push('page overflow');
    for (const element of document.querySelectorAll('.spending-page input, .spending-page button, .spending-record__body, .spending-bell__panel')) {
      if (element.scrollWidth > element.clientWidth + 1) issues.push(`content overflow: ${element.className}`);
      const rect = element.getBoundingClientRect();
      if (rect.left < -1 || rect.right > window.innerWidth + 1) issues.push(`offscreen: ${element.className}`);
    }
    const overlap = (elements, label) => {
      const rects = elements.map((element) => element.getBoundingClientRect());
      for (let i = 0; i < rects.length; i++) for (let j = i + 1; j < rects.length; j++) {
        if (Math.min(rects[i].right, rects[j].right) - Math.max(rects[i].left, rects[j].left) > 1 && Math.min(rects[i].bottom, rects[j].bottom) - Math.max(rects[i].top, rects[j].top) > 1) issues.push(`overlap: ${label}`);
      }
    };
    overlap([...document.querySelectorAll('.spending-page > header, .spending-page > section')], 'sections');
    overlap([...document.querySelectorAll('.spending-fields > label')], 'fields');
    overlap([...document.querySelector('.console-header').children].filter((element) => element.getBoundingClientRect().width), 'header');
    return issues;
  });
}

try {
  let origin = options.origin;
  if (!origin) {
    if (options.staticDir) {
      report.localStaticAudit = { cookies: 0, authorization: 0, api: 0 };
      server = await preview({ root, configFile: false, build: { outDir: path.resolve(options.staticDir) }, preview: { host: '127.0.0.1', port: 4173, strictPort: false, headers: { 'Set-Cookie': 'acceptance-only=must-not-enter-browser; Path=/; SameSite=Lax' } } });
      server.httpServer.on('request', (request) => {
        if (request.headers.cookie) report.localStaticAudit.cookies += 1;
        if (request.headers.authorization) report.localStaticAudit.authorization += 1;
        if (/^\/(api|pg|v1|v1beta)(\/|$)/.test(request.url)) report.localStaticAudit.api += 1;
      });
    } else {
      server = await createServer({ root, configFile: false, plugins: [react()], server: { host: '127.0.0.1', port: 5173, strictPort: false, hmr: false, watch: null } });
      await server.listen();
    }
    origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  }
  report.origin = origin;
  browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--disable-background-networking', '--disable-component-update', '--disable-sync'] });
  const widths = options.widths;
  for (const width of widths) for (const locale of ['zh-CN', 'en']) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ viewport: { width, height: width === 1440 ? 1000 : width === 320 ? 568 : 844 }, locale, serviceWorkers: 'block' });
    assert.deepEqual(await context.cookies(), []);
    await context.routeWebSocket('**/*', (socket) => { report.blockedRequests.push('WebSocket'); socket.close(); });
    await context.addInitScript(({ locale, theme }) => { window.localStorage.setItem('ztapi.locale', locale); window.localStorage.setItem('ztapi.theme', theme); }, { locale, theme });
    let config = { ...baseConfig };
    let read = false;
    let conflict = false;
    let unavailable = false;
    let expired = false;
    let ownerID = 7;
    let authRefreshCalls = 0;
    let logoutCalls = 0;
    let alertCalls = 0;
    let holdOld = false;
    let releaseOld;
    const mutations = [];
    const configResponse = (value) => {
      const { timezone, telegram_bound, telegram_available, ...preferences } = value;
      return { preferences, timezone, telegram_bound, telegram_available };
    };
    await context.route('**/*', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const respond = (data, status = 200, message = 'fixed_error') => route.fulfill({ status, json: { success: status === 200, data, message } });
      const disposition = requestDisposition({ url: url.href, type: request.resourceType(), method: request.method() }, { origin, mode: options.mode });
      if (disposition === 'blocked') { report.blockedRequests.push(`${request.method()} ${url.origin}${url.pathname}`); return route.abort(); }
      if (disposition === 'static') {
        try { return await serveStatic(route); }
        catch {
          report.blockedRequests.push(`static_resource_unavailable:${url.pathname}`);
          return route.abort().catch(() => undefined);
        }
      }
      report.apiRequests.push({ method: request.method(), path: url.pathname });
      const owner = request.headers()['new-api-user'];
      const session = () => ({ access_token: `offline-spending-${ownerID}`, expires_in: 900, user: { id: ownerID, username: `offline-owner-${ownerID}`, role: 1, group: 'default' } });
      if (url.pathname === '/api/auth/refresh') { authRefreshCalls += 1; return respond(session()); }
      if (url.pathname === '/api/auth/login') {
        const input = request.postDataJSON();
        assert.equal(input.password, 'offline-fixture-password');
        assert.ok(['offline-owner-7', 'offline-owner-8'].includes(input.username));
        ownerID = input.username === 'offline-owner-8' ? 8 : 7;
        return respond(session());
      }
      if (url.pathname === '/api/auth/session') return respond(session().user);
      if (url.pathname === '/api/spending/config') {
        if (unavailable) return respond(null, 503);
        if (request.method() === 'PUT') {
          const input = request.postDataJSON();
          mutations.push(input);
          assert.deepEqual(Object.keys(input).sort(), ['daily_spend_u', 'enabled', 'hourly_spend_u', 'input_tokens', 'low_balance_u', 'telegram', 'version']);
          if (conflict) return respond(null, 409, 'spending_version_conflict');
          config = { ...config, ...input, version: input.version + 1 };
          return respond(configResponse(config).preferences);
        }
        return respond(configResponse(owner === '8' ? { ...baseConfig, daily_spend_u: '27' } : config));
      }
      if (url.pathname === '/api/spending/overview') return respond(owner === '8' ? { ...baseOverview, available_balance_u: '27', confirmed_refunds_daily_u: null } : baseOverview);
      if (url.pathname === '/api/spending/alerts') {
        assert.equal(url.searchParams.get('limit'), '20'); alertCalls += 1;
        if (unavailable) return respond(null, 503);
        if (expired) return respond(null, 401);
        if (holdOld && owner === '7') { await new Promise((resolve) => { releaseOld = resolve; }); return respond({ items: [baseAlert], unread_count: 1 }); }
        return respond(owner === '8' ? { items: [], unread_count: 0 } : { items: [{ ...baseAlert, ...(read ? { read_at: '2026-11-02T03:00:00Z' } : {}) }], unread_count: read ? 0 : 1 });
      }
      if (url.pathname === '/api/spending/alerts/12/read') { assert.equal(request.method(), 'POST'); assert.deepEqual(request.postDataJSON(), {}); read = true; return respond({ read: true }); }
      if (url.pathname === '/api/auth/logout') { logoutCalls += 1; return respond({}); }
      // The workbench is navigated to, but no model request may leave this fixture.
      if (url.pathname === '/api/token/' || url.pathname === '/api/token') return respond({ items: [], total: 0, page: 1, page_size: Number(url.searchParams.get('page_size') ?? 100) });
      if (url.pathname === '/api/user/models') return respond([]);
      if (url.pathname === '/api/user/self') return respond({ id: 7, quota: 1000000, group: 'default' });
      if (url.pathname === '/api/status') return respond({ quota_per_unit: 100000 });
      if (url.pathname === '/api/log/self/stat') return respond({ quota: 0, token: 0, request_count: 0 });
      if (url.pathname === '/api/log/self') return respond({ items: [], total: 0, page: 1, page_size: Number(url.searchParams.get('page_size') ?? 100) });
      if (/^\/(api|pg|v1|v1beta)(\/|$)/.test(url.pathname)) { report.blockedRequests.push(`${request.method()} ${url.pathname}`); return route.abort(); }
      report.blockedRequests.push(`${request.method()} ${url.pathname}`);
      return route.abort();
    });
    const page = await context.newPage(); activePage = page;
    await page.clock.install();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/console/spending`);
    const zh = locale === 'zh-CN';
    await expect(page.getByRole('heading', { name: zh ? '消费预警' : 'Spending alerts', exact: true })).toBeVisible();
    await expect(page.getByLabel(zh ? '启用消费预警' : 'Enable spending alerts')).not.toBeChecked();
    await expect(page.getByText('2.000001 U', { exact: true })).toBeVisible();
    await expect(page.getByText('12.340000 U', { exact: true })).toBeVisible();
    await expect(page.getByLabel(zh ? 'Telegram 通知' : 'Telegram notifications')).toBeDisabled();
    await expect(page.getByRole('link', { name: zh ? '前往客服绑定 Telegram' : 'Bind Telegram in support' })).toHaveAttribute('href', '/console/support');
    assert.equal(alertCalls, 1, 'header and page share one poll');
    assert.deepEqual(await layoutIssues(page), []);
    const name = `${width}-${locale}-${theme}`;
    const screenshot = path.join(evidence, `spending-${name}.png`);
    await page.screenshot({ path: screenshot, fullPage: true });
    const bell = page.getByRole('button', { name: zh ? '消费通知' : 'Spending notifications', exact: true });
    await bell.click();
    const dialog = page.getByRole('dialog', { name: zh ? '消费通知' : 'Spending notifications' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: zh ? /今日消费达到阈值/ : /Daily spend threshold reached/ })).toBeVisible();
    assert.equal(await dialog.evaluate((element) => { const rect = element.getBoundingClientRect(); return rect.top >= 0 && rect.bottom <= window.innerHeight; }), true, 'notification panel fits the viewport vertically');
    assert.deepEqual(await layoutIssues(page), []);
    await page.screenshot({ path: path.join(evidence, `bell-${name}.png`), fullPage: true });
    await dialog.press('Escape'); await expect(bell).toBeFocused();

    if (width === 1440 && zh && theme === 'light') {
      const daily = page.getByLabel('今日确认消费阈值 (U)');
      await daily.fill('0.000001');
      await page.getByLabel('启用消费预警').check();
      assert.equal(mutations.length, 0);
      await page.getByRole('button', { name: '保存预警设置', exact: true }).click();
      await expect(page.getByText('预警设置已保存', { exact: true })).toBeVisible();
      assert.equal(mutations[0].daily_spend_u, '0.000001');
      await page.getByRole('button', { name: /今日消费达到阈值/ }).click();
      await expect(page.locator('.spending-record[data-unread="false"]')).toHaveCount(1);
      conflict = true;
      await daily.fill('7'); await page.getByRole('button', { name: '保存预警设置', exact: true }).click();
      await expect(page.getByText('设置已在其他会话更新。你的修改尚未保存。')).toBeVisible();
      await expect(daily).toHaveValue('7');
      conflict = false; config = { ...config, daily_spend_u: '8', version: 4 };
      await page.getByRole('button', { name: '放弃修改并重新加载' }).click();
      await expect(daily).toHaveValue('8');
      report.interactions.push('explicit-save-exact-decimal', 'owner-read', '409-preserves-edits-and-explicit-reload');

      holdOld = true;
      await relogin(page, origin, 7);
      await expect.poll(() => Boolean(releaseOld)).toBe(true);
      await expect(page.getByRole('button', { name: /今日消费达到阈值/ })).toHaveCount(0);
      await relogin(page, origin, 8);
      releaseOld();
      await expect(page.getByText('27 U', { exact: true })).toBeVisible();
      await expect(page.getByText('12.340000 U', { exact: true })).toHaveCount(0);
      await expect(page.getByRole('button', { name: /今日消费达到阈值/ })).toHaveCount(0);
      report.interactions.push('account-switch-ignores-late-old-response');

      await daily.fill('9.000001');
      const refreshBefore = authRefreshCalls;
      const logoutBefore = logoutCalls;
      expired = true;
      await page.clock.fastForward(15 * 60_000);
      await expect(page.getByText('消费预警登录凭证已过期或未被接受，主会话未退出。')).toBeVisible();
      assert.equal(authRefreshCalls, refreshBefore);
      assert.equal(logoutCalls, logoutBefore);
      const reauthenticate = page.getByRole('button', { name: '重新确认登录并刷新页面', exact: true });
      let warning = '';
      page.once('dialog', async (dialog) => { warning = dialog.message(); await dialog.dismiss(); });
      await reauthenticate.click();
      assert.match(warning, /未保存的修改/);
      await expect(daily).toHaveValue('9.000001');
      assert.equal(authRefreshCalls, refreshBefore);
      expired = false;
      page.once('dialog', (dialog) => dialog.accept());
      const reloaded = page.waitForEvent('domcontentloaded');
      await reauthenticate.click(); await reloaded;
      await expect(page.getByText('27 U', { exact: true })).toBeVisible();
      await expect(page.getByText('消费预警登录凭证已过期或未被接受，主会话未退出。')).toHaveCount(0);
      assert.equal(authRefreshCalls, refreshBefore + 1);
      assert.equal(logoutCalls, logoutBefore);
      await expect(page).toHaveURL(`${origin}/console/spending`);
      report.interactions.push('15min-sidecar-401-does-not-refresh-or-logout', 'cancelled-reload-preserves-unsaved-form', 'confirmed-current-url-reload-rechecks-main-session-once');

      unavailable = true;
      await relogin(page, origin, 8);
      await expect(page.getByText('预警服务暂不可用，请稍后重试。')).toBeVisible();
      await bell.click(); await expect(page.getByRole('dialog').getByText('通知暂不可用，请稍后重试。')).toBeVisible();
      await page.getByRole('dialog').press('Escape');
      await page.getByRole('link', { name: '工作台', exact: true }).click();
      await expect(page.getByRole('button', { name: '消费通知', exact: true })).toBeVisible();
      await expect(page).toHaveURL(`${origin}/console/workbench/text`);
      report.interactions.push('optional-503-does-not-logout-or-block-workbench');
    }
    assert.deepEqual(errors, []);
    assert.deepEqual(await context.cookies(), []);
    report.viewports.push({ width, locale, theme, screenshot, status: 'passed' });
    await context.close();
  }
  assert.deepEqual(report.blockedRequests, []);
  if (options.mode !== 'local-dev') assert.ok(report.staticAssets.some((asset) => /^\/assets\/.+\.js$/.test(asset.path)), 'Acceptance must load a compiled static bundle, not dev source');
  if (report.localStaticAudit) {
    assert.deepEqual(report.localStaticAudit, { cookies: 0, authorization: 0, api: 0 });
    assert.ok(report.strippedSetCookies > 0, 'Synthetic static Set-Cookie must be stripped');
  }
  report.status = 'passed';
} catch (error) {
  report.status = 'failed'; report.error = error.stack ?? error.message; process.exitCode = 1;
  if (activePage && !activePage.isClosed()) {
    report.failurePage = { url: activePage.url(), body: await activePage.locator('body').innerText() };
    await activePage.screenshot({ path: path.join(evidence, 'failure.png'), fullPage: true });
  }
} finally {
  await browser?.close();
  if (server) {
    if (typeof server.close === 'function') await server.close();
    else await new Promise((resolve) => server.httpServer.close(resolve));
  }
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}
