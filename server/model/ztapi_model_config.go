/*
Copyright (C) 2025 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/

package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

const (
	ZTAPIModelFamilyOpenAI = "openai"
	ZTAPIModelFamilyClaude = "claude"
	ZTAPIModelFamilyGemini = "gemini"

	ZTAPIProtocolOpenAICompatible = "openai_compatible"
	ZTAPIProtocolAnthropic        = "anthropic"
	ZTAPIProtocolGemini           = "gemini"

	ZTAPIProviderOpenAI    = "openai"
	ZTAPIProviderAnthropic = "anthropic"
	ZTAPIProviderGoogle    = "google"
	ZTAPIProviderDeepSeek  = "deepseek"
	ZTAPIProviderDoubao    = "doubao"
	ZTAPIProviderQwen      = "qwen"
	ZTAPIProviderMoonshot  = "moonshot"
	ZTAPIProviderGLM       = "glm"
	ZTAPIProviderMiniMax   = "minimax"
	ZTAPIProviderSeedance  = "seedance"
	ZTAPIProviderSeedream  = "seedream"
	ZTAPIProviderVidu      = "vidu"
	ZTAPIProviderOther     = "other"

	ztapiDefaultCacheReadRatio       = 0.1
	ztapiDefaultCacheCreationRatio   = 1.25
	ztapiDefaultCacheCreation5mRatio = 1.25
	ztapiDefaultCacheCreation1hRatio = 2.0
	ztapiDefaultImageRatio           = 1.0
	ztapiDefaultAudioRatio           = 1.0
)

var (
	ErrZTAPIModelVersionConflict = errors.New("ztapi model version conflict")
	ErrZTAPIModelNotPublic       = errors.New("ztapi model is not public")
	ErrZTAPIModelGroupForbidden  = errors.New("ztapi model is not enabled for this group")
)

type ZTAPIModelConfig struct {
	ID                    int     `json:"id" gorm:"primaryKey"`
	SourceModel           string  `json:"source_model" gorm:"size:255;not null;uniqueIndex"`
	PublicName            *string `json:"public_name,omitempty" gorm:"size:255;uniqueIndex"`
	Family                string  `json:"family" gorm:"size:32;not null;default:''"`
	Protocol              string  `json:"protocol" gorm:"size:32;not null;default:'';index"`
	ProviderFamily        string  `json:"provider_family" gorm:"size:32;not null;default:'';index"`
	InputCostPerMillion   float64 `json:"input_cost_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	OutputCostPerMillion  float64 `json:"output_cost_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	InputPricePerMillion  float64 `json:"input_price_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	OutputPricePerMillion float64 `json:"output_price_per_million" gorm:"type:decimal(20,8);not null;default:0"`
	CacheReadRatio        float64 `json:"cache_read_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	CacheCreationRatio    float64 `json:"cache_creation_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	CacheCreation5mRatio  float64 `json:"cache_creation_5m_ratio" gorm:"column:cache_creation_5m_ratio;type:decimal(20,8);not null;default:0"`
	CacheCreation1hRatio  float64 `json:"cache_creation_1h_ratio" gorm:"column:cache_creation_1h_ratio;type:decimal(20,8);not null;default:0"`
	ImageRatio            float64 `json:"image_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	AudioRatio            float64 `json:"audio_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	AudioCompletionRatio  float64 `json:"audio_completion_ratio" gorm:"type:decimal(20,8);not null;default:0"`
	EnabledGroups         string  `json:"-" gorm:"type:text;not null"`
	Published             bool    `json:"published" gorm:"not null;default:false;index"`
	PublicationSnapshotID int64   `json:"publication_snapshot_id" gorm:"not null;default:0;index"`
	Version               uint64  `json:"version" gorm:"not null;default:1"`
	CreatedAt             int64   `json:"created_at" gorm:"bigint;not null;default:0"`
	UpdatedAt             int64   `json:"updated_at" gorm:"bigint;not null;default:0"`
}

func (ZTAPIModelConfig) TableName() string { return "ztapi_model_configs" }

func (config ZTAPIModelConfig) ReasoningCapability() (ZTAPIReasoningCapability, bool) {
	return ZTAPIReasoningCapabilityFor(config.SourceModel)
}

func legacyZTAPIModelDimensions(family string) (string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case ZTAPIModelFamilyOpenAI:
		return ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, true
	case ZTAPIModelFamilyClaude:
		return ZTAPIProtocolAnthropic, ZTAPIProviderAnthropic, true
	case ZTAPIModelFamilyGemini:
		return ZTAPIProtocolGemini, ZTAPIProviderGoogle, true
	default:
		return "", "", false
	}
}

// BackfillZTAPIModelDimensions adds explicit transport and provider metadata to
// legacy catalog rows. Existing explicit values remain authoritative.
func BackfillZTAPIModelDimensions(db *gorm.DB) error {
	if db == nil {
		return errors.New("ztapi model dimension database is nil")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var configs []ZTAPIModelConfig
		if err := tx.Order("id ASC").Find(&configs).Error; err != nil {
			return err
		}
		for i := range configs {
			protocol, provider, ok := legacyZTAPIModelDimensions(configs[i].Family)
			if !ok {
				continue
			}
			updates := map[string]any{}
			if strings.TrimSpace(configs[i].Protocol) == "" {
				updates["protocol"] = protocol
			}
			if strings.TrimSpace(configs[i].ProviderFamily) == "" {
				updates["provider_family"] = provider
			}
			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&ZTAPIModelConfig{}).Where("id = ?", configs[i].ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func finitePositiveZTAPIRatio(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (c *ZTAPIModelConfig) HasCompletePricing() bool {
	if c == nil {
		return false
	}
	if ZTAPIModelModality(c.SourceModel) == ZTAPIModalityEmbedding {
		return finitePositiveZTAPIRatio(c.InputPricePerMillion) && c.OutputPricePerMillion == 0
	}
	return finitePositiveZTAPIRatio(c.InputPricePerMillion) &&
		finitePositiveZTAPIRatio(c.OutputPricePerMillion) &&
		finitePositiveZTAPIRatio(c.CacheReadRatio) &&
		finitePositiveZTAPIRatio(c.CacheCreationRatio) &&
		finitePositiveZTAPIRatio(c.CacheCreation5mRatio) &&
		finitePositiveZTAPIRatio(c.CacheCreation1hRatio) &&
		finitePositiveZTAPIRatio(c.ImageRatio) &&
		finitePositiveZTAPIRatio(c.AudioRatio) &&
		finitePositiveZTAPIRatio(c.AudioCompletionRatio)
}

type ztapiPricingRatioSnapshot struct {
	CacheReadRatio       float64 `json:"cache_read_ratio"`
	CacheCreationRatio   float64 `json:"cache_creation_ratio"`
	CacheCreation5mRatio float64 `json:"cache_creation_5m_ratio"`
	CacheCreation1hRatio float64 `json:"cache_creation_1h_ratio"`
	ImageRatio           float64 `json:"image_ratio"`
	AudioRatio           float64 `json:"audio_ratio"`
	AudioCompletionRatio float64 `json:"audio_completion_ratio"`
}

func snapshotZTAPIPricingRatios(config ZTAPIModelConfig) ztapiPricingRatioSnapshot {
	return ztapiPricingRatioSnapshot{
		CacheReadRatio:       config.CacheReadRatio,
		CacheCreationRatio:   config.CacheCreationRatio,
		CacheCreation5mRatio: config.CacheCreation5mRatio,
		CacheCreation1hRatio: config.CacheCreation1hRatio,
		ImageRatio:           config.ImageRatio,
		AudioRatio:           config.AudioRatio,
		AudioCompletionRatio: config.AudioCompletionRatio,
	}
}

func applyZTAPIDefaultPricingRatios(config *ZTAPIModelConfig) (bool, error) {
	if config == nil {
		return false, errors.New("ztapi model config is nil")
	}
	if ZTAPIModelModality(config.SourceModel) == ZTAPIModalityEmbedding {
		if config.Published && !config.HasCompletePricing() {
			return false, errors.New("published embedding requires positive input price and zero output price")
		}
		return false, nil
	}
	if config.Published && (!finitePositiveZTAPIRatio(config.InputPricePerMillion) || !finitePositiveZTAPIRatio(config.OutputPricePerMillion)) {
		return false, fmt.Errorf("published ZTAPI model %s requires positive input and output prices before ratio backfill", config.SourceModel)
	}

	changed := false
	setDefault := func(value *float64, fallback float64) {
		if finitePositiveZTAPIRatio(*value) {
			return
		}
		*value = fallback
		changed = true
	}
	setDefault(&config.CacheReadRatio, ztapiDefaultCacheReadRatio)
	setDefault(&config.CacheCreationRatio, ztapiDefaultCacheCreationRatio)
	setDefault(&config.CacheCreation5mRatio, ztapiDefaultCacheCreation5mRatio)
	setDefault(&config.CacheCreation1hRatio, ztapiDefaultCacheCreation1hRatio)
	setDefault(&config.ImageRatio, ztapiDefaultImageRatio)
	setDefault(&config.AudioRatio, ztapiDefaultAudioRatio)

	audioCompletionRatio := 1.0
	if finitePositiveZTAPIRatio(config.InputPricePerMillion) && finitePositiveZTAPIRatio(config.OutputPricePerMillion) {
		audioCompletionRatio = config.OutputPricePerMillion / config.InputPricePerMillion
	}
	if !finitePositiveZTAPIRatio(audioCompletionRatio) {
		return false, fmt.Errorf("ZTAPI model %s has an invalid derived audio completion ratio", config.SourceModel)
	}
	setDefault(&config.AudioCompletionRatio, audioCompletionRatio)
	return changed, nil
}

func backfillZTAPIPricingRatios() error {
	changedAny := false
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var configs []ZTAPIModelConfig
		if err := tx.Where("published = ?", true).Order("id ASC").Find(&configs).Error; err != nil {
			return err
		}
		now := common.GetTimestamp()
		for i := range configs {
			before := snapshotZTAPIPricingRatios(configs[i])
			changed, err := applyZTAPIDefaultPricingRatios(&configs[i])
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			nextVersion := configs[i].Version + 1
			result := tx.Model(&ZTAPIModelConfig{}).
				Where("id = ? AND version = ?", configs[i].ID, configs[i].Version).
				Updates(map[string]any{
					"cache_read_ratio":        configs[i].CacheReadRatio,
					"cache_creation_ratio":    configs[i].CacheCreationRatio,
					"cache_creation_5m_ratio": configs[i].CacheCreation5mRatio,
					"cache_creation_1h_ratio": configs[i].CacheCreation1hRatio,
					"image_ratio":             configs[i].ImageRatio,
					"audio_ratio":             configs[i].AudioRatio,
					"audio_completion_ratio":  configs[i].AudioCompletionRatio,
					"version":                 nextVersion,
					"updated_at":              now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrZTAPIModelVersionConflict
			}
			payload, err := json.Marshal(map[string]any{
				"migration": "ztapi_multimodal_ratio_v1",
				"before":    before,
				"after":     snapshotZTAPIPricingRatios(configs[i]),
			})
			if err != nil {
				return err
			}
			if err := tx.Create(&ZTAPIAuditEvent{
				Action:        "model.pricing_backfill",
				ModelConfigID: configs[i].ID,
				PublicName:    configs[i].PublicNameValue(),
				Version:       nextVersion,
				OperatorID:    0,
				Payload:       string(payload),
				CreatedAt:     now,
			}).Error; err != nil {
				return err
			}
			changedAny = true
		}
		return nil
	})
	if err == nil && changedAny {
		invalidateZTAPICatalogCaches()
	}
	return err
}

type ZTAPIAuditEvent struct {
	ID            int    `json:"id" gorm:"primaryKey"`
	Action        string `json:"action" gorm:"size:64;not null;index"`
	ModelConfigID int    `json:"model_config_id" gorm:"not null;index"`
	PublicName    string `json:"public_name" gorm:"size:255;not null"`
	Version       uint64 `json:"version" gorm:"not null"`
	OperatorID    int    `json:"operator_id" gorm:"not null"`
	Payload       string `json:"payload" gorm:"type:text;not null"`
	CreatedAt     int64  `json:"created_at" gorm:"bigint;not null"`
	DeliveredAt   int64  `json:"delivered_at" gorm:"bigint;not null;default:0"`
}

func (ZTAPIAuditEvent) TableName() string { return "ztapi_audit_events" }

func (c *ZTAPIModelConfig) PublicNameValue() string {
	if c == nil || c.PublicName == nil {
		return ""
	}
	return strings.TrimSpace(*c.PublicName)
}

func (c *ZTAPIModelConfig) Groups() []string {
	if c == nil || strings.TrimSpace(c.EnabledGroups) == "" {
		return []string{}
	}
	var groups []string
	if err := json.Unmarshal([]byte(c.EnabledGroups), &groups); err != nil {
		return []string{}
	}
	return normalizeZTAPIGroups(groups)
}

func normalizeZTAPIGroups(groups []string) []string {
	seen := make(map[string]struct{}, len(groups))
	normalized := make([]string, 0, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		normalized = append(normalized, group)
	}
	sort.Strings(normalized)
	return normalized
}

func EncodeZTAPIGroups(groups []string) (string, error) {
	encoded, err := json.Marshal(normalizeZTAPIGroups(groups))
	return string(encoded), err
}

func IsSupportedZTAPIModelFamily(family string) bool {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case ZTAPIModelFamilyOpenAI, ZTAPIModelFamilyClaude, ZTAPIModelFamilyGemini:
		return true
	default:
		return false
	}
}

func InferZTAPIModelFamily(sourceModel string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(sourceModel))
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	switch {
	case strings.Contains(name, "claude"):
		return ZTAPIModelFamilyClaude, true
	case strings.Contains(name, "gemini"):
		return ZTAPIModelFamilyGemini, true
	case strings.HasPrefix(name, "glm-"):
		// AIHub exposes GLM through its OpenAI-compatible chat adapter. Family
		// describes the wire adapter here, while ProviderFamily remains "glm".
		return ZTAPIModelFamilyOpenAI, true
	case strings.HasPrefix(name, "gpt-"), strings.HasPrefix(name, "chatgpt-"),
		strings.HasPrefix(name, "o1"), strings.HasPrefix(name, "o3"), strings.HasPrefix(name, "o4"),
		strings.HasPrefix(name, "text-embedding-3-"), name == "text-embedding-ada-002":
		return ZTAPIModelFamilyOpenAI, true
	default:
		return "", false
	}
}

func ztapiTrustedHost(baseURL string, channelType int, hosts ...string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" && channelType >= 0 && channelType < len(constant.ChannelBaseURLs) {
		baseURL = constant.ChannelBaseURLs[channelType]
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(host), "."))
		if hostname == host || strings.HasSuffix(hostname, "."+host) {
			return true
		}
	}
	return false
}

func ztapiFamilyForTrustedRoute(channelType int, baseURL string, managed bool, managedFamily string) (string, bool) {
	if managed {
		managedFamily = strings.ToLower(strings.TrimSpace(managedFamily))
		switch channelType {
		case constant.ChannelTypeOpenAI:
			return managedFamily, managedFamily == ZTAPIModelFamilyOpenAI
		case constant.ChannelTypeAnthropic:
			return managedFamily, managedFamily == ZTAPIModelFamilyClaude
		case constant.ChannelTypeGemini:
			return managedFamily, managedFamily == ZTAPIModelFamilyGemini
		default:
			return "", false
		}
	}
	switch channelType {
	case constant.ChannelTypeOpenAI:
		if ztapiTrustedHost(baseURL, channelType, "api.openai.com") {
			return ZTAPIModelFamilyOpenAI, true
		}
	case constant.ChannelTypeAzure:
		if ztapiTrustedHost(baseURL, channelType, "openai.azure.com", "cognitiveservices.azure.com") {
			return ZTAPIModelFamilyOpenAI, true
		}
	case constant.ChannelTypeCodex:
		if ztapiTrustedHost(baseURL, channelType, "chatgpt.com") {
			return ZTAPIModelFamilyOpenAI, true
		}
	case constant.ChannelTypeAnthropic:
		if ztapiTrustedHost(baseURL, channelType, "api.anthropic.com") {
			return ZTAPIModelFamilyClaude, true
		}
	case constant.ChannelTypeGemini:
		if ztapiTrustedHost(baseURL, channelType, "generativelanguage.googleapis.com") {
			return ZTAPIModelFamilyGemini, true
		}
	}
	return "", false
}

// ResolveZTAPIModelFamilyFromRoutes derives commercial family from enabled
// route metadata. A display name alone is never authoritative.
func ResolveZTAPIModelFamilyFromRoutes(sourceModel string) (string, bool, error) {
	return resolveZTAPIModelFamilyFromRoutesDB(DB, sourceModel)
}

func resolveZTAPIModelFamilyFromRoutesDB(db *gorm.DB, sourceModel string) (string, bool, error) {
	type trustedRoute struct {
		ChannelType  int    `gorm:"column:channel_type"`
		BaseURL      string `gorm:"column:base_url"`
		ZTAPIManaged bool   `gorm:"column:ztapi_managed"`
		ZTAPIFamily  string `gorm:"column:ztapi_family"`
	}
	var routes []trustedRoute
	err := db.Table("abilities").
		Select("DISTINCT channels.type AS channel_type, channels.base_url AS base_url, channels.ztapi_managed AS ztapi_managed, channels.ztapi_family AS ztapi_family").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", sourceModel, true, common.ChannelStatusEnabled).
		Scan(&routes).Error
	if err != nil {
		return "", false, err
	}
	nameFamily, nameSupported := InferZTAPIModelFamily(sourceModel)
	if !nameSupported {
		return "", false, nil
	}
	families := map[string]struct{}{}
	for _, route := range routes {
		if route.ZTAPIManaged && ztapiManagedAdapterSupportsFamily(route.ChannelType, nameFamily) {
			families[nameFamily] = struct{}{}
			continue
		}
		if family, ok := ztapiFamilyForTrustedRoute(route.ChannelType, route.BaseURL, route.ZTAPIManaged, route.ZTAPIFamily); ok && family == nameFamily {
			families[family] = struct{}{}
		}
	}
	if len(families) != 1 {
		return "", false, nil
	}
	for family := range families {
		return family, true, nil
	}
	return "", false, nil
}

func GetZTAPIModelConfig(id int) (*ZTAPIModelConfig, error) {
	var config ZTAPIModelConfig
	if err := DB.First(&config, id).Error; err != nil {
		return nil, err
	}
	return &config, nil
}

func EnsureZTAPIModelConfigsForEnabledAbilities() error {
	now := common.GetTimestamp()
	return withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var sources []string
		if err := tx.Table("abilities").
			Select("DISTINCT abilities.model").
			Joins("JOIN channels ON channels.id = abilities.channel_id").
			Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).
			Where("TRIM(abilities.model) <> ''").
			Pluck("abilities.model", &sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			source = strings.TrimSpace(source)
			if err := ValidateZTAPISourceModelNames(tx, []string{source}); err != nil {
				return err
			}
			family, _, err := resolveZTAPIModelFamilyFromRoutesDB(tx, source)
			if err != nil {
				return err
			}
			var config ZTAPIModelConfig
			err = tx.Where("source_model = ?", source).First(&config).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			config = ZTAPIModelConfig{
				SourceModel:   source,
				Family:        family,
				EnabledGroups: "[]",
				Version:       1,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			config.Protocol, config.ProviderFamily, _ = legacyZTAPIModelDimensions(family)
			if _, err := applyZTAPIDefaultPricingRatios(&config); err != nil {
				return err
			}
			if err := tx.Create(&config).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ValidateZTAPISourceModelNames prevents a later channel capability from
// stealing an existing public alias. Missing tables are tolerated during
// upgrade migrations only.
func ValidateZTAPISourceModelNames(db *gorm.DB, sourceModels []string) error {
	normalized := make([]string, 0, len(sourceModels))
	for _, source := range sourceModels {
		if source = strings.TrimSpace(source); source != "" {
			normalized = append(normalized, source)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	var conflict ZTAPIModelConfig
	err := db.Where("published = ? AND public_name IN ? AND source_model <> public_name", true, normalized).First(&conflict).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err != nil && isZTAPITableMissingError(err)) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("source model conflicts with published ZTAPI alias %s", conflict.PublicNameValue())
}

func SearchZTAPIModelConfigs(keyword string, offset, limit int) ([]ZTAPIModelConfig, int64, error) {
	query := DB.Model(&ZTAPIModelConfig{})
	keyword = strings.TrimSpace(keyword)
	if keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("source_model LIKE ? OR public_name LIKE ?", like, like)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var configs []ZTAPIModelConfig
	if err := query.Order("published DESC, source_model ASC").Offset(offset).Limit(limit).Find(&configs).Error; err != nil {
		return nil, 0, err
	}
	return configs, total, nil
}

func CountZTAPIReadyRoutes(source string, groups []string) (int64, error) {
	query := DB.Table("abilities").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.model = ? AND abilities.enabled = ? AND channels.status = ?", source, true, common.ChannelStatusEnabled)
	groups = normalizeZTAPIGroups(groups)
	if len(groups) > 0 {
		groupColumn := `abilities."group"`
		if common.UsingMySQL {
			groupColumn = "abilities.`group`"
		}
		query = query.Where(groupColumn+" IN ?", groups)
	}
	var count int64
	err := query.Distinct("abilities.channel_id").Count(&count).Error
	return count, err
}

func ZTAPIPublicNameConflictsWithSource(configID int, sourceModel, publicName string) (bool, error) {
	if publicName == "" || publicName == sourceModel {
		return false, nil
	}
	var count int64
	if err := DB.Model(&ZTAPIModelConfig{}).
		Where("id <> ? AND source_model = ?", configID, publicName).
		Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := DB.Model(&Ability{}).Where("model = ?", publicName).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func GetPublishedZTAPIModelConfigs() ([]ZTAPIModelConfig, error) {
	openIDs, err := ztapiHealthOpenModelIDs(DB)
	if err != nil {
		return nil, err
	}
	var configs []ZTAPIModelConfig
	query := DB.Where("published = ?", true)
	if len(openIDs) > 0 {
		query = query.Where("id NOT IN ?", openIDs)
	}
	err = query.Order("source_model ASC").Find(&configs).Error
	if err != nil || len(configs) == 0 {
		return configs, err
	}
	publications, err := loadZTAPIQuotedPublications(DB)
	if err != nil {
		return nil, err
	}
	allowed := make(map[int]ZTAPIRuntimePublication, len(publications))
	for _, publication := range publications {
		allowed[publication.ModelConfigID] = publication
	}
	result := make([]ZTAPIModelConfig, 0, len(configs))
	for _, config := range configs {
		if publication, ok := allowed[config.ID]; ok && publication.Version == config.Version &&
			publication.SnapshotID == config.PublicationSnapshotID && publication.SourceModel == config.SourceModel &&
			publication.PublicName == config.PublicNameValue() && publication.Protocol == config.Protocol &&
			publication.ProviderFamily == config.ProviderFamily {
			result = append(result, config)
		}
	}
	return result, nil
}

func GetAllZTAPIModelConfigs() ([]ZTAPIModelConfig, error) {
	var configs []ZTAPIModelConfig
	err := DB.Order("source_model ASC").Find(&configs).Error
	return configs, err
}

// UpdateZTAPIModelConfigAndBilling keeps its historical name for controller
// compatibility. Runtime billing now reads the publication row directly, so
// this transaction never rewrites the legacy global ratio JSON documents.
func UpdateZTAPIModelConfigAndBilling(next *ZTAPIModelConfig, expectedVersion uint64, auditEvent *ZTAPIAuditEvent) (*ZTAPIModelConfig, error) {
	var committed ZTAPIModelConfig
	err := withZTAPICatalogWrite(func(tx *gorm.DB) error {
		var current ZTAPIModelConfig
		if err := tx.First(&current, next.ID).Error; err != nil {
			return err
		}
		if current.Version != expectedVersion {
			committed = current
			return ErrZTAPIModelVersionConflict
		}
		if err := ValidateZTAPISourceModelNames(tx, []string{next.SourceModel}); err != nil {
			return err
		}
		// Identity evidence, not a legacy edit form, owns these dimensions.
		next.Protocol = current.Protocol
		next.ProviderFamily = current.ProviderFamily
		next.Family = current.Family
		if err := guardZTAPIHealthIdentityChangeTx(tx, &current, next.SourceModel, next.PublicNameValue(), next.Protocol, next.ProviderFamily); err != nil {
			return err
		}
		if err := validateZTAPIPublicNameClaimTx(tx, next.ID, next.SourceModel, next.PublicNameValue()); err != nil {
			return err
		}
		publicationSnapshotID := int64(0)
		if next.Published {
			blockers, evidence, err := ztapiPublicationBlockersTx(tx, next)
			if err != nil {
				return err
			}
			if len(blockers) > 0 {
				return &ZTAPIPublicationBlockedError{Blockers: blockers}
			}
			snapshot, err := createZTAPIModelPublicationSnapshotTx(tx, next, expectedVersion+1, evidence)
			if err != nil {
				return err
			}
			publicationSnapshotID = snapshot.ID
		}

		now := common.GetTimestamp()
		updates := map[string]interface{}{
			"source_model":             next.SourceModel,
			"public_name":              next.PublicName,
			"family":                   next.Family,
			"protocol":                 next.Protocol,
			"provider_family":          next.ProviderFamily,
			"input_cost_per_million":   next.InputCostPerMillion,
			"output_cost_per_million":  next.OutputCostPerMillion,
			"input_price_per_million":  next.InputPricePerMillion,
			"output_price_per_million": next.OutputPricePerMillion,
			"cache_read_ratio":         next.CacheReadRatio,
			"cache_creation_ratio":     next.CacheCreationRatio,
			"cache_creation_5m_ratio":  next.CacheCreation5mRatio,
			"cache_creation_1h_ratio":  next.CacheCreation1hRatio,
			"image_ratio":              next.ImageRatio,
			"audio_ratio":              next.AudioRatio,
			"audio_completion_ratio":   next.AudioCompletionRatio,
			"enabled_groups":           next.EnabledGroups,
			"published":                next.Published,
			"publication_snapshot_id":  publicationSnapshotID,
			"version":                  expectedVersion + 1,
			"updated_at":               now,
		}
		result := tx.Model(&ZTAPIModelConfig{}).Where("id = ? AND version = ?", next.ID, expectedVersion).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			if err := tx.First(&committed, next.ID).Error; err != nil {
				return err
			}
			return ErrZTAPIModelVersionConflict
		}
		if err := tx.First(&committed, next.ID).Error; err != nil {
			return err
		}
		if auditEvent != nil {
			event := *auditEvent
			event.ModelConfigID = committed.ID
			event.PublicName = committed.PublicNameValue()
			event.Version = committed.Version
			event.CreatedAt = now
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return &committed, err
	}
	invalidateZTAPICatalogCaches()
	return &committed, nil
}

type ztapiPublishedModel struct {
	ModelConfigID          int
	SnapshotID             int64
	PriceSourceID          int64
	PriceSourceVersion     uint64
	Modality               string
	SourceModel            string
	PublicName             string
	Family                 string
	ProviderFamily         string
	Protocol               string
	Groups                 []string
	AllowedChannelIDs      []int
	InputPricePerMillion   float64
	OutputPricePerMillion  float64
	CacheReadRatio         float64
	CacheCreationRatio     float64
	CacheCreation5mRatio   float64
	CacheCreation1hRatio   float64
	ImageRatio             float64
	AudioRatio             float64
	AudioCompletionRatio   float64
	BillingDimensions      []string
	SaleUSD                map[string]string
	MediaPriceContractJSON string
	InputPriceDisplay      string
	OutputPriceDisplay     string
	ImageProtocolContract  *types.ZTAPIImageProtocolContract
	VideoProtocolContract  *types.ZTAPIVideoProtocolContract
	Version                uint64
}

var ztapiPublicationCacheTTL = 5 * time.Second

var ztapiAliasCache = struct {
	sync.RWMutex
	epoch           uint64
	loaded          bool
	loadedAt        time.Time
	catalogEnforced bool
	aliases         map[string]ztapiPublishedModel
	sources         map[string]ztapiPublishedModel
}{aliases: map[string]ztapiPublishedModel{}, sources: map[string]ztapiPublishedModel{}}

func InvalidateZTAPIAliasCache() {
	ztapiAliasCache.Lock()
	ztapiAliasCache.epoch++
	ztapiAliasCache.loaded = false
	ztapiAliasCache.loadedAt = time.Time{}
	// Readers already past ensure must not fall back to direct upstream names.
	ztapiAliasCache.catalogEnforced = true
	ztapiAliasCache.aliases = map[string]ztapiPublishedModel{}
	ztapiAliasCache.sources = map[string]ztapiPublishedModel{}
	ztapiAliasCache.Unlock()
}

func refreshZTAPIAliasCache() error {
	ztapiAliasCache.RLock()
	epoch := ztapiAliasCache.epoch
	ztapiAliasCache.RUnlock()
	if DB == nil {
		return errors.New("ZTAPI database is not initialized")
	}
	configs, err := GetAllZTAPIModelConfigs()
	if err != nil && isZTAPITableMissingError(err) {
		ztapiAliasCache.Lock()
		if epoch != ztapiAliasCache.epoch {
			ztapiAliasCache.Unlock()
			return ErrZTAPIModelVersionConflict
		}
		ztapiAliasCache.epoch++
		ztapiAliasCache.aliases = map[string]ztapiPublishedModel{}
		ztapiAliasCache.sources = map[string]ztapiPublishedModel{}
		ztapiAliasCache.catalogEnforced = true
		ztapiAliasCache.loaded = true
		ztapiAliasCache.loadedAt = time.Now()
		ztapiAliasCache.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	publications, err := loadZTAPIQuotedPublications(DB)
	if err != nil {
		return err
	}
	aliases := make(map[string]ztapiPublishedModel, len(publications))
	sources := make(map[string]ztapiPublishedModel, len(configs))
	for i := range configs {
		sources[configs[i].SourceModel] = ztapiPublishedModel{SourceModel: configs[i].SourceModel}
	}
	for i := range publications {
		publication := ztapiPublishedModel{
			ModelConfigID: publications[i].ModelConfigID, SnapshotID: publications[i].SnapshotID,
			PriceSourceID: publications[i].PriceSourceID, PriceSourceVersion: publications[i].PriceSourceVersion,
			Modality:    publications[i].Modality,
			SourceModel: publications[i].SourceModel, PublicName: publications[i].PublicName,
			Family: publications[i].ProviderFamily, ProviderFamily: publications[i].ProviderFamily,
			Protocol: publications[i].Protocol, Groups: append([]string(nil), publications[i].Groups...),
			AllowedChannelIDs:     append([]int(nil), publications[i].AllowedChannelIDs...),
			InputPricePerMillion:  publications[i].InputPricePerMillion,
			OutputPricePerMillion: publications[i].OutputPricePerMillion,
			CacheReadRatio:        publications[i].CacheReadRatio,
			CacheCreationRatio:    publications[i].CacheCreationRatio,
			CacheCreation5mRatio:  publications[i].CacheCreation5mRatio,
			CacheCreation1hRatio:  publications[i].CacheCreation1hRatio,
			ImageRatio:            publications[i].ImageRatio, AudioRatio: publications[i].AudioRatio,
			AudioCompletionRatio: publications[i].AudioCompletionRatio, Version: publications[i].Version,
			BillingDimensions:      append([]string(nil), publications[i].BillingDimensions...),
			SaleUSD:                copyZTAPIStringMap(publications[i].SaleUSD),
			MediaPriceContractJSON: publications[i].MediaPriceContractJSON,
			InputPriceDisplay:      publications[i].InputPriceDisplay, OutputPriceDisplay: publications[i].OutputPriceDisplay,
			ImageProtocolContract: cloneZTAPIImageProtocolContract(publications[i].ImageProtocolContract),
			VideoProtocolContract: cloneZTAPIVideoProtocolContract(publications[i].VideoProtocolContract),
		}
		aliases[publication.PublicName] = publication
		sources[publication.SourceModel] = publication
	}
	authority := map[int]ztapiHealthPublication{}
	if len(publications) > 0 {
		authority, err = ztapiHealthPublicationAuthority(DB)
		if err != nil {
			return err
		}
	}
	ztapiAliasCache.Lock()
	if epoch != ztapiAliasCache.epoch {
		ztapiAliasCache.Unlock()
		return ErrZTAPIModelVersionConflict
	}
	ztapiAliasCache.epoch++
	ztapiAliasCache.aliases = aliases
	ztapiAliasCache.sources = sources
	filterZTAPIHealthAliasCacheLocked(authority)
	ztapiAliasCache.catalogEnforced = true
	ztapiAliasCache.loaded = true
	ztapiAliasCache.loadedAt = time.Now()
	ztapiAliasCache.Unlock()
	return nil
}

func isZTAPITableMissingError(err error) bool {
	message := strings.ToLower(err.Error())
	mentionsTable := strings.Contains(message, "ztapi_model_configs")
	return mentionsTable && (strings.Contains(message, "no such table") ||
		strings.Contains(message, "doesn't exist") || strings.Contains(message, "does not exist") ||
		strings.Contains(message, "undefined table"))
}

func ensureZTAPIAliasCache() error {
	ztapiAliasCache.RLock()
	fresh := ztapiAliasCache.loaded && time.Since(ztapiAliasCache.loadedAt) < ztapiPublicationCacheTTL
	ztapiAliasCache.RUnlock()
	if !fresh {
		if err := refreshZTAPIAliasCache(); err != nil {
			return err
		}
	}
	if err := filterZTAPIHealthAliasCache(); err != nil {
		return err
	}
	return filterZTAPIQuotationAliasCache()
}

func ResolveZTAPIPublicAlias(publicName string) (string, bool) {
	if err := ensureZTAPIAliasCache(); err != nil {
		return "", false
	}
	ztapiAliasCache.RLock()
	publication, ok := ztapiAliasCache.aliases[publicName]
	ztapiAliasCache.RUnlock()
	return publication.SourceModel, ok
}

func ztapiGroupAllowed(groups []string, group string) bool {
	group = strings.TrimSpace(group)
	if group == "" || group == "auto" {
		return true
	}
	for _, allowed := range groups {
		if allowed == "all" || allowed == group {
			return true
		}
	}
	return false
}

type ZTAPIRequestIdentity struct {
	PublicName  string
	SourceModel string
}

func ResolveZTAPICanonicalPublicName(requestedModel string) (string, bool, error) {
	if err := ensureZTAPIAliasCache(); err != nil {
		return "", false, err
	}
	ztapiAliasCache.RLock()
	publication, aliasFound := ztapiAliasCache.aliases[requestedModel]
	sourcePublication, sourceFound := ztapiAliasCache.sources[requestedModel]
	ztapiAliasCache.RUnlock()
	if aliasFound {
		return publication.PublicName, true, nil
	}
	if sourceFound && sourcePublication.PublicName != "" {
		return sourcePublication.PublicName, true, nil
	}
	return requestedModel, false, nil
}

func ResolveZTAPIRequestIdentity(requestedModel, group string) (ZTAPIRequestIdentity, error) {
	// Health state intentionally takes precedence over the filtered publication
	// cache so both the public alias and its official source alias return the
	// same temporary-unavailable result while a circuit is open.
	if err := CheckZTAPIHealthModelAvailable(requestedModel); err != nil {
		return ZTAPIRequestIdentity{}, err
	}
	if err := ensureZTAPIAliasCache(); err != nil {
		return ZTAPIRequestIdentity{}, err
	}
	ztapiAliasCache.RLock()
	publication, aliasFound := ztapiAliasCache.aliases[requestedModel]
	sourcePublication, sourceFound := ztapiAliasCache.sources[requestedModel]
	catalogEnforced := ztapiAliasCache.catalogEnforced
	ztapiAliasCache.RUnlock()
	if aliasFound {
		if !ztapiGroupAllowed(publication.Groups, group) {
			return ZTAPIRequestIdentity{}, ErrZTAPIModelGroupForbidden
		}
		return ZTAPIRequestIdentity{PublicName: publication.PublicName, SourceModel: publication.SourceModel}, nil
	}
	if sourceFound {
		if sourcePublication.PublicName == "" {
			return ZTAPIRequestIdentity{}, ErrZTAPIModelNotPublic
		}
		if !ztapiGroupAllowed(sourcePublication.Groups, group) {
			return ZTAPIRequestIdentity{}, ErrZTAPIModelGroupForbidden
		}
		return ZTAPIRequestIdentity{PublicName: sourcePublication.PublicName, SourceModel: sourcePublication.SourceModel}, nil
	}
	if catalogEnforced {
		return ZTAPIRequestIdentity{}, ErrZTAPIModelNotPublic
	}
	return ZTAPIRequestIdentity{PublicName: requestedModel, SourceModel: requestedModel}, nil
}

func ResolveZTAPIRequestModel(requestedModel, group string) (string, error) {
	identity, err := ResolveZTAPIRequestIdentity(requestedModel, group)
	if err != nil {
		return "", err
	}
	return identity.SourceModel, nil
}

func GetZTAPIPublishedSalePrice(publicName string) (float64, float64, bool, error) {
	if DB == nil {
		return 0, 0, false, nil
	}
	if err := ensureZTAPIAliasCache(); err != nil {
		return 0, 0, false, err
	}
	ztapiAliasCache.RLock()
	publication, ok := ztapiAliasCache.aliases[publicName]
	ztapiAliasCache.RUnlock()
	embedding := ZTAPIModelModality(publication.SourceModel) == ZTAPIModalityEmbedding
	if !ok || !finitePositiveZTAPIRatio(publication.InputPricePerMillion) || (embedding && publication.OutputPricePerMillion != 0) || (!embedding && !finitePositiveZTAPIRatio(publication.OutputPricePerMillion)) {
		return 0, 0, false, nil
	}
	return publication.InputPricePerMillion, publication.OutputPricePerMillion, true, nil
}

func GetZTAPIPublicationGroups(publicName string) []string {
	if err := ensureZTAPIAliasCache(); err != nil {
		return nil
	}
	ztapiAliasCache.RLock()
	publication, ok := ztapiAliasCache.aliases[publicName]
	ztapiAliasCache.RUnlock()
	if !ok {
		return nil
	}
	return append([]string(nil), publication.Groups...)
}

func MergeZTAPIAliasModelMapping(raw, publicName string) string {
	source, ok := ResolveZTAPIPublicAlias(publicName)
	if !ok || source == "" || source == publicName {
		return raw
	}
	mapping := map[string]string{}
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "{}" {
		if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
			return raw
		}
	}
	mapping[publicName] = source
	encoded, err := json.Marshal(mapping)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func ApplyZTAPIPublicPricing(pricing []Pricing) []Pricing {
	publications, err := listCachedZTAPIRuntimePublications()
	if err != nil || len(publications) == 0 {
		return []Pricing{}
	}
	return projectZTAPIPublicPricing(publications)
}

func projectZTAPIPublicPricing(publications []ZTAPIRuntimePublication) []Pricing {
	catalog := buildZTAPIPublicCatalog(publications)
	result := make([]Pricing, 0, len(catalog))
	publicationByName := make(map[string]ZTAPIRuntimePublication, len(publications))
	for _, publication := range publications {
		publicationByName[publication.PublicName] = publication
	}
	for _, public := range catalog {
		publication, ok := publicationByName[public.ModelName]
		if !ok {
			continue
		}
		item := Pricing{ModelName: public.ModelName}
		if public.Modality == ZTAPIModalityImage || public.Modality == ZTAPIModalityVideo {
			item.Modality = public.Modality
		}
		item.QuotaType = 0
		item.ModelPrice = 0
		if public.Modality != ZTAPIModalityImage && public.Modality != ZTAPIModalityVideo {
			item.ModelRatio = publication.InputPricePerMillion / 2
			item.CompletionRatio = publication.OutputPricePerMillion / publication.InputPricePerMillion
		}
		cacheReadRatio := publication.CacheReadRatio
		cacheCreationRatio := publication.CacheCreationRatio
		imageRatio := publication.ImageRatio
		audioRatio := publication.AudioRatio
		audioCompletionRatio := publication.AudioCompletionRatio
		item.CacheRatio = &cacheReadRatio
		item.CreateCacheRatio = &cacheCreationRatio
		item.ImageRatio = &imageRatio
		item.AudioRatio = &audioRatio
		item.AudioCompletionRatio = &audioCompletionRatio
		item.EnableGroup = append([]string(nil), public.EnableGroups...)
		item.BillingMode = "ratio"
		item.BillingExpr = ""
		item.PricingVersion = public.PricingVersion
		item.ProviderFamily = public.ProviderFamily
		item.VendorName = public.ProviderName
		item.InputPricePerMillion = public.InputPricePerMillion
		item.OutputPricePerMillion = public.OutputPricePerMillion
		item.BillingDimensions = append([]string{}, public.BillingDimensions...)
		item.SaleUSD = copyZTAPIStringMap(public.SaleUSD)
		item.BillingRule = public.BillingRule
		item.SupportedEndpointTypes = append([]constant.EndpointType(nil), public.SupportedEndpointTypes...)
		if public.SupportedOptions != nil {
			options := *public.SupportedOptions
			options.Sizes = append([]string(nil), public.SupportedOptions.Sizes...)
			options.Qualities = append([]string(nil), public.SupportedOptions.Qualities...)
			options.ResponseFormats = append([]string(nil), public.SupportedOptions.ResponseFormats...)
			options.Resolutions = append([]string(nil), public.SupportedOptions.Resolutions...)
			options.DurationSeconds = append([]int(nil), public.SupportedOptions.DurationSeconds...)
			item.SupportedOptions = &options
		}
		item.PricingRules = append([]ZTAPIPublicPricingRule(nil), public.PricingRules...)
		item.BillingUnit = public.BillingUnit
		result = append(result, item)
	}
	return result
}

func FilterZTAPIPublicVendors(pricing []Pricing, vendors []PricingVendor) []PricingVendor {
	return []PricingVendor{}
}

func ProjectZTAPIPublicSupportedEndpoints(pricing []Pricing, supported map[string]common.EndpointInfo) map[string]common.EndpointInfo {
	projected := make(map[string]common.EndpointInfo)
	for _, item := range pricing {
		for _, endpointType := range item.SupportedEndpointTypes {
			if endpoint, ok := common.GetDefaultEndpointInfo(endpointType); ok {
				projected[string(endpointType)] = endpoint
			}
		}
	}
	return projected
}

func ListZTAPIAuditEvents(offset, limit int) ([]ZTAPIAuditEvent, int64, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var total int64
	if err := DB.Model(&ZTAPIAuditEvent{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var events []ZTAPIAuditEvent
	err := DB.Order("id DESC").Offset(offset).Limit(limit).Find(&events).Error
	return events, total, err
}

func ValidateZTAPIPublishedBillingOption(key, raw string) error {
	billingKeys := map[string]bool{
		"ModelRatio": true, "CompletionRatio": true, "ModelPrice": true,
		"CacheRatio": true, "CreateCacheRatio": true, "ImageRatio": true,
		"AudioRatio": true, "AudioCompletionRatio": true,
		"billing_setting.billing_mode": true, "billing_setting.billing_expr": true,
	}
	if !billingKeys[key] {
		return nil
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return err
	}
	publications, err := listCachedZTAPIRuntimePublications()
	if err != nil {
		return err
	}
	for _, publication := range publications {
		alias := publication.PublicName
		if alias == "" {
			continue
		}
		actual, exists := values[alias]
		if key != "ModelRatio" && key != "CompletionRatio" {
			if exists {
				return fmt.Errorf("published ZTAPI model %s billing is managed from the ZTAPI model console", alias)
			}
			continue
		}
		expected := publication.InputPricePerMillion / 2
		if key == "CompletionRatio" {
			expected = publication.OutputPricePerMillion / publication.InputPricePerMillion
		}
		if !exists {
			continue
		}
		actualNumber, ok := actual.(float64)
		if !ok || actualNumber != expected {
			return fmt.Errorf("published ZTAPI model %s pricing is managed from the ZTAPI model console", alias)
		}
	}
	return nil
}
