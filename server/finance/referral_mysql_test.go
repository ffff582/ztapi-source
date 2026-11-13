package finance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func referralMySQLConfig(raw, mandatory string) (*mysqldriver.Config, error) {
	required := false
	switch strings.ToLower(strings.TrimSpace(mandatory)) {
	case "", "0", "false":
	case "1", "true":
		required = true
	default:
		return nil, errors.New("invalid ZTAPI_REQUIRE_COMMERCIAL_MYSQL switch")
	}
	if strings.TrimSpace(raw) == "" {
		if required {
			return nil, errors.New("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN is required")
		}
		return nil, nil
	}
	cfg, err := mysqldriver.ParseDSN(raw)
	if err != nil {
		return nil, errors.New("invalid commercial MySQL test DSN")
	}
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Net != "tcp" || cfg.DBName != "ztapi_commercial_test" {
		return nil, errors.New("finance MySQL regression requires loopback TCP and database ztapi_commercial_test")
	}
	if host == "localhost" {
		host = "127.0.0.1"
	} // Do not trust hostname resolution for the safety boundary.
	ip := net.ParseIP(host)
	numericPort, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || numericPort < 1 || numericPort > 65535 {
		return nil, errors.New("finance MySQL regression requires a literal loopback address and valid TCP port")
	}
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.ParseTime, cfg.MultiStatements, cfg.AllowAllFiles = true, false, false
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 10*time.Second, 10*time.Second
	return cfg, nil
}

func TestReferralMySQLConfigurationGuard(t *testing.T) {
	for _, tc := range []struct {
		name, dsn, mandatory string
		bad, skip            bool
	}{
		{"optional_missing", "", "", false, true},
		{"optional_false", "", "false", false, true},
		{"required_true_missing", "", "true", true, false},
		{"required_one_missing", "", "1", true, false},
		{"invalid_switch", "", "yes", true, false},
		{"loopback_v4", "test:pass@tcp(127.0.0.1:3306)/ztapi_commercial_test", "1", false, false},
		{"loopback_v6", "test:pass@tcp([::1]:13306)/ztapi_commercial_test", "TRUE", false, false},
		{"localhost_pinned", "test:pass@tcp(localhost:3306)/ztapi_commercial_test", "true", false, false},
		{"production_db", "test:pass@tcp(127.0.0.1:3306)/ztapi", "1", true, false},
		{"other_test_db", "test:pass@tcp(127.0.0.1:3306)/ztapi_test", "1", true, false},
		{"private_remote", "test:pass@tcp(172.17.0.2:3306)/ztapi_commercial_test", "1", true, false},
		{"public_remote", "test:pass@tcp(192.0.2.1:3306)/ztapi_commercial_test", "true", true, false},
		{"remote_hostname", "test:pass@tcp(mysql.example.test:3306)/ztapi_commercial_test", "1", true, false},
		{"loopback_lookalike", "test:pass@tcp(127.0.0.1.example.test:3306)/ztapi_commercial_test", "1", true, false},
		{"socket", "test:pass@unix(/tmp/mysql.sock)/ztapi_commercial_test", "1", true, false},
		{"bad_port", "test:pass@tcp(127.0.0.1:0)/ztapi_commercial_test", "1", true, false},
		{"bad_dsn", "not-a-dsn", "1", true, false},
		{"unsafe_options_clamped", "test:pass@tcp(127.0.0.1:3306)/ztapi_commercial_test?multiStatements=true&allowAllFiles=true", "1", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := referralMySQLConfig(tc.dsn, tc.mandatory)
			if tc.bad {
				require.Error(t, err)
				require.Nil(t, cfg)
				return
			}
			require.NoError(t, err)
			if tc.skip {
				require.Nil(t, cfg)
				return
			}
			require.NotNil(t, cfg)
			require.Equal(t, "tcp", cfg.Net)
			require.Equal(t, "ztapi_commercial_test", cfg.DBName)
			require.True(t, cfg.ParseTime)
			require.False(t, cfg.MultiStatements)
			require.False(t, cfg.AllowAllFiles)
			if tc.name == "localhost_pinned" {
				require.Equal(t, "127.0.0.1:3306", cfg.Addr)
			}
		})
	}
}

func TestReferralMySQLRequiredConfigurationFailsClosed(t *testing.T) {
	for _, flag := range []string{"true", "1"} {
		t.Run(flag, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReferralMySQLReadOnlyProjectionIntegration$", "-test.count=1", "-test.timeout=10s")
			for _, value := range os.Environ() {
				key, _, _ := strings.Cut(value, "=")
				if !strings.EqualFold(key, "ZTAPI_COMMERCIAL_MYSQL_TEST_DSN") && !strings.EqualFold(key, "ZTAPI_REQUIRE_COMMERCIAL_MYSQL") {
					cmd.Env = append(cmd.Env, value)
				}
			}
			cmd.Env = append(cmd.Env, "ZTAPI_COMMERCIAL_MYSQL_TEST_DSN=", "ZTAPI_REQUIRE_COMMERCIAL_MYSQL="+flag)
			output, err := cmd.CombinedOutput()
			require.NoError(t, ctx.Err())
			require.Error(t, err, "mandatory MySQL regression must fail, not skip")
			require.Contains(t, string(output), "ZTAPI_COMMERCIAL_MYSQL_TEST_DSN is required")
		})
	}
}

func referralMySQLGORM(t *testing.T, sqlDB *sql.DB) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

func requireReferralMySQLDenied(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var denied *mysqldriver.MySQLError
	require.ErrorAs(t, err, &denied, "assert database privilege denial, not an application hook or missing table")
	require.Contains(t, []uint16{1142, 1143}, denied.Number)
}

func TestReferralMySQLReadOnlyProjectionIntegration(t *testing.T) {
	cfg, err := referralMySQLConfig(os.Getenv("ZTAPI_COMMERCIAL_MYSQL_TEST_DSN"), os.Getenv("ZTAPI_REQUIRE_COMMERCIAL_MYSQL"))
	require.NoError(t, err)
	if cfg == nil {
		t.Skip("dedicated loopback ztapi_commercial_test DSN absent; real finance MySQL regression NOT executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	adminSQL, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, adminSQL.Close()) })
	adminSQL.SetMaxOpenConns(2)
	if err := adminSQL.PingContext(ctx); err != nil {
		t.Fatal("cannot connect to the dedicated loopback commercial MySQL test database")
	}
	var database, clientHost string
	require.NoError(t, adminSQL.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database))
	require.Equal(t, "ztapi_commercial_test", database, "revalidate the selected database before any schema/account operation")
	admin := referralMySQLGORM(t, adminSQL).WithContext(ctx)
	if !admin.Migrator().HasTable(&model.ZTAPIReferralLedgerEntry{}) || !admin.Migrator().HasTable(&model.ZTAPIReferralWithdrawal{}) {
		// This migration creates only referral schema in the validated CI database.
		// No Token, Channel, customer account, or business fixture is created.
		require.NoError(t, model.MigrateZTAPIReferral(admin))
	}
	require.NoError(t, adminSQL.QueryRowContext(ctx, "SELECT SUBSTRING_INDEX(USER(), '@', -1)").Scan(&clientHost))
	require.True(t, clientHost == "localhost" || net.ParseIP(clientHost) != nil, "unexpected MySQL client identity; do not grant wildcard hosts")
	var entropy [32]byte
	_, err = rand.Read(entropy[:])
	require.NoError(t, err)
	username := "ztfi_" + hex.EncodeToString(entropy[:8])
	password := "Rfi1!" + hex.EncodeToString(entropy[8:])
	// Only generated/validated ASCII is interpolated into account DDL. Never log
	// these statements or use DSN-supplied text as an account identifier/password.
	account := fmt.Sprintf("'%s'@'%s'", username, clientHost)
	var existing int
	require.NoError(t, adminSQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE User=? AND Host=?", username, clientHost).Scan(&existing))
	require.Zero(t, existing)
	attempted := false
	t.Cleanup(func() {
		if !attempted {
			return
		}
		cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := adminSQL.ExecContext(cleanupCtx, "DROP USER IF EXISTS "+account); err != nil {
			t.Errorf("cleanup of temporary MySQL account %s failed: %v", username, err)
			return
		}
		var remaining int
		require.NoError(t, adminSQL.QueryRowContext(cleanupCtx, "SELECT COUNT(*) FROM mysql.user WHERE User=? AND Host=?", username, clientHost).Scan(&remaining))
		require.Zero(t, remaining, "the exact temporary account must be removed")
	})
	attempted = true
	_, err = adminSQL.ExecContext(ctx, fmt.Sprintf("CREATE USER %s IDENTIFIED BY '%s'", account, password))
	require.NoError(t, err)
	for table, columns := range map[string]string{
		"ztapi_referral_ledger_entries": referralLedgerColumns,
		"ztapi_referral_withdrawals":    referralWithdrawalColumns,
	} {
		grant := fmt.Sprintf("GRANT SELECT (`%s`) ON `ztapi_commercial_test`.`%s` TO %s", strings.ReplaceAll(columns, ",", "`,`"), table, account)
		_, err := adminSQL.ExecContext(ctx, grant)
		require.NoError(t, err)
	}
	var columnGrants int
	require.NoError(t, adminSQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.COLUMN_PRIVILEGES WHERE GRANTEE=? AND TABLE_SCHEMA=? AND PRIVILEGE_TYPE='SELECT'", account, "ztapi_commercial_test").Scan(&columnGrants))
	require.Equal(t, 13, columnGrants)
	readerConfig := cfg.Clone()
	readerConfig.User, readerConfig.Passwd = username, password
	readonlySQL, err := sql.Open("mysql", readerConfig.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, readonlySQL.Close()) })
	require.NoError(t, readonlySQL.PingContext(ctx))
	reader := &Reader{DB: referralMySQLGORM(t, readonlySQL)}
	require.True(t, reader.SupportsReferralV1(ctx))
	var snapshot *ReferralSnapshot
	require.NotPanics(t, func() { snapshot = reader.loadReferrals(ctx, time.Now().UTC()) })
	require.True(t, snapshot.Supported)
	require.Equal(t, "available", snapshot.Status)
	require.Empty(t, snapshot.Reason)
	for _, column := range []string{"address", "tx_id"} {
		rows, err := readonlySQL.QueryContext(ctx, "SELECT "+column+" FROM ztapi_referral_withdrawals LIMIT 1")
		if rows != nil {
			require.NoError(t, rows.Close())
		}
		requireReferralMySQLDenied(t, err)
	}
	// WHERE 1=0 guarantees no business row can change even if grants regress.
	// Use database/sql directly so model immutability hooks cannot fake denial.
	for _, statement := range []string{
		"UPDATE ztapi_referral_ledger_entries SET amount_micros=amount_micros WHERE 1=0",
		"UPDATE ztapi_referral_withdrawals SET status=status WHERE 1=0",
	} {
		_, err := readonlySQL.ExecContext(ctx, statement)
		requireReferralMySQLDenied(t, err)
	}
}
