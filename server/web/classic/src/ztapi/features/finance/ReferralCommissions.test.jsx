import React from 'react';
import { render, screen, within } from '@testing-library/react';
import ReferralCommissions from './ReferralCommissions.jsx';

it.each([undefined, { supported: false, status: 'unsupported', reason: 'referral_schema_missing' }])('keeps old or missing referral sources unknown', (referral) => {
  render(<ReferralCommissions referral={referral} />);
  const region = screen.getByRole('region', { name: '推广佣金' });
  expect(region).toHaveTextContent('待核对');
  expect(region).not.toHaveTextContent('0.000000');
});

it('preserves exact signed amounts and unknown network fees', () => {
  render(<ReferralCommissions referral={{ supported: true, status: 'available', accrued: '9007199254.740993', refund_reversal: '-1.000001', net_commission_expense: '9007199253.740993', manual_loss: '0.000001', frozen_payable: '0.000000', withdrawal_reserved: '20.000000', paid: '40.000000' }} />);
  const region = screen.getByRole('region', { name: '推广佣金' });
  for (const value of ['9007199254.740993', '-1.000001', '9007199253.740993', '0.000001', '0.000000']) expect(within(region).getByText(`${value} USDT`)).toBeVisible();
  expect(region).toHaveTextContent('平台净网络费');
  expect(region).not.toHaveTextContent('NaN');
  expect(within(region).queryByRole('button')).toBeNull();
});
