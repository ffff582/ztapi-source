package common

import (
	"net/http/httptest"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIPublicationSnapshotPreservesQuoteProvenance(t *testing.T) {
	var snapshot ZTAPIPublicationSnapshot
	const contractJSON = `{"version":1,"modality":"image","rules":[]}`
	require.NoError(t, basecommon.Unmarshal([]byte(`{"PriceSourceID":17,"PriceSourceVersion":3,"BillingDimensions":["input_tokens","cache_read"],"SaleUSD":{"input_tokens":"1.2345678901","cache_read":"0"},"Modality":"embedding","MediaPriceContractJSON":"{\"version\":1,\"modality\":\"image\",\"rules\":[]}"}`), &snapshot))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetZTAPIPublicationSnapshot(c, &snapshot)
	encoded, err := basecommon.Marshal(GetZTAPIPublicationSnapshot(c))
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, basecommon.Unmarshal(encoded, &decoded))
	require.Equal(t, float64(17), decoded["PriceSourceID"])
	require.Equal(t, float64(3), decoded["PriceSourceVersion"])
	require.Equal(t, []any{"input_tokens", "cache_read"}, decoded["BillingDimensions"])
	require.Equal(t, map[string]any{"input_tokens": "1.2345678901", "cache_read": "0"}, decoded["SaleUSD"])
	require.Equal(t, "embedding", decoded["Modality"])
	require.Equal(t, contractJSON, decoded["MediaPriceContractJSON"])
	require.Equal(t, contractJSON, snapshot.Clone().MediaPriceContractJSON)
}

func TestZTAPIPublicationSnapshotDeepCopiesImageProtocolContract(t *testing.T) {
	contract := types.ZTAPIImageProtocolContract{
		Capabilities: types.ZTAPIImageCapabilities{Sizes: []string{"1024x1024"}},
		Response:     types.ZTAPIImageResponseContract{ResultFields: map[string]string{"url": "url"}},
		Usage:        types.ZTAPIImageUsageContract{Fields: map[string]string{"input_tokens": "input_tokens"}},
	}
	snapshot := &ZTAPIPublicationSnapshot{ImageProtocolContract: &contract}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	SetZTAPIPublicationSnapshot(c, snapshot)

	snapshot.ImageProtocolContract.Capabilities.Sizes[0] = "changed"
	snapshot.ImageProtocolContract.Response.ResultFields["url"] = "changed"
	snapshot.ImageProtocolContract.Usage.Fields["input_tokens"] = "changed"
	first := GetZTAPIPublicationSnapshot(c)
	require.Equal(t, "1024x1024", first.ImageProtocolContract.Capabilities.Sizes[0])
	require.Equal(t, "url", first.ImageProtocolContract.Response.ResultFields["url"])
	require.Equal(t, "input_tokens", first.ImageProtocolContract.Usage.Fields["input_tokens"])

	first.ImageProtocolContract.Capabilities.Sizes[0] = "mutated"
	require.Equal(t, "1024x1024", GetZTAPIPublicationSnapshot(c).ImageProtocolContract.Capabilities.Sizes[0])
}

func TestZTAPIPublicationSnapshotDeepCopiesVideoProtocolContract(t *testing.T) {
	contract := types.ZTAPIVideoProtocolContract{
		Capabilities: types.ZTAPIVideoCapabilities{Resolutions: []string{"720p"}, DurationSeconds: []int{5}},
		States:       types.ZTAPIVideoStateContract{Accepted: []string{"queued"}},
		Usage:        types.ZTAPIVideoUsageContract{Fields: map[string]string{"credits": "usage.credits"}},
		Reservations: []types.ZTAPIVideoReservationAuthority{{Resolution: "720p", DurationSeconds: 5, MaximumDimensions: map[string]string{"credits": "1"}}},
	}
	snapshot := &ZTAPIPublicationSnapshot{VideoProtocolContract: &contract}
	first := snapshot.Clone()

	snapshot.VideoProtocolContract.Capabilities.Resolutions[0] = "changed"
	snapshot.VideoProtocolContract.States.Accepted[0] = "changed"
	snapshot.VideoProtocolContract.Usage.Fields["credits"] = "changed"
	snapshot.VideoProtocolContract.Reservations[0].MaximumDimensions["credits"] = "2"

	require.Equal(t, "720p", first.VideoProtocolContract.Capabilities.Resolutions[0])
	require.Equal(t, "queued", first.VideoProtocolContract.States.Accepted[0])
	require.Equal(t, "usage.credits", first.VideoProtocolContract.Usage.Fields["credits"])
	require.Equal(t, "1", first.VideoProtocolContract.Reservations[0].MaximumDimensions["credits"])
}
