import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { DashboardPage } from '../dashboard/DashboardPage';
import { WalletPage } from './WalletPage';

const tradeNo = 'USDT-BALANCE-REGRESSION';
const topUp = {
  id: 16, amount: 10, money: 10.57, trade_no: tradeNo,
  payment_provider: 'usdt_trc20', payment_method: 'usdt_trc20',
  create_time: 1788585565, complete_time: 1788585730, status: 'success',
};

function success(data: unknown) {
  return new Response(JSON.stringify({ success: true, data }), {
    headers: { 'Content-Type': 'application/json' },
  });
}

function mockAccount(initialQuota = 5_000_000, divisor = 500_000) {
  const account = { quota: initialQuota, divisor, settled: initialQuota > 0, invalid: false };
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'https://ztapi.vip');
    switch (url.pathname) {
      case '/api/user/self/pending-settlements': return success({ items: [], next_after_id: 0 });
      case '/api/user/self': return success(account.invalid ? {} : { quota: account.quota });
      case '/api/status': return success({ quota_per_unit: account.divisor });
      case '/api/auth/session': return success({ id: 7, username: 'alice', role: 1, group: 'default' });
      case '/api/log/self/stat': return success({ rpm: 0, tpm: 0 });
      case '/api/log/self': return success({ page: 1, page_size: 5, total: 0, items: [] });
      case '/api/user/topup/info': return success({
        enable_usdt_trc20_topup: true, usdt_trc20_network: 'tron-mainnet',
        usdt_trc20_asset: 'USDT', usdt_trc20_min_topup: 10, usdt_trc20_order_ttl_seconds: 600,
      });
      case '/api/user/topup/self': return success({
        page: Number(url.searchParams.get('p')), page_size: 5,
        total: account.settled ? 1 : 0, items: account.settled ? [topUp] : [],
      });
      default:
        if (url.pathname.startsWith('/api/user/topup/usdt-trc20/orders')) {
          if (init?.method !== 'POST') {
            account.quota = 5_000_000;
            account.settled = true;
          }
          return success({
            id: 16, trade_no: tradeNo, credit_units: 10, pay_amount: '10.57',
            receiving_address: 'TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2',
            network: 'tron-mainnet', asset: 'USDT', expires_at: Math.floor(Date.now() / 1000) + 600,
            status: account.settled ? 'settled' : 'pending',
            ...(account.settled ? { tx_id: 'confirmed-transaction', settled_at: 1788585730 } : {}),
          });
        }
        throw new Error(`Unexpected request: ${url.pathname}`);
    }
  });
  vi.stubGlobal('fetch', fetchMock);
  return { account, fetchMock };
}

describe('recharge balance visibility', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setAuthSession({ access_token: 'test-only', expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' } });
  });
  afterEach(() => {
    cleanup(); clearAuthSession(); sessionStorage.clear();
    vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals();
  });

  it('shows the actual credited balance and persisted receipt without a session order', async () => {
    mockAccount();
    render(<WalletPage />);
    expect(await within(screen.getByRole('region', { name: '账户余额' })).findByText('$10.00')).toBeVisible();
    const history = screen.getByRole('region', { name: '充值记录' });
    expect(await within(history).findByText(tradeNo)).toBeVisible();
    expect(within(history).getByText('10.57 USDT')).toBeVisible();
    expect(within(history).getByText('$10.00')).toBeVisible();
    expect(within(history).getByText('已到账')).toBeVisible();
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBeNull();
  });

  it('refreshes balance and history after settlement without reloading or crediting again', async () => {
    vi.useFakeTimers();
    const { fetchMock } = mockAccount(0);
    render(<WalletPage />);
    await act(async () => undefined);
    expect(within(screen.getByRole('region', { name: '账户余额' })).getByText('$0.00')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '创建支付订单' }));
    await act(async () => undefined);
    await act(async () => vi.advanceTimersByTimeAsync(2_000));
    expect(screen.getByText('充值已到账')).toBeVisible();
    expect(within(screen.getByRole('region', { name: '账户余额' })).getByText('$10.00')).toBeVisible();
    expect(within(screen.getByRole('region', { name: '充值记录' })).getByText('已到账')).toBeVisible();
    const callsAtSettlement = fetchMock.mock.calls.length;
    await act(async () => vi.advanceTimersByTimeAsync(10_000));
    expect(fetchMock).toHaveBeenCalledTimes(callsAtSettlement);
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it.each([[1_000_000, 100_000, '$10.00'], [-400, 500_000, '-$0.0008']])(
    'uses the runtime conversion and preserves signed balance %s', async (quota, divisor, display) => {
      mockAccount(quota, divisor);
      render(<DashboardPage />);
      expect(await within(screen.getByRole('region', { name: '账户余额' })).findByText(display)).toBeVisible();
    },
  );

  it('does not invent a zero balance when the response is malformed and supports retry', async () => {
    const { account } = mockAccount();
    account.invalid = true;
    render(<WalletPage />);
    const balance = screen.getByRole('region', { name: '账户余额' });
    expect(await within(balance).findByText('余额加载失败，请刷新重试。')).toBeVisible();
    expect(within(balance).queryByText('$0.00')).toBeNull();
    account.invalid = false;
    fireEvent.click(within(balance).getByRole('button', { name: '刷新余额' }));
    expect(await within(balance).findByText('$10.00')).toBeVisible();
  });

  it('reloads the actual balance on window focus', async () => {
    const { account } = mockAccount(0);
    render(<DashboardPage />);
    const balance = screen.getByRole('region', { name: '账户余额' });
    expect(await within(balance).findByText('$0.00')).toBeVisible();
    account.quota = 5_000_000;
    fireEvent(window, new Event('focus'));
    expect(await within(balance).findByText('$10.00')).toBeVisible();
  });

  it('ignores a stale balance response that arrives after a newer refresh', async () => {
    const { fetchMock } = mockAccount();
    const originalFetch = fetchMock.getMockImplementation()!;
    let release: (response: Response) => void = () => undefined;
    let first = true;
    fetchMock.mockImplementation((input, init) => {
      if (String(input).endsWith('/user/self') && first) {
        first = false;
        return new Promise<Response>((resolve) => { release = resolve; });
      }
      return originalFetch(input, init);
    });
    render(<WalletPage />);
    await act(async () => undefined);
    fireEvent(window, new Event('focus'));
    const balance = screen.getByRole('region', { name: '账户余额' });
    expect(await within(balance).findByText('$10.00')).toBeVisible();
    await act(async () => release(success({ quota: 0 })));
    expect(within(balance).getByText('$10.00')).toBeVisible();
    expect(within(balance).queryByText('$0.00')).toBeNull();
  });

  it.each([0, -1])('rejects an invalid runtime conversion %s instead of showing money', async (divisor) => {
    mockAccount(5_000_000, divisor);
    render(<WalletPage />);
    const balance = screen.getByRole('region', { name: '账户余额' });
    expect(await within(balance).findByText('余额加载失败，请刷新重试。')).toBeVisible();
    expect(within(balance).queryByText('$10.00')).toBeNull();
  });

  it('paginates saved orders without presenting expired or other-provider payments as credited USD', async () => {
    const { fetchMock } = mockAccount();
    const originalFetch = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation((input, init) => {
      const url = new URL(String(input), 'https://ztapi.vip');
      if (url.pathname === '/api/user/topup/self') {
        const page = Number(url.searchParams.get('p'));
        return Promise.resolve(success({ page, page_size: 5, total: 6,
          items: page === 1 ? [{ ...topUp, status: 'expired' }]
            : [{ ...topUp, trade_no: 'LEGACY-ORDER', payment_provider: 'unknown' }],
        }));
      }
      return originalFetch(input, init);
    });
    render(<WalletPage />);
    const history = screen.getByRole('region', { name: '充值记录' });
    expect(await within(history).findByText('已过期')).toBeVisible();
    expect(within(history).queryByText('$10.00')).toBeNull();
    expect(within(history).getByRole('button', { name: '上一页充值记录' })).toBeDisabled();
    fireEvent.click(within(history).getByRole('button', { name: '下一页充值记录' }));
    expect(await within(history).findByText('LEGACY-ORDER')).toBeVisible();
    expect(within(history).queryByText('$10.00')).toBeNull();
    expect(within(history).queryByText('10.57 USDT')).toBeNull();
    expect(within(history).getByRole('button', { name: '下一页充值记录' })).toBeDisabled();
  });

  it('shows a retriable history error instead of claiming there are no orders', async () => {
    const { fetchMock } = mockAccount();
    const originalFetch = fetchMock.getMockImplementation()!;
    let fail = true;
    fetchMock.mockImplementation((input, init) => {
      if (String(input).includes('/user/topup/self?') && fail) return Promise.resolve(success({ items: [] }));
      return originalFetch(input, init);
    });
    render(<WalletPage />);
    const history = screen.getByRole('region', { name: '充值记录' });
    expect(await within(history).findByText('充值记录加载失败，请刷新重试。')).toBeVisible();
    expect(within(history).queryByText('暂无充值记录。')).toBeNull();
    fail = false;
    fireEvent.click(within(history).getByRole('button', { name: '刷新充值记录' }));
    expect(await within(history).findByText(tradeNo)).toBeVisible();
  });
});
