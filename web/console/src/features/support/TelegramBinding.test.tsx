import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { supportApi, SupportError, type BindingView } from '../../api/support';
import { TelegramBinding } from './TelegramBinding';

vi.mock('../../api/support', async (original) => ({
  ...await original<typeof import('../../api/support')>(),
  supportApi: { telegramBinding: vi.fn(), updateTelegramBinding: vi.fn() },
}));
const pending = () => ({ id: 'challenge-1', claimed: false, expires_at: new Date(Date.now() + 300_000).toISOString() });
const claimed = () => ({ ...pending(), claimed: true, telegram_id: 987654321, username: 'fixture_z', display_name: '合成用户' });
const unbound: BindingView = { enabled: true, bound: false };
const bound: BindingView = { enabled: true, bound: true, telegram_id: 987654321, username: 'fixture_z' };
const read = vi.mocked(supportApi.telegramBinding);
const mutate = vi.mocked(supportApi.updateTelegramBinding);
const flush = async () => { await act(async () => { await Promise.resolve(); }); };
beforeEach(() => { vi.resetAllMocks(); read.mockResolvedValue(unbound); });
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });

describe('Telegram binding consent boundary', () => {
  it('keeps a fresh link usable through unclaimed refreshes until the challenge expires', async () => {
    vi.useFakeTimers();
    const challenge = pending();
    mutate.mockResolvedValue({ ...unbound, pending: challenge, binding_url: 'https://t.me/fixture_bot?start=bind_SYNTHETIC' });
    render(<TelegramBinding />); await flush();
    fireEvent.click(screen.getByRole('button', { name: '绑定 Telegram' })); await flush();
    read.mockResolvedValue({ ...unbound, pending: challenge });
    await act(async () => { await vi.advanceTimersByTimeAsync(4000); });
    expect(screen.getByRole('link', { name: '前往小 Z 认领' })).toBeInTheDocument();
    await act(async () => { await vi.advanceTimersByTimeAsync(300_000); });
    expect(screen.queryByRole('link', { name: '前往小 Z 认领' })).not.toBeInTheDocument();
  });

  it('never overlaps a pending refresh with another read or mutation', async () => {
    vi.useFakeTimers();
    read.mockResolvedValue({ ...unbound, pending: claimed() });
    render(<TelegramBinding />); await flush();
    let finish!: (value: BindingView) => void;
    read.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => { await vi.advanceTimersByTimeAsync(16_000); });
    expect(read).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('button', { name: '刷新绑定状态' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '取消绑定' })).toBeDisabled();
    await act(async () => { finish(unbound); });
    await act(async () => { await vi.advanceTimersByTimeAsync(8000); });
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('creates an expiring bot link, but never persists its token or confirms automatically', async () => {
    mutate.mockResolvedValue({ ...unbound, pending: pending(), binding_url: 'https://t.me/fixture_bot?start=bind_SYNTHETIC' });
    const storage = vi.spyOn(Storage.prototype, 'setItem');
    render(<TelegramBinding />);
    fireEvent.click(await screen.findByRole('button', { name: '绑定 Telegram' }));
    const link = await screen.findByRole('link', { name: '前往小 Z 认领' });
    expect(link).toHaveAttribute('href', 'https://t.me/fixture_bot?start=bind_SYNTHETIC');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.getByText(/尚未绑定/)).toBeInTheDocument();
    expect(storage).not.toHaveBeenCalled();
    read.mockResolvedValue({ ...unbound, pending: claimed() });
    fireEvent.click(screen.getByRole('button', { name: '刷新绑定状态' }));
    await screen.findByText(/987654321/);
    expect(screen.queryByRole('link', { name: '前往小 Z 认领' })).not.toBeInTheDocument();
    expect(mutate).toHaveBeenCalledTimes(1);
    const confirm = screen.getByRole('button', { name: '确认绑定' });
    expect(confirm).toBeDisabled();
    fireEvent.click(screen.getByRole('checkbox', { name: /我确认这是我的 Telegram 账号/ }));
    expect(confirm).toBeDisabled();
    fireEvent.click(screen.getByRole('checkbox', { name: /同意接收工单状态通知/ }));
    mutate.mockResolvedValue(bound);
    fireEvent.click(confirm);
    await screen.findByText('已绑定');
    expect(mutate.mock.calls[1][0]).toMatchObject({ action: 'confirm', challenge_id: 'challenge-1', idempotency_key: expect.any(String) });
  });

  it.each(['cancel', 'unbind'] as const)('requires a separate stateful confirmation for %s', async (action) => {
    read.mockResolvedValue(action === 'unbind' ? bound : { ...unbound, pending: pending() });
    mutate.mockResolvedValue(unbound);
    render(<TelegramBinding />);
    fireEvent.click(await screen.findByRole('button', { name: action === 'unbind' ? '解除绑定' : '取消绑定' }));
    expect(mutate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: '保留当前状态' }));
    expect(mutate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: action === 'unbind' ? '解除绑定' : '取消绑定' }));
    fireEvent.click(screen.getByRole('button', { name: action === 'unbind' ? '确认解除绑定' : '确认取消绑定' }));
    await screen.findByRole('button', { name: '绑定 Telegram' });
    expect(mutate).toHaveBeenCalledExactlyOnceWith({ action, idempotency_key: expect.any(String), ...(action === 'cancel' ? { challenge_id: 'challenge-1' } : {}) });
  });

  it('expires locally, removes the raw link, and disables confirmation without auto-mutation', async () => {
    vi.useFakeTimers();
    read.mockResolvedValue({ ...unbound, pending: { ...claimed(), expires_at: new Date(Date.now() + 1000).toISOString() } });
    render(<TelegramBinding />); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(1001); });
    expect(screen.getByText(/绑定请求已过期/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '确认绑定' })).not.toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
    const calls = read.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(read).toHaveBeenCalledTimes(calls);
  });

  it('polls every 4s only while visible and pending, without overlap, and stops after expiry', async () => {
    vi.useFakeTimers();
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    read.mockResolvedValue({ ...unbound, pending: pending() });
    render(<TelegramBinding />); await flush();
    await act(async () => { await vi.advanceTimersByTimeAsync(3999); });
    expect(read).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(read).toHaveBeenCalledTimes(2);
    visibility.mockReturnValue('hidden');
    await act(async () => { await vi.advanceTimersByTimeAsync(12_000); });
    expect(read).toHaveBeenCalledTimes(2);
    visibility.mockReturnValue('visible');
    await act(async () => { await vi.advanceTimersByTimeAsync(4000); });
    expect(read).toHaveBeenCalledTimes(3);
    await act(async () => { await vi.advanceTimersByTimeAsync(310_000); });
    const count = read.mock.calls.length;
    expect(count).toBeLessThanOrEqual(76);
    await act(async () => { await vi.advanceTimersByTimeAsync(20_000); });
    expect(read).toHaveBeenCalledTimes(count);
  });

  it.each([409, 410, 403, 0])('shows uncertain/conflicting outcomes on %i, never a binding success', async (status) => {
    read.mockResolvedValue({ ...unbound, pending: claimed() });
    mutate.mockRejectedValue(new SupportError(status));
    render(<TelegramBinding />);
    await screen.findByText(/987654321/);
    fireEvent.click(screen.getByRole('checkbox', { name: /我确认这是我的 Telegram 账号/ }));
    fireEvent.click(screen.getByRole('checkbox', { name: /同意接收工单状态通知/ }));
    fireEvent.click(screen.getByRole('button', { name: '确认绑定' }));
    await screen.findByRole('alert');
    expect(screen.queryByText('已绑定')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '确认绑定' })).toBeDisabled();
    expect(mutate).toHaveBeenCalledTimes(1);
  });

  it.each([{ id: 'another-session' }, { telegram_id: 123456789 }])('resets both consents when another session changes %o', async (change) => {
    read.mockResolvedValue({ ...unbound, pending: claimed() });
    render(<TelegramBinding />); await screen.findByText(/987654321/);
    fireEvent.click(screen.getByRole('checkbox', { name: /我确认这是我的 Telegram 账号/ }));
    fireEvent.click(screen.getByRole('checkbox', { name: /同意接收工单状态通知/ }));
    read.mockResolvedValue({ ...unbound, pending: { ...claimed(), ...change } });
    fireEvent.click(screen.getByRole('button', { name: '刷新绑定状态' }));
    await act(async () => { await Promise.resolve(); });
    expect(screen.getByRole('button', { name: '确认绑定' })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: /同意接收工单状态通知/ })).not.toBeChecked();
  });

  it('shows a disabled bridge as unavailable without permitting any binding mutation', async () => {
    read.mockResolvedValue({ enabled: false, bound: false });
    render(<TelegramBinding />);
    await screen.findByText('Telegram 绑定暂未开放，网站客服不受影响。');
    expect(screen.queryByRole('button', { name: '绑定 Telegram' })).not.toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
  });

  it('does not make unsafe or restored URLs clickable and can reload without a raw bot token', async () => {
    read.mockResolvedValue({ ...unbound, pending: pending(), binding_url: 'javascript:alert(1)' });
    render(<TelegramBinding />);
    await screen.findByRole('button', { name: '取消绑定' });
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.getByText(/链接仅在创建时显示/)).toBeInTheDocument();
  });

  it('ignores late responses after unmount', async () => {
    let finish!: (value: BindingView) => void;
    read.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const component = render(<TelegramBinding />);
    component.unmount();
    await act(async () => { finish(bound); });
    expect(screen.queryByText('已绑定')).not.toBeInTheDocument();
  });
});
