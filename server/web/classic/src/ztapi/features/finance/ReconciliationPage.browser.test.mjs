import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { chromium, expect } from '@playwright/test';

// Local-only acceptance: intercept every API and reject all external resources.
const assetRoot = fileURLToPath(new URL('../../../../dist/', import.meta.url));
let server;
let baseURL = process.env.ZTAPI_RECONCILIATION_UI_BASE_URL;
if (!baseURL) {
  server = createServer(async (request, response) => {
    try {
      const pathname = new URL(request.url, 'http://127.0.0.1').pathname;
      const target = path.resolve(
        assetRoot,
        pathname === '/finance/reconciliation'
          ? 'index.html'
          : `.${decodeURIComponent(pathname)}`,
      );
      assert.ok(target.startsWith(`${path.resolve(assetRoot)}${path.sep}`));
      response.setHeader(
        'Content-Type',
        {
          '.html': 'text/html; charset=utf-8',
          '.js': 'application/javascript',
          '.css': 'text/css',
          '.png': 'image/png',
          '.svg': 'image/svg+xml',
          '.ico': 'image/x-icon',
        }[path.extname(target)] || 'application/octet-stream',
      );
      response.end(await readFile(target));
    } catch {
      response.statusCode = 404;
      response.end();
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  baseURL = `http://127.0.0.1:${server.address().port}`;
}
assert.equal(new URL(baseURL).hostname, '127.0.0.1');
const output = process.env.ZTAPI_RECONCILIATION_UI_EVIDENCE_DIR;
assert.ok(
  output && path.isAbsolute(output),
  'An absolute evidence directory is required',
);
await mkdir(output, { recursive: true });
const settlement = {
  id: 21,
  user_id: 8,
  request_id: 'req-20260907-21',
  model: 'zt-example',
  status: 'pending',
  reserved_quota: 1200,
  token_reserved_quota: 900,
  dispatched: true,
  missing_dimensions: '["output_tokens"]',
  created_at: '2026-09-07T01:00:00Z',
  updated_at: '2026-09-07T02:00:00Z',
};
const initialRefund = {
  id: 41,
  source: '供应商对账单',
  proof_id: 'proof-20260907-41',
  request_id: settlement.request_id,
  status: 'pending',
  pending_reason: 'awaiting_approval',
  charge_id: 7,
  ledger_id: 0,
  refunded_quota: 0,
  token_refunded_quota: 0,
  created_at: settlement.created_at,
  updated_at: settlement.updated_at,
  submission: {
    source: '供应商对账单',
    proof_id: 'proof-20260907-41',
    request_id: settlement.request_id,
    user_id: 8,
    attempt: 2,
    channel_id: 5,
    credential_version: 'version-3',
    upstream_request_id: `upstream-${'long-opaque-id-'.repeat(10)}`,
    upstream_task_id: 'task-42',
    upstream_bill_id: 'bill-42',
    mode: 'partial',
    units: [{ dimension: 'output_tokens', units: '25' }],
    evidence_reference: 'statement:20260907:41',
  },
};
const attemptReview = {
  id: 61,
  settlement_id: 22,
  request_id: 'req-settled-parent',
  attempt: 1,
  status: 'pending',
  pending_reason: 'upstream_attempt_billing_unconfirmed',
  applied_proof_id: 0,
  charge_id: 0,
  user_id: 8,
  model: 'zt-example',
  channel_id: 5,
  credential_version: 'version-3',
  upstream_request_id: 'upstream-first',
  created_at: settlement.created_at,
  updated_at: settlement.updated_at,
};
const billedSubmission = {
  source: 'supplier-statement',
  proof_id: 'bill-proof-71',
  request_id: attemptReview.request_id,
  user_id: 8,
  attempt: 1,
  channel_id: 5,
  credential_version: 'version-3',
  upstream_request_id: 'upstream-first',
  upstream_bill_id: 'bill-line-71',
  kind: 'billed',
  usage_semantic: 'openai',
  usage: [
    { dimension: 'input_tokens', quantity: 100 },
    { dimension: 'cache_read', quantity: 20 },
    { dimension: 'output_tokens', quantity: 30 },
  ],
  evidence_reference: 'statement:71',
  distinct_usage_reference: 'verified-distinct-execution-1',
};
const initialProof = {
  id: 71,
  source: billedSubmission.source,
  proof_id: billedSubmission.proof_id,
  request_id: billedSubmission.request_id,
  attempt: 1,
  status: 'pending',
  pending_reason: 'awaiting_approval',
  charge_id: 0,
  submission: billedSubmission,
  created_at: settlement.created_at,
  updated_at: settlement.updated_at,
};
const wholeNoCharge = {
  source: 'supplier-statement',
  proof_id: 'whole-nocharge-21',
  verification_reference: 'all-attempts-verified',
  attempts: [
    {
      attempt: 1,
      channel_id: 5,
      credential_version: 'version-3',
      upstream_request_id: 'upstream-31',
      verification_reference: 'supplier-nocharge-31',
    },
  ],
};
const browser = await chromium.launch({ channel: 'chrome', headless: true });
const results = [];
let activePage;
try {
  for (const width of [1440, 390, 320]) {
    const context = await browser.newContext({
      viewport: { width, height: 1000 },
      serviceWorkers: 'block',
    });
    const page = await context.newPage();
    activePage = page;
    const unexpected = [];
    const errors = [];
    const posts = [];
    let role = 3;
    let refund = structuredClone(initialRefund);
    let proof = structuredClone(initialProof);
    page.on('pageerror', (error) => errors.push(error.message));
    await context.route('**/*', async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.origin !== new URL(baseURL).origin) {
        unexpected.push('external resource');
        return route.abort();
      }
      if (!url.pathname.startsWith('/api/')) return route.continue();
      const respond = (data, status = 200) =>
        route.fulfill({ status, json: { success: status < 400, data } });
      if (url.pathname === '/api/auth/refresh')
        return respond({
          access_token: 'synthetic-local-test',
          expires_in: 3600,
          user: { id: 7, role, username: '本地财务验收' },
        });
      if (url.pathname === '/api/admin/request-settlements') {
        assert.equal(url.search, '?after_id=0&limit=20');
        return respond({ items: [settlement], next_after_id: 21 });
      }
      if (url.pathname === '/api/admin/request-settlements/21')
        return respond({
          ...settlement,
          token_id: 11,
          operation_id: 'operation-21',
          charged_quota: 100,
          additional_charged_quota: 80,
          total_charged_quota: 180,
          refunded_quota: 140,
          net_charged_quota: 40,
          charge_anchors: [
            {
              id: 81,
              attempt: 1,
              channel_id: 5,
              charged_quota: 80,
              refunded_quota: 40,
              billing_proof_id: 71,
              original_ledger_id: 91,
              dimensions: '[]',
            },
            {
              id: 82,
              attempt: 2,
              channel_id: 6,
              charged_quota: 100,
              refunded_quota: 100,
              billing_proof_id: 0,
              original_ledger_id: 92,
              dimensions: '[]',
            },
          ],
          last_ledger_id: 9,
          price_snapshot: '{"key":"PRIVATE_PRICE"}',
          usage: '{"prompt":"PRIVATE_PROMPT"}',
          charge_dimensions: '[]',
          attempts: [
            {
              id: 31,
              settlement_id: 21,
              attempt: 1,
              channel_id: 5,
              credential_version: 'version-3',
              protocol: 'openai',
              upstream_request_id: 'upstream-31',
              http_status: 502,
              api_key: 'PRIVATE_KEY',
            },
          ],
          attempt_evidence: [
            {
              execution_id: 'execution-31',
              final_outcome: { raw: 'PRIVATE_BODY' },
            },
          ],
          attempt_evidence_available: true,
        });
      if (
        url.pathname ===
          '/api/admin/request-settlements/21/confirm-no-charge' &&
        request.method() === 'POST'
      ) {
        assert.deepEqual(request.postDataJSON(), wholeNoCharge);
        posts.push({ endpoint: url.pathname, body: request.postDataJSON() });
        return respond({
          id: 21,
          request_id: settlement.request_id,
          status: 'released',
          ledger_id: 93,
          cache_sync_pending: true,
        });
      }
      if (url.pathname === '/api/admin/supplier-refunds') {
        if (request.method() === 'POST') {
          assert.deepEqual(request.postDataJSON(), initialRefund.submission);
          posts.push({ endpoint: url.pathname, body: request.postDataJSON() });
          return respond(initialRefund);
        }
        assert.equal(url.searchParams.get('after_id'), '0');
        assert.equal(url.searchParams.get('limit'), '20');
        assert.ok(
          ['pending', 'applied', 'completed', 'all'].includes(
            url.searchParams.get('status'),
          ),
        );
        return respond({
          items: [
            url.searchParams.get('status') === 'completed'
              ? {
                  ...refund,
                  status: 'completed',
                  pending_reason: '',
                  ledger_id: 991,
                  refunded_quota: 25,
                }
              : refund,
          ],
          next_after_id: 41,
        });
      }
      if (url.pathname === '/api/admin/attempt-billing/reviews') {
        assert.equal(url.searchParams.get('after_id'), '0');
        assert.equal(url.searchParams.get('limit'), '100');
        assert.ok(
          ['pending', 'verified_billed', 'verified_nocharge', 'all'].includes(
            url.searchParams.get('status'),
          ),
        );
        return respond({
          items: [
            {
              ...attemptReview,
              status:
                url.searchParams.get('status') === 'all'
                  ? 'pending'
                  : url.searchParams.get('status'),
            },
          ],
          next_after_id: 61,
        });
      }
      if (url.pathname === '/api/admin/attempt-billing/proofs') {
        if (request.method() === 'POST') {
          assert.deepEqual(request.postDataJSON(), billedSubmission);
          posts.push({ endpoint: url.pathname, body: request.postDataJSON() });
          return respond(proof);
        }
        assert.equal(url.searchParams.get('after_id'), '0');
        assert.equal(url.searchParams.get('limit'), '100');
        assert.ok(
          ['pending', 'approved', 'applied', 'completed', 'all'].includes(
            url.searchParams.get('status'),
          ),
        );
        return respond({
          items:
            url.searchParams.get('status') === 'all' ||
            url.searchParams.get('status') === proof.status
              ? [proof]
              : [],
          next_after_id: 71,
        });
      }
      if (
        url.pathname === '/api/admin/attempt-billing/proofs/71/approve' &&
        request.method() === 'POST'
      ) {
        assert.deepEqual(request.postDataJSON(), {
          verification_reference: 'verification:71',
        });
        posts.push({ endpoint: url.pathname, body: request.postDataJSON() });
        proof = {
          ...proof,
          status: 'approved',
          pending_reason: 'billing_application_pending',
        };
        return respond(proof, 202);
      }
      if (
        url.pathname === '/api/admin/supplier-refunds/41/approve' &&
        request.method() === 'POST'
      ) {
        const body = request.postDataJSON();
        assert.deepEqual(body, { verification_reference: 'review:41' });
        posts.push(body);
        refund = { ...refund, pending_reason: 'original_charge_unsettled' };
        return respond(refund, 202);
      }
      unexpected.push(`${request.method()} ${url.pathname}`);
      return respond({}, 404);
    });

    const assertFit = async () => {
      assert.equal(
        await page.locator('.ztapi-reconciliation').evaluate((root) => {
          const box = root.getBoundingClientRect();
          const unframed = [
            ...root.querySelectorAll(
              'h1,h2,h3,dt,dd,textarea,label,input,select,[role="tab"]',
            ),
          ];
          return (
            box.left >= -1 &&
            box.right <= innerWidth + 1 &&
            root.scrollWidth <= root.clientWidth + 1 &&
            unframed.every((node) => {
              if (node.closest('[hidden]')) return true;
              const bounds = node.getBoundingClientRect();
              if (node.closest('[role="dialog"]'))
                return bounds.left >= -1 && bounds.right <= innerWidth + 1;
              return (
                bounds.left >= box.left - 1 && bounds.right <= box.right + 1
              );
            })
          );
        }),
        true,
        `Page overflow at ${width}px`,
      );
    };
    await page.goto(`${baseURL}/finance/reconciliation`);
    await expect(page.getByRole('heading', { name: '财务对账' })).toBeVisible();
    await expect(
      page.getByText(settlement.request_id, { exact: true }),
    ).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-queue.png`),
      fullPage: true,
    });
    if (width < 700) {
      assert.equal(
        await page
          .getByRole('region', { name: '待结算请求表格' })
          .evaluate(
            (node) =>
              node.scrollWidth > node.clientWidth &&
              getComputedStyle(node).overflowX === 'auto',
          ),
        true,
      );
    }
    await page
      .getByRole('button', { name: `查看结算 ${settlement.request_id}` })
      .click();
    const detail = page.getByRole('region', {
      name: '待结算详情',
      exact: true,
    });
    await expect(
      detail.getByText('upstream-31', { exact: true }),
    ).toBeVisible();
    assert.doesNotMatch(await detail.innerText(), /PRIVATE_/);
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-settlement.png`),
      fullPage: true,
    });
    await detail.getByRole('button', { name: '核验整单未收费' }).click();
    const noChargeDialog = page.getByRole('dialog', { name: '核验整单未收费' });
    await expect(
      noChargeDialog.getByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toHaveCount(0);
    await noChargeDialog
      .getByLabel('凭证来源', { exact: true })
      .fill(wholeNoCharge.source);
    await noChargeDialog
      .getByLabel('凭证编号', { exact: true })
      .fill(wholeNoCharge.proof_id);
    await noChargeDialog
      .getByLabel('整单核验引用', { exact: true })
      .fill(wholeNoCharge.verification_reference);
    await noChargeDialog
      .getByLabel('尝试 1 核验引用', { exact: true })
      .fill(wholeNoCharge.attempts[0].verification_reference);
    await expect(
      noChargeDialog.getByRole('button', { name: '确认未收费并提交核验' }),
    ).toBeDisabled();
    assert.equal(posts.length, 0);
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-whole-nocharge.png`),
      fullPage: true,
    });
    await noChargeDialog
      .getByRole('checkbox', { name: /全部尝试均未收费/ })
      .check();
    await noChargeDialog
      .getByRole('button', { name: '确认未收费并提交核验' })
      .click();
    await expect(
      page.getByText('整单未收费核验已完成，预留已释放；账务同步仍在进行。'),
    ).toBeVisible();
    await expect(
      detail.getByRole('button', { name: '核验整单未收费' }),
    ).toHaveCount(0);
    await page.getByRole('tab', { name: '供应商退款' }).click();
    await page
      .getByRole('button', { name: `查看退款 ${initialRefund.proof_id}` })
      .click();
    await expect(
      page.getByText('statement:20260907:41', { exact: true }),
    ).toBeVisible();
    assert.equal(
      await page
        .getByRole('region', { name: '申报退款用量表格' })
        .evaluate((node) => node.scrollWidth <= node.clientWidth + 1),
      true,
      `Two-column refund quantities should fit at ${width}px`,
    );
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-review.png`),
      fullPage: true,
    });
    const submit = page.getByRole('button', { name: '确认核验并批准' });
    await expect(submit).toBeDisabled();
    await page.getByLabel('核验记录编号').fill('review:41');
    await expect(submit).toBeDisabled();
    await page.getByRole('checkbox', { name: /我已核验供应商凭证/ }).check();
    await submit.click();
    await expect(
      page.getByText('核验审批已受理，退款仍待对账，尚未入账。', {
        exact: true,
      }),
    ).toBeVisible();
    await expect(submit).toHaveCount(0);
    assert.equal(posts.length, 2);
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-pending.png`),
      fullPage: true,
    });
    await page.getByRole('button', { name: '登记退款凭证' }).click();
    let dialog = page.getByRole('dialog', { name: '登记退款凭证' });
    await expect(
      dialog.getByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toHaveCount(0);
    for (const [label, key] of [
      ['凭证来源', 'source'],
      ['凭证编号', 'proof_id'],
      ['证据引用', 'evidence_reference'],
      ['原请求 ID', 'request_id'],
      ['客户 ID', 'user_id'],
      ['渠道 ID', 'channel_id'],
      ['凭据版本', 'credential_version'],
      ['上游请求 ID', 'upstream_request_id'],
      ['上游任务 ID', 'upstream_task_id'],
      ['上游账单行编号', 'upstream_bill_id'],
    ])
      await dialog
        .getByLabel(label, { exact: true })
        .fill(String(initialRefund.submission[key]));
    await dialog
      .getByRole('combobox', { name: '尝试序号', exact: true })
      .selectOption('2');
    await dialog
      .getByRole('combobox', { name: '退款范围', exact: true })
      .selectOption('partial');
    await dialog.getByRole('button', { name: '添加用量' }).click();
    await dialog
      .getByRole('combobox', { name: '计费项 1', exact: true })
      .selectOption('output_tokens');
    await dialog.getByLabel('退款用量 1', { exact: true }).fill('25');
    await expect(
      dialog.getByRole('button', { name: '提交待审凭证' }),
    ).toBeDisabled();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-refund-submit.png`),
      fullPage: true,
    });
    await dialog.getByRole('checkbox').check();
    await dialog.getByRole('button', { name: '提交待审凭证' }).click();
    await expect(
      page.getByText('退款凭证已登记，等待财务审核；未执行退款。'),
    ).toBeVisible();
    await page.getByRole('tab', { name: '尝试对账', exact: true }).click();
    await expect(
      page.getByText('req-settled-parent', { exact: true }),
    ).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-attempts.png`),
      fullPage: true,
    });
    await page
      .getByRole('button', { name: '提交凭证 req-settled-parent 第1次' })
      .click();
    dialog = page.getByRole('dialog', { name: '提交尝试凭证' });
    await expect(
      dialog.getByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toHaveCount(0);
    await dialog
      .getByLabel('凭证来源', { exact: true })
      .fill(billedSubmission.source);
    await dialog
      .getByLabel('凭证编号', { exact: true })
      .fill(billedSubmission.proof_id);
    await dialog
      .getByLabel('证据引用', { exact: true })
      .fill(billedSubmission.evidence_reference);
    await dialog
      .getByRole('combobox', { name: '凭证类型', exact: true })
      .selectOption('nocharge');
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-attempt-nocharge.png`),
      fullPage: true,
    });
    await dialog
      .getByRole('combobox', { name: '凭证类型', exact: true })
      .selectOption('billed');
    await dialog
      .getByRole('combobox', { name: '用量口径', exact: true })
      .selectOption(billedSubmission.usage_semantic);
    await dialog
      .getByLabel('上游账单行编号', { exact: true })
      .fill(billedSubmission.upstream_bill_id);
    await dialog
      .getByLabel('独立用量核验引用', { exact: true })
      .fill(billedSubmission.distinct_usage_reference);
    for (const [index, line] of billedSubmission.usage.entries()) {
      await dialog.getByRole('button', { name: '添加用量' }).click();
      await dialog
        .getByRole('combobox', { name: `计费项 ${index + 1}`, exact: true })
        .selectOption(line.dimension);
      await dialog
        .getByLabel(`用量 ${index + 1}`, { exact: true })
        .fill(String(line.quantity));
    }
    await expect(
      dialog.getByRole('button', { name: '提交待审凭证' }),
    ).toBeDisabled();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-attempt-submit.png`),
      fullPage: true,
    });
    await dialog.getByRole('checkbox').check();
    await dialog.getByRole('button', { name: '提交待审凭证' }).click();
    await expect(
      page.getByText('凭证已登记，等待财务审核；未执行扣费或释放预留。'),
    ).toBeVisible();
    await page.getByRole('tab', { name: '待审凭证' }).click();
    await page.getByRole('button', { name: '查看凭证 bill-proof-71' }).click();
    const proofDetail = page.getByRole('region', { name: '尝试凭证审核' });
    await expect(
      proofDetail.getByText('verified-distinct-execution-1'),
    ).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-proof-review.png`),
      fullPage: true,
    });
    await proofDetail.getByLabel('核验记录编号').fill('verification:71');
    await expect(
      proofDetail.getByRole('button', { name: '确认批准凭证' }),
    ).toBeDisabled();
    await proofDetail.getByRole('checkbox').check();
    await proofDetail.getByRole('button', { name: '确认批准凭证' }).click();
    await expect(
      proofDetail.getByText('凭证审批已受理，仍待对账，未确认账务处理完成。'),
    ).toBeVisible();
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-proof-pending.png`),
      fullPage: true,
    });
    assert.equal(posts.length, 5);
    await page
      .getByRole('combobox', { name: '对账状态', exact: true })
      .selectOption('approved');
    await page.getByRole('button', { name: '刷新尝试对账' }).click();
    await page.getByRole('button', { name: '查看凭证 bill-proof-71' }).click();
    await expect(
      proofDetail.getByText('核验已通过，待入账', { exact: true }),
    ).toBeVisible();
    await expect(
      proofDetail.getByRole('button', { name: '确认批准凭证' }),
    ).toHaveCount(0);
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-approved-history.png`),
      fullPage: true,
    });
    await page.getByRole('tab', { name: '供应商退款' }).click();
    await page
      .getByRole('combobox', { name: '退款状态', exact: true })
      .selectOption('completed');
    await page
      .getByRole('button', { name: `查看退款 ${initialRefund.proof_id}` })
      .click();
    await expect(
      page
        .getByRole('region', { name: '退款审核详情' })
        .getByText('991', { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole('button', { name: '确认核验并批准' }),
    ).toHaveCount(0);
    await assertFit();
    await page.screenshot({
      path: path.join(output, `reconciliation-${width}-refund-history.png`),
      fullPage: true,
    });
    role = 2;
    await page.reload();
    await expect(page.getByRole('alert')).toHaveText('无权访问此页面');
    assert.deepEqual(unexpected, []);
    assert.deepEqual(errors, []);
    results.push({
      width,
      result: 'PASS',
      synthetic_write_posts: posts.length,
      unexpected_requests: unexpected.length,
      page_errors: errors.length,
    });
    await context.close();
  }
  await writeFile(
    path.join(output, 'reconciliation-browser.json'),
    JSON.stringify(
      {
        result: 'PASS',
        api: 'synthetic fixtures only',
        production_operations: false,
        normal_workflows_require_json: false,
        asset_root: assetRoot,
        base_url: baseURL,
        results,
      },
      null,
      2,
    ),
  );
  console.log(JSON.stringify(results, null, 2));
} catch (error) {
  if (activePage && !activePage.isClosed()) {
    await activePage.screenshot({
      path: path.join(output, 'failure.png'),
      fullPage: true,
    });
    await writeFile(
      path.join(output, 'failure-accessibility.txt'),
      await activePage.locator('body').ariaSnapshot(),
    );
  }
  throw error;
} finally {
  await browser.close();
  if (server) await new Promise((resolve) => server.close(resolve));
}
