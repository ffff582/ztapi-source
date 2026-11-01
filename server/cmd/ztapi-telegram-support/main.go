package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/QuantumNous/new-api/support"
	"github.com/QuantumNous/new-api/support/telegram"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	if err := run(); err != nil {
		log.Fatal("Telegram support stopped: ", err)
	}
}

func run() error {
	cfg, err := readConfig(os.Getenv("TELEGRAM_SUPPORT_CONFIG"))
	if err != nil {
		return err
	}
	path := os.Getenv("TELEGRAM_SUPPORT_STORE")
	if !filepath.IsAbs(path) {
		return errors.New("Telegram support requires a private absolute store path")
	}
	unlock, err := lockStore(path + ".runtime.lock")
	if err != nil {
		return errors.New("another Telegram receiver owns the private store")
	}
	defer unlock()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return errors.New("Telegram support private store unavailable")
	}
	raw, err := db.DB()
	if err != nil {
		return errors.New("Telegram support private connection unavailable")
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	for _, sql := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000"} {
		if db.Exec(sql).Error != nil {
			return errors.New("Telegram support durability unavailable")
		}
	}
	store, err := support.NewStore(db)
	if err != nil || os.Chmod(path, 0600) != nil {
		return errors.New("Telegram support private schema unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := store.InterruptOrphans(startup, time.Now().UTC()); err != nil {
		return errors.New("Telegram support orphan evidence unavailable")
	}
	provider, err := support.NewHTTPProvider(cfg.Provider, nil)
	if err != nil {
		return errors.New("Telegram support provider unavailable")
	}
	client, err := telegram.NewClient(cfg.BotToken, nil)
	if err != nil {
		return errors.New("Telegram support API configuration unavailable")
	}
	public := &support.Reader{CatalogURL: os.Getenv("TELEGRAM_SUPPORT_CATALOG_URL")}
	bridge, err := newRuntimeBridge(cfg.Bridge)
	if err != nil {
		return err
	}
	var bindingKey []byte
	if cfg.Bridge != nil {
		bindingKey, err = hex.DecodeString(cfg.Bridge.Secret)
		if err != nil {
			return errors.New("invalid protected Telegram binding configuration")
		}
	}
	worker, err := telegram.NewWorker(telegram.Config{BindingKey: bindingKey, Bridge: bridge, OwnerID: cfg.OwnerID, BotID: cfg.BotID, BotUsername: cfg.BotUsername, ChannelID: cfg.Provider.ChannelID, Model: cfg.Provider.Model, Supplier: cfg.Provider.Supplier, Environment: "telegram-production", VisionEnabled: cfg.Provider.VisionEnabled, AdmitWrite: storageAdmission(path)}, db, store, provider, public.PublicKnowledge, client)
	if err != nil {
		return errors.New("Telegram support worker initialization failed")
	}
	enabled := os.Getenv("TELEGRAM_SUPPORT_ENABLED") == "true"
	var receiver workerRunner
	if enabled {
		if err := client.Verify(startup, cfg.OwnerID, cfg.BotID, cfg.BotUsername); err != nil {
			return errors.New("Telegram bot identity, private recipient or receiver configuration mismatch")
		}
		receiver = worker
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if enabled {
			_, _ = w.Write([]byte(`{"ok":true,"enabled":true}`))
		} else {
			_, _ = w.Write([]byte(`{"ok":true,"enabled":false}`))
		}
	})
	address := os.Getenv("TELEGRAM_SUPPORT_LISTEN")
	if address == "" {
		address = "127.0.0.1:3013"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("Telegram support private health listener unavailable")
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	log.Print("Telegram support runtime ready")
	return runServices(ctx, server, listener, receiver)
}
