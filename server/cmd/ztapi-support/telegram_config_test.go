package main

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelegramBridgeConfigIsProtectedStrictAndSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.json")
	raw := `{"secret":"` + strings.Repeat("ab", 32) + `","bot_username":"ztapi_bot"}`
	require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
	cfg, e := readTelegramBridgeConfig(path)
	require.NoError(t, e)
	require.Len(t, cfg.Secret, 32)
	require.Equal(t, "ztapi_bot", cfg.BotUsername)
	require.NoError(t, os.WriteFile(path, []byte(`{"secret":"short","bot_username":"ztapi_bot"}`), 0600))
	_, e = readTelegramBridgeConfig(path)
	require.Error(t, e)
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSuffix(raw, "}")+`,"user_id":1}`), 0600))
	_, e = readTelegramBridgeConfig(path)
	require.Error(t, e)
}
