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
  within,
} from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import AdminSettingsPage from './AdminSettingsPage.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, adminRequest: vi.fn() };
});

const settings = {
  brand_name: 'ZTAPI',
  announcement: '计划维护时间：周日 02:00',
  registration_enabled: true,
  model_publication: {
    published_count: 6,
    manage_path: '/models',
    read_only: true,
  },
  payments: [{ provider: 'stripe', configured: true, minimum_topup: 2 }],
};

function renderPage() {
  return render(
    <MemoryRouter>
      <AdminSettingsPage />
    </MemoryRouter>,
  );
}

describe('AdminSettingsPage', () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it('shows only supported settings and configured non-secret payments', async () => {
    adminRequest.mockResolvedValue(settings);
    renderPage();
    expect(await screen.findByDisplayValue('ZTAPI')).toBeVisible();
    expect(screen.getByText('已公开 6 个模型')).toBeVisible();
    expect(screen.getByRole('link', { name: '管理公开模型' })).toHaveAttribute(
      'href',
      '/models',
    );
    expect(screen.getByText('Stripe')).toBeVisible();
    expect(
      screen.queryByText(/USDT|支付宝|微信|API 密钥|Webhook Secret/),
    ).toBeNull();
    expect(screen.queryByLabelText(/密钥|Secret/)).toBeNull();
  });

  it('confirms exact values and locks duplicate setting submissions', async () => {
    let resolvePatch;
    const patch = new Promise((resolve) => {
      resolvePatch = resolve;
    });
    adminRequest.mockResolvedValueOnce(settings).mockReturnValueOnce(patch);
    renderPage();
    const brand = await screen.findByLabelText('品牌名称');
    fireEvent.change(brand, { target: { value: 'ZTAPI Pro' } });
    fireEvent.click(screen.getByRole('button', { name: '保存品牌名称' }));
    const dialog = screen.getByRole('dialog', { name: '确认保存设置' });
    expect(within(dialog).getByText('ZTAPI')).toBeVisible();
    expect(within(dialog).getByText('ZTAPI Pro')).toBeVisible();
    const confirm = within(dialog).getByRole('button', { name: '确认保存' });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText('保存原因'), {
      target: { value: '品牌升级' },
    });
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(adminRequest).toHaveBeenCalledTimes(2);
    expect(adminRequest).toHaveBeenLastCalledWith({
      method: 'PATCH',
      url: '/api/admin/settings/brand_name',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        value: 'ZTAPI Pro',
        expected_value: 'ZTAPI',
        reason: '品牌升级',
      }),
    });
    expect(
      within(dialog).getByRole('button', { name: '正在保存...' }),
    ).toBeDisabled();
    resolvePatch({ ...settings, brand_name: 'ZTAPI Pro' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('keeps the draft and does not retry a 409 conflict', async () => {
    adminRequest
      .mockResolvedValueOnce(settings)
      .mockRejectedValueOnce(
        Object.assign(new Error('conflict'), { status: 409 }),
      );
    renderPage();
    const announcement = await screen.findByLabelText('运营公告');
    fireEvent.change(announcement, { target: { value: '新的维护公告' } });
    fireEvent.click(screen.getByRole('button', { name: '保存运营公告' }));
    fireEvent.change(screen.getByLabelText('保存原因'), {
      target: { value: '更新维护窗口' },
    });
    fireEvent.click(screen.getByRole('button', { name: '确认保存' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '设置已被其他管理员修改，请刷新后重新确认。',
    );
    expect(screen.getByLabelText('运营公告')).toHaveValue('新的维护公告');
    expect(adminRequest).toHaveBeenCalledTimes(2);
  });

  it('renders loading, error retry, and no-payment empty states', async () => {
    adminRequest
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce({ ...settings, payments: [] });
    renderPage();
    expect(screen.getByRole('status')).toHaveTextContent('正在加载系统设置');
    expect(await screen.findByRole('alert')).toHaveTextContent(
      '系统设置加载失败。',
    );
    fireEvent.click(screen.getByRole('button', { name: '重试加载设置' }));
    expect(
      await screen.findByText('当前没有已配置的支付提供方。'),
    ).toBeVisible();
  });
});
