package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPriceReduction20261107SeparatePolicyAndExactCeiling(t *testing.T) {
	policy := ZTAPIPricePolicy("reduction_15_12_20261107")
	for _, resource := range []string{"official", "original_resource", "enterprise", "pool"} {
		require.NoError(t, ValidateZTAPIPricePolicy(resource, policy))
	}
	_, err := CalculateZTAPISalePriceForPolicy(decimal.NewFromInt(1), policy)
	require.Error(t, err, "the dated policy needs its approved model and route-cost manifest, never a generic scalar fallback")
	old, err := CalculateZTAPISalePriceForPolicy(decimal.NewFromInt(1), ZTAPIPricePolicyCashPool20261030)
	require.NoError(t, err)
	require.Equal(t, "1.5", old.String(), "historical 30 percent policy must keep its meaning")
}

func reductionFixture20261107(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupZTAPIPublicCatalogTestDB(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIAuditEvent{}, &ZTAPICatalogLock{}))
	var m ztapiReductionManifest
	require.NoError(t, common.UnmarshalJsonStr(ztapiReductionManifest20261107, &m))
	for _, s := range m.Sources {
		for _, b := range m.Baseline {
			if b.SourceModel == s.SourceModel {
				s.ModelConfigID = b.ID
			}
		}
		s.BillingDimensions = s.Dimensions
		s.SpendTier = s.QuotationGrade
		if s.SpendTier == "" {
			s.SpendTier = "standard"
		}
		s.OperatorID = 7
		s.SaleMultiplier = "1"
		if s.ID == 551 || s.ID == 552 {
			s.SaleMultiplier = "0.9"
		}
		require.NoError(t, db.Create(&s.ZTAPIModelPriceSource).Error)
	}
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	for _, a := range []struct {
		id      int64
		modelID int
		name    string
	}{{782, 55, "Claude Sonnet 5"}, {794, 61, "Gemini 3 Flash Preview"}, {574, 86, "Kimi K2.7 Code"}} {
		anchor, err := BuildZTAPIABTextPriceSourceWithFX(quote, a.name, a.modelID, 7, 1789401600, platformFXParamsForTest("6.63", "6.7138"))
		require.NoError(t, err)
		anchor.ID = a.id
		// These three historical anchors were not exported. Reconstruct their
		// retained retail rules only in the local fixture, keeping the real
		// production validator's parent lookup and re-derivation intact.
		for _, s := range m.Sources {
			if s.SourceModel != anchor.SourceModel || s.PricePolicy != string(ZTAPIPricePolicyQuoteFrozenRetail20261019) {
				continue
			}
			var rules []ztapiABFrozenSaleRule
			require.NoError(t, common.UnmarshalJsonStr(s.TokenPriceRulesJSON, &rules))
			for i := range rules {
				rules[i].RetailBasisID = 0
			}
			raw, err := common.Marshal(rules)
			require.NoError(t, err)
			anchor.TokenPriceRulesJSON = string(raw)
			break
		}
		require.NoError(t, db.Create(&anchor).Error)
	}
	for _, b := range m.Baseline {
		_, model, base, _, err := ztapiReductionApproval20261107(b.ID)
		require.NoError(t, err)
		var source ZTAPIModelPriceSource
		require.NoError(t, db.First(&source, base.ID).Error)
		preview, err := BuildZTAPIModelPricePreview(&source)
		require.NoError(t, err, b.SourceModel)
		config := ZTAPIModelConfig{ID: b.ID, SourceModel: b.SourceModel, PublicName: &b.PublicName, Family: ZTAPIModelFamilyOpenAI,
			Protocol: model.Protocol, ProviderFamily: model.Family, Version: b.Version, Published: true, EnabledGroups: `["default"]`,
			InputPricePerMillion: b.Input.InexactFloat64(), OutputPricePerMillion: b.Output.InexactFloat64(),
			InputCostPerMillion: decimal.RequireFromString(preview.InputCostUSDPerMillion).InexactFloat64(), OutputCostPerMillion: decimal.RequireFromString(preview.OutputCostUSDPerMillion).InexactFloat64(),
			CacheReadRatio: model.ReadRatio, CacheCreationRatio: model.WriteRatio, CacheCreation5mRatio: model.Write5mRatio, CacheCreation1hRatio: model.Write1hRatio,
			ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1, PublicationSnapshotID: b.SnapshotID}
		require.NoError(t, db.Create(&config).Error)
		p := ZTAPIModelPublicationSnapshot{ID: b.SnapshotID, ModelConfigID: b.ID, ModelVersion: b.Version, SourceModel: b.SourceModel, PublicName: b.PublicName,
			Protocol: model.Protocol, ProviderFamily: model.Family, EnabledGroups: config.EnabledGroups, AllowedChannelIDs: b.Channels, VerificationIDs: `[31,32]`, IdentityUpdatedAt: 1,
			PriceSourceID: b.SourceID, PricePolicy: b.PricePolicy, SaleMultiplier: "1", TokenPriceRulesJSON: model.Rules, MediaPriceContractJSON: model.Media, CacheWritePricingMode: model.CacheMode,
			InputPricePerMillion: config.InputPricePerMillion, OutputPricePerMillion: config.OutputPricePerMillion,
			CacheReadRatio: config.CacheReadRatio, CacheCreationRatio: config.CacheCreationRatio, CacheCreation5mRatio: config.CacheCreation5mRatio, CacheCreation1hRatio: config.CacheCreation1hRatio, ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1}
		if b.ID == 47 {
			_, p.VideoProtocolContractJSON, err = types.BuildZTAPISeedanceProtocolContract(b.SourceModel)
			require.NoError(t, err)
		}
		if b.ID == 85 {
			raw, marshalErr := common.Marshal(image2ABEditProtocol(t))
			require.NoError(t, marshalErr)
			p.ImageProtocolContractJSON = string(raw)
		}
		require.NoError(t, db.Create(&p).Error, b.SourceModel)
	}
	return db
}

func TestPriceReduction20261107ApprovedDimensionsAndNativeEvidence(t *testing.T) {
	db := reductionFixture20261107(t)
	count := 0
	for _, id := range ztapiReductionSortedIDs20261107() {
		b, _, _, rows, err := ztapiReductionApproval20261107(id)
		require.NoError(t, err)
		var base ZTAPIModelPriceSource
		require.NoError(t, db.First(&base, b.SourceID).Error)
		if base.PricePolicy == string(ZTAPIPricePolicyQuoteFrozenRetail20261019) {
			require.NoError(t, ValidateZTAPISupplierPriceSource20261019(db, &base), b.SourceModel)
		}
		target, err := deriveZTAPIPriceReduction20261107(&base, 9)
		if id == 5 {
			require.ErrorContains(t, err, "Flash excluded")
			continue
		}
		require.NoError(t, err, b.SourceModel)
		require.NoError(t, ValidateZTAPIPriceReductionSource20261107(db, &target), b.SourceModel)
		preview, err := BuildZTAPIModelPricePreview(&target)
		require.NoError(t, err)
		require.NotEmpty(t, preview.SaleUSD)
		count += len(rows)
		if id == 66 {
			require.Equal(t, "8.6608020000", target.InputPerMillion)
			require.Equal(t, "E38", target.QuotationCell)
			require.Equal(t, ZTAPISupplierWorkbook20261019SHA256, target.SourceDocumentChecksum)
		} else {
			require.Equal(t, ztapiPriceSourceValues(&base), ztapiPriceSourceValues(&target), "sales ceilings must not rewrite native supplier costs")
		}
		bad := target
		bad.InputPerMillion = "0.01"
		require.Error(t, ValidateZTAPIPriceReductionSource20261107(db, &bad))
		bad = target
		bad.CashMarginBaseSourceID++
		require.Error(t, ValidateZTAPIPriceReductionSource20261107(db, &bad))
		bad = target
		bad.SaleMultiplier = "0.99"
		require.Error(t, ValidateZTAPIModelPriceSource(&bad))
		if id == 48 {
			pub := ZTAPIRuntimePublication{SourceModel: base.SourceModel, PublicName: b.PublicName, ProviderFamily: ZTAPIProviderAnthropic, Protocol: ZTAPIProtocolOpenAICompatible, PriceSourceID: 2000, PricePolicy: target.PricePolicy, BillingDimensions: preview.BillingDimensions, SaleUSD: preview.SaleUSD, TokenPriceRulesJSON: target.TokenPriceRulesJSON}
			require.NoError(t, applyZTAPICacheWritePricingMode(&pub, ZTAPIFableCashFloorCacheWriteMode))
			require.Equal(t, "16.2618000000", pub.SaleUSD[ZTAPIBillingDimensionCacheWrite])
			require.NotContains(t, pub.SaleUSD, ZTAPIBillingDimensionCacheWrite5m)
			require.NotContains(t, pub.SaleUSD, ZTAPIBillingDimensionCacheWrite1h)
			require.NotContains(t, pub.BillingDimensions, ZTAPIBillingDimensionCacheWrite5m)
			require.NotContains(t, pub.BillingDimensions, ZTAPIBillingDimensionCacheWrite1h)
		}
		_, _, _, err = ImportZTAPIModelPriceSource(ZTAPIModelPriceSourceImport{Source: target, OperatorID: 9, Reason: "generic import must not bypass dated publication", ExpectedVersion: b.Version})
		require.ErrorContains(t, err, "controlled dated publisher")
	}
	require.Equal(t, 80, count)
}

func TestPriceReduction20261107AtomicApplyRetryRollbackAndInflightImmutability(t *testing.T) {
	db := reductionFixture20261107(t)
	require.NoError(t, db.AutoMigrate(&ZTAPIRequestSettlement{}))
	selected := ApprovedZTAPIPriceReductionSelections20261107()
	var originalConfigs []ZTAPIModelConfig
	var originalSnapshots []ZTAPIModelPublicationSnapshot
	var originalSources []ZTAPIModelPriceSource
	require.NoError(t, db.Order("id").Find(&originalConfigs).Error)
	require.NoError(t, db.Order("id").Find(&originalSnapshots).Error)
	require.NoError(t, db.Order("id").Find(&originalSources).Error)
	preview, err := PreviewZTAPIPriceReduction20261107(selected, false)
	require.NoError(t, err)
	require.Len(t, preview, 16)
	require.True(t, preview[0].Skipped)
	require.Contains(t, preview[0].SkipReason, "non-Prime")
	var sourceCount, snapshotCount, auditCount int64
	require.NoError(t, db.Model(&ZTAPIModelPriceSource{}).Count(&sourceCount).Error)
	require.EqualValues(t, len(originalSources), sourceCount)
	require.NoError(t, db.Model(&ZTAPIModelPublicationSnapshot{}).Count(&snapshotCount).Error)
	require.EqualValues(t, len(originalSnapshots), snapshotCount)
	require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&auditCount).Error)
	require.Zero(t, auditCount)
	result, err := ApplyZTAPIPriceReduction20261107(9, selected, false)
	require.NoError(t, err)
	require.Equal(t, 15, result.Republished)
	require.Len(t, result.Skipped, 1)
	result, err = ApplyZTAPIPriceReduction20261107(9, selected, false)
	require.NoError(t, err)
	require.Equal(t, 15, result.Unchanged)
	require.Zero(t, result.Imported)
	rollback := []ZTAPIPriceReductionSelection20261107{}
	for _, s := range selected {
		var config ZTAPIModelConfig
		require.NoError(t, db.First(&config, s.ModelID).Error)
		if s.ModelID == 5 {
			require.Equal(t, originalConfigs[0], config)
			continue
		}
		var current, old ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&current, config.PublicationSnapshotID).Error)
		require.NoError(t, db.First(&old, s.SnapshotID).Error)
		require.Equal(t, s.Version+1, config.Version)
		require.Equal(t, old.AllowedChannelIDs, current.AllowedChannelIDs)
		require.Equal(t, old.VerificationIDs, current.VerificationIDs)
		require.Equal(t, old.ImageProtocolContractJSON, current.ImageProtocolContractJSON)
		require.Equal(t, old.VideoProtocolContractJSON, current.VideoProtocolContractJSON)
		require.Equal(t, old.CacheWritePricingMode, current.CacheWritePricingMode)
		var source ZTAPIModelPriceSource
		require.NoError(t, db.First(&source, current.PriceSourceID).Error)
		require.Equal(t, s.SourceID, source.CashMarginBaseSourceID)
		require.NoError(t, ValidateZTAPIPriceReductionSource20261107(db, &source))
		require.True(t, ztapiPublicationPriceBasisPermittedTx(db, &source))
		checksums, err := ztapiQuotationChecksums(db, []int64{current.ID})
		require.NoError(t, err)
		require.Equal(t, source.SourceDocumentChecksum, checksums[current.ID])
		rollback = append(rollback, ZTAPIPriceReductionSelection20261107{ModelID: s.ModelID, Version: config.Version, SnapshotID: current.ID, SourceID: source.ID, RestoreSnapshotID: s.SnapshotID})
	}
	status, err := CheckZTAPIPriceReductionDowngrade20261107()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Len(t, status.ActiveModels, 15)
	payload, err := common.Marshal(map[string]any{"PricePolicy": string(ZTAPIPricePolicyReduction20261107), "PriceSourceID": rollback[0].SourceID})
	require.NoError(t, err)
	inflight := ZTAPIRequestSettlement{OperationID: "reduction-inflight", RequestID: "reduction-inflight", PublicModel: "zt-deepseek-v4-pro", Status: ZTAPISettlementPending, PriceSnapshotJSON: string(payload), UsageJSON: `{}`, ChargeDimensionsJSON: `[]`, MissingDimensionsJSON: `[]`}
	require.NoError(t, db.Create(&inflight).Error)
	require.NoError(t, db.First(&inflight, inflight.ID).Error)
	_, err = PreviewZTAPIPriceReduction20261107(rollback, true)
	require.NoError(t, err)
	result, err = ApplyZTAPIPriceReduction20261107(9, rollback, true)
	require.NoError(t, err)
	require.Equal(t, 15, result.Republished)
	result, err = ApplyZTAPIPriceReduction20261107(9, rollback, true)
	require.NoError(t, err)
	require.Equal(t, 15, result.Unchanged)
	for _, old := range originalSnapshots {
		var persisted ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&persisted, old.ID).Error)
		require.Equal(t, old, persisted, "historic snapshots must be byte-for-byte unchanged")
		var c ZTAPIModelConfig
		require.NoError(t, db.First(&c, old.ModelConfigID).Error)
		if old.ModelConfigID == 5 {
			continue
		}
		var restored ZTAPIModelPublicationSnapshot
		require.NoError(t, db.First(&restored, c.PublicationSnapshotID).Error)
		require.NotEqual(t, old.ID, restored.ID)
		require.NotEqual(t, old.PriceSourceID, restored.PriceSourceID)
		require.Equal(t, old.PricePolicy, restored.PricePolicy)
		require.Equal(t, old.TokenPriceRulesJSON, restored.TokenPriceRulesJSON)
		require.Equal(t, old.MediaPriceContractJSON, restored.MediaPriceContractJSON)
		require.Equal(t, old.InputPricePerMillion, restored.InputPricePerMillion)
		require.Equal(t, old.OutputPricePerMillion, restored.OutputPricePerMillion)
		require.Equal(t, old.ModelVersion+2, c.Version)
	}
	for _, old := range originalSources {
		var persisted ZTAPIModelPriceSource
		require.NoError(t, db.First(&persisted, old.ID).Error)
		require.Equal(t, old, persisted)
	}
	var unchanged ZTAPIRequestSettlement
	require.NoError(t, db.First(&unchanged, inflight.ID).Error)
	require.Equal(t, inflight, unchanged)
	status, err = CheckZTAPIPriceReductionDowngrade20261107()
	require.NoError(t, err)
	require.False(t, status.CanDowngrade)
	require.Empty(t, status.ActiveModels)
	require.Equal(t, []uint{inflight.ID}, status.UnsettledRequestIDs)
	require.NoError(t, db.Model(&inflight).Update("status", ZTAPISettlementSettled).Error)
	status, err = CheckZTAPIPriceReductionDowngrade20261107()
	require.NoError(t, err)
	require.True(t, status.CanDowngrade)
}

func TestPriceReduction20261107BatchDriftFailsWholeTransaction(t *testing.T) {
	for _, drift := range []string{"version", "route", "cost", "tier", "admission", "source_identity", "stale_selection"} {
		t.Run(drift, func(t *testing.T) {
			db := reductionFixture20261107(t)
			selected := ApprovedZTAPIPriceReductionSelections20261107()
			selected = []ZTAPIPriceReductionSelection20261107{selected[1], selected[len(selected)-1]}
			s := selected[1]
			switch drift {
			case "version":
				require.NoError(t, db.Model(&ZTAPIModelConfig{}).Where("id = ?", s.ModelID).Update("version", s.Version+2).Error)
			case "route":
				require.NoError(t, db.Exec("UPDATE ztapi_model_publication_snapshots SET allowed_channel_ids = ? WHERE id = ?", "[1,2]", s.SnapshotID).Error)
			case "cost":
				require.NoError(t, db.Exec("UPDATE ztapi_model_price_sources SET input_per_million = ? WHERE id = ?", "0.01", s.SourceID).Error)
			case "tier":
				require.NoError(t, db.Exec("UPDATE ztapi_model_publication_snapshots SET token_price_rules_json = ? WHERE id = ?", "[]", s.SnapshotID).Error)
			case "admission":
				require.NoError(t, db.Exec("UPDATE ztapi_model_publication_snapshots SET verification_ids = ? WHERE id = ?", "[]", s.SnapshotID).Error)
			case "source_identity":
				require.NoError(t, db.Exec("UPDATE ztapi_model_price_sources SET quotation_model_code = ? WHERE id = ?", "unexpected-alias", s.SourceID).Error)
			case "stale_selection":
				selected[1].Version++
			}
			_, err := PreviewZTAPIPriceReduction20261107(selected, false)
			require.Error(t, err)
			_, err = ApplyZTAPIPriceReduction20261107(9, selected, false)
			require.Error(t, err)
			var first ZTAPIModelConfig
			require.NoError(t, db.First(&first, selected[0].ModelID).Error)
			require.Equal(t, selected[0].Version, first.Version)
			require.Equal(t, selected[0].SnapshotID, first.PublicationSnapshotID)
			var count int64
			require.NoError(t, db.Model(&ZTAPIAuditEvent{}).Count(&count).Error)
			require.Zero(t, count)
			if drift == "stale_selection" {
				require.ErrorIs(t, err, ErrZTAPIModelVersionConflict)
			}
		})
	}
}
