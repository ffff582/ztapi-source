import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, vi } from 'vitest';
import { clearAuthSession } from '../api/client';
import { AppProviders } from './providers';
import { createZTAPIRouter } from './router';

const NativeRequest = globalThis.Request;

beforeEach(() => {
  vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('zh-CN');
});

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
  localStorage.clear();
  document.documentElement.lang = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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
        name: '一个接口，连接主流 AI 模型',
      },
      { timeout: 10_000 },
    ),
  ).toBeInTheDocument();
}, 10_000);

it('renders the models route', async () => {
  render(<RouterProvider router={createZTAPIRouter(['/models'])} />);

  expect(
    await screen.findByRole('heading', { name: '模型与价格' }),
  ).toBeInTheDocument();
  expect(screen.getAllByRole('main')).toHaveLength(1);
});

it.each([
  ['/docs/api', 'API 手册'],
  ['/docs/integration', '集成指南'],
  ['/docs/user-guide', '用户指南'],
  ['/docs/faq', '常见问题'],
])('renders the public documentation route %s', async (path, heading) => {
  render(<RouterProvider router={createZTAPIRouter([path])} />);

  expect(await screen.findByRole('heading', { name: heading }, { timeout: 10_000 })).toBeInTheDocument();
  expect(screen.getByRole('navigation', { name: '文档分类' })).toBeVisible();
  expect(screen.getAllByText('https://ztapi.vip/v1').length).toBeGreaterThan(0);
}, 15_000);

it.each([
  ['/login', '登录 ZTAPI'],
  ['/register', '创建 ZTAPI 账号'],
  ['/forgot-password', '找回密码'],
  ['/reset-password?email=alice%40example.com&token=one-time-token', '设置新密码'],
])('renders the unauthenticated %s route', async (path, heading) => {
  renderWithProviders(path, false);

  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
});

it.each([
  ['/console', '看板'],
  ['/console/keys', 'API 密钥'],
  ['/console/models', '模型支持'],
  ['/console/workbench/text', '文本工作台'],
  ['/console/workbench/image', '图像工作台'],
  ['/console/workbench/video', '视频工作台'],
  ['/console/test', '文本工作台'],
  ['/console/guide', '使用说明'],
  ['/console/logs', '使用日志'],
  ['/console/wallet', '余额充值'],
])('renders the authenticated %s route', async (path, heading) => {
  renderWithProviders(path, true);

  expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
});

it('keeps model support and integration guidance in the console navigation', async () => {
  renderWithProviders('/console', true);

  await screen.findByRole('heading', { name: '看板' });
  expect(screen.getByRole('link', { name: '模型市场' })).toHaveAttribute(
    'href',
    '/console/models',
  );
  expect(screen.getByRole('link', { name: 'API 文档' })).toHaveAttribute(
    'href',
    '/console/guide',
  );
  expect(screen.getByRole('link', { name: '工作台' })).toHaveAttribute(
    'href',
    '/console/workbench/text',
  );
});

it('switches the public site to English and persists the language without changing technical values', async () => {
  vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('zh-CN');
  renderWithProviders('/', false);

  await screen.findByRole('heading', { name: '一个接口，连接主流 AI 模型' }, { timeout: 10_000 });
  const switcher = await screen.findByRole('button', { name: '切换到英文' });
  expect(document.documentElement.lang).toBe('zh-CN');

  fireEvent.click(switcher);

  expect(await screen.findByRole('heading', { name: 'One interface for leading AI models' })).toBeVisible();
  expect(screen.getByText('Access text, reasoning, image, and video models through one API, with clear requests, usage, and charges.')).toBeVisible();
  expect(screen.getByText('One interface for the models you need')).toBeVisible();
  expect(screen.queryByText('JavaScript')).toBeNull();
  expect(localStorage.getItem('ztapi.locale')).toBe('en');
  expect(document.documentElement.lang).toBe('en');
});

it('selects English from navigator.language for a first-time visitor', async () => {
  vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('fr-FR');

  renderWithProviders('/register', false);

  expect(await screen.findByRole('heading', { name: 'Create a ZTAPI account' })).toBeVisible();
  expect(screen.getByRole('navigation', { name: 'Public navigation' })).toBeVisible();
  expect(document.documentElement.lang).toBe('en');
  expect(localStorage.getItem('ztapi.locale')).toBeNull();
});

it('keeps a manually saved ZTAPI language ahead of navigator.language', async () => {
  localStorage.setItem('ztapi.locale', 'zh-CN');
  vi.spyOn(window.navigator, 'language', 'get').mockReturnValue('en-US');

  renderWithProviders('/login', false);

  expect(await screen.findByRole('heading', { name: '登录 ZTAPI' })).toBeVisible();
  expect(screen.getByRole('navigation', { name: '公共导航' })).toBeVisible();
});

it('uses a compatible saved i18next language when no ZTAPI preference exists', async () => {
  localStorage.setItem('i18nextLng', 'en-US');

  renderWithProviders('/login', false);

  expect(await screen.findByRole('heading', { name: 'Sign in to ZTAPI' })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Switch to Chinese' })).toBeVisible();
});

it.each([
  ['/docs/api', false, 'API reference'],
  ['/docs/integration', false, 'Integration guide'],
  ['/docs/user-guide', false, 'User guide'],
  ['/docs/faq', false, 'Frequently asked questions'],
  ['/models', false, 'Models and pricing'],
  ['/login', false, 'Sign in to ZTAPI'],
  ['/register', false, 'Create a ZTAPI account'],
  ['/console', true, 'Dashboard'],
  ['/console/keys', true, 'API keys'],
  ['/console/models', true, 'Supported models'],
  ['/console/workbench/text', true, 'Text workbench'],
  ['/console/workbench/image', true, 'Image workbench'],
  ['/console/workbench/video', true, 'Video workbench'],
  ['/console/test', true, 'Text workbench'],
  ['/console/guide', true, 'Integration guide'],
  ['/console/logs', true, 'Usage logs'],
  ['/console/wallet', true, 'Add funds'],
])('renders fixed copy in English on %s', async (path, authenticated, heading) => {
  localStorage.setItem('ztapi.locale', 'en');
  renderWithProviders(path, authenticated);

  expect(await screen.findByRole('heading', { name: heading }, { timeout: 10_000 })).toBeVisible();
}, 15_000);
