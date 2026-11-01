package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"runtime"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
	"github.com/QuantumNous/new-api/support/telegram"
)

type runtimeConfig struct {
	BotToken    string                 `json:"bot_token"`
	OwnerID     int64                  `json:"owner_id"`
	BotID       int64                  `json:"bot_id"`
	BotUsername string                 `json:"bot_username"`
	Provider    support.ProviderConfig `json:"provider"`
	Bridge      *bridgeConfig          `json:"bridge,omitempty"`
}

type bridgeConfig struct {
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
}

func (bridgeConfig) String() string     { return "TelegramBridgeConfig{credentials:hidden}" }
func (b bridgeConfig) GoString() string { return b.String() }

func newRuntimeBridge(cfg *bridgeConfig) (telegram.Bridge, error) {
	if cfg == nil {
		return nil, nil
	}
	secret, err := hex.DecodeString(cfg.Secret)
	if err != nil || len(cfg.Secret) != 64 || len(secret) != 32 || bytes.Equal(secret, make([]byte, 32)) {
		return nil, errors.New("invalid protected Telegram bridge configuration")
	}
	client, err := telegram.NewBridgeClient(cfg.Endpoint, secret, nil)
	if err != nil {
		return nil, errors.New("invalid protected Telegram bridge configuration")
	}
	return client, nil
}

func readConfig(path string) (runtimeConfig, error) {
	var cfg runtimeConfig
	file, err := os.Open(path)
	if err != nil {
		return cfg, errors.New("Telegram support configuration unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("Telegram support configuration must be protected")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &cfg) != nil ||
		cfg.OwnerID <= 0 || cfg.BotID <= 0 || !regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{10,128}$`).MatchString(cfg.BotToken) || !regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`).MatchString(cfg.BotUsername) {
		return runtimeConfig{}, errors.New("invalid Telegram support configuration")
	}
	if _, err := support.NewHTTPProvider(cfg.Provider, nil); err != nil {
		return runtimeConfig{}, errors.New("unauthorized Telegram support provider")
	}
	if _, err := newRuntimeBridge(cfg.Bridge); err != nil {
		return runtimeConfig{}, err
	}
	return cfg, nil
}
