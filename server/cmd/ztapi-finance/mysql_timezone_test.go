package main

import (
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestFinanceMySQLUsesWriterTimeZoneAcrossMonthBoundary(t *testing.T) {
	config, err := financeMySQLConfig("reader:secret@tcp(mysql:3306)/ztapi?parseTime=true&loc=UTC")
	require.NoError(t, err)
	instant := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	require.Equal(t, "2026-11-01 00:30", instant.In(config.Loc).Format("2006-01-02 15:04"))
	require.True(t, config.ParseTime)
	parsed, err := gomysql.ParseDSN(config.FormatDSN())
	require.NoError(t, err)
	require.Equal(t, "2026-11-01 00:30", instant.In(parsed.Loc).Format("2006-01-02 15:04"))
}

func TestFinanceMySQLRejectsInvalidDSN(t *testing.T) {
	_, err := financeMySQLConfig("bad dsn")
	require.Error(t, err)
}
