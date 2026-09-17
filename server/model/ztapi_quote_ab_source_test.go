package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIABTextPriceSourceUsesEnterpriseLongTierAndFrozenCells(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", source.SourceModel)
	require.Equal(t, "A", source.QuotationGrade)
	require.Equal(t, quote.WorkbookSHA256, source.SourceDocumentChecksum)
	require.Equal(t, string(ZTAPIPricePolicyEnterprise20Margin), source.PricePolicy)
	require.Equal(t, "6.2400000000", source.InputPerMillion)
	require.Equal(t, "23.4000000000", source.OutputPerMillion)
	var rules []struct {
		Conditions []string          `json:"conditions"`
		Sale       map[string]string `json:"sale"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 2)
	require.Equal(t, "3.9000000000", rules[0].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "7.8000000000", rules[1].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "29.2500000000", rules[1].Sale[ZTAPIBillingDimensionOutputTokens])
}

func TestZTAPIABTextPriceSourceConvertsCNYBeforeFrozenCharge(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GLM 5.2", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Equal(t, "CNY", source.Currency)
	require.Equal(t, "7.2000000000", source.CNYPerUSD)
	var rules []struct {
		Sale map[string]string `json:"sale"`
	}
	require.NoError(t, json.Unmarshal([]byte(source.TokenPriceRulesJSON), &rules))
	require.Len(t, rules, 1)
	require.Equal(t, "1.1771428571", rules[0].Sale[ZTAPIBillingDimensionInputTokens])
}

func TestZTAPIABEmbeddingUsesFlatInputOnlyPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "Text Embedding 3 Small", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.Empty(t, source.TokenPriceRulesJSON)
	require.Equal(t, `["input_tokens"]`, source.BillingDimensions)
	preview, err := BuildZTAPIModelPricePreview(&source)
	require.NoError(t, err)
	require.Equal(t, "0.0195000000", preview.InputSaleUSDPerMillion)
	require.Equal(t, "0.0000000000", preview.OutputSaleUSDPerMillion)
}

func TestZTAPIABTextPriceSourceRejectsUnmappedAndIncompleteQuotes(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "Gemini 3.8 Flash", 11, 7, 1_789_000_000)
	require.Error(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "GPT6 Astra", 11, 7, 1_789_000_000)
	require.Error(t, err)
	_, err = BuildZTAPIABTextPriceSource(quote, "DeepSeek V4 Pro", 11, 7, 1_789_000_000)
	require.ErrorContains(t, err, "unsupported quotation condition")
}

func TestZTAPIABSourceEvidenceRejectsChangedQuoteCellAndIdentity(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	source, err := BuildZTAPIABTextPriceSource(quote, "GPT 5.6 Sol", 11, 7, 1_789_000_000)
	require.NoError(t, err)
	require.NoError(t, validateZTAPIABPriceSource(&source))
	require.NoError(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-gpt-5.6-sol", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, quote.WorkbookSHA256))
	source.QuotationCell = "D999"
	require.Error(t, validateZTAPIABPriceSource(&source))
	require.Error(t, ValidateZTAPIQuotationIdentity(source.SourceModel, "zt-gpt-5.6-luna", ZTAPIProtocolOpenAICompatible, ZTAPIProviderOpenAI, quote.WorkbookSHA256))
}

func TestZTAPIABExactIdentityAndTextPriceReadinessCounts(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	frozen, err := ZTAPIQuotationEntries()
	require.NoError(t, err)
	bridge, err := BuildZTAPIABQuotationIdentityBridge(quote, frozen)
	require.NoError(t, err)
	require.Len(t, bridge.Mapped, 47)
	require.Len(t, bridge.Unmatched, 3)
	ready, blocked := 0, 0
	for _, identity := range bridge.Mapped {
		if _, err := BuildZTAPIABTextPriceSource(quote, identity.ModelName, 1, 7, 1_789_000_000); err == nil {
			ready++
		} else {
			blocked++
		}
	}
	require.Equal(t, 39, ready)
	require.Equal(t, 8, blocked)
}
