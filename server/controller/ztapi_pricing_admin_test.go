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

package controller_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiPricingTestConfig struct {
	ID                    int     `gorm:"column:id"`
	SourceModel           string  `gorm:"column:source_model"`
	PublicName            *string `gorm:"column:public_name"`
	Family                string  `gorm:"column:family"`
	InputCostPerMillion   float64 `gorm:"column:input_cost_per_million"`
	OutputCostPerMillion  float64 `gorm:"column:output_cost_per_million"`
	InputPricePerMillion  float64 `gorm:"column:input_price_per_million"`
	OutputPricePerMillion float64 `gorm:"column:output_price_per_million"`
	CacheReadRatio        float64 `gorm:"column:cache_read_ratio"`
	CacheCreationRatio    float64 `gorm:"column:cache_creation_ratio"`
	CacheCreation5mRatio  float64 `gorm:"column:cache_creation_5m_ratio"`
	CacheCreation1hRatio  float64 `gorm:"column:cache_creation_1h_ratio"`
	ImageRatio            float64 `gorm:"column:image_ratio"`
	AudioRatio            float64 `gorm:"column:audio_ratio"`
	AudioCompletionRatio  float64 `gorm:"column:audio_completion_ratio"`
	EnabledGroups         string  `gorm:"column:enabled_groups"`
	Published             bool    `gorm:"column:published"`
	Version               uint64  `gorm:"column:version"`
	CreatedAt             int64   `gorm:"column:created_at"`
	UpdatedAt             int64   `gorm:"column:updated_at"`
}

func (ztapiPricingTestConfig) TableName() string { return "ztapi_model_configs" }

type ztapiPricingResponse struct {
	Success bool `json:"success"`
	Data    struct {
		ID                   int     `json:"id"`
		Source               string  `json:"source_model"`
		PublicName           string  `json:"public_name"`
		Family               string  `json:"family"`
		InputPrice           float64 `json:"input_price_per_million"`
		OutputPrice          float64 `json:"output_price_per_million"`
		CacheReadRatio       float64 `json:"cache_read_ratio"`
		CacheCreationRatio   float64 `json:"cache_creation_ratio"`
		CacheCreation5mRatio float64 `json:"cache_creation_5m_ratio"`
		CacheCreation1hRatio float64 `json:"cache_creation_1h_ratio"`
		ImageRatio           float64 `json:"image_ratio"`
		AudioRatio           float64 `json:"audio_ratio"`
		AudioCompletionRatio float64 `json:"audio_completion_ratio"`
		Published            bool    `json:"published"`
		RouteReady           bool    `json:"route_ready"`
		Version              uint64  `json:"version"`
	} `json:"data"`
}

func setupZTAPIPricingController(t *testing.T) (*gorm.DB, *gin.Engine) {
	t.Helper()
	db, engine := setupChannelControllerTest(t)
	model.InvalidateZTAPIAliasCache()
	model.InvalidatePricingCache()
	t.Cleanup(func() {
		model.InvalidateZTAPIAliasCache()
		model.InvalidatePricingCache()
	})
	if err := db.AutoMigrate(&model.Option{}); err != nil {
		t.Fatalf("migrate options: %v", err)
	}
	if err := db.AutoMigrate(&model.ZTAPIAuditEvent{}); err != nil {
		t.Fatalf("migrate ZTAPI audit events: %v", err)
	}
	if err := db.AutoMigrate(
		&model.ZTAPICatalogLock{}, &model.ZTAPIDiscoverySnapshot{}, &model.ZTAPIDiscoveredModel{},
		&model.ZTAPIModelIdentity{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelVerification{},
		&model.ZTAPIModelPublicationSnapshot{},
	); err != nil {
		t.Fatalf("migrate ZTAPI publication evidence: %v", err)
	}
	originalModelRatios := ratio_setting.GetModelRatioCopy()
	originalCompletionRatios := ratio_setting.GetCompletionRatioCopy()
	originalOptionMap := map[string]string{}
	common.OptionMapRWMutex.RLock()
	for key, value := range common.OptionMap {
		originalOptionMap[key] = value
	}
	common.OptionMapRWMutex.RUnlock()
	if err := ratio_setting.UpdateModelRatioByJSONString(`{}`); err != nil {
		t.Fatalf("reset model ratios: %v", err)
	}
	if err := ratio_setting.UpdateCompletionRatioByJSONString(`{}`); err != nil {
		t.Fatalf("reset completion ratios: %v", err)
	}
	for key := range originalOptionMap {
		if key == "ModelRatio" || key == "CompletionRatio" {
			continue
		}
	}
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMap["ModelRatio"] = `{}`
	common.OptionMap["CompletionRatio"] = `{}`
	common.OptionMapRWMutex.Unlock()
	if err := db.Create(&model.Option{Key: "ModelRatio", Value: `{}`}).Error; err != nil {
		t.Fatalf("seed model ratio option: %v", err)
	}
	if err := db.Create(&model.Option{Key: "CompletionRatio", Value: `{}`}).Error; err != nil {
		t.Fatalf("seed completion ratio option: %v", err)
	}
	t.Cleanup(func() {
		modelJSON, _ := json.Marshal(originalModelRatios)
		completionJSON, _ := json.Marshal(originalCompletionRatios)
		_ = ratio_setting.UpdateModelRatioByJSONString(string(modelJSON))
		_ = ratio_setting.UpdateCompletionRatioByJSONString(string(completionJSON))
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})
	return db, engine
}

func seedZTAPIModelConfig(t *testing.T, db *gorm.DB, source string) ztapiPricingTestConfig {
	t.Helper()
	config := ztapiPricingTestConfig{
		SourceModel:   source,
		EnabledGroups: `[]`,
		Version:       1,
		CreatedAt:     time.Now().Unix(),
		UpdatedAt:     time.Now().Unix(),
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("seed ztapi model config: %v", err)
	}
	return config
}

func seedZTAPIRoute(t *testing.T, db *gorm.DB, source, group string) model.Channel {
	t.Helper()
	weight := uint(100)
	priority := int64(10)
	channelType := constant.ChannelTypeOpenAI
	channel := model.Channel{
		Name:         "ztapi-route-" + source,
		Type:         channelType,
		Key:          "secret-not-returned",
		Status:       common.ChannelStatusEnabled,
		Models:       source,
		Group:        group,
		Weight:       &weight,
		Priority:     &priority,
		CreatedTime:  common.GetTimestamp(),
		ZTAPIManaged: true,
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("seed route channel: %v", err)
	}
	if err := db.Create(&model.Ability{Group: group, Model: source, ChannelId: channel.Id, Enabled: true, Priority: &priority, Weight: weight}).Error; err != nil {
		t.Fatalf("seed route ability: %v", err)
	}
	return channel
}

func seedZTAPIPublicationEvidence(
	t *testing.T,
	db *gorm.DB,
	config ztapiPricingTestConfig,
	channel model.Channel,
	publicName string,
	provider string,
	inputCost float64,
	outputCost float64,
) {
	t.Helper()
	protocol := model.ZTAPIProtocolOpenAICompatible
	family := ""
	switch provider {
	case model.ZTAPIProviderOpenAI:
		family = model.ZTAPIModelFamilyOpenAI
	case model.ZTAPIProviderAnthropic:
		family = model.ZTAPIModelFamilyClaude
	case model.ZTAPIProviderGoogle:
		family = model.ZTAPIModelFamilyGemini
	}
	now := time.Now().UTC().Unix()
	if err := db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", config.ID).Updates(map[string]any{
		"public_name": publicName, "protocol": protocol,
		"provider_family": provider, "family": family,
	}).Error; err != nil {
		t.Fatalf("seed mapped model dimensions: %v", err)
	}
	identity := model.ZTAPIModelIdentity{
		ModelConfigID: config.ID, PublicName: publicName, Protocol: protocol,
		ProviderFamily: provider, SourceReference: "supplier-confirmation",
		VerificationState: "mapped", OperatorID: 1, UpdatedAt: now,
	}
	if err := db.Save(&identity).Error; err != nil {
		t.Fatalf("seed model identity: %v", err)
	}
	snapshot := model.ZTAPIDiscoverySnapshot{
		ChannelID: channel.Id, ModelListHash: strings.Repeat("d", 64),
		ModelCount: 1, FetchedAt: now,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("seed discovery snapshot: %v", err)
	}
	if err := db.Create(&model.ZTAPIDiscoveredModel{
		SnapshotID: snapshot.ID, ChannelID: channel.Id, SourceModel: config.SourceModel,
	}).Error; err != nil {
		t.Fatalf("seed discovered model: %v", err)
	}
	var priceVersion int64
	if err := db.Model(&model.ZTAPIModelPriceSource{}).Where("model_config_id = ?", config.ID).Count(&priceVersion).Error; err != nil {
		t.Fatalf("count price sources: %v", err)
	}
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: config.SourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: fmt.Sprintf("%.10f", inputCost), OutputPerMillion: fmt.Sprintf("%.10f", outputCost),
		CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		QuotationEffectiveAt: now, SourceDocumentChecksum: model.ZTAPIQuotationSHA256,
		OperatorID: 1, Version: uint64(priceVersion + 1), CreatedAt: now,
	}
	if err := db.Create(&price).Error; err != nil {
		t.Fatalf("seed price source: %v", err)
	}
	verification := model.ZTAPIModelVerification{
		ModelConfigID: config.ID, ChannelID: channel.Id, Protocol: protocol,
		NonStreamingPassed: true, StreamingRequired: true, StreamingPassed: true,
		UsageReconciled: true, InvalidKeyClassified: true,
		InsufficientBalanceClassified: true, RateLimitClassified: true,
		TimeoutClassified: true, StatusCategory: "verified", OperatorID: 1, VerifiedAt: now,
	}
	if err := db.Create(&verification).Error; err != nil {
		t.Fatalf("seed model verification: %v", err)
	}
}

func ztapiPricingUpdateBody(version uint64, source, publicName, family string, inputCost, outputCost, inputPrice, outputPrice float64, published, confirm bool) string {
	body, _ := json.Marshal(map[string]any{
		"version":                  version,
		"source_model":             source,
		"public_name":              publicName,
		"family":                   family,
		"input_cost_per_million":   inputCost,
		"output_cost_per_million":  outputCost,
		"input_price_per_million":  inputPrice,
		"output_price_per_million": outputPrice,
		"cache_read_ratio":         0.1,
		"cache_creation_ratio":     1.1,
		"cache_creation_5m_ratio":  1.25,
		"cache_creation_1h_ratio":  2.0,
		"image_ratio":              3.0,
		"audio_ratio":              4.0,
		"audio_completion_ratio":   5.0,
		"enabled_groups":           []string{"default"},
		"published":                published,
		"confirm_below_cost":       confirm,
	})
	return string(body)
}

func decodeZTAPIPricingResponse(t *testing.T, body []byte) ztapiPricingResponse {
	t.Helper()
	var response ztapiPricingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode pricing response: %v; body=%s", err, string(body))
	}
	return response
}

func TestZTAPIModelPricingIsAuthoritativeAndRejectsUnquotedRename(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "pricing-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "gpt-5.5", "default")
	config := seedZTAPIModelConfig(t, db, "gpt-5.5")
	seedZTAPIPublicationEvidence(t, db, config, channel, "zt-gpt-5.5", model.ZTAPIProviderOpenAI, 1, 2)

	first := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(1, "gpt-5.5", "zt-gpt-5.5", "openai", 1, 2, 1.6666666667, 3.3333333333, true, false), "")
	if first.Code != http.StatusOK {
		t.Fatalf("first publish status=%d body=%s", first.Code, first.Body.String())
	}
	firstBody := decodeZTAPIPricingResponse(t, first.Body.Bytes())
	if !firstBody.Success || firstBody.Data.Version != 2 || !firstBody.Data.Published || !firstBody.Data.RouteReady {
		t.Fatalf("unexpected first publish response: %s", first.Body.String())
	}
	if firstBody.Data.CacheReadRatio != 0.1 || firstBody.Data.CacheCreationRatio != 1.1 ||
		firstBody.Data.CacheCreation5mRatio != 1.25 || firstBody.Data.CacheCreation1hRatio != 2 ||
		firstBody.Data.ImageRatio != 3 || firstBody.Data.AudioRatio != 4 || firstBody.Data.AudioCompletionRatio != 5 {
		t.Fatalf("publication modality ratios were not persisted: %s", first.Body.String())
	}
	if input, output, ok, err := model.GetZTAPIPublishedSalePrice("zt-gpt-5.5"); err != nil || !ok || math.Abs(input-1.6666666667) > 0.00000001 || math.Abs(output-3.3333333333) > 0.00000001 {
		t.Fatalf("published sale price=%v/%v ok=%v, want 1.6666666667/3.3333333333", input, output, ok)
	}
	var before model.ZTAPIModelConfig
	if err := db.First(&before, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	renamed := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(2, "gpt-5.5", "zt-gpt-5.5-unquoted", "openai", 1, 2, 1.6666666667, 3.3333333333, true, false), "")
	if renamed.Code != http.StatusBadRequest || !strings.Contains(renamed.Body.String(), model.ErrZTAPIQuotationIdentityMismatch.Error()) {
		t.Fatalf("unquoted rename was not rejected: status=%d body=%s", renamed.Code, renamed.Body.String())
	}
	var unchanged model.ZTAPIModelConfig
	if err := db.First(&unchanged, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != before.Version || unchanged.PublicationSnapshotID != before.PublicationSnapshotID ||
		unchanged.PublicNameValue() != before.PublicNameValue() || unchanged.InputPricePerMillion != before.InputPricePerMillion ||
		unchanged.OutputPricePerMillion != before.OutputPricePerMillion {
		t.Fatal("rejected rename changed the approved identity or pricing snapshot")
	}
	if input, output, ok, err := model.GetZTAPIPublishedSalePrice("zt-gpt-5.5"); err != nil || !ok || math.Abs(input-1.6666666667) > 0.00000001 || math.Abs(output-3.3333333333) > 0.00000001 {
		t.Fatal("rejected rename changed the authoritative price")
	}
	seedZTAPIPublicationEvidence(t, db, config, channel, "zt-gpt-5.5", model.ZTAPIProviderOpenAI, 2, 4)

	second := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(2, "gpt-5.5", "zt-gpt-5.5", "openai", 2, 4, 3.3333333333, 6.6666666667, true, false), "")
	if second.Code != http.StatusOK {
		t.Fatalf("price update status=%d body=%s", second.Code, second.Body.String())
	}
	if _, _, ok, err := model.GetZTAPIPublishedSalePrice("zt-gpt-5.5-unquoted"); err != nil || ok {
		t.Fatal("unquoted alias gained authoritative pricing")
	}
	if input, output, ok, err := model.GetZTAPIPublishedSalePrice("zt-gpt-5.5"); err != nil || !ok || math.Abs(input-3.3333333333) > 0.00000001 || math.Abs(output-6.6666666667) > 0.00000001 {
		t.Fatalf("updated sale price=%v/%v ok=%v, want 3.3333333333/6.6666666667", input, output, ok)
	}
	if source, ok := model.ResolveZTAPIPublicAlias("zt-gpt-5.5"); !ok || source != "gpt-5.5" {
		t.Fatalf("public alias resolved to %q ok=%v, want gpt-5.5", source, ok)
	}
	mapping := model.MergeZTAPIAliasModelMapping(`{"gpt-5.5":"upstream-gpt"}`, "zt-gpt-5.5")
	if !strings.Contains(mapping, `"zt-gpt-5.5":"gpt-5.5"`) || !strings.Contains(mapping, `"gpt-5.5":"upstream-gpt"`) {
		t.Fatalf("public alias did not merge into the channel mapping chain: %s", mapping)
	}
	publicPricing := model.ApplyZTAPIPublicPricing([]model.Pricing{{ModelName: "gpt-5.5", EnableGroup: []string{"default"}}})
	if len(publicPricing) != 1 || publicPricing[0].ModelName != "zt-gpt-5.5" || math.Abs(publicPricing[0].ModelRatio-1.6666666667) > 0.0000001 || math.Abs(publicPricing[0].CompletionRatio-2) > 0.0000001 {
		t.Fatalf("public pricing projection is not authoritative: %#v", publicPricing)
	}

	legacyOverwrite := performChannelRequest(t, engine, http.MethodPut, "/api/option/", root, token,
		`{"key":"ModelPrice","value":"{\"zt-gpt-5.5\":0.01}"}`, "")
	if legacyOverwrite.Code != http.StatusConflict {
		t.Fatalf("legacy ratio overwrite status=%d body=%s", legacyOverwrite.Code, legacyOverwrite.Body.String())
	}
	if input, output, ok, err := model.GetZTAPIPublishedSalePrice("zt-gpt-5.5"); err != nil || !ok || math.Abs(input-3.3333333333) > 0.00000001 || math.Abs(output-6.6666666667) > 0.00000001 {
		t.Fatalf("legacy overwrite changed authoritative pricing: %v/%v ok=%v", input, output, ok)
	}

	var modelRatioOption model.Option
	if err := db.First(&modelRatioOption, "key = ?", "ModelRatio").Error; err != nil {
		t.Fatalf("load persisted model ratios: %v", err)
	}
	if modelRatioOption.Value != `{}` {
		t.Fatalf("ZTAPI publication rewrote legacy global ratios: %s", modelRatioOption.Value)
	}
}

func TestZTAPIModelPricingRejectsStaleVersionWithoutPartialBillingChange(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "pricing-cas-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "claude-sonnet-5", "default")
	config := seedZTAPIModelConfig(t, db, "claude-sonnet-5")
	seedZTAPIPublicationEvidence(t, db, config, channel, "zt-claude-sonnet-5", model.ZTAPIProviderAnthropic, 2, 4)

	first := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(1, "claude-sonnet-5", "zt-claude-sonnet-5", "claude", 2, 4, 3.3333333333, 6.6666666667, true, false), "")
	if first.Code != http.StatusOK {
		t.Fatalf("initial publish status=%d body=%s", first.Code, first.Body.String())
	}
	stale := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(1, "claude-sonnet-5", "stale-alias", "claude", 2, 4, 20, 40, true, false), "")
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	if !strings.Contains(stale.Body.String(), `"public_name":"zt-claude-sonnet-5"`) || !strings.Contains(stale.Body.String(), `"version":2`) {
		t.Fatalf("conflict response lacks current identity: %s", stale.Body.String())
	}
	if _, _, ok, err := model.GetZTAPIPublishedSalePrice("stale-alias"); err != nil || ok {
		t.Fatal("stale alias changed authoritative pricing")
	}
	if input, output, ok, err := model.GetZTAPIPublishedSalePrice("zt-claude-sonnet-5"); err != nil || !ok || math.Abs(input-3.3333333333) > 0.00000001 || math.Abs(output-6.6666666667) > 0.00000001 {
		t.Fatalf("committed price changed after stale write: %v/%v ok=%v", input, output, ok)
	}
}

func TestZTAPIModelPricingRequiresValidPublicationAndRootBelowCostConfirmation(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	admin, adminToken := createChannelOperator(t, db, "pricing-admin", common.RoleAdminUser)
	root, rootToken := createChannelOperator(t, db, "pricing-override-root", common.RoleRootUser)
	config := seedZTAPIModelConfig(t, db, "gemini-3.5-flash")

	noRoute := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, rootToken,
		ztapiPricingUpdateBody(1, "gemini-3.5-flash", "zt-gemini-3.5-flash", "gemini", 3, 6, 9, 18, true, false), "")
	if noRoute.Code != http.StatusBadRequest {
		t.Fatalf("publish without route status=%d body=%s", noRoute.Code, noRoute.Body.String())
	}
	channel := seedZTAPIRoute(t, db, "gemini-3.5-flash", "default")
	seedZTAPIPublicationEvidence(t, db, config, channel, "zt-gemini-3.5-flash", model.ZTAPIProviderGoogle, 3, 6)

	var incompletePricing map[string]any
	if err := json.Unmarshal([]byte(ztapiPricingUpdateBody(1, "gemini-3.5-flash", "zt-gemini-3.5-flash", "gemini", 3, 6, 9, 18, true, false)), &incompletePricing); err != nil {
		t.Fatal(err)
	}
	incompletePricing["image_ratio"] = 0
	incompleteBody, _ := json.Marshal(incompletePricing)
	incomplete := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, rootToken, string(incompleteBody), "")
	if incomplete.Code != http.StatusBadRequest {
		t.Fatalf("publish with incomplete modality pricing status=%d body=%s", incomplete.Code, incomplete.Body.String())
	}

	belowCostAdmin := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), admin, adminToken,
		ztapiPricingUpdateBody(1, "gemini-3.5-flash", "zt-gemini-3.5-flash", "gemini", 10, 20, 5, 10, true, true), "")
	if belowCostAdmin.Code != http.StatusForbidden {
		t.Fatalf("admin below-cost override status=%d body=%s", belowCostAdmin.Code, belowCostAdmin.Body.String())
	}
	belowCostRootWithoutConfirm := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, rootToken,
		ztapiPricingUpdateBody(1, "gemini-3.5-flash", "zt-gemini-3.5-flash", "gemini", 10, 20, 5, 10, true, false), "")
	if belowCostRootWithoutConfirm.Code != http.StatusBadRequest {
		t.Fatalf("root unconfirmed below-cost status=%d body=%s", belowCostRootWithoutConfirm.Code, belowCostRootWithoutConfirm.Body.String())
	}
	confirmed := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, rootToken,
		ztapiPricingUpdateBody(1, "gemini-3.5-flash", "zt-gemini-3.5-flash", "gemini", 10, 20, 5, 10, true, true), "")
	if confirmed.Code != http.StatusBadRequest || !strings.Contains(confirmed.Body.String(), model.ZTAPIPublicationBlockerPriceIncomplete) {
		t.Fatalf("unverified below-cost price status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
}

func TestZTAPIPricingPreviewContainsOnlyPublishedRouteReadyModels(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	support, token := createChannelOperator(t, db, "pricing-support", common.RoleSupportUser)
	root, rootToken := createChannelOperator(t, db, "pricing-preview-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "gpt-5.5", "default")
	published := seedZTAPIModelConfig(t, db, "gpt-5.5")
	seedZTAPIPublicationEvidence(t, db, published, channel, "zt-gpt-5.5", model.ZTAPIProviderOpenAI, 1, 2)
	seedZTAPIModelConfig(t, db, "draft-source")
	write := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", published.ID), root, rootToken,
		ztapiPricingUpdateBody(1, "gpt-5.5", "zt-gpt-5.5", "openai", 1, 2, 1.6666666667, 3.3333333333, true, false), "")
	if write.Code != http.StatusOK {
		t.Fatalf("publish preview model status=%d body=%s", write.Code, write.Body.String())
	}
	if err := db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", published.ID).Updates(map[string]any{
		"public_name": "tampered-preview-name", "input_price_per_million": 999,
	}).Error; err != nil {
		t.Fatalf("tamper mutable preview row: %v", err)
	}
	model.InvalidateZTAPIAliasCache()

	preview := performChannelRequest(t, engine, http.MethodGet, "/api/models/ztapi/pricing-preview", support, token, "", "")
	if preview.Code != http.StatusOK {
		t.Fatalf("pricing preview status=%d body=%s", preview.Code, preview.Body.String())
	}
	if !strings.Contains(preview.Body.String(), "zt-gpt-5.5") || strings.Contains(preview.Body.String(), "tampered-preview-name") ||
		strings.Contains(preview.Body.String(), `"input_price_per_million":999`) || strings.Contains(preview.Body.String(), "draft-source") ||
		strings.Contains(preview.Body.String(), "secret-not-returned") {
		t.Fatalf("unsafe or incorrect pricing preview: %s", preview.Body.String())
	}
}

func TestZTAPIModelPricingRejectsFamilyThatDoesNotMatchSourceModel(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "family-root", common.RoleRootUser)
	seedZTAPIRoute(t, db, "deepseek-chat", "default")
	config := seedZTAPIModelConfig(t, db, "deepseek-chat")

	response := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(config.Version, "deepseek-chat", "zt-gpt-pro", "openai", 1, 2, 3, 4, true, false), "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for forged family: %s", response.Code, response.Body.String())
	}
}

func TestZTAPIModelPricingRejectsForgedOpenAINameOnUntrustedProtocol(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "family-protocol-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "gpt-private", "default")
	if err := db.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("type", constant.ChannelTypeDeepSeek).Error; err != nil {
		t.Fatal(err)
	}
	config := seedZTAPIModelConfig(t, db, "gpt-private")

	response := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(config.Version, "gpt-private", "zt-gpt-private", "openai", 1, 2, 3, 4, true, false), "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for protocol-forged family: %s", response.Code, response.Body.String())
	}
}

func TestZTAPIModelPricingRejectsPublicAliasThatCollidesWithAnotherSource(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "alias-collision-root", common.RoleRootUser)
	seedZTAPIRoute(t, db, "gpt-source-a", "default")
	config := seedZTAPIModelConfig(t, db, "gpt-source-a")
	seedZTAPIModelConfig(t, db, "gpt-source-b")

	response := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(config.Version, "gpt-source-a", "gpt-source-b", "openai", 1, 2, 3, 4, true, false), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for alias/source collision: %s", response.Code, response.Body.String())
	}
}

func TestZTAPIPublicPricingIsStrictProjectionAndNeverLeaksSources(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "public-catalog-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "gpt-5.5", "default")
	published := seedZTAPIModelConfig(t, db, "gpt-5.5")
	publicName := "zt-gpt-5.5"
	seedZTAPIPublicationEvidence(t, db, published, channel, publicName, model.ZTAPIProviderOpenAI, 1, 2)
	publish := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", published.ID), root, token,
		ztapiPricingUpdateBody(published.Version, published.SourceModel, publicName, "openai", 1, 2, 1.6666666667, 3.3333333333, true, false), "")
	if publish.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publish.Code, publish.Body.String())
	}
	seedZTAPIModelConfig(t, db, "claude-draft-source")

	projection := model.ApplyZTAPIPublicPricing([]model.Pricing{
		{ModelName: "gpt-5.5", EnableGroup: []string{"default"}, VendorID: 99, Description: "OpenRouter upstream", Icon: "openrouter.svg"},
		{ModelName: "claude-draft-source", EnableGroup: []string{"default"}},
		{ModelName: "deepseek-unmanaged", EnableGroup: []string{"default"}},
	})
	if len(projection) != 1 || projection[0].ModelName != publicName {
		t.Fatalf("public pricing leaked draft/unmanaged/source models: %#v", projection)
	}
	if projection[0].CacheRatio == nil || *projection[0].CacheRatio != 0.1 ||
		projection[0].CreateCacheRatio == nil || *projection[0].CreateCacheRatio != 1.1 ||
		projection[0].ImageRatio == nil || *projection[0].ImageRatio != 3 ||
		projection[0].AudioRatio == nil || *projection[0].AudioRatio != 4 ||
		projection[0].AudioCompletionRatio == nil || *projection[0].AudioCompletionRatio != 5 {
		t.Fatalf("public pricing omitted modality ratios: %#v", projection[0])
	}
	for _, item := range projection {
		if strings.Contains(item.ModelName, "source") || item.ModelName == "deepseek-unmanaged" {
			t.Fatalf("public pricing leaked source model %q", item.ModelName)
		}
		if item.VendorID != 0 || item.Description != "" || item.Icon != "" {
			t.Fatalf("public pricing leaked source metadata: %#v", item)
		}
	}
	vendors := model.FilterZTAPIPublicVendors(projection, []model.PricingVendor{{ID: 99, Name: "OpenRouter"}})
	if len(vendors) != 0 {
		t.Fatalf("public pricing leaked vendors: %#v", vendors)
	}
	endpoints := model.ProjectZTAPIPublicSupportedEndpoints(projection, map[string]common.EndpointInfo{
		"gpt-5.5": {Path: "https://upstream.invalid/private", Method: "POST"},
	})
	if len(endpoints) != 1 || endpoints["openai"].Path != "/v1/chat/completions" {
		t.Fatalf("public endpoints are not protocol-only defaults: %#v", endpoints)
	}
}

func TestZTAPIPublicModelResolutionEnforcesGroupsForPublicAndOfficialNames(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "public-resolution-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "claude-sonnet-5", "vip")
	config := seedZTAPIModelConfig(t, db, "claude-sonnet-5")
	publicName := "zt-claude-sonnet-5"
	seedZTAPIPublicationEvidence(t, db, config, channel, publicName, model.ZTAPIProviderAnthropic, 1, 2)
	var body map[string]any
	if err := json.Unmarshal([]byte(ztapiPricingUpdateBody(config.Version, config.SourceModel, publicName, "claude", 1, 2, 1.6666666667, 3.3333333333, true, false)), &body); err != nil {
		t.Fatal(err)
	}
	body["enabled_groups"] = []string{"vip"}
	encoded, _ := json.Marshal(body)
	publish := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token, string(encoded), "")
	if publish.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publish.Code, publish.Body.String())
	}
	model.InvalidateZTAPIAliasCache()

	if _, err := model.ResolveZTAPIRequestModel(publicName, "default"); !errors.Is(err, model.ErrZTAPIModelGroupForbidden) {
		t.Fatalf("default group resolution error = %v, want group forbidden", err)
	}
	resolved, err := model.ResolveZTAPIRequestModel(publicName, "vip")
	if err != nil || resolved != "claude-sonnet-5" {
		t.Fatalf("vip resolution = %q, %v", resolved, err)
	}
	if _, err := model.ResolveZTAPIRequestModel("claude-sonnet-5", "default"); !errors.Is(err, model.ErrZTAPIModelGroupForbidden) {
		t.Fatalf("official source default-group error = %v, want group forbidden", err)
	}
	identity, err := model.ResolveZTAPIRequestIdentity("claude-sonnet-5", "vip")
	if err != nil || identity.PublicName != publicName || identity.SourceModel != "claude-sonnet-5" {
		t.Fatalf("official source identity = %#v, %v", identity, err)
	}
}

func TestZTAPIBelowCostOverrideCannotBypassVerifiedPriceEvidence(t *testing.T) {
	db, engine := setupZTAPIPricingController(t)
	root, token := createChannelOperator(t, db, "audit-root", common.RoleRootUser)
	channel := seedZTAPIRoute(t, db, "gpt-5.5", "default")
	config := seedZTAPIModelConfig(t, db, "gpt-5.5")
	seedZTAPIPublicationEvidence(t, db, config, channel, "zt-gpt-5.5", model.ZTAPIProviderOpenAI, 5, 8)

	response := performChannelRequest(t, engine, http.MethodPut, fmt.Sprintf("/api/models/ztapi/%d", config.ID), root, token,
		ztapiPricingUpdateBody(config.Version, "gpt-5.5", "zt-gpt-5.5", "openai", 5, 8, 4, 7, true, true), "")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), model.ZTAPIPublicationBlockerPriceIncomplete) {
		t.Fatalf("status = %d, want evidence-backed rejection: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := db.Table("ztapi_audit_events").Where("action = ? AND model_config_id = ?", "ztapi.model_below_cost_override", config.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected below-cost publication wrote %d durable audit events", count)
	}
}
