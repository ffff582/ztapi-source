package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const sharedMediaPriceContractForTest = `{"version":1,"modality":"image","rules":[{"id":"gt_200k","conditions":{"prompt_tokens_tier":"gt_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1","output_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667","output_tokens":"1.6666666667"},"source_cells":{"input_tokens":"A1","output_tokens":"B1"}},{"id":"lte_200k","conditions":{"prompt_tokens_tier":"lte_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1","output_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667","output_tokens":"1.6666666667"},"source_cells":{"input_tokens":"A2","output_tokens":"B2"}}]}`

func TestSharedMediaPriceParserPreservesStrictTask3Rejections(t *testing.T) {
	tests := map[string]string{
		"unknown field":     sharedMediaPriceContractForTest[:len(sharedMediaPriceContractForTest)-1] + `,"unknown":true}`,
		"duplicate key":     `{"version":1,"version":1,"modality":"image","rules":[]}`,
		"incomplete matrix": `{"version":1,"modality":"image","rules":[{"id":"lte_200k","conditions":{"prompt_tokens_tier":"lte_200k"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1","output_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667","output_tokens":"1.6666666667"},"source_cells":{"input_tokens":"A2","output_tokens":"B2"}}]}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseZTAPIMediaPriceContract(raw)
			require.Error(t, err)
		})
	}
}

func TestSharedMediaPriceContractCanonicalAndProtocolCompatibility(t *testing.T) {
	contract, err := ParseZTAPIMediaPriceContract(sharedMediaPriceContractForTest)
	require.NoError(t, err)
	canonical, err := CanonicalizeZTAPIMediaPriceContract(sharedMediaPriceContractForTest)
	require.NoError(t, err)
	require.Equal(t, sharedMediaPriceContractForTest, canonical)

	protocol := ZTAPIImageProtocolContract{Usage: ZTAPIImageUsageContract{
		Fields:         map[string]string{"input_tokens": "input_tokens", "output_tokens": "output_tokens"},
		TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
	}}
	require.NoError(t, ValidateZTAPIImagePriceProtocolCompatibility(contract, protocol))
	protocol.Usage.CacheSemantics = "separate_dimension"
	require.Error(t, ValidateZTAPIImagePriceProtocolCompatibility(contract, protocol))
	protocol.Usage.CacheSemantics = "not_reported"
	delete(protocol.Usage.Fields, "output_tokens")
	require.Error(t, ValidateZTAPIImagePriceProtocolCompatibility(contract, protocol))
}

func TestGPTImage2AllowsOnlyExactThreeFieldNotReportedUsageAgainstFivePrices(t *testing.T) {
	price := ZTAPIMediaPriceContract{Version: 1, Modality: "image"}
	for _, dimension := range []string{"text_input", "text_cached_input", "image_input", "image_cached_input", "image_output"} {
		price.Rules = append(price.Rules, ZTAPIMediaPriceRule{
			ID: dimension, Conditions: map[string]string{"token_bucket": dimension},
			BillingUnit: ZTAPIMediaBillingUnitUSDPerMillionTokens,
			CostUSD:     map[string]string{dimension: "0.6"}, SaleUSD: map[string]string{dimension: "1"}, SourceCells: map[string]string{dimension: "A1"},
		})
	}
	protocol := ZTAPIImageProtocolContract{
		Version: ZTAPIImageProtocolContractVersionV2, ProviderModel: "gpt-image-2",
		EndpointType: ZTAPIImageEndpointGeneration, Method: "POST", Path: "/v1/images/generations",
		Usage: ZTAPIImageUsageContract{
			UsageField: "usage", TotalField: "total_tokens",
			Fields: map[string]string{
				"text_input":   "input_tokens_details.text_tokens",
				"image_input":  "input_tokens_details.image_tokens",
				"image_output": "output_tokens_details.image_tokens",
			},
			TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
		},
	}

	require.NoError(t, ValidateZTAPIImagePriceProtocolCompatibility(price, protocol))

	for name, mutate := range map[string]func(*ZTAPIImageProtocolContract){
		"other model":        func(c *ZTAPIImageProtocolContract) { c.ProviderModel = "other-image-model" },
		"legacy version":     func(c *ZTAPIImageProtocolContract) { c.Version = ZTAPIImageProtocolContractVersion },
		"wrong cache policy": func(c *ZTAPIImageProtocolContract) { c.Usage.CacheSemantics = "separate_dimension" },
		"extra dimension": func(c *ZTAPIImageProtocolContract) {
			c.Usage.Fields["text_cached_input"] = "input_tokens_details.cached_tokens"
		},
		"synthetic five dimensions": func(c *ZTAPIImageProtocolContract) {
			c.Usage.Fields["text_cached_input"] = "synthetic_text_cache"
			c.Usage.Fields["image_cached_input"] = "synthetic_image_cache"
			c.Usage.CacheSemantics = "separate_dimension"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := protocol.Clone()
			mutate(&candidate)
			require.Error(t, ValidateZTAPIImagePriceProtocolCompatibility(price, candidate))
		})
	}
}
