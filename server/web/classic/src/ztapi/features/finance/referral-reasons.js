// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
const labels = {
  supplier_cost_unknown: ['上游成本尚未核实', 'Supplier cost is unverified'],
  cost_unverified: ['上游成本尚未核实', 'Supplier cost is unverified'],
  Flash_model_mapping_unknown: ['Flash 型号映射尚未核实', 'Flash model mapping is unverified'],
  catalog_or_allowed_routes_unknown: ['模型目录或可用渠道尚未核实', 'Model catalog or allowed routes are unverified'],
  duplicate_or_empty_dimension: ['计费维度为空或重复', 'Billing dimensions are missing or duplicated'],
  evidence_unknown: ['计费维度缺少核验凭据', 'Billing dimension evidence is unverified'],
  amount_or_multiplier_unknown: ['金额、用量上界或倍率尚未核实', 'Amounts, usage bounds or multipliers are unverified'],
  post_referral_margin_below_10pct: ['返利后报价毛利不足 10%', 'Quoted margin after referrals is below 10%'],
  required_dimension_missing_or_duplicate: ['必需计费维度缺失或重复', 'Required billing dimensions are missing or duplicated'],
  dimension_not_in_catalog: ['计费维度不在已核验目录内', 'Billing dimension is outside the verified catalog'],
  schema_missing: ['推广账本结构尚未就绪', 'Referral ledger schema is not ready'],
  finance_capability_unknown: ['财务读取能力尚未核实', 'Finance reporting capability is unverified'],
  worker_not_ready: ['归集任务或心跳尚未就绪', 'Reconciliation worker or heartbeat is not ready'],
  worker_disabled: ['归集任务未启用', 'Reconciliation worker is disabled'],
  platform_fee_unknown: ['平台承担的费用尚未核实', 'Platform-funded fees are unverified'],
  reconciliation_unavailable: ['归集暂不可用，已入账凭据将保留待重试', 'Reconciliation is unavailable; committed evidence is retained for retry'],
  referral_schema_missing: ['财务来源尚无推广账本', 'Referral tables are not available in this source'],
  referral_schema_outdated: ['推广账本版本较旧，财务数据待核对', 'Referral schema needs an upgrade; amounts are unverified'],
  referral_read_failed: ['推广财务读取暂不可用', 'Referral finance data is temporarily unavailable'],
  referral_source_missing: ['旧版财务来源未提供推广数据', 'Legacy finance source has no referral projection'],
  referral_source_unavailable: ['推广财务来源尚未就绪', 'Referral finance source is not ready'],
  referral_snapshot_invalid: ['推广快照时间待核对', 'Referral snapshot time requires verification'],
  referral_ledger_invalid: ['推广流水凭据待核对', 'Referral ledger evidence requires verification'],
  referral_availability_unknown: ['佣金解冻时间待核对', 'Commission availability time requires verification'],
  referral_withdrawal_invalid: ['提现凭据待核对', 'Withdrawal evidence requires verification'],
  referral_payment_invalid: ['付款时间或金额凭据待核对', 'Payment time or amount evidence requires verification'],
  referral_withdrawal_status_unknown: ['提现状态待核对', 'Withdrawal status requires verification'],
  referral_payable_invalid: ['推广应付款余额待核对', 'Referral payable balance requires verification'],
  local_mock_only: ['当前为本地只读模拟', 'Local read-only mock'],
  '采购型号、用量上界、平台费用或财务能力尚未全部核验，暂未开放新邀请': ['准入核验未完成，暂不接受新邀请', 'Readiness checks are incomplete; new invitations are closed'],
  '活动尚未开启': ['活动尚未开启', 'Campaign has not been enabled'],
};
const unknown = ['准入证据待核实，请由管理员核对', 'Readiness evidence requires administrator verification'];

export function referralReason(code) {
  if (typeof code !== 'string') return unknown;
  return labels[code] || labels[code.slice(code.lastIndexOf(':') + 1)] || unknown;
}

export function referralReasons(codes) {
  return [...new Map(codes.filter((code) => typeof code === 'string').map((code) => {
    const label = referralReason(code);
    return [label[0], label];
  })).values()];
}
