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
import CustomerServiceCosts from './CustomerServiceCosts.jsx';
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
it('keeps automatic pending support costs unknown and uses stable call identities', () => {
  const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  try {
    render(<CustomerServiceCosts report={{
      support_source_status: 'available',
      customer_service_costs: [
        {call_id:'auto-one', request_id:'auto-one', channel_id:2, status:'unconfirmed', input_tokens:null, output_tokens:null, net_usdt:null},
        {call_id:'auto-two', request_id:'auto-two', channel_id:2, status:'unconfirmed', input_tokens:0, output_tokens:0, input_token_scope:'inclusive', cache_read_tokens:0, net_usdt:''},
      ],
    }} />);
    const table = screen.getByRole('table', {name:'客服 Token 与成本明细'});
    expect(within(table).getAllByText('待确认')).toHaveLength(2);
    expect(within(table).queryByText('估算')).toBeNull();
    expect(within(table).queryByText('0 USDT')).toBeNull();
    expect(within(table).getByText('未提供 / 未提供')).toBeVisible();
    expect(within(table).getByText('0 / 0')).toBeVisible();
    expect(errors.mock.calls.filter(([message]) => /unique.*key|same key/i.test(String(message)))).toHaveLength(0);
  } finally { errors.mockRestore(); }
});
it('shows an unavailable support source instead of implying zero monthly costs', () => {
  render(<CustomerServiceCosts report={{support_source_status:'unavailable', customer_service_costs:[], customer_service_confirmed_total:'0.000000', customer_service_estimate_total:'0.000000'}} />);
  expect(screen.getByRole('alert')).toHaveTextContent('客服来源不可用');
  expect(screen.getByRole('alert')).toHaveTextContent('利润待核对');
  expect(screen.queryByText(/本月暂无客服费用记录/)).toBeNull();
  expect(screen.queryByText(/已确认 0\.000000/)).toBeNull();
});
it('renders automatic quoted credit as data rather than claiming an actual debit', () => {
  render(<CustomerServiceCosts report={{customer_service_costs:[{call_id:'quote-one', request_id:'quote-one', status:'estimate', currency:'USDT', net_credit:'0.06000000', net_usdt:'0.030000'}]}} />);
  expect(screen.getByText('0.06000000 Xcloud 额度（报价口径）')).toBeVisible();
  expect(screen.getByText('0.030000 USDT')).toBeVisible();
});
it('shows customer service tokens separately without recomputing backend profit', async () => {
  adminRequest.mockResolvedValue({
    ...report, confirmed: true, actual_cost: '15.000000', gross_profit: '5.000000', margin: '25.00',
    customer_service_confirmed_total: '2.000000', customer_service_estimate_total: '0.030000',
    customer_service_xcloud_credit_total: '4.00000000',
    customer_service_costs: [{fact_id:101, purpose:'customer_service', request_id:'support-one', model:'zt-deepseek-v4-flash', channel_id:4, input_tokens:100, output_tokens:0, input_token_scope:'inclusive', cache_read_tokens:20, cache_write_5m_tokens:0, cache_write_1h_tokens:null, status:'confirmed', coverage:'included', currency:'XCLOUD_CREDIT', net_credit:'4.00000000', net_usdt:'2.000000'}],
  });
  render(<OverviewPage canWrite={false} />);
  const section = await screen.findByRole('region', {name:'客服消耗成本'});
  expect(within(section).getByText('support-one')).toBeVisible();
  expect(within(section).getByText('100 / 0')).toBeVisible();
  expect(within(section).getByText('20 / 0 / 未提供')).toBeVisible();
  expect(within(section).getByText('含缓存')).toBeVisible();
  expect(within(section).getByText('账单内')).toBeVisible();
  expect(screen.getByText('5.000000')).toBeVisible();
  expect(within(section).queryByRole('button')).toBeNull();
});
it('does not turn a missing old customer-service report into a zero cost', async () => {
  render(<OverviewPage canWrite={false} />);
  const section = await screen.findByRole('region', {name:'客服消耗成本'});
  expect(within(section).getByText('当前报表未提供客服费用分类。')).toBeVisible();
  expect(within(section).queryByText(/已确认 0\.000000/)).toBeNull();
});
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

it.each([false, true])('shows supplier-neutral Yunxin image estimates with Xcloud rows: %s', async (includeXcloud) => {
  const data = {
    ...report,
    estimate_complete: true,
    estimated_cost: includeXcloud ? '0.173456' : '0.123456',
    estimated_gross_profit: includeXcloud ? '0.326544' : '0.176544',
    estimates: [{
      channel_id: 1,
      supplier: 'Yunxin enterprise',
      source_model: 'gpt-image-2',
      requests: 1,
      dimensions: { text_input: 100, image_input: 200, image_output: 300 },
      revenue_usdt: '0.300000',
      cost_usdt: '0.123456',
      gross_profit_usdt: '0.176544',
      quote_discount: '',
      is_estimate: true,
      complete: true,
    }, ...(includeXcloud ? [{
      channel_id: 2,
      supplier: 'Xcloud text channel',
      source_model: 'gpt-5.4',
      requests: 2,
      dimensions: { text_input: 1000, text_output: 200 },
      revenue_usdt: '0.200000',
      cost_usdt: '0.050000',
      gross_profit_usdt: '0.150000',
      quote_discount: '0.5',
      is_estimate: true,
      complete: true,
    }] : [])],
  };
  const before = JSON.stringify(data);
  adminRequest.mockResolvedValue(data);
  render(<OverviewPage canWrite={false} />);
  const region = await screen.findByRole('region', { name: '采购报价估算' });
  expect(within(region).getByRole('heading', { name: '采购报价估算' })).toBeVisible();
  expect(within(region).getByRole('columnheader', { name: '采购报价成本' })).toBeVisible();
  expect(region).toHaveTextContent('对应渠道的采购报价');
  expect(region).toHaveTextContent('冻结采购单价与原生 Token 分桶');
  expect(region).toHaveTextContent('不按对客图片张数');
  expect(region).toHaveTextContent('缓存 Token 不重复计入');
  expect(region).toHaveTextContent('不重复应用折扣');
  expect(region).toHaveTextContent('对应上游账单凭证');
  expect(region).toHaveTextContent('估算不代表上游实际扣费');
  expect(region).not.toHaveTextContent(/Xcloud\s*(公开报价|渠道报价估算|报价成本|账单凭证)|公开报价\s*[×*]|约定\s*5\s*折|0\.5/);
  const image = within(region).getByRole('row', { name: /gpt-image-2/ });
  expect(within(image).getByText('Yunxin enterprise')).toBeVisible();
  expect(within(image).getAllByRole('cell')[1]).toHaveTextContent(/^1$/);
  for (const amount of ['0.300000 USDT', '0.123456 USDT', '0.176544 USDT']) {
    expect(within(image).getByText(amount)).toBeVisible();
  }
  expect(within(image).getByText('采购报价可计算')).toBeVisible();
  if (includeXcloud) {
    const text = within(region).getByRole('row', { name: /gpt-5.4/ });
    expect(within(text).getByText('Xcloud text channel')).toBeVisible();
    expect(within(text).getByText('0.050000 USDT')).toBeVisible();
    expect(within(text).getByText('0.150000 USDT')).toBeVisible();
  }
  expect(within(region).getByText(data.estimated_cost)).toBeVisible();
  expect(within(region).getByText(data.estimated_gross_profit)).toBeVisible();
  expect(screen.getByText('毛利待核对')).toBeVisible();
  expect(JSON.stringify(data)).toBe(before);
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

const hostedTool = {
  tool: 'search',
  public_model: 'ztapi-tool-search',
  channel_id: 11,
  unit: 'call',
  successful_calls: 7,
  zero_charge_calls: 3,
  unknown_calls: 2,
  test_successful_calls: 2,
  test_unknown_calls: 1,
  customer_revenue_usdt: '0.004321',
  customer_known_revenue_usdt: '0.004321',
  customer_quoted_sale_cny: '0.050000',
  quoted_cost_cny: '0.035000',
  quoted_cost_usdt: '0.007000',
  test_quoted_cost_cny: '0.010000',
  test_quoted_cost_usdt: '0.002000',
  test_confirmed_cost_usdt: '0.002500',
  test_estimate_cost_usdt: '0.000500',
  quoted_cost_is_estimate: true,
  quote_estimate_complete: false,
};

it('shows hosted tool calls, cash revenue, quotes and confirmed test costs separately', async () => {
  adminRequest.mockResolvedValue({
    ...report,
    hosted_tools: [hostedTool, {
      ...hostedTool,
      tool: 'fetch',
      public_model: 'ztapi-tool-fetch',
      successful_calls: 0,
      zero_charge_calls: 1,
      unknown_calls: 0,
      test_successful_calls: 0,
      test_unknown_calls: 0,
      customer_revenue_usdt: '0.000000',
      customer_quoted_sale_cny: '0.000000',
      quoted_cost_cny: '0.000000',
      quoted_cost_usdt: '0.000000',
      test_quoted_cost_cny: '0.000000',
      test_quoted_cost_usdt: '0.000000',
      test_confirmed_cost_usdt: '0.000000',
      test_estimate_cost_usdt: '0.000000',
      quote_estimate_complete: true,
    }],
  });
  render(<OverviewPage canWrite={false} />);
  const region = await screen.findByRole('region', { name: '托管工具费用' });
  const table = within(region).getByRole('table', { name: '托管工具费用明细' });
  expect(table.parentElement).toHaveClass('finance-table-wrap');
  expect(within(table).getByRole('columnheader', { name: '成功（次）' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '对客零扣费（次）' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '未知（次）' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '其中测试：成功 / 未知（次）' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '工具报价估算' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '测试已确认成本' })).toBeVisible();
  expect(within(table).getByRole('columnheader', { name: '测试成本估算' })).toBeVisible();
  const search = within(table).getByRole('row', { name: /ztapi-tool-search/ });
  const counts = within(search).getAllByRole('cell');
  expect(counts[2]).toHaveTextContent(/^7$/);
  expect(counts[3]).toHaveTextContent(/^3$/);
  expect(counts[4]).toHaveTextContent(/^2$/);
  expect(counts[5]).toHaveTextContent(/^2 \/ 1$/);
  expect(within(search).getByText('按次（call）')).toBeVisible();
  for (const value of ['0.004321 USDT', '收费报价 0.050000 CNY', '0.035000 CNY', '0.007000 USDT', '0.010000 CNY', '0.002000 USDT', '0.002500 USDT', '0.000500 USDT']) {
    expect(within(search).getByText(value)).toBeVisible();
  }
  expect(within(search).getByText('估算待核对')).toBeVisible();
  const fetch = within(table).getByRole('row', { name: /ztapi-tool-fetch/ });
  expect(within(fetch).getAllByRole('cell')[2]).toHaveTextContent(/^0$/);
  expect(within(fetch).getByText('估算完整')).toBeVisible();
  expect(within(region).getByText(/报价仅为估算，不代表供应商实扣/)).toBeVisible();
  expect(within(region).getByText(/对客零扣费不代表供应商零成本/)).toBeVisible();
  expect(within(region).getByText(/账单内仅拆分展示，不重复扣除/)).toBeVisible();
  expect(within(region).queryByText(/token/i)).toBeNull();
  expect(within(region).queryByRole('button')).toBeNull();
});

it.each([undefined, null, []])('supports legacy hosted tool data %s without implying missing data is zero', async (tools) => {
  adminRequest.mockResolvedValue({ ...report, hosted_tools: tools });
  render(<OverviewPage canWrite={false} />);
  const region = await screen.findByRole('region', { name: '托管工具费用' });
  expect(within(region).getByText(Array.isArray(tools)
    ? '本月暂无托管工具记录。'
    : '旧版报表未提供工具明细。')).toBeVisible();
  expect(screen.getByText('毛利待核对')).toBeVisible();
  expect(screen.queryByRole('alert')).toBeNull();
  expect(adminRequest).toHaveBeenCalledTimes(1);
});

it.each(['5.000000', '0.000000', null])('keeps backend gross profit %s with hosted tool costs already covered', async (grossProfit) => {
  const data = {
    ...report,
    confirmed: grossProfit !== null,
    actual_cost: '15.000000',
    gross_profit: grossProfit,
    hosted_tools: [hostedTool],
    test_confirmed_total: '2.000000',
    test_estimate_total: '1.000000',
    test_costs: [{ fact_id: 1, request_id: 'tool-test', status: 'confirmed', coverage: 'included', net_usdt: '2.000000' }],
  };
  const before = JSON.stringify(data);
  adminRequest.mockResolvedValue(data);
  render(<OverviewPage />);
  await screen.findByRole('region', { name: '托管工具费用' });
  const metric = screen.getByText('毛利', { selector: 'dt' }).parentElement;
  expect(within(metric).getByRole('definition')).toHaveTextContent(grossProfit ?? '—');
  const actual = screen.getByText('上游实际消耗', { selector: 'dt' }).parentElement;
  expect(within(actual).getByRole('definition')).toHaveTextContent('15.000000');
  expect(within(screen.getByRole('region', { name: '测试费用' })).getByText('账单内')).toBeVisible();
  expect(JSON.stringify(data)).toBe(before);
  expect(adminRequest.mock.calls.every(([call]) => !call.method || call.method === 'GET')).toBe(true);
});

it('keeps unknown hosted tool money pending while preserving confirmed zero amounts', async () => {
  adminRequest.mockResolvedValue({ ...report, hosted_tools: [{
    ...hostedTool,
    unknown_calls: undefined,
    customer_revenue_usdt: null,
    customer_known_revenue_usdt: '0.004321',
    quoted_cost_cny: '',
    quoted_cost_usdt: null,
    test_confirmed_cost_usdt: '0.000000',
    test_estimate_cost_usdt: undefined,
  }] });
  render(<OverviewPage />);
  const region = await screen.findByRole('region', { name: '托管工具费用' });
  const row = within(region).getByRole('row', { name: /ztapi-tool-search/ });
  const cells = within(row).getAllByRole('cell');
  expect(cells[4]).toHaveTextContent(/^待核对$/);
  expect(cells[6]).toHaveTextContent('待核对');
  expect(within(cells[6]).getByText('已知部分 0.004321 USDT')).toBeVisible();
  expect(cells[7]).not.toHaveTextContent('0.000000');
  expect(within(row).getByText('0.000000 USDT')).toBeVisible();
  expect(cells[10]).toHaveTextContent(/^待核对$/);
  expect(region).not.toHaveTextContent(/undefined|null|NaN/);
});

it('removes stale hosted tool rows when another monthly report fails to load', async () => {
  adminRequest.mockResolvedValue({ ...report, hosted_tools: [hostedTool] });
  render(<OverviewPage />);
  expect(await screen.findByText('ztapi-tool-search')).toBeVisible();
  adminRequest.mockRejectedValueOnce(new Error('offline'));
  fireEvent.click(screen.getByRole('button', { name: '刷新财务数据' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('财务数据加载失败');
  expect(screen.queryByRole('region', { name: '托管工具费用' })).toBeNull();
  expect(screen.queryByText('ztapi-tool-search')).toBeNull();
});

it('preserves numeric zero in known hosted tool cash without confirming unknown total revenue', async () => {
  adminRequest.mockResolvedValue({ ...report, hosted_tools: [{
    ...hostedTool,
    customer_revenue_usdt: null,
    customer_known_revenue_usdt: 0,
    quoted_cost_cny: 0,
    quoted_cost_usdt: 0,
  }] });
  render(<OverviewPage />);
  const region = await screen.findByRole('region', { name: '托管工具费用' });
  const row = within(region).getByRole('row', { name: /ztapi-tool-search/ });
  const cells = within(row).getAllByRole('cell');
  expect(within(cells[6]).getByText('已知部分 0 USDT')).toBeVisible();
  expect(within(cells[6]).getByText('待核对')).toBeVisible();
  expect(within(cells[7]).getByText('0 CNY')).toBeVisible();
  expect(within(cells[7]).getByText('0 USDT')).toBeVisible();
});

it.each([undefined, null, '', -1, 1.5, '0', Number.NaN])(
  'does not turn invalid hosted call counts %s into confirmed zeroes',
  async (count) => {
    adminRequest.mockResolvedValue({
      ...report,
      hosted_tools: [{
        ...hostedTool,
        successful_calls: count,
        zero_charge_calls: count,
        unknown_calls: count,
        test_successful_calls: count,
        test_unknown_calls: count,
        quote_estimate_complete: undefined,
      }],
    });
    render(<OverviewPage />);
    const region = await screen.findByRole('region', { name: '托管工具费用' });
    const row = within(region).getByRole('row', { name: /ztapi-tool-search/ });
    const cells = within(row).getAllByRole('cell');
    for (const index of [2, 3, 4]) {
      expect(cells[index]).toHaveTextContent(/^待核对$/);
    }
    expect(cells[5]).toHaveTextContent(/^待核对 \/ 待核对$/);
    expect(within(row).getByText('估算待核对')).toBeVisible();
    expect(region).not.toHaveTextContent(/undefined|null|NaN/);
  },
);

it('preserves explicit zero in every hosted call count', async () => {
  adminRequest.mockResolvedValue({
    ...report,
    hosted_tools: [{
      ...hostedTool,
      successful_calls: 0,
      zero_charge_calls: 0,
      unknown_calls: 0,
      test_successful_calls: 0,
      test_unknown_calls: 0,
    }],
  });
  render(<OverviewPage />);
  const region = await screen.findByRole('region', { name: '托管工具费用' });
  const row = within(region).getByRole('row', { name: /ztapi-tool-search/ });
  const cells = within(row).getAllByRole('cell');
  for (const index of [2, 3, 4]) {
    expect(cells[index]).toHaveTextContent(/^0$/);
  }
  expect(cells[5]).toHaveTextContent(/^0 \/ 0$/);
});

it.each([
  { coverage: 'included', label: '账单内', cost: '15.000000', profit: '5.000000' },
  { coverage: 'extra', label: '账单外额外费用', cost: '17.000000', profit: '3.000000' },
  { coverage: 'unknown', label: '覆盖未知', cost: null, profit: null },
])('uses backend totals without adding hosted quotes or test subsets for $coverage coverage', async ({ coverage, label, cost, profit }) => {
  adminRequest.mockResolvedValue({
    ...report,
    confirmed: profit !== null,
    actual_cost: cost,
    gross_profit: profit,
    hosted_tools: [{ ...hostedTool, test_confirmed_cost_usdt: '2.000000' }],
    test_confirmed_total: '2.000000',
    test_estimate_total: '1.000000',
    test_costs: [{
      fact_id: 91,
      request_id: 'hosted-test-91',
      attempt: 1,
      channel_id: 11,
      model: 'ztapi-tool-search',
      status: 'confirmed',
      coverage,
      net_usdt: '2.000000',
    }],
  });
  render(<OverviewPage canWrite={false} />);
  const hosted = await screen.findByRole('region', { name: '托管工具费用' });
  const tests = screen.getByRole('region', { name: '测试费用' });
  expect(within(hosted).getByText('2.000000 USDT')).toBeVisible();
  expect(within(tests).getByText('2.000000 USDT')).toBeVisible();
  expect(within(tests).getByText(label)).toBeVisible();
  expect(within(tests).getByText(/已确认 2\.000000 USDT · 估算 1\.000000 USDT/)).toBeVisible();
  for (const [title, expected] of [
    ['消费对应实收收入', '20.000000'],
    ['上游实际消耗', cost],
    ['毛利', profit],
  ]) {
    const metric = screen.getByText(title, { selector: 'dt' }).parentElement;
    expect(within(metric).getByRole('definition').textContent).toBe(expected ?? '—');
  }
  expect(adminRequest).toHaveBeenCalledTimes(1);
  expect(adminRequest).toHaveBeenCalledWith({
    url: expect.stringMatching(/^\/api\/admin\/finance-reporting\/report\?month=/),
  });
});
