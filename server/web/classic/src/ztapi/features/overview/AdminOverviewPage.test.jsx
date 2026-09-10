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
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import AdminOverviewPage from './AdminOverviewPage.jsx';
import { adminRequest } from '../../auth/admin-session.js';

vi.mock('../../auth/admin-session.js', () => ({ adminRequest: vi.fn() }));

const responseFixture = {
  range: { from: 1754006400, to: 1754092800 },
  requests: { total: 12, success: 10, failure: 2 },
  billed_quota: 480,
  user_balance_total: 1200,
  pending_top_ups: 1,
  series: [
    { start: 1754006400, requests: 8, billed_quota: 320 },
    { start: 1754092800, requests: 4, billed_quota: 160 },
  ],
  channel_statuses: [
    { status: 1, count: 3 },
    { status: 2, count: 1 },
  ],
  recent_failures: [
    { id: 8, occurred_at: 1754050000, channel_id: 3, model_name: 'gpt-test', summary: '请求失败' },
  ],
  recent_audit_operations: [
    { id: 5, occurred_at: 1754040000, action: 'channel.update' },
  ],
};

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

describe('AdminOverviewPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(cleanup);

  it('keeps the loading skeleton in stable dashboard dimensions', () => {
    adminRequest.mockReturnValue(new Promise(() => {}));
    render(<AdminOverviewPage />);

    expect(screen.getByTestId('overview-loading')).toHaveClass('ztapi-overview-skeleton');
    expect(screen.getByTestId('overview-loading')).toHaveStyle({ minHeight: '360px' });
  });

  it('renders truthful zero data as an empty operations state', async () => {
    adminRequest.mockResolvedValue({
      ...responseFixture,
      requests: { total: 0, success: 0, failure: 0 },
      billed_quota: 0,
      user_balance_total: 0,
      pending_top_ups: 0,
      series: [],
      channel_statuses: [],
      recent_failures: [],
      recent_audit_operations: [],
    });
    render(<AdminOverviewPage />);

    expect(await screen.findByText('所选范围内暂无运营记录。')).toBeVisible();
    expect(screen.getByText('请求数').parentElement).toHaveTextContent('0请求数');
    expect(screen.queryByText(/%/)).toBeNull();
  });

  it('shows a safe error and retries the authenticated overview request', async () => {
    adminRequest.mockRejectedValueOnce(new Error('upstream body: api_key=secret')).mockResolvedValueOnce(responseFixture);
    render(<AdminOverviewPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent('无法加载运营概览。');
    expect(screen.queryByText('api_key=secret')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: '重试概览' }));

    await waitFor(() => expect(screen.getByText('请求数').parentElement).toHaveTextContent('12请求数'));
    expect(adminRequest).toHaveBeenCalledTimes(2);
    expect(adminRequest).toHaveBeenLastCalledWith(expect.objectContaining({ url: expect.stringMatching(/^\/api\/admin\/overview\?from=\d+&to=\d+$/) }));
  });

  it('treats a malformed successful response as a safe loading error', async () => {
    adminRequest.mockResolvedValue(undefined);
    render(<AdminOverviewPage />);

    expect(await screen.findByRole('alert')).toHaveTextContent('无法加载运营概览。');
  });

  it('keeps the latest refresh when an older request resolves later', async () => {
    const older = deferred();
    const newer = deferred();
    adminRequest.mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise);
    render(<AdminOverviewPage />);

    fireEvent.click(screen.getByRole('button', { name: '刷新概览' }));
    newer.resolve({ ...responseFixture, requests: { total: 99, success: 99, failure: 0 } });
    await waitFor(() => expect(screen.getByText('请求数').parentElement).toHaveTextContent('99请求数'));

    older.reject(new Error('stale upstream body api_key=secret'));
    await Promise.resolve();
    expect(screen.getByText('请求数').parentElement).toHaveTextContent('99请求数');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(adminRequest.mock.calls[0][0].signal.aborted).toBe(true);
  });

  it('keeps newer data when an older request succeeds later', async () => {
    const older = deferred();
    const newer = deferred();
    adminRequest.mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise);
    render(<AdminOverviewPage />);

    fireEvent.click(screen.getByRole('button', { name: '刷新概览' }));
    newer.resolve({ ...responseFixture, requests: { total: 99, success: 99, failure: 0 } });
    await waitFor(() => expect(screen.getByText('请求数').parentElement).toHaveTextContent('99请求数'));

    older.resolve({ ...responseFixture, requests: { total: 1, success: 1, failure: 0 } });
    await Promise.resolve();

    expect(screen.getByText('请求数').parentElement).toHaveTextContent('99请求数');
  });

  it('aborts the active overview request on unmount', () => {
    const pending = deferred();
    adminRequest.mockReturnValueOnce(pending.promise);
    const { unmount } = render(<AdminOverviewPage />);
    const request = adminRequest.mock.calls[0][0];

    unmount();

    expect(request.signal.aborted).toBe(true);
  });

  it('renders supplied values and never invents a trend comparison', async () => {
    adminRequest.mockResolvedValue(responseFixture);
    render(<AdminOverviewPage />);

    expect((await screen.findByText('请求数')).parentElement).toHaveTextContent('12请求数');
    expect(screen.getByText('已计费额度').parentElement).toHaveTextContent('480已计费额度');
    expect(screen.getByText('状态 1')).toBeVisible();
    expect(screen.getByLabelText('2025-08-01：8 次请求，320 已计费额度')).toBeVisible();
    expect(screen.getByText('请求失败')).toBeVisible();
    expect(screen.getByText('channel.update')).toBeVisible();
    expect(screen.queryByText(/%/)).toBeNull();
  });
});
