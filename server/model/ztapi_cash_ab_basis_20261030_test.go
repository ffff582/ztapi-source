package model

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func cashABBasisFX20261030() ZTAPIABFXParams {
	return ZTAPIABFXParams{
		Mode: ZTAPIFXModePlatformV1, PlatformRate: decimal.RequireFromString("6.63"),
		UpstreamUSDRate: decimal.RequireFromString("6.7411"),
	}
}

func cashABBasisSource20261030(t *testing.T, name string, fx ZTAPIABFXParams) ZTAPIModelPriceSource {
	t.Helper()
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSourceWithFX(quote, name, 11, 7, 1_790_640_000, fx)
	require.NoError(t, err)
	source.ID = 123
	require.NoError(t, validateZTAPIABPriceSource(&source))
	return source
}

func TestCashABBasis20261030CanonicalIdentitiesAndCNYCosts(t *testing.T) {
	for _, tc := range []struct {
		name, source, public, cell string
		conditions                 [][]string
		costs                      []map[string]string
	}{
		{"GPT 4.1 Mini", "gpt-4.1-mini", "zt-gpt-4.1-mini", "D95", [][]string{{}}, []map[string]string{{
			"input_tokens": "2.1032232", "cache_read": "0.5258058", "output_tokens": "8.4128928",
		}}},
		{"GPT 4.1 Nano", "gpt-4.1-nano", "zt-gpt-4.1-nano", "D94", [][]string{{}}, []map[string]string{{
			"input_tokens": "0.5258058", "cache_read": "0.13145145", "output_tokens": "2.1032232",
		}}},
		{"GPT 4o Mini", "gpt-4o-mini", "zt-gpt-4o-mini-2024-07-18", "D97", [][]string{{}}, []map[string]string{{
			"input_tokens": "0.7887087", "cache_read": "0.39435435", "output_tokens": "3.1548348",
		}}},
		{"GPT 5 Nano", "gpt-5-nano", "zt-gpt-5-nano", "D92", [][]string{{}}, []map[string]string{{
			"input_tokens": "0.2629029", "cache_read": "0.02629029", "output_tokens": "2.1032232",
		}}},
		{"Gemini 3.1 Flash Lite Preview", "gemini-3.1-flash-lite-preview", "zt-gemini-3.1-flash-lite-preview", "D111",
			[][]string{{"\u8f93\u5165\u7c7b\u578b=\u6587\u672c/\u56fe\u50cf/\u89c6\u9891"}, {"\u8f93\u5165\u7c7b\u578b=\u97f3\u9891"}}, []map[string]string{
				{"input_tokens": "1.3819255", "cache_read": "0.13819255", "output_tokens": "8.291553"},
				{"input_tokens": "2.763851", "cache_read": "0.2763851", "output_tokens": "8.291553"},
			}},
		{"Gemini 3.5 Flash", "gemini-3.5-flash", "zt-gemini-3.5-flash", "D113",
			[][]string{{"\u8f93\u5165\u957f\u5ea6\u22641M"}}, []map[string]string{{
				"input_tokens": "8.291553", "cache_read": "0.8291553", "output_tokens": "49.749318",
			}}},
		{"Text Embedding 3 Small", "text-embedding-3-small", "zt-text-embedding-3-small", "D99", [][]string{{}}, []map[string]string{{
			"input_tokens": "0.10516116",
		}}},
		{"Text Embedding Ada 002", "text-embedding-ada-002", "zt-text-embedding-ada-002", "D98", [][]string{{}}, []map[string]string{{
			"input_tokens": "0.5258058",
		}}},
		{"Claude Fable 5", "claude-fable-5", "zt-claude-fable-5", "D127", [][]string{{}}, []map[string]string{{
			"input_tokens": "43.81715", "cache_write_5m": "54.7714375", "cache_write_1h": "87.6343",
			"cache_read": "4.381715", "output_tokens": "219.08575",
		}}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			source := cashABBasisSource20261030(t, tc.name, cashABBasisFX20261030())
			require.Equal(t, tc.source, source.SourceModel)
			require.Equal(t, tc.cell, source.QuotationCell)
			bridge, err := CurrentZTAPIABQuotationIdentityBridge()
			require.NoError(t, err)
			var identity ZTAPIABModelIdentity
			for _, claim := range bridge.Mapped {
				if claim.SourceModel == source.SourceModel {
					identity = claim
				}
			}
			require.Equal(t, tc.name, identity.ModelName)
			require.Equal(t, tc.public, identity.PublicName)
			if tc.source == "claude-fable-5" {
				require.Equal(t, "pool_30_margin", source.PricePolicy)
				require.Equal(t, "pool", source.ResourceType)
				require.Equal(t, "B", source.QuotationGrade)
			} else {
				require.Equal(t, "enterprise_20_margin", source.PricePolicy)
				require.Equal(t, "enterprise", source.ResourceType)
				require.Equal(t, "A", source.QuotationGrade)
			}
			before := source
			tiers, err := ztapiCashABSourceTiers20261030(&source)
			require.NoError(t, err)
			require.Equal(t, before, source, "the helper must not normalize or mutate its caller")
			require.Len(t, tiers, len(tc.costs))
			maximum := map[string]decimal.Decimal{}
			for tier, costs := range tc.costs {
				require.Equal(t, tc.conditions[tier], tiers[tier].Conditions)
				require.Len(t, tiers[tier].CostCNY, len(costs), "do not invent unquoted zero dimensions")
				for dim, raw := range costs {
					want := decimal.RequireFromString(raw)
					got, present := tiers[tier].CostCNY[dim]
					require.True(t, present, dim)
					require.True(t, want.Equal(got), "%s tier %d: got %s, want %s CNY", dim, tier, got, want)
					if want.GreaterThan(maximum[dim]) {
						maximum[dim] = want
					}
				}
			}
			for dim, raw := range ztapiPriceSourceValues(&source) {
				require.True(t, decimal.RequireFromString(raw).Equal(maximum[dim]), "max cost including unused zeros: %s", dim)
			}
			if strings.HasPrefix(tc.source, "text-embedding-") {
				require.Equal(t, ZTAPIModalityEmbedding, identity.Modality)
				require.Empty(t, source.TokenPriceRulesJSON)
				require.Equal(t, `["input_tokens"]`, source.BillingDimensions)
				require.NotContains(t, tiers[0].CostCNY, "output_tokens")
			}
		})
	}
}

func TestCashABBasis20261030FrozenFXAndTenPlaceRounding(t *testing.T) {
	fx := cashABBasisFX20261030()
	fx.PlatformRate = decimal.RequireFromString("5.123456789")
	fx.UpstreamUSDRate = decimal.RequireFromString("8.987654321")
	source := cashABBasisSource20261030(t, "GPT 4.1 Mini", fx)
	tiers, err := ztapiCashABSourceTiers20261030(&source)
	require.NoError(t, err)
	require.Equal(t, "2.8041481482", tiers[0].CostCNY["input_tokens"].StringFixed(10))
	require.Equal(t, "0.7010370370", tiers[0].CostCNY["cache_read"].StringFixed(10))
	require.Equal(t, "11.2165925926", tiers[0].CostCNY["output_tokens"].StringFixed(10))

	fx.PlatformRate = decimal.RequireFromString("8.75")
	other := cashABBasisSource20261030(t, "GPT 4.1 Mini", fx)
	otherTiers, err := ztapiCashABSourceTiers20261030(&other)
	require.NoError(t, err)
	require.Equal(t, tiers, otherTiers, "procurement CNY is independent of platform unit conversion")
	require.NotEqual(t, source.TokenPriceRulesJSON, other.TokenPriceRulesJSON)
}

func TestCashABBasis20261030RejectsTamperedEvidence(t *testing.T) {
	base := cashABBasisSource20261030(t, "Gemini 3.1 Flash Lite Preview", cashABBasisFX20261030())
	for name, mutate := range map[string]func(*ZTAPIModelPriceSource){
		"cost":             func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "0.01" },
		"lower_tier_max":   func(s *ZTAPIModelPriceSource) { s.InputPerMillion = "1.3819255" },
		"cache_cost":       func(s *ZTAPIModelPriceSource) { s.CacheReadPerMillion = "0.01" },
		"unused_zero":      func(s *ZTAPIModelPriceSource) { s.CacheWritePerMillion = "0.01" },
		"malformed_cost":   func(s *ZTAPIModelPriceSource) { s.OutputPerMillion = "bad" },
		"negative_cost":    func(s *ZTAPIModelPriceSource) { s.AudioUnitCost = "-1" },
		"checksum":         func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = strings.Repeat("0", 64) },
		"other_workbook":   func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = ZTAPIQuotation929SHA256 },
		"model":            func(s *ZTAPIModelPriceSource) { s.SourceModel = "gpt-4.1-mini" },
		"model_whitespace": func(s *ZTAPIModelPriceSource) { s.SourceModel += " " },
		"resource":         func(s *ZTAPIModelPriceSource) { s.ResourceType = "pool" },
		"grade":            func(s *ZTAPIModelPriceSource) { s.QuotationGrade = "B" },
		"spend_tier":       func(s *ZTAPIModelPriceSource) { s.SpendTier = "B" },
		"policy":           func(s *ZTAPIModelPriceSource) { s.PricePolicy = "pool_30_margin" },
		"quotation_cell":   func(s *ZTAPIModelPriceSource) { s.QuotationCell = "D113" },
		"official_cell":    func(s *ZTAPIModelPriceSource) { s.OfficialPriceCell = "F113" },
		"model_code":       func(s *ZTAPIModelPriceSource) { s.QuotationModelCode = "zq-g35-f" },
		"currency":         func(s *ZTAPIModelPriceSource) { s.Currency = "USD" },
		"legacy_fx":        func(s *ZTAPIModelPriceSource) { s.FXMode = "" },
		"unknown_fx":       func(s *ZTAPIModelPriceSource) { s.FXMode = "other" },
		"upstream_fx":      func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "7.1" },
		"missing_upstream": func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "0" },
		"bad_upstream":     func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "bad" },
		"platform_fx":      func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit = "7.1" },
		"cny_fx":           func(s *ZTAPIModelPriceSource) { s.CNYPerUSD = "7.1" },
		"paired_fx": func(s *ZTAPIModelPriceSource) {
			s.PlatformCNYPerUnit, s.CNYPerUSD = "7.1", "7.1"
		},
		"zero_platform": func(s *ZTAPIModelPriceSource) {
			s.PlatformCNYPerUnit, s.CNYPerUSD = "0", "0"
		},
		"missing_tiers": func(s *ZTAPIModelPriceSource) { s.TokenPriceRulesJSON = "[]" },
		"broken_tiers":  func(s *ZTAPIModelPriceSource) { s.TokenPriceRulesJSON = "{" },
		"missing_dimension": func(s *ZTAPIModelPriceSource) {
			s.BillingDimensions = `["input_tokens","output_tokens"]`
		},
		"extra_dimension": func(s *ZTAPIModelPriceSource) {
			s.BillingDimensions = `["audio","cache_read","input_tokens","output_tokens"]`
		},
		"duplicate_dimension": func(s *ZTAPIModelPriceSource) {
			s.BillingDimensions = `["cache_read","input_tokens","input_tokens","output_tokens"]`
		},
		"cash_parent": func(s *ZTAPIModelPriceSource) { s.CashMarginBaseSourceID = 12 },
		"media":       func(s *ZTAPIModelPriceSource) { s.MediaPriceContractJSON = `{}` },
		"tier_sale": func(s *ZTAPIModelPriceSource) {
			cashABBasisMutateRules20261030(t, s, func(rules []ztapiABFrozenSaleRule) { rules[0].Sale["input_tokens"] = "0.01" })
		},
		"tier_condition": func(s *ZTAPIModelPriceSource) {
			cashABBasisMutateRules20261030(t, s, func(rules []ztapiABFrozenSaleRule) { rules[1].Conditions[0] = "audio" })
		},
		"tier_order": func(s *ZTAPIModelPriceSource) {
			cashABBasisMutateRules20261030(t, s, func(rules []ztapiABFrozenSaleRule) { rules[0], rules[1] = rules[1], rules[0] })
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			before := bad
			tiers, err := ztapiCashABSourceTiers20261030(&bad)
			require.Error(t, err)
			require.Nil(t, tiers, "never return a partial procurement basis")
			require.Equal(t, before, bad)
		})
	}
	tiers, err := ztapiCashABSourceTiers20261030(nil)
	require.Error(t, err)
	require.Nil(t, tiers)
}

func cashABBasisMutateRules20261030(t *testing.T, source *ZTAPIModelPriceSource, mutate func([]ztapiABFrozenSaleRule)) {
	t.Helper()
	var rules []ztapiABFrozenSaleRule
	require.NoError(t, common.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	mutate(rules)
	raw, err := common.Marshal(rules)
	require.NoError(t, err)
	source.TokenPriceRulesJSON = string(raw)
}

func TestCashABBasis20261030RejectsUnapprovedModelsAndIdentityAliases(t *testing.T) {
	for _, name := range []string{"GPT 5 Mini", "GPT 4.1", "Gemini 3 Flash Preview", "Claude Sonnet 5", "GLM 5.2"} {
		t.Run(name, func(t *testing.T) {
			source := cashABBasisSource20261030(t, name, cashABBasisFX20261030())
			tiers, err := ztapiCashABSourceTiers20261030(&source)
			require.Error(t, err)
			require.Nil(t, tiers)
		})
	}
	source := cashABBasisSource20261030(t, "GPT 4o Mini", cashABBasisFX20261030())
	for _, alias := range []string{"gpt-4o-mini-2024-07-18", "zt-gpt-4o-mini-2024-07-18", "GPT 4o Mini", "GPT-4o-Mini", "gpt-4o-mini "} {
		t.Run(alias, func(t *testing.T) {
			bad := source
			bad.SourceModel = alias
			tiers, err := ztapiCashABSourceTiers20261030(&bad)
			require.Error(t, err, "the dated public name is not a frozen source identity")
			require.Nil(t, tiers)
		})
	}
}

func TestCashABBasis20261030EmbeddingRejectsInventedOutputAndRules(t *testing.T) {
	for _, name := range []string{"Text Embedding 3 Small", "Text Embedding Ada 002"} {
		base := cashABBasisSource20261030(t, name, cashABBasisFX20261030())
		for label, mutate := range map[string]func(*ZTAPIModelPriceSource){
			"output": func(s *ZTAPIModelPriceSource) { s.OutputPerMillion = "0.001" },
			"cache":  func(s *ZTAPIModelPriceSource) { s.CacheReadPerMillion = "0.001" },
			"dimension": func(s *ZTAPIModelPriceSource) {
				s.BillingDimensions = `["input_tokens","output_tokens"]`
			},
			"rules": func(s *ZTAPIModelPriceSource) {
				s.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"input_tokens":"1"}}]`
			},
		} {
			t.Run(name+"/"+label, func(t *testing.T) {
				bad := base
				mutate(&bad)
				tiers, err := ztapiCashABSourceTiers20261030(&bad)
				require.Error(t, err)
				require.Nil(t, tiers)
			})
		}
	}
}

func TestCashABBasis20261030FableRejectsEnterpriseAndCollapsedTTLs(t *testing.T) {
	base := cashABBasisSource20261030(t, "Claude Fable 5", cashABBasisFX20261030())
	for name, mutate := range map[string]func(*ZTAPIModelPriceSource){
		"enterprise": func(s *ZTAPIModelPriceSource) {
			s.ResourceType, s.QuotationGrade, s.SpendTier = "enterprise", "A", "A"
			s.PricePolicy, s.QuotationCell = "enterprise_20_margin", "D135"
		},
		"official_pool": func(s *ZTAPIModelPriceSource) { s.PricePolicy = "pool_official_78_20260929" },
		"generic_write": func(s *ZTAPIModelPriceSource) { s.CacheWritePerMillion = s.CacheWrite5mPerMillion },
		"collapse_ttl":  func(s *ZTAPIModelPriceSource) { s.CacheWrite1hPerMillion = s.CacheWrite5mPerMillion },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			tiers, err := ztapiCashABSourceTiers20261030(&bad)
			require.Error(t, err)
			require.Nil(t, tiers)
		})
	}
}

func TestCashABBasis20261030ReturnsIndependentCanonicalData(t *testing.T) {
	source := cashABBasisSource20261030(t, "Gemini 3.1 Flash Lite Preview", cashABBasisFX20261030())
	before := source
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	tiers, err := ztapiCashABSourceTiers20261030(&source)
	require.NoError(t, err)
	tiers[0].Conditions[0] = "changed"
	tiers[0].CostCNY["cache_read"] = decimal.Zero
	tiers[0].CostCNY["request"] = decimal.NewFromInt(1)
	require.Equal(t, "0.2763851", tiers[1].CostCNY["cache_read"].String())
	again, err := ztapiCashABSourceTiers20261030(&source)
	require.NoError(t, err)
	require.Equal(t, "\u8f93\u5165\u7c7b\u578b=\u6587\u672c/\u56fe\u50cf/\u89c6\u9891", again[0].Conditions[0])
	require.Equal(t, "0.13819255", again[0].CostCNY["cache_read"].String())
	require.NotContains(t, again[0].CostCNY, "request")
	require.Equal(t, before, source)
	after, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	require.Equal(t, quote, after, "the embedded manifest must remain immutable")
}

func TestCashABBasis20261030RebuildIgnoresDerivedRulesAndFreezesEachSourceFX(t *testing.T) {
	for _, rate := range []string{"6.7138", "6.7147"} {
		for _, name := range []string{
			"GPT 4.1 Mini", "GPT 4.1 Nano", "GPT 4o Mini", "GPT 5 Nano",
			"Gemini 3.1 Flash Lite Preview", "Gemini 3.5 Flash",
			"Text Embedding 3 Small", "Text Embedding Ada 002", "Claude Fable 5",
		} {
			t.Run(rate+"/"+name, func(t *testing.T) {
				fx := cashABBasisFX20261030()
				fx.UpstreamUSDRate = decimal.RequireFromString(rate)
				original := cashABBasisSource20261030(t, name, fx)
				clone := original
				clone.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"input_tokens":"999"}}]`
				before := clone
				rebuilt, err := ztapiCashRebuildABBase20261030(&clone)
				require.NoError(t, err)
				require.Equal(t, before, clone)
				require.Empty(t, ztapiABPriceSourceEvidenceMismatch(&rebuilt, &original))
				require.NoError(t, validateZTAPIABPriceSource(&rebuilt))
				require.Equal(t, rate, decimal.RequireFromString(rebuilt.UpstreamCNYPerUSD).String())
				if name == "GPT 5 Nano" {
					want := map[string]string{"6.7138": "0.2618382000", "6.7147": "0.2618733000"}
					require.Equal(t, want[rate], rebuilt.InputPerMillion)
				}
				if name == "Claude Fable 5" {
					require.Equal(t, "pool_30_margin", rebuilt.PricePolicy)
					require.Equal(t, "B", rebuilt.QuotationGrade)
				} else {
					require.Equal(t, "enterprise_20_margin", rebuilt.PricePolicy)
					require.Equal(t, "A", rebuilt.QuotationGrade)
				}
				_, err = ztapiCashABSourceTiers20261030(&clone)
				require.Error(t, err, "reconstruction must not weaken strict original-source validation")
				clone.TokenPriceRulesJSON = rebuilt.TokenPriceRulesJSON
				_, err = ztapiCashABSourceTiers20261030(&clone)
				require.NoError(t, err)
			})
		}
	}
}

func TestCashABBasis20261030RebuildIsNotProcurementAuthorization(t *testing.T) {
	original := cashABBasisSource20261030(t, "GPT 4.1 Mini", cashABBasisFX20261030())
	clone := original
	clone.TokenPriceRulesJSON, clone.InputPerMillion = "{", "0.01"
	clone.PricePolicy, clone.ResourceType, clone.QuotationGrade = "cash_pool_30_20261030", "pool", "B"
	before := clone
	rebuilt, err := ztapiCashRebuildABBase20261030(&clone)
	require.NoError(t, err)
	require.Empty(t, ztapiABPriceSourceEvidenceMismatch(&rebuilt, &original))
	require.Equal(t, before, clone)
	clone.TokenPriceRulesJSON = rebuilt.TokenPriceRulesJSON
	tiers, err := ztapiCashABSourceTiers20261030(&clone)
	require.Error(t, err, "copying canonical rules does not authenticate other procurement fields")
	require.Nil(t, tiers)
}

func TestCashABBasis20261030RebuildRejectsMissingIdentityFXAndProvenance(t *testing.T) {
	base := cashABBasisSource20261030(t, "GPT 4.1 Mini", cashABBasisFX20261030())
	for name, mutate := range map[string]func(*ZTAPIModelPriceSource){
		"model":          func(s *ZTAPIModelPriceSource) { s.SourceModel = "unknown" },
		"unapproved":     func(s *ZTAPIModelPriceSource) { s.SourceModel = "gpt-5-mini" },
		"dated_alias":    func(s *ZTAPIModelPriceSource) { s.SourceModel = "gpt-4o-mini-2024-07-18" },
		"checksum":       func(s *ZTAPIModelPriceSource) { s.SourceDocumentChecksum = strings.Repeat("0", 64) },
		"currency":       func(s *ZTAPIModelPriceSource) { s.Currency = "USD" },
		"legacy_fx":      func(s *ZTAPIModelPriceSource) { s.FXMode = "" },
		"missing_fx":     func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "" },
		"zero_fx":        func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "0" },
		"negative_fx":    func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "-6.7" },
		"bad_fx":         func(s *ZTAPIModelPriceSource) { s.UpstreamCNYPerUSD = "bad" },
		"mismatched_fx":  func(s *ZTAPIModelPriceSource) { s.CNYPerUSD = "7.2" },
		"zero_platform":  func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit = "0" },
		"bad_platform":   func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit = "bad" },
		"out_of_range":   func(s *ZTAPIModelPriceSource) { s.PlatformCNYPerUnit, s.CNYPerUSD = "4.9", "4.9" },
		"missing_config": func(s *ZTAPIModelPriceSource) { s.ModelConfigID = 0 },
		"missing_time":   func(s *ZTAPIModelPriceSource) { s.QuotationEffectiveAt = 0 },
		"missing_actor":  func(s *ZTAPIModelPriceSource) { s.OperatorID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			before := bad
			rebuilt, err := ztapiCashRebuildABBase20261030(&bad)
			require.Error(t, err)
			require.Equal(t, ZTAPIModelPriceSource{}, rebuilt)
			require.Equal(t, before, bad)
		})
	}
	rebuilt, err := ztapiCashRebuildABBase20261030(nil)
	require.Error(t, err)
	require.Equal(t, ZTAPIModelPriceSource{}, rebuilt)
}
