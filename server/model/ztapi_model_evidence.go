package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZTAPIBillingDimensionInputTokens  = "input_tokens"
	ZTAPIBillingDimensionOutputTokens = "output_tokens"
	ZTAPIBillingDimensionCacheRead    = "cache_read"
	ZTAPIBillingDimensionCacheWrite   = "cache_write"
	ZTAPIBillingDimensionCacheWrite5m = "cache_write_5m"
	ZTAPIBillingDimensionCacheWrite1h = "cache_write_1h"
	ZTAPIBillingDimensionImage        = "image"
	ZTAPIBillingDimensionAudio        = "audio"
	ZTAPIBillingDimensionRequest      = "request"
)

type ZTAPIDiscoverySnapshot struct {
	ID            int64  `json:"id" gorm:"primaryKey"`
	ChannelID     int    `json:"channel_id" gorm:"not null;index"`
	ModelListHash string `json:"model_list_hash" gorm:"size:64;not null;index"`
	ModelCount    int    `json:"model_count" gorm:"not null"`
	FetchedAt     int64  `json:"fetched_at" gorm:"not null;index"`
}

func (ZTAPIDiscoverySnapshot) TableName() string { return "ztapi_discovery_snapshots" }

type ZTAPIDiscoveredModel struct {
	SnapshotID  int64  `json:"snapshot_id" gorm:"primaryKey"`
	ChannelID   int    `json:"channel_id" gorm:"primaryKey;index"`
	SourceModel string `json:"source_model" gorm:"primaryKey;size:255;index"`
}

func (ZTAPIDiscoveredModel) TableName() string { return "ztapi_discovered_models" }

type ZTAPIModelIdentity struct {
	ModelConfigID     int    `json:"model_config_id" gorm:"primaryKey"`
	PublicName        string `json:"public_name" gorm:"size:255;not null;uniqueIndex"`
	Protocol          string `json:"protocol" gorm:"size:32;not null;index"`
	ProviderFamily    string `json:"provider_family" gorm:"size:32;not null;index"`
	SourceReference   string `json:"source_reference" gorm:"size:255;not null"`
	VerificationState string `json:"verification_status" gorm:"column:verification_status;size:32;not null;index"`
	OperatorID        int    `json:"operator_id" gorm:"not null"`
	UpdatedAt         int64  `json:"updated_at" gorm:"bigint;not null"`
}

func (ZTAPIModelIdentity) TableName() string { return "ztapi_model_identities" }

// ZTAPIModelPriceSource stores decimal values as strings backed by fixed
// decimal columns. Arithmetic converts them to decimal.Decimal at the boundary.
type ZTAPIModelPriceSource struct {
	ID                     int64  `json:"id" gorm:"primaryKey"`
	ModelConfigID          int    `json:"model_config_id" gorm:"not null;index"`
	SourceModel            string `json:"source_model" gorm:"size:255;not null;index"`
	ResourceType           string `json:"resource_type" gorm:"size:32;not null;index"`
	PricePolicy            string `json:"price_policy" gorm:"size:32;not null;default:enterprise_40_margin"`
	MediaPriceContractJSON string `json:"media_price_contract,omitempty" gorm:"column:media_price_contract_json;type:text"`
	SpendTier              string `json:"spend_tier" gorm:"size:32;not null"`
	BillingDimensions      string `json:"-" gorm:"type:text;not null"`
	Currency               string `json:"currency" gorm:"size:8;not null"`
	InputPerMillion        string `json:"input_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	OutputPerMillion       string `json:"output_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	CacheReadPerMillion    string `json:"cache_read_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	CacheWritePerMillion   string `json:"cache_write_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	CacheWrite5mPerMillion string `json:"cache_write_5m_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	CacheWrite1hPerMillion string `json:"cache_write_1h_per_million" gorm:"type:decimal(24,10);not null;default:0"`
	ImageUnitCost          string `json:"image_unit_cost" gorm:"type:decimal(24,10);not null;default:0"`
	AudioUnitCost          string `json:"audio_unit_cost" gorm:"type:decimal(24,10);not null;default:0"`
	RequestUnitCost        string `json:"request_unit_cost" gorm:"type:decimal(24,10);not null;default:0"`
	CNYPerUSD              string `json:"cny_per_usd" gorm:"type:decimal(24,10);not null;default:0"`
	QuotationEffectiveAt   int64  `json:"quotation_effective_at" gorm:"bigint;not null;index"`
	SourceDocumentChecksum string `json:"source_document_checksum" gorm:"size:64;not null;index"`
	OperatorID             int    `json:"operator_id" gorm:"not null"`
	Version                uint64 `json:"version" gorm:"not null;default:1"`
	CreatedAt              int64  `json:"created_at" gorm:"bigint;not null"`
}

func (ZTAPIModelPriceSource) TableName() string { return "ztapi_model_price_sources" }

type ZTAPIModelPricePreview struct {
	Currency                string            `json:"currency"`
	PricePolicy             string            `json:"price_policy"`
	CNYPerUSD               string            `json:"cny_per_usd,omitempty"`
	BillingDimensions       []string          `json:"billing_dimensions"`
	CostUSD                 map[string]string `json:"cost_usd"`
	SaleUSD                 map[string]string `json:"sale_usd"`
	InputCostUSDPerMillion  string            `json:"input_cost_usd_per_million"`
	OutputCostUSDPerMillion string            `json:"output_cost_usd_per_million"`
	InputSaleUSDPerMillion  string            `json:"input_sale_usd_per_million"`
	OutputSaleUSDPerMillion string            `json:"output_sale_usd_per_million"`
}

type ZTAPIModelIdentityUpdate struct {
	ModelConfigID   int
	SourceModel     string
	PublicName      string
	Protocol        string
	ProviderFamily  string
	SourceReference string
	Reason          string
	ExpectedVersion uint64
	OperatorID      int
}

type ZTAPIModelPriceSourceImport struct {
	Source          ZTAPIModelPriceSource
	ExpectedVersion uint64
	OperatorID      int
	Reason          string
}

var ztapiPriceSourceChecksumPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

var (
	ztapiCNYFXBufferFactor = decimal.RequireFromString("1.03")
)

func CalculateZTAPISalePrice(cost decimal.Decimal) decimal.Decimal {
	price, _ := CalculateZTAPISalePriceForPolicy(cost, ZTAPIPricePolicyEnterprise40Margin)
	return price
}

func ConvertZTAPICNYCostToUSD(cost, cnyPerUSD decimal.Decimal) (decimal.Decimal, error) {
	if cost.IsNegative() {
		return decimal.Zero, errors.New("price cost cannot be negative")
	}
	if !cnyPerUSD.IsPositive() {
		return decimal.Zero, errors.New("CNY-per-USD exchange rate must be positive")
	}
	return cost.Div(cnyPerUSD).Mul(ztapiCNYFXBufferFactor).Round(10), nil
}

func HighestZTAPICost(costs ...decimal.Decimal) (decimal.Decimal, error) {
	if len(costs) == 0 {
		return decimal.Zero, errors.New("at least one cost is required")
	}
	highest := costs[0]
	for _, cost := range costs {
		if cost.IsNegative() {
			return decimal.Zero, errors.New("price cost cannot be negative")
		}
		if cost.GreaterThan(highest) {
			highest = cost
		}
	}
	return highest.Round(10), nil
}

func parseZTAPIBillingDimensions(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("billing dimensions must be a JSON string array")
	}
	allowed := map[string]bool{
		ZTAPIBillingDimensionInputTokens: true, ZTAPIBillingDimensionOutputTokens: true,
		ZTAPIBillingDimensionCacheRead: true, ZTAPIBillingDimensionCacheWrite: true,
		ZTAPIBillingDimensionCacheWrite5m: true, ZTAPIBillingDimensionCacheWrite1h: true,
		ZTAPIBillingDimensionImage: true, ZTAPIBillingDimensionAudio: true,
		ZTAPIBillingDimensionRequest: true,
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !allowed[value] {
			return nil, fmt.Errorf("unsupported billing dimension %q", value)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("duplicate billing dimension %q", value)
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil, errors.New("at least one billing dimension is required")
	}
	sort.Strings(normalized)
	return normalized, nil
}

func ztapiPriceSourceValues(source *ZTAPIModelPriceSource) map[string]string {
	return map[string]string{
		ZTAPIBillingDimensionInputTokens:  source.InputPerMillion,
		ZTAPIBillingDimensionOutputTokens: source.OutputPerMillion,
		ZTAPIBillingDimensionCacheRead:    source.CacheReadPerMillion,
		ZTAPIBillingDimensionCacheWrite:   source.CacheWritePerMillion,
		ZTAPIBillingDimensionCacheWrite5m: source.CacheWrite5mPerMillion,
		ZTAPIBillingDimensionCacheWrite1h: source.CacheWrite1hPerMillion,
		ZTAPIBillingDimensionImage:        source.ImageUnitCost,
		ZTAPIBillingDimensionAudio:        source.AudioUnitCost,
		ZTAPIBillingDimensionRequest:      source.RequestUnitCost,
	}
}

func ValidateZTAPIModelPriceSource(source *ZTAPIModelPriceSource) error {
	if source == nil {
		return errors.New("price source is nil")
	}
	if source.ModelConfigID <= 0 || strings.TrimSpace(source.SourceModel) == "" {
		return errors.New("price source model identity is required")
	}
	if strings.TrimSpace(source.ResourceType) == "" || strings.TrimSpace(source.SpendTier) == "" {
		return errors.New("price source resource type and spend tier are required")
	}
	if err := ValidateZTAPIPricePolicy(source.ResourceType, ZTAPIPricePolicy(source.PricePolicy)); err != nil {
		return err
	}
	if source.MediaPriceContractJSON != "" {
		canonical, err := canonicalizeZTAPIMediaPriceContract(source.MediaPriceContractJSON)
		if err != nil {
			return err
		}
		source.MediaPriceContractJSON = canonical
	}
	source.Currency = strings.ToUpper(strings.TrimSpace(source.Currency))
	if source.Currency != "USD" && source.Currency != "CNY" {
		return errors.New("price source currency must be USD or CNY")
	}
	if source.QuotationEffectiveAt <= 0 || !ztapiPriceSourceChecksumPattern.MatchString(source.SourceDocumentChecksum) {
		return errors.New("price source timestamp and SHA-256 checksum are required")
	}
	dimensions, err := parseZTAPIBillingDimensions(source.BillingDimensions)
	if err != nil {
		return err
	}
	values := ztapiPriceSourceValues(source)
	if ZTAPIModelModality(source.SourceModel) == ZTAPIModalityEmbedding {
		if len(dimensions) != 1 || dimensions[0] != ZTAPIBillingDimensionInputTokens {
			return errors.New("embedding pricing requires input_tokens only")
		}
		for dimension, raw := range values {
			if dimension == ZTAPIBillingDimensionInputTokens {
				continue
			}
			value, err := decimal.NewFromString(strings.TrimSpace(raw))
			if err != nil || !value.IsZero() {
				return fmt.Errorf("embedding cannot price %s", dimension)
			}
		}
	}
	for dimension, raw := range values {
		value, err := decimal.NewFromString(strings.TrimSpace(raw))
		if err != nil || value.IsNegative() {
			return fmt.Errorf("invalid cost for billing dimension %s", dimension)
		}
	}
	for _, dimension := range dimensions {
		value, _ := decimal.NewFromString(strings.TrimSpace(values[dimension]))
		if !value.IsPositive() {
			return fmt.Errorf("missing positive cost for billed dimension %s", dimension)
		}
	}
	if source.Currency == "CNY" {
		rate, err := decimal.NewFromString(strings.TrimSpace(source.CNYPerUSD))
		if err != nil || !rate.IsPositive() {
			return errors.New("CNY price source requires a positive CNY-per-USD rate")
		}
	}
	if ZTAPIPricePolicy(source.PricePolicy) == ZTAPIPricePolicyPool60Margin {
		if err := validateZTAPIPoolPriceSource(source, dimensions, values); err != nil {
			return err
		}
	}
	return nil
}

func BuildZTAPIModelPricePreview(source *ZTAPIModelPriceSource) (*ZTAPIModelPricePreview, error) {
	if err := ValidateZTAPIModelPriceSource(source); err != nil {
		return nil, err
	}
	dimensions, _ := parseZTAPIBillingDimensions(source.BillingDimensions)
	preview := &ZTAPIModelPricePreview{
		Currency: source.Currency, PricePolicy: source.PricePolicy, BillingDimensions: dimensions,
		CostUSD: make(map[string]string, len(dimensions)), SaleUSD: make(map[string]string, len(dimensions)),
	}
	rate := decimal.NewFromInt(1)
	if source.Currency == "CNY" {
		rate = decimal.RequireFromString(strings.TrimSpace(source.CNYPerUSD))
		preview.CNYPerUSD = rate.StringFixed(10)
	}
	values := ztapiPriceSourceValues(source)
	for _, dimension := range dimensions {
		cost := decimal.RequireFromString(strings.TrimSpace(values[dimension]))
		if source.Currency == "CNY" {
			cost, _ = ConvertZTAPICNYCostToUSD(cost, rate)
		} else {
			cost = cost.Round(10)
		}
		preview.CostUSD[dimension] = cost.StringFixed(10)
		sale, _ := CalculateZTAPISalePriceForPolicy(cost, ZTAPIPricePolicy(source.PricePolicy))
		preview.SaleUSD[dimension] = sale.StringFixed(10)
	}
	preview.InputCostUSDPerMillion = preview.CostUSD[ZTAPIBillingDimensionInputTokens]
	preview.OutputCostUSDPerMillion = preview.CostUSD[ZTAPIBillingDimensionOutputTokens]
	preview.InputSaleUSDPerMillion = preview.SaleUSD[ZTAPIBillingDimensionInputTokens]
	preview.OutputSaleUSDPerMillion = preview.SaleUSD[ZTAPIBillingDimensionOutputTokens]
	if ZTAPIModelModality(source.SourceModel) == ZTAPIModalityEmbedding {
		preview.OutputCostUSDPerMillion = "0.0000000000"
		preview.OutputSaleUSDPerMillion = "0.0000000000"
	}
	return preview, nil
}

func ztapiQuotedCacheRatios(preview *ZTAPIModelPricePreview) map[string]float64 {
	ratios := make(map[string]float64)
	input, err := decimal.NewFromString(preview.InputCostUSDPerMillion)
	if err != nil || !input.IsPositive() {
		return ratios
	}
	for dimension, column := range map[string]string{
		ZTAPIBillingDimensionCacheRead:    "cache_read_ratio",
		ZTAPIBillingDimensionCacheWrite:   "cache_creation_ratio",
		ZTAPIBillingDimensionCacheWrite5m: "cache_creation_5m_ratio",
		ZTAPIBillingDimensionCacheWrite1h: "cache_creation_1h_ratio",
	} {
		cost, err := decimal.NewFromString(preview.CostUSD[dimension])
		if err == nil && cost.IsPositive() {
			ratios[column] = cost.Div(input).Round(8).InexactFloat64()
		}
	}
	return ratios
}

func IsSupportedZTAPIProtocol(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case ZTAPIProtocolOpenAICompatible, ZTAPIProtocolAnthropic, ZTAPIProtocolGemini:
		return true
	default:
		return false
	}
}

func IsSupportedZTAPIProviderFamily(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case ZTAPIProviderOpenAI, ZTAPIProviderAnthropic, ZTAPIProviderGoogle,
		ZTAPIProviderDeepSeek, ZTAPIProviderDoubao, ZTAPIProviderQwen,
		ZTAPIProviderMoonshot, ZTAPIProviderGLM, ZTAPIProviderMiniMax,
		ZTAPIProviderSeedance, ZTAPIProviderSeedream, ZTAPIProviderVidu,
		ZTAPIProviderOther:
		return true
	default:
		return false
	}
}

func ztapiLegacyFamilyForProvider(provider string) string {
	switch provider {
	case ZTAPIProviderOpenAI, ZTAPIProviderGLM:
		return ZTAPIModelFamilyOpenAI
	case ZTAPIProviderAnthropic:
		return ZTAPIModelFamilyClaude
	case ZTAPIProviderGoogle:
		return ZTAPIModelFamilyGemini
	default:
		return ""
	}
}

func UpdateZTAPIModelIdentity(update ZTAPIModelIdentityUpdate) (*ZTAPIModelConfig, *ZTAPIModelIdentity, error) {
	update.SourceModel = strings.TrimSpace(update.SourceModel)
	update.PublicName = strings.TrimSpace(update.PublicName)
	update.Protocol = strings.ToLower(strings.TrimSpace(update.Protocol))
	update.ProviderFamily = strings.ToLower(strings.TrimSpace(update.ProviderFamily))
	update.SourceReference = strings.TrimSpace(update.SourceReference)
	update.Reason = strings.TrimSpace(update.Reason)
	if update.ModelConfigID <= 0 || update.SourceModel == "" || update.PublicName == "" {
		return nil, nil, errors.New("model identity requires exact source and public model names")
	}
	if update.SourceModel == update.PublicName {
		return nil, nil, errors.New("public model name must not expose the upstream source model")
	}
	if len(update.PublicName) > 255 || update.SourceReference == "" || len(update.SourceReference) > 255 {
		return nil, nil, errors.New("model identity alias or source reference is invalid")
	}
	if !IsSupportedZTAPIProtocol(update.Protocol) || !IsSupportedZTAPIProviderFamily(update.ProviderFamily) {
		return nil, nil, errors.New("model identity protocol or provider family is unsupported")
	}
	if update.OperatorID <= 0 || update.Reason == "" {
		return nil, nil, errors.New("model identity requires an operator and reason")
	}

	var committed ZTAPIModelConfig
	var identity ZTAPIModelIdentity
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, update.ModelConfigID).Error; err != nil {
			return err
		}
		committed = current
		if current.Version != update.ExpectedVersion {
			return ErrZTAPIModelVersionConflict
		}
		if current.SourceModel != update.SourceModel {
			return errors.New("identity source model does not match the catalog record")
		}
		if err := guardZTAPIHealthIdentityChangeTx(tx, &current, update.SourceModel, update.PublicName, update.Protocol, update.ProviderFamily); err != nil {
			return err
		}
		if err := validateZTAPIPublicNameClaimTx(tx, current.ID, current.SourceModel, update.PublicName); err != nil {
			return err
		}
		now := common.GetTimestamp()
		identity = ZTAPIModelIdentity{
			ModelConfigID: current.ID, PublicName: update.PublicName,
			Protocol: update.Protocol, ProviderFamily: update.ProviderFamily,
			SourceReference: update.SourceReference, VerificationState: "mapped",
			OperatorID: update.OperatorID, UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "model_config_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"public_name", "protocol", "provider_family", "source_reference",
				"verification_status", "operator_id", "updated_at",
			}),
		}).Create(&identity).Error; err != nil {
			return err
		}
		nextVersion := current.Version + 1
		result := tx.Model(&ZTAPIModelConfig{}).
			Where("id = ? AND version = ?", current.ID, current.Version).
			Updates(map[string]any{
				"public_name": update.PublicName, "protocol": update.Protocol,
				"provider_family": update.ProviderFamily,
				"family":          ztapiLegacyFamilyForProvider(update.ProviderFamily),
				"version":         nextVersion, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		payload, err := json.Marshal(map[string]any{
			"source_reference": update.SourceReference, "protocol": update.Protocol,
			"provider_family": update.ProviderFamily, "reason": update.Reason,
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{
			Action: "model.identity_mapped", ModelConfigID: current.ID,
			PublicName: update.PublicName, Version: nextVersion,
			OperatorID: update.OperatorID, Payload: string(payload), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&committed, current.ID).Error
	})
	if err != nil {
		return &committed, nil, err
	}
	invalidateZTAPICatalogCaches()
	return &committed, &identity, nil
}

func ImportZTAPIModelPriceSource(input ZTAPIModelPriceSourceImport) (*ZTAPIModelConfig, *ZTAPIModelPriceSource, *ZTAPIModelPricePreview, error) {
	input.Source.SourceModel = strings.TrimSpace(input.Source.SourceModel)
	input.Source.ResourceType = strings.TrimSpace(input.Source.ResourceType)
	input.Source.SpendTier = strings.TrimSpace(input.Source.SpendTier)
	input.Source.SourceDocumentChecksum = strings.ToLower(strings.TrimSpace(input.Source.SourceDocumentChecksum))
	input.Source.OperatorID = input.OperatorID
	input.Reason = strings.TrimSpace(input.Reason)
	if input.OperatorID <= 0 || input.Reason == "" {
		return nil, nil, nil, errors.New("price source import requires an operator and reason")
	}
	dimensions, err := parseZTAPIBillingDimensions(input.Source.BillingDimensions)
	if err != nil {
		return nil, nil, nil, err
	}
	encodedDimensions, _ := json.Marshal(dimensions)
	input.Source.BillingDimensions = string(encodedDimensions)
	preview, err := BuildZTAPIModelPricePreview(&input.Source)
	if err != nil {
		return nil, nil, nil, err
	}

	var committed ZTAPIModelConfig
	var persisted ZTAPIModelPriceSource
	err = withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, input.Source.ModelConfigID).Error; err != nil {
			return err
		}
		committed = current
		if current.Version != input.ExpectedVersion {
			return ErrZTAPIModelVersionConflict
		}
		if current.SourceModel != input.Source.SourceModel {
			return errors.New("price source model does not match the catalog record")
		}
		if current.Published && (preview.InputCostUSDPerMillion == "" || preview.OutputCostUSDPerMillion == "") {
			return errors.New("published token model requires input and output price evidence")
		}
		var latestVersion uint64
		if err := tx.Model(&ZTAPIModelPriceSource{}).
			Where("model_config_id = ?", current.ID).
			Select("COALESCE(MAX(version), 0)").Scan(&latestVersion).Error; err != nil {
			return err
		}
		persisted = input.Source
		persisted.ID = 0
		persisted.Version = latestVersion + 1
		persisted.CreatedAt = common.GetTimestamp()
		if err := tx.Create(&persisted).Error; err != nil {
			return err
		}

		now := common.GetTimestamp()
		updates := map[string]any{"version": current.Version + 1, "updated_at": now}
		if preview.InputCostUSDPerMillion != "" && preview.OutputCostUSDPerMillion != "" {
			inputCost, _ := decimal.NewFromString(preview.InputCostUSDPerMillion)
			outputCost, _ := decimal.NewFromString(preview.OutputCostUSDPerMillion)
			inputSale, _ := decimal.NewFromString(preview.InputSaleUSDPerMillion)
			outputSale, _ := decimal.NewFromString(preview.OutputSaleUSDPerMillion)
			updates["input_cost_per_million"] = inputCost.InexactFloat64()
			updates["output_cost_per_million"] = outputCost.InexactFloat64()
			updates["input_price_per_million"] = inputSale.InexactFloat64()
			updates["output_price_per_million"] = outputSale.InexactFloat64()
			for column, ratio := range ztapiQuotedCacheRatios(preview) {
				updates[column] = ratio
			}
		}
		result := tx.Model(&ZTAPIModelConfig{}).
			Where("id = ? AND version = ?", current.ID, current.Version).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrZTAPIModelVersionConflict
		}
		payload, err := json.Marshal(map[string]any{
			"price_source_version": persisted.Version,
			"resource_type":        persisted.ResourceType, "price_policy": persisted.PricePolicy,
			"spend_tier": persisted.SpendTier,
			"currency":   persisted.Currency, "source_document_checksum": persisted.SourceDocumentChecksum,
			"quotation_effective_at": persisted.QuotationEffectiveAt, "reason": input.Reason,
		})
		if err != nil {
			return err
		}
		if err := tx.Create(&ZTAPIAuditEvent{
			Action: "model.price_source_imported", ModelConfigID: current.ID,
			PublicName: current.PublicNameValue(), Version: current.Version + 1,
			OperatorID: input.OperatorID, Payload: string(payload), CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		return tx.First(&committed, current.ID).Error
	})
	if err != nil {
		return &committed, nil, nil, err
	}
	invalidateZTAPICatalogCaches()
	return &committed, &persisted, preview, nil
}

type ZTAPIModelVerification struct {
	Modality                         string `json:"modality" gorm:"size:32;not null;default:text"`
	ID                               int64  `json:"id" gorm:"primaryKey"`
	ModelConfigID                    int    `json:"model_config_id" gorm:"not null;index"`
	ChannelID                        int    `json:"channel_id" gorm:"not null;index"`
	Protocol                         string `json:"protocol" gorm:"size:32;not null"`
	NonStreamingPassed               bool   `json:"non_streaming_passed" gorm:"not null;default:false"`
	StreamingRequired                bool   `json:"streaming_required" gorm:"not null;default:false"`
	StreamingPassed                  bool   `json:"streaming_passed" gorm:"not null;default:false"`
	UsageReconciled                  bool   `json:"usage_reconciled" gorm:"not null;default:false"`
	MediaResultValid                 bool   `json:"media_result_valid" gorm:"not null;default:false"`
	VideoCreatePassed                bool   `json:"video_create_passed" gorm:"not null;default:false"`
	VideoFetchPassed                 bool   `json:"video_fetch_passed" gorm:"not null;default:false"`
	VideoTerminalPassed              bool   `json:"video_terminal_passed" gorm:"not null;default:false"`
	VideoRestartRecoveryPassed       bool   `json:"video_restart_recovery_passed" gorm:"not null;default:false"`
	VideoSettlementIdempotencePassed bool   `json:"video_settlement_idempotence_passed" gorm:"not null;default:false"`
	InvalidKeyClassified             bool   `json:"invalid_key_classified" gorm:"not null;default:false"`
	InsufficientBalanceClassified    bool   `json:"insufficient_balance_classified" gorm:"not null;default:false"`
	RateLimitClassified              bool   `json:"rate_limit_classified" gorm:"not null;default:false"`
	TimeoutClassified                bool   `json:"timeout_classified" gorm:"not null;default:false"`
	StatusCategory                   string `json:"status_category" gorm:"size:32;not null;default:''"`
	LatencyMilliseconds              int64  `json:"latency_milliseconds" gorm:"bigint;not null;default:0"`
	PromptTokens                     int    `json:"prompt_tokens" gorm:"not null;default:0"`
	CompletionTokens                 int    `json:"completion_tokens" gorm:"not null;default:0"`
	TotalTokens                      int    `json:"total_tokens" gorm:"not null;default:0"`
	ImageProtocolContractJSON        string `json:"image_protocol_contract,omitempty" gorm:"column:image_protocol_contract_json;type:text"`
	VideoProtocolContractJSON        string `json:"video_protocol_contract,omitempty" gorm:"column:video_protocol_contract_json;type:text"`
	OperatorID                       int    `json:"operator_id" gorm:"not null"`
	VerifiedAt                       int64  `json:"verified_at" gorm:"bigint;not null;index"`
}

func (ZTAPIModelVerification) TableName() string { return "ztapi_model_verifications" }

var (
	ErrZTAPIImageProtocolEvidenceImmutable = errors.New("image protocol verification evidence is immutable; create a new evidence row")
	ErrZTAPIVideoProtocolEvidenceImmutable = errors.New("video protocol verification evidence is immutable; create a new evidence row")
	ErrZTAPIImageProtocolSnapshotImmutable = errors.New("image protocol publication snapshot identity and pricing contract are immutable")
	ErrZTAPIInvalidUpdateDestination       = fmt.Errorf("invalid ZTAPI update destination: %w", gorm.ErrInvalidData)
)

func ztapiAssignedColumns(tx *gorm.DB) (map[string]struct{}, error) {
	assigned := map[string]struct{}{}
	if tx == nil || tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Dest == nil {
		return nil, ErrZTAPIInvalidUpdateDestination
	}

	stmt := tx.Statement
	selected, restricted := stmt.SelectAndOmitColumns(false, true)
	selectedForUpdate := func(dbName string) bool {
		value, ok := selected[dbName]
		return (ok && value) || (!ok && !restricted)
	}

	value := reflect.ValueOf(stmt.Dest)
	for value.IsValid() && (value.Kind() == reflect.Ptr || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return nil, ErrZTAPIInvalidUpdateDestination
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil, ErrZTAPIInvalidUpdateDestination
	}

	if values, ok := value.Interface().(map[string]interface{}); ok {
		for name := range values {
			dbName := name
			if field := stmt.Schema.LookUpField(name); field != nil && field.DBName != "" {
				dbName = field.DBName
			}
			if selectedForUpdate(dbName) {
				assigned[dbName] = struct{}{}
			}
		}
		return assigned, nil
	}

	if value.Kind() != reflect.Struct {
		return nil, ErrZTAPIInvalidUpdateDestination
	}

	updateSchema := stmt.Schema
	if stmt.Dest != stmt.Model {
		updateStmt := &gorm.Statement{DB: tx}
		if err := updateStmt.Parse(stmt.Dest); err != nil || updateStmt.Schema == nil {
			return nil, ErrZTAPIInvalidUpdateDestination
		}
		updateSchema = updateStmt.Schema
	}
	for _, dbName := range stmt.Schema.DBNames {
		field := updateSchema.LookUpField(dbName)
		if field == nil || !field.Updatable || !selectedForUpdate(dbName) {
			continue
		}
		_, isZero := field.ValueOf(stmt.Context, value)
		if explicitlySelected, ok := selected[dbName]; (ok && explicitlySelected) || !isZero {
			assigned[dbName] = struct{}{}
		}
	}
	return assigned, nil
}

func ztapiAssignsAny(tx *gorm.DB, fieldNames ...string) (bool, error) {
	assigned, err := ztapiAssignedColumns(tx)
	if err != nil {
		return false, err
	}
	for _, name := range fieldNames {
		dbName := name
		if tx != nil && tx.Statement != nil && tx.Statement.Schema != nil {
			if field := tx.Statement.Schema.LookUpField(name); field != nil && field.DBName != "" {
				dbName = field.DBName
			}
		}
		if _, ok := assigned[dbName]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (verification *ZTAPIModelVerification) BeforeCreate(tx *gorm.DB) error {
	if verification == nil || (verification.ImageProtocolContractJSON == "" && verification.VideoProtocolContractJSON == "") {
		return nil
	}
	if verification.ImageProtocolContractJSON != "" && verification.VideoProtocolContractJSON != "" {
		return errors.New("verification evidence cannot contain both image and video protocol contracts")
	}
	var providerModel string
	if verification.VideoProtocolContractJSON != "" {
		if verification.Modality != ZTAPIModalityVideo {
			return errors.New("video protocol verification requires video modality")
		}
		contract, canonical, err := types.ParseZTAPIVideoProtocolContract(verification.VideoProtocolContractJSON)
		if err != nil {
			return err
		}
		if canonical != verification.VideoProtocolContractJSON {
			return errors.New("video protocol verification contract must be canonical")
		}
		providerModel = contract.ProviderModel
	} else {
		if verification.Modality != ZTAPIModalityImage {
			return errors.New("image protocol verification requires image modality")
		}
		contract, canonical, err := types.ParseZTAPIImageProtocolContract(verification.ImageProtocolContractJSON)
		if err != nil {
			return err
		}
		if canonical != verification.ImageProtocolContractJSON {
			return errors.New("image protocol verification contract must be canonical")
		}
		providerModel = contract.ProviderModel
	}
	var config ZTAPIModelConfig
	if err := tx.Select("source_model").First(&config, verification.ModelConfigID).Error; err != nil {
		return err
	}
	if providerModel != config.SourceModel {
		return errors.New("protocol verification provider model does not match model evidence")
	}
	return nil
}

func (verification *ZTAPIModelVerification) BeforeUpdate(tx *gorm.DB) error {
	videoAssigned, err := ztapiAssignsAny(tx, "VideoProtocolContractJSON")
	if err != nil {
		return err
	}
	if videoAssigned && (verification.VideoProtocolContractJSON != "" || verification.Modality == ZTAPIModalityVideo) {
		return ErrZTAPIVideoProtocolEvidenceImmutable
	}
	imageOrIdentityAssigned, err := ztapiAssignsAny(tx, "ImageProtocolContractJSON", "ModelConfigID", "ChannelID", "Protocol", "Modality")
	if err != nil {
		return err
	}
	if imageOrIdentityAssigned {
		return ErrZTAPIImageProtocolEvidenceImmutable
	}
	if videoAssigned {
		return ErrZTAPIVideoProtocolEvidenceImmutable
	}
	return nil
}

type ZTAPIModelPublicationSnapshot struct {
	ID                        int64   `json:"id" gorm:"primaryKey"`
	ModelConfigID             int     `json:"model_config_id" gorm:"not null;index"`
	ModelVersion              uint64  `json:"model_version" gorm:"not null;index"`
	SourceModel               string  `json:"-" gorm:"size:255;not null"`
	PublicName                string  `json:"public_name" gorm:"size:255;not null;index"`
	Protocol                  string  `json:"protocol" gorm:"size:32;not null"`
	ProviderFamily            string  `json:"provider_family" gorm:"size:32;not null"`
	EnabledGroups             string  `json:"-" gorm:"type:text;not null"`
	AllowedChannelIDs         string  `json:"-" gorm:"type:text;not null"`
	PriceSourceID             int64   `json:"price_source_id" gorm:"not null;index"`
	PricePolicy               string  `json:"price_policy" gorm:"size:32;not null;default:enterprise_40_margin"`
	MediaPriceContractJSON    string  `json:"media_price_contract,omitempty" gorm:"column:media_price_contract_json;type:text"`
	ImageProtocolContractJSON string  `json:"image_protocol_contract,omitempty" gorm:"column:image_protocol_contract_json;type:text"`
	VideoProtocolContractJSON string  `json:"video_protocol_contract,omitempty" gorm:"column:video_protocol_contract_json;type:text"`
	InputPricePerMillion      float64 `json:"input_price_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	OutputPricePerMillion     float64 `json:"output_price_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	CacheReadRatio            float64 `json:"cache_read_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	CacheCreationRatio        float64 `json:"cache_creation_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	CacheCreation5mRatio      float64 `json:"cache_creation_5m_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	CacheCreation1hRatio      float64 `json:"cache_creation_1h_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	ImageRatio                float64 `json:"image_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	AudioRatio                float64 `json:"audio_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	AudioCompletionRatio      float64 `json:"audio_completion_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	VerificationIDs           string  `json:"-" gorm:"type:text;not null"`
	IdentityUpdatedAt         int64   `json:"identity_updated_at" gorm:"bigint;not null"`
	CreatedAt                 int64   `json:"created_at" gorm:"bigint;not null;index"`
}

func (snapshot *ZTAPIModelPublicationSnapshot) BeforeCreate(tx *gorm.DB) error {
	if snapshot == nil {
		return nil
	}
	modality := ZTAPIModelModality(snapshot.SourceModel)
	imageRequired := modality == ZTAPIModalityImage
	videoRequired := modality == ZTAPIModalityVideo
	if snapshot.PriceSourceID <= 0 {
		if imageRequired || videoRequired {
			return errors.New("media publication snapshot requires a media price source")
		}
		return nil
	}
	var source ZTAPIModelPriceSource
	if err := tx.Select("model_config_id", "source_model", "resource_type", "price_policy", "media_price_contract_json").First(&source, snapshot.PriceSourceID).Error; err != nil {
		return err
	}
	if err := ValidateZTAPIPricePolicy(source.ResourceType, ZTAPIPricePolicy(source.PricePolicy)); err != nil {
		return err
	}
	if snapshot.PricePolicy != "" && snapshot.PricePolicy != source.PricePolicy {
		return errors.New("publication snapshot price policy does not match price source")
	}
	if snapshot.MediaPriceContractJSON != "" && snapshot.MediaPriceContractJSON != source.MediaPriceContractJSON {
		return errors.New("publication snapshot media price contract does not match price source")
	}
	if snapshot.ImageProtocolContractJSON != "" || imageRequired {
		contract, canonical, err := types.ParseZTAPIImageProtocolContract(snapshot.ImageProtocolContractJSON)
		if err != nil {
			return err
		}
		if canonical != snapshot.ImageProtocolContractJSON {
			return errors.New("publication snapshot image protocol contract must be canonical")
		}
		if contract.ProviderModel != snapshot.SourceModel {
			return errors.New("publication snapshot image protocol provider model does not match source model")
		}
		if imageRequired {
			if source.ModelConfigID != snapshot.ModelConfigID || source.SourceModel != snapshot.SourceModel {
				return errors.New("image publication snapshot price source identity does not match snapshot identity")
			}
			mediaContract, err := parseZTAPIMediaPriceContract(source.MediaPriceContractJSON)
			if err != nil || mediaContract.Modality != ZTAPIModalityImage {
				return errors.New("image publication snapshot requires an exact image media price contract")
			}
			mediaCanonical, err := canonicalizeZTAPIMediaPriceContract(source.MediaPriceContractJSON)
			if err != nil || mediaCanonical != source.MediaPriceContractJSON {
				return errors.New("image publication snapshot media price contract must be canonical")
			}
			if err := validateZTAPIImagePriceProtocolCompatibility(mediaContract, contract); err != nil {
				return err
			}
		}
	}
	if snapshot.VideoProtocolContractJSON != "" || videoRequired {
		contract, canonical, err := types.ParseZTAPIVideoProtocolContract(snapshot.VideoProtocolContractJSON)
		if err != nil {
			return err
		}
		if canonical != snapshot.VideoProtocolContractJSON {
			return errors.New("publication snapshot video protocol contract must be canonical")
		}
		if contract.ProviderModel != snapshot.SourceModel {
			return errors.New("publication snapshot video protocol provider model does not match source model")
		}
		if !videoRequired {
			return errors.New("video protocol contract requires video publication modality")
		}
		if source.ModelConfigID != snapshot.ModelConfigID || source.SourceModel != snapshot.SourceModel {
			return errors.New("video publication snapshot price source identity does not match snapshot identity")
		}
		mediaContract, err := parseZTAPIMediaPriceContract(source.MediaPriceContractJSON)
		if err != nil || mediaContract.Modality != ZTAPIModalityVideo {
			return errors.New("video publication snapshot requires an exact video media price contract")
		}
		mediaCanonical, err := canonicalizeZTAPIMediaPriceContract(source.MediaPriceContractJSON)
		if err != nil || mediaCanonical != source.MediaPriceContractJSON {
			return errors.New("video publication snapshot media price contract must be canonical")
		}
		if err := types.ValidateZTAPIVideoPriceProtocolCompatibility(mediaContract, contract); err != nil {
			return err
		}
	}
	snapshot.PricePolicy = source.PricePolicy
	snapshot.MediaPriceContractJSON = source.MediaPriceContractJSON
	return nil
}

func (snapshot *ZTAPIModelPublicationSnapshot) BeforeUpdate(tx *gorm.DB) error {
	assigned, err := ztapiAssignedColumns(tx)
	if err != nil {
		return err
	}
	if len(assigned) != 0 {
		return ErrZTAPIImageProtocolSnapshotImmutable
	}
	return nil
}

func (ZTAPIModelPublicationSnapshot) TableName() string {
	return "ztapi_model_publication_snapshots"
}

func (snapshot *ZTAPIModelPublicationSnapshot) ChannelIDs() []int {
	if snapshot == nil {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(snapshot.AllowedChannelIDs), &ids); err != nil {
		return nil
	}
	sort.Ints(ids)
	return ids
}

func (snapshot *ZTAPIModelPublicationSnapshot) Groups() []string {
	if snapshot == nil {
		return nil
	}
	var groups []string
	if err := json.Unmarshal([]byte(snapshot.EnabledGroups), &groups); err != nil {
		return nil
	}
	return normalizeZTAPIGroups(groups)
}
