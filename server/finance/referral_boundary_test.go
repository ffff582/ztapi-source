package finance

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestReferralFreezeBoundaryAndRefundPeriod(t *testing.T) {
	s := fixture()
	s.Referral = referralFixture()
	s.Referral.Entries = []ReferralLedgerEntry{
		referralEntry(1, "commission", 5000000, "2026-08-31T16:00:00Z", "2026-10-26T00:00:00Z"),
		referralEntry(2, "refund", -1000000, "2026-09-30T16:00:00Z", "2026-10-26T00:00:00Z"),
	}
	r := calculate(t, s, facts())
	require.Equal(t, "5.000000", *r.Referral.NetCommissionExpense, "October refund cannot change September expense")
	require.Equal(t, "0.000000", *r.Referral.FrozenPayable, "available_at equality is unfrozen")
}

func TestReferralUnavailableReadDropsPartialEvidence(t *testing.T) {
	db := newStore(t).db
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_ledger_entries(id INTEGER,user_id INTEGER,kind TEXT,amount_micros BIGINT,available_at BIGINT,created_at BIGINT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_withdrawals(id INTEGER,amount_micros BIGINT,fee_micros BIGINT,net_amount_micros BIGINT,status TEXT,created_at BIGINT,paid_at BIGINT)").Error)
	require.NoError(t, db.Exec("INSERT INTO ztapi_referral_ledger_entries VALUES(1,1,'commission',5000000,?,?)", moment("2026-09-23T00:00:00Z").Unix(), moment("2026-09-16T00:00:00Z").Unix()).Error)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("referral_test_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_referral_withdrawals" {
			tx.AddError(errors.New("private credential must not leak"))
		}
	}))
	r := (&Reader{DB: db}).loadReferrals(context.Background(), moment("2026-10-26T00:00:00Z"))
	require.Equal(t, "unavailable", r.Status)
	require.Equal(t, "referral_read_failed", r.Reason)
	require.Empty(t, r.Entries)
}

func TestReferralSchemaProbeDoesNotReadWalletColumns(t *testing.T) {
	db := newStore(t).db
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_ledger_entries(id INTEGER,user_id INTEGER,kind TEXT,amount_micros BIGINT,available_at BIGINT,created_at BIGINT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_withdrawals(id INTEGER,amount_micros BIGINT,fee_micros BIGINT,net_amount_micros BIGINT,status TEXT,created_at BIGINT,paid_at BIGINT,address TEXT,tx_id TEXT)").Error)
	var output bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&output, "", 0), logger.Config{LogLevel: logger.Info})})
	s := (&Reader{DB: db}).loadReferrals(context.Background(), moment("2026-10-26T00:00:00Z"))
	require.Equal(t, "available", s.Status)
	require.NotContains(t, output.String(), "SELECT * FROM `ztapi_referral")
}

func TestReferralOptionalReadHasIndependentDeadline(t *testing.T) {
	db := newStore(t).db
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_ledger_entries(id INTEGER,user_id INTEGER,kind TEXT,amount_micros BIGINT,available_at BIGINT,created_at BIGINT)").Error)
	require.NoError(t, db.Exec("CREATE TABLE ztapi_referral_withdrawals(id INTEGER,amount_micros BIGINT,fee_micros BIGINT,net_amount_micros BIGINT,status TEXT,created_at BIGINT,paid_at BIGINT)").Error)
	checked := false
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("referral_deadline", func(tx *gorm.DB) {
		deadline, ok := tx.Statement.Context.Deadline()
		require.True(t, ok, "optional source cannot consume the entire report timeout")
		if ok {
			require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
		}
		checked = true
	}))
	parent := context.Background()
	s := (&Reader{DB: db}).loadReferrals(parent, moment("2026-10-26T00:00:00Z"))
	require.Equal(t, "available", s.Status)
	require.True(t, checked)
	require.NoError(t, parent.Err())
}
