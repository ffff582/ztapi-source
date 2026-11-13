import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { clearAuthSession, setAuthSession } from '../../api/client';
import { spendingApi, SpendingError } from '../../api/spending';
import { spendingInbox } from '../../features/spending/spending.fixture';
import { ConsoleHeader } from './ConsoleHeader';
vi.mock('../../auth/session', () => ({ useAuth: () => ({ user: { username: 'alice' }, logout: vi.fn() }) }));
vi.mock('../../api/spending', async (original) => ({ ...await original<typeof import('../../api/spending')>(), spendingApi: { alerts: vi.fn(), read: vi.fn() } }));
beforeEach(() => {
  setAuthSession({ access_token: 'test', expires_in: 900, user: { id: 7, username: 'alice', role: 1, group: 'default' } });
  vi.mocked(spendingApi.alerts).mockResolvedValue(spendingInbox);
  vi.mocked(spendingApi.read).mockResolvedValue(undefined);
});
afterEach(() => { cleanup(); clearAuthSession(); vi.clearAllMocks(); });

it('keeps a direct referral entry in the shared user header', async () => {
  render(<MemoryRouter><ConsoleHeader /></MemoryRouter>);
  await screen.findByRole('button', { name: '消费通知' });
  expect(screen.getByRole('link', { name: '推广返利' })).toHaveAttribute('href', '/console/referrals');
});

it.each(['/console', '/console/workbench/text'])('offers the shared bell in %s with keyboard close and focus restoration', async (path) => {
  render(<MemoryRouter initialEntries={[path]}><ConsoleHeader /></MemoryRouter>);
  const bell = screen.getByRole('button', { name: '消费通知' });
  expect(bell).toHaveAttribute('title', '消费通知');
  fireEvent.click(bell);
  const dialog = screen.getByRole('dialog', { name: '消费通知' });
  expect(await screen.findByRole('button', { name: /今日消费达到阈值/ })).toBeVisible();
  expect(screen.getByRole('link', { name: '全部记录与设置' })).toHaveAttribute('href', '/console/spending');
  fireEvent.keyDown(dialog, { key: 'Escape' });
  expect(screen.queryByRole('dialog')).toBeNull();
  expect(bell).toHaveFocus();
});

it('keeps workbench navigation intact when optional notifications fail', async () => {
  vi.mocked(spendingApi.alerts).mockRejectedValue(new SpendingError(503));
  render(<MemoryRouter><ConsoleHeader /></MemoryRouter>);
  fireEvent.click(screen.getByRole('button', { name: '消费通知' }));
  expect(await screen.findByText('通知暂不可用，请稍后重试。')).toBeVisible();
  expect(screen.getByRole('link', { name: '工作台' })).toHaveAttribute('href', '/console/workbench/text');
  expect(screen.getByRole('button', { name: '退出登录' })).toBeEnabled();
});

it('offers only an explicit warned reload when the workbench bell receives 401', async () => {
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  vi.mocked(spendingApi.alerts).mockRejectedValue(new SpendingError(401));
  render(<MemoryRouter initialEntries={['/console/workbench/text']}><ConsoleHeader /></MemoryRouter>);
  fireEvent.click(screen.getByRole('button', { name: '消费通知' }));
  expect(await screen.findByText('消费预警登录凭证已过期或未被接受，主会话未退出。')).toBeVisible();
  expect(confirm).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: '重新确认登录并刷新页面' }));
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(screen.getByRole('button', { name: '退出登录' })).toBeEnabled();
  expect(screen.queryByText('通知暂不可用，请稍后重试。')).toBeNull();
});
