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
import WatchedAddressesPage from './WatchedAddressesPage.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminRequest: vi.fn() };
});

const catalog = {
  receiving_address: 'TC1cha8BHG7hY9i5gCAVoJvgJqa8VLv4X2',
  max_enabled: 8,
  items: [
    {
      id: 3,
      address: 'TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu',
      label: '旧收款地址',
      enabled: true,
      operator_id: 4,
      updated_at: 1789793748,
    },
  ],
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('watched receiving addresses', () => {
  it('shows the address new orders use and states that it cannot be changed here', async () => {
    adminRequest.mockResolvedValue(catalog);

    render(<WatchedAddressesPage canWrite />);

    expect(
      await screen.findByText('TC1cha8BHG7hY9i5gCAVoJvgJqa8VLv4X2'),
    ).toBeInTheDocument();
    expect(screen.getByText(/由部署密钥决定，无法在此修改/)).toBeInTheDocument();
    expect(screen.getByText('TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu')).toBeInTheDocument();
    expect(screen.getByText('监听中')).toBeInTheDocument();
  });

  it('adds an address with the reason the audit trail records', async () => {
    adminRequest.mockResolvedValue(catalog);
    render(<WatchedAddressesPage canWrite />);
    await screen.findByText('TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu');

    fireEvent.change(screen.getByLabelText('待监听的 TRON 地址'), {
      target: { value: ' TLrsoJgfpKZrACoG73r95PAcCyr6cF4DwX ' },
    });
    fireEvent.change(screen.getByLabelText('监听地址备注'), {
      target: { value: '迁移前地址' },
    });
    fireEvent.change(screen.getByLabelText('监听地址变更原因'), {
      target: { value: '切换收款地址前保留入账' },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加监听' }));

    await waitFor(() => {
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'POST',
          url: '/api/admin/payment/watched-addresses',
        }),
      );
    });
    const call = adminRequest.mock.calls.find(
      ([config]) => config.method === 'POST',
    );
    expect(JSON.parse(call[0].body)).toEqual({
      address: 'TLrsoJgfpKZrACoG73r95PAcCyr6cF4DwX',
      label: '迁移前地址',
      reason: '切换收款地址前保留入账',
      confirm: true,
    });
  });

  // A change to what the site credits must never reach the audit trail without
  // a reason, so the page refuses to send one.
  it('refuses to disable an address without a reason', async () => {
    adminRequest.mockResolvedValue(catalog);
    render(<WatchedAddressesPage canWrite />);
    await screen.findByText('TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu');
    adminRequest.mockClear();

    fireEvent.click(screen.getByRole('button', { name: '停用' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('请先填写变更原因。');
    expect(adminRequest).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText('监听地址变更原因'), {
      target: { value: '地址已停用' },
    });
    fireEvent.click(screen.getByRole('button', { name: '停用' }));

    await waitFor(() => {
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'PUT',
          url: '/api/admin/payment/watched-addresses/3',
        }),
      );
    });
    const call = adminRequest.mock.calls.find(
      ([config]) => config.method === 'PUT',
    );
    expect(JSON.parse(call[0].body)).toEqual({
      enabled: false,
      reason: '地址已停用',
      confirm: true,
    });
  });

  it('lets an authorized administrator restore a disabled address', async () => {
    adminRequest.mockResolvedValueOnce({
      ...catalog,
      items: [{ ...catalog.items[0], enabled: false }],
    });
    render(<WatchedAddressesPage canWrite />);
    await screen.findByText('已停用');

    fireEvent.change(screen.getByLabelText('监听地址变更原因'), {
      target: { value: '恢复旧地址收款监听' },
    });
    fireEvent.click(screen.getByRole('button', { name: '启用' }));

    await waitFor(() => {
      expect(adminRequest).toHaveBeenCalledWith(
        expect.objectContaining({
          method: 'PUT',
          url: '/api/admin/payment/watched-addresses/3',
        }),
      );
    });
    const call = adminRequest.mock.calls.find(
      ([config]) => config.method === 'PUT',
    );
    expect(JSON.parse(call[0].body)).toEqual({
      enabled: true,
      reason: '恢复旧地址收款监听',
      confirm: true,
    });
  });

  it('hides every control from an administrator who may only read', async () => {
    adminRequest.mockResolvedValue(catalog);

    render(<WatchedAddressesPage canWrite={false} />);

    await screen.findByText('TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu');
    expect(screen.queryByRole('button', { name: '添加监听' })).toBeNull();
    expect(screen.queryByRole('button', { name: '停用' })).toBeNull();
    expect(screen.queryByLabelText('待监听的 TRON 地址')).toBeNull();
  });

  it('reports a rejected address instead of pretending it was added', async () => {
    adminRequest.mockResolvedValueOnce(catalog);
    render(<WatchedAddressesPage canWrite />);
    await screen.findByText('TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpu');

    adminRequest.mockRejectedValueOnce(
      new Error('收款地址无效，请检查是否为有效的 TRON 地址。'),
    );
    fireEvent.change(screen.getByLabelText('待监听的 TRON 地址'), {
      target: { value: 'TTn3KVXkxSi9eHnpdLBFL1PpncMZmm6Tpv' },
    });
    fireEvent.change(screen.getByLabelText('监听地址变更原因'), {
      target: { value: '试一下' },
    });
    fireEvent.click(screen.getByRole('button', { name: '添加监听' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '收款地址无效',
    );
  });
});
