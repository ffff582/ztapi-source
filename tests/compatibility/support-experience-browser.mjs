import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { mkdir, readFile, realpath, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { createServer as netServer, connect } from 'node:net';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium, expect as baseExpect } from '@playwright/test';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const output = path.join(repo, 'tests/compatibility/support-experience-browser-evidence/browser');
const requireConsole = createRequire(path.join(repo, 'web/console/package.json'));
const { createServer } = await import(pathToFileURL(requireConsole.resolve('vite')));
const react = requireConsole('@vitejs/plugin-react').default;
const expect = baseExpect.configure({ timeout: 15_000 });
const report = { pass: false, synthetic: true, paid_calls: 0, cases: [], screenshots: [], external_requests: [], page_errors: [], cleanup: {} };
let fixture, browser, vite, publicBase, fixtureBase, lastPage;
const logs = [];
const ports = [];
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
async function freePort() {
  const server = netServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const port = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return port;
}
async function listening(port) {
  return new Promise((resolve) => {
    const socket = connect(port, '127.0.0.1');
    socket.once('connect', () => { socket.destroy(); resolve(true); });
    socket.once('error', () => resolve(false));
  });
}
async function execute(command, args, cwd) {
  await new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env: { ...process.env, GOPROXY: 'off', GOSUMDB: 'off', GOTOOLCHAIN: 'local' }, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let text = '';
    child.stdout.on('data', (chunk) => { text += chunk; });
    child.stderr.on('data', (chunk) => { text += chunk; });
    child.once('error', reject);
    child.once('exit', (code) => code === 0 ? resolve() : reject(new Error(`${command} exited ${code}: ${text.slice(-5000)}`)));
  });
}
async function metrics() {
  const response = await fetch(fixtureBase + '/__fixture/status');
  assert.equal(response.status, 200);
  const value = (await response.json()).data;
  assert.equal(value.paid_calls, 0);
  return value;
}
async function actor(width, username) {
  const context = await browser.newContext({ viewport: { width, height: 1000 }, locale: 'zh-CN', colorScheme: 'light', serviceWorkers: 'block' });
  await context.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (url.origin !== publicBase) {
      report.external_requests.push({ origin: url.origin, pathname: url.pathname });
      return route.abort();
    }
    return route.continue();
  });
  await context.addInitScript(() => localStorage.setItem('ztapi.locale', 'zh-CN'));
  const logged = await context.request.post(publicBase + '/api/auth/login', { headers: { Origin: publicBase }, data: { username, password: 'fixture-only-password' } });
  assert.equal(logged.status(), 200);
  const session = (await logged.json()).data;
  const page = await context.newPage();
  lastPage = page;
  page.setDefaultTimeout(15_000);
  page.setDefaultNavigationTimeout(90_000);
  const mutations = [];
  let bindingReads = 0;
  page.on('pageerror', (error) => report.page_errors.push(error.message));
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/support/telegram-binding') {
      if (request.method() === 'POST') mutations.push(request.postDataJSON());
      else bindingReads++;
    }
  });
  return { context, page, session, mutations, reads: () => bindingReads };
}
async function api(user, method, pathname, data) {
  return user.context.request.fetch(publicBase + pathname, { method, headers: { Origin: publicBase, Authorization: `Bearer ${user.session.access_token}`, 'New-Api-User': String(user.session.user.id) }, ...(data === undefined ? {} : { data }) });
}
async function binding(user) {
  const response = await api(user, 'GET', '/api/support/telegram-binding');
  assert.equal(response.status(), 200);
  const value = (await response.json()).data;
  assert.equal(value.binding_url, undefined, 'GET must never return a capability URL');
  return value;
}
async function capture(page, name) {
  await page.evaluate(() => document.fonts.ready);
  const geometry = await page.evaluate(() => ({ width: innerWidth, scrollWidth: document.documentElement.scrollWidth }));
  assert.ok(geometry.scrollWidth <= geometry.width + 1, `${name} overflows: ${JSON.stringify(geometry)}`);
  const file = path.join(output, `${name}.png`);
  await page.screenshot({ path: file, fullPage: true, animations: 'disabled' });
  const aria = (await page.locator('body').ariaSnapshot()).replace(/([?&]start=bind_)[A-Za-z0-9_-]+/g, '$1[REDACTED_SYNTHETIC_CAPABILITY]');
  await writeFile(path.join(output, `${name}.aria.txt`), aria);
  report.screenshots.push({ name, file, sha256: hash(await readFile(file)), geometry });
}
async function step(name, run) {
  const start = Date.now();
  const result = await run();
  report.cases.push({ name, pass: true, elapsed_ms: Date.now() - start, ...result });
}
async function createAndClaim(user, usePolling = false, captureName) {
  await user.page.getByRole('button', { name: '绑定 Telegram', exact: true }).click();
  const link = user.page.getByRole('link', { name: '前往小 Z 认领' });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute('target', '_blank');
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  const token = new URL(await link.getAttribute('href')).searchParams.get('start').slice(5);
  if (captureName) await capture(user.page, captureName);
  const before = await binding(user);
  assert.equal(before.bound, false);
  const count = user.mutations.length;
  const claim = await api(user, 'POST', '/__fixture/telegram-claim', { token });
  assert.equal(claim.status(), 200, 'synthetic claim endpoint must use the real bridge');
  if (!usePolling) await user.page.getByRole('button', { name: '刷新绑定状态' }).click();
  await expect(user.page.getByText(`待确认 Telegram ID: ${900000000 + user.session.user.id}`, { exact: false })).toBeVisible({ timeout: 8000 });
  assert.equal(user.mutations.length, count, 'claim or poll must not auto-confirm');
  assert.equal((await binding(user)).bound, false);
  return before.pending.id;
}

try {
  await mkdir(output, { recursive: true });
  for (let i = 0; i < 3; i++) ports.push(await freePort());
  assert.equal(new Set(ports).size, 3);
  fixtureBase = `http://127.0.0.1:${ports[0]}`;
  publicBase = `http://127.0.0.1:${ports[1]}`;
  const binary = path.join(output, 'ztapi-support-fixture.exe');
  await execute('go', ['build', '-o', binary, './cmd/ztapi-support-fixture'], path.join(repo, 'server'));
  fixture = spawn(binary, ['-listen', `127.0.0.1:${ports[0]}`, '-public-origin', publicBase, '-admin-origin', `http://127.0.0.1:${ports[2]}`, '-state-root', output], { cwd: repo, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
  fixture.stdout.on('data', (chunk) => logs.push(chunk.toString()));
  fixture.stderr.on('data', (chunk) => logs.push(chunk.toString()));
  fixture.on('error', (error) => logs.push(error.message));
  const deadline = Date.now() + 45_000;
  while (true) {
    try { await metrics(); break; } catch (error) { if (Date.now() > deadline) throw error; await pause(250); }
  }
  vite = await createServer({ configFile: false, root: path.join(repo, 'web/console'), cacheDir: path.join(output, 'node_modules/.vite'), plugins: [react()], server: { host: '127.0.0.1', port: ports[1], strictPort: true, proxy: { '/api': { target: fixtureBase, changeOrigin: false }, '/__fixture': { target: fixtureBase, changeOrigin: false } }, fs: { allow: [repo, path.dirname(await realpath(path.join(repo, 'node_modules')))] } } });
  await vite.listen();
  browser = await chromium.launch({ channel: 'chrome', headless: true });

  for (const width of [1440, 390, 320]) {
    await step(`binding-and-existing-support-${width}`, async () => {
      const user = await actor(width, `fixture-alice-${width}`);
      const { page } = user;
      await page.goto(publicBase + '/console/support');
      await expect(page.getByRole('button', { name: '绑定 Telegram', exact: true })).toBeVisible();
      await expect(page.getByLabel('消息内容')).toBeEnabled();
      await page.getByLabel('消息内容').fill('合成配置咨询：如何配置客户端？');
      await page.getByRole('button', { name: '发送消息' }).click();
      await expect(page.getByRole('log')).toContainText('本地合成答复');
      const conversationId = new URL(page.url()).searchParams.get('conversation');
      assert.ok(conversationId);
      await page.getByRole('button', { name: new RegExp(`fixture-request-${user.session.user.id}`) }).click();
      await expect(page.getByRole('region', { name: '请求诊断证据' })).toContainText('0.125 U');
      await capture(page, `unbound-help-${width}`);
      await createAndClaim(user, true, `created-pending-${width}`);
      const confirm = page.getByRole('button', { name: '确认绑定', exact: true });
      await expect(confirm).toBeDisabled();
      await page.getByRole('checkbox', { name: /我确认这是我的 Telegram 账号/ }).check();
      await expect(confirm).toBeDisabled();
      await page.getByRole('checkbox', { name: /同意接收工单状态通知/ }).check();
      await capture(page, `claimed-manual-consent-${width}`);
      await confirm.click();
      await expect(page.getByText('已绑定', { exact: true })).toBeVisible();
      assert.equal((await binding(user)).bound, true);
      await capture(page, `bound-${width}`);
      let count = user.mutations.length;
      await page.getByRole('button', { name: '解除绑定', exact: true }).click();
      assert.equal(user.mutations.length, count);
      await page.getByRole('button', { name: '保留当前状态' }).click();
      assert.equal((await binding(user)).bound, true);
      await page.getByRole('button', { name: '解除绑定', exact: true }).click();
      await page.getByRole('button', { name: '确认解除绑定', exact: true }).click();
      await expect(page.getByText('未绑定', { exact: true })).toBeVisible();
      assert.equal((await binding(user)).bound, false);
      await capture(page, `unbound-${width}`);

      await page.getByRole('button', { name: '绑定 Telegram', exact: true }).click();
      await expect(page.getByRole('link', { name: '前往小 Z 认领' })).toBeVisible();
      await page.reload();
      await expect(page.getByRole('button', { name: '取消绑定', exact: true })).toBeVisible();
      await expect(page.getByRole('link', { name: '前往小 Z 认领' })).toHaveCount(0);
      count = user.mutations.length;
      await page.getByRole('button', { name: '取消绑定', exact: true }).click();
      assert.equal(user.mutations.length, count);
      await page.getByRole('button', { name: '确认取消绑定', exact: true }).click();
      await expect(page.getByRole('button', { name: '绑定 Telegram', exact: true })).toBeVisible();

      await page.getByRole('button', { name: '申请人工工单' }).click();
      await page.getByLabel('工单主题').fill(`合成工单 ${width}`);
      await page.getByLabel('问题详情').fill('Synthetic request, no customer data.');
      await page.getByRole('button', { name: '提交工单' }).click();
      await expect(page.locator('.support-ticket-status').first()).toContainText('工单已记录');
      await page.goto(`${publicBase}/console/support?conversation=${encodeURIComponent(conversationId)}`);
      await expect(page.getByRole('log')).toContainText('合成配置咨询');
      await expect(page.locator('.support-ticket-status').first()).toContainText('工单已记录');
      await capture(page, `owned-ticket-link-${width}`);
      const bob = await actor(width, 'fixture-bob');
      await bob.page.goto(`${publicBase}/console/support?conversation=${encodeURIComponent(conversationId)}`);
      await expect(bob.page.getByRole('alert')).toContainText('记录不存在或无权访问');
      await expect(bob.page.getByRole('log')).not.toContainText('合成配置咨询');
      await bob.context.close();
      const actions = user.mutations.map((input) => input.action);
      assert.deepEqual(actions.slice(0, 3), ['create', 'confirm', 'unbind']);
      await user.context.close();
      return { width, manual_confirmation: true, separate_notification_consent: true, owned_deep_link: true, cross_user_denied: true, restored_token_absent: true, actions };
    });
  }

  await step('conflict-expiry-disconnect-and-old-server', async () => {
    const user = await actor(390, 'fixture-recovery');
    const { page } = user;
    await page.goto(publicBase + '/console/support');
    const challenge = await createAndClaim(user);
    await page.getByRole('checkbox', { name: /我确认这是我的 Telegram 账号/ }).check();
    await page.getByRole('checkbox', { name: /同意接收工单状态通知/ }).check();
    const cancelled = await api(user, 'POST', '/api/support/telegram-binding', { action: 'cancel', challenge_id: challenge, idempotency_key: randomUUID() });
    assert.equal(cancelled.status(), 200);
    await page.getByRole('button', { name: '确认绑定', exact: true }).click();
    await expect(page.getByRole('alert')).toBeVisible();
    await expect(page.getByText('已绑定', { exact: true })).toHaveCount(0);
    await expect(page.getByRole('button', { name: '确认绑定', exact: true })).toBeDisabled();
    await capture(page, 'cross-session-conflict-390');
    await page.getByRole('button', { name: '刷新绑定状态' }).click();
    await expect(page.getByRole('button', { name: '绑定 Telegram', exact: true })).toBeVisible();
    await page.route('**/api/support/telegram-binding', (route) => route.request().method() === 'POST' ? route.abort('connectionreset') : route.continue());
    await page.getByRole('button', { name: '绑定 Telegram', exact: true }).click();
    await expect(page.getByRole('alert')).toContainText('绑定结果尚未确认');
    await expect(page.getByRole('button', { name: '绑定 Telegram', exact: true })).toBeDisabled();
    await capture(page, 'disconnected-create-390');
    await page.unroute('**/api/support/telegram-binding');
    await page.route('**/api/support/telegram-binding', (route) => route.fulfill({ json: { success: true, data: { enabled: true, bound: false, pending: { id: 'synthetic-expired', claimed: true, telegram_id: 900000131, expires_at: new Date(Date.now() - 60_000).toISOString() } } } }));
    await page.getByRole('button', { name: '刷新绑定状态' }).click();
    await expect(page.getByText('绑定请求已过期，请重新创建。')).toBeVisible();
    await expect(page.getByRole('button', { name: '确认绑定', exact: true })).toHaveCount(0);
    await capture(page, 'expired-390');
    await page.unroute('**/api/support/telegram-binding');
    await page.route('**/api/support/config', async (route) => {
      const response = await route.fetch();
      const envelope = await response.json();
      delete envelope.data.telegram_binding_enabled;
      await route.fulfill({ json: envelope });
    });
    const reads = user.reads();
    await page.reload();
    await expect(page.getByLabel('消息内容')).toBeEnabled();
    await expect(page.getByRole('region', { name: 'Telegram 工单通知' })).toHaveCount(0);
    assert.equal(user.reads(), reads);
    await capture(page, 'old-server-general-help-390');
    await user.context.close();
    return { real_cross_session_conflict: true, focused_expiry_stub: true, disconnected_mutation_not_retried: true, optional_old_config_supported: true };
  });
  assert.deepEqual(report.external_requests, []);
  assert.deepEqual(report.page_errors, []);
  const { paid_calls, provider_calls, image_calls } = await metrics();
  report.metrics = { paid_calls, provider_calls, image_calls };
  const sourceFiles = ['web/console/src/api/support.ts', 'web/console/src/features/support/SupportPage.tsx', 'web/console/src/features/support/TelegramBinding.tsx', 'web/console/src/features/support/support.css'];
  report.source_hashes = await Promise.all(sourceFiles.map(async (file) => ({ path: file, sha256: hash(await readFile(path.join(repo, file))) })));
  report.pass = true;
} catch (error) {
  report.failure = String(error.stack || error).replace(/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g, '[REDACTED_SYNTHETIC_JWT]').replace(/bind_[A-Za-z0-9_-]+/g, '[REDACTED_SYNTHETIC_CAPABILITY]');
  if (lastPage && !lastPage.isClosed()) await lastPage.screenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {});
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  if (vite) await vite.close();
  if (fixture && fixture.exitCode === null) {
    const exited = new Promise((resolve) => fixture.once('exit', resolve));
    fixture.kill();
    await exited;
  }
  report.cleanup = { browser_closed: !browser || !browser.isConnected(), fixture_stopped: !fixture || fixture.exitCode !== null || fixture.signalCode !== null, listening_ports: [] };
  for (const port of ports) if (await listening(port)) report.cleanup.listening_ports.push(port);
  if (!report.cleanup.browser_closed || !report.cleanup.fixture_stopped || report.cleanup.listening_ports.length) { report.pass = false; process.exitCode = 1; }
  await mkdir(output, { recursive: true });
  await writeFile(path.join(output, 'fixture.log'), logs.join(''));
  await writeFile(path.join(output, 'results.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ pass: report.pass, cases: report.cases, cleanup: report.cleanup, failure: report.failure, output }, null, 2));
}
