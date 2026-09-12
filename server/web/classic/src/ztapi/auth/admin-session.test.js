/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

import {
  AdminSessionError,
  adminDownload,
  adminLogin,
  adminLogout,
  adminRefresh,
  adminRequest,
  clearAdminSession,
  getAdminSession,
  setAdminSession,
} from './admin-session';

function sessionFixture(overrides = {}) {
  return {
    access_token: 'access-token',
    expires_in: 3600,
    user: {
      id: 7,
      username: 'operator',
      role: 100,
      group: 'default',
      ...overrides,
    },
  };
}

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('ZTAPI admin session', () => {
  let fetchMock;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    localStorage.clear();
    sessionStorage.clear();
    clearAdminSession();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('attaches bearer token and New-API-User without browser persistence', async () => {
    setAdminSession(sessionFixture({ role: 100 }));
    fetchMock.mockResolvedValueOnce(jsonResponse({ success: true, data: [] }));

    await adminRequest({ method: 'GET', url: '/api/channel/' });

    expect(fetchMock).toHaveBeenCalledWith(
      '/api/channel/',
      expect.objectContaining({
        credentials: 'include',
        headers: expect.objectContaining({
          Authorization: 'Bearer access-token',
          'New-API-User': '7',
        }),
      }),
    );
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it('coalesces simultaneous refresh attempts and retries once', async () => {
    const refreshedSession = {
      ...sessionFixture({ role: 100 }),
      access_token: 'refreshed-token',
    };
    setAdminSession(sessionFixture({ role: 100 }));
    fetchMock.mockImplementation(async (url, options) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse({ success: true, data: refreshedSession });
      }

      if (options.headers.Authorization === 'Bearer access-token') {
        return jsonResponse(
          { success: false, message: 'expired access-token' },
          401,
        );
      }

      return jsonResponse({ success: true, data: { ok: true } });
    });

    const results = await Promise.all([
      adminRequest({ method: 'GET', url: '/api/channel/' }),
      adminRequest({ method: 'GET', url: '/api/channel/' }),
    ]);

    expect(results).toEqual([{ ok: true }, { ok: true }]);
    expect(
      fetchMock.mock.calls.filter(([url]) => url === '/api/auth/refresh'),
    ).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(5);
  });

  it('clears the memory session when refresh fails', async () => {
    const secret = 'refresh-secret-token';
    setAdminSession(sessionFixture({ role: 100 }));
    fetchMock.mockImplementation(async (url) => {
      if (url === '/api/auth/refresh') {
        return jsonResponse({ success: false, message: secret }, 401);
      }

      return jsonResponse({ success: false, message: 'expired' }, 401);
    });

    await expect(
      adminRequest({ method: 'GET', url: '/api/channel/' }),
    ).rejects.toThrow();

    expect(getAdminSession()).toBeNull();
    await expect(
      adminRequest({ method: 'GET', url: '/api/channel/' }),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenLastCalledWith(
      '/api/auth/refresh',
      expect.anything(),
    );
    const thrown = await adminRequest({
      method: 'GET',
      url: '/api/channel/',
    }).catch((error) => error);
    expect(thrown.message).not.toContain(secret);
  });

  it('makes local logout authoritative when the server is unavailable', async () => {
    setAdminSession(sessionFixture({ role: 100 }));
    fetchMock.mockRejectedValueOnce(new Error('network failure'));

    await expect(adminLogout()).resolves.toBeUndefined();

    expect(getAdminSession()).toBeNull();
  });

  it('rejects a regular user login without retaining the returned token', async () => {
    const secret = 'regular-user-token';
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        success: true,
        data: sessionFixture({ access_token: secret, role: 1 }),
      }),
    );

    await expect(
      adminLogin({ username: 'user', password: 'password' }),
    ).rejects.toThrow();

    expect(getAdminSession()).toBeNull();
  });

  it('rejects an unknown elevated role during login without retaining the token', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        success: true,
        data: sessionFixture({ access_token: 'unknown-role-token', role: 4 }),
      }),
    );

    await expect(
      adminLogin({ username: 'unknown-role', password: 'password' }),
    ).rejects.toThrow();

    expect(getAdminSession()).toBeNull();
  });

  it('rejects an unknown elevated role returned by refresh', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        success: true,
        data: sessionFixture({
          access_token: 'unknown-refresh-token',
          role: 11,
        }),
      }),
    );

    await expect(adminRefresh()).rejects.toThrow();

    expect(getAdminSession()).toBeNull();
  });

  it('does not expose response body contents in legacy API errors', async () => {
    const secret = 'body-access-token';
    setAdminSession(sessionFixture({ role: 100 }));
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ success: false, message: secret }, 200),
    );

    const error = await adminRequest({
      method: 'GET',
      url: '/api/channel/',
    }).catch((value) => value);

    expect(error).toBeInstanceOf(Error);
    expect(error.message).not.toContain(secret);
  });

  it('preserves only safe conflict data on 409 errors', async () => {
    setAdminSession(sessionFixture({ role: 3 }));
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          success: false,
          message: 'internal conflict detail',
          data: { id: 17, trade_no: 'ZT-17', status: 'success' },
        },
        409,
      ),
    );

    const error = await adminRequest({
      method: 'POST',
      url: '/api/admin/topups/17/complete',
    }).catch((value) => value);

    expect(error).toBeInstanceOf(AdminSessionError);
    expect(error.status).toBe(409);
    expect(error.data).toEqual({
      id: 17,
      trade_no: 'ZT-17',
      status: 'success',
    });
    expect(error.message).not.toContain('internal conflict detail');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('keeps a safe success warning with mutation data', async () => {
    setAdminSession(sessionFixture({ role: 3 }));
    fetchMock.mockResolvedValueOnce(
      jsonResponse({
        success: true,
        data: { id: 17, status: 'success' },
        warning: 'committed_cache_sync_pending',
      }),
    );

    await expect(
      adminRequest({ url: '/api/admin/topups/17/complete', method: 'POST' }),
    ).resolves.toEqual({
      id: 17,
      status: 'success',
      warning: 'committed_cache_sync_pending',
    });
  });

  it('downloads a blob through the in-memory authenticated request path', async () => {
    setAdminSession(sessionFixture({ role: 10 }));
    const csv = 'id,status\n1,success';
    fetchMock.mockResolvedValueOnce(
      new Response(csv, {
        status: 200,
        headers: { 'Content-Type': 'text/csv' },
      }),
    );
    const createObjectURL = vi.fn(() => 'blob:admin-export');
    const revokeObjectURL = vi.fn();
    vi.stubGlobal('URL', { createObjectURL, revokeObjectURL });
    const click = vi
      .spyOn(HTMLAnchorElement.prototype, 'click')
      .mockImplementation(() => {});

    await adminDownload(
      { url: '/api/admin/request-logs/export' },
      'ztapi-request-logs.csv',
    );

    const request = fetchMock.mock.calls[0][1];
    expect(request.headers.Authorization).toBe('Bearer access-token');
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const downloaded = createObjectURL.mock.calls[0][0];
    expect(downloaded.type).toBe('text/csv');
    expect(downloaded.size).toBe(new TextEncoder().encode('id,status\n1,success').byteLength);
    const downloadedText =
      typeof downloaded.text === 'function'
        ? await downloaded.text()
        : await new Promise((resolve, reject) => {
            const reader = new FileReader();
            reader.addEventListener('load', () => resolve(reader.result));
            reader.addEventListener('error', () => reject(reader.error));
            reader.readAsText(downloaded);
          });
    expect(downloadedText).toBe('id,status\n1,success');
    expect(click).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:admin-export');
  });
});
