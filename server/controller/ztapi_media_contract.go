package controller

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ztapiMediaContractProjection struct {
	ModelID                    int    `json:"model_id"`
	PublicName                 string `json:"public_name"`
	Modality                   string `json:"modality"`
	Published                  bool   `json:"published"`
	QuotationSheet             string `json:"quotation_sheet,omitempty"`
	QuotationCell              string `json:"quotation_cell,omitempty"`
	QuotationLabel             string `json:"quotation_label,omitempty"`
	QuotationResource          string `json:"quotation_resource,omitempty"`
	QuotationDiscountPercent   string `json:"quotation_discount_percent,omitempty"`
	QuotationCurrency          string `json:"quotation_currency,omitempty"`
	QuotationEffectiveAt       int64  `json:"quotation_effective_at,omitempty"`
	SourceDocumentChecksum     string `json:"source_document_checksum,omitempty"`
	PricePolicy                string `json:"price_policy,omitempty"`
	PriceSourceVersion         uint64 `json:"price_source_version,omitempty"`
	FrozenPricingVersion       string `json:"frozen_pricing_version,omitempty"`
	ProtocolEvidenceSHA256     string `json:"protocol_evidence_sha256,omitempty"`
	PendingReconciliationCount int64  `json:"pending_reconciliation_count"`
}

func ztapiProtocolEvidenceDigest(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
}

func GetZTAPIMediaContract(c *gin.Context) {
	id, ok := parseZTAPIModelEvidenceID(c)
	if !ok {
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
	quotation, hasQuotation := model.GetZTAPIMediaQuotationReference(config.SourceModel)
	modality := model.ZTAPIModelModality(config.SourceModel)
	// Candidate media rows remain blocked by the strict publication gate until
	// their quotation mapping is confirmed, but admins still need their evidence.
	if modality != model.ZTAPIModalityImage && modality != model.ZTAPIModalityVideo && hasQuotation {
		modality = quotation.Modality
	}
	if modality != model.ZTAPIModalityImage && modality != model.ZTAPIModalityVideo {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "该模型不是媒体产品。"})
		return
	}

	projection := ztapiMediaContractProjection{
		ModelID: id, PublicName: config.PublicNameValue(), Modality: modality, Published: config.Published,
	}
	if hasQuotation {
		projection.QuotationSheet = quotation.Sheet
		projection.QuotationCell = quotation.Cell
		projection.QuotationLabel = quotation.Label
		projection.QuotationResource = quotation.Resource
		projection.QuotationDiscountPercent = quotation.DiscountPercent
		projection.QuotationCurrency = quotation.Currency
	}

	var source model.ZTAPIModelPriceSource
	sourceQuery := model.DB.WithContext(c.Request.Context()).Where("model_config_id = ? AND source_model = ?", id, config.SourceModel)
	if sourceErr := sourceQuery.Order("version DESC, id DESC").First(&source).Error; sourceErr != nil && !errors.Is(sourceErr, gorm.ErrRecordNotFound) {
		common.ApiError(c, sourceErr)
		return
	} else if sourceErr == nil {
		projection.PricePolicy = source.PricePolicy
		projection.PriceSourceVersion = source.Version
		projection.QuotationEffectiveAt = source.QuotationEffectiveAt
		projection.SourceDocumentChecksum = source.SourceDocumentChecksum
	}

	var protocolEvidence string
	if config.PublicationSnapshotID > 0 {
		var snapshot model.ZTAPIModelPublicationSnapshot
		if snapshotErr := model.DB.WithContext(c.Request.Context()).First(&snapshot, config.PublicationSnapshotID).Error; snapshotErr != nil {
			common.ApiError(c, snapshotErr)
			return
		}
		projection.FrozenPricingVersion = fmt.Sprintf("ztapi-snapshot-%d", snapshot.ID)
		projection.PricePolicy = snapshot.PricePolicy
		protocolEvidence = snapshot.ImageProtocolContractJSON
		if modality == model.ZTAPIModalityVideo {
			protocolEvidence = snapshot.VideoProtocolContractJSON
		}
	} else {
		var verification model.ZTAPIModelVerification
		verificationErr := model.DB.WithContext(c.Request.Context()).Where("model_config_id = ? AND modality = ?", id, modality).Order("verified_at DESC, id DESC").First(&verification).Error
		if verificationErr != nil && !errors.Is(verificationErr, gorm.ErrRecordNotFound) {
			common.ApiError(c, verificationErr)
			return
		}
		if verificationErr == nil {
			protocolEvidence = verification.ImageProtocolContractJSON
			if modality == model.ZTAPIModalityVideo {
				protocolEvidence = verification.VideoProtocolContractJSON
			}
		}
	}
	projection.ProtocolEvidenceSHA256 = ztapiProtocolEvidenceDigest(protocolEvidence)
	if projection.PublicName != "" {
		if countErr := model.DB.WithContext(c.Request.Context()).Model(&model.ZTAPIRequestSettlement{}).
			Where("public_model = ? AND status = ?", projection.PublicName, model.ZTAPISettlementPending).
			Count(&projection.PendingReconciliationCount).Error; countErr != nil {
			common.ApiError(c, countErr)
			return
		}
	}
	common.ApiSuccess(c, projection)
}
