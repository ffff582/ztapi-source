import { act, cleanup, render, screen } from '@testing-library/react';
import { StrictMode } from 'react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from '../../api/client';
import { spendingApi, SpendingError } from '../../api/spending';
import { spendingInbox } from './spending.fixture';
import { useSpendingInbox } from './useSpendingInbox';

vi.mock('../../api/spending', async (original) => ({ ...await original<typeof import('../../api/spending')>(), spendingApi: { alerts: vi.fn(), read: vi.fn() } }));
function Probe() {
  const inbox = useSpendingInbox();
  return <output>{inbox.data ? `${inbox.data.unread_count}:${inbox.data.items[0]?.id ?? 'empty'}` : inbox.status}</output>;
}
const login = (id = 7) => setAuthSession({ access_token: `token-${id}`, expires_in: 900, user: { id, username: 'test', role: 1, group: 'default' } });
beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  login();
  vi.mocked(spendingApi.alerts).mockResolvedValue(spendingInbox);
});
afterEach(() => { cleanup(); clearAuthSession(); vi.useRealTimers(); vi.clearAllMocks(); });
const flush = async () => { await act(async () => { await Promise.resolve(); }); };

it('shares a single poll, throttles visibility/remount to 60 seconds, and pauses while hidden', async () => {
  const first = render(<><Probe /><Probe /></>); await flush();
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
  await act(async () => { vi.advanceTimersByTime(59_999); });
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
  first.unmount(); render(<Probe />); await flush();
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  act(() => document.dispatchEvent(new Event('visibilitychange')));
  await act(async () => { vi.advanceTimersByTime(180_000); });
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  act(() => document.dispatchEvent(new Event('visibilitychange'))); await flush();
  expect(spendingApi.alerts).toHaveBeenCalledTimes(2);
});

it('does not overlap slow polling calls', async () => {
  let resolve!: (value: typeof spendingInbox) => void;
  vi.mocked(spendingApi.alerts).mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  render(<Probe />);
  await act(async () => { vi.advanceTimersByTime(180_000); });
  act(() => document.dispatchEvent(new Event('visibilitychange')));
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
  await act(async () => resolve(spendingInbox));
  expect(screen.getByText('1:12')).toBeVisible();
});

it('drops old-owner and same-owner old-session responses and clears cache at logout', async () => {
  let resolve!: (value: typeof spendingInbox) => void;
  render(<Probe />); await flush();
  expect(screen.getByText('1:12')).toBeVisible();
  vi.mocked(spendingApi.alerts).mockReturnValueOnce(new Promise((done) => { resolve = done; })).mockReturnValue(new Promise(() => {}));
  act(() => login());
  expect(screen.queryByText('1:12')).toBeNull();
  act(() => login(8));
  await act(async () => resolve(spendingInbox));
  expect(screen.queryByText('1:12')).toBeNull();
  act(() => clearAuthSession());
  expect(screen.getByText('idle')).toBeVisible();
});

it('does not poll an initially hidden page', async () => {
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
  render(<Probe />); await flush();
  expect(spendingApi.alerts).not.toHaveBeenCalled();
});

it('retains the single initial request through StrictMode subscription replay', async () => {
  vi.mocked(spendingApi.alerts).mockImplementation((signal) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => resolve(spendingInbox), 100);
    signal?.addEventListener('abort', () => { clearTimeout(timer); reject(new Error('aborted')); }, { once: true });
  }));
  render(<StrictMode><Probe /></StrictMode>);
  await act(async () => { vi.advanceTimersByTime(100); });
  expect(screen.getByText('1:12')).toBeVisible();
  expect(spendingApi.alerts).toHaveBeenCalledTimes(1);
});

it.each([[401, 'session-expired'], [503, 'error']])('exposes optional HTTP %s distinctly without mutating the main session', async (status, expected) => {
  const session = getAuthSession();
  vi.mocked(spendingApi.alerts).mockRejectedValue(new SpendingError(Number(status)));
  render(<Probe />); await flush();
  expect(screen.getByText(expected)).toBeVisible();
  await act(async () => { vi.advanceTimersByTime(15 * 60_000); });
  expect(screen.getByText(expected)).toBeVisible();
  expect(getAuthSession()).toBe(session);
});
