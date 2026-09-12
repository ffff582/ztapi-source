import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from '../../api/client';
import { WalletPage } from './WalletPage';

function success(data: unknown) {
  return new Response(JSON.stringify({ success: true, data }), {
    headers: { 'Content-Type': 'application/json' },
  });
}

function reservation(id: number, status = 'reserved') {
  return { id, request_id: `request-${id}`, model: 'zt-model', status,
    reserved_quota: 250_000, created_at: '2026-09-07T00:00:00Z', updated_at: '2026-09-07T00:00:00Z' };
}

function setup() {
  const state = { items: [reservation(1), reservation(2, 'pending')], divisor: 100_000 };
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://localhost');
    switch (url.pathname) {
      case '/api/user/self': return success({ quota: 1_000_000 });
      case '/api/status': return success({ quota_per_unit: state.divisor });
      case '/api/user/topup/info': return success({ enable_usdt_trc20_topup: false,
        usdt_trc20_network: 'tron-mainnet', usdt_trc20_asset: 'USDT',
        usdt_trc20_min_topup: 10, usdt_trc20_order_ttl_seconds: 600 });
      case '/api/user/topup/self': return success({ page: 1, page_size: 5, total: 0, items: [] });
      case '/api/user/self/pending-settlements': {
        const after = Number(url.searchParams.get('after_id'));
        const items = state.items.filter((item) => item.id > after).slice(0, Number(url.searchParams.get('limit')));
        return success({ items, next_after_id: items.at(-1)?.id ?? after });
      }
      default: throw new Error(`Unexpected local test request: ${url.pathname}`);
    }
  });
  vi.stubGlobal('fetch', fetchMock);
  return { state, fetchMock };
}

function holds() { return screen.getByRole('region', { name: '待核账预留' }); }

describe('wallet pending reservations', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setAuthSession({ access_token: 'offline-wallet-token', expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' } });
  });
  afterEach(() => {
    cleanup(); clearAuthSession(); sessionStorage.clear(); vi.restoreAllMocks(); vi.unstubAllGlobals();
  });

  it('shows uncharged holds separately without subtracting from available balance again', async () => {
    setup();
    render(<WalletPage />);
    const region = holds();
    expect(await within(region).findByText('request-1')).toBeVisible();
    expect(within(region).getAllByText('$2.50')).toHaveLength(2);
    expect(within(region).getByText('预留中')).toBeVisible();
    expect(within(region).getByText('待核账')).toBeVisible();
    expect(within(region).getByText(/尚未计费/)).toBeVisible();
    expect(await within(screen.getByRole('region', { name: '账户余额' })).findByText('$10.00')).toBeVisible();
    expect(within(region).queryByText(/合计|总额/)).toBeNull();
  });

  it('uses one bounded lookahead without skipping the next cursor item', async () => {
    const { state, fetchMock } = setup();
    state.items = Array.from({ length: 21 }, (_, index) => reservation(index + 1));
    render(<WalletPage />);
    expect(await within(holds()).findByText('request-20')).toBeVisible();
    expect(within(holds()).queryByText('request-21')).toBeNull();
    expect(within(holds()).getByRole('button', { name: '上一页预留记录' })).toBeDisabled();
    fireEvent.click(within(holds()).getByRole('button', { name: '下一页预留记录' }));
    expect(await within(holds()).findByText('request-21')).toBeVisible();
    expect(within(holds()).getByRole('button', { name: '下一页预留记录' })).toBeDisabled();
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('after_id=20&limit=21'))).toBe(true);
    fireEvent.click(within(holds()).getByRole('button', { name: '上一页预留记录' }));
    expect(await within(holds()).findByText('request-1')).toBeVisible();
  });

  it('disables next on an exact full final page', async () => {
    const { state } = setup();
    state.items = Array.from({ length: 20 }, (_, index) => reservation(index + 1));
    render(<WalletPage />);
    expect(await within(holds()).findByText('request-20')).toBeVisible();
    expect(within(holds()).getByRole('button', { name: '下一页预留记录' })).toBeDisabled();
  });

  it('distinguishes an empty later page after settlement and allows return', async () => {
    const { state } = setup();
    state.items = Array.from({ length: 21 }, (_, index) => reservation(index + 1));
    render(<WalletPage />);
    await within(holds()).findByText('request-20');
    state.items = [reservation(1)];
    fireEvent.click(within(holds()).getByRole('button', { name: '下一页预留记录' }));
    expect(await within(holds()).findByText('本页暂无预留记录。')).toBeVisible();
    expect(within(holds()).queryByText('暂无待核账预留。')).toBeNull();
    fireEvent.click(within(holds()).getByRole('button', { name: '上一页预留记录' }));
    expect(await within(holds()).findByText('request-1')).toBeVisible();
  });

  it('shows a genuine empty result without inventing held money', async () => {
    const { state } = setup(); state.items = [];
    render(<WalletPage />);
    expect(await within(holds()).findByText('暂无待核账预留。')).toBeVisible();
    expect(within(holds()).queryByText('$0.00')).toBeNull();
  });

  it.each(['http', 'contract'])('keeps login and balance on a %s resource error, then retries', async (kind) => {
    const { fetchMock } = setup();
    const original = fetchMock.getMockImplementation()!;
    let fail = true;
    fetchMock.mockImplementation(async (input) => {
      if (String(input).includes('/pending-settlements') && fail) {
        return kind === 'http' ? new Response(JSON.stringify({ success: false, message: 'resource unavailable' }), { status: 500 })
          : success({ items: [] });
      }
      return original(input);
    });
    render(<WalletPage />);
    expect(await within(holds()).findByText('预留记录加载失败，请刷新重试。')).toBeVisible();
    expect(within(holds()).queryByText('暂无待核账预留。')).toBeNull();
    expect(getAuthSession()?.access_token).toBe('offline-wallet-token');
    expect(await within(screen.getByRole('region', { name: '账户余额' })).findByText('$10.00')).toBeVisible();
    fail = false;
    fireEvent.click(within(holds()).getByRole('button', { name: '刷新预留记录' }));
    expect(await within(holds()).findByText('request-1')).toBeVisible();
  });

  it.each([-1, Number.MAX_SAFE_INTEGER + 1])('rejects invalid held quota %s', async (quota) => {
    const { state } = setup(); state.items[0].reserved_quota = quota;
    render(<WalletPage />);
    expect(await within(holds()).findByText('预留记录加载失败，请刷新重试。')).toBeVisible();
  });

  it('rejects invalid runtime conversion rather than guessing a price', async () => {
    const { state } = setup(); state.divisor = 0;
    render(<WalletPage />);
    expect(await within(holds()).findByText('预留记录加载失败，请刷新重试。')).toBeVisible();
  });

  it('ignores an old result after focus refresh', async () => {
    const { state, fetchMock } = setup();
    const original = fetchMock.getMockImplementation()!;
    let release: (response: Response) => void = () => undefined;
    let first = true;
    fetchMock.mockImplementation(async (input) => {
      if (String(input).includes('/pending-settlements') && first) {
        first = false;
        return new Promise<Response>((resolve) => { release = resolve; });
      }
      return original(input);
    });
    render(<WalletPage />);
    expect(within(holds()).getByText('正在读取预留记录...')).toBeVisible();
    await act(async () => undefined);
    state.items = [reservation(9)];
    fireEvent(window, new Event('focus'));
    expect(await within(holds()).findByText('request-9')).toBeVisible();
    await act(async () => release(success({ items: [reservation(1)], next_after_id: 1 })));
    expect(within(holds()).queryByText('request-1')).toBeNull();
  });
});
