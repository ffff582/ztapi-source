package router

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

func seedRouterPublicModel(t *testing.T, db *gorm.DB, channelID int, sourceModel, publicName string) {
	t.Helper()
	now := common.GetTimestamp()
	config := model.ZTAPIModelConfig{
		SourceModel: sourceModel, PublicName: &publicName,
		Family: model.ZTAPIModelFamilyOpenAI, Protocol: model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily:      model.ZTAPIProviderOpenAI,
		InputCostPerMillion: 1, OutputCostPerMillion: 2,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 2,
		EnabledGroups: `["default"]`, Published: true, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("create router model publication: %v", err)
	}
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: sourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1", OutputPerMillion: "2",
		CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		QuotationEffectiveAt: now, SourceDocumentChecksum: model.ZTAPIQuotationSHA256,
		OperatorID: 1, Version: 1, CreatedAt: now,
	}
	if err := db.Create(&price).Error; err != nil {
		t.Fatalf("create router model price evidence: %v", err)
	}
	channelIDs, err := common.Marshal([]int{channelID})
	if err != nil {
		t.Fatalf("marshal router model channels: %v", err)
	}
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: sourceModel, PublicName: publicName,
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily,
		EnabledGroups: config.EnabledGroups, AllowedChannelIDs: string(channelIDs), PriceSourceID: price.ID,
		InputPricePerMillion: config.InputPricePerMillion, OutputPricePerMillion: config.OutputPricePerMillion,
		CacheReadRatio: config.CacheReadRatio, CacheCreationRatio: config.CacheCreationRatio,
		CacheCreation5mRatio: config.CacheCreation5mRatio, CacheCreation1hRatio: config.CacheCreation1hRatio,
		ImageRatio: config.ImageRatio, AudioRatio: config.AudioRatio,
		AudioCompletionRatio: config.AudioCompletionRatio,
		VerificationIDs:      `[]`, IdentityUpdatedAt: now, CreatedAt: now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create router model publication snapshot: %v", err)
	}
	if err := db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error; err != nil {
		t.Fatalf("attach router model publication snapshot: %v", err)
	}
	model.InvalidateZTAPIAliasCache()
}
