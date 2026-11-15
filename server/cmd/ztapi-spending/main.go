// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/spending"
	"github.com/glebarez/sqlite"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type config struct{ dsn, driver, store, origin, listen, unit, telegram, binding string }

func readConfig() (config, error) {
	c := config{dsn: os.Getenv("SPENDING_READ_DSN"), driver: os.Getenv("SPENDING_READ_DRIVER"), store: os.Getenv("SPENDING_STORE"), origin: os.Getenv("SPENDING_PUBLIC_ORIGIN"), listen: os.Getenv("SPENDING_LISTEN"), unit: os.Getenv("SPENDING_QUOTA_PER_UNIT"), telegram: os.Getenv("SPENDING_TELEGRAM_CONFIG"), binding: os.Getenv("SPENDING_BINDING_STORE")}
	if c.driver == "" {
		c.driver = "mysql"
	}
	if c.listen == "" {
		c.listen = "127.0.0.1:3014"
	}
	if c.unit == "" {
		c.unit = "500000"
	}
	// The owner verified the serving deployment's fixed quota unit. Snapshots
	// carrying a unit are also checked on every read, never repriced.
	q, err := decimal.NewFromString(c.unit)
	if err != nil || c.unit != "500000" || !q.Equal(decimal.NewFromFloat(common.QuotaPerUnit)) || c.dsn == "" || !filepath.IsAbs(c.store) || !spending.ValidOrigin(c.origin) {
		return c, errors.New("invalid spending configuration")
	}
	if c.binding != "" && !filepath.IsAbs(c.binding) || c.telegram != "" && !filepath.IsAbs(c.telegram) {
		return c, errors.New("invalid spending optional configuration")
	}
	for _, other := range []string{c.binding, c.telegram} {
		if other != "" && samePath(c.store, other) {
			return c, errors.New("spending private store must be isolated")
		}
	}
	if c.driver == "sqlite" && samePath(c.store, c.dsn) {
		return c, errors.New("spending source must be separate")
	}
	return c, nil
}
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	x, e := os.Stat(a)
	y, f := os.Stat(b)
	return e == nil && f == nil && os.SameFile(x, y)
}
func mysqlReadConfig(raw string) (*gomysql.Config, error) {
	c, e := gomysql.ParseDSN(raw)
	if e != nil {
		return nil, errors.New("invalid spending read DSN")
	}
	c.ParseTime = true
	c.Loc = spending.Beijing
	c.Timeout = 3 * time.Second
	c.ReadTimeout = 3 * time.Second
	c.WriteTimeout = 3 * time.Second
	return c, nil
}
func openSource(c config) (*gorm.DB, error) {
	var d gorm.Dialector
	switch c.driver {
	case "mysql":
		cfg, e := mysqlReadConfig(c.dsn)
		if e != nil {
			return nil, e
		}
		d = mysql.New(mysql.Config{DSN: cfg.FormatDSN(), SkipInitializeWithVersion: true})
	case "postgres":
		d = postgres.Open(c.dsn)
	case "sqlite":
		b, e := spending.OpenBindingStore(c.dsn)
		if e != nil {
			return nil, e
		}
		return b.DB, nil
	default:
		return nil, errors.New("unsupported spending source driver")
	}
	db, e := gorm.Open(d, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		return nil, errors.New("spending source unavailable")
	}
	raw, e := db.DB()
	if e != nil {
		return nil, e
	}
	raw.SetMaxOpenConns(2)
	raw.SetMaxIdleConns(1)
	raw.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal("spending startup failed: ", err)
	}
}

func openPrivateStore(path string) (*spending.Store, func(), error) {
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("invalid spending private store")
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, nil, errors.New("invalid spending private store")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, errors.New("spending private directory unavailable")
	}
	unlock, err := lockStore(path + ".runtime.lock")
	if err != nil {
		return nil, nil, errors.New("another spending writer owns the private store")
	}
	filename := (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	db, err := gorm.Open(sqlite.Open("file:"+filename+"?_pragma=synchronous(FULL)"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		unlock()
		return nil, nil, errors.New("spending private store unavailable")
	}
	raw, err := db.DB()
	if err != nil {
		unlock()
		return nil, nil, err
	}
	var once sync.Once
	closeStore := func() { once.Do(func() { _ = raw.Close(); unlock() }) }
	store, err := spending.NewStore(db)
	if err != nil {
		closeStore()
		return nil, nil, errors.New("spending private store initialization failed")
	}
	if err = os.Chmod(path, 0600); err != nil {
		closeStore()
		return nil, nil, errors.New("spending store permissions unavailable")
	}
	return store, closeStore, nil
}
func run() error {
	cfg, err := readConfig()
	if err != nil {
		return err
	}
	if err = service.ConfigureZTAPISessionSigningKeyFromEnv(); err != nil {
		return errors.New("spending session verification unavailable")
	}
	read, err := openSource(cfg)
	if err != nil {
		return err
	}
	raw, err := read.DB()
	if err != nil {
		return err
	}
	defer raw.Close()
	reader := &spending.Reader{DB: read, VerifiedQuotaPerUnit: cfg.unit}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = reader.Preflight(ctx); err != nil {
		return errors.New("spending source preflight failed")
	}
	store, closeStore, err := openPrivateStore(cfg.store)
	if err != nil {
		return err
	}
	defer closeStore()
	var bindings *spending.BindingReader
	var bindingAPI spending.Bindings
	var sender *spending.TelegramSender
	var senderAPI spending.Sender
	if cfg.binding != "" {
		bindings, err = spending.OpenBindingStore(cfg.binding)
		if err != nil {
			log.Print("spending Telegram bindings unavailable")
		} else {
			defer bindings.Close()
			bindingAPI = bindings
		}
	}
	if cfg.telegram != "" {
		sender, err = spending.ReadTelegramSender(cfg.telegram)
		if err != nil {
			log.Print("spending Telegram sender unavailable")
		} else {
			senderAPI = sender
		}
	}
	engine := &spending.Engine{Store: store, Source: reader, Bindings: bindingAPI, Sender: senderAPI}
	handler := spending.NewHandler(reader, store, spending.HandlerConfig{PublicOrigin: cfg.origin, Bindings: bindingAPI, TelegramAvailable: bindingAPI != nil && senderAPI != nil})
	listener, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return errors.New("spending listener unavailable")
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 6 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			if err := engine.RunOnce(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Print("spending evaluation deferred")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	stop()
	<-workerDone
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("spending HTTP service stopped")
}
