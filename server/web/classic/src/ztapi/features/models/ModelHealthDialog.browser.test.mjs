import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { chromium, expect } from '@playwright/test';

// Run against the classic admin dev server; every API request is synthetic.
const baseURL =
  process.env.ZTAPI_HEALTH_UI_BASE_URL || 'http://127.0.0.1:51843';
assert.equal(new URL(baseURL).hostname, '127.0.0.1');
const output = process.env.ZTAPI_HEALTH_UI_EVIDENCE_DIR;
assert.ok(
  output && path.isAbsolute(output),
  'An absolute evidence directory is required',
);
await mkdir(output, { recursive: true });

const now = 1788710400;
const model = {
  id: 17,
  source_model: 'upstream-example',
  public_name: 'zt-example',
  provider_family: 'openai',
  protocol: 'openai',
  publication_blockers: ['health_circuit_open'],
  enabled_groups: ['default'],
  published: false,
  route_ready: true,
  enabled_route_count: 1,
  input_price_per_million: '2.50000000',
  output_price_per_million: '10.00000000',
};
const fixture = {
  model_id: 17,
  enabled: true,
  observed: true,
  state: {
    ModelID: 17,
    Generation: 4,
    Open: true,
    ConsecutiveFailures: 2,
    CompletionSequence: 152,
  },
  window: {
    ValidSamples: 150,
    Failures: 3,
    WindowStart: now - 86400,
    WindowEnd: now,
  },
  coverage: [
    {
      Stream: false,
      Source: 'real',
      ValidSamples: 12,
      UnknownSamples: 1,
      LastValidAt: now,
    },
    {
      Stream: true,
      Source: 'real',
      ValidSamples: 3,
      UnknownSamples: 0,
      LastValidAt: now - 120,
    },
  ],
  events: [
    {
      ID: 152,
      CompletionSequence: 152,
      Generation: 4,
      ConfigVersion: 8,
      Counted: true,
      Result: 'failure',
      Reason: 'upstream_5xx',
      HTTPStatus: 503,
      ChannelID: 42,
      UpstreamProtocol: 'openai',
      UpstreamRequestID: `upstream-${'long-request-id-'.repeat(12)}`,
      RequestID: 'request-local-152',
      CompletedAt: now,
      Source: 'real',
      Stream: true,
      Outcome: JSON.stringify({
        FinishReasons: ['length'],
        TerminalStatus: 'interrupted',
        response_body: 'PRIVATE_BODY',
      }),
    },
  ],
  incidents: [
    {
      ID: 1,
      Generation: 4,
      TriggerEventID: 152,
      Rule: 'consecutive_2',
      OpenedAt: now,
      Failures: 2,
      ValidSamples: 2,
      UnpublishedAt: now + 1,
    },
  ],
  outbox: [
    {
      id: 2,
      kind: 'unpublish',
      incident_id: 1,
      status: 'done',
      attempts: 1,
      delivered_at: now + 1,
    },
    {
      id: 1,
      kind: 'alert',
      incident_id: 1,
      status: 'pending',
      attempts: 2,
      last_error: 'delivery_timeout',
      next_attempt_at: now + 300,
      lease_token: 'LEASE_SECRET',
    },
  ],
};
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const results = [];
try {
  for (const width of [1440, 390, 320]) {
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      serviceWorkers: 'block',
    });
    const page = await context.newPage();
    const writes = [];
    const unexpected = [];
    const pageErrors = [];
    let health = structuredClone(fixture);
    let role = 100;
    page.on('pageerror', (error) => pageErrors.push(error.message));
    await context.route('**/*', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== new URL(baseURL).origin) {
        unexpected.push(`${request.method()} external resource`);
        return route.abort();
      }
      if (!url.pathname.startsWith('/api/')) return route.continue();
      const respond = (data, status = 200) =>
        route.fulfill({ status, json: { success: status === 200, data } });
      if (url.pathname === '/api/auth/refresh')
        return respond({
          access_token: 'synthetic-local-test',
          expires_in: 3600,
          user: { id: 1, role, username: 'local-review' },
        });
      if (url.pathname === '/api/models/ztapi/health/status')
        return respond({
          enabled: true,
          worker_status: [
            {
              component: 'alert',
              code: 'alert_recipient_missing_or_invalid',
              updated_at: now,
              reference: 'PRIVATE_REFERENCE',
            },
            {
              component: 'probe',
              code: 'probe_identity_missing',
              updated_at: now,
            },
          ],
          probe_budget: {
            allocated_nano_usd: 100000000,
            accounted_nano_usd: 50000000,
          },
        });
      if (url.pathname === '/api/models/ztapi/17/health')
        return respond(health);
      if (
        url.pathname === '/api/models/ztapi/17/health/recover' &&
        request.method() === 'POST'
      ) {
        const body = request.postDataJSON();
        writes.push({ path: url.pathname, body });
        assert.deepEqual(body, { generation: 4, evidence: 'local-review:17' });
        health.state.Open = false;
        health.state.Generation = 5;
        health.state.ConsecutiveFailures = 0;
        return respond({
          model_id: 17,
          recovered: true,
          published: false,
          publication_required: true,
        });
      }
      if (url.pathname === '/api/models/ztapi/')
        return respond({ items: [model] });
      if (url.pathname === '/api/models/ztapi/audit-events')
        return respond({ items: [] });
      unexpected.push(`${request.method()} ${url.pathname}`);
      return respond({}, 404);
    });
    await page.goto(`${baseURL}/models`);
    await page
      .getByRole('button', { name: '健康状态 zt-example', exact: true })
      .click();
    const dialog = page.getByRole('dialog', { name: '模型健康状态' });
    await expect(dialog.getByText('已熔断', { exact: true })).toBeVisible();
    await expect(dialog.getByText('告警接收配置缺失或无效')).toBeVisible();
    const assertFit = async () => {
      const fit = await dialog.evaluate((element) => {
        const rect = element.getBoundingClientRect();
        const outside = Array.from(
          element.querySelectorAll('button,textarea,dt,dd,h2,h3,summary'),
        ).filter((child) => {
          if (!child.getClientRects().length) return false;
          const box = child.getBoundingClientRect();
          return box.left < rect.left - 1 || box.right > rect.right + 1;
        });
        return (
          rect.left >= -1 &&
          rect.right <= innerWidth + 1 &&
          element.scrollWidth <= element.clientWidth + 1 &&
          outside.length === 0
        );
      });
      assert.equal(fit, true, `Panel overflow at ${width}px`);
    };
    await assertFit();
    await page.screenshot({
      path: path.join(output, `health-${width}-overview.png`),
    });
    await dialog.getByRole('tab', { name: '请求事件' }).click();
    await expect(dialog.getByText('length', { exact: true })).toBeVisible();
    await expect(
      dialog.getByText(fixture.events[0].UpstreamRequestID, { exact: true }),
    ).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `health-${width}-events.png`),
    });
    await dialog.getByRole('tab', { name: '事故与通知' }).click();
    await expect(dialog.getByText('下架任务已完成')).toBeVisible();
    await expect(dialog.getByText('等待发送')).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `health-${width}-incidents.png`),
    });
    assert.doesNotMatch(
      await dialog.innerText(),
      /PRIVATE_BODY|LEASE_SECRET|PRIVATE_REFERENCE/,
    );
    await dialog.getByRole('tab', { name: '概览' }).click();
    const submit = dialog.getByRole('button', { name: '解除熔断（保持下架）' });
    await expect(submit).toBeDisabled();
    await dialog
      .getByRole('textbox', { name: /复验记录/ })
      .fill('local-review:17');
    await expect(submit).toBeDisabled();
    await dialog
      .getByRole('checkbox', { name: /确认解除熔断但保持下架/ })
      .check();
    await submit.click();
    await expect(dialog.getByText('熔断已解除，模型保持下架。')).toBeVisible();
    await expect(
      dialog.getByRole('button', { name: '解除熔断（保持下架）' }),
    ).toHaveCount(0);
    assert.equal(writes.length, 1);
    await page.screenshot({
      path: path.join(output, `health-${width}-recovered.png`),
    });
    await dialog
      .getByRole('button', { name: '关闭健康状态', exact: true })
      .click();
    health = {
      ...health,
      enabled: false,
      observed: false,
      state: null,
      window: { ValidSamples: 0, Failures: 0 },
    };
    await page
      .getByRole('button', { name: '健康状态 zt-example', exact: true })
      .click();
    await expect(dialog.getByText('采集未启用', { exact: true })).toBeVisible();
    await expect(dialog.locator('.is-available')).toHaveCount(0);
    role = 2;
    health = structuredClone(fixture);
    await page.reload();
    await page
      .getByRole('button', { name: '健康状态 zt-example', exact: true })
      .click();
    await expect(dialog.getByText('已熔断', { exact: true })).toBeVisible();
    await expect(dialog.getByText('只读权限', { exact: true })).toBeVisible();
    await expect(dialog.getByRole('textbox', { name: /复验记录/ })).toHaveCount(
      0,
    );
    await expect(
      dialog.getByRole('button', { name: '解除熔断（保持下架）' }),
    ).toHaveCount(0);
    await page.screenshot({
      path: path.join(output, `health-${width}-readonly.png`),
    });
    assert.deepEqual(unexpected, []);
    assert.deepEqual(pageErrors, []);
    results.push({
      width,
      height: 1000,
      result: 'PASS',
      recovery_posts: writes.length,
      readonly_role_verified: true,
      unexpected_requests: unexpected.length,
      page_errors: pageErrors.length,
    });
    await context.close();
  }
  await writeFile(
    path.join(output, 'health-ui-browser.json'),
    `${JSON.stringify({ result: 'PASS', browser: 'local headless Chrome', api: 'synthetic route fixtures only', production_operations: false, upstream_inference_calls: 0, results }, null, 2)}\n`,
  );
  console.log(JSON.stringify(results, null, 2));
} finally {
  await browser.close();
}
