package model

import (
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCashImage20261030SharedReaderAndSourcePolicy(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	derived, err := deriveZTAPICashImageSource20261030(&base, 1, platformFXParamsForTest("6.63", "6.7138"))
	require.NoError(t, err)
	parsed, err := types.ParseZTAPIMediaPriceContract(derived.MediaPriceContractJSON)
	require.NoError(t, err)
	require.Len(t, parsed.Rules, 10)
	preview, err := BuildZTAPIModelPricePreview(&derived)
	require.NoError(t, err)
	require.True(t, ztapiSameDecimal(preview.InputCostUSDPerMillion, decimal.RequireFromString("26.18382").Div(decimal.RequireFromString("6.63")).Round(10).String()))
	require.True(t, ztapiSameDecimal(preview.OutputCostUSDPerMillion, decimal.RequireFromString("157.10292").Div(decimal.RequireFromString("6.63")).Round(10).String()))
	maximum, err := validateZTAPICashImageShape20261030(&derived)
	require.NoError(t, err)
	require.True(t, ztapiSameDecimal(preview.InputSaleUSDPerMillion, maximum["input_tokens"].String()))
	require.True(t, decimal.RequireFromString(preview.InputSaleUSDPerMillion).GreaterThan(decimal.RequireFromString("8.19")))
	require.True(t, ztapiSameDecimal(preview.OutputSaleUSDPerMillion, maximum["output_tokens"].String()))
}

func TestCashImage20261030DBParentValidationUsesChildFrozenFX(t *testing.T) {
	db := setupZTAPIPublicCatalogTestDB(t)
	base := cashImageLegacy20261030ForTest(t)
	require.NoError(t, db.Create(&base).Error)
	derived, err := deriveZTAPICashImageSource20261030(&base, 1, platformFXParamsForTest("6.63", "6.7138"))
	require.NoError(t, err)
	derived.Version = base.Version + 1
	require.NoError(t, db.Create(&derived).Error)
	// No global FX settings exist in this DB. A reader must use the child's
	// immutable rates, not today's exchange-rate configuration.
	require.NoError(t, ValidateZTAPICashMarginSource20261030(db, &derived))
	forged := derived
	forged.MediaPriceContractJSON = base.MediaPriceContractJSON
	require.Error(t, ValidateZTAPICashMarginSource20261030(db, &forged))
}

func TestCashImage20261030UnsupportedExtremeFXFloorCannotPublish(t *testing.T) {
	base := cashImageLegacy20261030ForTest(t)
	derived, err := deriveZTAPICashImageSource20261030(&base, 1, platformFXParamsForTest("8", "1"))
	require.NoError(t, err)
	// An unsupported cost-share after preserving old retail must fail admission,
	// not widen the shared parser or silently replace the current snapshot.
	require.Error(t, ValidateZTAPIModelPriceSource(&derived))
}
