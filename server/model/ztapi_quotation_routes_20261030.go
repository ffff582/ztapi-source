package model

import "slices"

// Channel IDs below belong to this approved quotation portfolio, not to every
// legacy/imported procurement contract that happens to share a model name.
func ztapiQuotationRouteRestrictionApplies20261030(source *ZTAPIModelPriceSource) bool {
	if source == nil || ztapiQuotationPortfolioChannels20261030(source.SourceModel) == nil {
		return false
	}
	if isZTAPICashMarginPolicy20261030(source.PricePolicy) ||
		source.PricePolicy == string(ZTAPIPricePolicyXcloudGLM53) ||
		source.SourceDocumentChecksum == ZTAPISupplierWorkbook20261019SHA256 {
		return true
	}
	if quote, err := ZTAPIQuotationABEntries(); err == nil && source.SourceDocumentChecksum == quote.WorkbookSHA256 {
		return true
	}
	return source.SourceDocumentChecksum == ZTAPIQuotationSHA256 &&
		((source.SourceModel == "gpt-image-2" && source.PricePolicy == string(ZTAPIPricePolicyPoolOfficial80)) ||
			(source.SourceModel == "qwen3.8-max" && source.ResourceType == "official" && source.PricePolicy == string(ZTAPIPricePolicyEnterprise20Margin)))
}

func ztapiQuotationSourceChannelPermitted20261030(source *ZTAPIModelPriceSource, channel int) bool {
	if !ztapiQuotationRouteRestrictionApplies20261030(source) {
		return true // The ordinary publication gate still validates this contract.
	}
	if source.PricePolicy == string(ZTAPIPricePolicyXcloudGLM53) {
		return channel == 8
	}
	if isZTAPICashMarginPolicy20261030(source.PricePolicy) {
		return ztapiQuotationChannelPermitted20261030(source.SourceModel, channel)
	}
	if quote, err := ZTAPIQuotationABEntries(); err == nil && source.SourceDocumentChecksum == quote.WorkbookSHA256 {
		for _, row := range quote.Entries {
			if row.QuotationCell != source.QuotationCell || row.Grade != source.QuotationGrade ||
				row.ModelCode != source.QuotationModelCode || !row.Active || !row.PricingBasis {
				continue
			}
			if row.Grade == "A" && source.ResourceType == "enterprise" {
				return channel == 1
			}
			if row.QuotationCell == "D52" && row.Grade == "A" && row.ResourceType == "\u539f\u5382\u8f6c\u53d1" && source.ResourceType == "original_resource" {
				return channel == 1
			}
			if row.Grade == "B" && source.ResourceType == "pool" {
				return channel == 2
			}
		}
		return false
	}
	if source.SourceDocumentChecksum == ZTAPISupplierWorkbook20261019SHA256 {
		if source.ResourceType == "enterprise" || source.ResourceType == "official" || source.ResourceType == "original_resource" {
			return channel == 1
		}
		if source.ResourceType == "pool" {
			return channel == 2 || (channel == 4 && (source.SourceModel == "claude-fable-5" || source.SourceModel == "claude-sonnet-5"))
		}
		return false
	}
	return channel == 1 // The two explicitly bound legacy enterprise corrections.
}

// Successful discovery is not procurement authorization. These quotations have
// no approved pool/Xcloud basis; new snapshots must retain only enterprise 1.
func ztapiQuotationPortfolioChannels20261030(source string) []int {
	switch source {
	case "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gpt-image-2", "qwen3.8-max",
		"gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o-mini", "gpt-5-nano",
		"text-embedding-3-small", "text-embedding-ada-002", "gemini-3.1-flash-lite-preview", "gemini-3.5-flash", "doubao-seedance-2-5":
		return []int{1}
	case "claude-fable-5", "claude-sonnet-5":
		return []int{2, 4}
	case "claude-opus-5", "deepseek-v4-flash", "deepseek-v4-pro", "glm-4.7", "glm-5", "glm-5.1",
		"gpt-5.6-luna", "gpt-6.1-sol", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code":
		return []int{2}
	case "glm-5.3":
		return []int{8}
	default:
		return nil
	}
}

func ztapiQuotationChannelPermitted20261030(source string, channel int) bool {
	channels := ztapiQuotationPortfolioChannels20261030(source)
	return channels == nil || slices.Contains(channels, channel)
}
