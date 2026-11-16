package setting

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultUSDTTopUpMinUnits         int64 = 10
	defaultUSDTTopUpOrderTTLSeconds        = 600
	defaultUSDTPollIntervalSeconds         = 5
	defaultUSDTSuffixCooldownSeconds       = 86400
)

type USDTTopUpConfig struct {
	Enabled          bool
	ReceivingAddress string
	TronGridAPIKey   string
	MinTopUp         int64
	OrderTTL         time.Duration
	PollInterval     time.Duration
	SuffixCooldown   time.Duration
}

func LoadUSDTTopUpConfig() (USDTTopUpConfig, error) {
	enabled, err := parseUSDTBool("USDT_TRC20_TOPUP_ENABLED", false)
	if err != nil {
		return USDTTopUpConfig{}, err
	}
	minTopUp, err := parseUSDTInt64("USDT_TRC20_MIN_TOPUP", defaultUSDTTopUpMinUnits)
	if err != nil {
		return USDTTopUpConfig{}, err
	}
	ttlSeconds, err := parseUSDTInt64("USDT_TRC20_ORDER_TTL_SECONDS", defaultUSDTTopUpOrderTTLSeconds)
	if err != nil {
		return USDTTopUpConfig{}, err
	}
	pollSeconds, err := parseUSDTInt64("USDT_TRC20_POLL_INTERVAL_SECONDS", defaultUSDTPollIntervalSeconds)
	if err != nil {
		return USDTTopUpConfig{}, err
	}
	cooldownSeconds, err := parseUSDTInt64("USDT_TRC20_SUFFIX_COOLDOWN_SECONDS", defaultUSDTSuffixCooldownSeconds)
	if err != nil {
		return USDTTopUpConfig{}, err
	}

	config := USDTTopUpConfig{
		Enabled:          enabled,
		ReceivingAddress: strings.TrimSpace(os.Getenv("USDT_TRC20_RECEIVING_ADDRESS")),
		TronGridAPIKey:   strings.TrimSpace(os.Getenv("TRONGRID_API_KEY")),
		MinTopUp:         minTopUp,
		OrderTTL:         time.Duration(ttlSeconds) * time.Second,
		PollInterval:     time.Duration(pollSeconds) * time.Second,
		SuffixCooldown:   time.Duration(cooldownSeconds) * time.Second,
	}

	if config.MinTopUp < defaultUSDTTopUpMinUnits {
		return USDTTopUpConfig{}, fmt.Errorf("USDT minimum topup must be at least %d", defaultUSDTTopUpMinUnits)
	}
	if config.OrderTTL <= 0 {
		return USDTTopUpConfig{}, fmt.Errorf("USDT order TTL must be positive")
	}
	if config.PollInterval <= 0 || config.PollInterval > config.OrderTTL {
		return USDTTopUpConfig{}, fmt.Errorf("USDT poll interval must be positive and no longer than the order TTL")
	}
	if config.SuffixCooldown < config.OrderTTL {
		return USDTTopUpConfig{}, fmt.Errorf("USDT suffix cooldown must be at least the order TTL")
	}
	if !config.Enabled {
		return config, nil
	}
	if config.ReceivingAddress == "" {
		return USDTTopUpConfig{}, fmt.Errorf("USDT receiving address is required when topup is enabled")
	}
	if config.TronGridAPIKey == "" {
		return USDTTopUpConfig{}, fmt.Errorf("TronGrid API key is required when USDT topup is enabled")
	}
	return config, nil
}

func parseUSDTBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func parseUSDTInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}
