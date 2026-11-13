package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func configFixture() string {
	return `{"bot_token":"123456:offline-test-token-only","owner_id":123456789,"bot_id":555777999,"bot_username":"example_test_bot","provider":{"endpoint":"https://www.xcloudtoken.com/v1/chat/completions","api_key":"offline-provider-key-only","model":"deepseek-v4.1-flash","channel_id":8,"supplier":"xcloud","group":"国产分组","group_multiplier":"0.6","cash_per_credit":"0.5","input_credit_per_million":"0.14","output_credit_per_million":"0.5","cache_read_credit_per_million":"0.003","quote_evidence":"fixture-quote","vision_enabled":true}}`
}

func TestTelegramConfigurationRequiresPrivateOwnerAndAuthorizedFlash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.json")
	require.NoError(t, os.WriteFile(path, []byte(configFixture()), 0600))
	cfg, err := readConfig(path)
	require.NoError(t, err)
	require.Equal(t, int64(123456789), cfg.OwnerID)
	for _, raw := range []string{
		strings.Replace(configFixture(), `"owner_id":123456789`, `"owner_id":-100123456`, 1),
		strings.Replace(configFixture(), `"bot_token":"123456:offline-test-token-only"`, `"bot_token":"wrong"`, 1),
		strings.Replace(configFixture(), "www.xcloudtoken.com", "arbitrary.invalid", 1),
		strings.Replace(configFixture(), `"bot_id":555777999`, `"bot_id":555777999,"bot_id":1`, 1),
	} {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
		_, err := readConfig(path)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "offline-test-token")
		require.NotContains(t, err.Error(), "offline-provider-key")
	}
}

func TestOptionalBridgeConfigurationRequiresFixedInternalEndpointAndRaw32ByteSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.json")
	endpoint := "http://ztapi-support-20261102:3012/internal/telegram-support"
	secret := strings.Repeat("ab", 32)
	withBridge := func(bridge string) string {
		return strings.TrimSuffix(configFixture(), "}") + `,"bridge":` + bridge + `}`
	}
	valid := `{"endpoint":"` + endpoint + `","secret":"` + secret + `"}`
	require.NoError(t, os.WriteFile(path, []byte(withBridge(valid)), 0600))
	_, err := readConfig(path)
	require.NoError(t, err)
	for _, bridge := range []string{
		`{}`, `{"endpoint":"` + endpoint + `"}`, `{"secret":"` + secret + `"}`,
		strings.Replace(valid, endpoint, "https://evil.invalid/internal/telegram-support", 1),
		strings.Replace(valid, secret, strings.Repeat("a", 63), 1),
		strings.Replace(valid, secret, strings.Repeat("zz", 32), 1),
		strings.Replace(valid, secret, strings.Repeat("00", 32), 1),
		strings.Replace(valid, `"endpoint":`, `"endpoint":"bad","endpoint":`, 1),
	} {
		require.NoError(t, os.WriteFile(path, []byte(withBridge(bridge)), 0600))
		_, err := readConfig(path)
		require.Error(t, err)
		require.NotContains(t, err.Error(), secret)
	}
}
