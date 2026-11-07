package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func referralDeletionSafetyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := referralTestDB(t)
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	return db
}

func referralDeletionSafetyDelete(userID int, hard bool) error {
	if hard {
		return HardDeleteUserById(userID)
	}
	return DeleteUserById(userID)
}

func TestZTAPIReferralDeletionSafetyExpiredPromoterPreservesLateSettlement(t *testing.T) {
	for _, hard := range []bool{false, true} {
		t.Run(fmt.Sprintf("hard=%t", hard), func(t *testing.T) {
			db := referralDeletionSafetyDB(t)
			referralEnable(t)
			referralBind(t, db, 2, 1, referralTestTime)
			var binding ZTAPIReferralBinding
			require.NoError(t, db.Where("user_id = ?", 2).First(&binding).Error)
			now := time.Now().UTC()
			require.Less(t, binding.ExpiresAt, now.Unix())
			settledAt := referralTestTime.Add(2 * time.Hour)
			require.Less(t, settledAt.Unix(), binding.ExpiresAt)
			require.Less(t, settledAt.Add(7*24*time.Hour).Unix(), now.Unix())
			order := referralSettlement(t, db, 400, referralTestTime.Add(time.Hour), settledAt)
			_, err := SetZTAPIReferralCampaign(context.Background(), ZTAPIReferralCampaignUpdate{
				ExpectedVersion: 1, OperatorID: 1, Enabled: false, Now: settledAt.Add(time.Hour),
			})
			require.NoError(t, err)
			// A cursor past this order is not a seal that its historical evidence
			// has been fully discovered or its obligations have been discharged.
			require.NoError(t, db.Model(&ZTAPIReferralCampaign{}).Where("id = ?", 1).Update("reconcile_cursor", order.ID+100).Error)
			var sourceBefore []BalanceLedger
			require.NoError(t, db.Where("request_id = ?", order.TradeNo).Order("id").Find(&sourceBefore).Error)
			var count int64
			require.NoError(t, db.Model(&ZTAPIReferralCommission{}).Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Model(&ZTAPIReferralLedgerEntry{}).Count(&count).Error)
			require.Zero(t, count)

			err = referralDeletionSafetyDelete(1, hard)
			require.ErrorIs(t, err, ErrZTAPIReferralAccountClosureNeedsReview)
			var owner User
			require.NoError(t, db.Unscoped().Select("id", "deleted_at", "status").First(&owner, 1).Error)
			require.False(t, owner.DeletedAt.Valid)
			require.Equal(t, common.UserStatusEnabled, owner.Status)
			var sourceAfter []BalanceLedger
			require.NoError(t, db.Where("request_id = ?", order.TradeNo).Order("id").Find(&sourceAfter).Error)
			require.Equal(t, sourceBefore, sourceAfter)
			var retainedBinding ZTAPIReferralBinding
			require.NoError(t, db.First(&retainedBinding, binding.ID).Error)
			require.Equal(t, binding, retainedBinding)
			var campaign ZTAPIReferralCampaign
			require.NoError(t, db.First(&campaign, 1).Error)
			require.Equal(t, order.ID+100, campaign.ReconcileCursor)
			require.NoError(t, db.Model(&ZTAPIReferralLedgerEntry{}).Count(&count).Error)
			require.Zero(t, count)

			result, err := ReconcileZTAPIReferralSettlements(context.Background(), 100)
			require.NoError(t, err)
			require.Equal(t, 1, result.Created)
			commissions, total, err := ListZTAPIReferralCommissions(context.Background(), 1, 1, 10)
			require.NoError(t, err)
			require.Equal(t, int64(1), total)
			require.Equal(t, int64(20_000_000), commissions[0].CommissionMicros)
			withdrawal, err := RequestZTAPIReferralWithdrawal(context.Background(), ZTAPIReferralWithdrawalRequest{
				UserID: 1, AmountMicros: 20_000_000, Address: USDTTopUpContractAddress,
				IdempotencyKey: "retained-expired-promoter", Now: now,
			})
			require.NoError(t, err)
			require.Equal(t, 1, withdrawal.UserID)
			require.Equal(t, "requested", withdrawal.Status)
		})
	}
}

func TestZTAPIReferralDeletionSafetyHistoricalBindingRequiresReviewOnBothSides(t *testing.T) {
	for _, userID := range []int{1, 2} {
		for _, hard := range []bool{false, true} {
			t.Run(fmt.Sprintf("user=%d/hard=%t", userID, hard), func(t *testing.T) {
				db := referralDeletionSafetyDB(t)
				referralEnable(t)
				referralBind(t, db, 2, 1, referralTestTime)
				var binding ZTAPIReferralBinding
				require.NoError(t, db.First(&binding).Error)
				require.Less(t, binding.ExpiresAt, time.Now().UTC().Unix())
				for _, model := range []any{&ZTAPIReferralCommission{}, &ZTAPIReferralLedgerEntry{}, &ZTAPIReferralWithdrawal{}} {
					var count int64
					require.NoError(t, db.Model(model).Count(&count).Error)
					require.Zero(t, count)
				}
				err := referralDeletionSafetyDelete(userID, hard)
				require.ErrorIs(t, err, ErrZTAPIReferralAccountClosureNeedsReview)
				var user User
				require.NoError(t, db.Unscoped().Select("id", "deleted_at").First(&user, userID).Error)
				require.False(t, user.DeletedAt.Valid)
				var retained ZTAPIReferralBinding
				require.NoError(t, db.First(&retained, binding.ID).Error)
				require.Equal(t, binding, retained)
			})
		}
	}
}

func TestZTAPIReferralDeletionSafetyUnrelatedUserKeepsDeletionSemantics(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, hard := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy=%t/hard=%t", legacy, hard), func(t *testing.T) {
				var db *gorm.DB
				userID := 3
				if legacy {
					db = setupBalanceLedgerTestDB(t)
					user := createBalanceLedgerTestUser(t, db, "plain-deletion", 12345)
					userID = user.Id
					require.False(t, db.Migrator().HasTable(&ZTAPIReferralBinding{}))
				} else {
					db = referralDeletionSafetyDB(t)
					referralEnable(t)
					referralBind(t, db, 2, 1, referralTestTime)
				}
				require.NoError(t, referralDeletionSafetyDelete(userID, hard))
				var scoped User
				require.ErrorIs(t, db.First(&scoped, userID).Error, gorm.ErrRecordNotFound)
				var unscoped User
				err := db.Unscoped().First(&unscoped, userID).Error
				if hard {
					require.ErrorIs(t, err, gorm.ErrRecordNotFound)
				} else {
					require.NoError(t, err)
					require.True(t, unscoped.DeletedAt.Valid)
				}
				if !legacy {
					var binding ZTAPIReferralBinding
					require.NoError(t, db.Where("user_id = ? AND inviter_id = ?", 2, 1).First(&binding).Error)
				}
			})
		}
	}
}

func TestZTAPIReferralDeletionSafetyPreservesAccountLockOrder(t *testing.T) {
	db := referralDeletionSafetyDB(t)
	referralEnable(t)
	referralBind(t, db, 2, 1, referralTestTime)
	locked := false
	violations := []string{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("referral_deletion_safety:before_query", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "ztapi_referral_campaigns":
			violations = append(violations, "deletion acquired campaign after account")
		case "ztapi_referral_bindings", "ztapi_referral_ledger_entries", "ztapi_referral_withdrawals":
			if !locked {
				violations = append(violations, "obligation check before account lock")
			}
		}
	}))
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("referral_deletion_safety:after_query", func(tx *gorm.DB) {
		if tx.Statement.Table == "ztapi_referral_account_locks" && tx.Error == nil {
			locked = true
		}
	}))
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove("referral_deletion_safety:before_query")
		_ = db.Callback().Query().Remove("referral_deletion_safety:after_query")
	})
	require.ErrorIs(t, HardDeleteUserById(1), ErrZTAPIReferralAccountClosureNeedsReview)
	require.True(t, locked)
	require.Empty(t, violations)
	locked = false
	require.NoError(t, HardDeleteUserById(3))
	require.True(t, locked)
	require.Empty(t, violations)
}
