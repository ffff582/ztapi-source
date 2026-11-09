package service

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func partialSaleFixture(t *testing.T, operation, size, quality string, n uint, refs, width, height int) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for i := 0; i < refs; i++ {
		part, err := writer.CreateFormFile("image", "ref.png")
		require.NoError(t, err)
		require.NoError(t, png.Encode(part, image.NewRGBA(image.Rect(0, 0, width, height))))
	}
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	path := "/v1/images/generations"
	if operation == "edit" {
		path = "/v1/images/edits"
	}
	c.Request = httptest.NewRequest("POST", path, &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, c.Request.ParseMultipartForm(32<<20))
	t.Cleanup(func() { _ = c.Request.MultipartForm.RemoveAll() })
	info := &relaycommon.RelayInfo{Request: &dto.ImageRequest{Prompt: "hello", N: &n, Size: size, Quality: quality, ResponseFormat: "b64_json"}, RequestURLPath: path,
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-image-2", ImageSaleContractJSON: types.ZTAPIImageSalePartial20261109JSON(), ImageProtocolContract: &types.ZTAPIImageProtocolContract{
			Capabilities: types.ZTAPIImageCapabilities{RenderOptions: []types.ZTAPIImageRenderOption{{Size: size, Quality: quality, Resolution: "1K"}}},
			Edit:         &types.ZTAPIImageEditEndpointContract{InputField: "image", MaxInputFiles: 15, MaxInputBytes: 20 << 20, MaxTotalInputBytes: 256 << 20}}}}
	return c, info
}

func TestZTAPIImageSalePartialRejectsBeforeQuoteFreeze(t *testing.T) {
	InitTokenEncoders()
	for _, tc := range []struct {
		name, op, size, quality string
		n                       uint
		refs, w, h              int
		prompt                  string
	}{
		{"size", "generation", "2048x2048", "low", 1, 0, 1, 1, "hello"},
		{"quality", "generation", "1024x1024", "high", 1, 0, 1, 1, "hello"},
		{"count", "generation", "1024x1024", "low", 2, 0, 1, 1, "hello"},
		{"prompt", "generation", "1024x1024", "low", 1, 0, 1, 1, strings.Repeat("hello world ", 2100)},
		{"generation_upload", "generation", "1024x1024", "low", 1, 1, 1, 1, "hello"},
		{"extra_refs", "edit", "1024x1024", "low", 1, 2, 1, 1, "hello"},
		{"pixels", "edit", "1024x1024", "low", 1, 1, 2049, 2048, "hello"},
		{"missing_ref", "edit", "1024x1024", "low", 1, 0, 1, 1, "hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, info := partialSaleFixture(t, tc.op, tc.size, tc.quality, tc.n, tc.refs, tc.w, tc.h)
			info.Request.(*dto.ImageRequest).Prompt = tc.prompt
			require.Error(t, PrepareZTAPIImageSaleQuote(c, info))
			quote, _ := info.GetZTAPIImageSaleQuote()
			require.Nil(t, quote)
			require.Nil(t, info.Billing)
		})
	}
}

func TestZTAPIImageSalePartialMutableRequestRejected(t *testing.T) {
	InitTokenEncoders()
	c, info := partialSaleFixture(t, "edit", "1024x1024", "low", 1, 1, 2048, 2048)
	require.NoError(t, PrepareZTAPIImageSaleQuote(c, info))
	require.NoError(t, VerifyZTAPIImageSaleQuote(c, info))
	info.Request.(*dto.ImageRequest).Prompt = "a changed short prompt"
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info))
	info.Request.(*dto.ImageRequest).Prompt = strings.Repeat("hello world ", 2100)
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info))
	info.Request.(*dto.ImageRequest).Prompt = "hello"
	c.Request.MultipartForm.File["image"] = append(c.Request.MultipartForm.File["image"], c.Request.MultipartForm.File["image"][0])
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info))
}

func TestZTAPIImageSalePartialPassedCasesAndOutboundMutation(t *testing.T) {
	InitTokenEncoders()
	for _, allowed := range []struct {
		operation, size, resolution string
		refs                        int
	}{
		{"generation", "1024x1024", "1K", 0}, {"generation", "1152x2048", "2K", 0}, {"edit", "1024x1024", "1K", 1},
	} {
		t.Run(allowed.operation+allowed.size, func(t *testing.T) {
			c, info := partialSaleFixture(t, allowed.operation, allowed.size, "low", 1, allowed.refs, 2048, 2048)
			protocol := info.ZTAPIPublicationSnapshot.ImageProtocolContract
			protocol.Capabilities.RenderOptions[0].Resolution = allowed.resolution
			require.NoError(t, PrepareZTAPIImageSaleQuote(c, info))
			quote, _ := info.GetZTAPIImageSaleQuote()
			require.Equal(t, "0.3", quote.TotalUSD)
			require.Equal(t, allowed.resolution, quote.Resolution)
			if allowed.operation == "generation" {
				protocol.Path, protocol.ProviderModel = "/v1/images/generations", "gpt-image-2"
				fields := map[string]any{"model": "gpt-image-2", "prompt": "hello", "n": 1, "size": allowed.size, "quality": "low", "response_format": "b64_json"}
				wire, err := common.Marshal(fields)
				require.NoError(t, err)
				dispatch := &relaycommon.ZTAPIManagedImageDispatch{ProviderPath: protocol.Path, Body: wire, ContentType: "application/json"}
				require.NoError(t, VerifyZTAPIImageSaleDispatch(info, dispatch))
				fields["prompt"] = "changed short prompt"
				dispatch.Body, err = common.Marshal(fields)
				require.NoError(t, err)
				require.Error(t, VerifyZTAPIImageSaleDispatch(info, dispatch))
			}
		})
	}
}

func TestZTAPIImageSalePartialReservationRejectsUnacceptedSelector(t *testing.T) {
	input := ztapiImageReservationFixture(t, 20)
	protocol, _, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	require.NoError(t, err)
	protocol.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{Size: "1024x1024", Quality: "standard", Resolution: "1K"}}
	selector, _, err := canonicalZTAPIMediaSelector(input.SelectorJSON)
	require.NoError(t, err)
	quote, err := types.QuoteZTAPIImageSale(types.ZTAPIImageSalePartial20261109JSON(), "1K", 1, 0, 1)
	require.NoError(t, err)
	_, _, err = deriveZTAPIImageSaleReservation(types.ZTAPIImageSalePartial20261109JSON(), protocol, selector, &quote, decimal.NewFromInt(500000))
	require.Error(t, err)
	selector.Quality = "low"
	protocol.Capabilities.RenderOptions[0].Quality = "low"
	for i := range protocol.Reservations {
		protocol.Reservations[i].Quality = "low"
	}
	dimensions, quota, err := deriveZTAPIImageSaleReservation(types.ZTAPIImageSalePartial20261109JSON(), protocol, selector, &quote, decimal.NewFromInt(500000))
	require.NoError(t, err)
	require.EqualValues(t, 150000, quota)
	require.Equal(t, map[string]string{"images": "1"}, dimensions)
}
