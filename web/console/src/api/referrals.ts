import { getAuthSession, getAuthSessionLifecycle } from './client';
import {
  isReferralMoney, referralMoneyMicros, parseReferralCampaign, parseReferralOverview, parseReferralCommissionPage,
  parseReferralWithdrawalPage, parseReferralWithdrawal, type ReferralWithdrawalInput,
} from './contracts';

export class ReferralError extends Error {
  constructor(public readonly status = 0, public readonly code = 'unknown') {
    super('Referral request failed.');
    this.name = 'ReferralError';
  }
}

export function normalizeReferralAmount(value: string, minimum: string, available: string): string | null {
  if (!/^(0|[1-9]\d{0,12})(?:\.\d{1,6})?$/.test(value) || !isReferralMoney(minimum) || !isReferralMoney(available)) return null;
  const [whole, fraction = ''] = value.split('.');
  const amount = `${whole}.${fraction.padEnd(6, '0')}`;
  if (!isReferralMoney(amount)) return null;
  const micros = referralMoneyMicros(amount);
  return micros >= referralMoneyMicros(minimum) && micros <= referralMoneyMicros(available) ? amount : null;
}

export const isTronAddress = (value: string) => /^T[1-9A-HJ-NP-Za-km-z]{33}$/.test(value);
const controlledCodes = new Set(['version_conflict', 'quote_expired', 'reauthentication_required', 'password_required', 'invalid_password', 'totp_required', 'invalid_totp', 'campaign_disabled', 'readiness_blocked', 'insufficient_commission', 'referral_not_ready', 'referral_not_found', 'referral_conflict', 'referral_insufficient', 'referral_invalid', 'referral_unavailable', 'referral_reauthentication_required', 'referral_totp_required']);

async function request<T>(path: string, parse: (value: unknown) => T, body?: unknown, signal?: AbortSignal, anonymous = false): Promise<T> {
  const session = anonymous ? null : getAuthSession();
  const lifecycle = getAuthSessionLifecycle();
  if (!anonymous && !session) throw new ReferralError(401);
  const controller = new AbortController();
  const abort = () => controller.abort();
  const timeout = setTimeout(abort, 15_000);
  signal?.addEventListener('abort', abort, { once: true });
  if (signal?.aborted) abort();
  const current = () => !controller.signal.aborted && (anonymous || (lifecycle === getAuthSessionLifecycle() && session?.user.id === getAuthSession()?.user.id));
  try {
    // Financial mutations are never automatically refreshed or replayed.
    const response = await fetch(anonymous ? '/api/referrals/status' : `/api/referrals/self${path}`, {
      method: body === undefined ? 'GET' : 'POST', credentials: 'include', cache: 'no-store', signal: controller.signal,
      headers: { Accept: 'application/json', ...(session ? { Authorization: `Bearer ${session.access_token}`, 'New-Api-User': String(session.user.id) } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (!current()) throw new ReferralError();
    let envelope: unknown;
    try { envelope = await response.json(); } catch { throw new ReferralError(response.ok ? 0 : response.status); }
    if (!current()) throw new ReferralError();
    const failed = typeof envelope === 'object' && envelope !== null && 'success' in envelope && envelope.success === false;
    if (!response.ok || failed) {
      const code = typeof envelope === 'object' && envelope !== null && 'code' in envelope && typeof envelope.code === 'string' && controlledCodes.has(envelope.code) ? envelope.code : 'unknown';
      throw new ReferralError(response.ok ? 400 : response.status, code);
    }
    if (typeof envelope !== 'object' || envelope === null || !('success' in envelope) || envelope.success !== true || !('data' in envelope)) throw new ReferralError();
    return parse(envelope.data);
  } catch (error) {
    throw error instanceof ReferralError ? error : new ReferralError();
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener('abort', abort);
  }
}

const validPage = (page: number) => Number.isSafeInteger(page) && page > 0;
const validVersion = (id: number, version: number) => validPage(id) && Number.isSafeInteger(version) && version >= 0;
export const referralsApi = {
  status: (signal?: AbortSignal) => request('', parseReferralCampaign, undefined, signal, true),
  self: (signal?: AbortSignal) => request('', parseReferralOverview, undefined, signal),
  commissions: (page: number, signal?: AbortSignal) => validPage(page) ? request(`/commissions?page=${page}&page_size=20`, parseReferralCommissionPage, undefined, signal) : Promise.reject(new ReferralError(400)),
  withdrawals: (page: number, signal?: AbortSignal) => validPage(page) ? request(`/withdrawals?page=${page}&page_size=20`, parseReferralWithdrawalPage, undefined, signal) : Promise.reject(new ReferralError(400)),
  request: (input: ReferralWithdrawalInput) => {
    if (!isReferralMoney(input.amount) || referralMoneyMicros(input.amount) < 20_000_000n || !isTronAddress(input.address) || !input.password || input.password.length > 1024 || !/^[A-Za-z0-9_-]{1,128}$/.test(input.idempotency_key) || (input.totp_code !== undefined && !/^\d{6}$/.test(input.totp_code))) return Promise.reject(new ReferralError(400));
    const { idempotency_key, amount, address, password, totp_code } = input;
    return request('/withdrawals', parseReferralWithdrawal, { idempotency_key, amount, address, password, ...(totp_code === undefined ? {} : { totp_code }) });
  },
  confirm: (id: number, version: number) => validVersion(id, version) ? request(`/withdrawals/${id}/confirm`, parseReferralWithdrawal, { version }) : Promise.reject(new ReferralError(400)),
  cancel: (id: number, version: number) => validVersion(id, version) ? request(`/withdrawals/${id}/cancel`, parseReferralWithdrawal, { version }) : Promise.reject(new ReferralError(400)),
};
