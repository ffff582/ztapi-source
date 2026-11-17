package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIPriceCatalogFollowupGeminiLatestIdentity(t *testing.T) {
	require.NoError(t, ValidateZTAPIQuotationIdentity("gemini-3.5-flash", "zt-gemini-3.5-flash", ZTAPIProtocolOpenAICompatible, ZTAPIProviderGoogle, ZTAPISupplierWorkbook20261019SHA256))
	require.Error(t, ValidateZTAPIQuotationIdentity("gemini-3.5-flash", "zt-other", ZTAPIProtocolOpenAICompatible, ZTAPIProviderGoogle, ZTAPISupplierWorkbook20261019SHA256))
}

func TestZTAPIPriceCatalogFollowupSingleTierKeepsOfficialReference(t *testing.T) {
	p := ZTAPIRuntimePublication{SourceModel: "deepseek-v4-pro", PublicName: "zt-deepseek-v4-pro", ProviderFamily: ZTAPIProviderDeepSeek,
		Protocol: ZTAPIProtocolOpenAICompatible, Groups: []string{"default"}, SnapshotID: 9999,
		BillingDimensions:   []string{"input_tokens", "output_tokens", "cache_read"},
		TokenPriceRulesJSON: `[{"conditions":[],"sale":{"input_tokens":"1.6198","output_tokens":"4.8592","cache_read":"0.0540"}}]`,
	}
	items := buildZTAPIPublicCatalog([]ZTAPIRuntimePublication{p})
	require.Len(t, items, 1)
	require.NotNil(t, items[0].OfficialReference)
	require.Len(t, items[0].TokenPriceRules[0].OfficialUSD, 3)
	require.Contains(t, items[0].OfficialReference.Note, "峰时")
	require.Equal(t, "1.6198", items[0].TokenPriceRules[0].SaleUSD["input_tokens"])
}

func TestZTAPIPriceCatalogFollowupAllPublishedRemainQuoted(t *testing.T) {
	db := reductionFixture20261107(t)
	_, err := ApplyZTAPIPriceReduction20261107(9, ApprovedZTAPIPriceReductionSelections20261107(), false)
	require.NoError(t, err)
	publications, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	names := make([]string, 0, len(publications))
	for _, p := range publications {
		names = append(names, p.PublicName)
	}
	require.Equal(t, 16, len(publications), "all approved model identities must survive the full quoted runtime reader: %v", names)
	items := buildZTAPIPublicCatalog(publications)
	require.Len(t, items, 16)
	references := 0
	for _, item := range items {
		if item.OfficialReference != nil {
			references++
		}
	}
	require.Equal(t, 15, references)
}

func TestZTAPIPriceCatalogFollowupRetainedGeminiAndFourteenReductions(t *testing.T) {
	db := reductionFixture20261107(t)
	_, err := ApplyZTAPIPriceReduction20261107(9, ApprovedZTAPIPriceReductionSelections20261107(), false)
	require.NoError(t, err)
	var config ZTAPIModelConfig
	require.NoError(t, db.First(&config, 66).Error)
	var applied ZTAPIModelPublicationSnapshot
	require.NoError(t, db.First(&applied, config.PublicationSnapshotID).Error)
	original, _, _, _, err := ztapiReductionApproval20261107(66)
	require.NoError(t, err)
	_, err = ApplyZTAPIPriceReduction20261107(9, []ZTAPIPriceReductionSelection20261107{{ModelID: 66, Version: config.Version, SnapshotID: applied.ID, SourceID: applied.PriceSourceID, RestoreSnapshotID: original.SnapshotID}}, true)
	require.NoError(t, err)
	publications, err := loadZTAPIQuotedPublications(db)
	require.NoError(t, err)
	require.Len(t, publications, 16)
	changed := 0
	for _, p := range publications {
		if p.PricePolicy == string(ZTAPIPricePolicyReduction20261107) {
			changed++
		}
		if p.ModelConfigID == 66 {
			require.Equal(t, original.Input.InexactFloat64(), p.InputPricePerMillion)
			require.Equal(t, original.Version+2, p.Version)
		}
	}
	require.Equal(t, 14, changed)
	items := buildZTAPIPublicCatalog(publications)
	references := 0
	for _, item := range items {
		if item.OfficialReference != nil {
			references++
		}
	}
	require.Equal(t, 15, references)
}
