package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestZTAPIImageSalePricingProjectionRetainsRetailContract(t *testing.T) {
	_, _ = setupImageSalePublicationForTest(t)
	_, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	publications, err := loadZTAPIActivePublications(DB)
	require.NoError(t, err)
	pricing := projectZTAPIPublicPricing(publications)
	require.Len(t, pricing, 1)
	require.NotNil(t, pricing[0].ImageSale)
	require.Equal(t, types.ZTAPIImageSaleBillingUnit, pricing[0].BillingUnit)
	require.NotContains(t, pricing[0].ImageSale.BasePrices, "4K")
	require.Len(t, pricing[0].PricingRules, 3)
	for _, rule := range pricing[0].PricingRules {
		require.NotEmpty(t, rule.Conditions["image_operation"])
		require.NotEmpty(t, rule.Conditions["size"])
		require.Equal(t, "low", rule.Conditions["quality"])
		require.False(t, rule.Conditions["image_operation"] == "edit" && rule.Conditions["resolution"] == "2K")
	}
}

func TestZTAPIImageSalePartialCatalogRestrictsOptionsWithoutMutatingProtocol(t *testing.T) {
	_, _ = setupImageSalePublicationForTest(t)
	_, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	publications, err := loadZTAPIActivePublications(DB)
	require.NoError(t, err)
	before, err := common.Marshal(publications[0].ImageProtocolContract)
	require.NoError(t, err)
	catalog := buildZTAPIPublicCatalog(publications)
	pricing := projectZTAPIPublicPricing(publications)
	require.Len(t, catalog, 1)
	require.Len(t, pricing, 1)
	for _, options := range []*ZTAPIPublicSupportedOptions{catalog[0].SupportedOptions, pricing[0].SupportedOptions} {
		require.NotNil(t, options)
		require.ElementsMatch(t, []string{"1024x1024", "1152x2048"}, options.Sizes)
		require.Equal(t, []string{"low"}, options.Qualities)
		require.Empty(t, options.Resolutions, "image resolution is represented by render_options, not the video-only resolutions field")
		require.Equal(t, 1, options.MaxCount)
		raw, err := common.Marshal(options)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"allowed_requests"`)
		require.NotContains(t, string(raw), "4K")
	}
	after, err := common.Marshal(publications[0].ImageProtocolContract)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}

func TestZTAPIImageSalePartialFiltersRenderOptionsAndEditFiles(t *testing.T) {
	publication := ZTAPIRuntimePublication{ModelConfigID: 85, SourceModel: "gpt-image-2", PublicName: "zt-gp-image-2", ProviderFamily: ZTAPIProviderOpenAI, Protocol: ZTAPIProtocolOpenAICompatible, Modality: ZTAPIModalityImage, ImageSaleContractJSON: types.ZTAPIImageSalePartial20261109JSON()}
	original := &ZTAPIPublicSupportedOptions{MaxCount: 10, SupportsEdits: true, EditInput: &ZTAPIPublicImageEditInput{MaxFiles: 16}, RenderOptions: []ZTAPIPublicImageRenderOption{
		{Size: "1024x1024", Quality: "low", Resolution: "1K"},
		{Size: "1152x2048", Quality: "low", Resolution: "2K"},
		{Size: "2048x2048", Quality: "low", Resolution: "2K"},
		{Size: "4096x4096", Quality: "high", Resolution: "4K"},
	}}
	item := ZTAPIPublicCatalogItem{SupportedOptions: original}
	require.True(t, applyZTAPIPublicImageSale(publication, &item))
	require.Len(t, item.SupportedOptions.RenderOptions, 2)
	require.Len(t, item.SupportedOptions.AllowedRequests, 3)
	require.Equal(t, 1, item.SupportedOptions.EditInput.MaxFiles)
	require.Equal(t, 16, original.EditInput.MaxFiles)
	require.Len(t, original.RenderOptions, 4)
}

func TestZTAPIImageSaleHealthUnpublishRetainsPolicyPointer(t *testing.T) {
	db, _ := setupImageSalePublicationForTest(t)
	next, err := PublishZTAPIImageSale(7, approvedImageSalePublicationRequestForTest())
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ZTAPIHealthIncident{}))
	store := NewZTAPIHealthStore(db)
	err = db.Transaction(func(tx *gorm.DB) error {
		config, err := store.unpublishTx(tx, &ZTAPIHealthState{ModelID: 85}, 999)
		if err != nil {
			return err
		}
		require.False(t, config.Published)
		require.Equal(t, next.ID, config.PublicationSnapshotID)
		return nil
	})
	require.NoError(t, err)
}
