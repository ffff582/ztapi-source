import {
  clearUserSession,
  getUserSession,
  userLogin,
  userLogout,
  userRefresh,
  userRequest,
} from './user-session';

function sessionFixture(token = 'user-access-token') {
  return {
    access_token: token,
    expires_in: 900,
    user: {
      id: 23,
      username: 'customer',
      role: 1,
      group: 'default',
    },
  };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('ZTAPI user session', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    clearUserSession();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('logs in a regular user and keeps the access token in memory only', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      jsonResponse({ success: true, data: sessionFixture() }),
    );
    vi.stubGlobal('fetch', fetchMock);

    const session = await userLogin({
      username: 'customer',
      password: 'customer-password',
    });

    expect(session).toEqual(sessionFixture());
    expect(getUserSession()).toEqual(sessionFixture());
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/auth/login',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        body: JSON.stringify({
          username: 'customer',
          password: 'customer-password',
        }),
      }),
    );
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it('attaches bearer identity and serializes a user request body', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ success: true, data: { trade_no: 'ZT-USDT-23' } }),
        ),
    );
    const loginFetch = fetch;
    loginFetch.mockResolvedValueOnce(
      jsonResponse({ success: true, data: sessionFixture() }),
    );
    await userLogin({ username: 'customer', password: 'customer-password' });
    loginFetch.mockClear();

    const result = await userRequest({
      method: 'POST',
      url: '/api/user/topup/usdt-trc20/orders',
      body: { amount: 10 },
    });

    expect(result).toEqual({ success: true, data: { trade_no: 'ZT-USDT-23' } });
    expect(loginFetch).toHaveBeenCalledWith(
      '/api/user/topup/usdt-trc20/orders',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        body: JSON.stringify({ amount: 10 }),
        headers: expect.objectContaining({
          Authorization: 'Bearer user-access-token',
          'New-API-User': '23',
          'Content-Type': 'application/json',
        }),
      }),
    );
  });

  it('coalesces refresh and makes local logout authoritative', async () => {
    const fetchMock = vi.fn((url) => {
      if (url === '/api/auth/login') {
        return Promise.resolve(
          jsonResponse({ success: true, data: sessionFixture() }),
        );
      }
      if (url === '/api/auth/refresh') {
        return Promise.resolve(
          jsonResponse({
            success: true,
            data: sessionFixture('refreshed-user-token'),
          }),
        );
      }
      if (url === '/api/auth/logout') {
        return Promise.reject(new Error('network unavailable'));
      }
      throw new Error(`unexpected request: ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    await userLogin({ username: 'customer', password: 'customer-password' });

    const [first, second] = await Promise.all([userRefresh(), userRefresh()]);

    expect(first.access_token).toBe('refreshed-user-token');
    expect(second.access_token).toBe('refreshed-user-token');
    expect(
      fetchMock.mock.calls.filter(([url]) => url === '/api/auth/refresh'),
    ).toHaveLength(1);

    await expect(userLogout()).resolves.toBeUndefined();
    expect(getUserSession()).toBeNull();
  });
});
