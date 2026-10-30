package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuotationRoutes20261030EnterpriseOnlyDoesNotFollowPoolDiscovery(t *testing.T) {
	for _, source := range []string{"gemini-3.1-pro-preview", "gpt-image-2", "qwen3.8-max", "gpt-4.1-mini", "gpt-4.1-nano", "gpt-4o-mini", "gpt-5-nano", "text-embedding-3-small", "text-embedding-ada-002", "gemini-3.1-flash-lite-preview", "gemini-3.5-flash", "doubao-seedance-2-5", "gemini-3-flash-preview"} {
		t.Run(source, func(t *testing.T) {
			require.True(t, ztapiQuotationChannelPermitted20261030(source, 1))
			for _, channel := range []int{2, 3, 4, 8} {
				require.False(t, ztapiQuotationChannelPermitted20261030(source, channel))
			}
		})
	}
	for _, source := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "claude-fable-5"} {
		require.True(t, ztapiQuotationChannelPermitted20261030(source, 2))
		require.False(t, ztapiQuotationChannelPermitted20261030(source, 1))
	}
	require.True(t, ztapiQuotationChannelPermitted20261030("claude-fable-5", 4))
	require.True(t, ztapiQuotationChannelPermitted20261030("glm-5.3", 8))
	require.False(t, ztapiQuotationChannelPermitted20261030("glm-5.3", 2))
}

func TestQuotationRoutes20261030GeminiPreservesLegacyReaderNotLegacyAdmission(t *testing.T) {
	source := &ZTAPIModelPriceSource{SourceModel: "gemini-3.1-pro-preview", PricePolicy: string(ZTAPIPricePolicyCashPool20261030)}
	for _, channels := range [][]int{{1}, {2}} {
		require.NoError(t, validateZTAPICashMarginRoute20261030(source, "zt-gemini-3.1-pro-preview", channels))
	}
	for _, channels := range [][]int{{1, 2}, {4}, {3}} {
		require.Error(t, validateZTAPICashMarginRoute20261030(source, "zt-gemini-3.1-pro-preview", channels))
	}
	require.False(t, ztapiQuotationChannelPermitted20261030(source.SourceModel, 2))
}

func TestQuotationRoutes20261030BindsRestrictionsToApprovedQuotation(t *testing.T) {
	legacy := &ZTAPIModelPriceSource{SourceModel: "deepseek-v4-flash", SourceDocumentChecksum: ZTAPIQuotationSHA256,
		ResourceType: "enterprise", PricePolicy: string(ZTAPIPricePolicyEnterprise40Margin)}
	require.False(t, ztapiQuotationRouteRestrictionApplies20261030(legacy), "unrelated legacy quotations must retain their own route authority")
	require.False(t, ztapiQuotationRouteRestrictionApplies20261030(nil))
	for _, source := range []*ZTAPIModelPriceSource{
		{SourceModel: "deepseek-v4-flash", SourceDocumentChecksum: ZTAPISupplierWorkbook20261019SHA256},
		{SourceModel: "gpt-image-2", SourceDocumentChecksum: ZTAPIQuotationSHA256, PricePolicy: string(ZTAPIPricePolicyPoolOfficial80)},
		{SourceModel: "qwen3.8-max", SourceDocumentChecksum: ZTAPIQuotationSHA256, ResourceType: "official", PricePolicy: string(ZTAPIPricePolicyEnterprise20Margin)},
		{SourceModel: "gemini-3.1-pro-preview", PricePolicy: string(ZTAPIPricePolicyCashPool20261030)},
	} {
		require.True(t, ztapiQuotationRouteRestrictionApplies20261030(source), source.SourceModel)
	}
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	require.True(t, ztapiQuotationRouteRestrictionApplies20261030(&ZTAPIModelPriceSource{SourceModel: "gpt-image-2", SourceDocumentChecksum: quote.WorkbookSHA256}))
	require.False(t, ztapiQuotationRouteRestrictionApplies20261030(&ZTAPIModelPriceSource{SourceModel: "gpt-5.6-sol", SourceDocumentChecksum: quote.WorkbookSHA256}), "unrelated legacy pool-admission contract retains its own validator")
}

func TestQuotationRoutes20261030SameModelUsesSelectedABResource(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	for _, cell := range []string{"D136", "D86", "D36", "D100", "D52"} {
		t.Run(cell, func(t *testing.T) {
			var row ZTAPIABQuotationEntry
			for _, entry := range quote.Entries {
				if entry.QuotationCell == cell {
					row = entry
				}
			}
			require.Equal(t, "A", row.Grade)
			source := &ZTAPIModelPriceSource{SourceDocumentChecksum: quote.WorkbookSHA256,
				QuotationCell: cell, QuotationGrade: row.Grade, QuotationModelCode: row.ModelCode,
				ResourceType: "enterprise", PricePolicy: string(ZTAPIPricePolicyEnterprise20Margin)}
			source.SourceModel = map[string]string{"D136": "claude-opus-5", "D86": "gpt-5.6-luna", "D36": "glm-5.3", "D100": "gpt-image-2", "D52": "doubao-seedance-2-5"}[cell]
			if cell == "D52" {
				source.ResourceType = "original_resource"
			}
			require.True(t, ztapiQuotationSourceChannelPermitted20261030(source, 1))
			for _, channel := range []int{2, 3, 4, 8} {
				require.False(t, ztapiQuotationSourceChannelPermitted20261030(source, channel))
			}
			source.QuotationCell = ""
			require.False(t, ztapiQuotationSourceChannelPermitted20261030(source, 1))
		})
	}
}
