import type { SpendingConfig, SpendingInbox, SpendingOverview } from '../../api/spending';

export const spendingConfig: SpendingConfig = { enabled: false, daily_spend_u: '', hourly_spend_u: '', low_balance_u: '', input_tokens: 0, telegram: false, version: 0, timezone: 'Asia/Shanghai', telegram_bound: false, telegram_available: true };
export const spendingOverview: SpendingOverview = { as_of: '2026-11-02T02:00:00Z', day_start: '2026-11-01T16:00:00Z', timezone: 'Asia/Shanghai', complete: false, confirmed_daily_u: null, confirmed_hourly_u: '0.000001', confirmed_refunds_daily_u: '2.000001', available_balance_u: '12.340000', pending_requests: 3, incomplete_requests: 2 };
export const spendingInbox: SpendingInbox = { unread_count: 1, items: [{ id: 12, kind: 'daily_spend', created_at: '2026-11-02T02:00:00Z', measured_value: '5.000001', threshold: '5', unit: 'U', request_id: `request-${'x'.repeat(100)}`, model: `model-${'m'.repeat(100)}`, delivery_status: 'not_requested' }] };
