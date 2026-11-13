import React from 'react';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { adminRequest } from '../../auth/admin-session.js';
import ReferralWithdrawalsPage from './ReferralWithdrawalsPage.jsx';

vi.mock('../../auth/admin-session.js', () => ({ adminRequest: vi.fn() }));
const item = { id: 12, version: 3, amount: '20.000001', fee: '0.123456', net_amount: '19.876545', address: 'TWalletOriginalAddress', status: 'requested', created_at: 1794000000 };
const data = (row = item) => ({ campaign: { enabled: false, accepting_invites: false, blocked_reasons: ['supplier_cost_unknown'] }, summary: {}, withdrawals: { items: row ? [row] : [], total: row ? 1 : 0 }, commissions: { items: [], total: 0 } });
beforeEach(() => {
  adminRequest.mockReset().mockImplementation(({ url }) => Promise.resolve(url.endsWith('/readiness') ? { blocked_reasons: ['worker_disabled'], schema: 'unknown' } : data()));
});

it('defaults to read only, displays original amounts, wallet and real readiness reasons', async () => {
  render(<ReferralWithdrawalsPage />);
  expect(await screen.findByText('TWalletOriginalAddress')).toBeVisible();
  for (const value of ['20.000001 USDT', '0.123456 USDT', '19.876545 USDT']) expect(screen.getByText(value)).toBeVisible();
  expect(screen.getByText('上游成本尚未核实')).toBeVisible();
  expect(screen.getByText('Supplier cost is unverified')).toBeVisible();
  expect(screen.getByText('归集任务未启用')).toBeVisible();
  expect(screen.queryByText('supplier_cost_unknown')).toBeNull();
  expect(screen.queryByRole('button', { name: '手续费报价 #12' })).toBeNull();
  expect(screen.queryByRole('button', { name: /开启|启用活动/ })).toBeNull();
  expect(adminRequest.mock.calls.every(([r]) => !r.method)).toBe(true);
});

it('quotes an exact string with version, never confirms for the user', async () => {
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '手续费报价 #12' }));
  fireEvent.change(screen.getByLabelText('手续费（USDT）'), { target: { value: '0.000001' } });
  fireEvent.click(screen.getByRole('button', { name: '提交报价' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' })));
  const post = adminRequest.mock.calls.find(([r]) => r.method === 'POST')[0];
  expect(post.url).toBe('/api/admin/referrals/withdrawals/12/quote');
  expect(JSON.parse(post.body)).toEqual({ version: 3, fee: '0.000001', note: '' });
  expect(adminRequest.mock.calls.some(([r]) => /\/confirm$/.test(r.url))).toBe(false);
});

it.each(['20.000001', '20.000002', '1e-6', '-1', '0.0000001'])('rejects invalid or gross-exceeding fee %s without POST', async (fee) => {
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '手续费报价 #12' }));
  fireEvent.change(screen.getByLabelText('手续费（USDT）'), { target: { value: fee } });
  fireEvent.click(screen.getByRole('button', { name: '提交报价' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('手续费');
  expect(adminRequest.mock.calls.some(([r]) => r.method === 'POST')).toBe(false);
});

it.each(['requested', 'fee_quoted', 'confirmed', 'paying', 'submitted_unknown', 'paid', 'manual_review', 'rejected', 'cancelled'])('gates action buttons by server status %s', async (status) => {
  adminRequest.mockResolvedValue(data({ ...item, status }));
  render(<ReferralWithdrawalsPage canWrite />);
  await screen.findByText('TWalletOriginalAddress');
  expect(!!screen.queryByRole('button', { name: '独占领取 #12' })).toBe(status === 'confirmed');
  expect(!!screen.queryByRole('button', { name: '手续费报价 #12' })).toBe(['requested', 'fee_quoted', 'confirmed'].includes(status));
  expect(!!screen.queryByRole('button', { name: '服务端核验付款 #12' })).toBe(['paying', 'submitted_unknown'].includes(status));
  expect(!!screen.queryByRole('button', { name: '拒绝 #12' })).toBe(['requested', 'fee_quoted', 'confirmed', 'manual_review'].includes(status));
});

it('rejects an unclaimed manual review with the server version and a refund reconciliation reason', async () => {
  let row = { ...item, status: 'manual_review' };
  adminRequest.mockImplementation(({ url, method }) => {
    if (method === 'POST') { row = { ...row, status: 'rejected', version: 4 }; return Promise.resolve({}); }
    return Promise.resolve(url.endsWith('/readiness') ? {} : data(row));
  });
  render(<ReferralWithdrawalsPage canWrite />);
  const reject = await screen.findByRole('button', { name: '拒绝 #12' });
  expect(screen.getByRole('note')).toHaveTextContent('先人工核对退款及剩余有效佣金');
  expect(screen.queryByRole('button', { name: '手续费报价 #12' })).toBeNull();
  expect(screen.queryByRole('button', { name: '独占领取 #12' })).toBeNull();
  fireEvent.click(reject);
  expect(within(screen.getByRole('dialog')).getByRole('note')).toHaveTextContent('核对后可拒绝释放');
  fireEvent.change(screen.getByLabelText('拒绝理由'), { target: { value: '已人工核对退款，释放剩余有效佣金' } });
  fireEvent.click(screen.getByRole('button', { name: '确认拒绝' }));
  expect(await screen.findByRole('cell', { name: '已拒绝', exact: true })).toBeVisible();
  const posts = adminRequest.mock.calls.filter(([request]) => request.method === 'POST');
  expect(posts).toHaveLength(1);
  expect(posts[0][0].url).toBe('/api/admin/referrals/withdrawals/12/reject');
  expect(JSON.parse(posts[0][0].body)).toEqual({ version: 3, note: '已人工核对退款，释放剩余有效佣金' });
});

it('keeps manual-review rejection unavailable without referral write permission', async () => {
  adminRequest.mockResolvedValue(data({ ...item, status: 'manual_review' }));
  render(<ReferralWithdrawalsPage />);
  await screen.findByText('TWalletOriginalAddress');
  expect(screen.getByRole('note')).toHaveTextContent('先人工核对退款');
  expect(screen.queryByRole('button', { name: '拒绝 #12' })).toBeNull();
  expect(adminRequest.mock.calls.some(([request]) => request.method === 'POST')).toBe(false);
});

it('refreshes on a two-administrator claim conflict and discards the stale action', async () => {
  let row = { ...item, status: 'confirmed' };
  adminRequest.mockImplementation(({ url, method }) => {
    if (method === 'POST') { row = { ...row, status: 'paying', version: 4 }; return Promise.reject({ status: 409 }); }
    return Promise.resolve(url.endsWith('/readiness') ? {} : data(row));
  });
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '独占领取 #12' }));
  fireEvent.click(screen.getByRole('button', { name: '确认领取' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('冲突');
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  expect(screen.queryByRole('button', { name: '独占领取 #12' })).toBeNull();
  const call = adminRequest.mock.calls.find(([r]) => r.method === 'POST')[0];
  expect(JSON.parse(call.body)).toEqual({ version: 3 });
});

it('never marks a hash as paid and keeps submitted_unknown after server verification is pending', async () => {
  adminRequest.mockImplementation(({ url, method }) => method === 'POST' ? Promise.reject({ status: 409 }) : Promise.resolve(url.endsWith('/readiness') ? {} : data({ ...item, status: 'submitted_unknown' })));
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '服务端核验付款 #12' }));
  fireEvent.change(screen.getByLabelText('交易哈希'), { target: { value: 'a'.repeat(64) } });
  fireEvent.click(screen.getByRole('button', { name: '请求只读核验' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('保持占用');
  expect(within(await screen.findByRole('table', { name: '提现记录' })).getByText('已提交，结果未知')).toBeVisible();
  expect(within(screen.getByRole('table', { name: '提现记录' })).queryByText('已付款')).toBeNull();
  const call = adminRequest.mock.calls.find(([r]) => r.method === 'POST')[0];
  expect(call.url.endsWith('/confirm-paid')).toBe(true);
  expect(JSON.parse(call.body)).toEqual({ version: 3, tx_id: 'a'.repeat(64) });
});

it('uses server filtered pagination and clears stale rows on failure', async () => {
  adminRequest.mockResolvedValue({ ...data(), withdrawals: { items: [item], total: 42 } });
  render(<ReferralWithdrawalsPage />);
  await screen.findByText('TWalletOriginalAddress');
  fireEvent.click(screen.getByRole('button', { name: '下一页' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith({ url: '/api/admin/referrals?status=&page=2&page_size=20' }));
  fireEvent.change(screen.getByLabelText('提现状态'), { target: { value: 'submitted_unknown' } });
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith({ url: '/api/admin/referrals?status=submitted_unknown&page=1&page_size=20' }));
  adminRequest.mockRejectedValue(new Error('offline'));
  fireEvent.click(screen.getByRole('button', { name: '刷新推广数据' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('加载失败');
  expect(screen.queryByText('TWalletOriginalAddress')).toBeNull();
});

it('shows empty data without inventing a campaign enabled state', async () => {
  adminRequest.mockResolvedValue(data(null));
  render(<ReferralWithdrawalsPage />);
  expect(await screen.findByText('暂无提现记录')).toBeVisible();
  expect(screen.getByText('活动已关闭')).toBeVisible();
});

it('uses the current backend readiness flags without inventing unknown values', async () => {
  adminRequest.mockImplementation(({ url }) => Promise.resolve(url.endsWith('/readiness') ? { schema_ready: true, worker_ready: false, finance_ready: true, price_gate: { allowed: false, blocked_reasons: ['cost_unverified'] } } : data(null)));
  render(<ReferralWithdrawalsPage />);
  await screen.findByText('暂无提现记录');
  for (const [label, value] of [['账本结构', '通过'], ['归集任务', '未通过'], ['财务能力', '通过'], ['报价准入', '未通过']]) {
    expect(screen.getByText(label).parentElement).toHaveTextContent(value);
  }
  expect(screen.getByText('上游成本尚未核实')).toBeVisible();
});

it('canonicalizes fee strings to six places without floating point', async () => {
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '手续费报价 #12' }));
  fireEvent.change(screen.getByLabelText('手续费（USDT）'), { target: { value: '1.2' } });
  fireEvent.click(screen.getByRole('button', { name: '提交报价' }));
  await waitFor(() => expect(adminRequest).toHaveBeenCalledWith(expect.objectContaining({ method: 'POST' })));
  expect(JSON.parse(adminRequest.mock.calls.find(([r]) => r.method === 'POST')[0].body).fee).toBe('1.200000');
});

it('allows an expired confirmed quote to be requoted, requiring fresh user confirmation', async () => {
  adminRequest.mockImplementation(({ method }) => Promise.resolve(method === 'POST' ? {} : data({ ...item, status: 'confirmed', fee_expires_at: 1 })));
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '手续费报价 #12' }));
  expect(screen.getByRole('dialog')).toHaveTextContent('手续费变化须重新确认');
});

it('requires a reason when quoting an explicitly zero fee', async () => {
  render(<ReferralWithdrawalsPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '手续费报价 #12' }));
  fireEvent.change(screen.getByLabelText('手续费（USDT）'), { target: { value: '0' } });
  fireEvent.click(screen.getByRole('button', { name: '提交报价' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('零手续费');
  expect(adminRequest.mock.calls.some(([r]) => r.method === 'POST')).toBe(false);
});

it('reads the nested readiness response and shows safe bilingual worker errors', async () => {
  const privateCode = 'supplier/account/channel/model/'.repeat(25) + ':evidence_unknown';
  adminRequest.mockImplementation(({ url }) => Promise.resolve(url.endsWith('/readiness') ? {
    readiness: { ready: false, schema_ready: true, finance_ready: false, worker_ready: false, price_gate: { allowed: false, blocked_reasons: [privateCode] }, blocked_reasons: ['worker_not_ready', 'unrecognized_private_code_' + 'x'.repeat(100)] },
    catalog_sha256: 'a'.repeat(64), required_dimension_keys: [privateCode],
    worker: { enabled: true, running: false, last_heartbeat: 1794000000, last_success: 0, last_error: 'reconciliation_unavailable' },
  } : data(null)));
  render(<ReferralWithdrawalsPage />);
  await screen.findByText('暂无提现记录');
  expect(screen.getByText('账本结构').parentElement).toHaveTextContent('通过');
  expect(screen.getByText('财务能力').parentElement).toHaveTextContent('未通过');
  expect(screen.getByText('计费维度缺少核验凭据')).toBeVisible();
  expect(screen.getByText('Billing dimension evidence is unverified')).toBeVisible();
  expect(screen.getByText('归集暂不可用，已入账凭据将保留待重试')).toBeVisible();
  expect(screen.queryByText(privateCode)).toBeNull();
  expect(screen.queryByText(/unrecognized_private_code/)).toBeNull();
  expect(screen.getByText('任务开关').parentElement).toHaveTextContent('已启用');
  expect(screen.getByText('任务进程').parentElement).toHaveTextContent('未运行');
});

it('keeps administrator summary amounts as exact strings', async () => {
  adminRequest.mockResolvedValue({ ...data(null), summary: { total_commission: '9007199254.740993', frozen_commission: '0.000000', available_commission: '20.000001', withdrawal_reserved: '40.000000', paid_commission: '60.000000', manual_loss: '1.000001' } });
  render(<ReferralWithdrawalsPage />);
  const summary = await screen.findByRole('region', { name: '推广汇总' });
  expect(within(summary).getByText('9007199254.740993 USDT')).toBeVisible();
  expect(within(summary).getByText('0.000000 USDT')).toBeVisible();
  expect(within(summary).getByText('1.000001 USDT')).toBeVisible();
});
