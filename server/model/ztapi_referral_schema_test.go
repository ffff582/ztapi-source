package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIReferralSQLiteMigrationPreservesUniqueIndexes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "referral-legacy.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE `ztapi_referral_commissions` ("+
		"`id` integer PRIMARY KEY,`order_id` integer NOT NULL,`top_up_id` integer NOT NULL,`balance_ledger_id` integer NOT NULL,"+
		"`user_id` integer NOT NULL,`invitee_id` integer NOT NULL,`binding_id` integer NOT NULL,`principal_micros` bigint NOT NULL,"+
		"`commission_micros` bigint NOT NULL,`refunded_principal_micros` bigint NOT NULL,`reversed_micros` bigint NOT NULL,"+
		"`manual_loss_micros` bigint NOT NULL,`block_timestamp_ms` integer NOT NULL,`earned_at` integer NOT NULL,"+
		"`available_at` integer NOT NULL,`rule_version` text NOT NULL,`evidence_identity` text NOT NULL)").Error)
	legacy := ZTAPIReferralCommission{ID: 1, OrderID: 9, TopUpID: 11, BalanceLedgerID: 101}
	require.NoError(t, db.Omit("CNYOrderID", "CNYMethod").Create(&legacy).Error)
	require.NoError(t, db.Exec("CREATE INDEX local_commission_rule_idx ON ztapi_referral_commissions(rule_version)").Error)
	require.NoError(t, MigrateZTAPIReferral(db))
	require.NoError(t, MigrateZTAPIReferral(db))
	require.True(t, db.Migrator().HasIndex(&ZTAPIReferralCommission{}, "local_commission_rule_idx"))

	for _, column := range []string{"order_id", "top_up_id", "cny_order_id", "balance_ledger_id"} {
		index := "idx_ztapi_referral_commissions_" + column
		var unique int
		require.NoError(t, db.Raw(`SELECT "unique" FROM pragma_index_list('ztapi_referral_commissions') WHERE name = ?`, index).Scan(&unique).Error)
		require.Equal(t, 1, unique, index)
	}
	var stored ZTAPIReferralCommission
	require.NoError(t, db.First(&stored, legacy.ID).Error)
	require.Equal(t, legacy, stored)
	duplicate := ZTAPIReferralCommission{OrderID: 9, TopUpID: 12, BalanceLedgerID: 102}
	require.Error(t, db.Create(&duplicate).Error)
	orderID := int64(9)
	require.NoError(t, db.Create(&ZTAPIReferralCommission{CNYOrderID: &orderID, BalanceLedgerID: 102}).Error)
	require.Error(t, db.Create(&ZTAPIReferralCommission{CNYOrderID: &orderID, BalanceLedgerID: 103}).Error)
}
