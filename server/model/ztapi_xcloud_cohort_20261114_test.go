package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIXcloud20261114GPTFrozenTiers(t *testing.T) {
	for _, tc := range []struct {
		model                      string
		id                         int
		cashInput, cashOutput      string
		input, output, read, write [2]string
	}{
		{"gpt-6-sol", 136, "1.8000000000", "9.0000000000", [2]string{"2.2236000000", "3.1200000000"}, [2]string{"11.1177000000", "11.7000000000"}, [2]string{"0.2224000000", "0.3120000000"}, [2]string{"2.7795000000", "3.9000000000"}},
		{"gpt-6-astra", 103, "18.0000000000", "67.5000000000", [2]string{"11.1177000000", "22.2353000000"}, [2]string{"55.5883000000", "83.3824000000"}, [2]string{"1.1118000000", "2.2236000000"}, [2]string{"13.8971000000", "27.7942000000"}},
	} {
		t.Run(tc.model, func(t *testing.T) {
			source, err := buildZTAPIXcloudSource20261114(tc.model, 9)
			require.NoError(t, err)
			require.Equal(t, tc.id, source.ModelConfigID)
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, tc.cashInput, preview.InputCostUSDPerMillion)
			require.Equal(t, tc.cashOutput, preview.OutputCostUSDPerMillion)
			require.Equal(t, tc.input[1], preview.InputSaleUSDPerMillion)
			var rules []ztapiABFrozenSaleRule
			require.NoError(t, common.UnmarshalJsonStr(source.TokenPriceRulesJSON, &rules))
			require.Len(t, rules, 2)
			snapshot := &relaycommon.ZTAPIPublicationSnapshot{PriceSourceID: 1, PriceSourceVersion: 1, BillingDimensions: preview.BillingDimensions, SaleUSD: preview.SaleUSD, TokenPriceRulesJSON: source.TokenPriceRulesJSON}
			for tier, input := range []int{272000, 272001} {
				usage := &dto.Usage{PromptTokens: input, CompletionTokens: 4}
				usage.PromptTokensDetails.CachedTokens = 9
				usage.PromptTokensDetails.CachedCreationTokens = 6
				selected, _, err := relaycommon.SelectZTAPIFrozenTokenTier(snapshot, usage)
				require.NoError(t, err)
				require.Equal(t, map[string]string{"input_tokens": tc.input[tier], "output_tokens": tc.output[tier], "cache_read": tc.read[tier], "cache_write": tc.write[tier]}, selected.SaleUSD)
				proof := relaycommon.ValidateZTAPIUsageDimensions(nil, &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: selected}, usage)
				require.False(t, proof.Pending, "%+v", proof)
				counts := map[string]int64{}
				for _, d := range proof.Dimensions {
					counts[d.Dimension] = d.Quantity
				}
				require.Equal(t, map[string]int64{"input_tokens": int64(input - 15), "output_tokens": 4, "cache_read": 9, "cache_write": 6}, counts)
			}
			require.NoError(t, ValidateZTAPIXcloudRoute20261114(tc.model, 6, "https://www.xcloudtoken.com", tc.model))
			require.NoError(t, ValidateZTAPIXcloudRoute20261114(tc.model, 2, "https://ai.yunxinapi.com/hub", tc.model+"-hc"))
			require.Error(t, ValidateZTAPIXcloudRoute20261114(tc.model, 7, "https://www.xcloudtoken.com", tc.model))
			source.TokenPriceRulesJSON = `[{"conditions":[],"sale":{"input_tokens":"0.01"}}]`
			require.Error(t, ValidateZTAPIModelPriceSource(&source))
		})
	}
}

func TestZTAPIXcloud20261114ExactRetailAndCosts(t *testing.T) {
	cases := []struct {
		model                            string
		inputCost, inputSale, outputSale string
		cacheRead, write5m, write1h      string
	}{
		{"claude-sonnet-5", "1.0000000000", "1.2353000000", "6.4853000000", "0.1390000000", "1.7603000000", "2.5015000000"},
		{"deepseek-v4.1-flash", "0.0420000000", "0.0655200000", "0.2340000000", "0.0023400000", "", ""},
		{"claude-fable-5-1", "5.0000000000", "8.1900000000", "40.9500000000", "0.3900000000", "7.7206000000", "12.3530000000"},
		{"claude-haiku-4-5-20251001", "0.5000000000", "0.7800000000", "3.9000000000", "0.0780000000", "0.9750000000", "1.5600000000"},
		{"claude-opus-4-6", "2.5000000000", "3.9000000000", "19.5000000000", "0.3900000000", "4.8750000000", "7.8000000000"},
		{"claude-opus-4-7", "2.5000000000", "3.9000000000", "19.5000000000", "0.3900000000", "4.8750000000", "7.8000000000"},
		{"claude-opus-4-8", "2.5000000000", "3.9000000000", "19.5000000000", "0.3900000000", "4.8750000000", "7.8000000000"},
		{"claude-sonnet-4-6", "1.5000000000", "2.3400000000", "11.7000000000", "0.2340000000", "2.9250000000", "4.6800000000"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			source, err := buildZTAPIXcloudSource20261114(tc.model, 9)
			require.NoError(t, err)
			preview, err := BuildZTAPIModelPricePreview(&source)
			require.NoError(t, err)
			require.Equal(t, tc.inputCost, preview.InputCostUSDPerMillion)
			require.Equal(t, tc.inputSale, preview.InputSaleUSDPerMillion)
			require.Equal(t, tc.outputSale, preview.OutputSaleUSDPerMillion)
			require.Equal(t, tc.cacheRead, preview.SaleUSD[ZTAPIBillingDimensionCacheRead])
			require.Equal(t, tc.write5m, preview.SaleUSD[ZTAPIBillingDimensionCacheWrite5m])
			require.Equal(t, tc.write1h, preview.SaleUSD[ZTAPIBillingDimensionCacheWrite1h])
			source.TokenPriceRulesJSON = ""
			require.Error(t, ValidateZTAPIModelPriceSource(&source))
		})
	}
	_, err := buildZTAPIXcloudSource20261114("claude-opus-5-5", 9)
	require.Error(t, err)
}

func TestZTAPIXcloud20261114PublishDraftAndRestore(t *testing.T) {
	t.Run("deepseek", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "deepseek-v4.1-flash", []int{8}, "", map[string]string{
			"input_tokens": "0.0655200000", "output_tokens": "0.2340000000", "cache_read": "0.0023400000",
		})
	})
	t.Run("fable", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "claude-fable-5-1", []int{4}, "", map[string]string{
			"input_tokens": "8.1900000000", "output_tokens": "40.9500000000", "cache_read": "0.3900000000",
			"cache_write_5m": "7.7206000000", "cache_write_1h": "12.3530000000",
		})
	})
	t.Run("haiku", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "claude-haiku-4-5-20251001", []int{4}, "", map[string]string{
			"input_tokens": "0.7800000000", "output_tokens": "3.9000000000", "cache_read": "0.0780000000",
			"cache_write_5m": "0.9750000000", "cache_write_1h": "1.5600000000",
		})
	})
	t.Run("opus", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "claude-opus-4-6", []int{2, 4}, "claude-opus-4-6-hc", map[string]string{
			"input_tokens": "3.9000000000", "output_tokens": "19.5000000000", "cache_read": "0.3900000000",
			"cache_write_5m": "4.8750000000", "cache_write_1h": "7.8000000000",
		})
	})
	t.Run("sol", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "gpt-6-sol", []int{2, 6}, "gpt-6-sol-hc",
			map[string]string{"input_tokens": "2.2236000000", "output_tokens": "11.1177000000", "cache_read": "0.2224000000", "cache_write": "2.7795000000"},
			map[string]string{"input_tokens": "3.1200000000", "output_tokens": "11.7000000000", "cache_read": "0.3120000000", "cache_write": "3.9000000000"})
	})
	t.Run("astra", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "gpt-6-astra", []int{2, 6}, "gpt-6-astra-hc",
			map[string]string{"input_tokens": "11.1177000000", "output_tokens": "55.5883000000", "cache_read": "1.1118000000", "cache_write": "13.8971000000"},
			map[string]string{"input_tokens": "22.2353000000", "output_tokens": "83.3824000000", "cache_read": "2.2236000000", "cache_write": "27.7942000000"})
	})
	t.Run("gpt56-sol", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "gpt-5.6-sol", []int{2, 7}, "gpt-5.6-sol-hc",
			map[string]string{"input_tokens": "3.1200000000", "output_tokens": "18.5295000000", "cache_read": "3.1200000000", "cache_write": "3.9000000000"},
			map[string]string{"input_tokens": "6.2400000000", "output_tokens": "23.4000000000", "cache_read": "6.2400000000", "cache_write": "7.8000000000"})
	})
	t.Run("gpt56-terra", func(t *testing.T) {
		testZTAPIXcloud20261114PublishDraftAndRestore(t, "gpt-5.6-terra", []int{2, 7}, "gpt-5.6-terra-hc",
			map[string]string{"input_tokens": "1.5600000000", "output_tokens": "9.3600000000", "cache_read": "1.5600000000", "cache_write": "1.9500000000"},
			map[string]string{"input_tokens": "3.1200000000", "output_tokens": "14.0400000000", "cache_read": "3.1200000000", "cache_write": "3.9000000000"})
	})
}

func testZTAPIXcloud20261114PublishDraftAndRestore(t *testing.T, source string, expectedChannels []int, backupPhysical string, expectedPrices ...map[string]string) {
	t.Helper()
	f := setupZTAPIPublicationGateFixture(t)
	require.NoError(t, f.db.AutoMigrate(&ZTAPIRequestSettlement{}))
	q, err := ztapiXcloudQuote20261114(source)
	require.NoError(t, err)
	url, priority := "https://www.xcloudtoken.com", int64(30)
	channel := f.channel
	channel.Id, channel.BaseURL, channel.Models, channel.Priority = q.primary, &url, q.source, &priority
	require.NoError(t, f.db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(f.db))
	config := f.config
	config.ID, config.SourceModel, config.PublicName = q.id, q.source, &q.public
	config.ProviderFamily = q.family
	require.NoError(t, f.db.Create(&config).Error)
	identity := f.identity
	identity.ModelConfigID, identity.PublicName = config.ID, q.public
	identity.ProviderFamily = q.family
	require.NoError(t, f.db.Create(&identity).Error)
	previous := f.price
	previous.ID, previous.ModelConfigID, previous.SourceModel = 0, q.id, q.source
	require.NoError(t, f.db.Create(&previous).Error)
	v := f.verification
	v.ID, v.ModelConfigID, v.ChannelID = 0, q.id, channel.Id
	require.NoError(t, f.db.Create(&v).Error)
	req := ZTAPIXcloudPublication20261114{SourceModel: q.source, Version: config.Version, SourceID: previous.ID, VerificationID: v.ID}
	_, err = ApplyZTAPIXcloudPublication20261114(9, req)
	require.Error(t, err, "fresh discovery is required")
	var sources int64
	require.NoError(t, f.db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", q.id).Count(&sources).Error)
	require.EqualValues(t, 1, sources, "a rejected publication must not leave a new price source")
	_, err = RecordZTAPIXcloudDiscovery20261114(q.source, channel.Id, []string{f.config.SourceModel}, 9, time.Now())
	require.Error(t, err)
	_, err = RecordZTAPIXcloudDiscovery20261114(q.source, channel.Id, []string{q.source}, 9, time.Now())
	require.NoError(t, err)
	if backupPhysical != "" {
		_, err = ApplyZTAPIXcloudPublication20261114(9, req)
		require.Error(t, err, "the verified primary alone does not authorize an unverified backup")
		backup := f.channel
		backup.Id = expectedChannels[0]
		backupURL, backupPriority := "https://ai.yunxinapi.com/hub", int64(10)
		mapping := `{"` + q.source + `":"` + backupPhysical + `"}`
		backup.BaseURL, backup.Models, backup.Priority, backup.ModelMapping = &backupURL, q.source, &backupPriority, &mapping
		require.NoError(t, f.db.Save(&backup).Error)
		require.NoError(t, backup.AddAbilities(f.db))
		_, err = RecordZTAPIXcloudDiscovery20261114(q.source, backup.Id, []string{backupPhysical}, 9, time.Now())
		require.NoError(t, err)
		backupV := v
		backupV.ID, backupV.ChannelID, backupV.VerifiedAt = 0, backup.Id, time.Now().Add(-48*time.Hour).Unix()
		require.NoError(t, f.db.Create(&backupV).Error)
		_, err = ApplyZTAPIXcloudPublication20261114(9, req)
		require.Error(t, err, "stale backup cannot publish")
		require.NoError(t, f.db.Model(&backupV).Update("verified_at", time.Now().Unix()).Error)
		failed := backupV
		failed.ID, failed.VerifiedAt, failed.UsageReconciled = 0, time.Now().Unix(), false
		require.NoError(t, f.db.Create(&failed).Error)
		_, err = ApplyZTAPIXcloudPublication20261114(9, req)
		require.ErrorContains(t, err, "latest Yunxin backup verification")
		require.NoError(t, f.db.Model(&failed).Update("usage_reconciled", true).Error)
		require.NoError(t, f.db.Model(&backup).Update("priority", priority).Error)
		_, err = ApplyZTAPIXcloudPublication20261114(9, req)
		require.ErrorContains(t, err, "Xcloud primary does not outrank")
		require.NoError(t, f.db.Model(&backup).Update("priority", int64(10)).Error)
	}
	blocked := errors.New("prospective cost proof does not match")
	_, err = ApplyZTAPIXcloudPublicationWithGate20261114(9, req, func(tx *gorm.DB) error {
		publications, err := loadZTAPIActivePublications(tx)
		require.NoError(t, err)
		require.Len(t, publications, 1)
		require.Equal(t, q.public, publications[0].PublicName)
		return blocked
	})
	require.ErrorIs(t, err, blocked)
	require.NoError(t, f.db.Model(&ZTAPIModelPriceSource{}).Where("model_config_id = ?", q.id).Count(&sources).Error)
	require.EqualValues(t, 1, sources)
	result, err := ApplyZTAPIXcloudPublication20261114(9, req)
	require.NoError(t, err)
	require.Equal(t, expectedChannels, result.ChannelIDs())
	require.Equal(t, "xcloud_verified_20261114", result.PricePolicy)
	publications, err := loadZTAPIActivePublications(f.db)
	require.NoError(t, err)
	require.Len(t, publications, 1)
	require.Equal(t, q.public, publications[0].PublicName)
	InvalidateZTAPIAliasCache()
	t.Cleanup(InvalidateZTAPIAliasCache)
	publicCatalog, err := ListZTAPIPublicCatalog()
	require.NoError(t, err)
	require.Len(t, publicCatalog, 1, "a verified Xcloud model must survive the public quotation gate")
	require.Equal(t, q.public, publicCatalog[0].ModelName)
	require.Len(t, publicCatalog[0].TokenPriceRules, len(expectedPrices))
	for i, prices := range expectedPrices {
		require.Equal(t, prices, publicCatalog[0].TokenPriceRules[i].SaleUSD)
	}
	resolved, ok := ResolveZTAPIPublicAlias(q.public)
	require.True(t, ok)
	require.Equal(t, q.source, resolved)
	downgrade, err := CheckZTAPIXcloudDowngrade20261114()
	require.NoError(t, err)
	require.False(t, downgrade.CanDowngrade)
	require.Contains(t, downgrade.ActiveModels, q.public)
	_, err = ApplyZTAPIXcloudPublication20261114(9, req)
	require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
	require.NoError(t, RestoreZTAPIXcloudPublication20261114(9, q.source, result.ModelVersion, result.ID))
	require.NoError(t, f.db.First(&config, q.id).Error)
	require.False(t, config.Published)
	require.NoError(t, f.db.First(&ZTAPIModelPublicationSnapshot{}, result.ID).Error, "in-flight snapshots remain readable")
	downgrade, err = CheckZTAPIXcloudDowngrade20261114()
	require.NoError(t, err)
	require.True(t, downgrade.CanDowngrade)
}

func TestZTAPIXcloud20261114RouteRequiresExactSupplierProduct(t *testing.T) {
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 4, "https://www.xcloudtoken.com", "claude-sonnet-5"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 2, "https://ai.yunxinapi.com/hub", "claude-sonnet-5-hc"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 2, "https://ai.yunxinapi.com/hub", "claude-sonnet-5"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-sonnet-5", 4, "https://www.xcloudtoken.com/other", "claude-sonnet-5"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 8, "https://www.xcloudtoken.com", "deepseek-v4.1-flash"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 8, "https://www.xcloudtoken.com", "deepseek-v4-flash"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("deepseek-v4.1-flash", 2, "https://ai.yunxinapi.com/hub", "deepseek-v4.1-flash"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-fable-5-1", 4, "https://www.xcloudtoken.com", "claude-fable-5-1"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-fable-5-1", 4, "https://www.xcloudtoken.com", "claude-fable-5"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-fable-5-1", 6, "https://www.xcloudtoken.com", "claude-fable-5-1"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-fable-5-1", 2, "https://ai.yunxinapi.com/hub", "claude-fable-5-1"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-haiku-4-5-20251001", 4, "https://www.xcloudtoken.com", "claude-haiku-4-5-20251001"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-haiku-4-5-20251001", 1, "https://ai.yunxinapi.com/hub", "claude-haiku-4-5-20251001"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-haiku-4-5-20251001", 2, "https://ai.yunxinapi.com/hub", "claude-haiku-4-5-20251001-hc"))
	require.NoError(t, ValidateZTAPIXcloudRoute20261114("claude-opus-4-8", 2, "https://ai.yunxinapi.com/hub", "claude-opus-4-8-hc"))
	require.Error(t, ValidateZTAPIXcloudRoute20261114("claude-opus-4-8", 1, "https://ai.yunxinapi.com/hub", "claude-opus-4-8-hc"))
}

func TestZTAPIXcloud20261114PriceImportRequiresControlledPublisher(t *testing.T) {
	source, err := buildZTAPIXcloudSource20261114("deepseek-v4.1-flash", 9)
	require.NoError(t, err)
	config, persisted, preview, err := ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{Source: source, OperatorID: 9})
	require.ErrorContains(t, err, "Xcloud price source requires its controlled publisher")
	require.Nil(t, config)
	require.Nil(t, persisted)
	require.Nil(t, preview)
}
