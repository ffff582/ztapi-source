import { cleanup, render, screen, within } from '@testing-library/react';
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
            page_size: 5,
            total: 1,
            items: [{
              timestamp: 1789100000,
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

    const row = (await screen.findByText('zt-claude-sonnet-5')).closest('tr');
    expect(row).not.toBeNull();
    const usageLog = within(row as HTMLElement);
    expect(usageLog.getByText('$0.004321')).toBeVisible();
    expect(usageLog.getByText('输入 120')).toBeVisible();
    expect(usageLog.getByText('输出 48')).toBeVisible();
    expect(usageLog.getByText('总计 168')).toBeVisible();
    expect(usageLog.getByText('2 秒')).toBeVisible();
    expect(usageLog.getByText('req-usage-clear-001')).toBeVisible();
    expect(usageLog.getByText('成功')).toBeVisible();

    const checklist = screen.getByRole('region', { name: '首次接入进度' });
    expect(within(checklist).getByText('2 / 4 已完成')).toBeVisible();
    expect(within(checklist).getByText('创建 API Key').closest('li')).toHaveAttribute('data-complete', 'true');
    expect(within(checklist).getByText('选择并测试模型').closest('li')).toHaveAttribute('data-complete', 'false');
    expect(within(checklist).getByText('发送首个请求').closest('li')).toHaveAttribute('data-complete', 'true');
    expect(within(checklist).getByText('查看费用日志').closest('li')).toHaveAttribute('data-complete', 'false');
  });
});
