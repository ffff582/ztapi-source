package finance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
)

// Only table discovery and the database name are adapted for the local SQLite
// fixture. HasColumn is GORM's actual generic implementation, not a mock.
type referralGenericDialector struct{ gorm.Dialector }

func (d referralGenericDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return referralGenericMigrator{
		Migrator: migrator.Migrator{Config: migrator.Config{DB: db, Dialector: d}},
		tables:   d.Dialector.Migrator(db),
	}
}

type referralGenericMigrator struct {
	migrator.Migrator
	tables gorm.Migrator
}

func (m referralGenericMigrator) HasTable(value any) bool { return m.tables.HasTable(value) }
func (m referralGenericMigrator) CurrentDatabase() string { return "finance_generic_stub" }

func referralGenericSchemaDB(t *testing.T, db *gorm.DB, omitColumn string) *gorm.DB {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	require.NoError(t, db.Exec("ATTACH DATABASE ':memory:' AS INFORMATION_SCHEMA").Error)
	require.NoError(t, db.Exec("CREATE TABLE INFORMATION_SCHEMA.columns(table_schema TEXT,table_name TEXT,column_name TEXT)").Error)
	for table, columns := range map[string]string{
		"ztapi_referral_ledger_entries": referralLedgerColumns,
		"ztapi_referral_withdrawals":    referralWithdrawalColumns,
	} {
		for _, column := range strings.Split(columns, ",") {
			if column == omitColumn {
				continue
			}
			require.NoError(t, db.Exec("INSERT INTO INFORMATION_SCHEMA.columns VALUES(?,?,?)", "finance_generic_stub", table, column).Error)
		}
	}
	db.Config.Dialector = referralGenericDialector{Dialector: db.Dialector}
	require.NoError(t, db.Exec("PRAGMA query_only = ON").Error)
	return db
}

func TestReferralGenericMigratorLoadDoesNotPanic(t *testing.T) {
	db := referralGenericSchemaDB(t, referralCapabilityDB(t), "")
	reader := &Reader{DB: db}
	require.True(t, reader.SupportsReferralV1(context.Background()), "reproduce the candidate's healthy capability before loading the report")
	var snapshot *ReferralSnapshot
	require.NotPanics(t, func() { snapshot = reader.loadReferrals(context.Background(), moment("2026-11-08T00:00:00Z")) })
	require.True(t, snapshot.Supported)
	require.Equal(t, "available", snapshot.Status)
	require.Empty(t, snapshot.Reason)
}

func TestReferralGenericMigratorRetainsFallbackStates(t *testing.T) {
	for _, mode := range []string{"missing", "outdated", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			db := referralCapabilityDB(t)
			if mode == "missing" {
				db = newStore(t).db
			}
			omit := ""
			if mode == "outdated" {
				omit = "net_amount_micros"
			}
			db = referralGenericSchemaDB(t, db, omit)
			if mode == "unavailable" {
				require.NoError(t, db.Callback().Query().Before("gorm:query").Register("generic_referral_read_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "ztapi_referral_withdrawals" {
						tx.AddError(errors.New("simulated read denial"))
					}
				}))
			}
			var snapshot *ReferralSnapshot
			require.NotPanics(t, func() {
				snapshot = (&Reader{DB: db}).loadReferrals(context.Background(), moment("2026-11-08T00:00:00Z"))
			})
			if mode == "unavailable" {
				require.True(t, snapshot.Supported)
				require.Equal(t, "unavailable", snapshot.Status)
				require.Equal(t, "referral_read_failed", snapshot.Reason)
			} else {
				require.False(t, snapshot.Supported)
				require.Equal(t, "unsupported", snapshot.Status)
				reason := "referral_schema_missing"
				if mode == "outdated" {
					reason = "referral_schema_outdated"
				}
				require.Equal(t, reason, snapshot.Reason)
			}
		})
	}
}
