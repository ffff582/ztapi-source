package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const ztapiFableCashModeForTest = "fable_cash_floor_20261030"

type ztapiFableCashRuleForTest struct {
	Conditions []string          `json:"conditions"`
	Sale       map[string]string `json:"sale"`
}

func ztapiFableCashPublicationForTest(t *testing.T, price string) ZTAPIRuntimePublication {
	t.Helper()
	publication := ZTAPIRuntimePublication{
		SourceModel: "claude-fable-5", PublicName: "zt-claude-fable-5",
		ProviderFamily: ZTAPIProviderAnthropic, Protocol: ZTAPIProtocolOpenAICompatible,
		PriceSourceID: 7, PricePolicy: "cash_pool_30_20261030",
		BillingDimensions: []string{"input_tokens", "cache_write", "cache_write_5m", "cache_read", "cache_write_1h", "cache_write", "cache_write_5m", "output_tokens"},
		SaleUSD:           map[string]string{"input_tokens": "2", "output_tokens": "10", "cache_read": "0.2", "cache_write_5m": price, "cache_write_1h": price},
		OfficialUSD:       map[string]string{"input_tokens": "5", "cache_write_5m": "6.25", "cache_write_1h": "10"},
		TokenOfficialRules: []ZTAPIPublicTokenPriceRule{{Conditions: []string{},
			OfficialUSD: map[string]string{"input_tokens": "5", "cache_write_5m": "6.25", "cache_write_1h": "10"}}},
	}
	rules := []ztapiFableCashRuleForTest{
		{Conditions: []string{"context<=200000"}, Sale: copyZTAPIStringMap(publication.SaleUSD)},
		{Conditions: []string{"context>200000"}, Sale: copyZTAPIStringMap(publication.SaleUSD)},
	}
	rules[1].Sale["input_tokens"] = "4"
	raw, err := common.Marshal(rules)
	require.NoError(t, err)
	publication.TokenPriceRulesJSON = string(raw)
	return publication
}

func TestZTAPIFableCashCacheProjectionUsesFrozenRetail(t *testing.T) {
	for _, price := range []string{"7.1250000011", "23.4567890123"} {
		t.Run(price, func(t *testing.T) {
			publication := ztapiFableCashPublicationForTest(t, price)
			frozenSale := copyZTAPIStringMap(publication.SaleUSD)
			frozenOfficial := copyZTAPIStringMap(publication.OfficialUSD)
			frozenRules := publication.TokenPriceRulesJSON
			sourceSale, sourceOfficial, sourceRules := publication.SaleUSD, publication.OfficialUSD, publication.TokenOfficialRules
			sourceRulesBefore, err := common.Marshal(sourceRules)
			require.NoError(t, err)

			require.NoError(t, applyZTAPICacheWritePricingMode(&publication, ztapiFableCashModeForTest))
			require.Equal(t, []string{"input_tokens", "cache_read", "output_tokens", "cache_write"}, publication.BillingDimensions)
			require.Equal(t, map[string]string{"input_tokens": "2", "output_tokens": "10", "cache_read": "0.2", "cache_write": price}, publication.SaleUSD)
			require.Equal(t, map[string]string{"input_tokens": "5"}, publication.OfficialUSD)
			require.Equal(t, map[string]string{"input_tokens": "5"}, publication.TokenOfficialRules[0].OfficialUSD)
			var rules []ztapiFableCashRuleForTest
			require.NoError(t, common.UnmarshalJsonStr(publication.TokenPriceRulesJSON, &rules))
			require.Len(t, rules, 2)
			for _, rule := range rules {
				require.Equal(t, price, rule.Sale["cache_write"])
				require.NotContains(t, rule.Sale, "cache_write_5m")
				require.NotContains(t, rule.Sale, "cache_write_1h")
			}
			require.Equal(t, []string{"context>200000"}, rules[1].Conditions)
			require.Equal(t, "4", rules[1].Sale["input_tokens"])
			require.Equal(t, frozenSale, sourceSale)
			require.Equal(t, frozenOfficial, sourceOfficial)
			sourceRulesAfter, err := common.Marshal(sourceRules)
			require.NoError(t, err)
			require.Equal(t, string(sourceRulesBefore), string(sourceRulesAfter))
			require.Contains(t, frozenRules, "cache_write_5m")

			items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{publication})
			require.Len(t, items, 1)
			require.Equal(t, publication.BillingDimensions, items[0].BillingDimensions)
			require.Len(t, items[0].TokenPriceRules, 2)
			for _, rule := range items[0].TokenPriceRules {
				require.Equal(t, price, rule.SaleUSD["cache_write"])
				require.NotContains(t, rule.SaleUSD, "cache_write_5m")
				require.NotContains(t, rule.SaleUSD, "cache_write_1h")
			}
		})
	}
}

func TestZTAPIFableCashCacheAcceptsEquivalentDecimalPrices(t *testing.T) {
	publication := ztapiFableCashPublicationForTest(t, "7.1250000000")
	publication.SaleUSD["cache_write_1h"] = "7.125"
	publication.SaleUSD["cache_write"] = "7.1250"
	var rules []ztapiFableCashRuleForTest
	require.NoError(t, common.UnmarshalJsonStr(publication.TokenPriceRulesJSON, &rules))
	rules[1].Sale["cache_write_5m"] = "7.125"
	rules[1].Sale["cache_write"] = "7.1250"
	raw, err := common.Marshal(rules)
	require.NoError(t, err)
	publication.TokenPriceRulesJSON = string(raw)
	require.NoError(t, applyZTAPICacheWritePricingMode(&publication, ztapiFableCashModeForTest))
	require.Equal(t, "7.1250000000", publication.SaleUSD["cache_write"])
}

func TestZTAPIFableCashCacheRejectsInvalidPricesAtomically(t *testing.T) {
	mutations := map[string]func(*ZTAPIRuntimePublication){
		"nil sale":         func(p *ZTAPIRuntimePublication) { p.SaleUSD = nil },
		"missing rules":    func(p *ZTAPIRuntimePublication) { p.TokenPriceRulesJSON = "" },
		"empty rules":      func(p *ZTAPIRuntimePublication) { p.TokenPriceRulesJSON = "[]" },
		"null rules":       func(p *ZTAPIRuntimePublication) { p.TokenPriceRulesJSON = "null" },
		"malformed rules":  func(p *ZTAPIRuntimePublication) { p.TokenPriceRulesJSON = "[{" },
		"wrong rule shape": func(p *ZTAPIRuntimePublication) { p.TokenPriceRulesJSON = "{}" },
	}
	for _, dimension := range []string{"cache_write_5m", "cache_write_1h", "cache_write"} {
		for _, value := range []string{"", "0", "-1", "NaN", "Infinity", "wrong", "7.1250000001"} {
			mutations["publication/"+dimension+"/"+value] = func(p *ZTAPIRuntimePublication) { p.SaleUSD[dimension] = value }
			mutations["last tier/"+dimension+"/"+value] = func(p *ZTAPIRuntimePublication) {
				var rules []ztapiFableCashRuleForTest
				require.NoError(t, common.UnmarshalJsonStr(p.TokenPriceRulesJSON, &rules))
				rules[1].Sale[dimension] = value
				raw, err := common.Marshal(rules)
				require.NoError(t, err)
				p.TokenPriceRulesJSON = string(raw)
			}
		}
		if dimension == "cache_write" {
			continue
		}
		mutations["missing publication/"+dimension] = func(p *ZTAPIRuntimePublication) { delete(p.SaleUSD, dimension) }
		mutations["missing last tier/"+dimension] = func(p *ZTAPIRuntimePublication) {
			var rules []ztapiFableCashRuleForTest
			require.NoError(t, common.UnmarshalJsonStr(p.TokenPriceRulesJSON, &rules))
			delete(rules[1].Sale, dimension)
			raw, err := common.Marshal(rules)
			require.NoError(t, err)
			p.TokenPriceRulesJSON = string(raw)
		}
	}
	mutations["different uniform last tier"] = func(p *ZTAPIRuntimePublication) {
		p.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"cache_write_5m":"7.125","cache_write_1h":"7.125"}},{"conditions":["context>200000"],"sale":{"cache_write_5m":"8","cache_write_1h":"8"}}]`
	}
	mutations["null last tier sale"] = func(p *ZTAPIRuntimePublication) {
		p.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"cache_write_5m":"7.125","cache_write_1h":"7.125"}},{"conditions":[],"sale":null}]`
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			publication := ztapiFableCashPublicationForTest(t, "7.125")
			mutate(&publication)
			before, err := common.Marshal(publication)
			require.NoError(t, err)
			require.Error(t, applyZTAPICacheWritePricingMode(&publication, ztapiFableCashModeForTest))
			after, err := common.Marshal(publication)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "rejection must not partially mutate publication")
		})
	}
}

func TestZTAPIFableCashCacheValidatesModeAndIdentityBeforePolicyLoading(t *testing.T) {
	publication := ztapiFableCashPublicationForTest(t, "7.125")
	snapshot := ZTAPIModelPublicationSnapshot{SourceModel: publication.SourceModel, PublicName: publication.PublicName,
		ProviderFamily: publication.ProviderFamily, Protocol: publication.Protocol, PriceSourceID: publication.PriceSourceID,
		CacheWritePricingMode: ztapiFableCashModeForTest}
	require.Empty(t, snapshot.PricePolicy)
	require.NoError(t, validateZTAPICacheWritePricingMode(&snapshot), "BeforeCreate calls this before filling policy from source")

	mutations := map[string]func(*ZTAPIRuntimePublication){
		"source model":           func(p *ZTAPIRuntimePublication) { p.SourceModel = "claude-opus-5" },
		"public name":            func(p *ZTAPIRuntimePublication) { p.PublicName = "zt-claude-opus-5" },
		"provider":               func(p *ZTAPIRuntimePublication) { p.ProviderFamily = ZTAPIProviderOpenAI },
		"protocol":               func(p *ZTAPIRuntimePublication) { p.Protocol = "anthropic" },
		"missing source":         func(p *ZTAPIRuntimePublication) { p.PriceSourceID = 0 },
		"negative source":        func(p *ZTAPIRuntimePublication) { p.PriceSourceID = -1 },
		"missing policy":         func(p *ZTAPIRuntimePublication) { p.PricePolicy = "" },
		"old policy":             func(p *ZTAPIRuntimePublication) { p.PricePolicy = "quote_frozen_retail_20261019" },
		"enterprise cash policy": func(p *ZTAPIRuntimePublication) { p.PricePolicy = "cash_enterprise_20_20261030" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			p := ztapiFableCashPublicationForTest(t, "7.125")
			mutate(&p)
			before, err := common.Marshal(p)
			require.NoError(t, err)
			require.Error(t, applyZTAPICacheWritePricingMode(&p, ztapiFableCashModeForTest))
			after, err := common.Marshal(p)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			if p.PricePolicy == "cash_pool_30_20261030" {
				invalid := snapshot
				invalid.SourceModel, invalid.PublicName = p.SourceModel, p.PublicName
				invalid.ProviderFamily, invalid.Protocol, invalid.PriceSourceID = p.ProviderFamily, p.Protocol, p.PriceSourceID
				require.Error(t, validateZTAPICacheWritePricingMode(&invalid))
			}
		})
	}
	for _, mode := range []string{"unknown", "fable_cash_floor_20261030 ", "FABLE_CASH_FLOOR_20261030"} {
		invalid := snapshot
		invalid.CacheWritePricingMode = mode
		require.Error(t, validateZTAPICacheWritePricingMode(&invalid))
		before, err := common.Marshal(publication)
		require.NoError(t, err)
		require.Error(t, applyZTAPICacheWritePricingMode(&publication, mode))
		after, err := common.Marshal(publication)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
	}
}

func TestZTAPIFableCashCacheLegacyPublisherRefusesBackslideWithoutWrites(t *testing.T) {
	for _, test := range []struct{ name, mode, policy string }{
		{"new mode", ztapiFableCashModeForTest, "enterprise_40_margin"},
		{"cash policy", "", "cash_pool_30_20261030"},
		{"legacy mode cash policy", ZTAPIFableUniformCacheWriteMode, "cash_pool_30_20261030"},
		{"new mode cash policy", ztapiFableCashModeForTest, "cash_pool_30_20261030"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := setupZTAPIPublicCatalogTestDB(t)
			require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}))
			config := seedZTAPIPublicCatalogRecord(t, db, "claude-fable-5", "zt-claude-fable-5", ZTAPIProviderAnthropic,
				ZTAPIProtocolOpenAICompatible, []string{"input_tokens", "output_tokens"})
			require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIModelPublicationSnapshot{}).
				Where("id = ?", config.PublicationSnapshotID).Updates(map[string]any{"cache_write_pricing_mode": test.mode, "price_policy": test.policy}).Error)
			readState := func() string {
				var configs []ZTAPIModelConfig
				var snapshots []ZTAPIModelPublicationSnapshot
				var sources []ZTAPIModelPriceSource
				var audits []ZTAPIAuditEvent
				require.NoError(t, db.Order("id").Find(&configs).Error)
				require.NoError(t, db.Order("id").Find(&snapshots).Error)
				require.NoError(t, db.Order("id").Find(&sources).Error)
				require.NoError(t, db.Order("id").Find(&audits).Error)
				raw, err := common.Marshal([]any{configs, snapshots, sources, audits})
				require.NoError(t, err)
				return string(raw)
			}
			before := readState()
			writes := 0
			countWrite := func(*gorm.DB) { writes++ }
			require.NoError(t, db.Callback().Create().Before("gorm:create").Register("fable_cash_no_create", countWrite))
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register("fable_cash_no_update", countWrite))
			require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("fable_cash_no_delete", countWrite))
			result, err := PublishZTAPIFableUniformCacheWrite(7, config.Version)
			require.ErrorContains(t, err, "cash", "refusal must explain the cash-policy backslide")
			require.Nil(t, result)
			require.Equal(t, 0, writes)
			require.Equal(t, before, readState(), "snapshots, config, supplier source and audits must remain unchanged")
		})
	}
}
