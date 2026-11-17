// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
import React from 'react';
import { referralReason } from './referral-reasons.js';

export const referralAmount = (value) => typeof value === 'string' && /^-?\d+\.\d{6}$/.test(value) ? `${value} USDT` : '待核对';

export default function ReferralCommissions({ referral }) {
  const known = referral?.supported === true && referral.status === 'available';
  const reason = referralReason(referral?.reason || 'referral_source_missing');
  return (
    <section aria-label='推广佣金' className='referral-summary'>
      <header className='finance-toolbar'>
        <h2>推广佣金</h2>
        <span className='finance-muted'>{known ? '佣金已归集' : '推广财务待核对'}</span>
      </header>
      {!known && <p className='finance-status'><span>{reason[0]}</span><small lang='en'>{reason[1]}</small></p>}
      <dl className='finance-secondary'>
        {[
          ['本期计提佣金', 'accrued'], ['本期退款冲回', 'refund_reversal'],
          ['本期净佣金费用', 'net_commission_expense'], ['退款未追回损失', 'manual_loss'],
          ['冻结应付款', 'frozen_payable'], ['提现占用', 'withdrawal_reserved'],
          ['累计已清偿', 'paid'], ['平台净网络费', 'platform_net_fee'],
        ].map(([label, key]) => <div key={key}><dt>{label}</dt><dd>{referralAmount(known ? referral[key] : null)}</dd></div>)}
      </dl>
      {known && referral?.as_of && <p className='finance-muted'>应付款快照：{referral.as_of}</p>}
    </section>
  );
}
