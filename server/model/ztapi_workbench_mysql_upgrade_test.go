package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func verifyWorkbenchMySQLUpgradeFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	var tableCount int
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tableCount).Error)
	require.Zero(t, tableCount, "upgrade fixture requires a brand-new database")

	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &BalanceLedger{}, &ZTAPIRequestSettlement{}, &CNYTopUpOrder{}))
	user := createBalanceLedgerTestUser(t, db, "legacy-upgrade", 1000)
	key := Token{UserId: user.Id, KeyHash: common.HashZTAPIKey("synthetic-legacy-key"),
		Status: common.TokenStatusEnabled, RemainQuota: 1000, ExpiredTime: -1, ModelLimits: "zt-test"}
	require.NoError(t, db.Create(&key).Error)
	order := CNYTopUpOrder{OrderNo: "legacy-order", UserID: user.Id, RequestKey: "legacy-request",
		MerchantNo: "synthetic-merchant", AppID: "synthetic-app", Method: "alipay", PayFen: 6800,
		Quota: 1000, CNYPerUnit: "6.80", State: "paid", CreatedAt: common.GetTimestamp(), PaidAt: common.GetTimestamp()}
	require.NoError(t, db.Create(&order).Error)
	input := ZTAPIRequestSettlement{OperationID: "legacy-hold", RequestID: "legacy-request", UserID: user.Id,
		TokenID: key.Id, PublicModel: "zt-test", ReservedQuota: 200,
		PriceSnapshotJSON: `{"synthetic":"` + strings.Repeat("x", 70*1024) + `"}`}
	before, err := BeginZTAPIRequestSettlement(input)
	require.NoError(t, err)

	// Remove only the newly added fields from this synthetic pre-Workbench schema.
	migrator := db.Migrator()
	require.NoError(t, migrator.DropColumn(&Token{}, "WorkbenchOnly"))
	for _, column := range []string{"WorkbenchTaskID", "WorkbenchBatchID", "WorkbenchStage", "WorkbenchBodySHA256", "WorkbenchOutboundMax", "WorkbenchOutputMax"} {
		require.NoError(t, migrator.DropColumn(&ZTAPIRequestSettlement{}, column))
	}
	require.False(t, migrator.HasTable(&ZTAPIWorkbenchTask{}))
	require.False(t, migrator.HasColumn(&Token{}, "WorkbenchOnly"))

	for range 2 {
		require.NoError(t, migrateDB())
		require.True(t, migrator.HasTable(&ZTAPIWorkbenchTask{}))
		require.True(t, migrator.HasTable(&ZTAPIWorkbenchPilotSpend{}))
		assertZTAPISettlementBalances(t, db, input, 800, 800)
		legacyKey, err := ValidateUserToken("synthetic-legacy-key")
		require.NoError(t, err)
		require.Equal(t, key.Id, legacyKey.Id)
		require.False(t, legacyKey.WorkbenchOnly)
		var stored CNYTopUpOrder
		require.NoError(t, db.First(&stored, order.ID).Error)
		require.Equal(t, order, stored)
		replay, err := BeginZTAPIRequestSettlement(input)
		require.NoError(t, err)
		require.Equal(t, before.ID, replay.ID)
		require.Equal(t, input.PriceSnapshotJSON, replay.PriceSnapshotJSON)
		require.Empty(t, replay.WorkbenchTaskID)
		var rows int64
		require.NoError(t, db.Model(&BalanceLedger{}).Where("user_id = ?", user.Id).Count(&rows).Error)
		require.EqualValues(t, 1, rows)
		assertZTAPISettlementBalances(t, db, input, 800, 800)
	}
}
