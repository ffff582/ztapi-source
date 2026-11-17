package finance

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, e := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "finance.db")), &gorm.Config{})
	require.NoError(t, e)
	sqlDB, e := db.DB()
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	s, e := NewStore(db)
	require.NoError(t, e)
	return s
}
func TestFinancialFactsAppendWithIdempotencyAndRevision(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := moment("2026-10-26T00:00:00Z")
	input := Submission{Kind: "funding", Ref: "ledger:1", Funding: &Funding{Kind: "cash", Amount: "100", Currency: "USDT", Rate: "1", ReceivedAt: now.Unix(), Evidence: "receipt123"}, IdempotencyKey: "idempotency-1"}
	a, e := s.Append(ctx, input, 7, now)
	require.NoError(t, e)
	b, e := s.Append(ctx, input, 7, now.Add(time.Second))
	require.NoError(t, e)
	require.Equal(t, a.ID, b.ID)
	input.Funding.Amount = "90"
	_, e = s.Append(ctx, input, 7, now)
	require.ErrorIs(t, e, ErrConflict)
	input.IdempotencyKey = "idempotency-2"
	_, e = s.Append(ctx, input, 7, now)
	require.ErrorIs(t, e, ErrConflict)
	input.PreviousID = a.ID
	c, e := s.Append(ctx, input, 7, now)
	require.NoError(t, e)
	require.NotEqual(t, a.ID, c.ID)
	all, e := s.List(ctx)
	require.NoError(t, e)
	require.Len(t, all, 2)
	require.Equal(t, 7, all[1].OperatorID)
	require.Error(t, s.db.Model(&factRecord{}).Where("id = ?", a.ID).Update("ref", "changed").Error)
	require.Error(t, s.db.Where("id = ?", a.ID).Delete(&factRecord{}).Error)
}
func TestRejectInvalidCurrencyAndUnprovenCost(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	ctx := context.Background()
	_, e := s.Append(ctx, Submission{Kind: "funding", Ref: "ledger:1", Funding: &Funding{Kind: "cash", Amount: "20", Currency: "CNY", Rate: "", Evidence: "receipt123", ReceivedAt: now.Unix()}, IdempotencyKey: "invalid-1"}, 1, now)
	require.Error(t, e)
	_, e = s.Append(ctx, Submission{Kind: "cost", Ref: "2026-09:2", Cost: &Cost{Month: "2026-09", ChannelID: 2, Supplier: "pool", Gross: "20", Refund: "21", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice123"}, IdempotencyKey: "invalid-2"}, 1, now)
	require.Error(t, e)
}

func TestConfirmedPriorPeriodCreditCanExceedThisMonthsConsumption(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	_, e := s.Append(context.Background(), Submission{Kind: "cost", Ref: "2026-09:2", Cost: &Cost{Month: "2026-09", ChannelID: 2, Supplier: "pool", Gross: "20", Refund: "30", RefundEvidence: "prior-period-credit-note-123", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice123"}, IdempotencyKey: "credit-note-1"}, 1, now)
	require.NoError(t, e)
}

func TestSameSupplierCreditCannotBeDeductedOnTwoChannels(t *testing.T) {
	s := newStore(t)
	now := moment("2026-10-26T00:00:00Z")
	in := Submission{Kind: "cost", Ref: "2026-09:2", Cost: &Cost{Month: "2026-09", ChannelID: 2, Supplier: "pool", Gross: "20", Refund: "5", RefundEvidence: "credit-123", Currency: "USDT", Rate: "1", Complete: true, Evidence: "invoice123"}, IdempotencyKey: "unique-cost-key-1"}
	_, e := s.Append(context.Background(), in, 1, now)
	require.NoError(t, e)
	in.Ref = "2026-09:1"
	in.Cost.ChannelID = 1
	in.Cost.Evidence = "invoice456"
	in.IdempotencyKey = "unique-cost-key-2"
	_, e = s.Append(context.Background(), in, 1, now)
	require.ErrorIs(t, e, ErrConflict)
}
