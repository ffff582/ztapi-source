// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"github.com/QuantumNous/new-api/finance"
	"github.com/QuantumNous/new-api/service"
	"github.com/glebarez/sqlite"
	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"net/http"
	"os"
	"os/signal"
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
	var handler http.Handler = finance.NewHandler(&finance.Reader{DB: read, QuotaPerUnit: 500000, SourceCommit: os.Getenv("FINANCE_SOURCE_COMMIT")}, store, origin, nil)
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
