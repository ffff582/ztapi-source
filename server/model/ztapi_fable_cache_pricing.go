package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const ZTAPIFableUniformCacheWriteMode = "fable_uniform_20261024"
const ZTAPIFableUniformCacheWritePrice = "18.86582633"
const ZTAPIFableCashFloorCacheWriteMode = "fable_cash_floor_20261030"

func validateZTAPICacheWritePricingMode(snapshot *ZTAPIModelPublicationSnapshot) error {
	if snapshot.CacheWritePricingMode == "" {
		return nil
	}
	if (snapshot.CacheWritePricingMode != ZTAPIFableUniformCacheWriteMode && snapshot.CacheWritePricingMode != ZTAPIFableCashFloorCacheWriteMode) || snapshot.SourceModel != "claude-fable-5" ||
		snapshot.PublicName != "zt-claude-fable-5" || snapshot.ProviderFamily != ZTAPIProviderAnthropic ||
		snapshot.Protocol != ZTAPIProtocolOpenAICompatible || snapshot.PriceSourceID <= 0 {
		return errors.New("uniform cache-write pricing is approved only for Fable 5")
	}
	return nil
}

// Retail override only: supplier quotes and historical snapshots stay intact.
func applyZTAPICacheWritePricingMode(publication *ZTAPIRuntimePublication, mode string) error {
	if mode == "" {
		return nil
	}
	identity := &ZTAPIModelPublicationSnapshot{SourceModel: publication.SourceModel, PublicName: publication.PublicName,
		ProviderFamily: publication.ProviderFamily, Protocol: publication.Protocol, PriceSourceID: publication.PriceSourceID, CacheWritePricingMode: mode}
	if err := validateZTAPICacheWritePricingMode(identity); err != nil {
		return err
	}
	if mode == ZTAPIFableCashFloorCacheWriteMode {
		return applyZTAPIFableCashFloorCacheWrite(publication)
	}
	prices := func(values map[string]string) map[string]string {
		result := copyZTAPIStringMap(values)
		if result == nil {
			result = map[string]string{}
		}
		delete(result, ZTAPIBillingDimensionCacheWrite5m)
		delete(result, ZTAPIBillingDimensionCacheWrite1h)
		result[ZTAPIBillingDimensionCacheWrite] = ZTAPIFableUniformCacheWritePrice
		return result
	}
	dimensions := make([]string, 0, len(publication.BillingDimensions))
	for _, dimension := range publication.BillingDimensions {
		if dimension != ZTAPIBillingDimensionCacheWrite && dimension != ZTAPIBillingDimensionCacheWrite5m && dimension != ZTAPIBillingDimensionCacheWrite1h {
			dimensions = append(dimensions, dimension)
		}
	}
	publication.BillingDimensions = append(dimensions, ZTAPIBillingDimensionCacheWrite)
	publication.SaleUSD = prices(publication.SaleUSD)
	if publication.TokenPriceRulesJSON != "" {
		var rules []struct {
			Conditions []string          `json:"conditions"`
			Sale       map[string]string `json:"sale"`
		}
		if err := common.UnmarshalJsonStr(publication.TokenPriceRulesJSON, &rules); err != nil {
			return err
		}
		if len(rules) == 0 {
			return errors.New("uniform cache-write requires nonempty token tiers")
		}
		for index := range rules {
			rules[index].Sale = prices(rules[index].Sale)
		}
		raw, err := common.Marshal(rules)
		if err != nil {
			return err
		}
		publication.TokenPriceRulesJSON = string(raw)
	}
	// A single retail rate has no single corresponding official TTL rate.
	delete(publication.OfficialUSD, ZTAPIBillingDimensionCacheWrite5m)
	delete(publication.OfficialUSD, ZTAPIBillingDimensionCacheWrite1h)
	for index := range publication.TokenOfficialRules {
		delete(publication.TokenOfficialRules[index].OfficialUSD, ZTAPIBillingDimensionCacheWrite5m)
		delete(publication.TokenOfficialRules[index].OfficialUSD, ZTAPIBillingDimensionCacheWrite1h)
	}
	return nil
}

func ztapiFableCashFloorCacheWritePrice(values map[string]string) (string, error) {
	price := values[ZTAPIBillingDimensionCacheWrite5m]
	uniform, err := decimal.NewFromString(price)
	if err != nil || !uniform.IsPositive() {
		return "", errors.New("cash-floor cache-write requires a positive cache_write_5m retail price")
	}
	hourly, err := decimal.NewFromString(values[ZTAPIBillingDimensionCacheWrite1h])
	if err != nil || !hourly.IsPositive() || !hourly.Equal(uniform) {
		return "", errors.New("cash-floor cache-write requires equal positive TTL retail prices")
	}
	if raw, exists := values[ZTAPIBillingDimensionCacheWrite]; exists {
		generic, err := decimal.NewFromString(raw)
		if err != nil || !generic.IsPositive() || !generic.Equal(uniform) {
			return "", errors.New("cash-floor cache-write generic retail price conflicts with TTL prices")
		}
	}
	return price, nil
}

func applyZTAPIFableCashFloorCacheWrite(publication *ZTAPIRuntimePublication) error {
	if publication.PricePolicy != string(ZTAPIPricePolicyCashPool20261030) {
		return errors.New("cash-floor cache-write requires cash_pool_30_20261030 price policy")
	}
	price, err := ztapiFableCashFloorCacheWritePrice(publication.SaleUSD)
	if err != nil {
		return err
	}
	var rules []struct {
		Conditions []string          `json:"conditions"`
		Sale       map[string]string `json:"sale"`
	}
	if err := common.UnmarshalJsonStr(publication.TokenPriceRulesJSON, &rules); err != nil {
		return fmt.Errorf("cash-floor cache-write requires valid token tiers: %w", err)
	}
	if len(rules) == 0 {
		return errors.New("cash-floor cache-write requires nonempty token tiers")
	}
	uniform, _ := decimal.NewFromString(price)
	for index := range rules {
		tierPrice, err := ztapiFableCashFloorCacheWritePrice(rules[index].Sale)
		if err != nil {
			return fmt.Errorf("cash-floor cache-write token tier %d: %w", index, err)
		}
		tier, _ := decimal.NewFromString(tierPrice)
		if !tier.Equal(uniform) {
			return fmt.Errorf("cash-floor cache-write token tier %d differs from publication retail price", index)
		}
	}
	prices := func(values map[string]string) map[string]string {
		result := copyZTAPIStringMap(values)
		delete(result, ZTAPIBillingDimensionCacheWrite5m)
		delete(result, ZTAPIBillingDimensionCacheWrite1h)
		result[ZTAPIBillingDimensionCacheWrite] = price
		return result
	}
	for index := range rules {
		rules[index].Sale = prices(rules[index].Sale)
	}
	raw, err := common.Marshal(rules)
	if err != nil {
		return err
	}

	// Stage the projection only after every frozen retail tier has been validated.
	projected := *publication
	projected.BillingDimensions = make([]string, 0, len(publication.BillingDimensions)+1)
	for _, dimension := range publication.BillingDimensions {
		if dimension != ZTAPIBillingDimensionCacheWrite && dimension != ZTAPIBillingDimensionCacheWrite5m && dimension != ZTAPIBillingDimensionCacheWrite1h {
			projected.BillingDimensions = append(projected.BillingDimensions, dimension)
		}
	}
	projected.BillingDimensions = append(projected.BillingDimensions, ZTAPIBillingDimensionCacheWrite)
	projected.SaleUSD = prices(publication.SaleUSD)
	projected.TokenPriceRulesJSON = string(raw)
	projected.OfficialUSD = copyZTAPIStringMap(publication.OfficialUSD)
	delete(projected.OfficialUSD, ZTAPIBillingDimensionCacheWrite5m)
	delete(projected.OfficialUSD, ZTAPIBillingDimensionCacheWrite1h)
	projected.TokenOfficialRules = cloneZTAPIPublicTokenPriceRules(publication.TokenOfficialRules)
	for index := range projected.TokenOfficialRules {
		delete(projected.TokenOfficialRules[index].OfficialUSD, ZTAPIBillingDimensionCacheWrite5m)
		delete(projected.TokenOfficialRules[index].OfficialUSD, ZTAPIBillingDimensionCacheWrite1h)
	}
	*publication = projected
	return nil
}

// PublishZTAPIFableUniformCacheWrite creates a new immutable retail version.
// It does not settle requests, mutate balances, or change procurement/routing.
func PublishZTAPIFableUniformCacheWrite(operatorID int, expectedVersion uint64) (*ZTAPIModelPublicationSnapshot, error) {
	if DB == nil || operatorID <= 0 || expectedVersion == 0 {
		return nil, errors.New("operator and expected version required")
	}
	var result *ZTAPIModelPublicationSnapshot
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var config ZTAPIModelConfig
		if err := tx.Where("public_name = ? AND published = ?", "zt-claude-fable-5", true).First(&config).Error; err != nil {
			return err
		}
		if config.Version != expectedVersion {
			return ErrZTAPIModelVersionConflict
		}
		var previous ZTAPIModelPublicationSnapshot
		if err := tx.First(&previous, config.PublicationSnapshotID).Error; err != nil {
			return err
		}
		if previous.CacheWritePricingMode == ZTAPIFableCashFloorCacheWriteMode || isZTAPICashMarginPolicy20261030(previous.PricePolicy) {
			return errors.New("Fable cash-floor pricing cannot be replaced by the legacy fixed cache-write rate")
		}
		publications, err := loadZTAPIActivePublications(tx)
		if err != nil {
			return err
		}
		found := false
		for _, publication := range publications {
			found = found || publication.ModelConfigID == config.ID
		}
		if !found {
			return errors.New("Fable requires a valid current publication")
		}
		if previous.SourceModel != config.SourceModel || previous.PublicName != config.PublicNameValue() || previous.Protocol != config.Protocol || previous.ProviderFamily != config.ProviderFamily {
			return errors.New("Fable current identity differs from its publication")
		}
		if previous.CacheWritePricingMode == ZTAPIFableUniformCacheWriteMode {
			result = &previous
			return nil
		}
		var verificationIDs []int64
		if err := common.UnmarshalJsonStr(previous.VerificationIDs, &verificationIDs); err != nil {
			return err
		}
		// Copy immutable published values, never mutable draft retail fields.
		config.InputPricePerMillion, config.OutputPricePerMillion = previous.InputPricePerMillion, previous.OutputPricePerMillion
		config.CacheReadRatio, config.CacheCreationRatio = previous.CacheReadRatio, previous.CacheCreationRatio
		config.CacheCreation5mRatio, config.CacheCreation1hRatio = previous.CacheCreation5mRatio, previous.CacheCreation1hRatio
		config.ImageRatio, config.AudioRatio, config.AudioCompletionRatio = previous.ImageRatio, previous.AudioRatio, previous.AudioCompletionRatio
		config.EnabledGroups = previous.EnabledGroups
		result, err = createZTAPIModelPublicationSnapshotTx(tx, &config, config.Version+1, ztapiPublicationEvidence{
			AllowedChannelIDs: previous.ChannelIDs(), VerificationIDs: verificationIDs, PriceSourceID: previous.PriceSourceID,
			IdentityUpdatedAt: previous.IdentityUpdatedAt, CacheWritePricingMode: ZTAPIFableUniformCacheWriteMode,
		})
		if err != nil {
			return err
		}
		now := common.GetTimestamp()
		updated := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ?", config.ID, expectedVersion).
			Updates(map[string]any{"publication_snapshot_id": result.ID, "version": result.ModelVersion, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		return tx.Create(&ZTAPIAuditEvent{Action: "model.fable_uniform_cache_write_published", ModelConfigID: config.ID,
			PublicName: previous.PublicName, Version: result.ModelVersion, OperatorID: operatorID, CreatedAt: now,
			Payload: common.MapToJsonStr(map[string]any{"previous_snapshot_id": previous.ID, "snapshot_id": result.ID,
				"cache_write_price_per_million": ZTAPIFableUniformCacheWritePrice, "mode": result.CacheWritePricingMode,
				"historical_repricing": false, "allowed_channels": previous.ChannelIDs()})}).Error
	})
	if err != nil {
		return nil, err
	}
	invalidateZTAPICatalogCaches()
	return result, nil
}
