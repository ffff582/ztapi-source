function sessionFixture(token = 'user-access-token') {
  return {
    access_token: token,
    expires_in: 900,
    user: { id: 23, username: 'customer', role: 1, group: 'default' },
  };
}

describe('ZTAPI classic user API authentication', () => {
  let API;
  let sessionModule;

  beforeAll(async () => {
    vi.resetModules();
    vi.stubEnv('VITE_ZTAPI_ADMIN_APP', 'false');
    vi.doMock('../../helpers/utils', () => ({
      formatMessageForAPI: vi.fn(),
      getUserIdFromLocalStorage: vi.fn(() => '23'),
      isValidMessage: vi.fn(() => true),
      showError: vi.fn(),
    }));
    sessionModule = await import('./user-session');
    ({ API } = await import('../../helpers/api'));
  }, 30_000);

  beforeEach(() => {
    sessionModule.clearUserSession();
    sessionModule.setUserSession(sessionFixture());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  afterAll(() => {
    vi.unstubAllEnvs();
    vi.doUnmock('../../helpers/utils');
  });

  it('attaches the in-memory user session to existing API requests', async () => {
    let capturedConfig;
    API.defaults.adapter = async (config) => {
      capturedConfig = config;
      return {
        config,
        data: { success: true, data: { quota: 100 } },
        headers: {},
        status: 200,
        statusText: 'OK',
      };
    };

    await API.get('/api/user/self');

    expect(capturedConfig.headers.get('Authorization')).toBe(
      'Bearer user-access-token',
    );
    expect(capturedConfig.headers.get('New-API-User')).toBe('23');
  });

  it('refreshes once and retries an expired existing API request', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            success: true,
            data: sessionFixture('refreshed-user-token'),
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    );
    const authorizations = [];
    API.defaults.adapter = async (config) => {
      const authorization = config.headers.get('Authorization');
      authorizations.push(authorization);
      if (authorization === 'Bearer user-access-token') {
        throw Object.assign(new Error('expired'), {
          config,
          response: { status: 401 },
        });
      }
      return {
        config,
        data: { success: true, data: { quota: 100 } },
        headers: {},
        status: 200,
        statusText: 'OK',
      };
    };

    const response = await API.get('/api/user/self');

    expect(response.data.data.quota).toBe(100);
    expect(authorizations).toEqual([
      'Bearer user-access-token',
      'Bearer refreshed-user-token',
    ]);
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledWith(
      '/api/auth/refresh',
      expect.objectContaining({ credentials: 'include', method: 'POST' }),
    );
  });
});
