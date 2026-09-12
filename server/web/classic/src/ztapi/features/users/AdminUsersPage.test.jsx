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
import AdminUsersPage from './AdminUsersPage.jsx';
import BalanceAdjustmentDialog from './BalanceAdjustmentDialog.jsx';
import StaffRolesPage from './StaffRolesPage.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminRequest: vi.fn() };
});

const user = {
  id: 7,
  username: 'alice',
  display_name: 'Alice',
  email: 'alice@example.com',
  role: 1,
  status: 1,
  group: 'default',
  quota: 1200,
  used_quota: 300,
  request_count: 9,
  remark: '重点客户',
  created_at: 1785800000,
  last_login_at: 1785870000,
};

function page(items = [user]) {
  return { items, total: items.length, page: 1, page_size: 20 };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

describe('AdminUsersPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('searches users and completes a reasoned idempotent balance adjustment', async () => {
    let detailReads = 0;
    let ledgerReads = 0;
    adminRequest.mockImplementation(async (request) => {
      if (request.url.startsWith('/api/admin/users?')) return page();
      if (request.url === '/api/admin/users/7') {
        detailReads += 1;
        return user;
      }
      if (request.url.startsWith('/api/admin/users/7/balance-ledger?')) {
        ledgerReads += 1;
        return {
          items: [
            {
              id: 21,
              delta: 200,
              balance_before: 1000,
              balance_after: 1200,
              reason: '首次充值',
              created_at: '2026-08-05T05:00:00Z',
            },
          ],
          total: 1,
        };
      }
      if (
        request.url === '/api/admin/users/7/balance-adjustments' &&
        request.method === 'POST'
      ) {
        return { id: 22, applied: true };
      }
      throw new Error(
        `unexpected request ${request.method || 'GET'} ${request.url}`,
      );
    });

    render(<AdminUsersPage canAdjustBalance canChangeStatus canViewLedger />);

    expect(await screen.findByText('alice')).toBeVisible();
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索用户' }), {
      target: { value: 'alice@example.com' },
    });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining('keyword=alice%40example.com'),
        }),
      ),
    );

    fireEvent.click(screen.getByRole('button', { name: '查看 alice' }));
    expect(
      await screen.findByRole('dialog', { name: '用户详情' }),
    ).toBeVisible();
    expect(await screen.findByText('首次充值')).toBeVisible();

    fireEvent.click(screen.getByRole('button', { name: '调整余额' }));
    fireEvent.change(screen.getByLabelText('调整金额'), {
      target: { value: '500' },
    });
    fireEvent.change(screen.getByLabelText('调整原因'), {
      target: { value: '线下充值到账' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认调整' }));

    await waitFor(() => {
      const request = adminRequest.mock.calls
        .map(([value]) => value)
        .find((value) => value.url.endsWith('/balance-adjustments'));
      expect(request).toBeTruthy();
      const body = JSON.parse(request.body);
      expect(body).toMatchObject({ delta: 500, reason: '线下充值到账' });
      expect(body.idempotency_key).toEqual(expect.any(String));
      expect(body.idempotency_key.length).toBeGreaterThan(10);
    });
    await waitFor(() => expect(detailReads).toBeGreaterThan(1));
    expect(ledgerReads).toBeGreaterThan(1);
  });

  it('lets support change common-user status without exposing balance controls', async () => {
    adminRequest.mockImplementation(async (request) => {
      if (request.url.startsWith('/api/admin/users?')) return page();
      if (request.url === '/api/admin/users/7' && !request.method) return user;
      if (request.url === '/api/admin/users/7/status')
        return { ...user, status: 2 };
      throw new Error(
        `unexpected request ${request.method || 'GET'} ${request.url}`,
      );
    });

    render(<AdminUsersPage canChangeStatus />);
    fireEvent.click(await screen.findByRole('button', { name: '查看 alice' }));
    expect(
      await screen.findByRole('dialog', { name: '用户详情' }),
    ).toBeVisible();
    expect(screen.queryByRole('button', { name: '调整余额' })).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: '停用用户' }));
    fireEvent.change(screen.getByLabelText('状态变更原因'), {
      target: { value: '客户申请暂停' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认停用' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith({
        method: 'PATCH',
        url: '/api/admin/users/7/status',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          status: 2,
          expected_status: 1,
          reason: '客户申请暂停',
        }),
      }),
    );
  });

  it('keeps the newest search result when an older request finishes last', async () => {
    const older = deferred();
    const newer = deferred();
    adminRequest.mockImplementation(async (request) => {
      if (!request.url.includes('keyword=')) return page([]);
      if (request.url.includes('keyword=older')) return older.promise;
      if (request.url.includes('keyword=newer')) return newer.promise;
      throw new Error(`unexpected request ${request.url}`);
    });

    render(<AdminUsersPage />);
    await screen.findByText('没有符合条件的用户。');

    const searchbox = screen.getByRole('searchbox', { name: '搜索用户' });
    fireEvent.change(searchbox, { target: { value: 'older' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining('keyword=older'),
        }),
      ),
    );
    fireEvent.change(searchbox, { target: { value: 'newer' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining('keyword=newer'),
        }),
      ),
    );

    newer.resolve(page([{ ...user, id: 9, username: 'newer-result' }]));
    expect(await screen.findByText('newer-result')).toBeVisible();
    older.resolve(page([{ ...user, id: 8, username: 'older-result' }]));
    await waitFor(() => expect(screen.queryByText('older-result')).toBeNull());
    expect(screen.getByText('newer-result')).toBeVisible();
  });
});

describe('BalanceAdjustmentDialog', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('cannot be closed while a balance adjustment is in flight', async () => {
    const adjustment = deferred();
    const onClose = vi.fn();
    const onSuccess = vi.fn().mockResolvedValue(undefined);
    adminRequest.mockReturnValue(adjustment.promise);

    render(
      <BalanceAdjustmentDialog
        user={user}
        onClose={onClose}
        onSuccess={onSuccess}
      />,
    );
    fireEvent.change(screen.getByLabelText('调整金额'), {
      target: { value: '100' },
    });
    fireEvent.change(screen.getByLabelText('调整原因'), {
      target: { value: '人工充值' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认调整' }));
    expect(
      await screen.findByRole('button', { name: '正在提交...' }),
    ).toBeDisabled();

    for (const button of screen.getAllByRole('button', {
      name: '关闭余额调整',
    })) {
      expect(button).toBeDisabled();
      fireEvent.click(button);
    }
    const cancel = screen.getByRole('button', { name: '取消' });
    expect(cancel).toBeDisabled();
    fireEvent.click(cancel);
    expect(onClose).not.toHaveBeenCalled();

    adjustment.resolve({ id: 31, applied: true });
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });
});

describe('StaffRolesPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('requires an explicit permission-impact confirmation before assigning a role', async () => {
    const staff = { ...user, id: 11, username: 'bob', role: 10 };
    adminRequest.mockImplementation(async (request) => {
      if (request.url.startsWith('/api/admin/staff?')) return page([staff]);
      if (request.url === '/api/admin/staff/11/role') {
        return { ...staff, role: 2 };
      }
      throw new Error(
        `unexpected request ${request.method || 'GET'} ${request.url}`,
      );
    });

    render(<StaffRolesPage />);
    fireEvent.click(
      await screen.findByRole('button', { name: '修改 bob 的角色' }),
    );
    fireEvent.change(screen.getByLabelText('新角色'), {
      target: { value: '2' },
    });
    fireEvent.change(screen.getByLabelText('变更原因'), {
      target: { value: '调整为客服轮班' },
    });
    expect(
      screen.getByText(/将获得用户读取、用户状态管理和日志读取权限/),
    ).toBeVisible();
    fireEvent.click(screen.getByRole('button', { name: '确认角色变更' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith({
        method: 'PATCH',
        url: '/api/admin/staff/11/role',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          role: 2,
          expected_role: 10,
          reason: '调整为客服轮班',
        }),
      }),
    );
  });

  it('keeps the newest staff search result when an older request finishes last', async () => {
    const older = deferred();
    const newer = deferred();
    adminRequest.mockImplementation(async (request) => {
      if (!request.url.includes('keyword=')) return page([]);
      if (request.url.includes('keyword=older')) return older.promise;
      if (request.url.includes('keyword=newer')) return newer.promise;
      throw new Error(`unexpected request ${request.url}`);
    });

    render(<StaffRolesPage />);
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.not.stringContaining('keyword='),
        }),
      ),
    );
    const searchbox = screen.getByRole('searchbox', { name: '搜索员工' });
    fireEvent.change(searchbox, { target: { value: 'older' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining('keyword=older'),
        }),
      ),
    );
    fireEvent.change(searchbox, { target: { value: 'newer' } });
    fireEvent.click(screen.getByRole('button', { name: '搜索' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining('keyword=newer'),
        }),
      ),
    );

    newer.resolve(
      page([{ ...user, id: 19, username: 'newer-staff', role: 2 }]),
    );
    expect(await screen.findByText('newer-staff')).toBeVisible();
    older.resolve(
      page([{ ...user, id: 18, username: 'older-staff', role: 2 }]),
    );
    await waitFor(() => expect(screen.queryByText('older-staff')).toBeNull());
    expect(screen.getByText('newer-staff')).toBeVisible();
  });
});
