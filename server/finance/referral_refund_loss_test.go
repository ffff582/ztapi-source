package finance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReferralPaidRefundLossCannotInflateProfit(t *testing.T) {
	for _, tc := range []struct {
		name               string
		refund, loss, paid int64
		expense, profit    string
	}{
		{"paid_full_refund", -5000000, 5000000, 5000000, "5.000000", "0.000000"},
		{"part_paid_full_refund", -5000000, 2000000, 2000000, "2.000000", "3.000000"},
		{"unpaid_full_refund", -5000000, 0, 0, "0.000000", "5.000000"},
		{"unpaid_partial_refund", -2000000, 0, 0, "3.000000", "2.000000"},
		{"paid_partial_refund", -2000000, 2000000, 5000000, "5.000000", "0.000000"},
		{"payment_alone_is_not_expense", 0, 0, 5000000, "5.000000", "0.000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture()
			s.Referral = referralFixture()
			s.Referral.Entries = []ReferralLedgerEntry{referralEntry(1, "commission", 5000000, "2026-09-10T00:00:00Z", "2026-09-17T00:00:00Z")}
			if tc.refund != 0 {
				s.Referral.Entries = append(s.Referral.Entries, referralEntry(2, "refund", tc.refund, "2026-09-25T00:00:00Z", "2026-09-17T00:00:00Z"))
			}
			if tc.loss != 0 {
				s.Referral.Entries = append(s.Referral.Entries, referralEntry(3, "manual_loss", tc.loss, "2026-09-25T00:00:00Z", "2026-09-17T00:00:00Z"))
			}
			if tc.paid != 0 {
				s.Referral.Withdrawals = []ReferralWithdrawal{{ID: 1, AmountMicros: tc.paid, FeeMicros: 100000, NetAmountMicros: tc.paid - 100000, Status: "paid", CreatedAt: moment("2026-09-20T00:00:00Z").Unix(), PaidAt: moment("2026-09-21T00:00:00Z").Unix()}}
			}
			r := calculate(t, s, facts())
			require.Equal(t, tc.expense, *r.Referral.NetCommissionExpense)
			require.Equal(t, tc.profit, *r.GrossProfit)
			require.Equal(t, "15.000000", *r.ActualCost, "upstream scope is unchanged")
		})
	}
}
