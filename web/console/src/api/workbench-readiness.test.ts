import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, refreshSession, setAuthSession } from './client';
import type { UserToken } from './contracts';
import { fetchWorkbenchKeys, keyReadiness } from './workbench-readiness';

const token: UserToken = {
  id: 5, status: 1, name: 'fixture', created_time: 1, accessed_time: 0,
  expired_time: -1, remain_quota: 100, unlimited_quota: false,
  model_limits_enabled: false, model_limits: '', allow_ips: '', key_prefix: 'sk-zt-safe...', key_copyable: false,
};
function session(id = 9) {
  return { access_token: `session-${id}`, expires_in: 900, user: { id, username: 'fixture', role: 1, group: 'default' } };
}
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const page = (items: unknown[] = [token], total = items.length) => ({ success: true, data: { page: 1, page_size: 100, total, items } });

describe('workbench Key metadata contract', () => {
  beforeEach(() => setAuthSession(session()));
  afterEach(() => { clearAuthSession(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it.each([200, 401])('accepts a delayed %s after another request refreshed the same login', async (status) => {
    let finish!: (value: Response) => void;
    let reads = 0;
    const fetchMock = vi.fn(async (...args: [RequestInfo | URL, RequestInit?]) => {
      const input = args[0];
      if (input.toString() === '/api/auth/refresh') return response({ success: true, data: { ...session(), access_token: 'fresh-token' } });
      if (++reads === 1) return new Promise<Response>((resolve) => { finish = resolve; });
      return response(page());
    });
    vi.stubGlobal('fetch', fetchMock);
    const result = fetchWorkbenchKeys().catch((error: unknown) => error);
    await refreshSession();
    finish(response(status === 200 ? page() : {}, status));
    expect(await result).toEqual([token]);
    expect(reads).toBe(status === 200 ? 1 : 2);
    if (status === 401) expect(fetchMock.mock.calls.at(-1)?.[1]?.headers).toMatchObject({ Authorization: 'Bearer fresh-token' });
    expect(fetchMock.mock.calls.filter(([input]) => input.toString() === '/api/auth/refresh')).toHaveLength(1);
  });

  it('accepts metadata when the same login refreshes while the response body is being read', async () => {
    let finishBody!: (value: unknown) => void;
    let started!: () => void;
    const reading = new Promise<void>((resolve) => { started = resolve; });
    const metadata = response(page());
    vi.spyOn(metadata, 'json').mockImplementation(() => {
      started();
      return new Promise((resolve) => { finishBody = resolve; });
    });
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => input.toString() === '/api/auth/refresh'
      ? response({ success: true, data: { ...session(), access_token: 'fresh-token' } }) : metadata));
    const result = fetchWorkbenchKeys().catch((error: unknown) => error);
    await reading;
    await refreshSession();
    finishBody(page());
    expect(await result).toEqual([token]);
  });

  it.each([200, 401])('rejects a delayed %s after the same session object is used in a new login', async (status) => {
    const original = getAuthSession()!;
    let finish!: (value: Response) => void;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/auth/refresh') return response({ success: true, data: { ...session(), access_token: 'unexpected-refresh' } });
      if (fetchMock.mock.calls.length > 1) return response(page());
      return new Promise<Response>((resolve) => { finish = resolve; });
    });
    vi.stubGlobal('fetch', fetchMock);
    const result = fetchWorkbenchKeys().catch((error: unknown) => error);
    clearAuthSession();
    setAuthSession(original);
    finish(response(status === 200 ? page() : {}, status));
    expect(await result).toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each([
    [{ expired_time: 100 }, 100, 'available', true],
    [{ expired_time: 100 }, 101, 'expired', false],
    [{ unlimited_quota: true, remain_quota: 0 }, 100, 'available', true],
    [{ unlimited_quota: false, remain_quota: 0 }, 100, 'exhausted', true],
    [{ status: 4, unlimited_quota: true }, 100, 'exhausted', false],
    [{ status: 2 }, 100, 'disabled', false],
    [{ status: 3 }, 100, 'expired', false],
    [{ status: 99 }, 100, 'unavailable', false],
  ])('distinguishes billable and status-only access for %j', (changes, now, status, canQuery) => {
    expect(keyReadiness([{ ...token, ...changes }], now)).toEqual({ status, canQuery });
  });

  it('does not infer backend availability from a usable key outside the newest 100', () => {
    const disabled = Array.from({ length: 100 }, (_, index) => ({ ...token, id: 10 + index, status: 2 }));
    expect(keyReadiness([token, ...disabled], 100)).toEqual({ status: 'disabled', canQuery: false });
  });

  it('uses only the safe newest-100 list endpoint and session credentials', async () => {
    const fetchMock = vi.fn(async () => response(page()));
    vi.stubGlobal('fetch', fetchMock);
    expect(await fetchWorkbenchKeys()).toEqual([token]);
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith('/api/token/?p=1&page_size=100', expect.objectContaining({
      method: 'GET', cache: 'no-store', credentials: 'include',
      headers: expect.objectContaining({ Authorization: 'Bearer session-9', 'New-Api-User': '9' }),
    }));
  });

  it.each([
    page([{ ...token, key: 'must-not-expose' }]),
    page([{ ...token, user_id: 10 }]),
    page([token], 100),
    { success: false, data: page().data },
  ])('rejects unsafe or incomplete metadata instead of enabling submission', async (body) => {
    vi.stubGlobal('fetch', vi.fn(async () => response(body)));
    await expect(fetchWorkbenchKeys()).rejects.toThrow();
  });

  it('does not retry an old 401 under a replacement account', async () => {
    let finish!: (value: Response) => void;
    const fetchMock = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal('fetch', fetchMock);
    const result = fetchWorkbenchKeys().catch((error: unknown) => error);
    setAuthSession(session(10));
    finish(response({}, 401));
    expect(await result).toMatchObject({ kind: 'unauthorized' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('refreshes a same-account expired session once without reading full API keys', async () => {
    let listCalls = 0;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (input.toString() === '/api/auth/refresh') return response({ success: true, data: { ...session(), access_token: 'refreshed-session' } });
      listCalls += 1;
      return listCalls === 1 ? response({}, 401) : response(page());
    });
    vi.stubGlobal('fetch', fetchMock);
    expect(await fetchWorkbenchKeys()).toEqual([token]);
    expect(getAuthSession()?.access_token).toBe('refreshed-session');
    expect(fetchMock.mock.calls.map(([input]) => input.toString())).toEqual([
      '/api/token/?p=1&page_size=100', '/api/auth/refresh', '/api/token/?p=1&page_size=100',
    ]);
  });
});
