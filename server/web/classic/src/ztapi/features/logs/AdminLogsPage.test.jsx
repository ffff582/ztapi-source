/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import React from 'react';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import AdminLogsPage from './AdminLogsPage.jsx';
import AuditLogPage from './AuditLogPage.jsx';
import { adminDownload, adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminDownload: vi.fn(), adminRequest: vi.fn() };
});

function page(items) {
  return { items, total: items.length, page: 1, page_size: 20 };
}

describe('AdminLogsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date('2026-08-05T12:00:00Z'));
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it('defaults to 24 hours and renders only the safe request projection', async () => {
    adminRequest.mockResolvedValue(
      page([
        {
          id: 7,
          created_at: 1785930000,
          status: 'success',
          type: 2,
          user_id: 8,
          username: 'alice',
          model: 'gpt-5',
          request_id: 'req-safe-7',
          latency: 2,
          prompt_tokens: 120,
          completion_tokens: 30,
          total_tokens: 150,
          quota: 420,
          billed_amount: 0.0042,
          content: 'RAW_SECRET_BODY',
          other: { access_token: 'SECRET' },
        },
      ]),
    );
    render(<AdminLogsPage />);
    expect(await screen.findByText('req-safe-7')).toBeVisible();
    expect(screen.getByText('150')).toBeVisible();
    expect(screen.getByText('2 秒')).toBeVisible();
    expect(screen.queryByText(/RAW_SECRET_BODY|SECRET/)).toBeNull();
    const url = adminRequest.mock.calls[0][0].url;
    const params = new URL(url, 'https://admin.ztapi.vip').searchParams;
    expect(Number(params.get('to')) - Number(params.get('from'))).toBe(86400);
  });

  it('filters on the server and exports exactly the active query', async () => {
    adminRequest.mockResolvedValue(page([]));
    render(<AdminLogsPage />);
    await screen.findByText('所选范围内没有请求日志。');
    fireEvent.change(
      screen.getByRole('searchbox', { name: '搜索 request ID' }),
      {
        target: { value: 'req-22' },
      },
    );
    fireEvent.change(screen.getByLabelText('请求结果'), {
      target: { value: 'error' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询日志' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenLastCalledWith(
        expect.objectContaining({
          url: expect.stringMatching(/request_id=req-22.*status=error/),
        }),
      ),
    );
    fireEvent.click(screen.getByRole('button', { name: '下载请求日志 CSV' }));
    expect(adminDownload).toHaveBeenCalledWith(
      expect.objectContaining({
        url: expect.stringMatching(
          /\/api\/admin\/request-logs\/export\?.*request_id=req-22.*status=error/,
        ),
      }),
      'ztapi-request-logs.csv',
    );
  });
});

describe('AuditLogPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('shows structured audit fields and no raw log payload', async () => {
    adminRequest.mockResolvedValue(
      page([
        {
          id: 12,
          created_at: 1785930000,
          request_id: 'req-audit-12',
          action: 'topup.reject',
          params: {
            trade_no: 'ZT-12',
            reason: '凭证无效',
            result: 'committed',
          },
          operator: {
            admin_id: 4,
            admin_username: 'root-operator',
            admin_role: 100,
          },
          content: 'RAW_AUDIT_SECRET',
        },
      ]),
    );
    render(<AuditLogPage />);
    expect(await screen.findByText('拒绝充值订单')).toBeVisible();
    expect(screen.getByText('root-operator')).toBeVisible();
    expect(screen.getByText('凭证无效')).toBeVisible();
    expect(screen.queryByText('RAW_AUDIT_SECRET')).toBeNull();
  });

  it('filters and exports audit logs', async () => {
    adminRequest.mockResolvedValue(page([]));
    render(<AuditLogPage />);
    await screen.findByText('所选范围内没有管理员审计记录。');
    fireEvent.change(screen.getByLabelText('审计动作'), {
      target: { value: 'settings.update' },
    });
    fireEvent.change(screen.getByLabelText('操作者 ID'), {
      target: { value: '4' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询审计' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenLastCalledWith(
        expect.objectContaining({
          url: expect.stringMatching(/action=settings.update.*operator_id=4/),
        }),
      ),
    );
    fireEvent.click(screen.getByRole('button', { name: '下载审计 CSV' }));
    expect(adminDownload).toHaveBeenCalledWith(
      expect.objectContaining({
        url: expect.stringContaining('/api/admin/audit-logs/export?'),
      }),
      'ztapi-audit-logs.csv',
    );
  });
});
