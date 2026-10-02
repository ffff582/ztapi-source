function sessionFixture(token = 'access-token') {
  return {
    access_token: token,
    expires_in: 3600,
    user: { id: 7, username: 'operator', role: 100, group: 'default' },
  };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('ZTAPI Axios authentication normalization', () => {
  let API;
  let sessionModule;
  let adapterCalls;

  beforeAll(async () => {
    vi.resetModules();
    vi.stubEnv('VITE_ZTAPI_ADMIN_APP', 'true');
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({
      fillRect: vi.fn(),
      fillStyle: '',
    });
    vi.doMock('../../helpers/utils', () => ({
      formatMessageForAPI: vi.fn(),
      getUserIdFromLocalStorage: vi.fn(() => ''),
      isValidMessage: vi.fn(() => true),
      showError: vi.fn(),
    }));
    sessionModule = await import('./admin-session');
    ({ API } = await import('../../helpers/api'));
  }, 30000);

  beforeEach(() => {
    sessionModule.clearAdminSession();
    sessionModule.setAdminSession(sessionFixture());
    adapterCalls = [];
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it('refreshes once and retries once for a legacy 200 auth failure', async () => {
    const refreshedSession = sessionFixture('refreshed-token');
    const fetchMock = vi.fn((url) => {
      if (url === '/api/auth/refresh') {
        return Promise.resolve(
          jsonResponse({ success: true, data: refreshedSession }),
        );
      }
      return Promise.reject(new Error(`unexpected fetch: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
    API.defaults.adapter = async (config) => {
      adapterCalls.push(config);
      const authorization = config.headers.get('Authorization');
      if (authorization === 'Bearer access-token') {
        return {
          config,
          data: {
            success: false,
            code: 'AUTH_REAUTH_REQUIRED',
            message: 'invalid access token',
          },
          headers: {},
          request: {},
          status: 200,
          statusText: 'OK',
        };
      }
      return {
        config,
        data: { success: true, data: { ok: true } },
        headers: { 'Auth-Version': '864b7076dbcd0a3c01b5520316720ebf' },
        request: {},
        status: 200,
        statusText: 'OK',
      };
    };

    const results = await Promise.all([
      API.get('/api/channel/1'),
      API.get('/api/channel/2'),
    ]);

    expect(results.map((response) => response.data)).toEqual([
      { success: true, data: { ok: true } },
      { success: true, data: { ok: true } },
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/auth/refresh',
      expect.objectContaining({ credentials: 'include' }),
    );
    expect(adapterCalls).toHaveLength(4);
    expect(
      adapterCalls.filter(
        (config) =>
          config.headers.get('Authorization') === 'Bearer refreshed-token',
      ),
    ).toHaveLength(2);
  });

  it('does not refresh an operational 200 failure with Auth-Version', async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    sessionModule.clearAdminSession();
    API.defaults.adapter = async (config) => {
      adapterCalls.push(config);
      return {
        config,
        data: { success: false, message: 'public validation failed' },
        headers: {},
        request: {},
        status: 200,
        statusText: 'OK',
      };
    };

    const response = await API.get('/api/public/validation');

    expect(response.data).toEqual({
      success: false,
      message: 'public validation failed',
    });
    expect(fetchMock).not.toHaveBeenCalled();
    expect(adapterCalls).toHaveLength(1);
  });

  it('retries a delayed legacy auth failure using its request-time session identity', async () => {
    const refreshedSession = sessionFixture('refreshed-token');
    let resolveRefresh;
    let resolveDelayedFailure;
    const fetchMock = vi.fn((url) => {
      if (url === '/api/auth/refresh') {
        return new Promise((resolve) => {
          resolveRefresh = () =>
            resolve(jsonResponse({ success: true, data: refreshedSession }));
        });
      }
      return Promise.reject(new Error(`unexpected fetch: ${url}`));
    });
    vi.stubGlobal('fetch', fetchMock);
    API.defaults.adapter = async (config) => {
      adapterCalls.push(config);
      const authorization = config.headers.get('Authorization');
      if (authorization === 'Bearer refreshed-token') {
        return {
          config,
          data: { success: true, data: { ok: true } },
          headers: { 'Auth-Version': 'auth-version' },
          request: {},
          status: 200,
          statusText: 'OK',
        };
      }

      if (adapterCalls.length === 2) {
        return new Promise((resolve) => {
          resolveDelayedFailure = () =>
            resolve({
              config,
              data: { success: false, code: 'AUTH_REAUTH_REQUIRED' },
              headers: {},
              request: {},
              status: 200,
              statusText: 'OK',
            });
        });
      }

      return {
        config,
        data: { success: false, code: 'AUTH_REAUTH_REQUIRED' },
        headers: {},
        request: {},
        status: 200,
        statusText: 'OK',
      };
    };

    const firstRequest = API.get('/api/channel/delayed-a');
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const secondRequest = API.get('/api/channel/delayed-b');
    await vi.waitFor(() => expect(adapterCalls).toHaveLength(2));

    resolveRefresh();
    await vi.waitFor(() => expect(adapterCalls).toHaveLength(3));
    resolveDelayedFailure();

    const results = await Promise.all([firstRequest, secondRequest]);

    expect(results.map((response) => response.data)).toEqual([
      { success: true, data: { ok: true } },
      { success: true, data: { ok: true } },
    ]);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(adapterCalls).toHaveLength(4);
  });
});
