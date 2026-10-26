import React from 'react';
import '@testing-library/jest-dom/vitest';
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import OverviewPage from './OverviewPage.jsx';
import { adminRequest, adminDownload } from '../../auth/admin-session.js';
vi.mock('../../auth/admin-session.js', () => ({
  adminRequest: vi.fn(),
  adminDownload: vi.fn(),
}));
const report = {
  month: '2026-09',
  period_closed: true,
  confirmed: false,
  revenue: '20.000000',
  known_revenue: '20.000000',
  actual_cost: null,
  gross_profit: null,
  receipts_total: '100.000000',
  charged_quota: 10500000,
  quota_per_unit: 500000,
  pending_requests: 2,
  issue_count: 1,
  issues: [{ code: 'supplier_cost_missing', ref: 'channel:2' }],
  receipts: [],
  costs: [],
  manual_funding: [],
  channels: [{ channel_id: 2, name: '号池', attempts: 3 }],
};
beforeEach(() => {
  vi.clearAllMocks();
  adminRequest.mockResolvedValue(report);
});
afterEach(cleanup);
it('shows unknown profit as pending rather than zero', async () => {
  render(<OverviewPage canWrite={false} />);
  expect(await screen.findByText('毛利待核对')).toBeVisible();
  expect(screen.getByText('100.000000')).toBeVisible();
  expect(screen.queryByRole('button', { name: /录入.*成本/ })).toBeNull();
});
it('shows confirmed gross profit and loads another month', async () => {
  adminRequest.mockResolvedValue({
    ...report,
    confirmed: true,
    actual_cost: '15.000000',
    gross_profit: '5.000000',
    margin: '25.00',
    issue_count: 0,
    issues: [],
  });
  render(<OverviewPage canWrite />);
  expect(await screen.findByText('5.000000')).toBeVisible();
  fireEvent.change(screen.getByLabelText('统计月份'), {
    target: { value: '2026-09' },
  });
  await waitFor(() =>
    expect(adminRequest).toHaveBeenLastCalledWith(
      expect.objectContaining({
        url: '/api/admin/finance-reporting/report?month=2026-09',
      }),
    ),
  );
});
it('offers evidence-only cost entry without touching wallet endpoints', async () => {
  render(<OverviewPage canWrite />);
  fireEvent.click(await screen.findByRole('button', { name: '录入号池成本' }));
  expect(screen.getByRole('dialog', { name: '上游实际成本' })).toBeVisible();
  expect(screen.getByLabelText('原币实际消耗')).toHaveValue(null);
  expect(screen.getByLabelText('账单凭证')).toHaveValue('');
  expect(
    screen.getByLabelText('确认已覆盖整月本平台全部凭据、失败重试和测试消耗'),
  ).not.toBeChecked();
});
it('does not retain stale financial data after a load fails', async () => {
  render(<OverviewPage canWrite={false} />);
  await screen.findByText('100.000000');
  adminRequest.mockRejectedValueOnce(new Error('offline'));
  fireEvent.click(screen.getByRole('button', { name: '刷新财务数据' }));
  expect(await screen.findByRole('alert')).toHaveTextContent(
    '财务数据加载失败',
  );
  expect(screen.queryByText('100.000000')).toBeNull();
});
it('exports with a dated CSV filename', async () => {
  render(<OverviewPage />);
  await screen.findByText('100.000000');
  fireEvent.click(screen.getByRole('button', { name: '导出本月财务报表' }));
  await waitFor(() =>
    expect(adminDownload).toHaveBeenCalledWith(
      expect.objectContaining({
        url: expect.stringContaining('/export?month='),
      }),
      expect.stringMatching(/^ztapi-finance-\d{4}-\d{2}\.csv$/),
    ),
  );
});

it('keeps dialog keyboard focus inside and restores the opener', async () => {
  render(<OverviewPage canWrite />);
  const opener = await screen.findByRole('button', { name: '录入号池成本' });
  opener.focus();
  fireEvent.click(opener);
  const dialog = screen.getByRole('dialog');
  fireEvent.keyDown(dialog, { key: 'Tab', shiftKey: true });
  expect(screen.getByRole('button', { name: '保存财务凭证' })).toHaveFocus();
  fireEvent.click(screen.getByRole('button', { name: '关闭财务凭证' }));
  expect(opener).toHaveFocus();
});
