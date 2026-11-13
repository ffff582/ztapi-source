package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIQuotationABContainsOnlyEnterpriseAndPoolRows(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	require.Equal(t, "3bc554b32065f3cc7c50ffae545af1d20fff1e908732f98b88a2970a3fcb4690", quote.WorkbookSHA256)
	require.Len(t, quote.Entries, 72)
	models := make(map[string]struct{})
	for _, entry := range quote.Entries {
		require.Contains(t, []string{"A", "B"}, entry.Grade)
		require.NotEmpty(t, entry.ModelCode)
		require.NotEmpty(t, entry.ModelOriginal)
		require.NotEmpty(t, entry.OfficialPriceText)
		require.NotEmpty(t, entry.QuotationCell)
		require.NotEmpty(t, entry.OfficialPriceCell)
		require.NotEmpty(t, entry.ResourceType)
		models[strings.ToLower(entry.ModelName)] = struct{}{}
	}
	require.Len(t, models, 50)
}

func TestZTAPIQuotationABDoesNotTreatClosedRowsAsActive(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	var fableEnterprise, fablePool, deepseekClosed bool
	for _, entry := range quote.Entries {
		if entry.ModelName == "Claude Fable 5" && entry.Grade == "A" {
			fableEnterprise = true
			require.False(t, entry.Active)
		}
		if entry.ModelName == "Claude Fable 5" && entry.Grade == "B" {
			fablePool = true
			require.True(t, entry.Active)
		}
		if entry.ModelName == "DeepSeek V4 Pro" && entry.Grade == "B" && entry.QuotedFraction == "0.5" {
			deepseekClosed = true
			require.False(t, entry.Active)
		}
	}
	require.True(t, fableEnterprise && fablePool && deepseekClosed)
}

func TestZTAPIEnterpriseBasisDoesNotUsePoolPrice(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	rows, err := quote.EnterpriseBasis("GPT 5.6 Sol")
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, "A", row.Grade)
		require.True(t, row.Active)
	}
	var poolIsCheaper bool
	for _, row := range quote.Entries {
		if row.ModelName == "GPT 5.6 Sol" && row.Grade == "B" && row.Active {
			poolIsCheaper = true
		}
	}
	require.True(t, poolIsCheaper)
}

func TestZTAPIEnterpriseBasisFailsClosedWithoutActiveAQuote(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	for _, name := range []string{"Claude Fable 5", "GLM 5.2", "Kimi K3"} {
		rows, err := quote.EnterpriseBasis(name)
		require.Error(t, err)
		require.Empty(t, rows)
	}
}

func TestZTAPIQuotationPricingBasisPrefersEnterpriseAndFallsBackToPool(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	tests := []struct {
		name   string
		grade  string
		policy ZTAPIPricePolicy
	}{
		{name: "GPT 5.6 Sol", grade: "A", policy: ZTAPIPricePolicyEnterprise20Margin},
		{name: "Claude Fable 5", grade: "B", policy: ZTAPIPricePolicyPool30Margin},
		{name: "GLM 5.2", grade: "B", policy: ZTAPIPricePolicyPool30Margin},
	}
	for _, test := range tests {
		rows, policy, err := quote.PricingBasis(test.name)
		require.NoError(t, err)
		require.NotEmpty(t, rows)
		require.Equal(t, test.policy, policy)
		for _, row := range rows {
			require.Equal(t, test.grade, row.Grade)
			require.True(t, row.Active)
		}
	}
}

func TestZTAPIQuotationPricingBasisCoversFiftyModelsWithoutS(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	names := map[string]struct{}{}
	for _, row := range quote.Entries {
		names[row.ModelName] = struct{}{}
	}
	enterprise, pool := 0, 0
	for name := range names {
		rows, policy, err := quote.PricingBasis(name)
		require.NoError(t, err)
		for _, row := range rows {
			require.NotEqual(t, "S", row.Grade)
		}
		switch policy {
		case ZTAPIPricePolicyEnterprise20Margin:
			enterprise++
		case ZTAPIPricePolicyPool30Margin:
			pool++
		default:
			t.Fatalf("unexpected price policy %q", policy)
		}
	}
	require.Equal(t, 39, enterprise)
	require.Equal(t, 11, pool)
}

func TestZTAPIQuotationABStructuredRulesAndPublicationBlockers(t *testing.T) {
	quote, err := ZTAPIQuotationABEntries()
	require.NoError(t, err)
	selected, ready := 0, 0
	for _, row := range quote.Entries {
		if !row.PricingBasis {
			continue
		}
		selected++
		if row.PricingBlocker != "" {
			require.Empty(t, row.TokenPriceRules)
			continue
		}
		ready++
		require.Equal(t, "text", row.Modality)
		require.NotEmpty(t, row.TokenPriceRules)
		for _, rule := range row.TokenPriceRules {
			require.NotEmpty(t, rule.Cost[ZTAPIBillingDimensionInputTokens])
			require.NotEmpty(t, rule.Sale[ZTAPIBillingDimensionInputTokens])
		}
	}
	require.Equal(t, 54, selected)
	require.Equal(t, 41, ready)
	rows, policy, err := quote.PublishablePricingBasis("GPT 5.6 Sol")
	require.NoError(t, err)
	require.Equal(t, ZTAPIPricePolicyEnterprise20Margin, policy)
	require.Len(t, rows, 1)
	require.Equal(t, "3.9", rows[0].TokenPriceRules[0].Sale[ZTAPIBillingDimensionInputTokens])
	require.Equal(t, "7.8", rows[0].TokenPriceRules[1].Sale[ZTAPIBillingDimensionInputTokens])
	_, _, err = quote.PublishablePricingBasis("Doubao Seed 2.0 Pro")
	require.ErrorContains(t, err, "paid cache storage")
	_, _, err = quote.PublishablePricingBasis("Seedance 2.0")
	require.ErrorContains(t, err, "media quotation")
}
