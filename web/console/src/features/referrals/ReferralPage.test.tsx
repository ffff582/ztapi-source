import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { LocaleProvider } from '../../i18n/locale';
import fixture from './referral.fixture.json';
import { ReferralPage } from './ReferralPage';

const address = 'TJRabPrwbZy45sbavfcjinPJC18kjpRTv8';
const quotation = { id: 9, version: 2, amount: '40.000000', fee: '1.250000', net_amount: '38.750000', address, status: 'fee_quoted', created_at: fixture.server_time, fee_expires_at: '2026-11-08T08:30:00Z', tx_id: '', note: '' };
let overview: typeof fixture;
let withdrawals: typeof quotation[];
let readError: boolean;
let mutationStatus: number;
let posts: { path: string; body: Record<string, unknown> }[];
let pages: string[];
const respond = (data: unknown) => new Response(JSON.stringify({ success: true, data }), { status: 200 });
const mount = () => render(<LocaleProvider><ReferralPage /></LocaleProvider>);

beforeEach(() => {
  overview = structuredClone(fixture);
  withdrawals = [];
  readError = false;
  mutationStatus = 200;
  posts = [];
  pages = [];
  setAuthSession({ access_token: 'offline', expires_in: 900, user: { id: 7, username: 'offline', role: 1, group: 'default' } });
  vi.stubGlobal('fetch', vi.fn(async (url: string, init: RequestInit) => {
    if (init.method === 'POST') {
      posts.push({ path: url, body: JSON.parse(String(init.body)) });
      if (mutationStatus !== 200) return new Response(JSON.stringify({ success: false, code: mutationStatus === 409 ? 'version_conflict' : 'reauthentication_required', message: 'PRIVATE SECRET' }), { status: mutationStatus });
      if (url.endsWith('/confirm')) withdrawals = [{ ...quotation, status: 'confirmed', version: 3 }];
      else if (url.endsWith('/cancel')) withdrawals = [{ ...quotation, status: 'cancelled', version: 3 }];
      else withdrawals = [{ ...quotation, status: 'requested', version: 1, fee: '0.000000', net_amount: '40.000000', fee_expires_at: '' }];
      return respond(withdrawals[0]);
    }
    if (readError) return new Response('{}', { status: 503 });
    if (url === '/api/referrals/self') return respond(overview);
    if (url.includes('/commissions?')) {
      pages.push(url);
      return respond({ total: 21, items: [{ id: url.includes('page=2') ? 21 : 1, invitee_label: url.includes('page=2') ? '好友 B' : '好友 A', principal: '100.000000', commission: '5.000000', reversed: '0.000000', earned_at: fixture.server_time, available_at: '2026-11-15T08:00:00Z', status: 'frozen' }] });
    }
    if (url.includes('/withdrawals?')) return respond({ total: withdrawals.length, items: withdrawals });
    throw new Error(`Unexpected request ${url}`);
  }));
});

afterEach(() => { cleanup(); clearAuthSession(); vi.unstubAllGlobals(); sessionStorage.clear(); });

async function fillWithdrawal(amount = '20.000001') {
  await screen.findByText('150.123456');
  fireEvent.change(screen.getByLabelText('提现金额 (USDT)'), { target: { value: amount } });
  fireEvent.change(screen.getByLabelText('TRC20 收款地址'), { target: { value: address } });
  fireEvent.change(screen.getByLabelText('当前账号密码'), { target: { value: 'in-memory-only' } });
}

describe('user referral page', () => {
  it('shows exact server balances, qualification and an invite link that can be copied', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    mount();
    expect(await screen.findByRole('heading', { name: '推广返利' })).toBeVisible();
    const summary = await screen.findByRole('region', { name: '佣金概览' });
    for (const label of ['佣金总额', '冻结佣金', '可提现佣金', '提现占用', '累计已付']) expect(within(summary).getByText(label)).toBeVisible();
    expect(within(summary).getByText('50.123456')).toBeVisible();
    expect(screen.getByText(/真实到账充值本金的 5%/)).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '复制邀请链接' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/register?invite=zt-ref-abc`));
    expect(screen.queryByText(/API.*余额.*可提现/)).toBeNull();
  });

  it('copies the canonical server invitation URL when the console is served from an alias', async () => {
    overview.invite_url = 'https://ztapi.vip/register?invite=zt-ref-abc';
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    mount();
    await screen.findByText('150.123456');
    fireEvent.click(screen.getByRole('button', { name: '复制邀请链接' }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('https://ztapi.vip/register?invite=zt-ref-abc'));
  });

  it.each(['disabled', 'readiness'])('keeps the page present but disables new participation when %s', async (mode) => {
    if (mode === 'disabled') overview.campaign.enabled = false;
    else overview.campaign.blocked_reasons = ['成本凭证待核验'] as never[];
    overview.summary.available_commission = '0.000000';
    mount();
    expect(await screen.findByText('推广返利暂未开放')).toBeVisible();
    expect(screen.getByRole('button', { name: '复制邀请链接' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '申请提现' })).toBeDisabled();
    if (mode === 'readiness') expect(screen.getByText('成本凭证待核验')).toBeVisible();
  });

  it.each(['disabled', 'readiness'])('preserves withdrawal access to earned available commissions when %s', async (mode) => {
    if (mode === 'disabled') overview.campaign.enabled = false;
    else overview.campaign.blocked_reasons = ['成本凭证待核验'] as never[];
    mount();
    await fillWithdrawal();
    expect(screen.getByText('推广返利暂未开放')).toBeVisible();
    expect(screen.getByRole('button', { name: '复制邀请链接' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '申请提现' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body.amount).toBe('20.000001');
  });

  it('submits exact six-decimal amounts once and clears the password without persisting credentials', async () => {
    mount();
    await fillWithdrawal();
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0].body).toEqual({ amount: '20.000001', address, password: 'in-memory-only', idempotency_key: expect.any(String) });
    await waitFor(() => expect(screen.getByLabelText('当前账号密码')).toHaveValue(''));
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.getItem('password')).toBeNull();
    expect(await screen.findByText('待手续费报价')).toBeVisible();
  });

  it.each(['19.999999', '20.0000001', '2e1', '50.123457'])('does not post invalid or unavailable amount %s', async (amount) => {
    mount();
    await fillWithdrawal(amount);
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('金额');
    expect(posts).toHaveLength(0);
  });

  it('requires explicit fee acknowledgement and sends only the current server version', async () => {
    withdrawals = [quotation];
    mount();
    const confirm = await screen.findByRole('button', { name: '确认手续费报价' });
    expect(confirm).toBeDisabled();
    expect(screen.getByText('1.250000')).toBeVisible();
    expect(screen.getByText('38.750000')).toBeVisible();
    expect(posts).toHaveLength(0);
    fireEvent.click(screen.getByRole('checkbox', { name: /确认手续费/ }));
    fireEvent.click(confirm);
    await waitFor(() => expect(posts[0]).toEqual({ path: '/api/referrals/self/withdrawals/9/confirm', body: { version: 2 } }));
    expect(await screen.findByText('已确认报价，待人工打款')).toBeVisible();
    expect(screen.queryByText('已付款')).toBeNull();
  });

  it('never enables an expired quotation based on the local clock', async () => {
    withdrawals = [{ ...quotation, fee_expires_at: '2026-11-08T07:59:59Z' }];
    mount();
    expect(await screen.findByText('手续费报价已过期，请刷新等待重新报价。')).toBeVisible();
    expect(screen.getByRole('button', { name: '确认手续费报价' })).toBeDisabled();
  });

  it('does not present a missing manual-review quote as a zero fee or zero net receipt', async () => {
    withdrawals = [{ ...quotation, status: 'manual_review', fee: '0.000000', net_amount: '0.000000', fee_expires_at: '' }];
    mount();
    await screen.findByText('待人工核验');
    expect(screen.getAllByText('待报价')).toHaveLength(2);
    expect(within(screen.getByRole('region', { name: '提现记录' })).queryAllByText('0.000000', { exact: true })).toHaveLength(0);
    expect(screen.getByRole('button', { name: '取消提现' })).toBeEnabled();
    expect(screen.getByRole('note')).toHaveTextContent('先人工核对退款及剩余有效佣金');
    expect(screen.queryByRole('button', { name: '确认手续费报价' })).toBeNull();
  });

  it('refreshes after 409 and discards the old fee acknowledgement', async () => {
    withdrawals = [quotation];
    mutationStatus = 409;
    mount();
    await screen.findByText('38.750000');
    fireEvent.click(screen.getByRole('checkbox', { name: /确认手续费/ }));
    withdrawals = [{ ...quotation, version: 3, fee: '2.000000', net_amount: '38.000000' }];
    fireEvent.click(screen.getByRole('button', { name: '确认手续费报价' }));
    expect(await screen.findByText(/状态或报价已更新/)).toBeVisible();
    expect(await screen.findByText('38.000000')).toBeVisible();
    expect(screen.getByRole('checkbox', { name: /确认手续费/ })).not.toBeChecked();
    expect(screen.getByRole('button', { name: '确认手续费报价' })).toBeDisabled();
    expect(posts).toHaveLength(1);
  });

  it.each(['confirmed', 'manual_review'])('cancels unclaimed %s and never releases paying or unknown-payment reservations', async (status) => {
    withdrawals = [{ ...quotation, id: 8, status: 'paying' }, { ...quotation, status: 'submitted_unknown' }, { ...quotation, id: 10, status }];
    mount();
    expect(await screen.findByText('打款结果待核验，资金继续占用')).toBeVisible();
    expect(screen.getAllByRole('button', { name: '取消提现' })).toHaveLength(1);
    fireEvent.click(screen.getByRole('button', { name: '取消提现' }));
    fireEvent.click(await screen.findByRole('button', { name: '确认取消提现' }));
    await waitFor(() => expect(posts[0]).toEqual({ path: '/api/referrals/self/withdrawals/10/cancel', body: { version: 2 } }));
  });

  it('explains refund reconciliation before a manual-review cancellation in English', async () => {
    localStorage.setItem('ztapi.locale', 'en');
    withdrawals = [{ ...quotation, status: 'manual_review' }];
    mount();
    expect(await screen.findByRole('note')).toHaveTextContent('Reconcile the refund and remaining valid commission before cancelling');
    expect(screen.getByRole('button', { name: 'Cancel withdrawal' })).toBeEnabled();
    expect(screen.queryByRole('button', { name: 'Confirm fee quote' })).toBeNull();
  });

  it('recovers loading errors and paginates commission details', async () => {
    readError = true;
    mount();
    expect(await screen.findByText('推广数据加载失败，请重试。')).toBeVisible();
    expect(screen.queryByText('0.000000')).toBeNull();
    readError = false;
    fireEvent.click(screen.getByRole('button', { name: '刷新推广数据' }));
    await screen.findByText('好友 A');
    fireEvent.click(screen.getByRole('button', { name: '下一页佣金明细' }));
    expect(await screen.findByText('好友 B')).toBeVisible();
    expect(pages.at(-1)).toContain('page=2&page_size=20');
  });

  it('requires controlled reauthentication without exposing raw errors', async () => {
    mutationStatus = 403;
    mount();
    await fillWithdrawal();
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('重新验证');
    expect(screen.queryByText('PRIVATE SECRET')).toBeNull();
    expect(screen.getByLabelText('当前账号密码')).toHaveValue('');
  });

  it('offers optional password-type OTP even when summary has no step-up flags', async () => {
    mount();
    await fillWithdrawal();
    const otp = screen.getByLabelText('二次验证码（选填）');
    expect(otp).toHaveAttribute('type', 'password');
    expect(otp).toHaveAttribute('inputmode', 'numeric');
    fireEvent.change(otp, { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    await waitFor(() => expect(posts[0].body.totp_code).toBe('123456'));
    await waitFor(() => expect(otp).toHaveValue(''));
    expect(sessionStorage.length).toBe(0);
  });

  it('requires OTP when enabled and blocks OAuth accounts without password reauthentication', async () => {
    Object.assign(overview.summary, { requires_totp: true });
    const view = mount();
    await fillWithdrawal();
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('二次验证码');
    expect(posts).toHaveLength(0);
    view.unmount();
    Object.assign(overview.summary, { withdrawal_reauth_available: false });
    mount();
    expect(await screen.findByText('请先在账号安全设置密码，再申请提现。')).toBeVisible();
    expect(screen.getByRole('button', { name: '申请提现' })).toBeDisabled();
  });

  it('renders English labels while keeping exact USDT strings', async () => {
    localStorage.setItem('ztapi.locale', 'en');
    mount();
    expect(await screen.findByRole('heading', { name: 'Referral Rewards' })).toBeVisible();
    expect(await screen.findByText('150.123456')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Request withdrawal' })).toBeVisible();
  });

  it('removes financial data immediately when the authenticated account changes', async () => {
    mount();
    await screen.findByText('150.123456');
    readError = true;
    await act(async () => setAuthSession({ access_token: 'other', expires_in: 900, user: { id: 8, username: 'other', role: 1, group: 'default' } }));
    expect(screen.queryByText('150.123456')).toBeNull();
  });

  it('retries an ambiguous withdrawal only with the same immutable key after explicit reauthentication', async () => {
    mutationStatus = 503;
    mount();
    await fillWithdrawal();
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    await screen.findByRole('button', { name: '重试原提现' });
    await screen.findByText('150.123456');
    const first = posts[0].body;
    expect(screen.getByLabelText('提现金额 (USDT)')).toHaveAttribute('readonly');
    expect(screen.getByLabelText('当前账号密码')).toHaveValue('');
    mutationStatus = 200;
    fireEvent.change(screen.getByLabelText('当前账号密码'), { target: { value: 'verified-again' } });
    fireEvent.click(screen.getByRole('button', { name: '重试原提现' }));
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1].body).toEqual({ ...first, password: 'verified-again' });
    expect(sessionStorage.length).toBe(0);
  });

  it('validates password, TRC20 address and OTP without making a network mutation', async () => {
    mount();
    await fillWithdrawal();
    fireEvent.change(screen.getByLabelText('TRC20 收款地址'), { target: { value: '0x-not-trc20' } });
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('TRC20');
    fireEvent.change(screen.getByLabelText('TRC20 收款地址'), { target: { value: address } });
    fireEvent.change(screen.getByLabelText('当前账号密码'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(screen.getByRole('alert')).toHaveTextContent('密码');
    fireEvent.change(screen.getByLabelText('当前账号密码'), { target: { value: 'present' } });
    fireEvent.change(screen.getByLabelText('二次验证码（选填）'), { target: { value: 'abc123' } });
    fireEvent.click(screen.getByRole('button', { name: '申请提现' }));
    expect(screen.getByRole('alert')).toHaveTextContent('二次验证码');
    expect(posts).toHaveLength(0);
  });
});
