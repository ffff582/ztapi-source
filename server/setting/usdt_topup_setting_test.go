package setting

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func clearUSDTTopUpEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"USDT_TRC20_TOPUP_ENABLED",
		"USDT_TRC20_RECEIVING_ADDRESS",
		"TRONGRID_API_KEY",
		"USDT_TRC20_MIN_TOPUP",
		"USDT_TRC20_ORDER_TTL_SECONDS",
		"USDT_TRC20_POLL_INTERVAL_SECONDS",
		"USDT_TRC20_SUFFIX_COOLDOWN_SECONDS",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadUSDTTopUpConfigUsesSafeDisabledDefaults(t *testing.T) {
	clearUSDTTopUpEnvironment(t)

	config, err := LoadUSDTTopUpConfig()

	require.NoError(t, err)
	require.False(t, config.Enabled)
	require.Equal(t, int64(10), config.MinTopUp)
	require.Equal(t, 10*time.Minute, config.OrderTTL)
	require.Equal(t, 5*time.Second, config.PollInterval)
	require.Equal(t, 24*time.Hour, config.SuffixCooldown)
}

func TestLoadUSDTTopUpConfigRequiresCompleteEnabledConfiguration(t *testing.T) {
	clearUSDTTopUpEnvironment(t)
	t.Setenv("USDT_TRC20_TOPUP_ENABLED", "true")

	_, err := LoadUSDTTopUpConfig()

	require.ErrorContains(t, err, "receiving address")
}

func TestLoadUSDTTopUpConfigRejectsUnsafeRuntimeValues(t *testing.T) {
	clearUSDTTopUpEnvironment(t)
	t.Setenv("USDT_TRC20_TOPUP_ENABLED", "true")
	t.Setenv("USDT_TRC20_RECEIVING_ADDRESS", "TJSdKoxvYJofK6CQBNnXwMM9kS1t4Sj3V2")
	t.Setenv("TRONGRID_API_KEY", "test-only-key")
	t.Setenv("USDT_TRC20_POLL_INTERVAL_SECONDS", "0")

	_, err := LoadUSDTTopUpConfig()

	require.ErrorContains(t, err, "poll interval")
}
