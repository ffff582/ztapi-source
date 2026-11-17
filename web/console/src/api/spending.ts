import { getAuthSession, getAuthSessionLifecycle } from './client';

export type SpendingPreferences = {
  enabled: boolean;
  daily_spend_u: string;
  hourly_spend_u: string;
  low_balance_u: string;
  input_tokens: number;
  telegram: boolean;
  version: number;
};
export type SpendingConfig = SpendingPreferences & {
  timezone: 'Asia/Shanghai';
  telegram_bound: boolean;
  telegram_available: boolean;
};
export type SpendingOverview = {
  as_of: string;
  timezone: 'Asia/Shanghai';
  day_start: string;
  complete: boolean;
  confirmed_daily_u: string | null;
  confirmed_hourly_u: string | null;
  confirmed_refunds_daily_u: string | null;
  available_balance_u: string | null;
  pending_requests: number;
  incomplete_requests: number;
};
export type SpendingAlert = {
  id: number;
  kind: 'daily_spend' | 'hourly_spend' | 'low_balance' | 'large_input';
  created_at: string;
  measured_value: string;
  threshold: string;
  unit: 'U' | 'tokens';
  request_id?: string;
  model?: string;
  read_at?: string | null;
  delivery_status: string;
};
export type SpendingInbox = { items: SpendingAlert[]; unread_count: number };
export type SpendingErrorCode = 'unknown' | 'version_conflict' | 'telegram_binding_required' | 'telegram_unavailable';
export class SpendingError extends Error {
  constructor(public readonly status = 0, public readonly code: SpendingErrorCode = 'unknown') {
    super('Spending service request failed.');
    this.name = 'SpendingError';
  }
}

function money(value: unknown, signed = false): value is string {
  return typeof value === 'string' && value.length <= 64 && (signed ? /^-?\d+(?:\.\d{1,6})?$/ : /^\d+(?:\.\d{1,6})?$/).test(value);
}
function threshold(value: unknown): value is string {
  if (value === '') return true;
  if (!money(value) || !/^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$/.test(value)) return false;
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0')) <= 1_000_000_000_000n;
}
const count = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const timestamp = (value: unknown): value is string => typeof value === 'string' && value.length <= 40 && /^\d{4}-\d\d-\d\dT/.test(value) && Number.isFinite(Date.parse(value));

export function validateSpendingPreferences(value: SpendingPreferences): string | null {
  if (![value.daily_spend_u, value.hourly_spend_u, value.low_balance_u].every(threshold)) return '金额须为 0 至 1000000，最多 6 位小数，不支持指数。';
  if (!count(value.input_tokens) || value.input_tokens > 1_000_000_000) return '输入 Token 须为 0 至 1000000000 的整数。';
  if (typeof value.enabled !== 'boolean' || typeof value.telegram !== 'boolean' || !count(value.version)) return '预警设置无效。';
  if (value.enabled && value.input_tokens === 0 && ![value.daily_spend_u, value.hourly_spend_u, value.low_balance_u].some((amount) => /[1-9]/.test(amount))) return '启用预警前，请至少设置一个大于 0 的阈值。';
  return null;
}
function parsePreferences(value: unknown): SpendingPreferences {
  if (!record(value) || validateSpendingPreferences(value as SpendingPreferences) !== null) throw new SpendingError();
  const { enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version } = value as SpendingPreferences;
  return { enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version };
}
function parseConfig(value: unknown): SpendingConfig {
  if (!record(value) || value.timezone !== 'Asia/Shanghai' || typeof value.telegram_bound !== 'boolean' || typeof value.telegram_available !== 'boolean') throw new SpendingError();
  return { ...parsePreferences(value.preferences), timezone: value.timezone, telegram_bound: value.telegram_bound, telegram_available: value.telegram_available };
}
function parseOverview(value: unknown): SpendingOverview {
  if (!record(value) || value.timezone !== 'Asia/Shanghai' || typeof value.complete !== 'boolean' || !timestamp(value.as_of) || !timestamp(value.day_start) || !count(value.pending_requests) || !count(value.incomplete_requests)) throw new SpendingError();
  if (![value.confirmed_daily_u, value.confirmed_hourly_u, value.confirmed_refunds_daily_u].every((amount) => amount === null || money(amount)) || !(value.available_balance_u === null || money(value.available_balance_u, true))) throw new SpendingError();
  return value as SpendingOverview;
}
function parseInbox(value: unknown): SpendingInbox {
  if (!record(value) || !Array.isArray(value.items) || value.items.length > 20 || !count(value.unread_count)) throw new SpendingError();
  const ids = new Set<number>();
  for (const item of value.items) {
    if (!record(item) || !count(item.id) || item.id === 0 || ids.has(item.id) || !['daily_spend', 'hourly_spend', 'low_balance', 'large_input'].includes(String(item.kind)) || !timestamp(item.created_at) || !money(item.measured_value, item.kind === 'low_balance') || !money(item.threshold) || item.unit !== (item.kind === 'large_input' ? 'tokens' : 'U') || typeof item.delivery_status !== 'string') throw new SpendingError();
    if (item.read_at != null && !timestamp(item.read_at)) throw new SpendingError();
    for (const field of ['model', 'request_id']) if (item[field] !== undefined && (typeof item[field] !== 'string' || item[field].length > 512)) throw new SpendingError();
    ids.add(item.id);
  }
  return value as SpendingInbox;
}

async function request<T>(path: string, parse: (value: unknown) => T, init: RequestInit = {}): Promise<T> {
  const session = getAuthSession();
  const lifecycle = getAuthSessionLifecycle();
  if (!session) throw new SpendingError(401);
  const current = () => lifecycle === getAuthSessionLifecycle() && session.user.id === getAuthSession()?.user.id;
  const controller = new AbortController();
  const abort = () => controller.abort();
  const timeout = setTimeout(abort, 15_000);
  init.signal?.addEventListener('abort', abort, { once: true });
  if (init.signal?.aborted) abort();
  try {
    // Reuse apiClient's in-memory identity, not its destructive 401 refresh/replay path.
    // An optional sidecar cannot establish that the main API session is invalid.
    const headers = new Headers(init.headers);
    headers.set('Accept', 'application/json');
    headers.set('Authorization', `Bearer ${session.access_token}`);
    headers.set('New-Api-User', String(session.user.id));
    const response = await fetch(`/api/spending${path}`, { ...init, method: init.method ?? 'GET', credentials: 'include', cache: 'no-store', headers, signal: controller.signal });
    if (!current() || controller.signal.aborted) throw new SpendingError();
    if (!response.ok) {
      let code: SpendingErrorCode = 'unknown';
      if (response.status === 409) {
        try {
          const failure: unknown = await response.json();
          if (record(failure) && failure.success === false) {
            if (failure.message === 'spending_version_conflict') code = 'version_conflict';
            else if (failure.message === 'spending_telegram_binding_required') code = 'telegram_binding_required';
            else if (failure.message === 'spending_telegram_unavailable') code = 'telegram_unavailable';
          }
        } catch { /* Only fixed public codes are recognized. */ }
      }
      if (!current() || controller.signal.aborted) throw new SpendingError();
      throw new SpendingError(response.status, code);
    }
    const envelope: unknown = await response.json();
    if (!current() || controller.signal.aborted || !record(envelope) || envelope.success !== true) throw new SpendingError();
    return parse(envelope.data);
  } catch (error) {
    throw error instanceof SpendingError ? error : new SpendingError();
  } finally {
    clearTimeout(timeout);
    init.signal?.removeEventListener('abort', abort);
  }
}

export const spendingApi = {
  config: (signal?: AbortSignal) => request('/config', parseConfig, { signal }),
  overview: (signal?: AbortSignal) => request('/overview', parseOverview, { signal }),
  alerts: (signal?: AbortSignal) => request('/alerts?limit=20', parseInbox, { signal }),
  save: (input: SpendingPreferences, signal?: AbortSignal) => {
    if (validateSpendingPreferences(input) !== null) return Promise.reject(new SpendingError(400));
    const { enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version } = input;
    return request('/config', parsePreferences, { signal, method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled, daily_spend_u, hourly_spend_u, low_balance_u, input_tokens, telegram, version }) });
  },
  read: (id: number, signal?: AbortSignal) => {
    if (!count(id) || id === 0) return Promise.reject(new SpendingError(400));
    return request(`/alerts/${id}/read`, (value) => {
      if (!record(value) || value.read !== true) throw new SpendingError();
    }, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', signal });
  },
};
