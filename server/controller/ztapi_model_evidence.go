package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var ztapiModelVerifier = service.VerifyZTAPIModel

type ztapiModelIdentityRequest struct {
	Version         uint64 `json:"version"`
	SourceModel     string `json:"source_model"`
	PublicName      string `json:"public_name"`
	Protocol        string `json:"protocol"`
	ProviderFamily  string `json:"provider_family"`
	SourceReference string `json:"source_reference"`
	Reason          string `json:"reason"`
	Confirm         bool   `json:"confirm"`
}

type ztapiModelPriceSourceRequest struct {
	Version                uint64   `json:"version"`
	SourceModel            string   `json:"source_model"`
	ResourceType           string   `json:"resource_type"`
	PricePolicy            string   `json:"price_policy"`
	SpendTier              string   `json:"spend_tier"`
	BillingDimensions      []string `json:"billing_dimensions"`
	Currency               string   `json:"currency"`
	InputPerMillion        string   `json:"input_per_million"`
	OutputPerMillion       string   `json:"output_per_million"`
	CacheReadPerMillion    string   `json:"cache_read_per_million"`
	CacheWritePerMillion   string   `json:"cache_write_per_million"`
	CacheWrite5mPerMillion string   `json:"cache_write_5m_per_million"`
	CacheWrite1hPerMillion string   `json:"cache_write_1h_per_million"`
	ImageUnitCost          string   `json:"image_unit_cost"`
	AudioUnitCost          string   `json:"audio_unit_cost"`
	RequestUnitCost        string   `json:"request_unit_cost"`
	CNYPerUSD              string   `json:"cny_per_usd"`
	QuotationEffectiveAt   int64    `json:"quotation_effective_at"`
	SourceDocumentChecksum string   `json:"source_document_checksum"`
	Reason                 string   `json:"reason"`
	Confirm                bool     `json:"confirm"`
}

type ztapiModelVerificationRequest struct {
	ChannelID int `json:"channel_id"`
}

func parseZTAPIModelEvidenceID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的模型配置编号。"})
		return 0, false
	}
	return id, true
}

func respondZTAPIModelEvidenceWriteError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrZTAPIModelVersionConflict):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "模型目录已被其他管理员更新，请刷新后重试。"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型配置不存在。"})
	case errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique"):
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "公开模型名或证据版本发生冲突。"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "模型证据不符合写入要求。"})
	}
}

func UpdateZTAPIModelIdentity(c *gin.Context) {
	id, ok := parseZTAPIModelEvidenceID(c)
	if !ok {
		return
	}
	var request ztapiModelIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "模型身份参数无效。"})
		return
	}
	if !request.Confirm {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "必须确认模型身份映射。"})
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(request.Protocol))
	provider := strings.ToLower(strings.TrimSpace(request.ProviderFamily))
	if !model.IsSupportedZTAPIProtocol(protocol) || !model.IsSupportedZTAPIProviderFamily(provider) ||
		strings.TrimSpace(request.SourceModel) == "" || strings.TrimSpace(request.PublicName) == "" ||
		strings.TrimSpace(request.SourceReference) == "" || strings.TrimSpace(request.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "模型身份映射缺少必填证据。"})
		return
	}
	committed, identity, err := model.UpdateZTAPIModelIdentity(model.ZTAPIModelIdentityUpdate{
		ModelConfigID: id, SourceModel: request.SourceModel, PublicName: request.PublicName,
		Protocol: protocol, ProviderFamily: provider,
		SourceReference: request.SourceReference, Reason: request.Reason,
		ExpectedVersion: request.Version, OperatorID: c.GetInt("id"),
	})
	if err != nil {
		respondZTAPIModelEvidenceWriteError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"model": gin.H{
			"id": committed.ID, "source_model": committed.SourceModel,
			"public_name": committed.PublicNameValue(), "protocol": committed.Protocol,
			"provider_family": committed.ProviderFamily, "version": committed.Version,
		},
		"identity": identity,
	})
}

func buildZTAPIModelPriceSource(id int, operatorID int, request ztapiModelPriceSourceRequest) (model.ZTAPIModelPriceSource, error) {
	dimensions, err := json.Marshal(request.BillingDimensions)
	if err != nil {
		return model.ZTAPIModelPriceSource{}, err
	}
	return model.ZTAPIModelPriceSource{
		ModelConfigID: id, SourceModel: request.SourceModel,
		ResourceType: request.ResourceType, PricePolicy: request.PricePolicy, SpendTier: request.SpendTier,
		BillingDimensions: string(dimensions), Currency: request.Currency,
		InputPerMillion: request.InputPerMillion, OutputPerMillion: request.OutputPerMillion,
		CacheReadPerMillion:    request.CacheReadPerMillion,
		CacheWritePerMillion:   request.CacheWritePerMillion,
		CacheWrite5mPerMillion: request.CacheWrite5mPerMillion,
		CacheWrite1hPerMillion: request.CacheWrite1hPerMillion,
		ImageUnitCost:          request.ImageUnitCost, AudioUnitCost: request.AudioUnitCost,
		RequestUnitCost: request.RequestUnitCost, CNYPerUSD: request.CNYPerUSD,
		QuotationEffectiveAt:   request.QuotationEffectiveAt,
		SourceDocumentChecksum: request.SourceDocumentChecksum, OperatorID: operatorID,
	}, nil
}

func bindZTAPIModelPriceSourceRequest(c *gin.Context, id int) (ztapiModelPriceSourceRequest, model.ZTAPIModelPriceSource, bool) {
	var request ztapiModelPriceSourceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "报价证据参数无效。"})
		return request, model.ZTAPIModelPriceSource{}, false
	}
	source, err := buildZTAPIModelPriceSource(id, c.GetInt("id"), request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "报价计费维度无效。"})
		return request, model.ZTAPIModelPriceSource{}, false
	}
	if err := model.ValidateZTAPIModelPriceSource(&source); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "报价证据不完整或格式错误。"})
		return request, model.ZTAPIModelPriceSource{}, false
	}
	return request, source, true
}

func PreviewZTAPIModelPriceSource(c *gin.Context) {
	id, ok := parseZTAPIModelEvidenceID(c)
	if !ok {
		return
	}
	_, source, ok := bindZTAPIModelPriceSourceRequest(c, id)
	if !ok {
		return
	}
	preview, err := model.BuildZTAPIModelPricePreview(&source)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无法计算报价预览。"})
		return
	}
	common.ApiSuccess(c, preview)
}

func ImportZTAPIModelPriceSource(c *gin.Context) {
	id, ok := parseZTAPIModelEvidenceID(c)
	if !ok {
		return
	}
	request, source, ok := bindZTAPIModelPriceSourceRequest(c, id)
	if !ok {
		return
	}
	if !request.Confirm || strings.TrimSpace(request.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "必须确认报价来源并填写变更原因。"})
		return
	}
	committed, persisted, preview, err := model.ImportZTAPIModelPriceSource(model.ZTAPIModelPriceSourceImport{
		Source: source, ExpectedVersion: request.Version,
		OperatorID: c.GetInt("id"), Reason: request.Reason,
	})
	if err != nil {
		respondZTAPIModelEvidenceWriteError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"model": gin.H{
			"id": committed.ID, "source_model": committed.SourceModel,
			"public_name": committed.PublicNameValue(), "version": committed.Version,
		},
		"price_source": gin.H{
			"id": persisted.ID, "price_source_version": persisted.Version,
			"resource_type": persisted.ResourceType, "spend_tier": persisted.SpendTier,
			"currency": persisted.Currency, "quotation_effective_at": persisted.QuotationEffectiveAt,
			"source_document_checksum": persisted.SourceDocumentChecksum,
		},
		"preview": preview,
	})
}

func VerifyZTAPIModel(c *gin.Context) {
	id, ok := parseZTAPIModelEvidenceID(c)
	if !ok {
		return
	}
	var request ztapiModelVerificationRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ChannelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "请选择需要验证的受管上游通道。"})
		return
	}
	config, err := model.GetZTAPIModelConfig(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "模型配置不存在。"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "无法读取模型配置。"})
		return
	}
	verification, verifyErr := ztapiModelVerifier(
		c.Request.Context(), request.ChannelID, config.SourceModel, c.GetInt("id"),
	)
	if verifyErr != nil {
		response := gin.H{"success": false, "message": "上游模型验证未通过。"}
		if verification != nil {
			response["verification"] = verification
		}
		c.JSON(http.StatusUnprocessableEntity, response)
		return
	}
	common.ApiSuccess(c, gin.H{"verification": verification})
}
