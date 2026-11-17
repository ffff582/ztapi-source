import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from './client';
import { spendingApi, SpendingError, validateSpendingPreferences } from './spending';

const preferences = { enabled: false, daily_spend_u: '', hourly_spend_u: '', low_balance_u: '', input_tokens: 0, telegram: false, version: 0 };
const session = { access_token: 'memory-only', expires_in: 900, user: { id: 7, username: 'alice', role: 1, group: 'default' } };
const response = (data: unknown, status = 200) => new Response(JSON.stringify({ success: status === 200, data, message: 'fixed_error' }), { status });
beforeEach(() => setAuthSession(session));
afterEach(() => { clearAuthSession(); vi.unstubAllGlobals(); });

it('keeps decimal strings and authenticates owner-scoped, no-store requests', async () => {
  const fetcher = vi.fn().mockResolvedValue(response({ preferences: { ...preferences, daily_spend_u: '0.000001' }, timezone: 'Asia/Shanghai', telegram_bound: false, telegram_available: true }));
  vi.stubGlobal('fetch', fetcher);
  const config = await spendingApi.config();
  expect(config.daily_spend_u).toBe('0.000001');
  const [url, init] = fetcher.mock.calls[0];
  expect(url).toBe('/api/spending/config');
  expect(new Headers(init.headers).get('Authorization')).toBe('Bearer memory-only');
  expect(new Headers(init.headers).get('New-Api-User')).toBe('7');
  expect(init).toMatchObject({ credentials: 'include', cache: 'no-store', method: 'GET' });
});

it.each([401, 403, 503])('isolates optional service %s without refresh or clearing the main session', async (status) => {
  const fetcher = vi.fn().mockResolvedValue(response(null, status));
  vi.stubGlobal('fetch', fetcher);
  await expect(spendingApi.config()).rejects.toMatchObject({ status });
  expect(getAuthSession()).toBe(session);
  expect(fetcher).toHaveBeenCalledTimes(1);
});

it('preserves 409 for explicit conflict handling and never replays a save', async () => {
  const fetcher = vi.fn().mockResolvedValue(response(null, 409));
  vi.stubGlobal('fetch', fetcher);
  await expect(spendingApi.save(preferences)).rejects.toEqual(new SpendingError(409));
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual(preferences);
});

it.each([
  ['spending_version_conflict', 'version_conflict'],
  ['spending_telegram_binding_required', 'telegram_binding_required'],
  ['spending_telegram_unavailable', 'telegram_unavailable'],
  ['private_unrecognized_detail', 'unknown'],
])('classifies fixed 409 code %s without exposing arbitrary server details', async (message, code) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: false, message }), { status: 409 })));
  await expect(spendingApi.save(preferences)).rejects.toMatchObject({ status: 409, code });
});

it('rejects a previous login response even when the same owner logs in again', async () => {
  let resolve!: (value: Response) => void;
  vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((done) => { resolve = done; })));
  const pending = spendingApi.overview();
  setAuthSession({ ...session, access_token: 'new-login' });
  resolve(response({ as_of: '2026-11-02T00:00:00Z', day_start: '2026-11-01T16:00:00Z', timezone: 'Asia/Shanghai', complete: false, confirmed_daily_u: null, confirmed_hourly_u: null, available_balance_u: null, pending_requests: 2, incomplete_requests: 1 }));
  await expect(pending).rejects.toMatchObject({ status: 0 });
});

it('preserves unavailable totals and fails closed on malformed financial evidence', async () => {
  const overview = { as_of: '2026-11-02T00:00:00Z', day_start: '2026-11-01T16:00:00Z', timezone: 'Asia/Shanghai', complete: false, confirmed_daily_u: null, confirmed_hourly_u: '0.000001', confirmed_refunds_daily_u: null, available_balance_u: null, pending_requests: 2, incomplete_requests: 1 };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response(overview)).mockResolvedValueOnce(response({ ...overview, confirmed_daily_u: 0 })));
  expect(await spendingApi.overview()).toEqual(overview);
  await expect(spendingApi.overview()).rejects.toBeInstanceOf(SpendingError);
});

it('rejects missing refund evidence rather than manufacturing zero or netting gross spend', async () => {
  const overview = { as_of: '2026-11-02T00:00:00Z', day_start: '2026-11-01T16:00:00Z', timezone: 'Asia/Shanghai', complete: true, confirmed_daily_u: '10', confirmed_hourly_u: '3', available_balance_u: '12', pending_requests: 0, incomplete_requests: 0 };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response(overview)).mockResolvedValueOnce(response({ ...overview, confirmed_refunds_daily_u: '2.000001' })));
  await expect(spendingApi.overview()).rejects.toBeInstanceOf(SpendingError);
  expect(await spendingApi.overview()).toMatchObject({ confirmed_daily_u: '10', confirmed_refunds_daily_u: '2.000001' });
});

it('requests only the recent 20 and acknowledges an owner record with strict empty JSON', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], unread_count: 0 })).mockResolvedValueOnce(response({ read: true }));
  vi.stubGlobal('fetch', fetcher);
  expect(await spendingApi.alerts()).toEqual({ items: [], unread_count: 0 });
  await spendingApi.read(12);
  expect(fetcher.mock.calls[0][0]).toBe('/api/spending/alerts?limit=20');
  expect(fetcher.mock.calls[1][0]).toBe('/api/spending/alerts/12/read');
  expect(fetcher.mock.calls[1][1].method).toBe('POST');
  expect(JSON.parse(fetcher.mock.calls[1][1].body)).toEqual({});
  expect(new Headers(fetcher.mock.calls[1][1].headers).get('Content-Type')).toBe('application/json');
});

it('accepts the PUT preferences-only response without inventing binding state', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ ...preferences, version: 1 })));
  expect(await spendingApi.save(preferences)).toEqual({ ...preferences, version: 1 });
});

it('rejects an unconfirmed read acknowledgement rather than fabricating a read state', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ read: false })));
  await expect(spendingApi.read(12)).rejects.toBeInstanceOf(SpendingError);
});

it.each(['-0.000001', '-12.340000'])('keeps a negative low_balance measurement %s without rejecting the whole inbox', async (measured_value) => {
  const alert = { id: 1, kind: 'daily_spend', created_at: '2026-11-02T02:00:00Z', measured_value: '5', threshold: '4', unit: 'U', delivery_status: 'not_requested' };
  const inbox = { items: [alert, { ...alert, id: 2, kind: 'low_balance', measured_value, threshold: '1' }], unread_count: 2 };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response(inbox)));
  expect(await spendingApi.alerts()).toEqual(inbox);
});

it.each(['daily_spend', 'hourly_spend', 'large_input'])('still rejects negative measured_value for %s', async (kind) => {
  const alert = { id: 1, kind, created_at: '2026-11-02T02:00:00Z', measured_value: '-1', threshold: '1', unit: kind === 'large_input' ? 'tokens' : 'U', delivery_status: 'not_requested' };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ items: [alert], unread_count: 1 })));
  await expect(spendingApi.alerts()).rejects.toBeInstanceOf(SpendingError);
});

it.each(['daily_spend', 'hourly_spend', 'low_balance', 'large_input'])('still rejects a negative threshold for %s', async (kind) => {
  const alert = { id: 1, kind, created_at: '2026-11-02T02:00:00Z', measured_value: '0', threshold: '-1', unit: kind === 'large_input' ? 'tokens' : 'U', delivery_status: 'not_requested' };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ items: [alert], unread_count: 1 })));
  await expect(spendingApi.alerts()).rejects.toBeInstanceOf(SpendingError);
});

it.each(['--1', '-1e3', '-Infinity', '-0.0000001', '+1'])('rejects malformed signed low_balance measured_value %s', async (measured_value) => {
  const alert = { id: 1, kind: 'low_balance', created_at: '2026-11-02T02:00:00Z', measured_value, threshold: '1', unit: 'U', delivery_status: 'not_requested' };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ items: [alert], unread_count: 1 })));
  await expect(spendingApi.alerts()).rejects.toBeInstanceOf(SpendingError);
});

it.each(['-1', 'NaN', 'Infinity', '1e2', '0.0000001', '1000000.000001', '1.', ' 1', '.5', '0001.000001'])('rejects invalid money %s', (value) => {
  expect(validateSpendingPreferences({ ...preferences, daily_spend_u: value })).not.toBeNull();
});
it.each(['', '0', '0.000000', '1000000', '1.000001'])('accepts exact bounded money %s without float conversion', (value) => {
  expect(validateSpendingPreferences({ ...preferences, daily_spend_u: value })).toBeNull();
});
it('requires one positive threshold only when enabled and bounds ordinary input tokens', () => {
  expect(validateSpendingPreferences(preferences)).toBeNull();
  expect(validateSpendingPreferences({ ...preferences, enabled: true, daily_spend_u: '0' })).not.toBeNull();
  expect(validateSpendingPreferences({ ...preferences, enabled: true, input_tokens: 1 })).toBeNull();
  for (const input_tokens of [-1, 1.5, 1_000_000_001, NaN]) expect(validateSpendingPreferences({ ...preferences, input_tokens })).not.toBeNull();
});
