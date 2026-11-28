package model

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func xcloudConfig20261128(source, public, provider string) ZTAPIModelConfig {
	return ZTAPIModelConfig{ID: 901, Version: 1, SourceModel: source, PublicName: &public,
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: provider}
}

func TestZTAPIXcloudSource20261128PreviewAndFrozenCost(t *testing.T) {
	config := xcloudConfig20261128("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProviderOpenAI)
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "gpt-plus", 9, "")
	require.NoError(t, err)
	require.Equal(t, string(ZTAPIPricePolicyXcloud20261128), source.PricePolicy)
	require.Equal(t, ZTAPIXcloudQuoteSHA25620261128, source.SourceDocumentChecksum)
	require.Equal(t, "1.0000000000", source.InputPerMillion)
	require.Equal(t, "5.0000000000", source.OutputPerMillion)
	require.NoError(t, ValidateZTAPIModelPriceSource(&source))
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "1.2728000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "6.3637000000", preview.OutputSaleUSDPerMillion)
	require.Equal(t, "0.0637000000", preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
	require.Equal(t, "1.5910000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite])
	require.Equal(t, "3.1819000000", preview.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
	usage := map[string]int64{"input_tokens": 1000, "output_tokens": 100, "cache_read": 200, "cache_write": 10}
	cost, err := ZTAPIXcloudQuotedCost20261128(&source, config.PublicNameValue(), []int{7}, 7, "https://www.xcloudtoken.com", usage)
	require.NoError(t, err)
	require.Equal(t, "0.0015225", cost.String())
}

func TestZTAPIXcloudSource20261128PreservesHigherRetail(t *testing.T) {
	config := xcloudConfig20261128("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProviderOpenAI)
	prior := `[{"conditions":[],"sale":{"input_tokens":"2","output_tokens":"9","cache_read":"0.1","cache_write":"2","cache_write_1h":"4"}}]`
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "gpt-plus", 9, prior)
	require.NoError(t, err)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "2.0000000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "9.0000000000", preview.OutputSaleUSDPerMillion)
	_, err = BuildZTAPIXcloudPriceSource20261128(config, "gpt-plus", 9, `[{"conditions":[],"sale":{"input_tokens":"2","input_tokens":"0.1"}}]`)
	require.Error(t, err)
}

func TestZTAPIXcloudSource20261128RejectsForgedEvidenceAndRoutes(t *testing.T) {
	config := xcloudConfig20261128("gpt-6.2-sol", "zt-gpt-6.2-sol", ZTAPIProviderOpenAI)
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "gpt-plus", 9, "")
	require.NoError(t, err)
	for _, change := range []func(*ZTAPIModelPriceSource){
		func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.5" },
		func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPISupplierWorkbook20261019SHA256 },
		func(s *ZTAPIModelPriceSource) { s.SpendTier = "new-vip" },
		func(s *ZTAPIModelPriceSource) {
			s.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"input_tokens":"0.01","output_tokens":"1"}}]`
		},
		func(s *ZTAPIModelPriceSource) { s.SaleMultiplier = "0.5" },
	} {
		forged := source
		change(&forged)
		require.Error(t, ValidateZTAPIModelPriceSource(&forged))
	}
	for _, tc := range []struct {
		public, url string
		allowed     []int
		channel     int
	}{
		{"zt-gpt-6.1-sol", "https://www.xcloudtoken.com", []int{7}, 7},
		{"zt-gpt-6.2-sol", "https://ai.yunxinapi.com/hub", []int{7}, 7},
		{"zt-gpt-6.2-sol", "https://www.xcloudtoken.com", []int{6}, 6},
		{"zt-gpt-6.2-sol", "https://www.xcloudtoken.com", []int{2, 7}, 7},
	} {
		_, err := ZTAPIXcloudQuotedCost20261128(&source, tc.public, tc.allowed, tc.channel, tc.url, map[string]int64{"input_tokens": 1})
		require.Error(t, err)
	}
	for _, usage := range []map[string]int64{{"input_tokens": -1}, {"input_tokens": 1, "image": 1}, {"input_tokens": 1, "cache_write_5m": 1}} {
		_, err := ZTAPIXcloudQuotedCost20261128(&source, config.PublicNameValue(), []int{7}, 7, "https://www.xcloudtoken.com", usage)
		require.Error(t, err)
	}
}

func TestZTAPIXcloudSource20261128KnownFreeCacheIsScoped(t *testing.T) {
	config := xcloudConfig20261128("kimi-k3", "zt-kimi-k3", ZTAPIProviderMoonshot)
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "国产分组", 9, "")
	require.NoError(t, err)
	require.NoError(t, ValidateZTAPIModelPriceSource(&source))
	require.Equal(t, "0.0000000000", source.CacheWrite5mPerMillion)
	cost, err := ZTAPIXcloudQuotedCost20261128(&source, config.PublicNameValue(), []int{8}, 8, "https://www.xcloudtoken.com", map[string]int64{"cache_write_5m": 1000})
	require.NoError(t, err)
	require.True(t, cost.Equal(decimal.Zero))
	generic := source
	generic.PricePolicy = string(ZTAPIPricePolicyPool30Margin)
	require.Error(t, ValidateZTAPIModelPriceSource(&generic))
}

func TestZTAPIXcloudSource20261128ContextUsesAllExclusivePromptDimensions(t *testing.T) {
	config := xcloudConfig20261128("gpt-6-astra", "zt-gpt-6-astra", ZTAPIProviderOpenAI)
	source, err := BuildZTAPIXcloudPriceSource20261128(config, "new-vip", 9, "")
	require.NoError(t, err)
	usage := map[string]int64{"input_tokens": 271999, "cache_read": 2}
	cost, err := ZTAPIXcloudQuotedCost20261128(&source, config.PublicNameValue(), []int{6}, 6, "https://www.xcloudtoken.com", usage)
	require.NoError(t, err)
	require.Equal(t, "4.8959856", cost.String())
}
