package spending

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestDateTimeWindowSQLKeepsNativeRangesOutsideSQLite(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := fixture(t)
			raw, err := db.DB()
			require.NoError(t, err)
			// Reuse an isolated SQLite handle for SQL generation only. DryRun and disabled
			// automatic ping prevent either other driver from opening a network connection.
			switch dialect {
			case "mysql":
				db, err = gorm.Open(mysql.New(mysql.Config{Conn: raw, SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			case "postgres":
				db, err = gorm.Open(postgres.New(postgres.Config{Conn: raw}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			default:
				db = db.Session(&gorm.Session{DryRun: true})
			}
			require.NoError(t, err)
			start := time.Date(2026, 11, 2, 0, 0, 0, 0, Beijing)
			end := start.Add(time.Hour)
			var rows []ledger
			query := dateTimeWindow(db.Table("balance_ledgers l").Select("l.id"), "l.created_at", start, end).Find(&rows)
			require.NoError(t, query.Error)
			sql := query.Statement.SQL.String()
			require.Len(t, query.Statement.Vars, 2)
			if dialect == "sqlite" {
				require.Equal(t, 4, strings.Count(sql, "julianday("))
				require.Equal(t, start.UTC(), query.Statement.Vars[0])
				require.Equal(t, end.UTC(), query.Statement.Vars[1])
			} else {
				require.NotContains(t, sql, "julianday(")
				require.Contains(t, sql, "l.created_at >= ")
				require.Contains(t, sql, "l.created_at < ")
				require.Equal(t, start, query.Statement.Vars[0])
				require.Equal(t, end, query.Statement.Vars[1])
			}
		})
	}
}
