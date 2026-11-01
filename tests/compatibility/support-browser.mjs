import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { createServer as createNetServer } from 'node:net';
import { mkdir, readFile, writeFile, realpath } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium, expect as baseExpect } from '@playwright/test';
import {
  defaultVisionPath,
  generateVisionFixture,
} from './support-vision-fixture.mjs';

const repo = fileURLToPath(new URL('../../', import.meta.url));
const stamp = new Date().toISOString().replace(/[:.]/g, '-');
const output = path.resolve(
  process.env.SUPPORT_E2E_OUTPUT ||
    path.join(path.dirname(defaultVisionPath), `e2e-${stamp}`),
);
const requireConsole = createRequire(
  path.join(repo, 'web/console/package.json'),
);
const { createServer: createViteServer, transformWithEsbuild } = await import(
  pathToFileURL(requireConsole.resolve('vite'))
);
const react = requireConsole('@vitejs/plugin-react').default;
const report = {
  pass: false,
  synthetic: true,
  paid_calls: 0,
  external_requests: [],
  page_errors: [],
  asset_failures: [],
  cases: [],
  screenshots: [],
  commands: [],
  output,
  started_at: new Date().toISOString(),
};
const password = 'fixture-only-password';
const scrollOnly = process.argv.includes('--scroll-only');
const communityOnly = process.argv.includes('--community-only');
report.scope = communityOnly
  ? 'public-and-support-community-only'
  : scrollOnly
    ? 'support-scroll-only'
    : 'full';
let fixture, browser, publicServer, adminServer, currentPage;
let fixtureBase, publicBase, adminBase;
const logs = [];
const expect = baseExpect.configure({ timeout: 15_000 });

async function freePort() {
  const socket = createNetServer();
  await new Promise((resolve, reject) => {
    socket.once('error', reject);
    socket.listen(0, '127.0.0.1', resolve);
  });
  const port = socket.address().port;
  await new Promise((resolve, reject) =>
    socket.close((error) => (error ? reject(error) : resolve())),
  );
  return port;
}
function execute(command, args, cwd, env = process.env) {
  report.commands.push({ command, args, cwd });
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let text = '';
    child.stdout.on('data', (chunk) => {
      text += chunk;
    });
    child.stderr.on('data', (chunk) => {
      text += chunk;
    });
    child.once('error', reject);
    child.once('exit', (code) =>
      code === 0
        ? resolve(text)
        : reject(new Error(`${command} exited ${code}: ${text.slice(-6000)}`)),
    );
  });
}
async function poll(operation, label, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      return await operation();
    } catch (error) {
      last = error;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
  }
  throw new Error(`${label}: ${last?.message || 'timeout'}`);
}
async function status() {
  const response = await fetch(fixtureBase + '/__fixture/status');
  assert.equal(response.status, 200);
  const envelope = await response.json();
  assert.equal(envelope.data.paid_calls, 0);
  return envelope.data;
}
async function context(width, username, admin = false) {
  const base = admin ? adminBase : publicBase;
  const ctx = await browser.newContext({
    viewport: { width, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    colorScheme: 'light',
    serviceWorkers: 'block',
  });
  ctx.setDefaultTimeout(15_000);
  ctx.setDefaultNavigationTimeout(90_000);
  const allowed = new Set([publicBase, adminBase, fixtureBase]);
  await ctx.route('**/*', (route) => {
    const url = new URL(route.request().url());
    if (!allowed.has(url.origin)) {
      report.external_requests.push({
        origin: url.origin,
        pathname: url.pathname,
      });
      return route.abort();
    }
    return route.continue();
  });
  await ctx.addInitScript(() => {
    localStorage.setItem('ztapi.locale', 'zh-CN');
  });
  const logged = await ctx.request.post(base + '/api/auth/login', {
    headers: { Origin: base },
    data: { username, password },
  });
  assert.equal(logged.status(), 200, `synthetic login failed for ${username}`);
  const session = (await logged.json()).data;
  assert.equal(session.user.username, username);
  const page = await ctx.newPage();
  currentPage = page;
  const observed = {
    message_posts: 0,
    attachment_gets: 0,
    attachment_jwt: [],
    status_posts: 0,
  };
  page.on('pageerror', (error) =>
    report.page_errors.push({ width, username, message: error.message }),
  );
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (
      /\/api\/support\/conversations\/[^/]+\/messages$/.test(url.pathname) &&
      request.method() === 'POST'
    )
      observed.message_posts++;
    if (
      /\/api\/(?:admin\/)?support\/attachments\/\d+$/.test(url.pathname) &&
      request.method() === 'GET'
    ) {
      observed.attachment_gets++;
      observed.attachment_jwt.push(
        /^Bearer /.test(request.headers().authorization || '') &&
          request.headers()['new-api-user'] === String(session.user.id),
      );
    }
    if (
      /\/api\/admin\/support\/tickets\/\d+\/status$/.test(url.pathname) &&
      request.method() === 'POST'
    )
      observed.status_posts++;
  });
  page.on('response', (response) => {
    const url = new URL(response.url());
    if (
      response.status() >= 400 &&
      !url.pathname.startsWith('/api/') &&
      !url.pathname.endsWith('favicon.ico')
    )
      report.asset_failures.push({
        pathname: url.pathname,
        status: response.status(),
        width,
      });
  });
  return { context: ctx, page, session, base, observed };
}
async function api(actor, method, pathname, data) {
  return actor.context.request.fetch(actor.base + pathname, {
    method,
    headers: {
      Authorization: `Bearer ${actor.session.access_token}`,
      'New-Api-User': String(actor.session.user.id),
      Origin: actor.base,
    },
    ...(data === undefined ? {} : { data }),
  });
}
async function data(actor, pathname) {
  const response = await api(actor, 'GET', pathname);
  assert.equal(response.status(), 200, pathname);
  return (await response.json()).data;
}
async function capture(page, name) {
  currentPage = page;
  await page.evaluate(() => document.fonts.ready);
  const geometry = await page.evaluate(() => ({
    viewport: innerWidth,
    document: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
    offenders: [...document.querySelectorAll('body *')]
      .filter(
        (el) =>
          el.getBoundingClientRect().right > innerWidth + 1 ||
          el.getBoundingClientRect().left < -1,
      )
      .slice(0, 12)
      .map((el) => ({
        tag: el.tagName,
        class: String(el.className).slice(0, 100),
        right: Math.round(el.getBoundingClientRect().right),
        left: Math.round(el.getBoundingClientRect().left),
      })),
  }));
  await page.screenshot({
    path: path.join(output, `${name}.png`),
    fullPage: true,
    animations: 'disabled',
  });
  await writeFile(
    path.join(output, `${name}.aria.txt`),
    await page.locator('body').ariaSnapshot(),
  );
  report.screenshots.push({ name, file: `${name}.png`, geometry });
  assert.ok(
    geometry.document <= geometry.viewport + 1,
    `${name}: horizontal overflow ${JSON.stringify(geometry)}`,
  );
  return geometry;
}
async function step(name, operation) {
  console.log(`SUPPORT_E2E_START ${name}`);
  const started = Date.now();
  try {
    const details = await operation();
    report.cases.push({
      name,
      pass: true,
      elapsed_ms: Date.now() - started,
      details,
    });
    console.log(`SUPPORT_E2E_PASS ${name}`);
  } catch (error) {
    report.cases.push({
      name,
      pass: false,
      elapsed_ms: Date.now() - started,
      error: error.message,
    });
    throw error;
  }
}

try {
  await mkdir(output, { recursive: true });
  const ports = await Promise.all([freePort(), freePort(), freePort()]);
  assert.equal(new Set(ports).size, 3);
  [fixtureBase, publicBase, adminBase] = ports.map(
    (port) => `http://127.0.0.1:${port}`,
  );
  report.origins = {
    fixture: fixtureBase,
    public: publicBase,
    admin: adminBase,
  };
  const binary = path.join(output, 'ztapi-support-fixture.exe');
  await execute(
    'go',
    ['build', '-o', binary, './cmd/ztapi-support-fixture'],
    path.join(repo, 'server'),
    { ...process.env, GOPROXY: 'off', GOSUMDB: 'off', GOTOOLCHAIN: 'local' },
  );
  const args = [
    '-listen',
    `127.0.0.1:${ports[0]}`,
    '-public-origin',
    publicBase,
    '-admin-origin',
    adminBase,
    '-state-root',
    output,
  ];
  report.commands.push({ command: binary, args, cwd: repo });
  fixture = spawn(binary, args, {
    cwd: repo,
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  fixture.stdout.on('data', (chunk) => logs.push(chunk.toString()));
  fixture.stderr.on('data', (chunk) => logs.push(chunk.toString()));
  fixture.on('error', (error) => logs.push(error.message));
  report.fixture = await poll(status, 'fixture startup');
  const dependencyRoot = path.dirname(
    await realpath(path.join(repo, 'node_modules')),
  );
  const commonServer = {
    host: '127.0.0.1',
    strictPort: true,
    proxy: { '/api': { target: fixtureBase, changeOrigin: false } },
    fs: { allow: [repo, dependencyRoot] },
  };
  publicServer = await createViteServer({
    configFile: false,
    root: path.join(repo, 'web/console'),
    cacheDir: path.join(output, 'node_modules/.vite-public'),
    plugins: [react()],
    server: { ...commonServer, port: ports[1] },
  });
  await publicServer.listen();
  adminServer = await createViteServer({
    configFile: false,
    root: path.join(repo, 'server/web/classic'),
    cacheDir: path.join(output, 'node_modules/.vite-admin'),
    plugins: [
      {
        name: 'isolated-admin-entry',
        transformIndexHtml: {
          order: 'pre',
          handler: () =>
            '<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"><title>ZTAPI local admin acceptance</title></head><body><div id="root"></div><script type="module" src="/src/ztapi/admin-entry.jsx"></script></body></html>',
        },
      },
      {
        name: 'classic-jsx-files',
        async transform(code, id) {
          if (/\/src\/.*\.js$/.test(id.replaceAll('\\', '/')))
            return transformWithEsbuild(code, id, {
              loader: 'jsx',
              jsx: 'automatic',
            });
        },
      },
      react(),
    ],
    define: {
      'import.meta.env.VITE_REACT_APP_SERVER_URL': '""',
      'import.meta.env.VITE_ZTAPI_ADMIN_APP': '"true"',
    },
    resolve: { alias: { '@': path.join(repo, 'server/web/classic/src') } },
    optimizeDeps: { entries: ['src/ztapi/admin-entry.jsx'] },
    server: { ...commonServer, port: ports[2] },
  });
  await adminServer.listen();
  browser = await chromium.launch({ channel: 'chrome', headless: true });
  report.vision_fixture = await generateVisionFixture(browser);
  const imageBytes = await readFile(defaultVisionPath);
  const bob = await context(1440, 'fixture-bob');

  for (const width of scrollOnly || communityOnly ? [] : [390, 320, 1440]) {
    await step(`public-admin-finance-${width}`, async () => {
      const user = await context(width, `fixture-alice-${width}`);
      await user.page.goto(publicBase + '/console/support');
      await expect(
        user.page.getByRole('heading', { name: '平台客服' }),
      ).toBeVisible();
      await expect(user.page.getByLabel('消息内容')).toBeEnabled();
      const history = user.page.getByRole('navigation', { name: '客服会话' });
      await user.page
        .getByLabel('消息内容')
        .fill(`draft-not-a-conversation-${width}`);
      await user.page.waitForTimeout(400);
      assert.equal(
        (await data(user, '/api/support/conversations')).items.length,
        0,
      );
      await expect(history.getByRole('button')).toHaveCount(0);
      assert.equal(user.observed.message_posts, 0);
      await capture(user.page, `support-draft-${width}`);
      await user.page
        .getByRole('button', {
          name: new RegExp(`fixture-request-${user.session.user.id}`),
        })
        .click();
      await expect(
        user.page.getByRole('region', { name: '请求诊断证据' }),
      ).toContainText('UPSTREAM_UNAVAILABLE');
      const ownDiagnosis = await api(
        user,
        'GET',
        `/api/support/diagnostics/fixture-request-${user.session.user.id}`,
      );
      assert.equal(ownDiagnosis.status(), 200);
      const foreignDiagnosis = await api(
        bob,
        'GET',
        `/api/support/diagnostics/fixture-request-${user.session.user.id}`,
      );
      const missingDiagnosis = await api(
        bob,
        'GET',
        '/api/support/diagnostics/missing-id',
      );
      assert.equal(foreignDiagnosis.status(), 404);
      assert.equal(
        await foreignDiagnosis.text(),
        await missingDiagnosis.text(),
      );
      await user.page.getByLabel('消息内容').fill(`HTTP 503 排查 ${width}`);
      await user.page.getByRole('button', { name: '发送消息' }).click();
      await expect(
        user.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText('本地合成答复');
      const conversationId = new URL(user.page.url()).searchParams.get(
        'conversation',
      );
      assert.ok(conversationId);
      assert.equal(
        await user.page.evaluate(() => window.__supportXss === true),
        false,
      );
      assert.equal(await user.page.locator('img[src="x"]').count(), 0);
      await user.page.reload();
      await expect(
        user.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText(`HTTP 503 排查 ${width}`);
      await expect(history.getByRole('button')).toHaveCount(1);
      assert.equal(user.observed.message_posts, 1);
      const foreignConversation = await api(
        bob,
        'GET',
        `/api/support/conversations/${conversationId}`,
      );
      const missingConversation = await api(
        bob,
        'GET',
        '/api/support/conversations/does-not-exist',
      );
      assert.equal(foreignConversation.status(), 404);
      assert.equal(
        await foreignConversation.text(),
        await missingConversation.text(),
      );
      await capture(user.page, `support-history-${width}`);
      await expect(
        user.page.getByText(/上传前请遮住 API Key、密码/),
      ).toBeVisible();
      await user.page.getByLabel('上传截图').setInputFiles({
        name: 'vision-fixture.png',
        mimeType: 'image/png',
        buffer: imageBytes,
      });
      await expect(
        user.page.getByRole('button', { name: '移除截图' }),
      ).toBeVisible();
      await expect(user.page.getByLabel('消息内容')).toHaveValue('');
      await user.page.getByRole('button', { name: '发送消息' }).click();
      await expect(
        user.page.getByRole('button', { name: '查看截图' }),
      ).toBeVisible();
      await expect(user.page.getByLabel('消息内容')).toBeEnabled();
      await user.page.reload();
      await expect(
        user.page.getByRole('button', { name: '查看截图' }),
      ).toBeVisible();
      assert.equal(user.observed.attachment_gets, 0);
      const conversation = await data(
        user,
        `/api/support/conversations/${conversationId}`,
      );
      const attachment = conversation.messages.find(
        (item) => item.attachment_id,
      );
      assert.ok(attachment);
      const privateResponse = await api(
        user,
        'GET',
        `/api/support/attachments/${attachment.attachment_id}`,
      );
      assert.equal(privateResponse.status(), 200);
      const privateImage = (await privateResponse.json()).data;
      assert.equal(
        createHash('sha256')
          .update(Buffer.from(privateImage.data_base64, 'base64'))
          .digest('hex'),
        report.vision_fixture.sha256,
      );
      const foreignAttachment = await api(
        bob,
        'GET',
        `/api/support/attachments/${attachment.attachment_id}`,
      );
      const missingAttachment = await api(
        bob,
        'GET',
        '/api/support/attachments/4294967294',
      );
      assert.equal(foreignAttachment.status(), 404);
      assert.equal(
        await foreignAttachment.text(),
        await missingAttachment.text(),
      );
      await user.page.getByRole('button', { name: '查看截图' }).click();
      await expect(
        user.page.getByRole('img', { name: '会话截图' }),
      ).toBeVisible();
      assert.ok(
        await user.page
          .getByRole('img', { name: '会话截图' })
          .evaluate(
            (img) =>
              img.complete &&
              img.naturalWidth === 640 &&
              img.naturalHeight === 360 &&
              img.src.startsWith('data:image/png;base64,'),
          ),
      );
      assert.deepEqual(user.observed.attachment_jwt, [true]);
      await capture(user.page, `support-private-image-${width}`);
      await user.page.getByRole('button', { name: '关闭截图' }).click();
      await user.page.getByRole('button', { name: '申请人工工单' }).click();
      const subject = `人工核查 ${width}`;
      await user.page.getByLabel('工单主题').fill(subject);
      await user.page
        .getByLabel('问题详情')
        .fill('合成 HTTP 503 证据，请人工核实，不涉及真实客户或资金。');
      await user.page
        .getByLabel('联系方式（选填）')
        .fill('fixture-only@example.invalid');
      await user.page.getByRole('button', { name: '提交工单' }).click();
      await expect(
        user.page.getByText('人工工单处理中，后续消息交由人工，不调用 AI。'),
      ).toBeVisible();
      const ticket = (
        await data(user, `/api/support/conversations/${conversationId}`)
      ).ticket;
      assert.ok(ticket);
      const beforeHuman = await status();
      await user.page.getByLabel('消息内容').fill(`人工补充信息 ${width}`);
      await user.page.getByRole('button', { name: '发送消息' }).click();
      await expect(
        user.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText(`补充信息已交给人工工单 #${ticket.id}`);
      assert.equal((await status()).provider_calls, beforeHuman.provider_calls);
      const staff = await context(width, 'fixture-support', true);
      assert.equal(staff.session.user.role, 2);
      await staff.page.goto(adminBase + '/support/tickets');
      await expect(
        staff.page.getByRole('heading', { name: '客服工单' }),
      ).toBeVisible();
      await staff.page
        .getByRole('button', { name: new RegExp(`#${ticket.id} ${subject}`) })
        .click();
      await expect(staff.page.getByLabel('人工回复内容')).toBeVisible();
      assert.equal(staff.observed.attachment_gets, 0);
      await staff.page.getByRole('button', { name: '查看截图' }).click();
      await expect(
        staff.page.getByRole('img', { name: '工单截图' }),
      ).toBeVisible();
      assert.deepEqual(staff.observed.attachment_jwt, [true]);
      await capture(staff.page, `admin-ticket-image-${width}`);
      await staff.page.getByRole('button', { name: '关闭截图' }).click();
      const humanReply = `人工核实回复 ${width}：这是合成工单，不承诺退款或固定充值优惠。`;
      await staff.page.getByLabel('人工回复内容').fill(humanReply);
      await staff.page.getByRole('button', { name: '发送人工回复' }).click();
      await expect(
        staff.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText(humanReply);
      const afterReply = (
        await data(staff, `/api/admin/support/tickets/${ticket.id}`)
      ).ticket;
      const selectedStatus =
        afterReply.status === 'in_progress' ? 'open' : 'in_progress';
      await staff.page.getByLabel('目标工单状态').selectOption(selectedStatus);
      const [applied] = await Promise.all([
        staff.page.waitForResponse(
          (response) =>
            response
              .url()
              .endsWith(`/api/admin/support/tickets/${ticket.id}/status`) &&
            response.request().method() === 'POST',
        ),
        staff.page.getByRole('button', { name: '应用状态' }).click(),
      ]);
      assert.equal(applied.status(), 200);
      const appliedTicket = (await applied.json()).data;
      assert.equal(appliedTicket.status, selectedStatus);
      await user.page.reload();
      await expect(
        user.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText(humanReply);
      await expect(
        user.page.getByText('人工回复', { exact: true }),
      ).toBeVisible();
      assert.equal((await status()).provider_calls, beforeHuman.provider_calls);
      await capture(user.page, `support-human-reply-${width}`);
      if (width === 1440) {
        const latest = (
          await data(staff, `/api/admin/support/tickets/${ticket.id}`)
        ).ticket;
        const concurrentStatus =
          latest.status === 'open' ? 'in_progress' : 'open';
        const changed = await api(
          staff,
          'POST',
          `/api/admin/support/tickets/${ticket.id}/status`,
          {
            status: concurrentStatus,
            version: latest.version,
            idempotency_key: 'fixture-concurrent-status-1440',
          },
        );
        assert.equal(changed.status(), 200);
        await staff.page.getByLabel('目标工单状态').selectOption('resolved');
        const postCount = staff.observed.status_posts;
        const [conflict] = await Promise.all([
          staff.page.waitForResponse(
            (response) =>
              response
                .url()
                .endsWith(`/api/admin/support/tickets/${ticket.id}/status`) &&
              response.request().method() === 'POST',
          ),
          staff.page.getByRole('button', { name: '应用状态' }).click(),
        ]);
        assert.equal(conflict.status(), 409);
        await expect(staff.page.getByRole('alert')).toContainText(
          '状态已被其他员工修改',
        );
        await expect(staff.page.getByLabel('目标工单状态')).toHaveValue(
          concurrentStatus,
        );
        assert.equal(staff.observed.status_posts, postCount + 1);
      }
      await capture(staff.page, `admin-ticket-${width}`);
      const finance = await context(width, 'fixture-finance', true);
      assert.equal(finance.session.user.role, 3);
      await finance.page.goto(adminBase + '/support/tickets');
      await expect(finance.page.getByRole('alert')).toHaveText(
        '无权访问此页面',
      );
      assert.equal(
        (
          await api(
            finance,
            'GET',
            '/api/admin/support/tickets?status=all&after_id=0&limit=20',
          )
        ).status(),
        403,
      );
      assert.equal(
        (
          await api(
            finance,
            'GET',
            `/api/admin/support/attachments/${attachment.attachment_id}`,
          )
        ).status(),
        403,
      );
      await capture(finance.page, `finance-forbidden-tickets-${width}`);
      await finance.page.goto(adminBase + '/finance/overview');
      const costs = finance.page.getByRole('region', { name: '客服消耗成本' });
      await expect(
        costs.getByText('fixture-synthetic-flash').first(),
      ).toBeVisible();
      const metrics = await status();
      const financialReport = await data(
        finance,
        `/api/admin/finance-reporting/report?month=${metrics.finance_month}`,
      );
      assert.equal(financialReport.support_source_status, 'available');
      assert.equal(
        financialReport.customer_service_costs.length,
        metrics.provider_calls,
      );
      assert.equal(
        financialReport.customer_service_estimate_total,
        (metrics.provider_calls * 0.000123).toFixed(6),
      );
      assert.equal(
        financialReport.customer_service_confirmed_total,
        '0.000000',
      );
      assert.ok(
        financialReport.customer_service_costs.every(
          (row) =>
            row.status === 'estimate' &&
            row.input_tokens === 123 &&
            row.output_tokens === 45 &&
            row.cache_read_tokens === 23,
        ),
      );
      await capture(finance.page, `finance-real-support-costs-${width}`);
      const details = {
        viewport: width,
        conversation_id: conversationId,
        ticket_id: ticket.id,
        attachment_id: attachment.attachment_id,
        draft_posts: 0,
        final_message_posts: user.observed.message_posts,
        owner_diagnosis: 200,
        foreign_diagnosis: 404,
        foreign_conversation: 404,
        foreign_attachment: 404,
        attachment_sha256: report.vision_fixture.sha256,
        user_attachment_jwt: user.observed.attachment_jwt,
        staff_attachment_jwt: staff.observed.attachment_jwt,
        provider_calls_before_human: beforeHuman.provider_calls,
        provider_calls_after_human: metrics.provider_calls,
        finance_ticket_status: 403,
        finance_support_cost_rows:
          financialReport.customer_service_costs.length,
        finance_estimate_total: financialReport.customer_service_estimate_total,
        finance_confirmed_total:
          financialReport.customer_service_confirmed_total,
      };
      await user.context.close();
      await staff.context.close();
      await finance.context.close();
      return details;
    });
  }
  if (!scrollOnly && !communityOnly)
    await step('disconnected-system-terminal-recovery', async () => {
      const user = await context(390, 'fixture-recovery');
      await user.page.goto(publicBase + '/console/support');
      await expect(user.page.getByLabel('消息内容')).toBeEnabled();
      const before = await status();
      let injected = false;
      await user.context.route(
        '**/api/support/conversations/*/messages',
        async (route) => {
          if (injected || route.request().method() !== 'POST')
            return route.continue();
          const response = await route.fetch();
          assert.equal(response.status(), 200);
          const result = (await response.json()).data;
          assert.equal(result.reply.role, 'system');
          injected = true;
          await route.abort('failed');
        },
      );
      await user.page.getByLabel('消息内容').fill('FIXTURE_SYSTEM_TERMINAL');
      await user.page.getByRole('button', { name: '发送消息' }).click();
      await expect(
        user.page.getByRole('button', { name: '重试原消息' }),
      ).toBeVisible();
      assert.equal(
        await user.page.evaluate(
          (id) =>
            Boolean(sessionStorage.getItem(`ztapi.support.pending.${id}`)),
          user.session.user.id,
        ),
        true,
      );
      const conversationId = new URL(user.page.url()).searchParams.get(
        'conversation',
      );
      await user.page.reload();
      await expect(
        user.page.getByRole('log', { name: '聊天记录' }),
      ).toContainText('智能客服暂时无法完成答复');
      await expect(user.page.getByLabel('消息内容')).toBeEnabled();
      await expect(
        user.page.getByRole('button', { name: '新建会话' }),
      ).toBeEnabled();
      await expect(
        user.page
          .getByRole('navigation', { name: '客服会话' })
          .getByRole('button'),
      ).toBeEnabled();
      assert.equal(
        await user.page.evaluate(
          (id) => sessionStorage.getItem(`ztapi.support.pending.${id}`),
          user.session.user.id,
        ),
        null,
      );
      assert.equal(user.observed.message_posts, 1);
      assert.equal((await status()).provider_calls, before.provider_calls + 1);
      await capture(user.page, 'support-system-terminal-recovery-390');
      await user.page.getByRole('button', { name: '新建会话' }).click();
      await expect(user.page.getByLabel('消息内容')).toBeEnabled();
      assert.equal(user.observed.message_posts, 1);
      await user.context.close();
      return {
        conversation_id: conversationId,
        real_handler_finished_before_client_disconnect: injected,
        pending_cleared: true,
        message_posts: 1,
        provider_call_delta: 1,
        operations_unlocked: true,
        automatic_post_retries: 0,
      };
    });
  if (scrollOnly)
    for (const width of [390, 320, 1440]) {
      await step(`support-scroll-${width}`, async () => {
        const user = await context(width, `fixture-alice-${width}`);
        const staff = await context(width, 'fixture-support', true);
        await user.page.goto(publicBase + '/console/support');
        await expect(user.page.getByLabel('消息内容')).toBeEnabled();
        await user.page.getByLabel('消息内容').fill(`滚动验收 ${width}`);
        await user.page.getByRole('button', { name: '发送消息' }).click();
        await expect(
          user.page.getByRole('log', { name: '聊天记录' }),
        ).toContainText('本地合成答复');
        const conversationId = new URL(user.page.url()).searchParams.get(
          'conversation',
        );
        const created = await api(user, 'POST', '/api/support/tickets', {
          conversation_id: conversationId,
          subject: `滚动人工工单 ${width}`,
          content: 'Only synthetic scroll acceptance history.',
          idempotency_key: randomUUID(),
        });
        assert.equal(created.status(), 201);
        const ticket = (await created.json()).data;
        const reply = async (id, content) => {
          const response = await api(
            staff,
            'POST',
            `/api/admin/support/tickets/${id}/replies`,
            { content, idempotency_key: randomUUID() },
          );
          assert.equal(response.status(), 201);
        };
        const longHistory = '合成历史排查记录，不含客户资料。'.repeat(100);
        await reply(ticket.id, `历史一 ${longHistory}`);
        await reply(ticket.id, `历史二 ${longHistory}`);
        const openingReply = `打开会话应可见的最新人工回复 ${width}`;
        await reply(ticket.id, openingReply);
        await user.page.reload();
        const log = user.page.getByRole('log', { name: '聊天记录' });
        const bottom = () =>
          log.evaluate((el) =>
            Math.max(0, el.scrollHeight - el.clientHeight - el.scrollTop),
          );
        await expect(log).toContainText(openingReply);
        assert.ok(
          (await log.evaluate((el) => el.scrollHeight - el.clientHeight)) > 200,
          'scroll fixture must overflow the internal chat log',
        );
        await expect.poll(bottom).toBeLessThanOrEqual(2);
        await log.scrollIntoViewIfNeeded();
        await expect(
          user.page.getByText(openingReply, { exact: true }),
        ).toBeInViewport();
        await capture(user.page, `support-open-at-latest-${width}`);
        await log.evaluate((el) => {
          el.scrollTop = 0;
          el.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        const whileReading = `读历史时不要强拉的新增人工回复 ${width}`;
        await reply(ticket.id, whileReading);
        await expect(log).toContainText(whileReading);
        assert.ok(
          await log.evaluate((el) => el.scrollTop <= 2),
          'incoming reply pulled a reader away from history',
        );
        await expect(
          user.page.getByText(whileReading, { exact: true }),
        ).not.toBeInViewport();
        await capture(user.page, `support-preserve-history-${width}`);
        await log.evaluate((el) => {
          el.scrollTop = Math.max(0, el.scrollHeight - el.clientHeight - 20);
          el.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        const followReply = `接近底部应自动跟随的人工回复 ${width}`;
        await reply(ticket.id, followReply);
        await expect(log).toContainText(followReply);
        await expect.poll(bottom).toBeLessThanOrEqual(2);
        await log.scrollIntoViewIfNeeded();
        await expect(
          user.page.getByText(followReply, { exact: true }),
        ).toBeInViewport();
        await capture(user.page, `support-follow-new-reply-${width}`);
        await log.evaluate((el) => {
          el.scrollTop = 0;
          el.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        await user.page
          .getByLabel('消息内容')
          .fill(`从旧历史位置自己发送 ${width}`);
        await user.page.getByRole('button', { name: '发送消息' }).click();
        await expect(log).toContainText(`补充信息已交给人工工单 #${ticket.id}`);
        await expect.poll(bottom).toBeLessThanOrEqual(2);
        await log.scrollIntoViewIfNeeded();
        await expect(
          log.getByText(new RegExp(`补充信息已交给人工工单 #${ticket.id}`)),
        ).toBeInViewport();
        await capture(user.page, `support-own-send-at-latest-${width}`);
        const ownSendBottom = await bottom();
        const another = await api(user, 'POST', '/api/support/conversations', {
          idempotency_key: randomUUID(),
        });
        assert.equal(another.status(), 201);
        const otherId = (await another.json()).data.id;
        const otherCreated = await api(user, 'POST', '/api/support/tickets', {
          conversation_id: otherId,
          subject: '第二滚动工单',
          content: 'Synthetic history only.',
          idempotency_key: randomUUID(),
        });
        assert.equal(otherCreated.status(), 201);
        const otherTicket = (await otherCreated.json()).data;
        await reply(otherTicket.id, longHistory);
        const otherLatest = `第二会话最新人工回复 ${width}`;
        await reply(otherTicket.id, otherLatest);
        await user.page.reload();
        await expect(log).toContainText(`从旧历史位置自己发送 ${width}`);
        await log.evaluate((el) => {
          el.scrollTop = 0;
          el.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        const history = user.page.getByRole('navigation', { name: '客服会话' });
        await history
          .getByRole('button', { name: '新对话', exact: true })
          .click();
        await expect(log).toContainText(otherLatest);
        await expect.poll(bottom).toBeLessThanOrEqual(2);
        await log.evaluate((el) => {
          el.scrollTop = 0;
          el.dispatchEvent(new Event('scroll', { bubbles: true }));
        });
        await history
          .getByRole('button', { name: `滚动验收 ${width}`, exact: true })
          .click();
        await expect(log).toContainText(`从旧历史位置自己发送 ${width}`);
        await expect.poll(bottom).toBeLessThanOrEqual(2);
        await capture(user.page, `support-switch-at-latest-${width}`);
        const metrics = await status();
        await user.context.close();
        await staff.context.close();
        return {
          viewport: width,
          opens_at_latest: true,
          reader_history_position_preserved: true,
          near_bottom_reply_followed: true,
          human_reply_in_viewport: true,
          own_send_scroll_bottom: ownSendBottom,
          switching_conversations_at_latest: true,
          provider_calls: metrics.provider_calls,
          paid_calls: metrics.paid_calls,
        };
      });
    }
  if (communityOnly)
    for (const width of [390, 320, 1440]) {
      await step(`public-support-community-${width}`, async () => {
        const visitor = await browser.newContext({
          viewport: { width, height: 1000 },
          deviceScaleFactor: 1,
          locale: 'zh-CN',
          serviceWorkers: 'block',
        });
        visitor.setDefaultTimeout(15_000);
        visitor.setDefaultNavigationTimeout(90_000);
        await visitor.addInitScript(() =>
          localStorage.setItem('ztapi.locale', 'zh-CN'),
        );
        await visitor.route('**/*', (route) => {
          const url = new URL(route.request().url());
          if (url.origin !== publicBase) {
            report.external_requests.push({
              origin: url.origin,
              pathname: url.pathname,
            });
            return route.abort();
          }
          return route.continue();
        });
        const page = await visitor.newPage();
        currentPage = page;
        page.on('pageerror', (error) =>
          report.page_errors.push({
            width,
            username: 'unauthenticated-visitor',
            message: error.message,
          }),
        );
        assert.equal((await visitor.cookies()).length, 0);
        await page.goto(publicBase + '/');
        await expect(
          page.getByRole('heading', { name: '一个接口，连接主流 AI 模型' }),
        ).toBeVisible();
        if (width < 980)
          await page.getByRole('button', { name: '打开导航菜单' }).click();
        const group = page.getByRole('link', {
          name: '用户交流群',
          exact: true,
        });
        await expect(group).toBeVisible();
        await expect(group).toBeInViewport();
        await expect(group).toHaveAttribute('href', 'https://t.me/ztapivip');
        await expect(group).toHaveAttribute('target', '_blank');
        await expect(group).toHaveAttribute('rel', 'noopener noreferrer');
        assert.equal(await group.locator('svg').count(), 1);
        await capture(page, `public-community-${width}`);
        assert.equal((await visitor.cookies()).length, 0);
        await visitor.close();
        const user = await context(width, `fixture-alice-${width}`);
        await user.page.goto(publicBase + '/console/support');
        await expect(user.page.getByLabel('消息内容')).toBeEnabled();
        const supportGroup = user.page.getByRole('link', {
          name: '用户交流群',
          exact: true,
        });
        await expect(supportGroup).toBeVisible();
        await expect(supportGroup).toBeInViewport();
        await expect(supportGroup).toHaveAttribute(
          'href',
          'https://t.me/ztapivip',
        );
        await expect(supportGroup).toHaveAttribute('target', '_blank');
        await expect(supportGroup).toHaveAttribute(
          'rel',
          'noopener noreferrer',
        );
        await expect(
          user.page.getByRole('button', { name: '申请人工工单' }),
        ).toBeEnabled();
        await capture(user.page, `support-community-${width}`);
        await user.context.close();
        return {
          viewport: width,
          unauthenticated_home_visible: true,
          mobile_menu_entry: width < 980,
          support_entry_visible: true,
          href: 'https://t.me/ztapivip',
          target: '_blank',
          rel: 'noopener noreferrer',
          lucide_icon: true,
          external_navigation_performed: false,
        };
      });
    }
  await bob.context.close();
  assert.deepEqual(report.external_requests, []);
  assert.deepEqual(report.page_errors, []);
  assert.deepEqual(report.asset_failures, []);
  report.final_metrics = await status();
  report.pass = true;
} catch (error) {
  report.failure = String(error.stack || error).replace(
    /eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/g,
    '[REDACTED_SYNTHETIC_JWT]',
  );
  if (currentPage && !currentPage.isClosed()) {
    try {
      await currentPage.screenshot({
        path: path.join(output, 'failure.png'),
        fullPage: true,
      });
      await writeFile(
        path.join(output, 'failure.aria.txt'),
        await currentPage.locator('body').ariaSnapshot(),
      );
    } catch {}
  }
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  if (publicServer) await publicServer.close();
  if (adminServer) await adminServer.close();
  if (fixture && fixture.exitCode === null) {
    fixture.kill();
    await Promise.race([
      new Promise((resolve) => fixture.once('exit', resolve)),
      new Promise((resolve) => setTimeout(resolve, 5000)),
    ]);
  }
  report.completed_at = new Date().toISOString();
  await mkdir(output, { recursive: true });
  await writeFile(path.join(output, 'fixture.log'), logs.join(''));
  await writeFile(
    path.join(output, 'results.json'),
    JSON.stringify(report, null, 2),
  );
  console.log(
    JSON.stringify(
      {
        pass: report.pass,
        output,
        cases: report.cases.map(({ name, pass }) => ({ name, pass })),
        failure: report.failure,
      },
      null,
      2,
    ),
  );
}
