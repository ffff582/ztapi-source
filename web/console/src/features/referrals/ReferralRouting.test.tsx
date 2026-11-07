import { fireEvent, render, screen, waitFor, cleanup } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';
import { clearAuthSession } from '../../api/client';
import fixture from './referral.fixture.json';

afterEach(() => { cleanup(); clearAuthSession(); vi.unstubAllGlobals(); });

it('routes public referral visitors through login and back to the real referral page', async () => {
  let signedIn = false;
  const NativeRequest = globalThis.Request;
  vi.stubGlobal('Request', class extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) { super(input, init ? { ...init, signal: undefined } : init); }
  });
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const success = (data: unknown) => new Response(JSON.stringify({ success: true, data }));
    if (url === '/api/auth/login') signedIn = true;
    if (url === '/api/auth/refresh' || url === '/api/auth/login') return signedIn ? success({ access_token: 'offline', expires_in: 900, user: { id: 7, username: 'offline', role: 1, group: 'default' } }) : new Response(JSON.stringify({ success: false, message: 'unauthorized' }), { status: 401 });
    if (url === '/api/referrals/self') return success(fixture);
    if (url.startsWith('/api/referrals/self/')) return success({ items: [], total: 0 });
    if (url.startsWith('/api/spending/')) return new Response('{}', { status: 503 });
    if (url === '/api/status') return success({});
    throw new Error(`Unexpected request ${url}`);
  }));
  const router = createZTAPIRouter(['/console/referrals']);
  render(<AppProviders><RouterProvider router={router} /></AppProviders>);
  await screen.findByRole('heading', { name: '登录 ZTAPI' });
  expect(router.state.location.search).toBe('?redirect=%2Fconsole%2Freferrals');
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: 'offline' } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'offline-password' } });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
  await screen.findByRole('heading', { name: '推广返利' });
  await waitFor(() => expect(router.state.location.pathname).toBe('/console/referrals'));
});

it('retains the server unbound-registration warning after successful invited registration redirects', async () => {
  const NativeRequest = globalThis.Request;
  vi.stubGlobal('Request', class extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) { super(input, init ? { ...init, signal: undefined } : init); }
  });
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    const success = (data: unknown) => new Response(JSON.stringify({ success: true, data }));
    if (url === '/api/auth/refresh') return new Response(JSON.stringify({ success: false, message: 'unauthorized' }), { status: 401 });
    if (url === '/api/auth/captcha') return success({ captcha_id: 'offline-captcha', captcha_image: 'data:image/png;base64,iVBORw0KGgo=' });
    if (url === '/api/auth/register') return success({ access_token: 'offline', expires_in: 900, user: { id: 7, username: 'offline', role: 1, group: 'default' }, referral_bound: false });
    if (url === '/api/referrals/status') return success({ ...fixture.campaign, enabled: false, accepting_invites: false });
    if (url === '/api/referrals/self') return success({ ...fixture, campaign: { ...fixture.campaign, enabled: false, accepting_invites: false } });
    if (url.startsWith('/api/referrals/self/')) return success({ items: [], total: 0 });
    if (url.startsWith('/api/spending/')) return new Response('{}', { status: 503 });
    throw new Error(`Unexpected request ${url}`);
  }));
  const router = createZTAPIRouter(['/register?invite=zt-ref-abc&redirect=%2Fconsole%2Freferrals']);
  render(<AppProviders><RouterProvider router={router} /></AppProviders>);
  await screen.findByAltText('验证码图片');
  fireEvent.change(screen.getByLabelText('账号'), { target: { value: 'offline' } });
  fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'offline-password' } });
  fireEvent.change(screen.getByLabelText('验证码'), { target: { value: 'code' } });
  fireEvent.click(screen.getByRole('button', { name: '创建账号' }));
  expect(await screen.findByText('注册成功，但本次未建立推广返佣关系，请勿将本次邀请视为已生效。')).toBeVisible();
  await waitFor(() => expect(router.state.location.pathname).toBe('/console/referrals'));
});
