import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react';
import { RouterProvider } from 'react-router-dom';
import { afterEach, beforeAll, afterAll, describe, expect, it, vi } from 'vitest';
import { AppProviders } from '../../app/providers';
import { createZTAPIRouter } from '../../app/router';

const NativeRequest = globalThis.Request;
const user = {
  id: 1,
  username: 'alice',
  role: 1,
  group: 'default',
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function authResponse(accessToken = 'access-token') {
  return {
    success: true,
    data: {
      access_token: accessToken,
      expires_in: 900,
      user,
    },
  };
}

function requestUrl(input: RequestInfo | URL) {
  if (typeof input === 'string') {
    return input;
  }

  return input instanceof URL ? input.href : input.url;
}

function renderRoute(path: string) {
  return render(
    <AppProviders>
      <RouterProvider router={createZTAPIRouter([path])} />
    </AppProviders>,
  );
}

async function waitForLoginForm() {
  return screen.findByRole('heading', { name: '登录 ZTAPI' });
}

async function waitForRegisterForm() {
  return screen.findByRole('heading', { name: '创建 ZTAPI 账号' });
}

function submitLogin(username = 'alice', password = 'correct-horse') {
  fireEvent.change(screen.getByLabelText('账号'), {
    target: { value: username },
  });
  fireEvent.change(screen.getByLabelText('密码'), {
    target: { value: password },
  });
  fireEvent.click(screen.getByRole('button', { name: '登录' }));
}

function submitRegistration(
  username = 'alice',
  password = 'correct-horse',
) {
  fireEvent.change(screen.getByLabelText('账号'), {
    target: { value: username },
  });
  fireEvent.change(screen.getByLabelText('密码'), {
    target: { value: password },
  });
  fireEvent.click(screen.getByRole('button', { name: '创建账号' }));
}

beforeAll(() => {
  globalThis.Request = class TestRequest extends NativeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(input, init === undefined ? init : { ...init, signal: undefined });
    }
  };
});

afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
  sessionStorage.clear();

  try {
    const { clearAuthSession } = await import('../../api/client');
    clearAuthSession();
  } catch {
    // The client module intentionally does not exist during the first RED run.
  }
});

afterAll(() => {
  globalThis.Request = NativeRequest;
});

describe('protected authentication routes', () => {
  it('redirects an unauthenticated console visit to login', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );

    renderRoute('/console');

    expect(
      await screen.findByRole('heading', { name: '登录 ZTAPI' }),
    ).toBeInTheDocument();
  });

  it('does not flash console content while session recovery is loading', async () => {
    let resolveRefresh: ((response: Response) => void) | undefined;
    const refreshResponse = new Promise<Response>((resolve) => {
      resolveRefresh = resolve;
    });
    vi.stubGlobal('fetch', vi.fn(() => refreshResponse));

    renderRoute('/console');

    expect(
      screen.queryByRole('heading', { name: '使用概览' }),
    ).not.toBeInTheDocument();

    await act(async () => {
      resolveRefresh?.(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      );
    });

    expect(await waitForLoginForm()).toBeInTheDocument();
  });

  it('focuses the username input when the login route first renders', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();

    expect(screen.getByLabelText('账号')).toHaveFocus();
  });

  it('focuses the username input when the registration route first renders', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );

    renderRoute('/register');
    await waitForRegisterForm();

    expect(screen.getByLabelText('账号')).toHaveFocus();
  });

  it('posts only the submitted username and password when logging in', async () => {
    const fetchMock = vi.fn(
      async (...args: [RequestInfo | URL, RequestInit?]) => {
        const [input] = args;

        if (requestUrl(input).endsWith('/refresh')) {
          return jsonResponse(
            { success: false, message: 'unauthorized' },
            401,
          );
        }

        return jsonResponse(
          { success: false, message: 'invalid credentials' },
          401,
        );
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(([input]) =>
          requestUrl(input as RequestInfo | URL).endsWith('/login'),
        ),
      ).toBe(true);
    });

    const loginCall = fetchMock.mock.calls.find(([input]) =>
      requestUrl(input as RequestInfo | URL).endsWith('/login'),
    );
    const init = loginCall?.[1] as RequestInit | undefined;

    expect(init?.method).toBe('POST');
    expect(init?.credentials).toBe('include');
    expect(JSON.parse(String(init?.body))).toEqual({
      username: 'alice',
      password: 'correct-horse',
    });
  });

  it('navigates to the console after a successful login', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse(authResponse()),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    expect(
      await screen.findByRole('heading', { name: '使用概览' }),
    ).toBeInTheDocument();
    expect(screen.getByText('alice')).toBeInTheDocument();
  });

  it('disables duplicate login submissions and shows pending state', async () => {
    let resolveLogin: ((response: Response) => void) | undefined;
    const loginResponse = new Promise<Response>((resolve) => {
      resolveLogin = resolve;
    });
    const fetchMock = vi.fn(
      async (...args: [RequestInfo | URL, RequestInit?]) =>
        requestUrl(args[0]).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : loginResponse,
    );
    vi.stubGlobal('fetch', fetchMock);

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    const pendingButton = await screen.findByRole('button', {
      name: '登录中...',
    });
    expect(pendingButton).toBeDisabled();
    fireEvent.click(pendingButton);
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/login'),
      ),
    ).toHaveLength(1);

    await act(async () => {
      resolveLogin?.(
        jsonResponse({ success: false, message: 'invalid credentials' }, 401),
      );
    });
  });

  it('focuses the first invalid login field and clears its stale error on edit', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );
    renderRoute('/login');
    await waitForLoginForm();

    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    const username = screen.getByLabelText('账号');
    expect(username).toHaveFocus();
    expect(username).toHaveAttribute('aria-invalid', 'true');

    fireEvent.change(username, { target: { value: 'alice' } });
    expect(username).toHaveAttribute('aria-invalid', 'false');
  });

  it('shows and hides the login password without changing credentials', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );
    renderRoute('/login');
    await waitForLoginForm();
    const password = screen.getByLabelText('密码');
    fireEvent.change(password, { target: { value: 'correct-horse' } });
    fireEvent.click(screen.getByRole('button', { name: '显示密码' }));
    expect(password).toHaveAttribute('type', 'text');
    expect(password).toHaveValue('correct-horse');
  });

  it('keeps entered values after a server login error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse({ success: false, message: 'unauthorized' }, 401)
          : jsonResponse({ success: false, message: 'invalid credentials' }, 401),
      ),
    );
    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();
    await screen.findByRole('alert');
    expect(screen.getByLabelText('账号')).toHaveValue('alice');
    expect(screen.getByLabelText('密码')).toHaveValue('correct-horse');
  });

  it('clears a login server notice when either credential field is edited', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse({ success: false, message: 'unauthorized' }, 401)
          : jsonResponse({ success: false, message: 'invalid credentials' }, 401),
      ),
    );
    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();
    await screen.findByRole('alert');

    fireEvent.change(screen.getByLabelText('账号'), {
      target: { value: 'alice-updated' },
    });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await screen.findByRole('alert');
    fireEvent.change(screen.getByLabelText('密码'), {
      target: { value: 'correct-horse-updated' },
    });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('focuses registration errors in field order and clears them on edit', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );
    renderRoute('/register');
    await waitForRegisterForm();

    fireEvent.click(screen.getByRole('button', { name: '创建账号' }));
    const username = screen.getByLabelText('账号');
    const password = screen.getByLabelText('密码');
    expect(username).toHaveFocus();

    fireEvent.change(username, { target: { value: 'alice' } });
    expect(username).toHaveAttribute('aria-invalid', 'false');
    fireEvent.click(screen.getByRole('button', { name: '创建账号' }));
    expect(password).toHaveFocus();
  });

  it('preserves registration values after a server error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse({ success: false, message: 'unauthorized' }, 401)
          : jsonResponse({ success: false, message: 'username unavailable' }, 409),
      ),
    );
    renderRoute('/register');
    await waitForRegisterForm();
    submitRegistration();
    await screen.findByRole('alert');

    expect(screen.getByLabelText('账号')).toHaveValue('alice');
    expect(screen.getByLabelText('密码')).toHaveValue('correct-horse');
    fireEvent.click(screen.getByRole('button', { name: '显示密码' }));
    expect(screen.getByLabelText('密码')).toHaveValue('correct-horse');
  });

  it('uses password-manager autocomplete attributes', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );

    const { unmount } = renderRoute('/login');
    await waitForLoginForm();
    expect(screen.getByLabelText('账号')).toHaveAttribute(
      'autocomplete',
      'username',
    );
    expect(screen.getByLabelText('密码')).toHaveAttribute(
      'autocomplete',
      'current-password',
    );

    unmount();
    renderRoute('/register');
    await waitForRegisterForm();
    expect(screen.getByLabelText('密码')).toHaveAttribute(
      'autocomplete',
      'new-password',
    );
  });

  it('validates registration against UTF-8 byte and character boundaries', async () => {
    const { validateRegistrationInput } = await import('./RegisterPage');

    expect(
      validateRegistrationInput({
        username: 'ab',
        password: '1234567890',
      }),
    ).toEqual({
      username: '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符',
    });
    expect(
      validateRegistrationInput({
        username: '用'.repeat(11),
        password: '1234567890',
      }),
    ).toEqual({
      username: '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符',
    });
    expect(
      validateRegistrationInput({
        username: 'alice!',
        password: '1234567890',
      }),
    ).toEqual({
      username: '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符',
    });
    expect(
      validateRegistrationInput({
        username: 'alice',
        password: '九个字符12345',
      }),
    ).toEqual({
      password: '密码需至少 10 个字符且不超过 256 字节',
    });
    expect(
      validateRegistrationInput({
        username: 'alice',
        password: '😀'.repeat(65),
      }),
    ).toEqual({
      password: '密码需至少 10 个字符且不超过 256 字节',
    });
    expect(
      validateRegistrationInput({
        username: '用户',
        password: '😀'.repeat(64),
      }),
    ).toEqual({});
  });

  it('shows visible validation and does not submit invalid registration', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ success: false, message: 'unauthorized' }, 401),
    );
    vi.stubGlobal('fetch', fetchMock);

    renderRoute('/register');
    await waitForRegisterForm();

    fireEvent.change(screen.getByLabelText('账号'), {
      target: { value: 'ab' },
    });
    fireEvent.change(screen.getByLabelText('密码'), {
      target: { value: '123456789' },
    });
    fireEvent.click(screen.getByRole('button', { name: '创建账号' }));

    expect(
      screen.getByText(
        '账号需为 3-32 字节，仅可使用字母、数字、下划线或连字符',
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText('密码需至少 10 个字符且不超过 256 字节'),
    ).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/register'),
      ),
    ).toHaveLength(0);
  });

  it('posts trimmed registration credentials and navigates to the console', async () => {
    const fetchMock = vi.fn(
      async (...args: [RequestInfo | URL, RequestInit?]) =>
        requestUrl(args[0]).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse(authResponse()),
    );
    vi.stubGlobal('fetch', fetchMock);

    renderRoute('/register');
    await waitForRegisterForm();
    submitRegistration(' alice ');

    expect(
      await screen.findByRole('heading', { name: '使用概览' }),
    ).toBeInTheDocument();

    const registerCall = fetchMock.mock.calls.find(([input]) =>
      requestUrl(input as RequestInfo | URL).endsWith('/register'),
    );
    const init = registerCall?.[1] as RequestInit | undefined;
    expect(JSON.parse(String(init?.body))).toEqual({
      username: 'alice',
      password: 'correct-horse',
    });
  });

  it.each([
    ['invalid credentials', '账号或密码错误'],
    ['username unavailable', '账号不可用，请更换后重试'],
    ['invalid registration', '账号格式或密码不符合要求'],
  ])('maps the known server error "%s" to safe UI copy', async (
    serverMessage,
    uiMessage,
  ) => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse({ success: false, message: serverMessage }, 400),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    expect(await screen.findByRole('alert')).toHaveTextContent(uiMessage);
  });

  it('maps unknown server errors to generic copy without exposing details', async () => {
    const rawMessage = 'upstream channel alpha leaked header x-secret';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse({ success: false, message: rawMessage }, 503),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('服务暂不可用，请稍后重试');
    expect(alert).not.toHaveTextContent(rawMessage);
  });

  it('rejects malformed authentication success payloads without committing them', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse({
              success: true,
              data: {
                access_token: '',
                expires_in: 900,
                user,
              },
            }),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      '服务暂不可用，请稍后重试',
    );
    const { getAuthSession } = await import('../../api/client');
    expect(getAuthSession()).toBeNull();
  });

  it('never writes access tokens to browser storage or visible DOM state', async () => {
    const storageWrite = vi.spyOn(Storage.prototype, 'setItem');
    const token = 'secret-access-token-never-persist';
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) =>
        requestUrl(input).endsWith('/refresh')
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse(authResponse(token)),
      ),
    );

    renderRoute('/login');
    await waitForLoginForm();
    submitLogin();
    await screen.findByRole('heading', { name: '使用概览' });

    expect(storageWrite).not.toHaveBeenCalled();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    expect(document.body).not.toHaveTextContent(token);
  });

  it('shares one refresh across concurrent 401 responses and retries each once', async () => {
    const { authRequest, setAuthSession } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveRefresh: ((response: Response) => void) | undefined;
    const refreshResponse = new Promise<Response>((resolve) => {
      resolveRefresh = resolve;
    });
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);

        if (url.endsWith('/refresh')) {
          return refreshResponse;
        }

        const authorization = new Headers(init?.headers).get('Authorization');

        return authorization === 'Bearer old-token'
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse({ success: true, data: user });
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const sessionRequests = [
      authRequest<typeof user>('/session'),
      authRequest<typeof user>('/session'),
    ];

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.filter(([input]) =>
          requestUrl(input as RequestInfo | URL).endsWith('/refresh'),
        ),
      ).toHaveLength(1);
    });

    resolveRefresh?.(jsonResponse(authResponse('new-token')));

    await expect(Promise.all(sessionRequests)).resolves.toEqual([user, user]);

    const sessionCalls = fetchMock.mock.calls.filter(([input]) =>
      requestUrl(input as RequestInfo | URL).endsWith('/session'),
    );
    expect(sessionCalls).toHaveLength(4);
    expect(
      sessionCalls.map(([, init]) =>
        new Headers((init as RequestInit | undefined)?.headers).get(
          'Authorization',
        ),
      ),
    ).toEqual([
      'Bearer old-token',
      'Bearer old-token',
      'Bearer new-token',
      'Bearer new-token',
    ]);
  });

  it('reuses the refreshed token when a stale 401 arrives after refresh completes', async () => {
    const { authRequest, setAuthSession } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveStaleResponse: ((response: Response) => void) | undefined;
    const staleResponse = new Promise<Response>((resolve) => {
      resolveStaleResponse = resolve;
    });
    let oldTokenRequests = 0;
    let refreshRequests = 0;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const authorization = new Headers(init?.headers).get('Authorization');

        if (url.endsWith('/refresh')) {
          refreshRequests += 1;
          return jsonResponse(authResponse('new-token'));
        }

        if (authorization === 'Bearer old-token') {
          oldTokenRequests += 1;
          return oldTokenRequests === 1
            ? jsonResponse({ success: false, message: 'unauthorized' }, 401)
            : staleResponse;
        }

        return jsonResponse({ success: true, data: user });
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const firstRequest = authRequest<typeof user>('/session');
    const delayedRequest = authRequest<typeof user>('/session');

    await expect(firstRequest).resolves.toEqual(user);
    resolveStaleResponse?.(
      jsonResponse({ success: false, message: 'unauthorized' }, 401),
    );
    await expect(delayedRequest).resolves.toEqual(user);
    expect(refreshRequests).toBe(1);
  });

  it('exposes typed API helpers on the shared authenticated client', async () => {
    const { apiClient, setAuthSession } = await import('../../api/client');
    setAuthSession(authResponse('console-token').data);
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);

        if (url.endsWith('/pricing')) {
          return jsonResponse({ success: true, data: { currency: 'USD' } });
        }

        return jsonResponse({
          success: true,
          data: JSON.parse(String(init?.body)),
        });
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(
      apiClient.get<{ currency: string }>('/pricing'),
    ).resolves.toEqual({ currency: 'USD' });
    await expect(
      apiClient.post<{ name: string }>('/token/', { name: 'primary' }),
    ).resolves.toEqual({ name: 'primary' });

    expect(requestUrl(fetchMock.mock.calls[0][0])).toBe('/api/pricing');
    expect(requestUrl(fetchMock.mock.calls[1][0])).toBe('/api/token/');
    for (const [, init] of fetchMock.mock.calls) {
      expect(init?.credentials).toBe('include');
      expect(new Headers(init?.headers).get('Authorization')).toBe(
        'Bearer console-token',
      );
    }
  });

  it('attaches the user id required by UserAuth routes', async () => {
    const { apiClient, setAuthSession } = await import('../../api/client');
    setAuthSession(authResponse('console-token').data);
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({
        success: true,
        data: { enable_usdt_trc20_topup: true },
      }),
    );
    vi.stubGlobal('fetch', fetchMock);

    await apiClient.get('/user/topup/info');

    const headers = new Headers(fetchMock.mock.calls[0][1]?.headers);
    expect(headers.get('New-Api-User')).toBe('1');
  });

  it('clears the memory session when refresh fails', async () => {
    const { authRequest, getAuthSession, setAuthSession } = await import(
      '../../api/client'
    );
    setAuthSession(authResponse('expired-token').data);
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        jsonResponse({ success: false, message: 'unauthorized' }, 401),
      ),
    );

    await expect(authRequest('/session')).rejects.toMatchObject({
      kind: 'unauthorized',
    });
    expect(getAuthSession()).toBeNull();
  });

  it('does not let an in-flight refresh restore a logged-out session', async () => {
    const {
      authRequest,
      getAuthSession,
      logoutSession,
      setAuthSession,
    } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveRefresh: ((response: Response) => void) | undefined;
    const refreshResponse = new Promise<Response>((resolve) => {
      resolveRefresh = resolve;
    });
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const authorization = new Headers(init?.headers).get('Authorization');

        if (url.endsWith('/refresh')) {
          return refreshResponse;
        }

        if (url.endsWith('/logout')) {
          return jsonResponse({ success: true });
        }

        return authorization === 'Bearer old-token'
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            );
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const request = authRequest('/session');
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.filter(([input]) =>
          requestUrl(input as RequestInfo | URL).endsWith('/refresh'),
        ),
      ).toHaveLength(1);
    });

    const logout = logoutSession();
    expect(getAuthSession()).toBeNull();
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/logout'),
      ),
    ).toHaveLength(0);
    resolveRefresh?.(jsonResponse(authResponse('stale-token')));

    await expect(request).rejects.toMatchObject({ kind: 'unauthorized' });
    await logout;
    expect(getAuthSession()).toBeNull();
  });

  it('serializes a newer login after an old refresh failure', async () => {
    const {
      authRequest,
      getAuthSession,
      login,
      setAuthSession,
    } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveRefresh: ((response: Response) => void) | undefined;
    const refreshResponse = new Promise<Response>((resolve) => {
      resolveRefresh = resolve;
    });
    const newerSession = authResponse('new-login-token').data;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const authorization = new Headers(init?.headers).get('Authorization');

        if (url.endsWith('/refresh')) {
          return refreshResponse;
        }

        if (url.endsWith('/login')) {
          return jsonResponse({ success: true, data: newerSession });
        }

        return authorization === 'Bearer old-token'
          ? jsonResponse(
              { success: false, message: 'unauthorized' },
              401,
            )
          : jsonResponse({ success: true, data: user });
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const staleRequest = authRequest('/session');
    await waitFor(() => {
      expect(
        fetchMock.mock.calls.filter(([input]) =>
          requestUrl(input as RequestInfo | URL).endsWith('/refresh'),
        ),
      ).toHaveLength(1);
    });

    const newerLogin = login({
      username: 'alice',
      password: 'correct-horse',
    });
    await Promise.resolve();
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/login'),
      ),
    ).toHaveLength(0);
    resolveRefresh?.(
      jsonResponse({ success: false, message: 'invalid session' }, 401),
    );

    await expect(staleRequest).rejects.toMatchObject({
      kind: 'unauthorized',
    });
    await expect(newerLogin).resolves.toEqual(newerSession);
    expect(getAuthSession()).toEqual(newerSession);
  });

  it('does not let a stale retry 401 clear a newer login', async () => {
    const {
      authRequest,
      getAuthSession,
      login,
      setAuthSession,
    } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveRetry: ((response: Response) => void) | undefined;
    const retryResponse = new Promise<Response>((resolve) => {
      resolveRetry = resolve;
    });
    const newerSession = authResponse('new-login-token').data;
    let retriedWithRefreshedToken = false;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = requestUrl(input);
        const authorization = new Headers(init?.headers).get('Authorization');

        if (url.endsWith('/refresh')) {
          return jsonResponse(authResponse('refreshed-token'));
        }

        if (url.endsWith('/login')) {
          return jsonResponse({ success: true, data: newerSession });
        }

        if (authorization === 'Bearer old-token') {
          return jsonResponse(
            { success: false, message: 'unauthorized' },
            401,
          );
        }

        if (authorization === 'Bearer refreshed-token') {
          retriedWithRefreshedToken = true;
          return retryResponse;
        }

        return jsonResponse({ success: true, data: user });
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const staleRequest = authRequest('/session');
    await waitFor(() => {
      expect(retriedWithRefreshedToken).toBe(true);
    });

    await login({ username: 'alice', password: 'correct-horse' });
    resolveRetry?.(
      jsonResponse({ success: false, message: 'unauthorized' }, 401),
    );

    await expect(staleRequest).rejects.toMatchObject({
      kind: 'unauthorized',
    });
    expect(getAuthSession()).toEqual(newerSession);
  });

  it('waits for an older logout response before starting a new login', async () => {
    const {
      getAuthSession,
      login,
      logoutSession,
      setAuthSession,
    } = await import('../../api/client');
    setAuthSession(authResponse('old-token').data);

    let resolveLogout: ((response: Response) => void) | undefined;
    const logoutResponse = new Promise<Response>((resolve) => {
      resolveLogout = resolve;
    });
    const newerSession = authResponse('new-login-token').data;
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL) => {
        const url = requestUrl(input);

        if (url.endsWith('/logout')) {
          return logoutResponse;
        }

        if (url.endsWith('/login')) {
          return jsonResponse({ success: true, data: newerSession });
        }

        return jsonResponse({ success: false, message: 'unknown' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const logout = logoutSession();
    const newLogin = login({
      username: 'alice',
      password: 'correct-horse',
    });

    await Promise.resolve();
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/login'),
      ),
    ).toHaveLength(0);

    resolveLogout?.(jsonResponse({ success: true }));
    await logout;
    await expect(newLogin).resolves.toEqual(newerSession);
    expect(getAuthSession()).toEqual(newerSession);
  });

  it('serializes credential responses so memory and refresh stay on the newest user', async () => {
    const {
      clearAuthSession,
      getAuthSession,
      login,
      refreshSession,
      register,
    } = await import('../../api/client');
    const firstUser = { ...user, id: 1, username: 'first-user' };
    const secondUser = { ...user, id: 2, username: 'second-user' };
    const firstSession = {
      ...authResponse('first-token').data,
      user: firstUser,
    };
    const secondSession = {
      ...authResponse('second-token').data,
      user: secondUser,
    };
    let cookieOwner: 'first' | 'second' | null = null;
    let resolveFirst: ((response: Response) => void) | undefined;
    const firstResponse = new Promise<Response>((resolve) => {
      resolveFirst = resolve;
    });
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL) => {
        const url = requestUrl(input);

        if (url.endsWith('/login')) {
          return firstResponse;
        }

        if (url.endsWith('/register')) {
          cookieOwner = 'second';
          return jsonResponse({ success: true, data: secondSession });
        }

        if (url.endsWith('/refresh')) {
          return jsonResponse({
            success: true,
            data: cookieOwner === 'second' ? secondSession : firstSession,
          });
        }

        return jsonResponse({ success: false, message: 'unknown' }, 500);
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    const firstLogin = login({
      username: 'first-user',
      password: 'correct-horse',
    });
    const secondRegistration = register({
      username: 'second-user',
      password: 'correct-horse',
    });

    await Promise.resolve();
    expect(
      fetchMock.mock.calls.filter(([input]) =>
        requestUrl(input as RequestInfo | URL).endsWith('/register'),
      ),
    ).toHaveLength(0);

    cookieOwner = 'first';
    resolveFirst?.(jsonResponse({ success: true, data: firstSession }));
    await expect(firstLogin).resolves.toEqual(firstSession);
    await expect(secondRegistration).resolves.toEqual(secondSession);
    expect(getAuthSession()).toEqual(secondSession);

    clearAuthSession();
    await expect(refreshSession()).resolves.toEqual(secondSession);
    expect(getAuthSession()).toEqual(secondSession);
  });

  it('clears client state and redirects to login when logout fails', async () => {
    const fetchMock = vi.fn(
      async (input: RequestInfo | URL) => {
        if (requestUrl(input).endsWith('/refresh')) {
          return jsonResponse(authResponse());
        }

        throw new Error('network unavailable');
      },
    );
    vi.stubGlobal('fetch', fetchMock);

    renderRoute('/console');
    await screen.findByRole('heading', { name: '使用概览' });
    fireEvent.click(screen.getByRole('button', { name: '退出登录' }));

    expect(await waitForLoginForm()).toBeInTheDocument();

    const { getAuthSession } = await import('../../api/client');
    expect(getAuthSession()).toBeNull();
  });

  it('redirects an authenticated login visit to the console', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(authResponse())));

    renderRoute('/login');

    expect(
      await screen.findByRole('heading', { name: '使用概览' }),
    ).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '登录 ZTAPI' })).toBeNull();
  });

  it('uses one main landmark and marks the current console route', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(authResponse())));

    renderRoute('/console/keys');
    await screen.findByRole('heading', { name: 'API 密钥' });

    expect(screen.getAllByRole('main')).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'API 密钥' })).toHaveAttribute(
      'aria-current',
      'page',
    );
    expect(
      screen
        .getAllByRole('link')
        .filter((link) => link.getAttribute('aria-current') === 'page'),
    ).toHaveLength(1);
  });
});
