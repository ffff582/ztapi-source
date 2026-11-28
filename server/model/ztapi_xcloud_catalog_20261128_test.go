package model

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestZTAPIXcloud20261128CatalogHasExactQuotes(t *testing.T) {
	quotes, err := ZTAPIXcloudCatalogQuotes20261128()
	require.NoError(t, err)
	require.Len(t, quotes, 40)
	models := map[string]bool{}
	for _, quote := range quotes {
		models[quote.SourceModel] = true
		require.True(t, quote.GroupMultiplier.IsPositive())
		require.True(t, quote.RechargeDiscount.Equal(decimal.RequireFromString("0.5")))
		for dimension, rate := range quote.CashRates {
			require.True(t, rate.Equal(quote.BaseRates[dimension].Mul(quote.GroupMultiplier).Mul(quote.RechargeDiscount)))
		}
	}
	require.Len(t, models, 23)
	for _, removed := range []string{"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-4-6", "claude-sonnet-5", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra"} {
		require.NotContains(t, models, removed)
	}
}

func TestZTAPIXcloud20261128GroupAndExpressionPrices(t *testing.T) {
	for _, tc := range []struct{ source, group, input, output, read string }{
		{"gpt-6.1-sol", "gpt-plus", "1", "5", "0.05"},
		{"gpt-6.2-astra", "gpt-plus", "4", "20", "0.2"},
		{"gpt-6.1-terra", "gpt-plus", "0.5", "2.5", "0.025"},
		{"gpt-6.2-sol", "gpt-plus", "1", "5", "0.05"},
		{"claude-fable-5-2", "claude-max", "4", "20", "0.1"},
		{"claude-fable-5-2", "new-vip", "7.2", "36", "0.18"},
		{"claude-haiku-5-5", "claude-max", "0.05", "0.25", "0.005"},
		{"claude-haiku-4-5-20251001", "claude-kiro", "0.5", "2.5", "0.05"},
		{"glm-5.3-flash", "国产分组", "0.24", "0.84", "0.069"},
		{"deepseek-v4.1-flash", "国产分组", "0.042", "0.15", "0.0009"},
	} {
		t.Run(tc.source+"/"+tc.group, func(t *testing.T) {
			quote, err := ZTAPIXcloudQuoteFor20261128(tc.source, tc.group, 1)
			require.NoError(t, err)
			for dimension, want := range map[string]string{"input_tokens": tc.input, "output_tokens": tc.output, "cache_read": tc.read} {
				require.True(t, quote.CashRates[dimension].Equal(decimal.RequireFromString(want)), "%s: %s", dimension, quote.CashRates[dimension])
			}
		})
	}
}

func TestZTAPIXcloud20261128ExactGroupAndContextBoundary(t *testing.T) {
	standard, err := ZTAPIXcloudQuoteFor20261128("gpt-6-astra", "new-vip", 272000)
	require.NoError(t, err)
	require.Equal(t, "standard", standard.Tier)
	require.Equal(t, "9", standard.CashRates["input_tokens"].String())
	long, err := ZTAPIXcloudQuoteFor20261128("gpt-6-astra", "new-vip", 272001)
	require.NoError(t, err)
	require.Equal(t, "long_context", long.Tier)
	require.Equal(t, "18", long.CashRates["input_tokens"].String())
	require.Equal(t, "67.5", long.CashRates["output_tokens"].String())
	for _, tc := range [][2]string{{"claude-fable-5", "claude-max"}, {"claude-haiku-4-5-20251001", "claude-max"}, {"gpt-6.2-sol", "default"}, {"gpt-6.2-sol", "svip"}, {"gpt-5.6-sol", "gpt-plus"}} {
		_, err := ZTAPIXcloudQuoteFor20261128(tc[0], tc[1], 1)
		require.Error(t, err)
	}
	_, err = ZTAPIXcloudQuoteFor20261128("gpt-6.2-sol", "gpt-plus", -1)
	require.Error(t, err)
}

func TestZTAPIXcloud20261128MissingAndFreeCacheAreDifferent(t *testing.T) {
	free, err := ZTAPIXcloudQuoteFor20261128("kimi-k3", "国产分组", 1)
	require.NoError(t, err)
	value, exists := free.CashRates["cache_write_5m"]
	require.True(t, exists)
	require.True(t, value.IsZero())
	require.Equal(t, "0.45", free.CashRates["cache_write_1h"].String())
	missing, err := ZTAPIXcloudQuoteFor20261128("deepseek-v4.1-flash", "国产分组", 1)
	require.NoError(t, err)
	require.NotContains(t, missing.CashRates, "cache_write_5m")
	require.NotContains(t, missing.CashRates, "cache_write_1h")
}

func TestZTAPIXcloud20261128GPTCreationDoesNotInventTTL(t *testing.T) {
	quote, err := ZTAPIXcloudQuoteFor20261128("gpt-6.2-sol", "gpt-plus", 1)
	require.NoError(t, err)
	require.Equal(t, "1.25", quote.CashRates[ZTAPIBillingDimensionCacheWrite].String())
	require.NotContains(t, quote.CashRates, ZTAPIBillingDimensionCacheWrite5m)
	require.Equal(t, "2.5", quote.CashRates[ZTAPIBillingDimensionCacheWrite1h].String())
	old, err := buildZTAPIXcloudSource20261114("gpt-6-astra", 9)
	require.NoError(t, err)
	config := xcloudConfig20261128("gpt-6-astra", "zt-gpt-6-astra", ZTAPIProviderOpenAI)
	_, err = BuildZTAPIXcloudPriceSource20261128(config, "new-vip", 9, old.TokenPriceRulesJSON)
	require.NoError(t, err, "existing generic GPT creation retail must remain compatible")
}

func TestZTAPIXcloud20261128RejectsChangedLiveQuote(t *testing.T) {
	raw, err := os.ReadFile("ztapi_xcloud_catalog_20261128.json")
	require.NoError(t, err)
	require.NoError(t, ValidateZTAPIXcloudLiveQuote20261128(raw))
	for _, pair := range [][2]string{
		{ZTAPIXcloudPricingVersion20261128, "changed-pricing-version"},
		{"p * 2 + c * 10", "p * 2 + c * 11"},
	} {
		changed := strings.Replace(string(raw), pair[0], pair[1], 1)
		require.NotEqual(t, string(raw), changed)
		require.Error(t, ValidateZTAPIXcloudLiveQuote20261128([]byte(changed)))
	}
}

func TestZTAPIXcloud20261128LiveEndpointOrderDoesNotChangeTheQuote(t *testing.T) {
	var manifest ztapiXcloudManifest20261128Type
	require.NoError(t, json.Unmarshal(ztapiXcloudManifest20261128, &manifest))
	for i := range manifest.Data {
		slices.Reverse(manifest.Data[i].Endpoints)
		slices.Reverse(manifest.Data[i].Groups)
	}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, ValidateZTAPIXcloudLiveQuote20261128(raw))
}

func TestZTAPIXcloud20261128RetailFloorCoversDoubleReward(t *testing.T) {
	for _, raw := range []string{"0", "0.0000000001", "0.0009", "0.05", "0.24", "1", "4", "67.5"} {
		cost := decimal.RequireFromString(raw)
		sale, err := ZTAPIXcloudRetailFloor20261128(cost)
		require.NoError(t, err)
		require.True(t, sale.Mul(decimal.NewFromInt(11)).GreaterThanOrEqual(cost.Mul(decimal.NewFromInt(14))))
		require.True(t, sale.Mul(decimal.NewFromInt(17)).GreaterThanOrEqual(cost.Mul(decimal.NewFromInt(21))))
		if sale.IsPositive() {
			// Seven CNY gross, 5% processor fee, 5% USDT commission,
			// 5% balance bonus and a 6.8 CNY cash purchase stress rate.
			gross := decimal.NewFromInt(7).Mul(sale)
			expenses := decimal.RequireFromString("0.69").Mul(sale).Add(decimal.RequireFromString("7.14").Mul(cost))
			require.True(t, gross.Sub(expenses).GreaterThanOrEqual(gross.Mul(decimal.RequireFromString("0.1"))))
		}
	}
	floor, err := ZTAPIXcloudRetailFloor20261128(decimal.NewFromInt(1))
	require.NoError(t, err)
	require.Equal(t, "1.2728", floor.String())
	_, err = ZTAPIXcloudRetailFloor20261128(decimal.NewFromInt(-1))
	require.Error(t, err)
}

func TestZTAPIXcloud20261128RejectsUnsupportedSupplierGrammar(t *testing.T) {
	raw, err := os.ReadFile("ztapi_xcloud_catalog_20261128.json")
	require.NoError(t, err)
	for _, pair := range [][2]string{{"p * 2 + c * 10", "p * 2 + p * 10"}, {"p * 2 + c * 10", "p * 2 + c * -10"}, {"p * 2 + c * 10", "run(2) + c * 10"}} {
		tampered := strings.Replace(string(raw), pair[0], pair[1], 1)
		require.NotEqual(t, string(raw), tampered)
		_, err := parseZTAPIXcloudQuoteManifest20261128([]byte(tampered))
		require.Error(t, err)
	}
}

func TestZTAPIXcloud20261128DoesNotMutateHistoricalOrSharedQuotes(t *testing.T) {
	before, err := buildZTAPIXcloudSource20261114("claude-haiku-4-5-20251001", 9)
	require.NoError(t, err)
	quote, err := ZTAPIXcloudQuoteFor20261128("claude-haiku-4-5-20251001", "claude-kiro", 1)
	require.NoError(t, err)
	quote.CashRates["input_tokens"] = decimal.NewFromInt(99)
	again, err := ZTAPIXcloudQuoteFor20261128("claude-haiku-4-5-20251001", "claude-kiro", 1)
	require.NoError(t, err)
	require.Equal(t, "0.5", again.CashRates["input_tokens"].String())
	after, err := buildZTAPIXcloudSource20261114("claude-haiku-4-5-20251001", 9)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, "claude-max", after.SpendTier)
}
