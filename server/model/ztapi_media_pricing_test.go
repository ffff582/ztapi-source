package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ztapiMediaBillingUnit = "usd_per_million_tokens"

func mediaRule(id string, conditions, cost, sale, cells map[string]string) ZTAPIMediaPriceRule {
	return ZTAPIMediaPriceRule{
		ID: id, Conditions: conditions, BillingUnit: ztapiMediaBillingUnit,
		CostUSD: cost, SaleUSD: sale, SourceCells: cells,
	}
}

func mediaContractJSON(t *testing.T, modality string, rules []ZTAPIMediaPriceRule) string {
	t.Helper()
	raw, err := common.Marshal(ZTAPIMediaPriceContract{Version: 1, Modality: modality, Rules: rules})
	require.NoError(t, err)
	return string(raw)
}

func gpImage2ContractForTest(t *testing.T) string {
	t.Helper()
	cases := []struct {
		id, cost, sale, cell string
	}{
		{"text_input", "1.65", "4.00", "G56"},
		{"text_cached_input", "0.4125", "1.00", "H56"},
		{"image_input", "2.64", "6.40", "G57"},
		{"image_cached_input", "0.66", "1.60", "H57"},
		{"image_output", "9.90", "24.00", "J57"},
	}
	rules := make([]ZTAPIMediaPriceRule, 0, len(cases))
	for _, tc := range cases {
		rules = append(rules, mediaRule(tc.id,
			map[string]string{"token_bucket": tc.id},
			map[string]string{tc.id: tc.cost},
			map[string]string{tc.id: tc.sale},
			map[string]string{tc.id: tc.cell},
		))
	}
	return mediaContractJSON(t, ZTAPIModalityImage, rules)
}

func geminiImageContractForTest(t *testing.T) string {
	t.Helper()
	return mediaContractJSON(t, ZTAPIModalityImage, []ZTAPIMediaPriceRule{
		mediaRule("lte_200k", map[string]string{"prompt_tokens_tier": "lte_200k"},
			map[string]string{"input_tokens": "0.246", "output_tokens": "2.05"},
			map[string]string{"input_tokens": "0.3075", "output_tokens": "2.5625"},
			map[string]string{"input_tokens": "F67", "output_tokens": "J67"}),
		mediaRule("gt_200k", map[string]string{"prompt_tokens_tier": "gt_200k"},
			map[string]string{"input_tokens": "0.246", "output_tokens": "24.60"},
			map[string]string{"input_tokens": "0.3075", "output_tokens": "30.75"},
			map[string]string{"input_tokens": "F68", "output_tokens": "J68"}),
	})
}

func seedanceContractForTest(t *testing.T) string {
	t.Helper()
	type priceCase struct {
		resolution, containsVideo, cost, sale, cell string
	}
	cases := []priceCase{
		{"480p", "false", "6.2515277778", "7.8144097223", "F2"},
		{"480p", "true", "3.8052777778", "4.7565972223", "G2"},
		{"720p", "false", "6.2515277778", "7.8144097223", "F2"},
		{"720p", "true", "3.8052777778", "4.7565972223", "G2"},
		{"1080p", "false", "6.9310416667", "8.6638020834", "F3"},
		{"1080p", "true", "4.2129861111", "5.2662326389", "G3"},
		{"4k", "false", "3.5334722222", "4.4168402778", "F4"},
		{"4k", "true", "2.1744444444", "2.7180555555", "G4"},
	}
	rules := make([]ZTAPIMediaPriceRule, 0, len(cases))
	for _, tc := range cases {
		id := tc.resolution + "_video_" + tc.containsVideo
		rules = append(rules, mediaRule(id,
			map[string]string{"contains_video_input": tc.containsVideo, "resolution": tc.resolution},
			map[string]string{"input_tokens": tc.cost},
			map[string]string{"input_tokens": tc.sale},
			map[string]string{"input_tokens": tc.cell},
		))
	}
	return mediaContractJSON(t, ZTAPIModalityVideo, rules)
}

func seedanceVariantContractForTest(t *testing.T, withoutCost, withoutSale, withoutCell, withCost, withSale, withCell string) string {
	t.Helper()
	return mediaContractJSON(t, ZTAPIModalityVideo, []ZTAPIMediaPriceRule{
		mediaRule("without_video_input", map[string]string{"contains_video_input": "false"},
			map[string]string{"input_tokens": withoutCost}, map[string]string{"input_tokens": withoutSale},
			map[string]string{"input_tokens": withoutCell}),
		mediaRule("with_video_input", map[string]string{"contains_video_input": "true"},
			map[string]string{"input_tokens": withCost}, map[string]string{"input_tokens": withSale},
			map[string]string{"input_tokens": withCell}),
	})
}

func TestZTAPIMediaPriceContractSelectsExactQuotedCases(t *testing.T) {
	tests := []struct {
		name, raw, modality string
		conditions          map[string]string
		wantID              string
		wantCost, wantSale  map[string]string
	}{
		{"gp text", gpImage2ContractForTest(t), ZTAPIModalityImage, map[string]string{"token_bucket": "text_input"}, "text_input", map[string]string{"text_input": "1.65"}, map[string]string{"text_input": "4.00"}},
		{"gp cached text", gpImage2ContractForTest(t), ZTAPIModalityImage, map[string]string{"token_bucket": "text_cached_input"}, "text_cached_input", map[string]string{"text_cached_input": "0.4125"}, map[string]string{"text_cached_input": "1.00"}},
		{"gp image", gpImage2ContractForTest(t), ZTAPIModalityImage, map[string]string{"token_bucket": "image_input"}, "image_input", map[string]string{"image_input": "2.64"}, map[string]string{"image_input": "6.40"}},
		{"gp cached image", gpImage2ContractForTest(t), ZTAPIModalityImage, map[string]string{"token_bucket": "image_cached_input"}, "image_cached_input", map[string]string{"image_cached_input": "0.66"}, map[string]string{"image_cached_input": "1.60"}},
		{"gp output", gpImage2ContractForTest(t), ZTAPIModalityImage, map[string]string{"token_bucket": "image_output"}, "image_output", map[string]string{"image_output": "9.90"}, map[string]string{"image_output": "24.00"}},
		{"gemini short", geminiImageContractForTest(t), ZTAPIModalityImage, map[string]string{"prompt_tokens_tier": "lte_200k"}, "lte_200k", map[string]string{"input_tokens": "0.246", "output_tokens": "2.05"}, map[string]string{"input_tokens": "0.3075", "output_tokens": "2.5625"}},
		{"gemini long", geminiImageContractForTest(t), ZTAPIModalityImage, map[string]string{"prompt_tokens_tier": "gt_200k"}, "gt_200k", map[string]string{"input_tokens": "0.246", "output_tokens": "24.60"}, map[string]string{"input_tokens": "0.3075", "output_tokens": "30.75"}},
		{"seedance 480p no video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "480p", "contains_video_input": "false"}, "480p_video_false", map[string]string{"input_tokens": "6.2515277778"}, map[string]string{"input_tokens": "7.8144097223"}},
		{"seedance 480p video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "480p", "contains_video_input": "true"}, "480p_video_true", map[string]string{"input_tokens": "3.8052777778"}, map[string]string{"input_tokens": "4.7565972223"}},
		{"seedance 720p no video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "720p", "contains_video_input": "false"}, "720p_video_false", map[string]string{"input_tokens": "6.2515277778"}, map[string]string{"input_tokens": "7.8144097223"}},
		{"seedance 720p video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "720p", "contains_video_input": "true"}, "720p_video_true", map[string]string{"input_tokens": "3.8052777778"}, map[string]string{"input_tokens": "4.7565972223"}},
		{"seedance 1080p no video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "1080p", "contains_video_input": "false"}, "1080p_video_false", map[string]string{"input_tokens": "6.9310416667"}, map[string]string{"input_tokens": "8.6638020834"}},
		{"seedance 1080p video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "1080p", "contains_video_input": "true"}, "1080p_video_true", map[string]string{"input_tokens": "4.2129861111"}, map[string]string{"input_tokens": "5.2662326389"}},
		{"seedance quoted cheap 4k no video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "4k", "contains_video_input": "false"}, "4k_video_false", map[string]string{"input_tokens": "3.5334722222"}, map[string]string{"input_tokens": "4.4168402778"}},
		{"seedance quoted cheap 4k video", seedanceContractForTest(t), ZTAPIModalityVideo, map[string]string{"resolution": "4k", "contains_video_input": "true"}, "4k_video_true", map[string]string{"input_tokens": "2.1744444444"}, map[string]string{"input_tokens": "2.7180555555"}},
		{"fast no video", seedanceVariantContractForTest(t, "5.0284027778", "6.2855034723", "F5", "2.9898611111", "3.7373263889", "G5"), ZTAPIModalityVideo, map[string]string{"contains_video_input": "false"}, "without_video_input", map[string]string{"input_tokens": "5.0284027778"}, map[string]string{"input_tokens": "6.2855034723"}},
		{"fast video", seedanceVariantContractForTest(t, "5.0284027778", "6.2855034723", "F5", "2.9898611111", "3.7373263889", "G5"), ZTAPIModalityVideo, map[string]string{"contains_video_input": "true"}, "with_video_input", map[string]string{"input_tokens": "2.9898611111"}, map[string]string{"input_tokens": "3.7373263889"}},
		{"mini no video", seedanceVariantContractForTest(t, "3.1257638889", "3.9072048611", "F6", "1.9026388889", "2.3782986111", "G6"), ZTAPIModalityVideo, map[string]string{"contains_video_input": "false"}, "without_video_input", map[string]string{"input_tokens": "3.1257638889"}, map[string]string{"input_tokens": "3.9072048611"}},
		{"mini video", seedanceVariantContractForTest(t, "3.1257638889", "3.9072048611", "F6", "1.9026388889", "2.3782986111", "G6"), ZTAPIModalityVideo, map[string]string{"contains_video_input": "true"}, "with_video_input", map[string]string{"input_tokens": "1.9026388889"}, map[string]string{"input_tokens": "2.3782986111"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, ValidateZTAPIMediaPriceContract(tc.raw))
			got, err := SelectZTAPIMediaPriceRule(tc.raw, ZTAPIMediaPriceSelector{Modality: tc.modality, Conditions: tc.conditions})
			require.NoError(t, err)
			require.Equal(t, tc.wantID, got.ID)
			require.Equal(t, tc.wantCost, got.CostUSD)
			require.Equal(t, tc.wantSale, got.SaleUSD)
		})
	}
}

func TestZTAPIMediaPriceContractFailsClosed(t *testing.T) {
	valid := geminiImageContractForTest(t)
	selectors := []struct {
		raw      string
		selector ZTAPIMediaPriceSelector
	}{
		{valid, ZTAPIMediaPriceSelector{Modality: "audio", Conditions: map[string]string{"prompt_tokens_tier": "lte_200k"}}},
		{valid, ZTAPIMediaPriceSelector{Modality: ZTAPIModalityImage, Conditions: map[string]string{"prompt_tokens_tier": "unknown"}}},
		{gpImage2ContractForTest(t), ZTAPIMediaPriceSelector{Modality: ZTAPIModalityImage, Conditions: map[string]string{"token_bucket": "unknown_input_type"}}},
		{valid, ZTAPIMediaPriceSelector{Modality: ZTAPIModalityImage, Conditions: map[string]string{"prompt_tokens_tier": "lte_200k", "duration": "5"}}},
		{seedanceContractForTest(t), ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"resolution": "2k", "contains_video_input": "false"}}},
		{seedanceContractForTest(t), ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"resolution": "1080p", "contains_video_input": "unknown"}}},
		{seedanceContractForTest(t), ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"resolution": "1080p", "contains_video_input": "false", "duration": "5"}}},
	}
	for _, tc := range selectors {
		_, err := SelectZTAPIMediaPriceRule(tc.raw, tc.selector)
		require.Error(t, err)
	}

	var contract ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(valid, &contract))
	mutations := map[string]func(*ZTAPIMediaPriceContract){
		"missing source cell": func(c *ZTAPIMediaPriceContract) { delete(c.Rules[0].SourceCells, "input_tokens") },
		"duplicate condition": func(c *ZTAPIMediaPriceContract) { c.Rules[1].Conditions = c.Rules[0].Conditions },
		"negative value":      func(c *ZTAPIMediaPriceContract) { c.Rules[0].CostUSD["input_tokens"] = "-0.1" },
		"float exponent":      func(c *ZTAPIMediaPriceContract) { c.Rules[0].SaleUSD["input_tokens"] = "4.1e-1" },
		"overlapping rule": func(c *ZTAPIMediaPriceContract) {
			c.Rules = append(c.Rules, mediaRule("overlap", map[string]string{"prompt_tokens_tier": "lte_200k"},
				map[string]string{"input_tokens": "0.246"}, map[string]string{"input_tokens": "0.41"},
				map[string]string{"input_tokens": "F67"}))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var changed ZTAPIMediaPriceContract
			require.NoError(t, common.UnmarshalJsonStr(valid, &changed))
			mutate(&changed)
			raw, err := common.Marshal(changed)
			require.NoError(t, err)
			require.Error(t, ValidateZTAPIMediaPriceContract(string(raw)))
		})
	}

	withUnknownField := strings.TrimSuffix(valid, "}") + `,"billing_unlt":"typo"}`
	require.Error(t, ValidateZTAPIMediaPriceContract(withUnknownField))
}

func TestZTAPIMediaPriceContractRejectsUnknownAndIncompleteMatrices(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		mutate func(*ZTAPIMediaPriceContract)
	}{
		{"gp missing bucket", gpImage2ContractForTest(t), func(c *ZTAPIMediaPriceContract) { c.Rules = c.Rules[:4] }},
		{"gp extra bucket", gpImage2ContractForTest(t), func(c *ZTAPIMediaPriceContract) {
			c.Rules = append(c.Rules, mediaRule("other_bucket", map[string]string{"token_bucket": "other_bucket"},
				map[string]string{"input_tokens": "0.6"}, map[string]string{"input_tokens": "1"},
				map[string]string{"input_tokens": "Z99"}))
		}},
		{"gemini missing tier", geminiImageContractForTest(t), func(c *ZTAPIMediaPriceContract) { c.Rules = c.Rules[:1] }},
		{"gemini extra tier", geminiImageContractForTest(t), func(c *ZTAPIMediaPriceContract) {
			c.Rules = append(c.Rules, mediaRule("eq_200k", map[string]string{"prompt_tokens_tier": "eq_200k"},
				map[string]string{"input_tokens": "0.6"}, map[string]string{"input_tokens": "1"},
				map[string]string{"input_tokens": "Z99"}))
		}},
		{"standard missing combination", seedanceContractForTest(t), func(c *ZTAPIMediaPriceContract) { c.Rules = c.Rules[:7] }},
		{"standard unknown resolution", seedanceContractForTest(t), func(c *ZTAPIMediaPriceContract) {
			c.Rules[0].ID = "2k_video_false"
			c.Rules[0].Conditions["resolution"] = "2k"
		}},
		{"standard unknown input type", seedanceContractForTest(t), func(c *ZTAPIMediaPriceContract) {
			c.Rules[0].ID = "480p_video_yes"
			c.Rules[0].Conditions["contains_video_input"] = "yes"
		}},
		{"variant missing branch", seedanceVariantContractForTest(t, "5.0284027778", "6.2855034723", "F5", "2.9898611111", "3.7373263889", "G5"), func(c *ZTAPIMediaPriceContract) {
			c.Rules = c.Rules[:1]
		}},
		{"variant extra branch", seedanceVariantContractForTest(t, "3.1257638889", "3.9072048611", "F6", "1.9026388889", "2.3782986111", "G6"), func(c *ZTAPIMediaPriceContract) {
			c.Rules = append(c.Rules, mediaRule("unknown_video_input", map[string]string{"contains_video_input": "unknown"},
				map[string]string{"input_tokens": "0.6"}, map[string]string{"input_tokens": "1"},
				map[string]string{"input_tokens": "Z99"}))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var contract ZTAPIMediaPriceContract
			require.NoError(t, common.UnmarshalJsonStr(tc.raw, &contract))
			tc.mutate(&contract)
			raw, err := common.Marshal(contract)
			require.NoError(t, err)
			require.Error(t, ValidateZTAPIMediaPriceContract(string(raw)))
		})
	}
}

func TestZTAPIMediaPriceContractRejectsDuplicateJSONMembersAtEveryDepth(t *testing.T) {
	valid := gpImage2ContractForTest(t)
	tests := map[string]struct {
		old string
		new string
	}{
		"top-level field": {
			old: `"version":1`,
			new: `"version":1,"version":1`,
		},
		"escaped-equivalent top-level field": {
			old: `"version":1`,
			new: `"version":1,"\u0076ersion":1`,
		},
		"rule conditions object": {
			old: `"conditions":{"token_bucket":"text_input"}`,
			new: `"conditions":{"token_bucket":"text_input"},"conditions":{"token_bucket":"text_input"}`,
		},
		"rule cost object": {
			old: `"cost_usd":{"text_input":"1.65"}`,
			new: `"cost_usd":{"text_input":"1.65"},"cost_usd":{"text_input":"1.65"}`,
		},
		"nested condition key": {
			old: `"conditions":{"token_bucket":"text_input"}`,
			new: `"conditions":{"token_bucket":"text_input","token_bucket":"text_input"}`,
		},
		"nested dimension key": {
			old: `"cost_usd":{"text_input":"1.65"}`,
			new: `"cost_usd":{"text_input":"1.65","text_input":"1.65"}`,
		},
		"escaped-equivalent nested object field": {
			old: `"cost_usd":{"text_input":"1.65"}`,
			new: `"cost_usd":{"text_input":"1.65"},"cost_\u0075sd":{"text_input":"1.65"}`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			duplicate := strings.Replace(valid, tc.old, tc.new, 1)
			require.NotEqual(t, valid, duplicate)
			require.Error(t, ValidateZTAPIMediaPriceContract(duplicate))
		})
	}
}

func TestZTAPIMediaPriceContractUsesFrozenDecimalCalculations(t *testing.T) {
	tests := []struct {
		name, raw, discount, currency, cost, sale string
		policy                                    ZTAPIPricePolicy
	}{
		{"gp text", "5", "0.33", "USD", "1.65", "4.00", ZTAPIPricePolicyPoolOfficial80},
		{"gemini long output", "30", "0.82", "USD", "24.60", "30.75", ZTAPIPricePolicyEnterprise20Margin},
		{"seedance 480p no video", "46", "0.95", "CNY", "6.2515277778", "7.8144097223", ZTAPIPricePolicyEnterprise20Margin},
		{"seedance 1080p video", "31", "0.95", "CNY", "4.2129861111", "5.2662326389", ZTAPIPricePolicyEnterprise20Margin},
		{"seedance quoted cheap 4k no video", "26", "0.95", "CNY", "3.5334722222", "4.4168402778", ZTAPIPricePolicyEnterprise20Margin},
		{"fast video", "22", "0.95", "CNY", "2.9898611111", "3.7373263889", ZTAPIPricePolicyEnterprise20Margin},
		{"mini no video", "23", "0.95", "CNY", "3.1257638889", "3.9072048611", ZTAPIPricePolicyEnterprise20Margin},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cost := decimal.RequireFromString(tc.raw).Mul(decimal.RequireFromString(tc.discount))
			var err error
			if tc.currency == "CNY" {
				cost, err = ConvertZTAPICNYCostToUSD(cost, decimal.RequireFromString("7.2"))
				require.NoError(t, err)
			}
			require.Truef(t, cost.Equal(decimal.RequireFromString(tc.cost)), "cost: got %s want %s", cost, tc.cost)
			var sale decimal.Decimal
			if tc.policy == ZTAPIPricePolicyPoolOfficial80 {
				sale, err = CalculateZTAPIPoolSalePrice(decimal.RequireFromString(tc.raw))
			} else {
				sale, err = CalculateZTAPISalePriceForPolicy(cost, tc.policy)
			}
			require.NoError(t, err)
			require.Truef(t, sale.Equal(decimal.RequireFromString(tc.sale)), "sale: got %s want %s", sale, tc.sale)
		})
	}
}

func TestZTAPIMediaPriceContractCanonicalizesRulesAndMaps(t *testing.T) {
	raw := geminiImageContractForTest(t)
	var contract ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(raw, &contract))
	contract.Rules[0], contract.Rules[1] = contract.Rules[1], contract.Rules[0]
	reversed, err := common.Marshal(contract)
	require.NoError(t, err)

	canonical, err := canonicalizeZTAPIMediaPriceContract(string(reversed))
	require.NoError(t, err)
	var got ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(canonical, &got))
	require.Equal(t, []string{"gt_200k", "lte_200k"}, []string{got.Rules[0].ID, got.Rules[1].ID})
	require.Equal(t, canonical, mustCanonicalZTAPIMediaPriceContract(t, canonical))
}

func mustCanonicalZTAPIMediaPriceContract(t *testing.T, raw string) string {
	t.Helper()
	canonical, err := canonicalizeZTAPIMediaPriceContract(raw)
	require.NoError(t, err)
	return canonical
}

func TestZTAPIMediaPriceContractQuotationEvidenceDoesNotPublish(t *testing.T) {
	want := map[string]struct {
		modality, cell, currency, discount, rate, buffer string
		contract                                         string
		policy                                           string
	}{
		"gp-image-2":        {ZTAPIModalityImage, "C56", "USD", "33", "", "", gpImage2ContractForTest(t), string(ZTAPIPricePolicyPoolOfficial80)},
		"gm25-fl-IMAGE":     {ZTAPIModalityImage, "C67", "USD", "82", "", "", geminiImageContractForTest(t), string(ZTAPIPricePolicyEnterprise20Margin)},
		"seedance-2.0":      {ZTAPIModalityVideo, "C2", "CNY", "95", "7.2", "1.03", seedanceContractForTest(t), string(ZTAPIPricePolicyEnterprise20Margin)},
		"Seedance 2.0 Fast": {ZTAPIModalityVideo, "C5", "CNY", "95", "7.2", "1.03", seedanceVariantContractForTest(t, "5.0284027778", "6.2855034723", "F5", "2.9898611111", "3.7373263889", "G5"), string(ZTAPIPricePolicyEnterprise20Margin)},
		"Seedance 2.0 Mini": {ZTAPIModalityVideo, "C6", "CNY", "95", "7.2", "1.03", seedanceVariantContractForTest(t, "3.1257638889", "3.9072048611", "F6", "1.9026388889", "2.3782986111", "G6"), string(ZTAPIPricePolicyEnterprise20Margin)},
	}
	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	require.Len(t, entries, 46)
	matched := 0
	for _, entry := range entries {
		expected, ok := want[entry.Label]
		if !ok {
			continue
		}
		matched++
		if entry.Label == "gp-image-2" {
			require.Equal(t, "mapped", entry.Status)
			require.Equal(t, "gpt-image-2", entry.SourceModel)
			require.Equal(t, "zt-gp-image-2", entry.PublicName)
			require.Equal(t, ZTAPIProtocolOpenAICompatible, entry.Protocol)
			require.Equal(t, ZTAPIProviderOpenAI, entry.ProviderFamily)
		} else if entry.Label == "gm25-fl-IMAGE" {
			require.Equal(t, "mapped", entry.Status)
			require.Equal(t, "gemini-2.5-flash-image", entry.SourceModel)
			require.Equal(t, "zt-gemini-2.5-flash-image", entry.PublicName)
			require.Equal(t, ZTAPIProtocolOpenAICompatible, entry.Protocol)
			require.Equal(t, ZTAPIProviderGoogle, entry.ProviderFamily)
		} else {
			require.Equal(t, "mapping_pending", entry.Status)
			require.Empty(t, entry.SourceModel)
			require.Empty(t, entry.PublicName)
			require.Empty(t, entry.Protocol)
			require.Empty(t, entry.ProviderFamily)
		}
		require.Equal(t, expected.modality, entry.Modality)
		var priced []ZTAPIQuotationRow
		for _, row := range entry.QuoteRows {
			if row.MediaPriceContractJSON != "" {
				priced = append(priced, row)
			}
		}
		require.Len(t, priced, 1)
		row := priced[0]
		require.Equal(t, expected.cell, row.Cell)
		require.Equal(t, expected.currency, row.Currency)
		require.Equal(t, expected.discount, row.DiscountPercent)
		require.Equal(t, expected.rate, row.CNYPerUSD)
		require.Equal(t, expected.buffer, row.FXBuffer)
		require.Equal(t, expected.policy, row.PricePolicy)
		require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, expected.contract), row.MediaPriceContractJSON)
		require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, row.MediaPriceContractJSON), row.MediaPriceContractJSON)
	}
	require.Equal(t, 5, matched)
}

func TestZTAPIMediaPriceContractManifestRejectsMutatedEvidence(t *testing.T) {
	mutations := map[string]func(*ZTAPIQuotationRow){
		"currency": func(row *ZTAPIQuotationRow) { row.Currency = "EUR" },
		"discount": func(row *ZTAPIQuotationRow) { row.DiscountPercent = "94" },
		"exchange rate": func(row *ZTAPIQuotationRow) {
			if row.Currency == "CNY" {
				row.CNYPerUSD = "7.1"
			} else {
				row.Currency = "CNY"
			}
		},
		"valid but unquoted source cell": func(row *ZTAPIQuotationRow) {
			var contract ZTAPIMediaPriceContract
			require.NoError(t, common.UnmarshalJsonStr(row.MediaPriceContractJSON, &contract))
			for dimension := range contract.Rules[0].SourceCells {
				contract.Rules[0].SourceCells[dimension] = "Z99"
				break
			}
			raw, err := common.Marshal(contract)
			require.NoError(t, err)
			row.MediaPriceContractJSON = mustCanonicalZTAPIMediaPriceContract(t, string(raw))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var manifest ztapiQuotationManifest
			require.NoError(t, common.Unmarshal(ztapiQuotationManifestJSON, &manifest))
			var row *ZTAPIQuotationRow
			for entryIndex := range manifest.Entries {
				for rowIndex := range manifest.Entries[entryIndex].QuoteRows {
					candidate := &manifest.Entries[entryIndex].QuoteRows[rowIndex]
					if candidate.MediaPriceContractJSON != "" {
						row = candidate
						break
					}
				}
				if row != nil {
					break
				}
			}
			require.NotNil(t, row)
			mutate(row)
			raw, err := common.Marshal(manifest)
			require.NoError(t, err)
			_, err = parseZTAPIQuotationManifest(raw)
			require.ErrorIs(t, err, ErrZTAPIQuotationManifestInvalid)
		})
	}
}

func TestZTAPIMediaPriceContractImportCanonicalizesAndSnapshotCopies(t *testing.T) {
	db := setupZTAPIModelEvidenceWriteTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelPublicationSnapshot{}))
	config := ZTAPIModelConfig{SourceModel: "unpublished-media-candidate", EnabledGroups: "[]", Version: 2}
	require.NoError(t, db.Create(&config).Error)

	var contract ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(geminiImageContractForTest(t), &contract))
	contract.Rules[0], contract.Rules[1] = contract.Rules[1], contract.Rules[0]
	uncanonical, err := common.Marshal(contract)
	require.NoError(t, err)
	source := validZTAPIPriceSourceForTest()
	source.ModelConfigID = config.ID
	source.SourceModel = config.SourceModel
	source.PricePolicy = string(ZTAPIPricePolicyEnterprise20Margin)
	source.MediaPriceContractJSON = string(uncanonical)

	_, persisted, _, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: config.Version, OperatorID: 7, Reason: "offline media price evidence",
	})
	require.NoError(t, err)
	require.Equal(t, mustCanonicalZTAPIMediaPriceContract(t, source.MediaPriceContractJSON), persisted.MediaPriceContractJSON)

	snapshot := ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version + 1,
		SourceModel: config.SourceModel, PublicName: "zt-unpublished-media-candidate",
		Protocol: ZTAPIProtocolOpenAICompatible, ProviderFamily: ZTAPIProviderOther,
		EnabledGroups: `[]`, AllowedChannelIDs: `[]`, PriceSourceID: persisted.ID,
		VerificationIDs: `[]`, IdentityUpdatedAt: 1, CreatedAt: 2,
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.Equal(t, persisted.PricePolicy, snapshot.PricePolicy)
	require.Equal(t, persisted.MediaPriceContractJSON, snapshot.MediaPriceContractJSON)

	invalid := source
	invalid.ID = 0
	invalid.MediaPriceContractJSON = strings.TrimSuffix(geminiImageContractForTest(t), "}") + `,"unknown":true}`
	_, _, _, err = ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: invalid, ExpectedVersion: config.Version + 1, OperatorID: 7, Reason: "reject typoed media price evidence",
	})
	require.Error(t, err)

	var incomplete ZTAPIMediaPriceContract
	require.NoError(t, common.UnmarshalJsonStr(geminiImageContractForTest(t), &incomplete))
	incomplete.Rules = incomplete.Rules[:1]
	incompleteRaw, err := common.Marshal(incomplete)
	require.NoError(t, err)
	invalid.MediaPriceContractJSON = string(incompleteRaw)
	_, _, _, err = ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{
		Source: invalid, ExpectedVersion: config.Version + 1, OperatorID: 7, Reason: "reject incomplete media pricing matrix",
	})
	require.Error(t, err)
}

func TestZTAPIMediaPriceContractMigratesLegacyTextRowsWithoutChangingPublicPayload(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-media-price.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIModelConfig{}, &ZTAPIModelPriceSource{}, &ZTAPIModelPublicationSnapshot{}))
	require.NoError(t, MigrateZTAPIHealth(db))
	previous := DB
	DB = db
	InvalidateZTAPIAliasCache()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		DB = previous
		InvalidateZTAPIAliasCache()
		require.NoError(t, sqlDB.Close())
	})
	config := seedZTAPIPublicCatalogRecord(t, db, "gpt-5.5", "zt-gpt-5.5", ZTAPIProviderOpenAI, ZTAPIProtocolOpenAICompatible,
		[]string{ZTAPIBillingDimensionInputTokens, ZTAPIBillingDimensionOutputTokens})
	before, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, before, 1)

	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_price_sources RENAME COLUMN media_price_contract_json TO legacy_media_price_contract_json").Error)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_publication_snapshots RENAME COLUMN media_price_contract_json TO legacy_media_price_contract_json").Error)
	require.NoError(t, migrateZTAPIMediaPricingColumns(db))
	InvalidateZTAPIAliasCache()

	after, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Equal(t, before, after)
	var source ZTAPIModelPriceSource
	require.NoError(t, db.Where("model_config_id = ?", config.ID).First(&source).Error)
	require.Empty(t, source.MediaPriceContractJSON)
	var snapshot ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&snapshot, config.PublicationSnapshotID).Error)
	require.Empty(t, snapshot.MediaPriceContractJSON)
}

func TestZTAPIImageProtocolContractColumnsMigrateLegacyEvidenceTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-image-protocol.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelPriceSource{}, &ZTAPIModelVerification{}, &ZTAPIModelPublicationSnapshot{}))
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_verifications RENAME COLUMN image_protocol_contract_json TO legacy_image_protocol_contract_json").Error)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_publication_snapshots RENAME COLUMN image_protocol_contract_json TO legacy_image_protocol_contract_json").Error)
	require.False(t, db.Migrator().HasColumn(&ZTAPIModelVerification{}, "ImageProtocolContractJSON"))
	require.False(t, db.Migrator().HasColumn(&ZTAPIModelPublicationSnapshot{}, "ImageProtocolContractJSON"))

	require.NoError(t, migrateZTAPIMediaPricingColumns(db))
	require.True(t, db.Migrator().HasColumn(&ZTAPIModelVerification{}, "ImageProtocolContractJSON"))
	require.True(t, db.Migrator().HasColumn(&ZTAPIModelPublicationSnapshot{}, "ImageProtocolContractJSON"))
}

func TestZTAPIVideoProtocolContractColumnsMigrateLegacyEvidenceTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/legacy-video-protocol.db"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.AutoMigrate(&ZTAPIModelPriceSource{}, &ZTAPIModelVerification{}, &ZTAPIModelPublicationSnapshot{}))
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_verifications RENAME COLUMN video_protocol_contract_json TO legacy_video_protocol_contract_json").Error)
	require.NoError(t, db.Exec("ALTER TABLE ztapi_model_publication_snapshots RENAME COLUMN video_protocol_contract_json TO legacy_video_protocol_contract_json").Error)
	require.False(t, db.Migrator().HasColumn(&ZTAPIModelVerification{}, "VideoProtocolContractJSON"))
	require.False(t, db.Migrator().HasColumn(&ZTAPIModelPublicationSnapshot{}, "VideoProtocolContractJSON"))

	require.NoError(t, migrateZTAPIMediaPricingColumns(db))
	require.True(t, db.Migrator().HasColumn(&ZTAPIModelVerification{}, "VideoProtocolContractJSON"))
	require.True(t, db.Migrator().HasColumn(&ZTAPIModelPublicationSnapshot{}, "VideoProtocolContractJSON"))
}

type ztapiMediaMigrationRaceMigrator struct {
	hasColumnResults []bool
	addErr           error
	addCalls         int
	columnChecks     []string
}

func (m *ztapiMediaMigrationRaceMigrator) HasTable(any) bool { return true }

func (m *ztapiMediaMigrationRaceMigrator) HasColumn(_ any, field string) bool {
	m.columnChecks = append(m.columnChecks, field)
	if len(m.hasColumnResults) == 0 {
		return false
	}
	result := m.hasColumnResults[0]
	m.hasColumnResults = m.hasColumnResults[1:]
	return result
}

func (m *ztapiMediaMigrationRaceMigrator) AddColumn(_ any, _ string) error {
	m.addCalls++
	return m.addErr
}

func TestZTAPIMediaPricingColumnMigrationHandlesConcurrentAddWithoutSwallowingErrors(t *testing.T) {
	concurrentAdd := errors.New("duplicate column name: media_price_contract_json")
	race := &ztapiMediaMigrationRaceMigrator{
		hasColumnResults: []bool{false, true, true, true, true, true, true},
		addErr:           concurrentAdd,
	}
	require.NoError(t, migrateZTAPIMediaPricingColumnsWithMigrator(race))
	require.Equal(t, 1, race.addCalls)
	require.Equal(t, []string{"MediaPriceContractJSON", "MediaPriceContractJSON", "MediaPriceContractJSON", "ImageProtocolContractJSON", "ImageProtocolContractJSON", "VideoProtocolContractJSON", "VideoProtocolContractJSON"}, race.columnChecks)

	imageRace := &ztapiMediaMigrationRaceMigrator{
		hasColumnResults: []bool{true, true, false, true, true, true, true},
		addErr:           errors.New("duplicate column name: image_protocol_contract_json"),
	}
	require.NoError(t, migrateZTAPIMediaPricingColumnsWithMigrator(imageRace))
	require.Equal(t, 1, imageRace.addCalls)
	require.Equal(t, []string{"MediaPriceContractJSON", "MediaPriceContractJSON", "ImageProtocolContractJSON", "ImageProtocolContractJSON", "ImageProtocolContractJSON", "VideoProtocolContractJSON", "VideoProtocolContractJSON"}, imageRace.columnChecks)

	unrelated := errors.New("permission denied")
	failure := &ztapiMediaMigrationRaceMigrator{
		hasColumnResults: []bool{false, false},
		addErr:           unrelated,
	}
	require.ErrorIs(t, migrateZTAPIMediaPricingColumnsWithMigrator(failure), unrelated)
	require.Equal(t, 1, failure.addCalls)
}

func TestZTAPIExistingPublicationSnapshotsUnchanged(t *testing.T) {
	fixtureRaw, err := os.ReadFile("testdata/ztapi_public_pricing_baseline_v1.json")
	require.NoError(t, err)
	var fixtureRecords []Pricing
	require.NoError(t, common.Unmarshal(fixtureRaw, &fixtureRecords))
	require.Len(t, fixtureRecords, 39)

	entries, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	quoted := make(map[string]ZTAPIQuotationEntry, len(entries))
	for _, entry := range entries {
		if entry.PublicName != "" {
			quoted[entry.PublicName] = entry
		}
	}
	publications := make([]ZTAPIRuntimePublication, 0, len(fixtureRecords))
	textCount := 0
	embeddingCount := 0
	for _, record := range fixtureRecords {
		entry, ok := quoted[record.ModelName]
		require.Truef(t, ok, "missing quotation identity for %s", record.ModelName)
		require.NotNil(t, record.CacheRatio)
		require.NotNil(t, record.CreateCacheRatio)
		require.NotNil(t, record.ImageRatio)
		require.NotNil(t, record.AudioRatio)
		require.NotNil(t, record.AudioCompletionRatio)
		snapshotID, err := strconv.ParseInt(strings.TrimPrefix(record.PricingVersion, "ztapi-snapshot-"), 10, 64)
		require.NoError(t, err)
		inputPrice := record.ModelRatio * 2
		outputPrice := inputPrice * record.CompletionRatio
		modality := ZTAPIModelModality(entry.SourceModel)
		switch modality {
		case ZTAPIModalityText:
			textCount++
		case ZTAPIModalityEmbedding:
			embeddingCount++
		}
		publications = append(publications, ZTAPIRuntimePublication{
			Modality: modality, SnapshotID: snapshotID,
			SourceModel: entry.SourceModel, PublicName: entry.PublicName,
			ProviderFamily: entry.ProviderFamily, Protocol: entry.Protocol,
			Groups: record.EnableGroup, InputPricePerMillion: inputPrice, OutputPricePerMillion: outputPrice,
			CacheReadRatio: *record.CacheRatio, CacheCreationRatio: *record.CreateCacheRatio,
			ImageRatio: *record.ImageRatio, AudioRatio: *record.AudioRatio,
			AudioCompletionRatio: *record.AudioCompletionRatio,
			BillingDimensions:    record.BillingDimensions, SaleUSD: record.SaleUSD,
			InputPriceDisplay: record.InputPricePerMillion, OutputPriceDisplay: record.OutputPricePerMillion,
		})
	}
	require.Equal(t, 37, textCount)
	require.Equal(t, 2, embeddingCount)
	sort.Slice(publications, func(i, j int) bool {
		if publications[i].ProviderFamily == publications[j].ProviderFamily {
			return publications[i].PublicName < publications[j].PublicName
		}
		return publications[i].ProviderFamily < publications[j].ProviderFamily
	})
	current := projectZTAPIPublicPricing(publications)
	require.Equal(t, fixtureRecords, current)

	tuples := make([][]string, 0, len(current))
	for _, record := range current {
		encoded, err := common.Marshal(record)
		require.NoError(t, err)
		var canonical map[string]any
		require.NoError(t, common.Unmarshal(encoded, &canonical))
		encoded, err = common.Marshal(canonical)
		require.NoError(t, err)
		hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
		tuples = append(tuples, []string{record.PricingVersion, hash})
	}
	sort.Slice(tuples, func(i, j int) bool { return tuples[i][0] < tuples[j][0] })
	encoded, err := common.Marshal(tuples)
	require.NoError(t, err)
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	const frozen = "e1164b1db818e814ae1398836680972fe132956531cbf9db42e17b8844f2b6ab"
	require.Equal(t, frozen, digest)
}
