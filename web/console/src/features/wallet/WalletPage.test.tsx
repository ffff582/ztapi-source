import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { WalletPage } from './WalletPage';

const address = 'TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2';

function jsonResponse(data: unknown, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function success(data: unknown, status = 200) {
  return jsonResponse({ success: true, data }, status);
}

function configuration() {
  return {
    enable_usdt_trc20_topup: true,
    usdt_trc20_network: 'tron-mainnet',
    usdt_trc20_asset: 'USDT',
    usdt_trc20_min_topup: 10,
    usdt_trc20_order_ttl_seconds: 600,
  };
}

function order(status = 'pending') {
  return {
    id: 41,
    trade_no: 'USDT-TEST-41',
    credit_units: 10,
    pay_amount: '10.37',
    receiving_address: address,
    network: 'tron-mainnet',
    asset: 'USDT',
    expires_at: Math.floor(Date.now() / 1000) + 600,
    status,
    ...(status === 'settled'
      ? { tx_id: 'confirmed-transaction', settled_at: Math.floor(Date.now() / 1000) }
      : {}),
  };
}

function requestPath(input: RequestInfo | URL) {
  return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
}

function accountRead(path: string) {
  if (path.includes('/api/user/self/pending-settlements?')) return Promise.resolve(success({ items: [], next_after_id: 0 }));
  if (path.endsWith('/api/user/self')) return Promise.resolve(success({ quota: 0 }));
  if (path.endsWith('/api/status')) return Promise.resolve(success({ quota_per_unit: 500_000 }));
  if (path.includes('/api/user/topup/self?')) {
    return Promise.resolve(success({ page: 1, page_size: 5, total: 0, items: [] }));
  }
  return Promise.reject(new Error(`Unexpected request: ${path}`));
}

describe('WalletPage', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setAuthSession({
      access_token: 'wallet-session-token',
      expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' },
    });
  });

  afterEach(() => {
    cleanup();
    clearAuthSession();
    sessionStorage.clear();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('creates a whole-unit order and renders exact address-only TRC-20 instructions', async () => {
    vi.spyOn(Date, 'now').mockReturnValue(new Date('2026-09-04T06:00:00Z').getTime());
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = requestPath(input);
      if (path.endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders') && init?.method === 'POST') {
        return Promise.resolve(success(order(), 201));
      }
      return accountRead(path);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<WalletPage />);
    const amount = await screen.findByLabelText('充值数量');
    fireEvent.change(amount, { target: { value: '10' } });
    fireEvent.click(screen.getByRole('button', { name: '创建支付订单' }));

    expect(await screen.findByText('10.37 USDT')).toBeInTheDocument();
    expect(screen.getByText(address)).toBeInTheDocument();
    expect(screen.getByText('TRON Mainnet · TRC-20')).toBeInTheDocument();
    expect(screen.getByTestId('usdt-address-qr')).toHaveAttribute('data-qr-value', address);
    expect(screen.getByText('10:00')).toBeInTheDocument();
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBe('USDT-TEST-41');

    const createCall = fetchMock.mock.calls.find(([input, init]) =>
      requestPath(input as RequestInfo | URL).endsWith('/orders') &&
      (init as RequestInit | undefined)?.method === 'POST',
    );
    expect(createCall).toBeDefined();
    expect(JSON.parse(String((createCall?.[1] as RequestInit).body))).toEqual({ amount: 10 });
    expect(new Headers((createCall?.[1] as RequestInit).headers).get('Authorization'))
      .toBe('Bearer wallet-session-token');
  });

  it('rejects decimal input before creating an order', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      if (requestPath(input).endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      return accountRead(requestPath(input));
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<WalletPage />);
    fireEvent.change(await screen.findByLabelText('充值数量'), {
      target: { value: '10.5' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建支付订单' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('只能输入整数');
    expect(fetchMock.mock.calls.some(([input]) => requestPath(input).endsWith('/orders'))).toBe(false);
  });

  it('polls a pending order after two seconds and stops after settlement', async () => {
    vi.useFakeTimers();
    let statusRequests = 0;
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = requestPath(input);
      if (path.endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders') && init?.method === 'POST') {
        return Promise.resolve(success(order(), 201));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders/USDT-TEST-41')) {
        statusRequests += 1;
        return Promise.resolve(success(order('settled')));
      }
      return accountRead(path);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<WalletPage />);
    await act(async () => undefined);
    fireEvent.change(screen.getByLabelText('充值数量'), { target: { value: '10' } });
    fireEvent.click(screen.getByRole('button', { name: '创建支付订单' }));
    await act(async () => undefined);
    expect(screen.getByText('等待链上确认')).toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(1_999));
    expect(statusRequests).toBe(0);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(screen.getByText('充值已到账')).toBeInTheDocument();
    expect(statusRequests).toBe(1);
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBeNull();

    await act(async () => vi.advanceTimersByTimeAsync(4_000));
    expect(statusRequests).toBe(1);
  });

  it('keeps polling a confirming order without showing reusable payment instructions', async () => {
    vi.useFakeTimers();
    sessionStorage.setItem('ztapi.usdt.pending_trade_no', 'USDT-TEST-41');
    let statusRequests = 0;
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = requestPath(input);
      if (path.endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders/USDT-TEST-41')) {
        statusRequests += 1;
        return Promise.resolve(success(order(statusRequests === 1 ? 'confirming' : 'settled')));
      }
      return accountRead(path);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<WalletPage />);
    await act(async () => undefined);

    expect(screen.getByText('正在确认到账')).toBeInTheDocument();
    expect(screen.queryByText('10.37 USDT')).not.toBeInTheDocument();
    expect(screen.queryByText(address)).not.toBeInTheDocument();
    expect(screen.queryByTestId('usdt-address-qr')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '创建支付订单' })).toBeDisabled();
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBe('USDT-TEST-41');

    await act(async () => vi.advanceTimersByTimeAsync(2_000));
    expect(screen.getByText('充值已到账')).toBeInTheDocument();
    expect(statusRequests).toBe(2);
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBeNull();
  });

  it('hides payment instructions as soon as the local payment window ends', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-09-04T06:00:00Z'));
    const expiringOrder = {
      ...order(),
      expires_at: Math.floor(Date.now() / 1_000) + 1,
    };
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = requestPath(input);
      if (path.endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders') && init?.method === 'POST') {
        return Promise.resolve(success(expiringOrder, 201));
      }
      return accountRead(path);
    });
    vi.stubGlobal('fetch', fetchMock);

    render(<WalletPage />);
    await act(async () => undefined);
    fireEvent.change(screen.getByLabelText('充值数量'), { target: { value: '10' } });
    fireEvent.click(screen.getByRole('button', { name: '创建支付订单' }));
    await act(async () => undefined);
    expect(screen.getByText('10.37 USDT')).toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(1_000));

    expect(screen.getByText('正在确认到账')).toBeInTheDocument();
    expect(screen.queryByText('10.37 USDT')).not.toBeInTheDocument();
    expect(screen.queryByText(address)).not.toBeInTheDocument();
    expect(screen.queryByTestId('usdt-address-qr')).not.toBeInTheDocument();
  });

  it.each([
    ['expired', '订单已过期'],
    ['manual_review', '订单待人工核验'],
  ])('shows the terminal %s state', async (status, label) => {
    sessionStorage.setItem('ztapi.usdt.pending_trade_no', 'USDT-TEST-41');
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const path = requestPath(input);
      if (path.endsWith('/api/user/topup/info')) {
        return Promise.resolve(success(configuration()));
      }
      if (path.endsWith('/api/user/topup/usdt-trc20/orders/USDT-TEST-41')) {
        return Promise.resolve(success(order(status)));
      }
      return accountRead(path);
    }));

    render(<WalletPage />);

    expect(await screen.findByText(label)).toBeInTheDocument();
    expect(sessionStorage.getItem('ztapi.usdt.pending_trade_no')).toBeNull();
  });
});
