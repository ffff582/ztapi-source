package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"runtime"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

type runtimeConfig struct {
	BotToken    string                 `json:"bot_token"`
	OwnerID     int64                  `json:"owner_id"`
	BotID       int64                  `json:"bot_id"`
	BotUsername string                 `json:"bot_username"`
	Provider    support.ProviderConfig `json:"provider"`
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
	return cfg, nil
}
