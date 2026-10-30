import React from 'react';
import '@testing-library/jest-dom/vitest';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import OverviewPage from './OverviewPage.jsx';
import { adminRequest, adminDownload } from '../../auth/admin-session.js';
vi.mock('../../auth/admin-session.js', () => ({
  adminRequest: vi.fn(),
  adminDownload: vi.fn(),
}));
const report = {
  month: '2026-09',
  period_closed: true,
  confirmed: false,
  revenue: '20.000000',
  known_revenue: '20.000000',
  actual_cost: null,
  gross_profit: null,
  receipts_total: '100.000000',
  charged_quota: 10500000,
  quota_per_unit: 500000,
  pending_requests: 2,
  issue_count: 1,
  issues: [{ code: 'supplier_cost_missing', ref: 'channel:2' }],
  receipts: [],
  costs: [],
  manual_funding: [],
  channels: [{ channel_id: 2, name: '号池', attempts: 3 }],
};
beforeEach(() => {
  vi.clearAllMocks();
  adminRequest.mockResolvedValue(report);
});
afterEach(cleanup);
it('records direct verification bills without a fabricated request', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入直接上游验收费用' }));
  fireEvent.change(screen.getByLabelText('供应商'), { target: { value: 'Xcloud' } });
  fireEvent.change(screen.getByLabelText('上游账户标识'), { target: { value: 'account-1' } });
  fireEvent.change(screen.getByLabelText('上游账单 ID'), { target: { value: 'bill-1' } });
  fireEvent.change(screen.getByLabelText('验收记录 ID'), { target: { value: '656' } });
  fireEvent.change(screen.getByLabelText('验收渠道 ID'), { target: { value: '2' } });
  fireEvent.change(screen.getByLabelText('账单发生时间（北京时间）'), { target: { value: '2026-09-16T10:00' } });
  fireEvent.change(screen.getByLabelText('上游模型'), { target: { value: 'model-a' } });
  fireEvent.change(screen.getByLabelText('输入 token'), { target: { value: '123' } });
  fireEvent.change(screen.getByLabelText('输出 token'), { target: { value: '0' } });
  fireEvent.change(screen.getByLabelText('输入 token 口径'), { target: { value: 'inclusive' } });
  fireEvent.change(screen.getByLabelText('缓存读取 token'), { target: { value: '900000' } });
  fireEvent.change(screen.getByLabelText('缓存写入 5 分钟 token'), { target: { value: '0' } });
  fireEvent.change(screen.getByLabelText('直接费用原币金额'), { target: { value: '0.000022' } });
  fireEvent.change(screen.getByLabelText('上游账单凭证'), { target: { value: 'statement-1' } });
  fireEvent.change(screen.getByLabelText('验收关联凭证'), { target: { value: 'link-656' } });
  fireEvent.click(screen.getByRole('button', { name: '保存直接费用' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({method:'POST', body: expect.stringContaining('"kind":"direct_cost"')})));
  const post = adminRequest.mock.calls.find(([call]) => call.method === 'POST')[0];
  const body = JSON.parse(post.body);
  expect(body.direct_cost).toMatchObject({ verification_id:656, bill_id:'bill-1', amount:'0.000022', currency:'XCLOUD_CREDIT', coverage:'unknown' });
  expect(body.direct_cost).toMatchObject({ model:'model-a', input_tokens:123, output_tokens:0 });
  expect(body.direct_cost).toMatchObject({input_token_scope:'inclusive', cache_read:900000, cache_write_5m:0, cache_write_1h:null});
  expect(body.direct_cost.request_id).toBeUndefined();
});
it('shows and edits direct cache evidence without treating unknown as zero', async () => {
  adminRequest.mockResolvedValue({ ...report, direct_costs:[{ fact_id:7, bill_id:'bill-cache', supplier:'Xcloud', account:'account-1', verification_id:656, channel_id:2, occurred_at:'2026-09-16T02:00:00Z', amount:'1', currency:'XCLOUD_CREDIT', coverage:'unknown', input_token_scope:'exclusive', cache_read:null, cache_write_5m:0, cache_write_1h:500 }] });
  render(<OverviewPage canWrite />);
  expect(await screen.findByText('exclusive')).toBeVisible();
  expect(screen.getByText('待核对 / 0 / 500')).toBeVisible();
  fireEvent.click(screen.getByRole('button', {name:'修订bill-cache直接费用'}));
  expect(screen.getByLabelText('输入 token 口径')).toHaveValue('exclusive');
  expect(screen.getByLabelText('缓存读取 token')).toHaveValue(null);
  expect(screen.getByLabelText('缓存写入 5 分钟 token')).toHaveValue(0);
  expect(screen.getByLabelText('缓存写入 1 小时 token')).toHaveValue(500);
});
it('shows unknown profit as pending rather than zero', async () => {
  render(<OverviewPage canWrite={false} />);
  expect(await screen.findByText('毛利待核对')).toBeVisible();
  expect(screen.getByText('100.000000')).toBeVisible();
  expect(screen.queryByRole('button', { name: /录入.*成本/ })).toBeNull();
});
it('shows confirmed gross profit and loads another month', async () => {
  adminRequest.mockResolvedValue({
    ...report,
    confirmed: true,
    actual_cost: '15.000000',
    gross_profit: '5.000000',
    margin: '25.00',
    issue_count: 0,
    issues: [],
  });
  render(<OverviewPage canWrite />);
  expect(await screen.findByText('5.000000')).toBeVisible();
  fireEvent.change(screen.getByLabelText('统计月份'), {
    target: { value: '2026-09' },
  });
  await waitFor(() =>
    expect(adminRequest).toHaveBeenLastCalledWith(
      expect.objectContaining({
        url: '/api/admin/finance-reporting/report?month=2026-09',
      }),
    ),
  );
});
it('offers evidence-only cost entry without touching wallet endpoints', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入号池成本' }));
  expect(screen.getByRole('dialog', { name: '上游实际成本' })).toBeVisible();
  expect(screen.getByLabelText('原币实际消耗')).toHaveValue(null);
  expect(screen.getByLabelText('账单凭证')).toHaveValue('');
  expect(
    screen.getByLabelText('确认已覆盖整月本平台全部凭据、失败重试和测试消耗'),
  ).not.toBeChecked();
});
it('does not retain stale financial data after a load fails', async () => {
  render(<OverviewPage canWrite={false} />);
  await screen.findByText('100.000000');
  adminRequest.mockRejectedValueOnce(new Error('offline'));
  fireEvent.click(screen.getByRole('button', { name: '刷新财务数据' }));
  expect(await screen.findByRole('alert')).toHaveTextContent(
    '财务数据加载失败',
  );
  expect(screen.queryByText('100.000000')).toBeNull();
});
it('exports with a dated CSV filename', async () => {
  render(<OverviewPage />);
  await screen.findByText('100.000000');
  fireEvent.click(screen.getByRole('button', { name: '导出本月财务报表' }));
  await waitFor(() =>
    expect(adminDownload).toHaveBeenCalledWith(
      expect.objectContaining({
        url: expect.stringContaining('/export?month='),
      }),
      expect.stringMatching(/^ztapi-finance-\d{4}-\d{2}\.csv$/),
    ),
  );
});

it('keeps dialog keyboard focus inside and restores the opener', async () => {
  render(<OverviewPage canWrite />);
  const opener = await screen.findByRole('button', { name: '录入号池成本' });
  opener.focus();
  fireEvent.click(opener);
  const dialog = screen.getByRole('dialog');
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
  expect(screen.getByRole('button', { name: '保存财务凭证' })).toHaveFocus();
  fireEvent.click(screen.getByRole('button', { name: '关闭财务凭证' }));
  expect(opener).toHaveFocus();
});

it('shows test costs separately and limits entry to finance writers', async () => {
  adminRequest.mockResolvedValue({
    ...report,
    test_confirmed_total: '2.000000',
    test_estimate_total: '1.000000',
    test_costs: [
      {
        fact_id: 8,
        request_id: 'test-r1',
        attempt: 2,
        channel_id: 2,
        status: 'confirmed',
        coverage: 'included',
        net_usdt: '2.000000',
        attempt_at: '2026-09-16T00:00:00Z',
      },
    ],
  });
  const { rerender } = render(<OverviewPage canWrite={false} />);
  expect(await screen.findByText('测试费用')).toBeVisible();
  expect(screen.getByText('test-r1')).toBeVisible();
  expect(screen.getByText('账单内')).toBeVisible();
  expect(screen.queryByRole('button', { name: '录入测试费用' })).toBeNull();
  rerender(<OverviewPage canWrite />);
  fireEvent.click(screen.getByRole('button', { name: '录入测试费用' }));
  expect(screen.getByRole('dialog', { name: '测试费用凭证' })).toBeVisible();
  expect(screen.getByLabelText('请求 ID')).toBeRequired();
  expect(screen.getByLabelText('覆盖关系')).toHaveValue('unknown');
});

it('submits a request-linked test estimate through the facts endpoint', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入测试费用' }));
  fireEvent.change(screen.getByLabelText('请求 ID'), {
    target: { value: 'request-1' },
  });
  fireEvent.change(screen.getByLabelText('尝试序号'), {
    target: { value: '2' },
  });
  fireEvent.change(screen.getByLabelText('渠道 ID'), {
    target: { value: '3' },
  });
  fireEvent.change(screen.getByLabelText('原币费用'), {
    target: { value: '1.2' },
  });
  fireEvent.change(screen.getByLabelText('证据类别'), {
    target: { value: 'quote' },
  });
  fireEvent.change(screen.getByLabelText('费用凭证或报价依据'), {
    target: { value: 'quote-123' },
  });
  fireEvent.click(screen.getByRole('button', { name: '保存凭证' }));
  await waitFor(() =>
    expect(adminRequest).toHaveBeenCalledWith(
      expect.objectContaining({
        url: '/api/admin/finance-reporting/facts',
        method: 'POST',
        body: expect.stringContaining('"ref":"test:request-1:2:3"'),
      }),
    ),
  );
  const post = adminRequest.mock.calls.find(
    ([call]) => call.method === 'POST',
  )[0];
  expect(JSON.parse(post.body)).toMatchObject({
    previous_id: 0,
    test_cost: {
      status: 'estimate',
      evidence_type: 'quote',
      coverage: 'unknown',
      amount: '1.2',
    },
  });
});

it('keeps test-cost dialog focus contained and restores it on close', async () => {
  render(<OverviewPage canWrite />);
  const opener = await screen.findByRole('button', { name: '录入测试费用' });
  opener.focus();
  fireEvent.click(opener);
  const dialog = screen.getByRole('dialog', { name: '测试费用凭证' });
  expect(dialog).toHaveFocus();
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
  expect(screen.getByRole('button', { name: '保存凭证' })).toHaveFocus();
  fireEvent.keyDown(dialog, { key: 'Escape' });
  expect(opener).toHaveFocus();
});

it('records Xcloud credits and token counts without inventing a cash exchange rate', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入测试费用' }));
  fireEvent.change(screen.getByLabelText('请求 ID'), { target: { value: 'request-kimi' } });
  fireEvent.change(screen.getByLabelText('尝试序号'), { target: { value: '1' } });
  fireEvent.change(screen.getByLabelText('渠道 ID'), { target: { value: '8' } });
  fireEvent.change(screen.getByLabelText('上游输入 Token'), { target: { value: '125' } });
  fireEvent.change(screen.getByLabelText('上游输出 Token'), { target: { value: '24' } });
  fireEvent.change(screen.getByLabelText('原币费用'), { target: { value: '0.001726' } });
  fireEvent.change(screen.getByLabelText('原币币种'), { target: { value: 'XCLOUD_CREDIT' } });
  fireEvent.change(screen.getByLabelText('证据类别'), { target: { value: 'supplier_statement' } });
  fireEvent.change(screen.getByLabelText('费用凭证或报价依据'), { target: { value: 'supplier-receipt-123' } });
  expect(screen.getByLabelText('每单位原币折合 USDT')).toBeEnabled();
  expect(screen.getByLabelText('每单位原币折合 USDT')).toHaveValue(null);
  fireEvent.click(screen.getByRole('button', { name: '保存凭证' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' })));
  const post = adminRequest.mock.calls.find(([call]) => call.method === 'POST')[0];
  expect(JSON.parse(post.body).test_cost).toMatchObject({ input_tokens: 125, output_tokens: 24, currency: 'XCLOUD_CREDIT', rate: '', status: 'confirmed', coverage: 'unknown' });
});

it('preserves an explicit credit conversion and its evidence on revision', async () => {
  adminRequest.mockResolvedValue({...report, test_costs:[{fact_id:7, request_id:'converted-test', attempt:1, channel_id:8, amount:'2', refund:'0', currency:'XCLOUD_CREDIT', rate:'0.5', rate_evidence:'owner confirmed 100 for 200', status:'confirmed', evidence_type:'supplier_statement', evidence:'receipt-123', coverage:'unknown', net_usdt:'1.000000'}]});
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button',{name:'修订converted-test测试费用'}));
  expect(screen.getByLabelText('每单位原币折合 USDT')).toHaveValue(0.5);
  expect(screen.getByLabelText('测试费用折算依据')).toHaveValue('owner confirmed 100 for 200');
  fireEvent.click(screen.getByRole('button',{name:'保存凭证'}));
  await waitFor(()=>expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({method:'POST'})));
  const post=adminRequest.mock.calls.find(([call])=>call.method==='POST')[0];
  expect(JSON.parse(post.body).test_cost).toMatchObject({currency:'XCLOUD_CREDIT',rate:'0.5',rate_evidence:'owner confirmed 100 for 200'});
});

it('records an optional supplier bill ID without defaulting missing usage to zero', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入测试费用' }));
  const bill = screen.getByLabelText('供应商账单 ID（可选）');
  expect(bill).not.toBeRequired();
  fireEvent.change(bill, { target: { value: ' supplier-request-bill ' } });
  fireEvent.change(screen.getByLabelText('请求 ID'), { target: { value: 'request-bill' } });
  fireEvent.change(screen.getByLabelText('尝试序号'), { target: { value: '1' } });
  fireEvent.change(screen.getByLabelText('渠道 ID'), { target: { value: '2' } });
  fireEvent.change(screen.getByLabelText('原币费用'), { target: { value: '2' } });
  fireEvent.change(screen.getByLabelText('证据类别'), { target: { value: 'quote' } });
  fireEvent.change(screen.getByLabelText('费用凭证或报价依据'), { target: { value: 'quote-123' } });
  fireEvent.click(screen.getByRole('button', { name: '保存凭证' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' })));
  const post = adminRequest.mock.calls.find(([call]) => call.method === 'POST')[0];
  expect(JSON.parse(post.body).test_cost).toMatchObject({ supplier_bill_id: 'supplier-request-bill', input_tokens: null, output_tokens: null });
});

it.each([undefined, 'supplier-request-bill'])('appends a supplier bill revision from %s and preserves credit and cash display', async (supplierBillID) => {
  adminRequest.mockResolvedValue({ ...report, test_costs: [{ fact_id: 7, request_id: 'request-bill', attempt: 1, channel_id: 2, supplier_bill_id: supplierBillID, amount: '2', refund: '0', currency: 'XCLOUD_CREDIT', rate: '0.5', rate_evidence: 'paid 100 for 200', status: 'confirmed', evidence_type: 'supplier_statement', evidence: 'supplier receipt', coverage: 'unknown', net_credit: '2.00000000', net_usdt: '1.000000' }] });
  render(<OverviewPage canWrite />);
  expect(await screen.findByText('2.00000000 Xcloud 额度')).toBeVisible();
  expect(screen.getByText('1.000000 USDT')).toBeVisible();
  if (supplierBillID) expect(screen.getByText(supplierBillID)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: '修订request-bill测试费用' }));
  expect(screen.getByLabelText('供应商账单 ID（可选）')).toHaveValue(supplierBillID || '');
  fireEvent.change(screen.getByLabelText('供应商账单 ID（可选）'), { target: { value: 'supplier-request-bill' } });
  fireEvent.click(screen.getByRole('button', { name: '保存凭证' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' })));
  const post = adminRequest.mock.calls.find(([call]) => call.method === 'POST')[0];
  expect(JSON.parse(post.body)).toMatchObject({ previous_id: 7, test_cost: { supplier_bill_id: 'supplier-request-bill', rate: '0.5', rate_evidence: 'paid 100 for 200', input_tokens: null, output_tokens: null } });
});

it('shows missing test cost cash as pending and preserves an explicit zero', async () => {
  adminRequest.mockResolvedValue({ ...report, test_costs: [{ fact_id: 7, request_id: 'unknown-cash', currency: 'XCLOUD_CREDIT', net_credit: '2.00000000', net_usdt: '' }, { fact_id: 8, request_id: 'zero-cash', currency: 'USDT', amount: '0', net_usdt: '0.000000' }] });
  render(<OverviewPage canWrite={false} />);
  expect(await screen.findByText('2.00000000 Xcloud 额度')).toBeVisible();
  expect(screen.getByText('折合待核对')).toBeVisible();
  expect(screen.getByText('0.000000 USDT')).toBeVisible();
  expect(screen.queryByText('undefined USDT')).toBeNull();
  expect(within(screen.getByRole('region', { name: '测试费用' })).getByText(/已确认 待核对/)).toBeVisible();
  expect(within(screen.getByRole('region', { name: '直接上游验收费用' })).getByText(/已折算 待核对/)).toBeVisible();
});
