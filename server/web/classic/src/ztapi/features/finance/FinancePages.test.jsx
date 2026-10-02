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
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react';
import OrdersPage from './OrdersPage.jsx';
import LedgerPage from './LedgerPage.jsx';
import { adminDownload, adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    adminDownload: vi.fn(),
    adminRequest: vi.fn(),
  };
});

const order = {
  id: 17,
  user_id: 8,
  username: 'alice',
  amount: 1000,
  money: 10.5,
  trade_no: 'ZT-20260805-17',
  payment_method: 'stripe',
  payment_provider: 'stripe',
  create_time: 1785902400,
  complete_time: 0,
  status: 'pending',
};

const ledger = {
  id: 31,
  user_id: 8,
  operator_id: 4,
  delta: 500,
  balance_before: 1000,
  balance_after: 1500,
  reason: '人工充值',
  request_id: 'req-ledger-31',
  source_type: 'admin_adjustment',
  created_at: '2026-08-05T05:00:00Z',
};

function page(items) {
  return { items, total: items.length, page: 1, page_size: 20 };
}

function deferred() {
  let resolve;
  const promise = new Promise((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
}

describe('OrdersPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('filters server data and never renders an empty operation column', async () => {
    adminRequest.mockResolvedValue(page([order]));
    render(<OrdersPage canWrite={false} />);

    expect(await screen.findByText(order.trade_no)).toBeVisible();
    expect(screen.queryByText('操作')).toBeNull();
    expect(screen.queryByRole('button', { name: /手工完成|拒绝/ })).toBeNull();

    fireEvent.change(screen.getByLabelText('订单状态'), {
      target: { value: 'pending' },
    });
    fireEvent.change(screen.getByRole('searchbox', { name: '搜索订单' }), {
      target: { value: 'ZT-17' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询订单' }));

    await waitFor(() =>
      expect(adminRequest).toHaveBeenLastCalledWith(
        expect.objectContaining({
          url: expect.stringMatching(
            /\/api\/admin\/topups\?.*keyword=ZT-17.*status=pending/,
          ),
        }),
      ),
    );
  });

  it('requires a reason, locks duplicate completion, and displays warnings', async () => {
    const mutation = deferred();
    adminRequest
      .mockResolvedValueOnce(page([order]))
      .mockReturnValueOnce(mutation.promise);
    render(<OrdersPage canWrite />);
    fireEvent.click(
      await screen.findByRole('button', {
        name: `手工完成 ${order.trade_no}`,
      }),
    );
    const dialog = screen.getByRole('dialog', { name: '手工完成订单' });
    const confirm = within(dialog).getByRole('button', {
      name: '确认完成并入账',
    });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('处理原因'), {
      target: { value: '支付已线下核验' },
    });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(
      within(dialog).getByRole('button', { name: '正在处理...' }),
    ).toBeDisabled();
    expect(
      adminRequest.mock.calls.filter(
        ([request]) => request.url === '/api/admin/topups/17/complete',
      ),
    ).toHaveLength(1);

    await act(async () =>
      mutation.resolve({
        ...order,
        status: 'success',
        warning: 'committed_cache_sync_pending',
      }),
    );
    expect(
      await screen.findByText('订单已完成，缓存同步仍在进行。'),
    ).toBeVisible();
    expect(screen.getByText('成功')).toBeVisible();
  });

  it('keeps the reason and shows the current order after a 409 conflict', async () => {
    const conflict = Object.assign(new Error('conflict'), {
      status: 409,
      data: { ...order, status: 'rejected' },
    });
    adminRequest
      .mockResolvedValueOnce(page([order]))
      .mockRejectedValueOnce(conflict);
    render(<OrdersPage canWrite />);
    fireEvent.click(
      await screen.findByRole('button', { name: `拒绝 ${order.trade_no}` }),
    );
    fireEvent.change(screen.getByLabelText('处理原因'), {
      target: { value: '支付凭证无效' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认拒绝订单' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '订单已由其他管理员处理，当前状态为已拒绝。',
    );
    expect(screen.getByLabelText('处理原因')).toHaveValue('支付凭证无效');
    expect(adminRequest).toHaveBeenCalledTimes(2);
  });

  it('keeps the newest search result and exports the active filters', async () => {
    const older = deferred();
    const newer = deferred();
    adminRequest
      .mockResolvedValueOnce(page([]))
      .mockImplementationOnce(() => older.promise)
      .mockImplementationOnce(() => newer.promise);
    render(<OrdersPage canWrite />);
    await screen.findByText('没有符合条件的充值订单。');
    const search = screen.getByRole('searchbox', { name: '搜索订单' });
    fireEvent.change(search, { target: { value: 'older' } });
    fireEvent.click(screen.getByRole('button', { name: '查询订单' }));
    fireEvent.change(search, { target: { value: 'newer' } });
    fireEvent.click(screen.getByRole('button', { name: '查询订单' }));
    await act(async () =>
      newer.resolve(page([{ ...order, id: 19, trade_no: 'newer-result' }])),
    );
    expect(await screen.findByText('newer-result')).toBeVisible();
    await act(async () =>
      older.resolve(page([{ ...order, id: 18, trade_no: 'older-result' }])),
    );
    expect(screen.queryByText('older-result')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: '下载订单 CSV' }));
    await waitFor(() =>
      expect(adminDownload).toHaveBeenCalledWith(
        expect.objectContaining({
          url: expect.stringContaining(
            '/api/admin/topups/export?keyword=newer',
          ),
        }),
        'ztapi-topups.csv',
      ),
    );
  });
});

describe('LedgerPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('filters, paginates, exports, and remains immutable', async () => {
    adminRequest.mockResolvedValue({ ...page([ledger]), total: 25 });
    render(<LedgerPage />);
    expect(await screen.findByText('req-ledger-31')).toBeVisible();
    expect(screen.queryByText('操作')).toBeNull();
    fireEvent.change(screen.getByLabelText('账本用户 ID'), {
      target: { value: '8' },
    });
    fireEvent.change(screen.getByLabelText('账本来源'), {
      target: { value: 'admin_adjustment' },
    });
    fireEvent.click(screen.getByRole('button', { name: '查询账本' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenLastCalledWith(
        expect.objectContaining({
          url: expect.stringMatching(/user_id=8.*source_type=admin_adjustment/),
        }),
      ),
    );
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));
    await waitFor(() =>
      expect(adminRequest).toHaveBeenLastCalledWith(
        expect.objectContaining({ url: expect.stringContaining('p=2') }),
      ),
    );
    fireEvent.click(screen.getByRole('button', { name: '下载账本 CSV' }));
    expect(adminDownload).toHaveBeenCalledWith(
      expect.objectContaining({
        url: expect.stringContaining('/api/admin/balance-ledger/export?'),
      }),
      'ztapi-balance-ledger.csv',
    );
  });
});
