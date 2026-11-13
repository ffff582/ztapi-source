import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from '../../api/client';
import { DashboardPage } from './DashboardPage';

function success(data: unknown) {
  return new Response(JSON.stringify({ success: true, data }), { headers: { 'Content-Type': 'application/json' } });
}

function logPage(url: URL, count = 3) {
  const page = Number(url.searchParams.get('p'));
  const items = Array.from({ length: count }, (_, index) => ({
    timestamp: Number(url.searchParams.get('start_timestamp')) + 1,
    request_id: `req-${getAuthSession()?.user.id}-${index}`, model: 'zt-public-model', status: 'success', latency: 1,
    prompt_tokens: 10, completion_tokens: 5, total_tokens: 15, billed_amount: 0.1,
  }));
  return success({ page, page_size: 2, total: count, items: items.slice((page - 1) * 2, page * 2) });
}

function installLogs(load: (url: URL, init?: RequestInit) => Promise<Response>) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://ztapi.test');
    switch (url.pathname) {
      case '/api/auth/session': return success(getAuthSession()?.user);
      case '/api/log/self/stat': return success({ rpm: 9, tpm: 9999, quota: 12345 });
      case '/api/log/self': return load(url, init);
      case '/api/token/': return success({ page: 1, page_size: 1, total: 1, items: [] });
      case '/api/user/self': return success({ quota: 5000000 });
      case '/api/status': return success({ quota_per_unit: 500000 });
      default: throw new Error(`Unexpected local fixture request: ${url.pathname}`);
    }
  }));
}

beforeEach(() => {
  setAuthSession({ access_token: 'synthetic-a', expires_in: 900,
    user: { id: 7, username: 'a', role: 1, group: 'default' } });
});
afterEach(() => { cleanup(); clearAuthSession(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('period statistics presentation and failure boundaries', () => {
  it('reports a mismatched session response instead of showing another account or an unexplained blank page', async () => {
    installLogs(async (url) => logPage(url));
    const originalFetch = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation((input, init) => String(input).endsWith('/auth/session')
      ? Promise.resolve(success({ id: 99, username: 'other-account', role: 1, group: 'default' })) : originalFetch(input, init));
    render(<DashboardPage />);
    expect(await screen.findByRole('alert')).toHaveTextContent('未展示部分合计');
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
    expect(screen.queryByText('other-account')).not.toBeInTheDocument();
  });

  it('cancels outstanding pagination if another dashboard dependency fails', async () => {
    let pageSignal!: AbortSignal;
    installLogs(async (_url, init) => {
      pageSignal = init!.signal!;
      return new Promise<Response>(() => undefined);
    });
    const originalFetch = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation((input, init) => String(input).includes('/log/self/stat?')
      ? Promise.resolve(new Response('{}', { status: 503 })) : originalFetch(input, init));
    render(<DashboardPage />);
    expect(await screen.findByRole('alert')).toHaveTextContent('未展示部分合计');
    expect(pageSignal.aborted).toBe(true);
  });

  it('never shows partial totals on a later-page failure and allows retry', async () => {
    let fail = true;
    installLogs(async (url) => fail && url.searchParams.get('p') === '2'
      ? new Response('{}', { status: 503 }) : logPage(url));
    render(<DashboardPage />);
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
    expect(await screen.findByRole('alert')).toHaveTextContent('未展示部分合计');
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole('button', { name: '重试统计' }));
    const metrics = await screen.findByRole('region', { name: '关键统计' });
    expect(within(metrics).getByText('0.300000 U')).toBeVisible();
    expect(within(metrics).getByText('45')).toBeVisible();
  });

  it('hides a previous range while the next range is loading and after its failure', async () => {
    let rejectNext!: (reason: Error) => void;
    let pending = false;
    installLogs(async (url) => pending ? new Promise<Response>((_resolve, reject) => { rejectNext = reject; }) : logPage(url));
    render(<DashboardPage />);
    expect(await screen.findByRole('region', { name: '关键统计' })).toBeVisible();
    pending = true;
    fireEvent.click(screen.getByRole('button', { name: '近 30 天' }));
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
    await act(async () => { rejectNext(new Error('synthetic-upstream-secret-never-render')); });
    expect(await screen.findByRole('alert')).toHaveTextContent('未展示部分合计');
    expect(screen.queryByText('synthetic-upstream-secret-never-render')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
  });

  it('aborts and ignores a stale range response after a newer range has completed', async () => {
    let release!: (response: Response) => void;
    let oldURL!: URL;
    let oldSignal!: AbortSignal;
    let first = true;
    installLogs(async (url, init) => {
      if (first) {
        first = false;
        oldURL = url;
        oldSignal = init!.signal!;
        return new Promise<Response>((resolve) => { release = resolve; });
      }
      return logPage(url, 1);
    });
    render(<DashboardPage />);
    fireEvent.click(screen.getByRole('button', { name: '今天' }));
    const metrics = await screen.findByRole('region', { name: '关键统计' });
    expect(within(metrics).getByText('0.100000 U')).toBeVisible();
    expect(oldSignal.aborted).toBe(true);
    await act(async () => { release(logPage(oldURL)); });
    expect(within(screen.getByRole('region', { name: '关键统计' })).getByText('0.100000 U')).toBeVisible();
  });

  it('clears the prior account metrics and loads only the new account records', async () => {
    let release!: (response: Response) => void;
    let newURL!: URL;
    installLogs(async (url) => {
      if (getAuthSession()?.user.id === 8 && url.searchParams.get('p') === '1' && !newURL) {
        newURL = url;
        return new Promise<Response>((resolve) => { release = resolve; });
      }
      return logPage(url, 1);
    });
    render(<DashboardPage />);
    expect(await screen.findByText('req-7-0')).toBeVisible();
    await act(async () => {
      setAuthSession({ access_token: 'synthetic-b', expires_in: 900,
        user: { id: 8, username: 'b', role: 1, group: 'default' } });
    });
    expect(screen.queryByText('req-7-0')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: '关键统计' })).not.toBeInTheDocument();
    await act(async () => { release(logPage(newURL, 1)); });
    expect(await screen.findByText('req-8-0')).toBeVisible();
  });

  it('identifies preview charts as recent-record samples rather than period-wide rankings', async () => {
    installLogs(async (url) => logPage(url));
    render(<DashboardPage />);
    expect(await screen.findByRole('region', { name: '关键统计' })).toBeVisible();
    expect(screen.getByRole('heading', { name: '最近记录模型分布' })).toBeVisible();
    expect(screen.getByRole('heading', { name: '最近已结算费用' })).toBeVisible();
  });
});
