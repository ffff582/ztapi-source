/* global document:readonly, window:readonly, navigator:readonly, localStorage:readonly, sessionStorage:readonly, structuredClone:readonly */
import assert from 'node:assert/strict';
import console from 'node:console';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath, URL } from 'node:url';
import { chromium, expect } from '@playwright/test';
import { preview } from 'vite';
import { referralBrowserLaunchOptions } from './referral-browser-policy.mjs';

const root = fileURLToPath(new URL('../../../', import.meta.url));
const evidence = path.join(root, 'output/playwright/referrals');
const fixture = JSON.parse(await readFile(new URL('./referral.fixture.json', import.meta.url), 'utf8'));
fixture.server_time = 1794124800;
fixture.campaign.started_at = 1794096000;
const address = 'TJRabPrwbZy45sbavfcjinPJC18kjpRTv8';
const quote = { id: 9, version: 2, amount: '40.000000', fee: '1.250000', net_amount: '38.750000', address, status: 'fee_quoted', created_at: fixture.server_time, fee_expires_at: 1794126600, tx_id: '', note: '' };
const session = { access_token: 'offline-referral-browser', expires_in: 900, user: { id: 7, username: 'offline-only', role: 1, group: 'default' } };
const report = { status: 'running', blockedRequests: [], cases: [] };
let browser;
let server;
await mkdir(evidence, { recursive: true });

async function checkLayout(page, name) {
  const issues = await page.evaluate(() => {
    const problems = [];
    if (document.documentElement.scrollWidth > window.innerWidth + 1) problems.push('document overflow');
    for (const element of document.querySelectorAll('.referral-page button, .referral-page label, .referral-page dd, .referral-page th, .referral-page td, .public-header__inner, .public-header__link')) {
      const box = element.getBoundingClientRect();
      if (!box.width || !box.height) continue;
      if (box.left < -1 || box.right > window.innerWidth + 1) problems.push(`outside viewport: ${element.textContent.slice(0, 50)}`);
      if (element.scrollWidth > element.clientWidth + 1) problems.push(`element overflow: ${element.tagName} ${element.textContent.slice(0, 50)}`);
    }
    const siblings = [...document.querySelectorAll('.referral-page > header, .referral-page > section')].map((node) => node.getBoundingClientRect());
    for (let i = 0; i < siblings.length; i++) for (let j = i + 1; j < siblings.length; j++) {
      if (Math.min(siblings[i].bottom, siblings[j].bottom) - Math.max(siblings[i].top, siblings[j].top) > 1) problems.push('section overlap');
    }
    return problems;
  });
  await page.screenshot({ path: path.join(evidence, `${name}.png`), fullPage: true });
  assert.deepEqual(issues, [], `${name}: ${issues.join(', ')}`);
}

try {
  server = await preview({ root, configFile: false, preview: { host: '127.0.0.1', port: 0, strictPort: true } });
  const origin = `http://127.0.0.1:${server.httpServer.address().port}`;
  const launchOptions = referralBrowserLaunchOptions(process.platform, process.env.ZTAPI_REFERRAL_BROWSER_CHANNEL);
  report.browser = { platform: process.platform, channel: launchOptions.channel ?? 'bundled-chromium' };
  browser = await chromium.launch(launchOptions);
  for (const locale of ['zh-CN', 'en']) for (const width of [1440, 390, 320]) {
    const context = await browser.newContext({ viewport: { width, height: width === 1440 ? 1000 : 844 }, locale, serviceWorkers: 'block' });
    await context.addInitScript((language) => {
      localStorage.setItem('ztapi.locale', language);
      localStorage.setItem('ztapi.theme', 'light');
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (text) => { window.__referralClipboard = text; } } });
    }, locale);
    const english = locale === 'en';
    const text = (zh, en) => english ? en : zh;
    let signedIn = false;
    let mode = 'ready';
    let conflict = false;
    const posts = [];
    const states = ['requested', 'confirmed', 'paying', 'submitted_unknown', 'paid', 'rejected', 'cancelled', 'manual_review'];
    let items = [...states.map((status, index) => ({ ...quote, id: index + 1, amount: '20.000000', fee: '1.000000', net_amount: '19.000000', status, tx_id: status === 'paid' ? 'a'.repeat(64) : '', note: status === 'manual_review' ? 'Review pending / '.repeat(15) : '' })), { ...quote }];
    let registerMode = 'captcha';
    await context.route('**/*', async (route) => {
      const url = new URL(route.request().url());
      const respond = (data) => route.fulfill({ json: { success: true, data } });
      if (url.origin !== origin) { report.blockedRequests.push(url.origin); return route.abort(); }
      if (!url.pathname.startsWith('/api/') && !/^\/(pg|v1|v1beta)(\/|$)/.test(url.pathname)) return route.continue();
      if (url.pathname === '/api/auth/refresh') return signedIn ? respond(session) : route.fulfill({ status: 401, json: { success: false, message: 'unauthorized' } });
      if (url.pathname === '/api/auth/login') { signedIn = true; return respond(session); }
      if (url.pathname === '/api/auth/captcha') return respond({ captcha_id: 'offline-captcha', captcha_image: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLbtAAAAABJRU5ErkJggg==' });
      if (url.pathname === '/api/auth/register') {
        posts.push({ path: url.pathname, body: route.request().postDataJSON() });
        if (registerMode === 'success') { signedIn = true; return respond({ ...session, referral_bound: false }); }
        return route.fulfill({ status: registerMode === 'unavailable' ? 409 : 400, json: { success: false, message: registerMode === 'captcha' ? 'invalid captcha' : registerMode === 'unavailable' ? 'invitation temporarily unavailable' : 'invalid invitation code' } });
      }
      if (url.pathname === '/api/referrals/status') {
        assert.equal(route.request().method(), 'GET');
        assert.equal(route.request().headers().authorization, undefined);
        return respond({ ...fixture.campaign, enabled: false, accepting_invites: false, blocked_reasons: ['活动尚未开启'] });
      }
      if (url.pathname === '/api/status') return respond({ password_reset_enabled: false });
      if (url.pathname.startsWith('/api/spending/')) return route.fulfill({ status: 503, json: { success: false, code: 'disabled', message: 'offline-disabled' } });
      if (url.pathname.startsWith('/api/referrals/self') && route.request().method() === 'POST') {
        const body = route.request().postDataJSON();
        posts.push({ path: url.pathname, body });
        assert.ok(route.request().headers().authorization.startsWith('Bearer offline-'));
        if (url.pathname.endsWith('/confirm')) {
          const item = items.find((entry) => entry.id === 9);
          if (conflict) {
            conflict = false;
            Object.assign(item, { version: 3, fee: '2.000000', net_amount: '38.000000' });
            return route.fulfill({ status: 409, json: { success: false, code: 'version_conflict', message: 'private raw error' } });
          }
          assert.equal(body.version, item.version);
          Object.assign(item, { status: 'confirmed', version: item.version + 1 });
          return respond(item);
        }
        if (url.pathname.endsWith('/cancel')) {
          const id = Number(url.pathname.split('/').at(-2));
          const item = items.find((entry) => entry.id === id);
          assert.ok(['requested', 'fee_quoted', 'confirmed', 'manual_review'].includes(item.status));
          assert.deepEqual(body, { version: item.version });
          Object.assign(item, { status: 'cancelled', version: item.version + 1 });
          return respond(item);
        }
        assert.equal(body.amount, '20.000001');
        assert.equal(body.totp_code, '012345');
        assert.equal(body.address, address);
        const item = { ...quote, id: 101, amount: body.amount, status: 'requested', fee: '0.000000', net_amount: body.amount, fee_expires_at: '' };
        items.push(item);
        return respond(item);
      }
      if (url.pathname.startsWith('/api/referrals/self') && mode === 'error') return route.fulfill({ status: 503, json: { success: false, message: 'offline error' } });
      if (url.pathname === '/api/referrals/self') {
        const overview = structuredClone(fixture);
        if (mode === 'disabled' || mode === 'closed-obligations' || mode === 'unbound') { overview.campaign.enabled = false; overview.campaign.accepting_invites = false; }
        if (mode === 'disabled' || mode === 'blocked') overview.summary.available_commission = '0.000000';
        if (mode === 'blocked') overview.campaign.blocked_reasons = ['成本凭证待核验'];
        overview.requires_totp = true;
        if (mode === 'oauth') overview.withdrawal_reauth_available = false;
        if (mode === 'empty' || mode === 'unbound') for (const key of Object.keys(overview.summary)) overview.summary[key] = key === 'invited_count' ? 0 : '0.000000';
        return respond(overview);
      }
      if (url.pathname === '/api/referrals/self/commissions') {
        assert.equal(url.searchParams.get('page_size'), '20');
        const second = url.searchParams.get('page') === '2';
        return respond({ total: mode === 'empty' || mode === 'unbound' ? 0 : 21, items: mode === 'empty' || mode === 'unbound' ? [] : [{ id: second ? 21 : 1, invitee_label: second ? 'Invited B' : 'Invited A', principal: '100.000000', commission: '5.000000', reversed: '0.000000', earned_at: fixture.server_time, available_at: '2026-11-15T08:00:00Z', status: 'frozen' }] });
      }
      if (url.pathname === '/api/referrals/self/withdrawals') return respond({ total: mode === 'empty' || mode === 'unbound' ? 0 : 21, items: mode === 'empty' || mode === 'unbound' ? [] : url.searchParams.get('page') === '2' ? [{ ...quote, id: 221, status: 'requested' }] : items });
      report.blockedRequests.push(`${route.request().method()} ${url.pathname}`);
      return route.abort();
    });
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', (error) => errors.push(error.message));
    await page.goto(`${origin}/login`);
    if (width < 981) await page.getByRole('button', { name: text('打开导航菜单', 'Open navigation menu') }).click();
    await expect(page.getByRole('link', { name: text('推广返利', 'Referral Rewards'), exact: true })).toBeVisible();
    await checkLayout(page, `${locale}-${width}-public`);
    await page.getByRole('link', { name: text('推广返利', 'Referral Rewards'), exact: true }).click();
    await expect(page).toHaveURL(`${origin}/login?redirect=%2Fconsole%2Freferrals`);
    await page.getByLabel(text('账号', 'Username'), { exact: true }).fill('offline-only');
    await page.getByLabel(text('密码', 'Password'), { exact: true }).fill('offline-password');
    await page.getByRole('button', { name: text('登录', 'Sign in'), exact: true }).click();
    await expect(page).toHaveURL(`${origin}/console/referrals`);
    await expect(page.getByRole('heading', { name: text('推广返利', 'Referral Rewards'), exact: true })).toBeVisible();
    await expect(page.getByText('150.123456', { exact: true })).toBeVisible();
    await expect(page.locator('.console-header').getByRole('link', { name: text('推广返利', 'Referral Rewards'), exact: true })).toBeInViewport();
    await checkLayout(page, `${locale}-${width}-ready`);
    await page.getByRole('button', { name: text('复制邀请链接', 'Copy invitation link') }).click();
    assert.equal(await page.evaluate(() => window.__referralClipboard), `${origin}/register?invite=zt-ref-abc`);
    const next = page.getByRole('button', { name: text('下一页佣金明细', 'Next commission page') });
    await next.click();
    await expect(page.getByRole('cell', { name: /Invited B/ })).toBeVisible();
    await page.getByRole('button', { name: text('下一页提现记录', 'Next withdrawal page') }).click();
    await expect(page.getByText('#221', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: text('上一页提现记录', 'Previous withdrawal page') }).click();
    const record = () => page.locator('.referral-withdrawals > li').filter({ has: page.getByText('#9', { exact: true }) });
    await expect(record().getByRole('button', { name: text('确认手续费报价', 'Confirm fee quote') })).toBeDisabled();
    await record().getByRole('checkbox').check();
    conflict = true;
    await record().getByRole('button', { name: text('确认手续费报价', 'Confirm fee quote') }).click();
    await expect(record().getByText('38.000000', { exact: true })).toBeVisible();
    await expect(record().getByRole('checkbox')).not.toBeChecked();
    await expect(record().getByRole('button', { name: text('确认手续费报价', 'Confirm fee quote') })).toBeDisabled();
    assert.equal(posts.filter((post) => post.path.endsWith('/confirm')).length, 1);
    await record().getByRole('checkbox').check();
    await record().getByRole('button', { name: text('确认手续费报价', 'Confirm fee quote') }).click();
    await expect(record().getByText(text('已确认报价，待人工打款', 'Fee confirmed, awaiting manual payment'))).toBeVisible();
    const pending = page.locator('.referral-withdrawals > li').filter({ has: page.getByText('#1', { exact: true }) });
    await pending.getByRole('button', { name: text('取消提现', 'Cancel withdrawal') }).click();
    await pending.getByRole('button', { name: text('确认取消提现', 'Confirm withdrawal cancellation') }).click();
    await expect(pending.getByText(text('已取消', 'Cancelled'), { exact: true })).toBeVisible();
    const unknown = page.locator('.referral-withdrawals > li').filter({ has: page.getByText('#4', { exact: true }) });
    await expect(unknown.getByText(text('打款结果待核验，资金继续占用', 'Payment result unverified; funds remain reserved'))).toBeVisible();
    await expect(unknown.getByRole('button')).toHaveCount(0);
    const paying = page.locator('.referral-withdrawals > li').filter({ has: page.getByText('#3', { exact: true }) });
    await expect(paying.getByRole('button')).toHaveCount(0);
    const manualReview = page.locator('.referral-withdrawals > li').filter({ has: page.getByText('#8', { exact: true }) });
    await expect(manualReview.getByRole('note')).toContainText(text('先人工核对退款及剩余有效佣金', 'Reconcile the refund and remaining valid commission before cancelling'));
    await expect(manualReview.getByRole('button', { name: text('确认手续费报价', 'Confirm fee quote') })).toHaveCount(0);
    await manualReview.getByRole('button', { name: text('取消提现', 'Cancel withdrawal') }).click();
    await manualReview.getByRole('button', { name: text('确认取消提现', 'Confirm withdrawal cancellation') }).click();
    await expect(manualReview.getByText(text('已取消', 'Cancelled'), { exact: true })).toBeVisible();
    assert.deepEqual(posts.at(-1), { path: '/api/referrals/self/withdrawals/8/cancel', body: { version: 2 } });
    await page.getByLabel(text('提现金额 (USDT)', 'Withdrawal amount (USDT)')).fill('20.000001');
    await page.getByLabel(text('TRC20 收款地址', 'TRC20 receiving address'), { exact: true }).fill(address);
    await page.getByLabel(text('当前账号密码', 'Current account password')).fill('memory-only-withdrawal');
    await page.getByLabel(text('二次验证码（选填）', 'Two-factor code (optional)')).fill('012345');
    await page.getByRole('button', { name: text('申请提现', 'Request withdrawal'), exact: true }).click();
    await expect(page.getByText('#101', { exact: true })).toBeVisible();
    await expect(page.getByLabel(text('当前账号密码', 'Current account password'))).toHaveValue('');
    await expect(page.getByLabel(text('二次验证码（选填）', 'Two-factor code (optional)'))).toHaveValue('');
    const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
    assert.ok(!storage.includes('memory-only') && !storage.includes('012345') && !storage.includes('offline-password'));
    for (const state of ['disabled', 'blocked', 'closed-obligations', 'empty', 'error', 'oauth', 'ready']) {
      mode = state;
      await page.getByRole('button', { name: text('刷新推广数据', 'Refresh referral data') }).click();
      if (state === 'disabled' || state === 'blocked') {
        await expect(page.getByText(text('推广返利暂未开放', 'Referral rewards are not open yet'), { exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: text('申请提现', 'Request withdrawal'), exact: true })).toBeDisabled();
        await expect(page.getByRole('button', { name: text('复制邀请链接', 'Copy invitation link') })).toBeDisabled();
      } else if (state === 'closed-obligations') {
        await expect(page.getByText(text('推广返利暂未开放', 'Referral rewards are not open yet'), { exact: true })).toBeVisible();
        await expect(page.getByRole('button', { name: text('申请提现', 'Request withdrawal'), exact: true })).toBeEnabled();
        await expect(page.getByRole('button', { name: text('复制邀请链接', 'Copy invitation link') })).toBeDisabled();
      } else if (state === 'empty') {
        await expect(page.getByText(text('暂无充值佣金明细。', 'No top-up commissions yet.'))).toBeVisible();
        await expect(page.getByText(text('暂无提现记录。', 'No withdrawals yet.'))).toBeVisible();
      } else if (state === 'error') {
        await expect(page.getByText(text('推广数据加载失败，请重试。', 'Referral data could not be loaded. Try again.'))).toBeVisible();
      } else if (state === 'oauth') {
        await expect(page.getByText(text('请先在账号安全设置密码，再申请提现。', 'Set a password in account security before requesting a withdrawal.'), { exact: false })).toBeVisible();
        await expect(page.getByRole('button', { name: text('申请提现', 'Request withdrawal'), exact: true })).toBeDisabled();
      } else await expect(page.getByText('150.123456', { exact: true })).toBeVisible();
      await checkLayout(page, `${locale}-${width}-${state}`);
    }
    signedIn = false;
    await page.goto(`${origin}/register?invite=zt-ref-abc&key=never-store-this`);
    await expect(page.getByText(text('推广活动当前未开放，暂不接受现金返佣绑定；本次是否绑定以注册结果为准。', 'The campaign is not open for cash-commission bindings. This registration result determines whether an invitation is bound.'))).toBeVisible();
    await page.getByLabel(text('账号', 'Username'), { exact: true }).fill('offline-only');
    await page.getByLabel(text('密码', 'Password'), { exact: true }).fill('in-memory-register');
    await page.getByLabel(text('验证码', 'Verification code'), { exact: true }).fill('bad');
    await page.getByRole('button', { name: text('创建账号', 'Create account'), exact: true }).click();
    await expect(page.getByText(text('验证码错误或已过期，请输入新的验证码', 'The verification code is incorrect or expired. Enter the new code.'))).toBeVisible();
    assert.equal(posts.at(-1).body.invite_code, 'zt-ref-abc');
    assert.deepEqual(await page.evaluate(() => ({ ...sessionStorage })), { invite_code: 'zt-ref-abc' });
    await page.reload();
    await expect(page.getByRole('button', { name: text('清除邀请码并普通注册', 'Remove invitation and register normally') })).toBeVisible();
    registerMode = 'invitation';
    await page.getByLabel(text('账号', 'Username'), { exact: true }).fill('offline-only');
    await page.getByLabel(text('密码', 'Password'), { exact: true }).fill('in-memory-register');
    await page.getByLabel(text('验证码', 'Verification code'), { exact: true }).fill('bad');
    await page.getByRole('button', { name: text('创建账号', 'Create account'), exact: true }).click();
    await expect(page.getByText(text('邀请码无效，请核对或清除邀请码后普通注册。', 'The invitation code is invalid. Check it or remove it to register normally.'))).toBeVisible();
    registerMode = 'unavailable';
    await page.getByLabel(text('验证码', 'Verification code'), { exact: true }).fill('retry');
    await page.getByRole('button', { name: text('创建账号', 'Create account'), exact: true }).click();
    await expect(page.getByText(text('推广活动暂不可用，可移除邀请码后继续注册', 'Referral participation is temporarily unavailable. Remove the invitation code to continue registering.'))).toBeVisible();
    await page.getByRole('button', { name: text('清除邀请码并普通注册', 'Remove invitation and register normally') }).click();
    assert.equal(await page.evaluate(() => sessionStorage.getItem('invite_code')), null);
    assert.equal(new URL(page.url()).searchParams.has('invite'), false);
    await checkLayout(page, `${locale}-${width}-registration`);
    mode = 'unbound';
    registerMode = 'success';
    await page.goto(`${origin}/register?invite=zt-ref-abc&redirect=%2Fconsole%2Freferrals`);
    await page.getByLabel(text('账号', 'Username'), { exact: true }).fill('offline-only');
    await page.getByLabel(text('密码', 'Password'), { exact: true }).fill('in-memory-register');
    await page.getByLabel(text('验证码', 'Verification code'), { exact: true }).fill('code');
    await page.getByRole('button', { name: text('创建账号', 'Create account'), exact: true }).click();
    await expect(page).toHaveURL(`${origin}/console/referrals`);
    await expect(page.getByText(text('注册成功，但本次未建立推广返佣关系，请勿将本次邀请视为已生效。', 'Registration succeeded, but no referral commission relationship was created. This invitation must not be treated as active.'))).toBeVisible();
    await expect(page.getByText(text('暂无充值佣金明细。', 'No top-up commissions yet.'))).toBeVisible();
    await checkLayout(page, `${locale}-${width}-unbound-registration`);
    assert.deepEqual(errors, []);
    report.cases.push({ locale, width, status: 'passed', mutations: posts.length, screenshots: 10 });
    await context.close();
  }
  assert.deepEqual(report.blockedRequests, []);
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  report.error = error.stack ?? error.message;
  process.exitCode = 1;
} finally {
  await browser?.close();
  if (server) await new Promise((resolve) => server.httpServer.close(resolve));
  await writeFile(path.join(evidence, 'result.json'), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
}
