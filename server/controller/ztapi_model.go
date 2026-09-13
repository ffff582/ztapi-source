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

package controller

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiModelUpdateRequest struct {
	Version               uint64   `json:"version"`
	SourceModel           string   `json:"source_model"`
	PublicName            string   `json:"public_name"`
	Family                string   `json:"family"`
	InputCostPerMillion   float64  `json:"input_cost_per_million"`
	OutputCostPerMillion  float64  `json:"output_cost_per_million"`
	InputPricePerMillion  float64  `json:"input_price_per_million"`
	OutputPricePerMillion float64  `json:"output_price_per_million"`
	CacheReadRatio        float64  `json:"cache_read_ratio"`
	CacheCreationRatio    float64  `json:"cache_creation_ratio"`
	CacheCreation5mRatio  float64  `json:"cache_creation_5m_ratio"`
	CacheCreation1hRatio  float64  `json:"cache_creation_1h_ratio"`
	ImageRatio            float64  `json:"image_ratio"`
	AudioRatio            float64  `json:"audio_ratio"`
	AudioCompletionRatio  float64  `json:"audio_completion_ratio"`
	EnabledGroups         []string `json:"enabled_groups"`
	Published             bool     `json:"published"`
	ConfirmBelowCost      bool     `json:"confirm_below_cost"`
}

var ztapiCommercialRepricer = model.ApplyZTAPICommercialPricingV2

func RepriceZTAPICommercialCatalog(c *gin.Context) {
	var request struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请确认执行商业价格迁移。"})
		return
	}
	result, err := ztapiCommercialRepricer(c.GetInt("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "商业价格迁移未完成。", "detail": err.Error()})
		return
	}
	common.ApiSuccess(c, result)
}

type ztapiModelProjection struct {
	ID                                        int      `json:"id"`
	SourceModel                               string   `json:"source_model"`
	PublicName                                string   `json:"public_name"`
	Modality                                  string   `json:"modality"`
	Family                                    string   `json:"family"`
	Protocol                                  string   `json:"protocol"`
	ProviderFamily                            string   `json:"provider_family"`
	InputCostPerMillion                       float64  `json:"input_cost_per_million"`
	OutputCostPerMillion                      float64  `json:"output_cost_per_million"`
	InputPricePerMillion                      float64  `json:"input_price_per_million"`
	OutputPricePerMillion                     float64  `json:"output_price_per_million"`
	CacheReadRatio                            float64  `json:"cache_read_ratio"`
	CacheCreationRatio                        float64  `json:"cache_creation_ratio"`
	CacheCreation5mRatio                      float64  `json:"cache_creation_5m_ratio"`
	CacheCreation1hRatio                      float64  `json:"cache_creation_1h_ratio"`
	ImageRatio                                float64  `json:"image_ratio"`
	AudioRatio                                float64  `json:"audio_ratio"`
	AudioCompletionRatio                      float64  `json:"audio_completion_ratio"`
	EnabledGroups                             []string `json:"enabled_groups"`
	Published                                 bool     `json:"published"`
	Version                                   uint64   `json:"version"`
	RouteReady                                bool     `json:"route_ready"`
	EnabledRouteCount                         int64    `json:"enabled_route_count"`
	PublicationBlockers                       []string `json:"publication_blockers"`
	ReasoningEfforts                          []string `json:"reasoning_efforts"`
	ReasoningCapabilityEvidence               string   `json:"reasoning_capability_evidence"`
	ReasoningCapabilityLiveValidationRequired bool     `json:"reasoning_capability_live_validation_required"`
}

type ztapiReasoningProjection struct {
	ReasoningEfforts                          []string
	ReasoningCapabilityEvidence               string
	ReasoningCapabilityLiveValidationRequired bool
}

func ztapiReasoningProjectionFor(sourceModel string) ztapiReasoningProjection {
	capability, ok := model.ZTAPIReasoningCapabilityFor(sourceModel)
	if !ok {
		return ztapiReasoningProjection{ReasoningEfforts: []string{}}
	}
	return ztapiReasoningProjection{
		ReasoningEfforts:                          capability.ReasoningEfforts,
		ReasoningCapabilityEvidence:               capability.EvidenceQuote,
		ReasoningCapabilityLiveValidationRequired: capability.LiveValidationRequired,
	}
}

type ztapiPublicPricingProjection struct {
	PublicName            string   `json:"public_name"`
	Family                string   `json:"family"`
	InputPricePerMillion  float64  `json:"input_price_per_million"`
	OutputPricePerMillion float64  `json:"output_price_per_million"`
	CacheReadRatio        float64  `json:"cache_read_ratio"`
	CacheCreationRatio    float64  `json:"cache_creation_ratio"`
	CacheCreation5mRatio  float64  `json:"cache_creation_5m_ratio"`
	CacheCreation1hRatio  float64  `json:"cache_creation_1h_ratio"`
	ImageRatio            float64  `json:"image_ratio"`
	AudioRatio            float64  `json:"audio_ratio"`
	AudioCompletionRatio  float64  `json:"audio_completion_ratio"`
	EnabledGroups         []string `json:"enabled_groups"`
	Version               uint64   `json:"version"`
}

func buildZTAPIModelProjection(config *model.ZTAPIModelConfig) (ztapiModelProjection, error) {
	groups := config.Groups()
	modality := model.ZTAPIModelModality(config.SourceModel)
	if quotation, found := model.GetZTAPIMediaQuotationReference(config.SourceModel); found {
		modality = quotation.Modality
	}
	trustedIDs, routeErr := model.GetZTAPITrustedRouteChannelIDs(config.SourceModel, config.Family, groups)
	count := int64(len(trustedIDs))
	blockers, blockersErr := model.ZTAPIPublicationBlockers(config.ID)
	if blockersErr != nil {
		blockers = []string{"publication_evidence_unavailable"}
	}
	reasoning := ztapiReasoningProjectionFor(config.SourceModel)
	return ztapiModelProjection{
		ID:                          config.ID,
		SourceModel:                 config.SourceModel,
		PublicName:                  config.PublicNameValue(),
		Modality:                    modality,
		Family:                      config.Family,
		Protocol:                    config.Protocol,
		ProviderFamily:              config.ProviderFamily,
		InputCostPerMillion:         config.InputCostPerMillion,
		OutputCostPerMillion:        config.OutputCostPerMillion,
		InputPricePerMillion:        config.InputPricePerMillion,
		OutputPricePerMillion:       config.OutputPricePerMillion,
		CacheReadRatio:              config.CacheReadRatio,
		CacheCreationRatio:          config.CacheCreationRatio,
		CacheCreation5mRatio:        config.CacheCreation5mRatio,
		CacheCreation1hRatio:        config.CacheCreation1hRatio,
		ImageRatio:                  config.ImageRatio,
		AudioRatio:                  config.AudioRatio,
		AudioCompletionRatio:        config.AudioCompletionRatio,
		EnabledGroups:               groups,
		Published:                   config.Published,
		Version:                     config.Version,
		RouteReady:                  routeErr == nil && count > 0,
		EnabledRouteCount:           count,
		PublicationBlockers:         blockers,
		ReasoningEfforts:            reasoning.ReasoningEfforts,
		ReasoningCapabilityEvidence: reasoning.ReasoningCapabilityEvidence,
		ReasoningCapabilityLiveValidationRequired: reasoning.ReasoningCapabilityLiveValidationRequired,
	}, nil
}

func GetZTAPIModels(c *gin.Context) {
	if err := model.EnsureZTAPIModelConfigsForEnabledAbilities(); err != nil {
		common.ApiError(c, err)
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", c.DefaultQuery("p", "1")))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	configs, total, err := model.SearchZTAPIModelConfigs(c.Query("keyword"), (page-1)*pageSize, pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]ztapiModelProjection, 0, len(configs))
	for i := range configs {
		item, err := buildZTAPIModelProjection(&configs[i])
		if err != nil {
			common.ApiError(c, err)
			return
		}
		items = append(items, item)
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

func GetZTAPIModel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的模型配置编号。"})
		return
	}
	config, err := model.GetZTAPIModelConfig(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型配置不存在。"})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	projection, err := buildZTAPIModelProjection(config)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, projection)
}

func finitePositive(values ...float64) bool {
	for _, value := range values {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func validateZTAPIModelUpdate(c *gin.Context, id int, request *ztapiModelUpdateRequest) (*model.ZTAPIModelConfig, bool, int, string) {
	request.SourceModel = strings.TrimSpace(request.SourceModel)
	request.PublicName = strings.TrimSpace(request.PublicName)
	request.Family = strings.ToLower(strings.TrimSpace(request.Family))
	if request.SourceModel == "" {
		return nil, false, http.StatusBadRequest, "上游模型名不能为空。"
	}
	groupsJSON, err := model.EncodeZTAPIGroups(request.EnabledGroups)
	if err != nil {
		return nil, false, http.StatusBadRequest, "用户组配置无效。"
	}
	groups := []string{}
	_ = common.Unmarshal([]byte(groupsJSON), &groups)
	belowCost := request.Published && (request.InputPricePerMillion < request.InputCostPerMillion || request.OutputPricePerMillion < request.OutputCostPerMillion)
	if request.Published {
		if request.PublicName == "" {
			return nil, belowCost, http.StatusBadRequest, "公开模型名不能为空。"
		}
		conflicts, err := model.ZTAPIPublicNameConflictsWithSource(id, request.SourceModel, request.PublicName)
		if err != nil {
			return nil, belowCost, http.StatusInternalServerError, "无法校验公开模型名。"
		}
		if conflicts {
			return nil, belowCost, http.StatusConflict, "公开模型名不能与其他上游模型名冲突。"
		}
		if len(groups) == 0 {
			return nil, belowCost, http.StatusBadRequest, "至少选择一个可用用户组。"
		}
	}
	if belowCost {
		if c.GetInt("role") != common.RoleRootUser {
			return nil, true, http.StatusForbidden, "仅超级管理员可确认低于成本的售价。"
		}
		if !request.ConfirmBelowCost {
			return nil, true, http.StatusBadRequest, "请明确确认低于成本发布。"
		}
	}
	publicName := request.PublicName
	var publicNamePointer *string
	if publicName != "" {
		publicNamePointer = &publicName
	}
	return &model.ZTAPIModelConfig{
		ID:                    id,
		SourceModel:           request.SourceModel,
		PublicName:            publicNamePointer,
		Family:                request.Family,
		InputCostPerMillion:   request.InputCostPerMillion,
		OutputCostPerMillion:  request.OutputCostPerMillion,
		InputPricePerMillion:  request.InputPricePerMillion,
		OutputPricePerMillion: request.OutputPricePerMillion,
		CacheReadRatio:        request.CacheReadRatio,
		CacheCreationRatio:    request.CacheCreationRatio,
		CacheCreation5mRatio:  request.CacheCreation5mRatio,
		CacheCreation1hRatio:  request.CacheCreation1hRatio,
		ImageRatio:            request.ImageRatio,
		AudioRatio:            request.AudioRatio,
		AudioCompletionRatio:  request.AudioCompletionRatio,
		EnabledGroups:         groupsJSON,
		Published:             request.Published,
	}, belowCost, 0, ""
}

func UpdateZTAPIModel(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的模型配置编号。"})
		return
	}
	var request ztapiModelUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "模型配置参数无效。"})
		return
	}
	next, belowCost, status, message := validateZTAPIModelUpdate(c, id, &request)
	if status != 0 {
		c.JSON(status, gin.H{"success": false, "message": message})
		return
	}
	var auditEvent *model.ZTAPIAuditEvent
	if belowCost {
		auditEvent = &model.ZTAPIAuditEvent{
			Action:     "ztapi.model_below_cost_override",
			OperatorID: c.GetInt("id"),
			Payload: common.MapToJsonStr(map[string]interface{}{
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
			}),
		}
	}
	committed, err := model.UpdateZTAPIModelConfigAndBilling(next, request.Version, auditEvent)
	if errors.Is(err, model.ErrZTAPIHealthIdentityLocked) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "该模型已有健康监控请求记录，不能替换模型身份；请新建模型条目。价格和分组仍可修改。"})
		return
	}
	if errors.Is(err, model.ErrZTAPIModelVersionConflict) {
		projection, projectionErr := buildZTAPIModelProjection(committed)
		if projectionErr != nil {
			common.ApiError(c, projectionErr)
			return
		}
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "模型配置已被其他管理员更新。", "data": projection})
		return
	}
	var blocked *model.ZTAPIPublicationBlockedError
	if errors.As(err, &blocked) {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false, "message": "模型尚未满足发布条件。", "data": gin.H{"blockers": blocked.Blockers},
		})
		return
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")) {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "上游模型名或公开模型名已存在。"})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if belowCost {
		recordManageAudit(c, "ztapi.model_below_cost_override", map[string]interface{}{
			"id":          committed.ID,
			"public_name": committed.PublicNameValue(),
			"version":     committed.Version,
		})
	}
	projection, err := buildZTAPIModelProjection(committed)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, projection)
}

func GetZTAPIPricingPreview(c *gin.Context) {
	publications, err := model.ListZTAPIActiveRuntimePublications()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	items := make([]ztapiPublicPricingProjection, 0, len(publications))
	for i := range publications {
		family := model.ZTAPIModelFamilyOpenAI
		switch publications[i].Protocol {
		case model.ZTAPIProtocolAnthropic:
			family = model.ZTAPIModelFamilyClaude
		case model.ZTAPIProtocolGemini:
			family = model.ZTAPIModelFamilyGemini
		}
		items = append(items, ztapiPublicPricingProjection{
			PublicName:            publications[i].PublicName,
			Family:                family,
			InputPricePerMillion:  publications[i].InputPricePerMillion,
			OutputPricePerMillion: publications[i].OutputPricePerMillion,
			CacheReadRatio:        publications[i].CacheReadRatio,
			CacheCreationRatio:    publications[i].CacheCreationRatio,
			CacheCreation5mRatio:  publications[i].CacheCreation5mRatio,
			CacheCreation1hRatio:  publications[i].CacheCreation1hRatio,
			ImageRatio:            publications[i].ImageRatio,
			AudioRatio:            publications[i].AudioRatio,
			AudioCompletionRatio:  publications[i].AudioCompletionRatio,
			EnabledGroups:         publications[i].Groups,
			Version:               publications[i].Version,
		})
	}
	common.ApiSuccess(c, items)
}

func GetZTAPIAuditEvents(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 50
	}
	events, total, err := model.ListZTAPIAuditEvents((page-1)*pageSize, pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": events, "total": total, "page": page, "page_size": pageSize})
}
