import React from 'react';
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import AdminApp from '../../AdminApp.jsx';
import ReconciliationPage from './ReconciliationPage.jsx';
import {
  clearAdminSession,
  setAdminSession,
} from '../../auth/admin-session.js';

const settlement = {
  id: 21,
  request_id: 'req-original-21',
  model: 'zt-example',
  status: 'pending',
  reserved_quota: 1200,
  user_id: 8,
  dispatched: true,
  token_reserved_quota: 900,
  missing_dimensions: '["output_tokens"]',
  created_at: '2026-09-07T01:00:00Z',
  updated_at: '2026-09-07T02:00:00Z',
};
const refund = {
  id: 41,
  source: 'supplier-statement',
  proof_id: 'proof-41',
  request_id: 'req-original-21',
  status: 'pending',
  pending_reason: 'awaiting_approval',
  charge_id: 7,
  ledger_id: 0,
  refunded_quota: 0,
  token_refunded_quota: 0,
  created_at: '2026-09-07T02:00:00Z',
  updated_at: '2026-09-07T02:00:00Z',
  submission: {
    source: 'supplier-statement',
    proof_id: 'proof-41',
    request_id: 'req-original-21',
    user_id: 8,
    attempt: 2,
    channel_id: 5,
    credential_version: 'version-3',
    upstream_request_id: 'upstream-42',
    upstream_task_id: 'task-42',
    upstream_bill_id: 'bill-42',
    mode: 'partial',
    units: [{ dimension: 'output_tokens', units: '25' }],
    evidence_reference: 'statement:2026-09-07:41',
  },
};
const list = (items, next = items.at(-1)?.id || 0) => ({
  items,
  next_after_id: next,
});
const reply = (data, status = 200) =>
  new Response(JSON.stringify({ success: status < 400, data }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
function deferred() {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function mount(role = 3) {
  window.history.replaceState({}, '', '/finance/reconciliation');
  setAdminSession({
    access_token: 'synthetic-test-only',
    expires_in: 3600,
    user: { id: 7, username: 'finance', role },
  });
  return render(<AdminApp />);
}
let handler;
let requests;
beforeEach(() => {
  requests = [];
  handler = (url) =>
    reply(list(url.includes('supplier-refunds') ? [refund] : [settlement]));
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url, init) => {
      requests.push({ url, ...init });
      return handler(url, init);
    }),
  );
});
afterEach(() => {
  cleanup();
  clearAdminSession();
  vi.unstubAllGlobals();
});

async function openRefund() {
  fireEvent.click(await screen.findByRole('tab', { name: '供应商退款' }));
  fireEvent.click(
    await screen.findByRole('button', { name: '查看退款 proof-41' }),
  );
  return screen.getByRole('region', { name: '退款审核详情' });
}
function confirmReview() {
  fireEvent.change(screen.getByLabelText('核验记录编号'), {
    target: { value: '  review:41  ' },
  });
  fireEvent.click(screen.getByRole('checkbox', { name: /已核验供应商凭证/ }));
}

describe('ReconciliationPage routing and queues', () => {
  it.each([
    [
      {
        charged_quota: 100,
        additional_charged_quota: 80,
        total_charged_quota: 180,
        refunded_quota: 140,
        net_charged_quota: 40,
      },
      ['100', '80', '180', '140', '40'],
    ],
    [
      { charged_quota: 100, refunded_quota: 40 },
      ['100', '0', '100', '40', '60'],
    ],
    [
      {
        charged_quota: 0,
        additional_charged_quota: 0,
        total_charged_quota: 0,
        refunded_quota: 0,
        net_charged_quota: 0,
      },
      ['0', '0', '0', '0', '0'],
    ],
  ])(
    'shows aggregate request charges and refunds with legacy fallback: %j',
    async (amounts, expected) => {
      handler = (url) =>
        reply(
          url.endsWith('/21')
            ? {
                ...settlement,
                ...amounts,
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
                ],
              }
            : list([settlement]),
        );
      mount();
      fireEvent.click(
        await screen.findByRole('button', { name: '查看结算 req-original-21' }),
      );
      await screen.findByText('累计扣费（原始单位）');
      for (const [index, label] of [
        '原始扣费（原始单位）',
        '追加扣费（原始单位）',
        '累计扣费（原始单位）',
        '累计退款（原始单位）',
        '净扣费（原始单位）',
      ].entries()) {
        expect(screen.getByText(label).nextElementSibling).toHaveTextContent(
          new RegExp(`^${expected[index]}$`),
        );
      }
      const charges = screen.getByRole('region', { name: '尝试扣费记录表格' });
      expect(within(charges).getByText('81')).toBeVisible();
      expect(within(charges).getByText('71')).toBeVisible();
      expect(within(charges).getByText('91')).toBeVisible();
      expect(
        requests.every(
          (request) => !request.method || request.method === 'GET',
        ),
      ).toBe(true);
    },
  );
  it('mounts the real finance route and navigation with bounded read-only settlement data', async () => {
    mount();
    expect(
      await screen.findByRole('heading', { name: '财务对账' }),
    ).toBeVisible();
    expect(screen.getByRole('link', { name: '财务对账' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    expect(await screen.findByText('req-original-21')).toBeVisible();
    expect(requests[0].url).toBe(
      '/api/admin/request-settlements?after_id=0&limit=20',
    );
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
    expect(screen.queryByRole('spinbutton')).toBeNull();
    expect(requests.every((r) => !r.method || r.method === 'GET')).toBe(true);
  });

  it('denies support staff the route and never fetches finance data', () => {
    mount(2);
    expect(screen.getByRole('alert')).toHaveTextContent('无权访问此页面');
    expect(screen.queryByRole('link', { name: '财务对账' })).toBeNull();
    expect(requests).toHaveLength(0);
  });

  it('advances and returns using server cursors and resets them when switching tabs', async () => {
    handler = (url) =>
      reply(
        url.includes('supplier-refunds')
          ? list([refund])
          : url.includes('after_id=40')
            ? list([], 40)
            : list(
                Array.from({ length: 20 }, (_, i) => ({
                  ...settlement,
                  id: i + 21,
                  request_id: `req-${i + 21}`,
                })),
                40,
              ),
      );
    mount();
    await screen.findByText('req-21');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('暂无待结算请求。');
    expect(requests.at(-1).url).toBe(
      '/api/admin/request-settlements?after_id=40&limit=20',
    );
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await screen.findByText('req-21');
    expect(requests.at(-1).url).toBe(
      '/api/admin/request-settlements?after_id=0&limit=20',
    );
    fireEvent.click(screen.getByRole('tab', { name: '供应商退款' }));
    await screen.findByText('proof-41');
    expect(requests.at(-1).url).toBe(
      '/api/admin/supplier-refunds?status=pending&after_id=0&limit=20',
    );
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
  });

  it('does not advance with a non-increasing server cursor', async () => {
    handler = () =>
      reply(
        list(
          Array.from({ length: 20 }, (_, i) => ({
            ...settlement,
            id: i + 21,
            request_id: `req-${i}`,
          })),
          0,
        ),
      );
    mount();
    await screen.findByText('req-0');
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
  });

  it('shows loading, generic safe errors and refresh recovery without rendering raw errors', async () => {
    const pending = deferred();
    handler = () => pending.promise;
    mount();
    expect(screen.getByRole('status')).toHaveTextContent('正在加载');
    await act(async () =>
      pending.resolve(reply({ message: 'PRIVATE_ERROR' }, 503)),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '对账记录加载失败',
    );
    expect(screen.queryByText(/PRIVATE_ERROR/)).toBeNull();
    handler = () => reply(list([]));
    fireEvent.click(screen.getByRole('button', { name: '刷新对账记录' }));
    expect(await screen.findByText('暂无待结算请求。')).toBeVisible();
  });

  it('ignores a late list response after switching tabs', async () => {
    const older = deferred();
    handler = (url) =>
      url.includes('supplier-refunds') ? reply(list([refund])) : older.promise;
    mount();
    fireEvent.click(await screen.findByRole('tab', { name: '供应商退款' }));
    await screen.findByText('proof-41');
    await act(async () => older.resolve(reply(list([settlement]))));
    expect(screen.getByText('proof-41')).toBeVisible();
    expect(
      screen.queryByRole('button', { name: '查看结算 req-original-21' }),
    ).toBeNull();
  });

  it('loads original settlement identity and attempt IDs without raw evidence or secrets', async () => {
    handler = (url) =>
      reply(
        url.endsWith('/21')
          ? {
              ...settlement,
              token_id: 11,
              operation_id: 'operation-21',
              charged_quota: 0,
              refunded_quota: 0,
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
            }
          : list([settlement]),
      );
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    const detail = await screen.findByRole('region', { name: '待结算详情' });
    expect(await within(detail).findByText('operation-21')).toBeVisible();
    expect(within(detail).getByText('upstream-31')).toBeVisible();
    expect(within(detail).getByText('输出文本量')).toBeVisible();
    expect(within(detail).getByText('1200')).toBeVisible();
    expect(detail).not.toHaveTextContent(/PRIVATE_/);
    expect(requests.at(-1).url).toBe('/api/admin/request-settlements/21');
  });

  it('shows detail errors and retries only the selected record', async () => {
    handler = (url) =>
      url.endsWith('/21') ? reply({}, 503) : reply(list([settlement]));
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '结算详情加载失败',
    );
    handler = () => reply({ ...settlement, attempts: [] });
    fireEvent.click(screen.getByRole('button', { name: '重新加载详情' }));
    expect(await screen.findByText('暂无请求尝试记录。')).toBeVisible();
  });

  it('discards a late detail response after selecting another request', async () => {
    const older = deferred();
    handler = (url) =>
      url.endsWith('/21')
        ? older.promise
        : reply(
            url.endsWith('/22')
              ? { ...settlement, id: 22, request_id: 'req-22', attempts: [] }
              : list([
                  settlement,
                  { ...settlement, id: 22, request_id: 'req-22' },
                ]),
          );
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    fireEvent.click(screen.getByRole('button', { name: '查看结算 req-22' }));
    const detail = screen.getByRole('region', { name: '待结算详情' });
    await within(detail).findByText('req-22');
    await act(async () =>
      older.resolve(reply({ ...settlement, attempts: [] })),
    );
    expect(within(detail).queryByText('req-original-21')).toBeNull();
    expect(within(detail).getByText('req-22')).toBeVisible();
  });

  it('does not misrepresent a malformed queue response as empty', async () => {
    handler = () => reply({ items: null, next_after_id: 0 });
    mount();
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '对账记录加载失败',
    );
    expect(screen.queryByText('暂无待结算请求。')).toBeNull();
  });

  it('paginates refunds and refreshes the current cursor without accumulating rows', async () => {
    handler = (url) =>
      reply(
        !url.includes('supplier-refunds')
          ? list([])
          : url.includes('after_id=60')
            ? list([{ ...refund, id: 61, proof_id: 'proof-61' }], 61)
            : list(
                Array.from({ length: 20 }, (_, i) => ({
                  ...refund,
                  id: i + 41,
                  proof_id: `proof-${i + 41}`,
                })),
                60,
              ),
      );
    mount();
    fireEvent.click(await screen.findByRole('tab', { name: '供应商退款' }));
    await screen.findByText('proof-41');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('proof-61');
    expect(screen.queryByText('proof-41')).toBeNull();
    expect(requests.at(-1).url).toBe(
      '/api/admin/supplier-refunds?status=pending&after_id=60&limit=20',
    );
    fireEvent.click(screen.getByRole('button', { name: '刷新对账记录' }));
    await screen.findByText('proof-61');
    expect(requests.at(-1).url).toBe(
      '/api/admin/supplier-refunds?status=pending&after_id=60&limit=20',
    );
  });

  it('supports keyboard tab selection and restores focus when closing details', async () => {
    mount();
    fireEvent.keyDown(screen.getByRole('tab', { name: '待结算请求' }), {
      key: 'ArrowRight',
    });
    expect(screen.getByRole('tab', { name: '供应商退款' })).toHaveFocus();
    const opener = await screen.findByRole('button', {
      name: '查看退款 proof-41',
    });
    fireEvent.click(opener);
    expect(screen.getByRole('heading', { name: '退款审核详情' })).toHaveFocus();
    fireEvent.click(screen.getByRole('button', { name: '关闭详情' }));
    expect(opener).toHaveFocus();
  });
});

describe('supplier refund approval', () => {
  it('requires evidence and explicit confirmation, posts no money, and keeps pending distinct from credited', async () => {
    const mutation = deferred();
    handler = (url, init) =>
      init.method === 'POST'
        ? mutation.promise
        : reply(
            list(url.includes('supplier-refunds') ? [refund] : [settlement]),
          );
    mount();
    const detail = await openRefund();
    expect(within(detail).getByText('statement:2026-09-07:41')).toBeVisible();
    expect(within(detail).getByText('upstream-42')).toBeVisible();
    expect(within(detail).getByText('部分用量退款')).toBeVisible();
    expect(screen.queryByRole('spinbutton')).toBeNull();
    const submit = screen.getByRole('button', { name: '确认核验并批准' });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText('核验记录编号'), {
      target: { value: 'review:41' },
    });
    expect(submit).toBeDisabled();
    confirmReview();
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() =>
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1),
    );
    const post = requests.find((r) => r.method === 'POST');
    expect(post.url).toBe('/api/admin/supplier-refunds/41/approve');
    expect(JSON.parse(post.body)).toEqual({
      verification_reference: 'review:41',
    });
    expect(screen.getByRole('button', { name: '正在提交...' })).toBeDisabled();
    await act(async () =>
      mutation.resolve(
        reply({ ...refund, pending_reason: 'original_charge_unsettled' }, 202),
      ),
    );
    expect(
      await screen.findByText('核验审批已受理，退款仍待对账，尚未入账。'),
    ).toBeVisible();
    expect(screen.getAllByText('原请求尚未结算').length).toBeGreaterThan(0);
    expect(screen.queryByRole('button', { name: '确认核验并批准' })).toBeNull();
    expect(screen.queryByText('退款已入账。')).toBeNull();
  });

  it.each([
    ['applied', '退款已入账，缓存同步仍在进行。'],
    ['completed', '退款处理已完成。'],
    ['unexpected', '审批结果状态未知，请刷新核对，勿重复提交。'],
  ])(
    'reports returned %s state without inventing external payment',
    async (status, message) => {
      handler = (url, init) =>
        reply(
          init.method === 'POST'
            ? {
                ...refund,
                status,
                pending_reason: '',
                refunded_quota: 25,
                ledger_id: 99,
              }
            : list(url.includes('supplier-refunds') ? [refund] : [settlement]),
        );
      mount();
      await openRefund();
      confirmReview();
      fireEvent.click(screen.getByRole('button', { name: '确认核验并批准' }));
      expect(await screen.findByText(message)).toBeVisible();
      expect(
        screen.queryByRole('button', { name: '确认核验并批准' }),
      ).toBeNull();
    },
  );

  it.each([403, 409, 503])(
    'keeps verification input on HTTP %s and does not automatically retry',
    async (status) => {
      handler = (url, init) =>
        init.method === 'POST'
          ? reply({}, status)
          : reply(
              list(url.includes('supplier-refunds') ? [refund] : [settlement]),
            );
      mount();
      await openRefund();
      confirmReview();
      fireEvent.click(screen.getByRole('button', { name: '确认核验并批准' }));
      expect(await screen.findByRole('alert')).toHaveTextContent(
        /权限|冲突|未确认/,
      );
      expect(screen.getByLabelText('核验记录编号')).toHaveValue(
        '  review:41  ',
      );
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
      expect(
        screen.getByRole('button', { name: '确认核验并批准' }),
      ).toBeDisabled();
    },
  );

  it('enforces the backend UTF-8 byte bound for Chinese verification references', async () => {
    mount();
    await openRefund();
    confirmReview();
    fireEvent.change(screen.getByLabelText('核验记录编号'), {
      target: { value: '核'.repeat(171) },
    });
    expect(
      screen.getByRole('button', { name: '确认核验并批准' }),
    ).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('核验记录编号过长');
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it('defaults to read-only review without hiding refund evidence', async () => {
    render(<ReconciliationPage />);
    const detail = await openRefund();
    expect(within(detail).getByText('statement:2026-09-07:41')).toBeVisible();
    expect(within(detail).getByText('只读权限')).toBeVisible();
    expect(screen.queryByLabelText('核验记录编号')).toBeNull();
    expect(screen.queryByRole('button', { name: '确认核验并批准' })).toBeNull();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it('removes approval when financeWrite is revoked while review is open', async () => {
    const view = render(<ReconciliationPage canWrite />);
    await openRefund();
    confirmReview();
    view.rerender(<ReconciliationPage canWrite={false} />);
    expect(screen.queryByRole('button', { name: '确认核验并批准' })).toBeNull();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });
});

const noChargeAttempts = [1, 2].map((attempt) => ({
  id: 30 + attempt,
  attempt,
  channel_id: 4 + attempt,
  credential_version: `version-${attempt}`,
  upstream_request_id: `wire-${attempt}`,
  verification_reference: `verified-wire-${attempt}`,
}));
const wholeProof = {
  source: 'supplier-statement',
  proof_id: 'whole-nocharge-21',
  verification_reference: 'verified-all-21',
  attempts: noChargeAttempts.map(({ id, ...proof }) => proof),
};

describe('whole-request no-charge verification', () => {
  beforeEach(() => {
    handler = (url) =>
      reply(
        url.endsWith('/21')
          ? { ...settlement, attempts: noChargeAttempts }
          : list([settlement]),
      );
  });
  async function openWhole() {
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    fireEvent.click(
      await screen.findByRole('button', { name: '核验整单未收费' }),
    );
    return screen.getByRole('dialog', { name: '核验整单未收费' });
  }
  it.each([
    [false, 'pending'],
    [true, 'settled'],
    [true, 'released'],
  ])('hides release for write=%s and parent=%s', async (canWrite, status) => {
    handler = (url) =>
      reply(
        url.endsWith('/21')
          ? { ...settlement, status, attempts: noChargeAttempts }
          : list([settlement]),
      );
    render(<ReconciliationPage canWrite={canWrite} />);
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    await screen.findByText('wire-1');
    expect(screen.queryByRole('button', { name: '核验整单未收费' })).toBeNull();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });
  it.each([
    { ...wholeProof, attempts: wholeProof.attempts.slice(0, 1) },
    {
      ...wholeProof,
      attempts: [wholeProof.attempts[0], wholeProof.attempts[0]],
    },
    {
      ...wholeProof,
      attempts: wholeProof.attempts.map((a) => ({ ...a, channel_id: 99 })),
    },
    {
      ...wholeProof,
      attempts: wholeProof.attempts.map((a) => ({
        ...a,
        verification_reference: '',
      })),
    },
    { ...wholeProof, amount: 100 },
    {
      ...wholeProof,
      attempts: wholeProof.attempts.map((a) => ({ ...a, price: 100 })),
    },
  ])(
    'rejects missing, mismatched or money-bearing all-attempt evidence: %j',
    async (evidence) => {
      render(<ReconciliationPage canWrite />);
      const dialog = await openWhole();
      fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
        target: { value: JSON.stringify(evidence) },
      });
      fireEvent.click(within(dialog).getByRole('checkbox'));
      expect(within(dialog).getByRole('alert')).toBeVisible();
      expect(
        within(dialog).getByRole('button', { name: '确认未收费并提交核验' }),
      ).toBeDisabled();
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    },
  );
  it('requires all-attempt confirmation, locks duplicate writes and reports cache synchronization separately', async () => {
    const pending = deferred();
    handler = (url, init) =>
      init.method === 'POST'
        ? pending.promise
        : reply(
            url.endsWith('/21')
              ? { ...settlement, attempts: noChargeAttempts }
              : list([settlement]),
          );
    render(<ReconciliationPage canWrite />);
    const dialog = await openWhole();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
      target: { value: JSON.stringify(wholeProof) },
    });
    const submit = within(dialog).getByRole('button', {
      name: '确认未收费并提交核验',
    });
    expect(submit).toBeDisabled();
    fireEvent.click(
      within(dialog).getByRole('checkbox', { name: /全部尝试均未收费/ }),
    );
    fireEvent.click(submit);
    fireEvent.submit(submit.closest('form'));
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
    expect(requests.at(-1).url).toBe(
      '/api/admin/request-settlements/21/confirm-no-charge',
    );
    expect(JSON.parse(requests.at(-1).body)).toEqual(wholeProof);
    await act(async () =>
      pending.resolve(
        reply({
          id: 21,
          request_id: settlement.request_id,
          status: 'released',
          ledger_id: 91,
          cache_sync_pending: true,
        }),
      ),
    );
    await screen.findByText(
      '整单未收费核验已完成，预留已释放；账务同步仍在进行。',
    );
    expect(screen.queryByRole('button', { name: '核验整单未收费' })).toBeNull();
  });
});

const attemptReview = {
  id: 61,
  settlement_id: 21,
  request_id: 'req-settled-parent',
  attempt: 1,
  status: 'pending',
  pending_reason: 'awaiting_proof',
  applied_proof_id: 0,
  charge_id: 0,
  created_at: settlement.created_at,
  updated_at: settlement.updated_at,
  user_id: 8,
  model: 'zt-example',
  channel_id: 5,
  credential_version: 'version-3',
  upstream_request_id: 'upstream-first',
};
const attemptSubmission = {
  source: 'supplier-statement',
  proof_id: 'nocharge-71',
  request_id: 'req-settled-parent',
  user_id: 8,
  attempt: 1,
  channel_id: 5,
  credential_version: 'version-3',
  upstream_request_id: 'upstream-first',
  upstream_bill_id: '',
  kind: 'nocharge',
  usage_semantic: '',
  usage: [],
  evidence_reference: 'statement:71',
  distinct_usage_reference: '',
};
const attemptProof = {
  id: 71,
  source: attemptSubmission.source,
  proof_id: attemptSubmission.proof_id,
  request_id: attemptSubmission.request_id,
  attempt: 1,
  status: 'pending',
  pending_reason: 'awaiting_approval',
  charge_id: 0,
  submission: attemptSubmission,
  created_at: settlement.created_at,
  updated_at: settlement.updated_at,
};
function attemptHandler(url) {
  return reply(
    list(
      url.includes('/reviews?')
        ? [attemptReview]
        : url.includes('/attempt-billing/proofs?')
          ? [attemptProof]
          : [],
    ),
  );
}
async function openAttempts() {
  fireEvent.click(
    await screen.findByRole('tab', { name: '尝试对账', exact: true }),
  );
  await screen.findByText('req-settled-parent');
}
async function openAttemptProof() {
  await openAttempts();
  fireEvent.click(screen.getByRole('tab', { name: '待审凭证', exact: true }));
  fireEvent.click(
    await screen.findByRole('button', { name: '查看凭证 nocharge-71' }),
  );
  return screen.getByRole('region', { name: '尝试凭证审核' });
}

describe('reconciliation status filters', () => {
  it('filters verified attempt reviews and resets a nonzero cursor without exposing submission again', async () => {
    handler = (url) => {
      if (!url.includes('/reviews?')) return reply(list([]));
      const params = new URL(url, 'http://local').searchParams;
      if (params.get('status') !== 'pending')
        return reply(
          list([
            { ...attemptReview, status: 'verified_billed', pending_reason: '' },
          ]),
        );
      return reply(
        params.get('after_id') === '0'
          ? list(
              Array.from({ length: 100 }, (_, i) => ({
                ...attemptReview,
                id: i + 61,
                request_id: `request-${i}`,
              })),
              160,
            )
          : list([attemptReview]),
      );
    };
    render(<ReconciliationPage canWrite />);
    fireEvent.click(screen.getByRole('tab', { name: '尝试对账', exact: true }));
    await screen.findByText('request-99');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('req-settled-parent');
    const filter = screen.getByRole('combobox', { name: '对账状态' });
    expect(
      within(filter)
        .getAllByRole('option')
        .map((option) => option.value),
    ).toEqual(['pending', 'verified_billed', 'verified_nocharge', 'all']);
    fireEvent.change(filter, { target: { value: 'verified_billed' } });
    await waitFor(() =>
      expect(requests.at(-1).url).toBe(
        '/api/admin/attempt-billing/reviews?status=verified_billed&after_id=0&limit=100',
      ),
    );
    await screen.findByText('req-settled-parent');
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: /提交凭证/ })).toBeNull();
  });

  it('keeps approved proofs discoverable across refresh and exposes only supported proof statuses', async () => {
    handler = (url) =>
      url.includes('/proofs?')
        ? reply(
            list([
              {
                ...attemptProof,
                status: new URL(url, 'http://local').searchParams.get('status'),
                pending_reason: 'billing_application_pending',
              },
            ]),
          )
        : attemptHandler(url);
    render(<ReconciliationPage canWrite />);
    await openAttempts();
    fireEvent.click(screen.getByRole('tab', { name: '待审凭证' }));
    await screen.findByText('nocharge-71');
    const filter = screen.getByRole('combobox', { name: '对账状态' });
    expect(
      within(filter)
        .getAllByRole('option')
        .map((option) => option.value),
    ).toEqual(['pending', 'approved', 'applied', 'completed', 'all']);
    fireEvent.change(filter, { target: { value: 'approved' } });
    fireEvent.click(
      await screen.findByRole('button', { name: '查看凭证 nocharge-71' }),
    );
    const detail = screen.getByRole('region', { name: '尝试凭证审核' });
    expect(within(detail).getByText('核验已通过，待入账')).toBeVisible();
    expect(
      within(detail).queryByRole('button', { name: '确认批准凭证' }),
    ).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '刷新尝试对账' }));
    await screen.findByText('nocharge-71');
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/proofs?status=approved&after_id=0&limit=100',
    );
    fireEvent.change(filter, { target: { value: 'completed' } });
    await screen.findByText('nocharge-71');
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/proofs?status=completed&after_id=0&limit=100',
    );
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it('finds completed refund receipts with reset pagination and never offers unsupported approved status', async () => {
    handler = (url) => {
      if (!url.includes('/supplier-refunds?')) return reply(list([]));
      const params = new URL(url, 'http://local').searchParams;
      if (params.get('status') === 'completed')
        return reply(
          list([
            {
              ...refund,
              status: 'completed',
              pending_reason: '',
              ledger_id: 991,
              refunded_quota: 25,
            },
          ]),
        );
      return reply(
        params.get('after_id') === '0'
          ? list(
              Array.from({ length: 20 }, (_, i) => ({
                ...refund,
                id: i + 41,
                proof_id: `proof-${i}`,
              })),
              60,
            )
          : list([refund]),
      );
    };
    render(<ReconciliationPage canWrite />);
    fireEvent.click(screen.getByRole('tab', { name: '供应商退款' }));
    await screen.findByText('proof-19');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('proof-41');
    const filter = screen.getByRole('combobox', { name: '退款状态' });
    expect(
      within(filter)
        .getAllByRole('option')
        .map((option) => option.value),
    ).toEqual(['pending', 'applied', 'completed', 'all']);
    fireEvent.change(filter, { target: { value: 'completed' } });
    fireEvent.click(
      await screen.findByRole('button', { name: '查看退款 proof-41' }),
    );
    expect(requests.at(-1).url).toBe(
      '/api/admin/supplier-refunds?status=completed&after_id=0&limit=20',
    );
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    expect(
      within(screen.getByRole('region', { name: '退款审核详情' })).getByText(
        '991',
      ),
    ).toBeVisible();
    expect(screen.queryByRole('button', { name: '确认核验并批准' })).toBeNull();
  });
});

describe('structured evidence forms', () => {
  const fill = (dialog, label, value) =>
    fireEvent.change(within(dialog).getByLabelText(label, { exact: true }), {
      target: { value: String(value) },
    });
  async function attemptDialog() {
    fireEvent.click(
      await screen.findByRole('tab', { name: '尝试对账', exact: true }),
    );
    fireEvent.click(
      await screen.findByRole('button', {
        name: '提交凭证 req-settled-parent 第1次',
      }),
    );
    return screen.getByRole('dialog', { name: '提交尝试凭证' });
  }
  function common(dialog) {
    fill(dialog, '凭证来源', attemptSubmission.source);
    fill(dialog, '凭证编号', attemptSubmission.proof_id);
    fill(dialog, '证据引用', attemptSubmission.evidence_reference);
  }
  beforeEach(() => {
    handler = attemptHandler;
  });

  it('submits billed usage from normal controls with locked selected identities and exact wire fields', async () => {
    handler = (url, init) =>
      init.method === 'POST' ? reply(attemptProof) : attemptHandler(url);
    mount();
    const dialog = await attemptDialog();
    expect(
      within(dialog).queryByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toBeNull();
    expect(
      within(dialog).getByRole('button', { name: '高级导入' }),
    ).toHaveAttribute('aria-expanded', 'false');
    expect(within(dialog).getByText('req-settled-parent')).toBeVisible();
    expect(within(dialog).getByText('version-3')).toBeVisible();
    expect(
      within(dialog).queryByRole('textbox', { name: '原请求 ID' }),
    ).toBeNull();
    common(dialog);
    fill(dialog, '凭证类型', 'billed');
    fill(dialog, '用量口径', 'openai');
    fill(dialog, '上游账单行编号', 'bill-line-71');
    fill(dialog, '独立用量核验引用', 'execution-first-only');
    fireEvent.click(within(dialog).getByRole('button', { name: '添加用量' }));
    fill(dialog, '计费项 1', 'input_tokens');
    fill(dialog, '用量 1', 10);
    fireEvent.click(within(dialog).getByRole('button', { name: '添加用量' }));
    fill(dialog, '计费项 2', 'cache_read');
    fill(dialog, '用量 2', 2);
    const submit = within(dialog).getByRole('button', { name: '提交待审凭证' });
    expect(submit).toBeDisabled();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fill(dialog, '证据引用', 'updated-evidence');
    expect(submit).toBeDisabled();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(submit);
    await screen.findByText('凭证已登记，等待财务审核；未执行扣费或释放预留。');
    expect(JSON.parse(requests.at(-1).body)).toEqual({
      ...attemptSubmission,
      kind: 'billed',
      usage_semantic: 'openai',
      upstream_bill_id: 'bill-line-71',
      distinct_usage_reference: 'execution-first-only',
      evidence_reference: 'updated-evidence',
      usage: [
        { dimension: 'input_tokens', quantity: 10 },
        { dimension: 'cache_read', quantity: 2 },
      ],
    });
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
  });

  it('clears billed-only data when switching to nocharge and submits without JSON', async () => {
    handler = (url, init) =>
      init.method === 'POST' ? reply(attemptProof) : attemptHandler(url);
    mount();
    const dialog = await attemptDialog();
    common(dialog);
    fill(dialog, '凭证类型', 'billed');
    fill(dialog, '上游账单行编号', 'discarded-bill');
    fill(dialog, '用量口径', 'anthropic');
    fireEvent.click(within(dialog).getByRole('button', { name: '添加用量' }));
    fill(dialog, '计费项 1', 'input_tokens');
    fill(dialog, '用量 1', 10);
    fill(dialog, '凭证类型', 'nocharge');
    expect(within(dialog).queryByLabelText('用量 1')).toBeNull();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    );
    await screen.findByText('凭证已登记，等待财务审核；未执行扣费或释放预留。');
    expect(JSON.parse(requests.at(-1).body)).toEqual(attemptSubmission);
  });

  it.each(['0', '1.5', '2147483648'])(
    'rejects invalid structured quantity %s before POST',
    async (quantity) => {
      mount();
      const dialog = await attemptDialog();
      common(dialog);
      fill(dialog, '凭证类型', 'billed');
      fill(dialog, '用量口径', 'openai');
      fireEvent.click(within(dialog).getByRole('button', { name: '添加用量' }));
      fill(dialog, '计费项 1', 'input_tokens');
      fill(dialog, '用量 1', quantity);
      fireEvent.click(within(dialog).getByRole('checkbox'));
      expect(
        within(dialog).getByRole('button', { name: '提交待审凭证' }),
      ).toBeDisabled();
      expect(within(dialog).getByRole('alert')).toBeVisible();
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    },
  );

  it('generates all-attempt no-charge evidence from per-attempt references without editable identities', async () => {
    handler = (url, init) =>
      init.method === 'POST'
        ? reply({
            id: 21,
            request_id: settlement.request_id,
            status: 'released',
            ledger_id: 91,
            cache_sync_pending: false,
          })
        : reply(
            url.endsWith('/21')
              ? { ...settlement, attempts: noChargeAttempts }
              : list([settlement]),
          );
    mount();
    fireEvent.click(
      await screen.findByRole('button', { name: '查看结算 req-original-21' }),
    );
    fireEvent.click(
      await screen.findByRole('button', { name: '核验整单未收费' }),
    );
    const dialog = screen.getByRole('dialog');
    expect(
      within(dialog).queryByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toBeNull();
    fill(dialog, '凭证来源', wholeProof.source);
    fill(dialog, '凭证编号', wholeProof.proof_id);
    fill(dialog, '整单核验引用', wholeProof.verification_reference);
    fill(dialog, '尝试 1 核验引用', 'verified-wire-1');
    fireEvent.click(within(dialog).getByRole('checkbox'));
    const submit = within(dialog).getByRole('button', {
      name: '确认未收费并提交核验',
    });
    expect(submit).toBeDisabled();
    fill(dialog, '尝试 2 核验引用', 'verified-wire-2');
    expect(submit).toBeDisabled();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(submit);
    await screen.findByText('整单未收费核验已完成，预留已释放。');
    expect(JSON.parse(requests.at(-1).body)).toEqual(wholeProof);
  });

  it('registers a supplier refund using fields and preserves decimal usage strings', async () => {
    handler = (url, init) =>
      init.method === 'POST' ? reply(refund) : reply(list([]));
    mount();
    fireEvent.click(screen.getByRole('tab', { name: '供应商退款' }));
    fireEvent.click(
      await screen.findByRole('button', { name: '登记退款凭证' }),
    );
    const dialog = screen.getByRole('dialog');
    for (const [label, key] of [
      ['凭证来源', 'source'],
      ['凭证编号', 'proof_id'],
      ['证据引用', 'evidence_reference'],
      ['原请求 ID', 'request_id'],
      ['客户 ID', 'user_id'],
      ['尝试序号', 'attempt'],
      ['渠道 ID', 'channel_id'],
      ['凭据版本', 'credential_version'],
      ['上游请求 ID', 'upstream_request_id'],
      ['上游任务 ID', 'upstream_task_id'],
      ['上游账单行编号', 'upstream_bill_id'],
    ])
      fill(dialog, label, refund.submission[key]);
    fill(dialog, '退款范围', 'partial');
    fireEvent.click(within(dialog).getByRole('button', { name: '添加用量' }));
    fill(dialog, '计费项 1', 'output_tokens');
    fill(dialog, '退款用量 1', '25.125');
    expect(
      within(dialog).queryByRole('textbox', { name: '供应商凭证 JSON' }),
    ).toBeNull();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    );
    await screen.findByText('退款凭证已登记，等待财务审核；未执行退款。');
    const posts = requests.filter((r) => r.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(JSON.parse(posts[0].body)).toEqual({
      ...refund.submission,
      units: [{ dimension: 'output_tokens', units: '25.125' }],
    });
  });
});

describe('attempt reconciliation', () => {
  beforeEach(() => {
    handler = attemptHandler;
  });

  it('exposes pending reason and credential identity to read-only finance users', async () => {
    render(<ReconciliationPage canWrite={false} />);
    await openAttempts();
    const queue = screen.getByRole('region', { name: '待对账尝试表格' });
    expect(within(queue).getByText('version-3')).toBeVisible();
    expect(within(queue).getByText('待补供应商凭证')).toBeVisible();
    expect(screen.queryByRole('button', { name: /提交凭证/ })).toBeNull();
  });

  it('requires explicit no-charge verification and reports completed proof without implying a whole-request release', async () => {
    handler = (url, init) =>
      init.method === 'POST'
        ? reply({ ...attemptProof, status: 'completed', pending_reason: '' })
        : attemptHandler(url);
    mount();
    const detail = await openAttemptProof();
    fireEvent.change(within(detail).getByLabelText('核验记录编号'), {
      target: { value: 'verified-statement-71' },
    });
    fireEvent.click(
      within(detail).getByRole('checkbox', { name: /确认本次尝试确未收费/ }),
    );
    fireEvent.click(
      within(detail).getByRole('button', { name: '确认批准凭证' }),
    );
    await screen.findByText('本次尝试凭证已处理完成；整单状态以请求账本为准。');
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/proofs/71/approve',
    );
  });

  it('reports the returned state when submission replays an already completed proof', async () => {
    handler = (url, init) =>
      init.method === 'POST'
        ? reply({ ...attemptProof, status: 'completed' })
        : attemptHandler(url);
    mount();
    await openAttempts();
    fireEvent.click(
      screen.getByRole('button', { name: '提交凭证 req-settled-parent 第1次' }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
      target: { value: JSON.stringify(attemptSubmission) },
    });
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    );
    await screen.findByText('凭证记录已返回：处理已完成。请按记录核对账本。');
    expect(
      screen.queryByText('凭证已登记，等待财务审核；未执行扣费或释放预留。'),
    ).toBeNull();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
  });

  it('bounds attempt pagination at 100 and rejects backward cursors', async () => {
    handler = (url) =>
      url.includes('/reviews?')
        ? reply(
            url.includes('after_id=160')
              ? list([attemptReview], 159)
              : list(
                  Array.from({ length: 100 }, (_, index) => ({
                    ...attemptReview,
                    id: 61 + index,
                    request_id: `attempt-page-${index}`,
                  })),
                  160,
                ),
          )
        : reply(list([]));
    render(<ReconciliationPage />);
    fireEvent.click(screen.getByRole('tab', { name: '尝试对账', exact: true }));
    await screen.findByText('attempt-page-99');
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await screen.findByText('req-settled-parent');
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/reviews?status=pending&after_id=160&limit=100',
    );
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    await screen.findByText('attempt-page-99');
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/reviews?status=pending&after_id=0&limit=100',
    );
  });

  it('supports keyboard navigation between attempt views', async () => {
    render(<ReconciliationPage />);
    await openAttempts();
    const reviews = screen.getByRole('tab', { name: '待对账尝试' });
    reviews.focus();
    fireEvent.keyDown(reviews, { key: 'ArrowRight' });
    await screen.findByText('nocharge-71');
    expect(screen.getByRole('tab', { name: '待审凭证' })).toHaveFocus();
    expect(reviews).toHaveAttribute('tabindex', '-1');
  });

  it('removes submission and approval controls after financeWrite is revoked', async () => {
    const view = render(<ReconciliationPage canWrite />);
    await openAttempts();
    const opener = screen.getByRole('button', {
      name: '提交凭证 req-settled-parent 第1次',
    });
    opener.focus();
    fireEvent.click(opener);
    expect(screen.getByRole('dialog')).toHaveFocus();
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(opener).toHaveFocus();
    fireEvent.click(opener);
    view.rerender(<ReconciliationPage canWrite={false} />);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('button', { name: /提交凭证/ })).toBeNull();
    view.rerender(<ReconciliationPage canWrite />);
    expect(screen.queryByRole('dialog')).toBeNull();
    await openAttemptProof();
    view.rerender(<ReconciliationPage canWrite={false} />);
    expect(screen.queryByRole('button', { name: '确认批准凭证' })).toBeNull();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it('loads pending attempts independently when the parent settlement is no longer in the pending queue', async () => {
    mount();
    await screen.findByText('暂无待结算请求。');
    await openAttempts();
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/reviews?status=pending&after_id=0&limit=100',
    );
    expect(screen.getByText('upstream-first')).toBeVisible();
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it('keeps attempts and proof evidence available to read-only finance viewers', async () => {
    render(<ReconciliationPage />);
    const detail = await openAttemptProof();
    expect(within(detail).getByText('statement:71')).toBeVisible();
    expect(
      screen.queryByRole('button', { name: /提交凭证|批准凭证/ }),
    ).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(requests.at(-1).url).toBe(
      '/api/admin/attempt-billing/proofs?status=pending&after_id=0&limit=100',
    );
  });

  it('supports a third keyboard tab without breaking existing navigation', async () => {
    mount();
    const first = screen.getByRole('tab', { name: '待结算请求' });
    fireEvent.keyDown(first, { key: 'End' });
    expect(screen.getByRole('tab', { name: '尝试对账' })).toHaveFocus();
    await screen.findByText('req-settled-parent');
  });

  it('shows loading and safe failures and refreshes the active attempt queue', async () => {
    const pending = deferred();
    handler = (url) =>
      url.includes('/attempt-billing/') ? pending.promise : reply(list([]));
    mount();
    fireEvent.click(screen.getByRole('tab', { name: '尝试对账' }));
    expect(screen.getByRole('status')).toHaveTextContent('正在加载');
    await act(async () =>
      pending.resolve(reply({ message: 'PRIVATE_ERROR' }, 503)),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '尝试对账加载失败',
    );
    handler = () => reply(list([]));
    fireEvent.click(screen.getByRole('button', { name: '刷新尝试对账' }));
    expect(await screen.findByText('暂无待对账尝试。')).toBeVisible();
    expect(screen.queryByText(/PRIVATE_ERROR/)).toBeNull();
  });

  it('discards an old attempt response when switching to proof review', async () => {
    const older = deferred();
    handler = (url) =>
      url.includes('/reviews?')
        ? older.promise
        : reply(list(url.includes('/proofs?') ? [attemptProof] : []));
    mount();
    fireEvent.click(screen.getByRole('tab', { name: '尝试对账' }));
    fireEvent.click(screen.getByRole('tab', { name: '待审凭证' }));
    await screen.findByText('nocharge-71');
    await act(async () => older.resolve(reply(list([attemptReview]))));
    expect(screen.getByText('nocharge-71')).toBeVisible();
    expect(
      screen.queryByRole('button', { name: /提交凭证 req-settled-parent/ }),
    ).toBeNull();
  });

  it('requires an approval reference and confirmation and preserves pending semantics', async () => {
    const pending = deferred();
    handler = (url, init) =>
      init.method === 'POST' ? pending.promise : attemptHandler(url);
    mount();
    const detail = await openAttemptProof();
    const approve = within(detail).getByRole('button', {
      name: '确认批准凭证',
    });
    expect(approve).toBeDisabled();
    fireEvent.change(within(detail).getByLabelText('核验记录编号'), {
      target: { value: ' review-attempt:71 ' },
    });
    expect(approve).toBeDisabled();
    fireEvent.click(
      within(detail).getByRole('checkbox', { name: /已核验本次尝试/ }),
    );
    fireEvent.click(approve);
    fireEvent.click(approve);
    await waitFor(() =>
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1),
    );
    const post = requests.find((r) => r.method === 'POST');
    expect(post.url).toBe('/api/admin/attempt-billing/proofs/71/approve');
    expect(JSON.parse(post.body)).toEqual({
      verification_reference: 'review-attempt:71',
    });
    await act(async () =>
      pending.resolve(
        reply({ ...attemptProof, pending_reason: 'parent_pending' }, 202),
      ),
    );
    expect(
      await screen.findByText('凭证审批已受理，仍待对账，未确认账务处理完成。'),
    ).toBeVisible();
    expect(screen.queryByRole('button', { name: '确认批准凭证' })).toBeNull();
    expect(requests.some((r) => r.url.includes('confirm-no-charge'))).toBe(
      false,
    );
  });

  it.each([403, 409, 503])(
    'locks proof approval after HTTP %s without automatic retries',
    async (status) => {
      handler = (url, init) =>
        init.method === 'POST' ? reply({}, status) : attemptHandler(url);
      mount();
      const detail = await openAttemptProof();
      fireEvent.change(within(detail).getByLabelText('核验记录编号'), {
        target: { value: 'review:71' },
      });
      fireEvent.click(within(detail).getByRole('checkbox'));
      fireEvent.click(
        within(detail).getByRole('button', { name: '确认批准凭证' }),
      );
      expect(await screen.findByRole('alert')).toHaveTextContent(
        /权限|冲突|未确认/,
      );
      expect(
        screen.getByRole('button', { name: '确认批准凭证' }),
      ).toBeDisabled();
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
    },
  );

  it('submits an identity-bound nocharge proof only after explicit confirmation, without approving or freeing holds', async () => {
    handler = (url, init) =>
      init.method === 'POST' ? reply(attemptProof) : attemptHandler(url);
    mount();
    await openAttempts();
    const opener = screen.getByRole('button', {
      name: '提交凭证 req-settled-parent 第1次',
    });
    fireEvent.click(opener);
    const dialog = screen.getByRole('dialog', { name: '提交尝试凭证' });
    const input = within(dialog).getByLabelText('供应商凭证 JSON');
    fireEvent.change(input, {
      target: { value: JSON.stringify(attemptSubmission) },
    });
    const submit = within(dialog).getByRole('button', { name: '提交待审凭证' });
    expect(submit).toBeDisabled();
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(submit);
    expect(
      await screen.findByText(
        '凭证已登记，等待财务审核；未执行扣费或释放预留。',
      ),
    ).toBeVisible();
    const posts = requests.filter((r) => r.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toBe('/api/admin/attempt-billing/proofs');
    expect(JSON.parse(posts[0].body)).toEqual(attemptSubmission);
  });

  it.each([
    ['identity', { ...attemptSubmission, user_id: 9 }],
    ['money', { ...attemptSubmission, charged_quota: 10 }],
    [
      'nested price',
      {
        ...attemptSubmission,
        kind: 'billed',
        usage: [{ dimension: 'input_tokens', quantity: 3, price: 5 }],
      },
    ],
    [
      'fraction',
      {
        ...attemptSubmission,
        kind: 'billed',
        usage: [{ dimension: 'input_tokens', quantity: 1.5 }],
      },
    ],
    [
      'duplicate dimension',
      {
        ...attemptSubmission,
        kind: 'billed',
        usage: [
          { dimension: 'input_tokens', quantity: 1 },
          { dimension: 'input_tokens', quantity: 2 },
        ],
      },
    ],
  ])(
    'rejects %s in imported attempt proof before issuing a write',
    async (_, evidence) => {
      mount();
      await openAttempts();
      fireEvent.click(
        screen.getByRole('button', {
          name: '提交凭证 req-settled-parent 第1次',
        }),
      );
      const dialog = screen.getByRole('dialog');
      fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
        target: { value: JSON.stringify(evidence) },
      });
      fireEvent.click(within(dialog).getByRole('checkbox'));
      expect(within(dialog).getByRole('alert')).toBeVisible();
      expect(
        within(dialog).getByRole('button', { name: '提交待审凭证' }),
      ).toBeDisabled();
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
    },
  );

  it.each([
    [
      'zero quantity',
      {
        ...attemptSubmission,
        kind: 'billed',
        usage_semantic: 'openai',
        usage: [
          { dimension: 'input_tokens', quantity: 0 },
          { dimension: 'output_tokens', quantity: 1 },
        ],
      },
    ],
    [
      'unknown nocharge semantic',
      { ...attemptSubmission, usage_semantic: 'invented' },
    ],
  ])('rejects model-invalid %s before submission', async (_, evidence) => {
    mount();
    await openAttempts();
    fireEvent.click(
      screen.getByRole('button', { name: '提交凭证 req-settled-parent 第1次' }),
    );
    const dialog = screen.getByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
      target: { value: JSON.stringify(evidence) },
    });
    fireEvent.click(within(dialog).getByRole('checkbox'));
    expect(within(dialog).getByRole('alert')).toBeVisible();
    expect(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    ).toBeDisabled();
    expect(requests.filter((r) => r.method === 'POST')).toHaveLength(0);
  });

  it.each(['openai', 'anthropic'])(
    'submits normalized billed evidence using %s without changing its identities or charging',
    async (semantic) => {
      const evidence = {
        ...attemptSubmission,
        kind: 'billed',
        usage_semantic: semantic,
        upstream_bill_id: 'bill-line-71',
        distinct_usage_reference: 'execution-first-only',
        usage: [
          { dimension: 'input_tokens', quantity: 10 },
          { dimension: 'cache_read', quantity: 2 },
        ],
      };
      handler = (url, init) =>
        init.method === 'POST'
          ? reply({ ...attemptProof, submission: evidence })
          : attemptHandler(url);
      mount();
      await openAttempts();
      fireEvent.click(
        screen.getByRole('button', {
          name: '提交凭证 req-settled-parent 第1次',
        }),
      );
      const dialog = screen.getByRole('dialog');
      fireEvent.change(within(dialog).getByLabelText('供应商凭证 JSON'), {
        target: { value: JSON.stringify(evidence) },
      });
      fireEvent.click(within(dialog).getByRole('checkbox'));
      fireEvent.click(
        within(dialog).getByRole('button', { name: '提交待审凭证' }),
      );
      await screen.findByText(
        '凭证已登记，等待财务审核；未执行扣费或释放预留。',
      );
      expect(requests.filter((r) => r.method === 'POST')).toHaveLength(1);
      expect(JSON.parse(requests.at(-1).body)).toEqual(evidence);
    },
  );

  it('registers a supplier refund as pending without approval and refuses forged amount fields', async () => {
    handler = (url, init) =>
      init.method === 'POST' ? reply(refund) : reply(list([]));
    mount();
    fireEvent.click(screen.getByRole('tab', { name: '供应商退款' }));
    fireEvent.click(
      await screen.findByRole('button', { name: '登记退款凭证' }),
    );
    const dialog = screen.getByRole('dialog', { name: '登记退款凭证' });
    const input = within(dialog).getByLabelText('供应商凭证 JSON');
    fireEvent.change(input, {
      target: { value: JSON.stringify({ ...refund.submission, amount: 99 }) },
    });
    fireEvent.click(within(dialog).getByRole('checkbox'));
    expect(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    ).toBeDisabled();
    fireEvent.change(input, {
      target: { value: JSON.stringify(refund.submission) },
    });
    fireEvent.click(within(dialog).getByRole('checkbox'));
    fireEvent.click(
      within(dialog).getByRole('button', { name: '提交待审凭证' }),
    );
    expect(
      await screen.findByText('退款凭证已登记，等待财务审核；未执行退款。'),
    ).toBeVisible();
    const posts = requests.filter((r) => r.method === 'POST');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toBe('/api/admin/supplier-refunds');
    expect(JSON.parse(posts[0].body)).toEqual(refund.submission);
  });
});
