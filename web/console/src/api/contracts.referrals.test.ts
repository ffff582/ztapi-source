import { describe, expect, it } from 'vitest';
import fixture from '../features/referrals/referral.fixture.json';
import { parseReferralOverview, parseReferralWithdrawal, parseReferralCommissionPage } from './contracts';

export const withdrawal = {
  id: 9, version: 2, amount: '40.000000', fee: '1.250000', net_amount: '38.750000',
  address: 'TJRabPrwbZy45sbavfcjinPJC18kjpRTv8', status: 'fee_quoted',
  created_at: '2026-11-08T07:00:00Z', fee_expires_at: '2026-11-08T08:30:00Z', tx_id: '', note: '',
};

describe('referral API data contracts', () => {
  it('keeps micros beyond the JS safe-integer boundary as exact strings', () => {
    const value = { ...fixture, summary: { ...fixture.summary, total_commission: '9007199254.740993' } };
    expect(parseReferralOverview(value).summary.total_commission).toBe('9007199254.740993');
  });

  it.each([20, '20', '20.0000001', '2e1', '-20.000000', '9223372036854.775808'])('rejects non-contract money %s instead of manufacturing a zero balance', (amount) => {
    expect(() => parseReferralOverview({ ...fixture, summary: { ...fixture.summary, available_commission: amount } })).toThrow();
  });

  it('supports disabled campaigns with no start date and no invitation URL', () => {
    expect(parseReferralOverview({ ...fixture, campaign: { ...fixture.campaign, enabled: false, started_at: null }, invite_code: '', invite_url: '' }).campaign.enabled).toBe(false);
  });

  it('rejects missing readiness, unsupported networks and unknown withdrawal states', () => {
    expect(() => parseReferralOverview({ ...fixture, campaign: { ...fixture.campaign, blocked_reasons: undefined } })).toThrow();
    expect(() => parseReferralOverview({ ...fixture, campaign: { ...fixture.campaign, network: 'ERC20' } })).toThrow();
    expect(() => parseReferralWithdrawal({ ...withdrawal, status: 'sent_probably' })).toThrow();
  });

  it('requires a consistent positive net amount for a fee quotation', () => {
    expect(parseReferralWithdrawal(withdrawal).net_amount).toBe('38.750000');
    expect(() => parseReferralWithdrawal({ ...withdrawal, net_amount: '39.000000' })).toThrow();
    expect(() => parseReferralWithdrawal({ ...withdrawal, fee: '40.000000', net_amount: '0.000000' })).toThrow();
  });

  it('does not expose undeclared private fields from commission payloads', () => {
    const page = parseReferralCommissionPage({ total: 1, items: [{ id: 1, invitee_label: '好友 A', principal: '100.000000', commission: '5.000000', reversed: '0.000000', earned_at: fixture.server_time, available_at: '2026-11-15T08:00:00Z', status: 'frozen', email: 'private@example.com' }] });
    expect(page.items[0]).not.toHaveProperty('email');
    expect(page.items[0].commission).toBe('5.000000');
  });

  it('accepts an independently enabled CNY rail and shows only known payment methods', () => {
    const row = { id: 1, invitee_label: '好友 A', principal: '100.000000', commission: '5.000000', reversed: '0.000000', earned_at: fixture.server_time, available_at: fixture.server_time, status: 'frozen', payment_method: 'wxpay' };
    const value = parseReferralOverview({ ...fixture, campaign: { ...fixture.campaign, cny_accepting_orders: true }, commissions: [row] });
    expect(value.campaign.cny_accepting_orders).toBe(true);
    expect(value.commissions[0].payment_method).toBe('wxpay');
    expect(() => parseReferralCommissionPage({ total: 1, items: [{ ...row, payment_method: 'unknown' }] })).toThrow();
  });

  it('normalizes Unix-second backend times and top-level step-up flags without losing micro precision', () => {
    const value = parseReferralOverview({ ...fixture, server_time: 1794124800, campaign: { ...fixture.campaign, started_at: 0 }, requires_totp: true, withdrawal_reauth_available: false,
      commissions: [{ id: 1, invitee_label: '好友 #1', principal: '100.000000', commission: '5.000000', reversed: '0.000000', earned_at: 1794124800, available_at: 1794729600, status: 'frozen' }],
      withdrawals: [{ ...withdrawal, status: 'requested', created_at: 1794124800, fee_expires_at: 0 }] });
    expect(value.server_time).toBe('2026-11-08T08:00:00.000Z');
    expect(value.campaign.started_at).toBeNull();
    expect(value.summary.requires_totp).toBe(true);
    expect(value.summary.withdrawal_reauth_available).toBe(false);
    expect(value.withdrawals[0].fee_expires_at).toBeNull();
    expect(value.commissions[0].available_at).toBe('2026-11-15T08:00:00.000Z');
  });
});
