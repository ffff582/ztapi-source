package model

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIWorkbenchExpiredHoldsRecoverOnlyUnusedReservations(t *testing.T) {
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIWorkbenchTask{}, &ZTAPIRequestAttempt{}))
	verifyWorkbenchExpiredHolds(t, db, input, "sqlite")
}

func verifyWorkbenchExpiredHolds(t *testing.T, db *gorm.DB, input ZTAPIRequestSettlement, namespace string) {
	t.Helper()
	now := time.Now().UTC()
	type item struct{ name, state string }
	items := []item{{"expired", ZTAPISettlementReleased}, {"deleted", ZTAPISettlementReleased},
		{"active", ZTAPISettlementReserved}, {"dispatched", ZTAPISettlementReserved},
		{"attempt", ZTAPISettlementReserved}, {"ordinary", ZTAPISettlementReserved},
		{"intent", ZTAPISettlementReserved}, {"recent", ZTAPISettlementReserved}}
	for i, item := range items {
		id := namespace + "-" + item.name
		key := Token{UserId: input.UserID, KeyHash: id, RemainQuota: 100, WorkbenchOnly: item.name != "ordinary", ExpiredTime: now.Add(time.Minute).Unix()}
		require.NoError(t, db.Create(&key).Error)
		hold := input
		hold.OperationID, hold.RequestID, hold.TokenID, hold.ReservedQuota = id, id, key.Id, 50
		hold.CreatedAt = now.Add(-20 * time.Minute)
		if item.name == "ordinary" {
			_, err := BeginZTAPIRequestSettlement(hold)
			require.NoError(t, err)
			require.NoError(t, db.Model(&key).Update("expired_time", now.Add(-10*time.Minute).Unix()).Error)
			continue
		}
		_, err := BeginZTAPIWorkbenchBatch(fmt.Sprintf("5afe26a0-c454-456b-862e-%012d", input.UserID*100+i), 700, []ZTAPIWorkbenchLeg{{
			Stage: "main", Settlement: hold, Native: &ZTAPIWorkbenchNativeBounds{BodySHA256: fmt.Sprintf("%064d", i), OutboundBytes: 1024, MaxOutput: 64},
		}})
		require.NoError(t, err)
		if item.name != "active" {
			expiry := now.Add(-10 * time.Minute)
			if item.name == "recent" {
				expiry = now.Add(-20 * time.Second)
			}
			require.NoError(t, db.Model(&key).Update("expired_time", expiry.Unix()).Error)
		}
		switch item.name {
		case "deleted":
			require.NoError(t, db.Delete(&key).Error)
		case "dispatched":
			require.NoError(t, db.Model(&ZTAPIRequestSettlement{}).Where("operation_id = ?", id).Update("dispatched", true).Error)
		case "attempt":
			var row ZTAPIRequestSettlement
			require.NoError(t, db.Where("operation_id = ?", id).Take(&row).Error)
			require.NoError(t, db.Create(&ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: 1, ChannelID: 1, CredentialVersion: "fixture", Protocol: "fixture"}).Error)
		case "intent":
			require.NoError(t, db.Create(&ZTAPISettlementFinalizationIntent{OperationID: id, RequestID: id,
				UsageJSON: "{}", ChargeDimensionsJSON: "[]", EvidenceJSON: "{}", PayloadSHA256: fmt.Sprintf("%064d", i)}).Error)
		}
	}
	count, err := ReleaseZTAPIExpiredWorkbenchHolds(now, 1)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var wg sync.WaitGroup
	type result struct {
		count int
		err   error
	}
	results := make(chan result, 4)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count, err := ReleaseZTAPIExpiredWorkbenchHolds(now, 1)
			results <- result{count, err}
		}()
	}
	wg.Wait()
	close(results)
	released := 0
	for result := range results {
		require.NoError(t, result.err)
		released += result.count
	}
	require.Equal(t, 1, released)
	for _, item := range items {
		id := namespace + "-" + item.name
		var row ZTAPIRequestSettlement
		require.NoError(t, db.Where("operation_id = ?", id).Take(&row).Error)
		require.Equal(t, item.state, row.Status, item.name)
		var refunds int64
		require.NoError(t, db.Model(&BalanceLedger{}).Where("request_id = ? AND source_type = ?", id, BalanceLedgerSourceBillingRefund).Count(&refunds).Error)
		if item.state == ZTAPISettlementReleased {
			require.EqualValues(t, 1, refunds)
		} else {
			require.Zero(t, refunds)
		}
	}
	var user User
	require.NoError(t, db.First(&user, input.UserID).Error)
	require.Equal(t, 700, user.Quota)
}
