// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/support"
	"github.com/glebarez/sqlite"
	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func readProviderConfig(path string) (support.ProviderConfig, error) {
	var cfg support.ProviderConfig
	file, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("support provider configuration unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("support provider configuration must be protected")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &cfg) != nil {
		return cfg, errors.New("invalid support provider configuration")
	}
	if _, err = support.NewHTTPProvider(cfg, nil); err != nil {
		return cfg, errors.New("support provider configuration is not authorized")
	}
	return cfg, nil
}
func validOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path == ""
}
func main() {
	if err := run(); err != nil {
		log.Fatal("support startup failed: ", err)
	}
}

func run() error {
	if err := service.ConfigureZTAPISessionSigningKeyFromEnv(); err != nil {
		return errors.New("support session verification unavailable")
	}
	providerConfig, err := readProviderConfig(os.Getenv("SUPPORT_PROVIDER_CONFIG"))
	if err != nil {
		return err
	}
	provider, err := support.NewHTTPProvider(providerConfig, nil)
	if err != nil {
		return errors.New("support provider unavailable")
	}
	path, dsn := os.Getenv("SUPPORT_STORE"), os.Getenv("SUPPORT_READ_DSN")
	publicOrigin, adminOrigin := os.Getenv("SUPPORT_PUBLIC_ORIGIN"), os.Getenv("SUPPORT_ADMIN_ORIGIN")
	if !filepath.IsAbs(path) || dsn == "" || !validOrigin(publicOrigin) || !validOrigin(adminOrigin) {
		return errors.New("missing or invalid support runtime configuration")
	}
	unlock, err := lockStore(path + ".runtime.lock")
	if err != nil {
		return errors.New("another support writer owns the private store")
	}
	defer unlock()
	readConfig, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return errors.New("invalid support read connection")
	}
	readConfig.ParseTime = true
	readConfig.Loc = time.FixedZone("Asia/Shanghai", 8*3600)
	readDB, err := gorm.Open(mysql.Open(readConfig.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return errors.New("support read connection unavailable")
	}
	readSQL, err := readDB.DB()
	if err != nil {
		return err
	}
	defer readSQL.Close()
	readSQL.SetMaxOpenConns(2)
	readSQL.SetMaxIdleConns(1)
	readSQL.SetConnMaxLifetime(5 * time.Minute)
	privateDB, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return errors.New("support private store unavailable")
	}
	privateSQL, err := privateDB.DB()
	if err != nil {
		return err
	}
	defer privateSQL.Close()
	privateSQL.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if privateDB.Exec(q).Error != nil {
			return errors.New("support store durability unavailable")
		}
	}
	store, err := support.NewStore(privateDB)
	if err != nil {
		return errors.New("support store migration failed")
	}
	if err = os.Chmod(path, 0600); err != nil {
		return errors.New("support store permissions failed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err = store.InterruptOrphans(startup, time.Now().UTC()); err != nil {
		return errors.New("support incomplete calls could not be recorded")
	}
	source := &support.Reader{DB: readDB, CatalogURL: os.Getenv("SUPPORT_CATALOG_URL")}
	handler := support.NewHandler(source, store, provider, support.HandlerConfig{Enabled: os.Getenv("SUPPORT_ENABLED") == "true", VisionEnabled: providerConfig.VisionEnabled, Model: providerConfig.Model, ChannelID: providerConfig.ChannelID, Supplier: providerConfig.Supplier, Environment: os.Getenv("SUPPORT_ENVIRONMENT"), Origins: []string{publicOrigin, adminOrigin}, AdmitWrite: storageAdmission(path)})
	address := os.Getenv("SUPPORT_LISTEN")
	if address == "" {
		address = "127.0.0.1:3012"
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("support HTTP listener unavailable")
	}
	log.Print("support service ready")
	return serveSupport(ctx, server, listener)
}

func serveSupport(ctx context.Context, server *http.Server, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	drained := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		drained <- server.Shutdown(shutdown)
	}()
	err := server.Serve(listener)
	cancel()
	shutdownErr := <-drained
	if shutdownErr != nil {
		return errors.New("support shutdown did not drain all requests")
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return errors.New("support HTTP service stopped unexpectedly")
}
