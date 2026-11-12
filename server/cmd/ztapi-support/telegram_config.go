package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/support"
)

func readTelegramBridgeConfig(path string) (support.TelegramBridgeConfig, error) {
	var out support.TelegramBridgeConfig
	if !filepath.IsAbs(path) {
		return out, errors.New("bridge config must use a protected absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return out, errors.New("bridge config must be a protected regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return out, errors.New("bridge config unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	var in struct {
		Secret      string `json:"secret"`
		BotUsername string `json:"bot_username"`
	}
	if err != nil || len(raw) > 4096 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil || common.DecodeJsonStrict(bytes.NewReader(raw), &in) != nil {
		return out, errors.New("invalid bridge config")
	}
	secret, err := hex.DecodeString(in.Secret)
	if err != nil || len(secret) != 32 || in.BotUsername != "ztapi_bot" {
		return out, errors.New("invalid bridge identity")
	}
	out.Secret, out.BotUsername = secret, in.BotUsername
	return out, nil
}
