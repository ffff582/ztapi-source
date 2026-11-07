import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearAuthSession, getAuthSession, setAuthSession } from './client';
import { referralsApi, ReferralError, normalizeReferralAmount } from './referrals';
import fixture from '../features/referrals/referral.fixture.json';

const item = { id: 9, version: 2, amount: '40.000000', fee: '1.250000', net_amount: '38.750000', address: 'TJRabPrwbZy45sbavfcjinPJC18kjpRTv8', status: 'fee_quoted', created_at: fixture.server_time, fee_expires_at: '2026-11-08T08:30:00Z', tx_id: '', note: '' };
const session = { access_token: 'memory-only', expires_in: 900, user: { id: 7, username: 'offline', role: 1, group: 'default' } };
const success = (data: unknown) => new Response(JSON.stringify({ success: true, data }), { status: 200 });

beforeEach(() => setAuthSession(session));
afterEach(() => { clearAuthSession(); vi.unstubAllGlobals(); });

describe('referral requests', () => {
  it.each([['20', '20.000000'], ['20.000001', '20.000001'], ['9007199254.740993', '9007199254.740993'], ['19.999999', null], ['20.0000001', null], ['2e1', null], ['+20', null], ['20,00', null], ['9223372036854.775808', null]])('validates amount %s without floating point', (amount, expected) => {
    expect(normalizeReferralAmount(amount, '20.000000', '9223372036854.775807')).toBe(expected);
  });

  it('uses only self-scoped authenticated reads and paginated endpoints', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(success(fixture)).mockResolvedValueOnce(success({ items: [], total: 40 })).mockResolvedValueOnce(success({ items: [], total: 0 }));
    vi.stubGlobal('fetch', fetcher);
    await referralsApi.self();
    await referralsApi.commissions(2);
    await referralsApi.withdrawals(1);
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual(['/api/referrals/self', '/api/referrals/self/commissions?page=2&page_size=20', '/api/referrals/self/withdrawals?page=1&page_size=20']);
    expect(new Headers(fetcher.mock.calls[0][1].headers).get('New-Api-User')).toBe('7');
    expect(new Headers(fetcher.mock.calls[0][1].headers).get('Authorization')).toBe('Bearer memory-only');
  });

  it('reads the anonymous status endpoint as a direct campaign DTO without auth headers', async () => {
    clearAuthSession();
    const fetcher = vi.fn().mockResolvedValue(success({ ...fixture.campaign, enabled: false, accepting_invites: false, started_at: 0 }));
    vi.stubGlobal('fetch', fetcher);
    expect((await referralsApi.status()).enabled).toBe(false);
    expect(fetcher.mock.calls[0][0]).toBe('/api/referrals/status');
    expect(new Headers(fetcher.mock.calls[0][1].headers).has('Authorization')).toBe(false);
    expect(new Headers(fetcher.mock.calls[0][1].headers).has('New-Api-User')).toBe(false);
  });

  it('posts exact strings, an idempotency key and server quote version', async () => {
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(success(item)));
    vi.stubGlobal('fetch', fetcher);
    await referralsApi.request({ amount: '9007199254.740993', address: item.address, password: 'in-memory-password', idempotency_key: 'attempt-1' });
    await referralsApi.confirm(9, 2);
    await referralsApi.cancel(9, 2);
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ amount: '9007199254.740993', address: item.address, password: 'in-memory-password', idempotency_key: 'attempt-1' });
    expect(fetcher.mock.calls.slice(1).map(([url, init]) => [url, JSON.parse(init.body)])).toEqual([['/api/referrals/self/withdrawals/9/confirm', { version: 2 }], ['/api/referrals/self/withdrawals/9/cancel', { version: 2 }]]);
  });

  it('returns controlled conflict codes without leaking server messages or replaying writes', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: false, code: 'quote_expired', message: 'private stack: secret' }), { status: 409 }));
    vi.stubGlobal('fetch', fetcher);
    await expect(referralsApi.confirm(9, 2)).rejects.toMatchObject({ status: 409, code: 'quote_expired' });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(new ReferralError(409).message).not.toContain('secret');
  });

  it('forwards optional OTP only with a password-verified withdrawal request', async () => {
    const fetcher = vi.fn().mockResolvedValue(success(item));
    vi.stubGlobal('fetch', fetcher);
    await referralsApi.request({ amount: '40.000000', address: item.address, password: 'memory-only', totp_code: '012345', idempotency_key: 'attempt-otp' });
    expect(JSON.parse(fetcher.mock.calls[0][1].body).totp_code).toBe('012345');
  });

  it('does not clear the main session or retry a financial mutation on 401', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('{}', { status: 401 }));
    vi.stubGlobal('fetch', fetcher);
    await expect(referralsApi.cancel(9, 2)).rejects.toMatchObject({ status: 401 });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(getAuthSession()?.user.id).toBe(7);
  });

  it('discards reads from an old account after an account switch', async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; })));
    const request = referralsApi.self();
    setAuthSession({ ...session, user: { ...session.user, id: 8 } });
    finish(success(fixture));
    await expect(request).rejects.toBeInstanceOf(ReferralError);
  });

  it.each(['referral_reauthentication_required', 'referral_totp_required', 'referral_conflict'])('preserves the real backend controlled code %s', async (code) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ success: false, code, message: 'raw error must not render' }), { status: code === 'referral_conflict' ? 409 : 403 })));
    await expect(referralsApi.confirm(9, 2)).rejects.toMatchObject({ code });
  });

  it.each([null, {}, { success: 'yes' }])('treats a malformed HTTP 200 financial result as unknown, not a definitive rejection: %s', async (body) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })));
    await expect(referralsApi.confirm(9, 2)).rejects.toMatchObject({ status: 0 });
  });
});
