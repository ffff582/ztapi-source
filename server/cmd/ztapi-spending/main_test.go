package main

import (
	"context"
	"github.com/QuantumNous/new-api/spending"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeConfigurationAndMySQLTimezoneBounds(t *testing.T) {
	t.Setenv("SPENDING_READ_DSN", "reader:fixture@tcp(localhost:3306)/db")
	t.Setenv("SPENDING_STORE", t.TempDir()+"/spending.db")
	t.Setenv("SPENDING_PUBLIC_ORIGIN", "https://ztapi.vip")
	cfg, err := readConfig()
	require.NoError(t, err)
	require.Equal(t, "500000", cfg.unit)
	require.Equal(t, "127.0.0.1:3014", cfg.listen)
	mysql, err := mysqlReadConfig(cfg.dsn)
	require.NoError(t, err)
	require.True(t, mysql.ParseTime)
	require.Equal(t, 3*time.Second, mysql.ReadTimeout)
	_, offset := time.Now().In(mysql.Loc).Zone()
	require.Equal(t, 8*3600, offset)
	t.Setenv("SPENDING_QUOTA_PER_UNIT", "1000000")
	_, err = readConfig()
	require.Error(t, err)
	t.Setenv("SPENDING_QUOTA_PER_UNIT", "500000")
	t.Setenv("SPENDING_PUBLIC_ORIGIN", "https://ztapi.vip/evil")
	_, err = readConfig()
	require.Error(t, err)
}

func TestRuntimeLockPreventsRecoveryOfOtherWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spending.db")
	store, closeStore, err := openPrivateStore(path)
	require.NoError(t, err)
	defer closeStore()
	now := time.Now()
	ctx := context.Background()
	_, err = store.SavePreferences(ctx, 1, spending.Preferences{Enabled: true, DailySpendU: "1"}, now)
	require.NoError(t, err)
	amount := "2"
	require.NoError(t, store.Evaluate(ctx, 1, spending.Snapshot{Overview: spending.Overview{Complete: true, ConfirmedDailyU: &amount}}, now))
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	raw, err := db.DB()
	require.NoError(t, err)
	defer raw.Close()
	require.NoError(t, db.Exec(`UPDATE spending_alerts SET delivery_status='sending'`).Error)
	_, closeOther, err := openPrivateStore(path)
	if closeOther != nil {
		defer closeOther()
	}
	require.Error(t, err)
	var status string
	require.NoError(t, db.Raw(`SELECT delivery_status FROM spending_alerts`).Scan(&status).Error)
	require.Equal(t, "sending", status)
	closeStore()
	_, closeNext, err := openPrivateStore(path)
	require.NoError(t, err)
	defer closeNext()
	require.NoError(t, db.Raw(`SELECT delivery_status FROM spending_alerts`).Scan(&status).Error)
	require.Equal(t, "unknown", status)
}
