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
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleReservationIsExactAndSelectorBound(t *testing.T) {
	input := ztapiImageReservationFixture(t, 20)
	protocol, _, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	require.NoError(t, err)
	protocol.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{AspectRatio: "1:1", Resolution: "1K", Size: "1024x1024", Quality: "standard"}}
	selector, _, err := canonicalZTAPIMediaSelector(input.SelectorJSON)
	require.NoError(t, err)
	quote, err := types.QuoteZTAPIImageSale(types.ZTAPIImageSaleContract20261109JSON(), "1K", 2001, 0, 1)
	require.NoError(t, err)
	dimensions, quota, err := deriveZTAPIImageSaleReservation(types.ZTAPIImageSaleContract20261109JSON(), protocol, selector, &quote, decimal.NewFromInt(500000))
	require.NoError(t, err)
	require.EqualValues(t, 160000, quota)
	require.Equal(t, map[string]string{"images": "1"}, dimensions)
	_, _, err = deriveZTAPIImageSaleReservation(types.ZTAPIImageSaleContract20261109JSON(), protocol, selector, &quote, decimal.NewFromInt(1))
	require.Error(t, err, "an unrepresentable exact retail price must not be rounded up and charged")
	quote.Images = 2
	_, _, err = deriveZTAPIImageSaleReservation(types.ZTAPIImageSaleContract20261109JSON(), protocol, selector, &quote, decimal.NewFromInt(500000))
	require.Error(t, err)
}

func TestZTAPIImageSaleQuoteInspectsUploadedDimensionsAndLongPrompt(t *testing.T) {
	InitTokenEncoders()
	prompt := strings.Repeat("a long user prompt ", 2100)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{"model": "gpt-image-2", "prompt": prompt, "n": "2", "size": "1024x1024", "quality": "low", "response_format": "b64_json"} {
		require.NoError(t, writer.WriteField(key, value))
	}
	for i := 0; i < 2; i++ {
		part, err := writer.CreateFormFile("image", "reference.png")
		require.NoError(t, err)
		require.NoError(t, png.Encode(part, image.NewRGBA(image.Rect(0, 0, 2049, 2048))))
	}
	require.NoError(t, writer.Close())
	wire := append([]byte(nil), body.Bytes()...)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/images/edits", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, c.Request.ParseMultipartForm(32<<20))
	defer c.Request.MultipartForm.RemoveAll()
	count := uint(2)
	info := &relaycommon.RelayInfo{Request: &dto.ImageRequest{Prompt: prompt, N: &count, Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json"}, RequestURLPath: "/v1/images/edits", ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-image-2", ImageSaleContractJSON: types.ZTAPIImageSaleContract20261109JSON(), ImageProtocolContract: &types.ZTAPIImageProtocolContract{ProviderModel: "gpt-image-2", Capabilities: types.ZTAPIImageCapabilities{RenderOptions: []types.ZTAPIImageRenderOption{{Size: "1024x1024", Quality: "low", Resolution: "1K"}}}, Edit: &types.ZTAPIImageEditEndpointContract{InputField: "image", MaxInputFiles: 15, MaxInputBytes: 20 << 20, MaxTotalInputBytes: 256 << 20, ProviderPath: "/v1/images/edits"}}}}
	require.NoError(t, PrepareZTAPIImageSaleQuote(c, info))
	dispatch := &relaycommon.ZTAPIManagedImageDispatch{Body: wire, ContentType: writer.FormDataContentType(), ProviderPath: "/v1/images/edits"}
	require.NoError(t, VerifyZTAPIImageSaleDispatch(info, dispatch))
	modified := *dispatch
	modified.Body = bytes.Replace(wire, []byte(prompt), []byte(prompt+" changed"), 1)
	require.Error(t, VerifyZTAPIImageSaleDispatch(info, &modified))
	frozen, _ := info.GetZTAPIImageSaleQuote()
	require.EqualValues(t, 4, frozen.ReferenceUnits)
	require.Greater(t, frozen.PromptTokens, int64(2000))
	want, err := types.QuoteZTAPIImageSale(info.ZTAPIPublicationSnapshot.ImageSaleContractJSON, "1K", int64(CountTextToken(prompt, "gpt-image-2")), 4, 2)
	require.NoError(t, err)
	require.Equal(t, want, *frozen)
	frozen.TotalUSD = "0.01"
	require.NoError(t, VerifyZTAPIImageSaleQuote(c, info), "returned quote must be a defensive copy")
	info.Request.(*dto.ImageRequest).Prompt += " changed"
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info))
	info.Request.(*dto.ImageRequest).Prompt = prompt
	files := c.Request.MultipartForm.File["image"]
	c.Request.MultipartForm.File["image"] = files[:1]
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info))
	require.NoError(t, VerifyZTAPIImageSaleDispatch(info, dispatch), "the immutable outbound bytes stay bound even if the source form changes")
	c.Request.MultipartForm.File["image"] = files
	files[0].Size--
	require.Error(t, VerifyZTAPIImageSaleQuote(c, info), "a declared prefix must not hide trailing file bytes")
	files[0].Size++
	part, err := c.Request.MultipartForm.File["image"][0].Open()
	require.NoError(t, err)
	defer part.Close()
	_, _, err = image.DecodeConfig(part)
	require.NoError(t, err, "pricing inspection must not consume the forwarded file")
}

func TestZTAPIImageSaleReservationRejectsFrozenPriceTampering(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Model(user).Update("quota", 200000).Error)
	input := flatImageReservationFixture(t)
	input.UserID, input.TokenID = user.Id, token.Id
	input.ImageSaleQuote.TotalUSD = "0.01"
	_, err := BeginZTAPIMediaReservation(input)
	require.Error(t, err)
	input = flatImageReservationFixture(t)
	input.UserID, input.TokenID = user.Id, token.Id
	row, err := BeginZTAPIMediaReservation(input)
	require.NoError(t, err)
	tampered := strings.Replace(row.PriceSnapshotJSON, `"total_usd":"0.3"`, `"total_usd":"0.01"`, 1)
	require.NotEqual(t, row.PriceSnapshotJSON, tampered)
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Where("id = ?", row.ID).UpdateColumn("price_snapshot_json", tampered).Error)
	_, _, err = loadZTAPIMediaSettlement(row.OperationID)
	require.Error(t, err)
}

func flatImageReservationFixture(t *testing.T) ZTAPIMediaReservationInput {
	input := ztapiImageReservationFixture(t, 150000)
	var snapshot relaycommon.ZTAPIPublicationSnapshot
	require.NoError(t, common.UnmarshalJsonStr(input.PriceSnapshotJSON, &snapshot))
	snapshot.ImageSaleContractJSON = types.ZTAPIImageSaleContract20261109JSON()
	raw, err := common.Marshal(snapshot)
	require.NoError(t, err)
	input.PriceSnapshotJSON = string(raw)
	protocol, _, err := types.ParseZTAPIImageProtocolContract(input.ProtocolContractJSON)
	require.NoError(t, err)
	protocol.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{AspectRatio: "1:1", Resolution: "1K", Size: "1024x1024", Quality: "standard"}}
	protocol, input.ProtocolContractJSON, err = types.SealZTAPIImageProtocolContract(protocol)
	require.NoError(t, err)
	input.ProtocolEvidenceHash = protocol.EvidenceHash
	quote, err := types.QuoteZTAPIImageSale(snapshot.ImageSaleContractJSON, "1K", 2000, 0, 1)
	require.NoError(t, err)
	input.ImageSaleQuote = &quote
	input.ImageSaleInputSHA256 = strings.Repeat("a", 64)
	input.MaximumDimensions = map[string]string{"images": "1"}
	return input
}

func TestZTAPIImageSaleFixedSettlementAndReplay(t *testing.T) {
	for _, scenario := range []string{"high_tokens", "missing_tokens", "large_usage"} {
		t.Run(scenario, func(t *testing.T) {
			db, user, token := setupServiceTokenQuotaTest(t)
			migrateZTAPIMediaBillingTestTables(t)
			require.NoError(t, db.Model(user).Update("quota", 150000).Error)
			require.NoError(t, db.Create(&model.Channel{Id: 35, Name: "flat-image", Status: common.ChannelStatusEnabled, Type: 1}).Error)
			input := flatImageReservationFixture(t)
			input.UserID, input.TokenID = user.Id, token.Id
			row, err := BeginZTAPIMediaReservation(input)
			require.NoError(t, err)
			require.EqualValues(t, 150000, row.ReservedQuota)
			attempt, err := model.BeginZTAPIRequestAttempt(row.OperationID, 35, "credential-v1", "/v1/images/generations")
			require.NoError(t, err)
			require.NoError(t, model.RecordZTAPIRequestAttemptResponse(row.OperationID, attempt.Attempt, 35, 200, "upstream-request-1"))
			evidence := trustedZTAPIImageEvidence(t, frozenMediaPriceContract(t, row), `{"input_tokens":100,"output_tokens":100,"total_tokens":200}`)
			if scenario != "high_tokens" {
				evidence = relaycommon.ZTAPIMediaUsageEvidence{UpstreamRequestID: "upstream-request-1", ResultCount: 1, ResultAvailable: true, Pending: true, Reason: "usage is missing"}
			}
			if scenario == "large_usage" {
				evidence.RawUsageJSON = `{"large":"` + strings.Repeat("x", 65536) + `"}`
			}
			settled, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
			require.NoError(t, err)
			require.EqualValues(t, 150000, settled.ChargedQuota)
			require.Contains(t, settled.ChargeDimensionsJSON, `"dimension":"images"`)
			if scenario == "large_usage" {
				require.Contains(t, settled.UsageJSON, `"raw_usage_sha256"`)
				require.Less(t, len(settled.UsageJSON), 65536)
			}
			again, err := FinalizeZTAPIImageSettlement(row.OperationID, evidence, attempt.Attempt)
			require.NoError(t, err)
			require.Equal(t, settled.ChargedQuota, again.ChargedQuota)
			var current model.User
			require.NoError(t, db.First(&current, user.Id).Error)
			require.Equal(t, 0, current.Quota)
		})
	}
}

func TestZTAPIImageSaleCannotBypassNativeProtocolPriceMismatch(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Model(user).Update("quota", 200000).Error)
	input := flatImageReservationFixture(t)
	input.UserID, input.TokenID = user.Id, token.Id
	_, _, protocol, _ := gptImageThreeDimensionContracts(t)
	protocol.Capabilities.RenderOptions = []types.ZTAPIImageRenderOption{{AspectRatio: "1:1", Resolution: "1K", Size: "1024x1024", Quality: "low"}}
	var err error
	protocol, input.ProtocolContractJSON, err = types.SealZTAPIImageProtocolContract(protocol)
	require.NoError(t, err)
	input.ProtocolEvidenceHash = protocol.EvidenceHash
	input.SelectorJSON = `{"modality":"image","n":1,"quality":"low","response_format":"b64_json","size":"1024x1024"}`
	_, err = BeginZTAPIMediaReservation(input)
	require.Error(t, err, "fixed retail must not hide incompatible native price/usage contracts")
}

func TestZTAPIImageSaleCannotReserveBeyondBalance(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	migrateZTAPIMediaBillingTestTables(t)
	require.NoError(t, db.Model(user).Update("quota", 149999).Error)
	input := flatImageReservationFixture(t)
	input.UserID, input.TokenID = user.Id, token.Id
	_, err := BeginZTAPIMediaReservation(input)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestZTAPIImageSaleVerifierRetainsProductionSizeFix(t *testing.T) {
	options := ztapiGPTImage2RenderOptions()
	for _, option := range options {
		if option.Resolution == "1K" && option.AspectRatio == "16:9" {
			require.Equal(t, "1280x720", option.Size)
		}
		if option.Resolution == "1K" && option.AspectRatio == "9:16" {
			require.Equal(t, "720x1280", option.Size)
		}
	}
	require.Equal(t, "16384", ztapiGPTImage2MaximumImageOutput("1024x1024", 1))
}
