import { cleanup, render, screen } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, vi } from 'vitest';
import { clearAuthSession } from '../api/client';
import { AppProviders } from './providers';
import { createZTAPIRouter } from './router';

const NativeRequest = globalThis.Request;

beforeAll(() => {
  globalThis.Request = class TestRequest extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? init : { ...init, signal: undefined });
    }
  };
});

afterAll(() => {
  globalThis.Request = NativeRequest;
});

afterEach(() => {
  cleanup();
  clearAuthSession();
  vi.unstubAllGlobals();
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function renderWithProviders(path: string, authenticated: boolean) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue(
      authenticated
        ? jsonResponse({
            success: true,
            data: {
              access_token: 'memory-token',
              expires_in: 900,
              user: {
                id: 1,
                username: 'alice',
                role: 1,
                group: 'default',
              },
            },
          })
        : jsonResponse(
            { success: false, message: 'unauthorized' },
            401,
          ),
    ),
  );

  render(
    <AppProviders>
      <RouterProvider router={createZTAPIRouter([path])} />
    </AppProviders>,
  );
}

it('renders the ZTAPI home route', async () => {
  render(<RouterProvider router={createZTAPIRouter(['/'])} />);

  expect(
    await screen.findByRole(
      'heading',
      {
        name: 'ZTAPI',
      },
      { timeout: 5_000 },
    ),
  ).toBeInTheDocument();
});

it('renders the models route', async () => {
  render(<RouterProvider router={createZTAPIRouter(['/models'])} />);

  expect(
    await screen.findByRole('heading', { name: '模型与价格' }),
  ).toBeInTheDocument();
  expect(screen.getAllByRole('main')).toHaveLength(1);
});

it.each([
  ['/login', '登录 ZTAPI'],
  ['/register', '创建 ZTAPI 账号'],
])('renders the unauthenticated %s route', async (path, heading) => {
  renderWithProviders(path, false);

  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
});

it.each([
  ['/console', '使用概览'],
  ['/console/keys', 'API 密钥'],
  ['/console/models', '模型支持'],
  ['/console/guide', '使用说明'],
  ['/console/logs', '使用日志'],
  ['/console/wallet', '余额充值'],
])('renders the authenticated %s route', async (path, heading) => {
  renderWithProviders(path, true);

  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
});

it('keeps model support and integration guidance in the console navigation', async () => {
  renderWithProviders('/console', true);

  await screen.findByRole('heading', { name: '使用概览' });
  expect(screen.getByRole('link', { name: '模型支持' })).toHaveAttribute(
    'href',
    '/console/models',
  );
  expect(screen.getByRole('link', { name: '使用说明' })).toHaveAttribute(
    'href',
    '/console/guide',
  );
});
