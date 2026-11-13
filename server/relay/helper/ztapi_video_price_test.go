package helper

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func ztapiVideoRelayInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "zt-seedance-2.0",
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: "zt-seedance-2.0", SourceModel: "doubao-seedance-2.0", Modality: "video",
			PriceSourceID: 31, PriceSourceVersion: 4,
		},
	}
}

// A video model is priced by its frozen media contract, never by the legacy
// ratio table it is deliberately absent from. Reading that table here refused
// every customer's video as unpriced.
func TestZTAPIVideoPerCallPriceLeavesTheChargeToTheMediaContract(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := ztapiVideoRelayInfo()

	price, err := ModelPriceHelperPerCall(c, info)

	require.NoError(t, err)
	require.True(t, price.UsePrice)
	// The durable reservation replaces this quota before anything is
	// pre-consumed, so nothing here may look like a free request either.
	require.Zero(t, price.Quota)
	require.False(t, price.FreeModel)
	require.Zero(t, price.ModelRatio)
	require.Zero(t, price.ModelPrice)
}

// A channel test carries no customer and reserves nothing, so it keeps reading
// the legacy table exactly as before. It still finds no entry there, which is
// the behaviour this change deliberately leaves alone.
func TestZTAPIVideoPerCallPriceLeavesAChannelTestAlone(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := ztapiVideoRelayInfo()
	info.IsChannelTest = true

	_, err := ModelPriceHelperPerCall(c, info)

	require.ErrorContains(t, err, "zt-seedance-2.0")
}

func TestPerCallPriceStillRefusesAnUnpricedModelOutsideZTAPI(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{OriginModelName: "not-a-configured-model"}

	_, err := ModelPriceHelperPerCall(c, info)

	require.ErrorContains(t, err, "not-a-configured-model")
}

// Only a video model carries a reservation that replaces this quota. An image
// model is priced by the token helper instead and never reaches here, so the
// per-call helper keeps treating it exactly as it did before.
func TestZTAPIImagePerCallPriceIsUnchanged(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := ztapiVideoRelayInfo()
	info.ZTAPIPublicationSnapshot.Modality = "image"

	_, err := ModelPriceHelperPerCall(c, info)

	require.ErrorContains(t, err, "zt-seedance-2.0")
}
