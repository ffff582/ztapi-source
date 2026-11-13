export type ReferralMoney = string;
export type ReferralWithdrawalStatus = 'requested' | 'fee_quoted' | 'confirmed' | 'paying' | 'submitted_unknown' | 'paid' | 'rejected' | 'cancelled' | 'manual_review';
export interface ReferralCampaign {
  enabled: boolean;
  accepting_invites: boolean;
  cny_accepting_orders?: boolean;
  started_at: string | null;
  rate: '0.05';
  freeze_days: 7;
  term_months: 12;
  minimum_withdrawal: ReferralMoney;
  network: 'TRC20';
  blocked_reasons: string[];
}
export interface ReferralSummary {
  invited_count: number;
  total_commission: ReferralMoney;
  frozen_commission: ReferralMoney;
  available_commission: ReferralMoney;
  withdrawal_reserved: ReferralMoney;
  paid_commission: ReferralMoney;
  manual_loss: ReferralMoney;
  requires_totp?: boolean;
  withdrawal_reauth_available?: boolean;
}
export interface ReferralCommission {
  id: number;
  invitee_label: string;
  principal: ReferralMoney;
  commission: ReferralMoney;
  reversed: ReferralMoney;
  earned_at: string;
  available_at: string;
  status: string;
  payment_method?: 'usdt' | 'alipay' | 'wxpay';
}
export interface ReferralWithdrawal {
  id: number;
  version: number;
  amount: ReferralMoney;
  fee: ReferralMoney | null;
  net_amount: ReferralMoney | null;
  address: string;
  status: ReferralWithdrawalStatus;
  created_at: string;
  fee_expires_at: string | null;
  tx_id: string;
  note: string;
}
export interface ReferralPageData<T> { items: T[]; total: number }
export interface ReferralOverview {
  campaign: ReferralCampaign;
  invite_code: string;
  invite_url: string;
  summary: ReferralSummary;
  commissions: ReferralCommission[];
  withdrawals: ReferralWithdrawal[];
  server_time: string;
  requires_totp?: boolean;
  withdrawal_reauth_available?: boolean;
}
export interface ReferralWithdrawalInput {
  idempotency_key: string;
  amount: ReferralMoney;
  address: string;
  password: string;
  totp_code?: string;
}

export class ReferralContractError extends Error {
  constructor() { super('Invalid referral response.'); this.name = 'ReferralContractError'; }
}
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const text = (v: unknown, max = 2048): v is string => typeof v === 'string' && v.length <= max;
const integer = (v: unknown): v is number => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
const timestamp = (v: unknown): v is string | number => (text(v, 40) && /^\d{4}-\d\d-\d\dT/.test(v) && Number.isFinite(Date.parse(v))) || (integer(v) && v <= 253402300799);
const timeString = (value: string | number): string => typeof value === 'number' ? new Date(value * 1000).toISOString() : value;
const optionalTime = (v: unknown): v is string | number | null => v === null || v === '' || timestamp(v);
const optionalTimeString = (value: string | number | null): string | null => value === null || value === '' || value === 0 ? null : timeString(value);

export function referralMoneyMicros(value: string): bigint {
  return BigInt(value.replace('.', ''));
}
export function isReferralMoney(value: unknown): value is ReferralMoney {
  return text(value, 20) && /^(0|[1-9]\d{0,12})\.\d{6}$/.test(value) && referralMoneyMicros(value) <= 9223372036854775807n;
}
function fail(): never { throw new ReferralContractError(); }

function parseCommission(value: unknown): ReferralCommission {
  if (!record(value) || !integer(value.id) || value.id === 0 || !text(value.invitee_label, 128) || !text(value.status, 64) || !timestamp(value.earned_at) || !timestamp(value.available_at) || !isReferralMoney(value.principal) || !isReferralMoney(value.commission) || !isReferralMoney(value.reversed)) return fail();
  const { id, invitee_label, principal, commission, reversed, earned_at, available_at, status, payment_method } = value;
  if (payment_method !== undefined && payment_method !== 'usdt' && payment_method !== 'alipay' && payment_method !== 'wxpay') return fail();
  return { id, invitee_label, principal, commission, reversed, earned_at: timeString(earned_at), available_at: timeString(available_at), status, ...(payment_method === undefined ? {} : { payment_method }) };
}

const withdrawalStatuses: ReferralWithdrawalStatus[] = ['requested', 'fee_quoted', 'confirmed', 'paying', 'submitted_unknown', 'paid', 'rejected', 'cancelled', 'manual_review'];
export function parseReferralWithdrawal(value: unknown): ReferralWithdrawal {
  if (!record(value) || !integer(value.id) || value.id === 0 || !integer(value.version) || !isReferralMoney(value.amount) || !text(value.address, 128) || !withdrawalStatuses.includes(value.status as ReferralWithdrawalStatus) || !timestamp(value.created_at) || !optionalTime(value.fee_expires_at) || !text(value.tx_id, 256) || !text(value.note, 2048)) return fail();
  if (!(value.fee === null || isReferralMoney(value.fee)) || !(value.net_amount === null || isReferralMoney(value.net_amount))) return fail();
  const status = value.status as ReferralWithdrawalStatus;
  if (['fee_quoted', 'confirmed', 'paying', 'submitted_unknown', 'paid'].includes(status)) {
    if (!isReferralMoney(value.fee) || !isReferralMoney(value.net_amount) || referralMoneyMicros(value.net_amount) <= 0n || referralMoneyMicros(value.fee) + referralMoneyMicros(value.net_amount) !== referralMoneyMicros(value.amount)) return fail();
    if (status === 'fee_quoted' && !timestamp(value.fee_expires_at)) return fail();
  }
  return { id: value.id, version: value.version, amount: value.amount, fee: value.fee, net_amount: value.net_amount, address: value.address, status, created_at: timeString(value.created_at), fee_expires_at: optionalTimeString(value.fee_expires_at), tx_id: value.tx_id, note: value.note };
}

function parsePage<T extends { id: number }>(value: unknown, parse: (entry: unknown) => T): ReferralPageData<T> {
  if (!record(value) || !integer(value.total) || !Array.isArray(value.items) || value.items.length > 20 || value.total < value.items.length) return fail();
  const items = value.items.map(parse);
  if (new Set(items.map((item) => item.id)).size !== items.length) return fail();
  return { items, total: value.total };
}
export const parseReferralCommissionPage = (value: unknown) => parsePage(value, parseCommission);
export const parseReferralWithdrawalPage = (value: unknown) => parsePage(value, parseReferralWithdrawal);

export function parseReferralCampaign(value: unknown): ReferralCampaign {
  if (!record(value)) return fail();
  const c = value;
  if (typeof c.enabled !== 'boolean' || typeof c.accepting_invites !== 'boolean' || !optionalTime(c.started_at) || c.rate !== '0.05' || c.freeze_days !== 7 || c.term_months !== 12 || c.minimum_withdrawal !== '20.000000' || c.network !== 'TRC20' || !Array.isArray(c.blocked_reasons) || c.blocked_reasons.length > 100 || !c.blocked_reasons.every((reason) => text(reason, 512))) return fail();
  if (c.cny_accepting_orders !== undefined && typeof c.cny_accepting_orders !== 'boolean') return fail();
  return { enabled: c.enabled, accepting_invites: c.accepting_invites, ...(c.cny_accepting_orders === undefined ? {} : { cny_accepting_orders: c.cny_accepting_orders }), started_at: optionalTimeString(c.started_at), rate: c.rate, freeze_days: c.freeze_days, term_months: c.term_months, minimum_withdrawal: c.minimum_withdrawal, network: c.network, blocked_reasons: [...c.blocked_reasons] as string[] };
}

export function parseReferralOverview(value: unknown): ReferralOverview {
  if (!record(value) || !record(value.campaign) || !record(value.summary) || !timestamp(value.server_time) || !text(value.invite_code, 128) || !text(value.invite_url, 2048) || !Array.isArray(value.commissions) || !Array.isArray(value.withdrawals)) return fail();
  const campaign = parseReferralCampaign(value.campaign);
  const s = value.summary;
  if (!integer(s.invited_count) || !isReferralMoney(s.total_commission) || !isReferralMoney(s.frozen_commission) || !isReferralMoney(s.available_commission) || !isReferralMoney(s.withdrawal_reserved) || !isReferralMoney(s.paid_commission) || !isReferralMoney(s.manual_loss)) return fail();
  for (const flag of ['requires_totp', 'withdrawal_reauth_available']) {
    if ((s[flag] !== undefined && typeof s[flag] !== 'boolean') || (value[flag] !== undefined && typeof value[flag] !== 'boolean')) return fail();
  }
  const flags = {
    ...(s.requires_totp === undefined && value.requires_totp === undefined ? {} : { requires_totp: s.requires_totp === true || value.requires_totp === true }),
    ...(s.withdrawal_reauth_available === undefined && value.withdrawal_reauth_available === undefined ? {} : { withdrawal_reauth_available: s.withdrawal_reauth_available !== false && value.withdrawal_reauth_available !== false }),
  };
  return {
    campaign,
    summary: { invited_count: s.invited_count, total_commission: s.total_commission, frozen_commission: s.frozen_commission, available_commission: s.available_commission, withdrawal_reserved: s.withdrawal_reserved, paid_commission: s.paid_commission, manual_loss: s.manual_loss, ...flags },
    invite_code: value.invite_code, invite_url: value.invite_url,
    commissions: value.commissions.map(parseCommission), withdrawals: value.withdrawals.map(parseReferralWithdrawal), server_time: timeString(value.server_time), ...flags,
  };
}
