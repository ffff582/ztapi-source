import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { DashboardPage } from './DashboardPage';

function success(data: unknown) {
  return new Response(JSON.stringify({ success: true, data }), {
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('DashboardPage usage log', () => {
  beforeEach(() => {
    setAuthSession({
      access_token: 'dashboard-session-token',
      expires_in: 900,
      user: { id: 7, username: 'alice', role: 1, group: 'default' },
    });
  });

  afterEach(() => {
    cleanup();
    clearAuthSession();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('makes the used model, actual charge, token breakdown, latency, and request ID clear', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'https://ztapi.vip');
      switch (url.pathname) {
        case '/api/auth/session':
          return success({ id: 7, username: 'alice', role: 1, group: 'default' });
        case '/api/log/self/stat':
          return success({ rpm: 2, tpm: 168, quota: 0 });
        case '/api/log/self':
          return success({
            page: 1,
            page_size: 100,
            total: 1,
            items: [{
              timestamp: Number(url.searchParams.get('start_timestamp')) + 1,
              request_id: 'req-usage-clear-001',
              model: 'zt-claude-sonnet-5',
              status: 'success',
              latency: 2,
              prompt_tokens: 120,
              completion_tokens: 48,
              total_tokens: 168,
              billed_amount: 0.004321,
            }],
          });
        case '/api/token/':
          return success({ page: 1, page_size: 1, total: 1, items: [] });
        case '/api/user/self':
          return success({ quota: 5_000_000 });
        case '/api/status':
          return success({ quota_per_unit: 500_000 });
        default:
          throw new Error(`Unexpected request: ${url.pathname}`);
      }
    }));

    render(<DashboardPage />);

    const table = await screen.findByRole('table');
    const row = (await within(table).findByText('zt-claude-sonnet-5')).closest('tr');
    expect(row).not.toBeNull();
    const usageLog = within(row as HTMLElement);
    expect(usageLog.getByText('0.004321 U')).toBeVisible();
    expect(usageLog.getByText('输入 120')).toBeVisible();
    expect(usageLog.getByText('输出 48')).toBeVisible();
    expect(usageLog.getByText('总计 168')).toBeVisible();
    expect(usageLog.getByText('2 秒')).toBeVisible();
    expect(usageLog.getByText('req-usage-clear-001')).toBeVisible();
    expect(usageLog.getByText('已结算')).toBeVisible();

    const checklist = screen.getByRole('region', { name: '首次接入进度' });
    expect(within(checklist).getByText('2 / 4 已完成')).toBeVisible();
    expect(within(checklist).getByText('创建 API Key').closest('li')).toHaveAttribute('data-complete', 'true');
    expect(within(checklist).getByText('选择并测试模型').closest('li')).toHaveAttribute('data-complete', 'false');
    expect(within(checklist).getByText('发送首个请求').closest('li')).toHaveAttribute('data-complete', 'true');
    expect(within(checklist).getByText('查看费用日志').closest('li')).toHaveAttribute('data-complete', 'false');
  });

  it('shows whole-period request records and settled totals rather than the preview or minute counters', async () => {
    const logQueries: URLSearchParams[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://ztapi.test');
      switch (url.pathname) {
        case '/api/auth/session': return success({ id: 7, username: 'alice', role: 1, group: 'default' });
        case '/api/log/self/stat': return success({ rpm: 9, tpm: 9999, quota: 750000 });
        case '/api/log/self': {
          logQueries.push(url.searchParams);
          const timestamp = Number(url.searchParams.get('start_timestamp')) + 1;
          const items = [
            { status: 'success', total_tokens: 10, billed_amount: 0.1 },
            { status: 'info', total_tokens: 990, billed_amount: 999 },
            { status: 'error', total_tokens: 500, billed_amount: 3 },
            { status: 'success', total_tokens: 20, billed_amount: 0.2 },
            { status: 'success', total_tokens: 30, billed_amount: 0.3 },
            { status: 'success', total_tokens: 40, billed_amount: 0.4 },
            { status: 'success', total_tokens: 50, billed_amount: 0.5 },
          ].map((item, index) => ({ timestamp, request_id: `req-period-${index}`,
            model: 'zt-public-model', latency: 1, prompt_tokens: item.total_tokens,
            completion_tokens: 0, ...item }));
          const page = Number(url.searchParams.get('p'));
          return success({ page, page_size: 2, total: items.length, items: items.slice((page - 1) * 2, page * 2) });
        }
        case '/api/token/': return success({ page: 1, page_size: 1, total: 1, items: [] });
        case '/api/user/self': return success({ quota: 5000000 });
        case '/api/status': return success({ quota_per_unit: 500000 });
        default: throw new Error(`Unexpected request: ${url.pathname}`);
      }
    }));

    render(<DashboardPage />);
    const metrics = await screen.findByRole('region', { name: '关键统计' });
    expect(within(metrics).getByText('已结算费用')).toBeVisible();
    expect(within(metrics).getByText('1.500000 U')).toBeVisible();
    expect(within(metrics).getByText('请求记录数')).toBeVisible();
    expect(within(metrics).getByText('6')).toBeVisible();
    expect(within(metrics).getByText('已结算 Token')).toBeVisible();
    expect(within(metrics).getByText('150')).toBeVisible();
    expect(logQueries.some((query) => query.get('p') === '4')).toBe(true);
    expect(new Set(logQueries.map((query) => query.get('start_timestamp'))).size).toBe(1);
    expect(new Set(logQueries.map((query) => query.get('end_timestamp'))).size).toBe(1);

    fireEvent.click(screen.getByRole('button', { name: '昨天' }));
    await act(async () => undefined);
    const yesterday = logQueries.at(-1)!;
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    expect(Number(yesterday.get('end_timestamp'))).toBe(today.getTime() / 1000 - 1);
    expect(within(screen.getByRole('region', { name: '关键统计' })).getByText('1.500000 U')).toBeVisible();
  });
});
