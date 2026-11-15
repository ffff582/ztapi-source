package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPIWorkbenchSchemaInGatewayMigrations(t *testing.T) {
	for _, migration := range []struct {
		name string
		run  func() error
	}{
		{"normal", migrateDB},
		{"fast", migrateDBFast},
	} {
		t.Run(migration.name, func(t *testing.T) {
			db, input := setupZTAPISettlement(t)
			db.Logger = logger.Default.LogMode(logger.Silent)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			require.False(t, db.Migrator().HasTable(&ZTAPIWorkbenchTask{}))
			require.NoError(t, migration.run())
			require.True(t, db.Migrator().HasTable(&ZTAPIWorkbenchTask{}))
			var user User
			require.NoError(t, db.First(&user, input.UserID).Error)
			require.Equal(t, 1000, user.Quota)
			config := ZTAPIModelConfig{SourceModel: "fixture-migration", InputCostPerMillion: 0.12345678}
			require.NoError(t, db.Create(&config).Error)
			snapshot := ZTAPIModelPublicationSnapshot{ModelConfigID: config.ID, ModelVersion: 1, InputPricePerMillion: 0.98765432}
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&snapshot).Error)
			require.NoError(t, db.Exec(`CREATE TRIGGER fixture_publication_untouched AFTER UPDATE ON ztapi_model_publication_snapshots BEGIN SELECT 1; END`).Error)

			rows, err := BeginZTAPIWorkbenchBatch(workbenchTestTask, 700, []ZTAPIWorkbenchLeg{workbenchTestLeg(input, "-migration", "main", 200)})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.NoError(t, migration.run())
			var task ZTAPIWorkbenchTask
			require.NoError(t, db.Take(&task, "id = ?", workbenchTestTask).Error)
			require.Equal(t, ZTAPIWorkbenchTask{ID: workbenchTestTask, UserID: input.UserID, Model: input.PublicModel, LimitQuota: 700}, task)
			require.NoError(t, db.First(&user, input.UserID).Error)
			require.Equal(t, 800, user.Quota)
			var migratedConfig ZTAPIModelConfig
			var migratedSnapshot ZTAPIModelPublicationSnapshot
			require.NoError(t, db.First(&migratedConfig, config.ID).Error)
			require.NoError(t, db.First(&migratedSnapshot, snapshot.ID).Error)
			require.Equal(t, config.InputCostPerMillion, migratedConfig.InputCostPerMillion)
			require.Equal(t, snapshot.InputPricePerMillion, migratedSnapshot.InputPricePerMillion)
			var column struct{ Type, DefaultValue string }
			require.NoError(t, db.Raw(`SELECT type, dflt_value AS default_value FROM pragma_table_info('ztapi_model_publication_snapshots') WHERE name = 'input_price_per_million'`).Scan(&column).Error)
			require.Equal(t, "decimal(20,8)", column.Type)
			require.Equal(t, "0.000000", column.DefaultValue)
			var triggers int
			require.NoError(t, db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'fixture_publication_untouched'`).Scan(&triggers).Error)
			require.Equal(t, 1, triggers)
		})
	}
}
