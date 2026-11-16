import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, expect, it, vi } from 'vitest';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';

function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function urlOf(input: RequestInfo | URL) {
  return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
}

afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  const { clearAuthSession } = await import('../../api/client');
  clearAuthSession();
});

it('lets a signed-in user add and verify an email later', async () => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = urlOf(input);
    if (url.endsWith('/auth/refresh')) {
      return response({
        success: true,
        data: {
          access_token: 'access-token',
          expires_in: 900,
          user: { id: 7, username: 'alice', role: 1, group: 'default' },
        },
      });
    }
    if (url.endsWith('/user/self') && (!init?.method || init.method === 'GET')) {
      return response({
        success: true,
        data: { email: '', pending_email: '', email_verified: false },
      });
    }
    return response({ success: true, data: null });
  });
  vi.stubGlobal('fetch', fetchMock);

  render(
    <AppProviders>
      <RouterProvider router={createZTAPIRouter(['/console/account'])} />
    </AppProviders>,
  );

  expect(await screen.findByRole('heading', { name: '账号设置' })).toBeInTheDocument();
  fireEvent.change(await screen.findByLabelText('邮箱地址'), {
    target: { value: ' Owner@Example.com ' },
  });
  fireEvent.click(screen.getByRole('button', { name: '发送验证码' }));

  await waitFor(() => {
    expect(fetchMock.mock.calls.some(([input]) => urlOf(input).endsWith('/user/self/email/verification'))).toBe(true);
  });
  fireEvent.change(screen.getByLabelText('邮箱验证码'), {
    target: { value: 'abc123' },
  });
  fireEvent.click(screen.getByRole('button', { name: '确认绑定' }));

  await waitFor(() => {
    const call = fetchMock.mock.calls.find(([input]) => urlOf(input).endsWith('/user/self/email'));
    expect(call).toBeDefined();
    expect(JSON.parse(String((call?.[1] as RequestInit)?.body))).toEqual({
      email: 'owner@example.com',
      verification_code: 'abc123',
    });
  });
  expect(await screen.findByText('邮箱已验证，可用于找回密码。')).toBeInTheDocument();
});
