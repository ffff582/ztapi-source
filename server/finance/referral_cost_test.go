package finance

import (
	"context"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func referralFixture() *ReferralSnapshot {
	return &ReferralSnapshot{Supported: true, Status: "available", AsOf: moment("2026-10-26T00:00:00Z")}
}

func referralEntry(id int64, kind string, amount int64, created, available string) ReferralLedgerEntry {
	return ReferralLedgerEntry{ID: id, UserID: 1, Kind: kind, AmountMicros: amount, CreatedAt: moment(created).Unix(), AvailableAt: moment(available).Unix()}
}

func TestReferralAccrualRefundAndPaymentNeverDoubleCount(t *testing.T) {
	s := fixture()
	s.Referral = referralFixture()
	s.Referral.Entries = []ReferralLedgerEntry{
		referralEntry(1, "commission", 5000000, "2026-09-30T15:59:59Z", "2026-10-07T15:59:59Z"),
		referralEntry(2, "refund", -1000000, "2026-09-30T15:59:59Z", "2026-10-07T15:59:59Z"),
		referralEntry(3, "manual_loss", 700000, "2026-09-30T15:59:59Z", "2026-10-07T15:59:59Z"),
		referralEntry(4, "commission", 9000000, "2026-09-30T16:00:00Z", "2026-11-01T00:00:00Z"),
	}
	r := calculate(t, s, facts())
	require.Equal(t, "5.000000", *r.Referral.Accrued)
	require.Equal(t, "-1.000000", *r.Referral.RefundReversal)
	require.Equal(t, "4.700000", *r.Referral.NetCommissionExpense)
	require.Equal(t, "0.700000", *r.Referral.ManualLoss)
	require.Equal(t, "9.000000", *r.Referral.FrozenPayable)
	require.Equal(t, "15.000000", *r.ActualCost, "upstream costs retain their existing scope")
	require.Equal(t, "0.300000", *r.GrossProfit)
	s.Referral.Withdrawals = []ReferralWithdrawal{
		{ID: 1, AmountMicros: 2000000, FeeMicros: 100000, NetAmountMicros: 1900000, Status: "paid", CreatedAt: moment("2026-09-20T00:00:00Z").Unix(), PaidAt: moment("2026-10-20T00:00:00Z").Unix()},
		{ID: 2, AmountMicros: 1000000, Status: "submitted_unknown", CreatedAt: moment("2026-10-20T00:00:00Z").Unix()},
	}
	r = calculate(t, s, facts())
	require.Equal(t, "2.000000", *r.Referral.Paid)
	require.Equal(t, "1.000000", *r.Referral.WithdrawalReserved)
	require.Equal(t, "4.700000", *r.Referral.NetCommissionExpense)
	require.Equal(t, "0.300000", *r.GrossProfit)
	require.Nil(t, r.Referral.PlatformNetFee, "withheld fee cannot establish actual on-chain fees")
	w := httptest.NewRecorder()
	exportReport(w, r)
	require.Contains(t, w.Body.String(), "net_commission_expense")
	require.Contains(t, w.Body.String(), "4.700000")
}

func TestReferralUnknownDoesNotBecomeZeroOrConfirmedProfit(t *testing.T) {
	for _, source := range []*ReferralSnapshot{nil, {Status: "unsupported", Reason: "referral_schema_missing"}, {Supported: true, Status: "unavailable", Reason: "referral_read_failed"}} {
		s := fixture()
		s.Referral = source
		r := calculate(t, s, facts())
		require.NotNil(t, r.Referral)
		require.Nil(t, r.Referral.NetCommissionExpense)
		require.Nil(t, r.Referral.Paid)
		require.Nil(t, r.GrossProfit)
		require.False(t, r.Confirmed)
		require.Equal(t, "15.000000", *r.ActualCost)
	}
}

func TestReferralExactMicrosAndInvalidEvidence(t *testing.T) {
	s := fixture()
	s.Referral = referralFixture()
	s.Referral.Entries = []ReferralLedgerEntry{
		referralEntry(1, "commission", math.MaxInt64, "2026-09-15T00:00:00Z", "2026-09-22T00:00:00Z"),
		referralEntry(2, "commission", math.MaxInt64, "2026-09-15T00:00:00Z", "2026-09-22T00:00:00Z"),
	}
	r := calculate(t, s, facts())
	require.Equal(t, "18446744073709.551614", *r.Referral.Accrued)
	for _, kind := range []string{"refund", "unknown"} {
		s.Referral.Entries[0].Kind = kind
		r = calculate(t, s, facts())
		require.Equal(t, "unavailable", r.Referral.Status)
		require.Nil(t, r.Referral.Accrued)
		require.Nil(t, r.GrossProfit)
	}
}

func TestReferralReaderProbesLegacySchemaAndRemainsReadOnly(t *testing.T) {
	for _, schema := range []string{"missing", "old", "current"} {
		t.Run(schema, func(t *testing.T) {
			db := newStore(t).db
			if schema != "missing" {
				require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_ledger_entries(id INTEGER,user_id INTEGER,kind TEXT,amount_micros BIGINT,available_at BIGINT,created_at BIGINT)").Error)
				require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_withdrawals(id INTEGER,user_id INTEGER,amount_micros BIGINT,fee_micros BIGINT,net_amount_micros BIGINT,status TEXT,created_at BIGINT,paid_at BIGINT)").Error)
				if schema == "old" {
					require.NoError(t, db.Exec("ALTER TABLE ztapi_referral_withdrawals DROP COLUMN net_amount_micros").Error)
				}
				if schema == "current" {
					require.NoError(t, db.Exec("INSERT INTO ztapi_referral_ledger_entries VALUES(1,1,'commission',5000000,?,?)", moment("2026-09-23T00:00:00Z").Unix(), moment("2026-09-16T00:00:00Z").Unix()).Error)
					require.NoError(t, db.Exec("INSERT INTO ztapi_referral_ledger_entries VALUES(2,1,'commission',5000000,?,?)", moment("2026-11-23T00:00:00Z").Unix(), moment("2026-11-16T00:00:00Z").Unix()).Error)
				}
			}
			require.NoError(t, db.Exec("PRAGMA query_only = ON").Error)
			r := (&Reader{DB: db}).loadReferrals(context.Background(), moment("2026-10-26T00:00:00Z"))
			require.Equal(t, schema == "current", r.Supported)
			if schema == "current" {
				require.Equal(t, "available", r.Status)
				require.Len(t, r.Entries, 1)
			} else {
				require.Equal(t, "unsupported", r.Status)
			}
			var count int64
			require.NoError(t, db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'ztapi_referral_%'").Scan(&count).Error)
			require.Equal(t, map[bool]int64{true: 0, false: 2}[schema == "missing"], count)
		})
	}
}

func TestReferralHTTPAbsentSchemaStillExportsAndReports(t *testing.T) {
	base, token := api(t, common.RoleRootUser)
	h := base.(*handler)
	h.source.(*testSource).s.Referral = &ReferralSnapshot{Status: "unsupported", Reason: "referral_schema_missing"}
	for _, endpoint := range []string{"report", "export"} {
		w := request(h, token, "GET", prefix+endpoint+"?month=2026-09", "", "")
		require.Equal(t, 200, w.Code)
		require.Contains(t, w.Body.String(), "referral_schema_missing")
	}
}
