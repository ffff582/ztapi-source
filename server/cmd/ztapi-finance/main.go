// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/finance"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"
)

func main() {
	if e := run(); e != nil {
		log.Fatal("finance startup failed: ", e)
	}
}

func financeReadOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusLocked)
			_, _ = w.Write([]byte(`{"success":false,"message":"finance rollback mode is read-only"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func run() error {
	// This entry point intentionally does not call main.InitResources/model.InitDB.
	if e := service.ConfigureZTAPISessionSigningKeyFromEnv(); e != nil {
		return e
	}
	dsn := os.Getenv("FINANCE_READ_DSN")
	origin := os.Getenv("FINANCE_ADMIN_ORIGIN")
	path := os.Getenv("FINANCE_STORE")
	if dsn == "" || origin == "" || path == "" {
		return errors.New("missing finance configuration")
	}
	var dialect gorm.Dialector
	switch os.Getenv("FINANCE_READ_DRIVER") {
	case "postgres":
		dialect = postgres.Open(dsn)
	case "sqlite":
		dialect = sqlite.Open(dsn)
	case "mysql":
		config, err := financeMySQLConfig(dsn)
		if err != nil {
			return errors.New("invalid finance read connection configuration")
		}
		dialect = mysql.New(mysql.Config{DSN: config.FormatDSN(), DSNConfig: config})
	default:
		return errors.New("unsupported finance reader driver")
	}
	read, e := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return errors.New("finance read connection unavailable")
	}
	sqlDB, e := read.DB()
	if e != nil {
		return e
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	private, e := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return errors.New("private finance store unavailable")
	}
	storeDB, e := private.DB()
	if e != nil {
		return e
	}
	defer storeDB.Close()
	storeDB.SetMaxOpenConns(1)
	store, e := finance.NewStore(private)
	if e != nil {
		return e
	}
	address := os.Getenv("FINANCE_LISTEN")
	if address == "" {
		address = "127.0.0.1:3011"
	}
	var handler http.Handler = finance.NewHandlerWithSupportReader(&finance.Reader{DB: read, QuotaPerUnit: 500000, SourceCommit: os.Getenv("FINANCE_SOURCE_COMMIT")}, store, origin, nil, financeSupportReaders(os.Getenv("FINANCE_SUPPORT_STORE"), os.Getenv("FINANCE_TELEGRAM_SUPPORT_STORE")))
	if os.Getenv("FINANCE_REPORT_ONLY") == "true" {
		handler = financeReadOnly(handler)
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("finance reporting listening on ", address)
	e = server.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

func financeMySQLConfig(dsn string) (*gomysql.Config, error) {
	config, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	// The application writes MySQL DATETIME values in Beijing local time.
	config.Loc = time.FixedZone("Asia/Shanghai", 8*60*60)
	config.ParseTime = true
	return config, nil
}

func openFinanceSupportStore(path string) (*gorm.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("support finance store path unavailable")
	}
	// Treat configuration as a literal filename, not a caller-controlled SQLite
	// URI: embedded query parameters cannot change the read-only open mode.
	filename := (&url.URL{Path: filepath.ToSlash(absolute)}).EscapedPath()
	db, err := gorm.Open(sqlite.Open("file:"+filename+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, errors.New("support finance store unavailable")
	}
	return db, nil
}

func financeSupportReader(path string) finance.SupportUsageReader {
	if path == "" {
		return nil
	}
	// Open lazily so a missing/temporarily unavailable optional source cannot
	// prevent finance startup, and subsequent reports can recover without restart.
	return func(ctx context.Context, start, end time.Time) ([]support.FinancialUsage, error) {
		monthStart, monthEnd, err := finance.MonthBounds(start.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01"))
		if err != nil || !end.After(start) || start.Before(monthStart) || end.After(monthEnd) {
			return nil, errors.New("invalid support finance range")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		db, err := openFinanceSupportStore(path)
		if err != nil {
			return nil, err
		}
		raw, err := db.DB()
		if err != nil {
			return nil, err
		}
		defer raw.Close()
		raw.SetMaxOpenConns(1)
		return support.ReadFinancialUsage(ctx, db, start, end)
	}
}

func financeSupportReaders(paths ...string) finance.SupportUsageReader {
	readers := []finance.SupportUsageReader{}
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return func(context.Context, time.Time, time.Time) ([]support.FinancialUsage, error) {
				return nil, errors.New("support cost configuration unavailable")
			}
		}
		if !seen[absolute] {
			readers = append(readers, financeSupportReader(absolute))
			seen[absolute] = true
		}
	}
	if len(readers) == 0 {
		return nil
	}
	return func(ctx context.Context, start, end time.Time) ([]support.FinancialUsage, error) {
		var result []support.FinancialUsage
		for _, read := range readers {
			rows, err := read(ctx, start, end)
			if err != nil {
				return nil, err
			}
			result = append(result, rows...)
		}
		return result, nil
	}
}
