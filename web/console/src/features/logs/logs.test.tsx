import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { MemoryRouter, RouterProvider } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';
import { LogsPage } from './LogsPage';

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function authResponse() {
  return jsonResponse({
    success: true,
    data: {
      access_token: 'memory-only-session',
      expires_in: 900,
      user: {
        id: 41,
        username: 'alice',
        role: 1,
        group: 'default',
      },
    },
  });
}

function logItem(requestID: string, timestamp: number, overrides: Record<string, unknown> = {}) {
  return {
    timestamp,
    request_id: requestID,
    model: 'zt-claude-sonnet-5',
    status: 'success',
    latency: 2,
    prompt_tokens: 120,
    completion_tokens: 48,
    total_tokens: 168,
    billed_amount: 0.004321,
    ...overrides,
  };
}

function logPageResponse(page: number) {
  return jsonResponse({
    success: true,
    data: {
      page,
      page_size: 50,
      total: 51,
      items:
        page === 1
          ? [logItem('ztapi-newest', 1_900_000_100)]
          : [logItem('ztapi-oldest', 1_900_000_000)],
    },
  });
}

afterEach(() => {
  cleanup();
  clearAuthSession();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ZTAPI user log pagination', () => {
  it('navigates across the first and last log pages', async () => {
    let resolveAuth!: (response: Response) => void;
    const pendingAuth = new Promise<Response>((resolve) => {
      resolveAuth = resolve;
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/api/auth/refresh')) {
        return pendingAuth;
      }
      if (url.includes('/api/log/self?')) {
        const page = new URL(url, 'http://ztapi.test').searchParams.get('p');
        return logPageResponse(page === '2' ? 2 : 1);
      }
      return jsonResponse({ success: false, message: 'unexpected' }, 500);
    });
    vi.stubGlobal('fetch', fetchMock);
    render(
      <AppProviders>
        <RouterProvider router={createZTAPIRouter(['/console/logs'])} />
      </AppProviders>,
    );

    await act(async () => {
      resolveAuth(authResponse());
      await pendingAuth;
    });
    expect(screen.getByText('ztapi-newest')).toBeVisible();
    expect(screen.getByText('zt-claude-sonnet-5')).toBeVisible();
    expect(screen.getByText('0.004321 U')).toBeVisible();
    expect(screen.getByText('本次实际扣费')).toBeVisible();
    expect(screen.getByText('输入 120')).toBeVisible();
    expect(screen.getByText('输出 48')).toBeVisible();
    expect(screen.getByText('总计 168')).toBeVisible();
    expect(screen.getByText('2 秒')).toBeVisible();
    expect(screen.getByText('第 1 / 2 页')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '下一页' })).toBeEnabled();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('ztapi-oldest')).toBeVisible();
    expect(screen.getByText('第 2 / 2 页')).toBeVisible();
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled();
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: '上一页' }));
    expect(await screen.findByText('ztapi-newest')).toBeVisible();
  });

  it('filters by model, result, and request ID while explaining safe error codes', async () => {
    let resolveAuth!: (response: Response) => void;
    const pendingAuth = new Promise<Response>((resolve) => { resolveAuth = resolve; });
    const requestedUrls: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (url.endsWith('/api/auth/refresh')) return pendingAuth;
      if (url.includes('/api/log/self?')) {
        requestedUrls.push(url);
        return jsonResponse({
          success: true,
          data: {
            page: 1,
            page_size: 50,
            total: 1,
            items: [logItem('req-filter-1', 1_900_000_100, {
              model: 'zt-gpt-5.6-sol',
              status: 'error',
              error_code: 'model_unavailable',
              billed_amount: 0,
            })],
          },
        });
      }
      return jsonResponse({ success: false, message: 'unexpected' }, 500);
    }));
    render(
      <AppProviders>
        <RouterProvider router={createZTAPIRouter(['/console/logs?request_id=req-filter-1'])} />
      </AppProviders>,
    );
    await act(async () => { resolveAuth(authResponse()); await pendingAuth; });

    expect(await screen.findByText('模型暂不可用')).toBeVisible();
    expect(screen.getByLabelText('请求 ID 筛选')).toHaveValue('req-filter-1');
    expect(requestedUrls[0]).toContain('request_id=req-filter-1');

    fireEvent.change(screen.getByLabelText('模型筛选'), { target: { value: 'zt-gpt-5.6-sol' } });
    fireEvent.change(screen.getByLabelText('结果筛选'), { target: { value: 'error' } });
    fireEvent.click(screen.getByRole('button', { name: '筛选' }));

    await act(async () => undefined);
    const filtered = requestedUrls.at(-1) ?? '';
    expect(filtered).toContain('model_name=zt-gpt-5.6-sol');
    expect(filtered).toContain('request_id=req-filter-1');
    expect(filtered).toContain('type=5');

    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));
    await act(async () => undefined);
    expect(requestedUrls.at(-1)).not.toMatch(/model_name|request_id|type=5/);
  });
});

const diagnosticLog = { timestamp: 1900000100, request_id: 'req-public-1', model: 'zt-public-model', status: 'error',
  latency: 2, prompt_tokens: 12, completion_tokens: 3, total_tokens: 15, billed_amount: 0, error_code: 'model_unavailable' };

function diagnosticPage(items: unknown[], page = 1, total = 51) {
  return jsonResponse({ success: true, data: { page, page_size: 50, total, items } });
}

function renderLogs() {
  return render(<MemoryRouter><LogsPage /></MemoryRouter>);
}

function readBlob(blob: Blob) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = reject;
    reader.readAsText(blob);
  });
}

describe('safe current-page diagnostic downloads', () => {
  beforeEach(() => {
    setAuthSession({ access_token: 'synthetic-session-never-export', expires_in: 900,
      user: { id: 7, username: 'private-account-never-export', role: 1, group: 'default' } });
  });

  it('downloads only public fields on the visible page, with no session, filters, or other pages', async () => {
    const blobs: Blob[] = [];
    const revoke = vi.fn();
    vi.stubGlobal('URL', class extends URL {
      static createObjectURL(blob: Blob) { blobs.push(blob); return 'blob:synthetic-diagnostic'; }
      static revokeObjectURL = revoke;
    });
    const clicks: Array<{ filename: string; href: string }> = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push({ filename: this.download, href: this.href });
    });
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const page = Number(new URL(String(input), 'http://ztapi.test').searchParams.get('p'));
      return diagnosticPage([{ ...diagnosticLog, request_id: `req-public-${page}` }], page);
    });
    vi.stubGlobal('fetch', fetchMock);
    renderLogs();
    expect(await screen.findByText('req-public-1')).toBeVisible();
    fireEvent.change(screen.getByLabelText('请求 ID 筛选'), { target: { value: 'unsent-private-draft-never-export' } });
    fireEvent.click(screen.getByRole('button', { name: '导出本页诊断' }));
    expect(clicks).toEqual([{ filename: 'ztapi-diagnostics-page-1.json', href: 'blob:synthetic-diagnostic' }]);
    expect(blobs[0].type).toBe('application/json');
    const exported = await readBlob(blobs[0]);
    expect(JSON.parse(exported)).toEqual([diagnosticLog]);
    expect(exported).not.toMatch(/synthetic-session|private-account|unsent-private|authorization|cookie|prompt"|upstream/i);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(revoke).toHaveBeenCalledWith('blob:synthetic-diagnostic'));

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    expect(await screen.findByText('req-public-2')).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '导出本页诊断' }));
    expect(clicks[1].filename).toBe('ztapi-diagnostics-page-2.json');
    expect(JSON.parse(await readBlob(blobs[1]))).toEqual([{ ...diagnosticLog, request_id: 'req-public-2' }]);
    await waitFor(() => expect(revoke).toHaveBeenCalledTimes(2));
  });

  it('disables export while loading and for an empty page', async () => {
    let release!: (value: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { release = resolve; })));
    renderLogs();
    expect(screen.getByRole('button', { name: '导出本页诊断' })).toBeDisabled();
    await act(async () => { release(diagnosticPage([], 1, 0)); });
    expect(await screen.findByText('当前账户还没有请求日志。')).toBeVisible();
    expect(screen.getByRole('button', { name: '导出本页诊断' })).toBeDisabled();
  });

  it.each([
    ['credentials', { authorization: 'Bearer synthetic-secret-never-export' }],
    ['customer input', { prompt: 'private-customer-input-never-export' }],
    ['raw upstream details', { upstream_response: { headers: { token: 'private-upstream-never-export' } } }],
  ])('refuses responses containing %s and offers no export', async (_name, extra) => {
    vi.stubGlobal('fetch', vi.fn(async () => diagnosticPage([{ ...diagnosticLog, ...extra }])));
    renderLogs();
    expect(await screen.findByRole('alert')).toHaveTextContent('使用日志加载失败');
    expect(screen.getByRole('button', { name: '导出本页诊断' })).toBeDisabled();
    expect(screen.queryByText('req-public-1')).not.toBeInTheDocument();
  });

  it('removes the prior account page and disables export immediately on account change', async () => {
    let release!: (value: Response) => void;
    const fetchMock = vi.fn().mockResolvedValueOnce(diagnosticPage([diagnosticLog]))
      .mockImplementation(() => new Promise<Response>((resolve) => { release = resolve; }));
    vi.stubGlobal('fetch', fetchMock);
    renderLogs();
    expect(await screen.findByText('req-public-1')).toBeVisible();
    await act(async () => {
      setAuthSession({ access_token: 'synthetic-b', expires_in: 900,
        user: { id: 8, username: 'b', role: 1, group: 'default' } });
    });
    expect(screen.queryByText('req-public-1')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '导出本页诊断' })).toBeDisabled();
    await act(async () => { release(diagnosticPage([{ ...diagnosticLog, request_id: 'req-account-b' }])); });
    expect(await screen.findByText('req-account-b')).toBeVisible();
    expect(screen.getByRole('button', { name: '导出本页诊断' })).toBeEnabled();
  });

  it('does not describe an error record as proof of zero settled charge', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => diagnosticPage([diagnosticLog])));
    renderLogs();
    expect(await screen.findByText('req-public-1')).toBeVisible();
    expect(screen.getByRole('columnheader', { name: '已结算费用' })).toBeVisible();
    expect(screen.getByText('未记录结算费用')).toBeVisible();
    expect(screen.queryByText('本次实际扣费')).not.toBeInTheDocument();
    expect(screen.queryByText('0.000000 U')).not.toBeInTheDocument();
  });

  it('labels consumption records as settled rather than promising successful model delivery', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => diagnosticPage([{ ...diagnosticLog, status: 'success', billed_amount: 0.01 }])));
    renderLogs();
    expect(await screen.findByText('req-public-1')).toBeVisible();
    expect(screen.getByText('已结算')).toBeVisible();
    expect(screen.getByRole('option', { name: '仅已结算' })).toBeVisible();
  });
});
