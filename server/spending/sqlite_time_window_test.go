package spending

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLiteOffsetMixedRefundsAtBeijingMidnight(t *testing.T) {
	for _, firstUTC := range []bool{true, false} {
		t.Run(fmt.Sprint("first_utc_", firstUTC), func(t *testing.T) {
			r, now := sourceFixture(t)
			seedSettlement(t, r, now, 1, "old-refund-parent", "settled", 100000)
			require.NoError(t, r.DB.Exec(`UPDATE ztapi_settlement_log_outboxes SET created_at=?`, now.Add(-30*24*time.Hour).Unix()).Error)
			require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET refunded_quota=10000 WHERE id=1`).Error)
			require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refund_charges VALUES(1,1,'old-refund-parent',1,0,'',10,100000,10000,'{}')`).Error)
			day := emptyOverview(now).DayStart
			events := []time.Time{day.Add(-time.Second), day, now.Add(-time.Minute), now}
			for i, at := range events {
				if (i%2 == 0) == firstUTC {
					at = at.UTC()
				} else {
					at = at.In(Beijing)
				}
				ledgerID, quota := 30+i, int64((i+1)*1000)
				require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(?,1,'old-refund-parent','supplier_refund',?,?,?)`, ledgerID, fmt.Sprint("refund-", i), quota, at).Error)
				require.NoError(t, r.DB.Exec(`INSERT INTO ztapi_supplier_refunds VALUES(?,1,'old-refund-parent','applied',?,?)`, i+1, ledgerID, quota).Error)
			}
			snap, err := r.Load(context.Background(), 1, now)
			require.NoError(t, err)
			require.True(t, snap.Overview.Complete)
			require.Equal(t, "0.01", *snap.Overview.ConfirmedRefundsDailyU, "include midnight and inside, exclude previous day and exact as_of")
			require.Equal(t, "0", *snap.Overview.ConfirmedDailyU)
		})
	}
}

func TestSQLiteOffsetMixedEvidenceUnionWindow(t *testing.T) {
	for _, kind := range []string{"orphan_ledger", "missing_confirmation"} {
		t.Run(kind, func(t *testing.T) {
			for _, utc := range []bool{true, false} {
				t.Run(fmt.Sprint("utc_", utc), func(t *testing.T) {
					for _, position := range []string{"before", "start", "inside", "end"} {
						t.Run(position, func(t *testing.T) {
							r, now := sourceFixture(t)
							start := now.Add(-time.Hour)
							at := start
							included := true
							switch position {
							case "before":
								at = start.Add(-time.Second)
								included = false
							case "inside":
								at = now.Add(-15 * time.Minute)
							case "end":
								at = now
								included = false
							}
							if utc {
								at = at.UTC()
							} else {
								at = at.In(Beijing)
							}
							if kind == "orphan_ledger" {
								require.NoError(t, r.DB.Exec(`INSERT INTO balance_ledgers VALUES(999,1,'unknown','usage_settlement','unproven',-1,?)`, at).Error)
							} else {
								seedSettlement(t, r, now, 1, "no-receipt", "settled", 100000)
								require.NoError(t, r.DB.Exec(`DELETE FROM ztapi_settlement_log_outboxes`).Error)
								require.NoError(t, r.DB.Exec(`UPDATE ztapi_request_settlements SET updated_at=? WHERE id=1`, at).Error)
							}
							snap, err := r.Load(context.Background(), 1, now)
							require.NoError(t, err)
							require.Equal(t, !included, snap.Overview.Complete)
							if included {
								require.Nil(t, snap.Overview.ConfirmedDailyU)
								require.Equal(t, 1, snap.Overview.IncompleteRequests)
							} else {
								require.Equal(t, "0", *snap.Overview.ConfirmedDailyU)
							}
						})
					}
				})
			}
		})
	}
}
