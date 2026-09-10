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
