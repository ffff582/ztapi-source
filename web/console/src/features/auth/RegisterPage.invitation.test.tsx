import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { AuthClientError } from '../../api/client';
import { LocaleProvider } from '../../i18n/locale';
import { RegisterPage } from './RegisterPage';

const { register } = vi.hoisted(() => ({ register: vi.fn() }));
vi.mock('../../auth/session', () => ({ useAuth: () => ({ register }) }));
const mount = (path: string) => render(<MemoryRouter initialEntries={[path]}><LocaleProvider><RegisterPage /></LocaleProvider></MemoryRouter>);

beforeEach(() => {
  sessionStorage.clear();
  register.mockReset().mockRejectedValue(new AuthClientError('invalid_captcha'));
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ success: true, data: { captcha_id: 'c1', captcha_image: 'data:image/png;base64,iVBORw0KGgo=' } }))));
});
afterEach(() => { cleanup(); sessionStorage.clear(); vi.unstubAllGlobals(); });

async function submit() {
  await screen.findByAltText('验证码图片');
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: 'offline' } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'in-memory-secret' } });
  fireEvent.change(screen.getByLabelText('验证码'), { target: { value: 'bad' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: '创建账号' })); });
}

it('forwards invite_code on captcha failure and after a refresh without persisting passwords', async () => {
  const first = mount('/register?invite=zt-ref-abc&password=never');
  await submit();
  await screen.findByText('验证码错误或已过期，请输入新的验证码');
  expect(register).toHaveBeenLastCalledWith(expect.objectContaining({ invite_code: 'zt-ref-abc' }));
  expect(sessionStorage.getItem('invite_code')).toBe('zt-ref-abc');
  expect(sessionStorage.length).toBe(1);
  first.unmount();
  mount('/register');
  await submit();
  await waitFor(() => expect(register).toHaveBeenCalledTimes(2));
  expect(register).toHaveBeenLastCalledWith(expect.objectContaining({ invite_code: 'zt-ref-abc' }));
});

it('does not add invite_code to ordinary registration', async () => {
  mount('/register');
  await submit();
  await waitFor(() => expect(register).toHaveBeenCalledOnce());
  expect(register.mock.calls[0][0]).not.toHaveProperty('invite_code');
});

it('allows ordinary registration after removing a rejected invitation from storage and URL', async () => {
  register.mockRejectedValueOnce(new AuthClientError('invalid_invitation'));
  mount('/register?invite=invalid-code');
  await submit();
  expect(await screen.findByText('邀请码无效，请核对或清除邀请码后普通注册。')).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: '清除邀请码并普通注册' }));
  expect(sessionStorage.getItem('invite_code')).toBeNull();
  await submit();
  await waitFor(() => expect(register).toHaveBeenCalledTimes(2));
  expect(register.mock.calls[1][0]).not.toHaveProperty('invite_code');
});

it.each(['zh-CN', 'en'])('provides a translated recoverable campaign-unavailable error in %s', async (locale) => {
  localStorage.setItem('ztapi.locale', locale);
  register.mockRejectedValueOnce(new AuthClientError('invitation_unavailable'));
  mount('/register?invite=zt-ref-abc');
  await screen.findByAltText(locale === 'en' ? 'Verification code image' : '验证码图片');
  fireEvent.change(screen.getByLabelText(locale === 'en' ? 'Username' : '账号'), { target: { value: 'offline' } });
  fireEvent.change(screen.getByLabelText(locale === 'en' ? 'Password' : '密码'), { target: { value: 'memory-only-password' } });
  fireEvent.change(screen.getByLabelText(locale === 'en' ? 'Verification code' : '验证码'), { target: { value: 'code' } });
  await act(async () => fireEvent.click(screen.getByRole('button', { name: locale === 'en' ? 'Create account' : '创建账号' })));
  expect(await screen.findByText(locale === 'en' ? 'Referral participation is temporarily unavailable. Remove the invitation code to continue registering.' : '推广活动暂不可用，可移除邀请码后继续注册')).toBeVisible();
  expect(screen.getByRole('button', { name: locale === 'en' ? 'Remove invitation and register normally' : '清除邀请码并普通注册' })).toBeEnabled();
  expect(sessionStorage.getItem('invite_code')).toBe('zt-ref-abc');
});

it('shows the anonymous closed status for invited registration without blocking submission', async () => {
  const fetcher = vi.fn(async (url: string) => new Response(JSON.stringify({ success: true, data: url === '/api/referrals/status'
    ? { enabled: false, accepting_invites: false, blocked_reasons: ['活动尚未开启'], started_at: 0, rate: '0.05', freeze_days: 7, term_months: 12, minimum_withdrawal: '20.000000', network: 'TRC20' }
    : { captcha_id: 'c1', captcha_image: 'data:image/png;base64,iVBORw0KGgo=' } })));
  vi.stubGlobal('fetch', fetcher);
  mount('/register?invite=zt-ref-abc');
  expect(await screen.findByText('推广活动当前未开放，暂不接受现金返佣绑定；本次是否绑定以注册结果为准。')).toBeVisible();
  await submit();
  expect(register).toHaveBeenCalledWith(expect.objectContaining({ invite_code: 'zt-ref-abc' }));
});

it('does not request campaign status during ordinary registration', async () => {
  const fetcher = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ success: true, data: { captcha_id: 'c1', captcha_image: 'data:image/png;base64,iVBORw0KGgo=' } })));
  vi.stubGlobal('fetch', fetcher);
  mount('/register');
  await submit();
  expect(fetcher.mock.calls.some(([url]) => url === '/api/referrals/status')).toBe(false);
});
