import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from '../../api/client';
import { spendingApi, SpendingError } from '../../api/spending';
import { SpendingPage } from './SpendingPage';
import { spendingConfig, spendingInbox, spendingOverview } from './spending.fixture';

vi.mock('../../api/spending', async (original) => ({ ...await original<typeof import('../../api/spending')>(), spendingApi: { config: vi.fn(), overview: vi.fn(), alerts: vi.fn(), save: vi.fn(), read: vi.fn() } }));
beforeEach(() => {
  setAuthSession({ access_token: 'test', expires_in: 900, user: { id: 7, username: 'alice', role: 1, group: 'default' } });
  vi.mocked(spendingApi.config).mockResolvedValue({ ...spendingConfig });
  vi.mocked(spendingApi.overview).mockResolvedValue(spendingOverview);
  vi.mocked(spendingApi.alerts).mockResolvedValue(spendingInbox);
  vi.mocked(spendingApi.save).mockImplementation(async (input) => ({ ...input, version: input.version + 1 }));
  vi.mocked(spendingApi.read).mockResolvedValue(undefined);
});
afterEach(() => { cleanup(); clearAuthSession(); vi.clearAllMocks(); });
const mount = () => render(<MemoryRouter><SpendingPage /></MemoryRouter>);

it('shows unknown money, incomplete evidence and pending counts separately, defaulting OFF', async () => {
  mount();
  expect(await screen.findByLabelText('启用消费预警')).not.toBeChecked();
  expect(screen.getByText('暂不可用')).toBeVisible();
  expect(screen.getByText('汇总不完整，已确认金额可能尚未覆盖全部记录。')).toBeVisible();
  expect(screen.getByText('0.000001 U')).toBeVisible();
  expect(screen.getByText('12.340000 U')).toBeVisible();
  expect(screen.getByText('今日确认退款')).toBeVisible();
  expect(screen.getByText('2.000001 U')).toBeVisible();
  expect(screen.getByText('今日已确认消费')).toBeVisible();
  expect(screen.getByText('近1小时已确认消费')).toBeVisible();
  expect(screen.getByText('消费阈值按已确认消费累计计算，退款单独列示，不抵减阈值统计。', { exact: false })).toBeVisible();
  expect(screen.getByLabelText('单次普通输入 Token 阈值')).toBeVisible();
  expect(screen.getByLabelText('待确认请求')).toHaveTextContent('3');
  expect(screen.getByLabelText('证据不完整请求')).toHaveTextContent('2');
  expect(screen.getByLabelText('Telegram 通知')).toBeDisabled();
  expect(screen.getByRole('link', { name: '前往客服绑定 Telegram' })).toHaveAttribute('href', '/console/support');
});

it('refreshes the overview without silently discarding unsaved settings', async () => {
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '7.000001' } });
  fireEvent.click(screen.getByRole('button', { name: '刷新消费概览' }));
  await waitFor(() => expect(spendingApi.overview).toHaveBeenCalledTimes(2));
  expect(spendingApi.config).toHaveBeenCalledTimes(1);
  expect(screen.getByLabelText('今日确认消费阈值 (U)')).toHaveValue('7.000001');
});

it('edits locally, validates, and explicitly saves exact decimal strings with version', async () => {
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.click(screen.getByLabelText('启用消费预警'));
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled();
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '1e2' } });
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled();
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '0.000001' } });
  expect(spendingApi.save).not.toHaveBeenCalled();
  expect(screen.getByText('有未保存的修改')).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await screen.findByText('预警设置已保存');
  expect(spendingApi.save).toHaveBeenCalledWith({ enabled: true, daily_spend_u: '0.000001', hourly_spend_u: '', low_balance_u: '', input_tokens: 0, telegram: false, version: 0 }, expect.anything());
});

it('keeps edits on 409 and reloads only after explicitly discarding them', async () => {
  vi.mocked(spendingApi.save).mockRejectedValue(new SpendingError(409, 'version_conflict'));
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '5' } });
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await screen.findByText('设置已在其他会话更新。你的修改尚未保存。');
  expect(screen.getByLabelText('今日确认消费阈值 (U)')).toHaveValue('5');
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled();
  vi.mocked(spendingApi.config).mockResolvedValue({ ...spendingConfig, version: 4, daily_spend_u: '8' });
  fireEvent.click(screen.getByRole('button', { name: '放弃修改并重新加载' }));
  await waitFor(() => expect(screen.getByLabelText('今日确认消费阈值 (U)')).toHaveValue('8'));
});

it.each(['telegram_binding_required', 'telegram_unavailable'] as const)('retains edits and offers binding recovery for %s, without blocking master disable', async (code) => {
  vi.mocked(spendingApi.config).mockResolvedValue({ ...spendingConfig, enabled: true, telegram: true, daily_spend_u: '5', telegram_bound: true });
  vi.mocked(spendingApi.save).mockRejectedValueOnce(new SpendingError(409, code));
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '7' } });
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await screen.findByText(code === 'telegram_binding_required' ? 'Telegram 绑定已失效，修改仍保留。请重新绑定，或关闭 Telegram 通知后保存。' : 'Telegram 绑定状态暂不可用，修改仍保留。请稍后重试，或关闭 Telegram 通知后保存。');
  expect(screen.queryByText('设置已在其他会话更新。你的修改尚未保存。')).toBeNull();
  expect(screen.getByLabelText('今日确认消费阈值 (U)')).toHaveValue('7');
  expect(screen.getByRole('link', { name: '前往客服绑定 Telegram' })).toHaveAttribute('href', '/console/support');
  fireEvent.click(screen.getByLabelText('启用消费预警'));
  expect(screen.getByLabelText('Telegram 通知')).toBeChecked();
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeEnabled();
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await screen.findByText('预警设置已保存');
  expect(spendingApi.save).toHaveBeenLastCalledWith(expect.objectContaining({ enabled: false, telegram: true, daily_spend_u: '7', version: 0 }), expect.anything());
});

it('acknowledges only the clicked notification, without optimistic read on failure', async () => {
  vi.mocked(spendingApi.read).mockRejectedValueOnce(new SpendingError(503));
  mount();
  const notification = await screen.findByRole('button', { name: /今日消费达到阈值/ });
  fireEvent.click(notification);
  await screen.findByText('未能标记已读，请重试。');
  expect(notification).toHaveAttribute('data-unread', 'true');
  fireEvent.click(notification);
  await waitFor(() => expect(notification).toHaveAttribute('data-unread', 'false'));
  expect(spendingApi.read).toHaveBeenLastCalledWith(12, expect.anything());
});

it('clears settings, totals and notifications when the login lifecycle changes', async () => {
  mount(); await screen.findByLabelText('启用消费预警');
  vi.mocked(spendingApi.config).mockReturnValue(new Promise(() => {}));
  vi.mocked(spendingApi.overview).mockReturnValue(new Promise(() => {}));
  vi.mocked(spendingApi.alerts).mockReturnValue(new Promise(() => {}));
  act(() => setAuthSession({ access_token: 'next', expires_in: 900, user: { id: 7, username: 'alice', role: 1, group: 'default' } }));
  expect(screen.queryByText('12.340000 U')).toBeNull();
  expect(screen.queryByLabelText('今日确认消费阈值 (U)')).toBeNull();
  expect(screen.queryByRole('button', { name: /今日消费达到阈值/ })).toBeNull();
});

it('keeps the page usable when the optional service is unavailable', async () => {
  vi.mocked(spendingApi.config).mockRejectedValue(new SpendingError(503));
  mount();
  expect(await screen.findByText('预警服务暂不可用，请稍后重试。')).toBeVisible();
  expect(screen.getByRole('heading', { name: '消费预警' })).toBeVisible();
  expect(screen.queryByLabelText('启用消费预警')).toBeNull();
  expect(screen.queryByText('消费预警登录凭证已过期或未被接受，主会话未退出。')).toBeNull();
});

it('distinguishes optional 401 from unavailability without clearing or refreshing the main login', async () => {
  const session = getAuthSession();
  vi.mocked(spendingApi.config).mockRejectedValue(new SpendingError(401));
  vi.mocked(spendingApi.overview).mockRejectedValue(new SpendingError(401));
  vi.mocked(spendingApi.alerts).mockRejectedValue(new SpendingError(401));
  mount();
  expect(await screen.findByText('消费预警登录凭证已过期或未被接受，主会话未退出。')).toBeVisible();
  expect(screen.getAllByRole('button', { name: '重新确认登录并刷新页面' })).toHaveLength(1);
  expect(screen.queryByText('预警服务暂不可用，请稍后重试。')).toBeNull();
  expect(getAuthSession()).toBe(session);
});

it('retains unsaved edits after save 401 and a cancelled explicit reauthentication reload', async () => {
  const session = getAuthSession();
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  vi.mocked(spendingApi.save).mockRejectedValue(new SpendingError(401));
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '7.000001' } });
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await screen.findByText('消费预警登录凭证已过期或未被接受，主会话未退出。');
  expect(confirm).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole('button', { name: '重新确认登录并刷新页面' }));
  expect(confirm).toHaveBeenCalledWith('将重新加载当前页面以重新确认主登录。未保存的修改或未完成的操作可能丢失，是否继续？');
  expect(screen.getByLabelText('今日确认消费阈值 (U)')).toHaveValue('7.000001');
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled();
  expect(spendingApi.save).toHaveBeenCalledTimes(1);
  expect(getAuthSession()).toBe(session);
});

it('does not duplicate the reload action when both the inbox and a save require reauthentication', async () => {
  vi.mocked(spendingApi.alerts).mockRejectedValue(new SpendingError(401));
  vi.mocked(spendingApi.save).mockRejectedValue(new SpendingError(401));
  mount(); await screen.findByLabelText('启用消费预警');
  fireEvent.change(screen.getByLabelText('今日确认消费阈值 (U)'), { target: { value: '7' } });
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  await waitFor(() => expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled());
  expect(screen.getAllByRole('button', { name: '重新确认登录并刷新页面' })).toHaveLength(1);
});

it('labels a per-request warning as ordinary input, never total cached context', async () => {
  vi.mocked(spendingApi.alerts).mockResolvedValue({ unread_count: 1, items: [{ ...spendingInbox.items[0], kind: 'large_input', measured_value: '5000', threshold: '4000', unit: 'tokens' }] });
  mount();
  expect(await screen.findByRole('button', { name: /单次普通输入 Token 达到阈值/ })).toBeVisible();
});

it('keeps Telegram opt-in separate for a verified binding and prevents concurrent saves', async () => {
  vi.mocked(spendingApi.config).mockResolvedValue({ ...spendingConfig, telegram_bound: true });
  vi.mocked(spendingApi.save).mockReturnValue(new Promise(() => {}));
  mount(); await screen.findByLabelText('启用消费预警');
  const telegram = screen.getByLabelText('Telegram 通知');
  expect(telegram).toBeEnabled(); expect(telegram).not.toBeChecked();
  fireEvent.click(telegram);
  expect(screen.getByLabelText('启用消费预警')).not.toBeChecked();
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  expect(screen.getByRole('button', { name: '保存预警设置' })).toBeDisabled();
  expect(screen.getByLabelText('今日确认消费阈值 (U)')).toBeDisabled();
  fireEvent.click(screen.getByRole('button', { name: '保存预警设置' }));
  expect(spendingApi.save).toHaveBeenCalledTimes(1);
});

it('shows every null total as unavailable, without converting pending or incomplete evidence to zero money', async () => {
  vi.mocked(spendingApi.overview).mockResolvedValue({ ...spendingOverview, confirmed_hourly_u: null, confirmed_refunds_daily_u: null, available_balance_u: null });
  mount(); await screen.findByLabelText('启用消费预警');
  expect(screen.getAllByText('暂不可用')).toHaveLength(4);
  expect(screen.queryByText(/^0(?:\.0+)? U$/)).toBeNull();
  expect(screen.getByLabelText('待确认请求')).toHaveTextContent('3');
  expect(screen.getByLabelText('证据不完整请求')).toHaveTextContent('2');
});
