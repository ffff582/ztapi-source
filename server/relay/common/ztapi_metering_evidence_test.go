package common

import (
	"net/http/httptest"
	"strings"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func meteringTestInfo() *RelayInfo {
	return &RelayInfo{RelayFormat: types.RelayFormatClaude,
		ZTAPIPublicationSnapshot: &ZTAPIPublicationSnapshot{Modality: "text"}}
}

func TestZTAPIMeteringCapturesCountsWithoutCustomerContent(t *testing.T) {
	body := `{"model":"test","max_tokens":64,"system":"PRIVATE_SYSTEM","messages":[{"role":"user","content":"PRIVATE_CODE"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var request dto.ClaudeRequest
	require.NoError(t, basecommon.UnmarshalJsonStr(body, &request))
	info := meteringTestInfo()
	info.Request = &request
	info.SetEstimatePromptTokens(123)
	CaptureZTAPIRequestMetering(c, info, request.GetTokenCountMeta())
	defer basecommon.CleanupBodyStorage(c)
	require.NotNil(t, info.ZTAPIMetering)
	require.EqualValues(t, len(body), *info.ZTAPIMetering.EntryBodyBytes)
	require.Equal(t, 1, info.ZTAPIMetering.Messages)
	require.Equal(t, 1, info.ZTAPIMetering.Tools)
	require.Positive(t, info.ZTAPIMetering.ToolDefinitionBytes)
	require.Equal(t, 64, info.ZTAPIMetering.MaxOutputTokens)
	require.Equal(t, 123, info.ZTAPIMetering.EstimatedInputTokens)
	require.Positive(t, info.ZTAPIMetering.CombinedTextBytes)
	encoded, err := basecommon.Marshal(info.ZTAPIMetering)
	require.NoError(t, err)
	for _, secret := range []string{"PRIVATE_SYSTEM", "PRIVATE_CODE", "read_file", "input_schema"} {
		require.NotContains(t, string(encoded), secret)
	}
}

func TestZTAPIMeteringRetainsOnlyNumericUsageAllowlist(t *testing.T) {
	info := meteringTestInfo()
	CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"usage":{"prompt_tokens":377286,"completion_tokens":1195,"total_tokens":378481,"prompt_tokens_details":{"cached_tokens":0,"api_key":"PRIVATE_KEY","unknown_count":12},"usage_source":"local_estimate","provider_note":"PRIVATE_SECRET","unknown_count":45},"choices":[{"message":{"content":"PRIVATE_REPLY"}}]}`), "stream")
	require.NotNil(t, info.ZTAPIMetering)
	require.Equal(t, "upstream_reported", info.ZTAPIMetering.UsageSource)
	require.Equal(t, "stream", info.ZTAPIMetering.UsageLocation)
	require.Len(t, info.ZTAPIMetering.RawUsageSHA256, 64)
	encoded, err := basecommon.Marshal(info.ZTAPIMetering)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"prompt_tokens":377286`)
	require.Contains(t, string(encoded), `"cached_tokens":0`)
	for _, forbidden := range []string{"PRIVATE_", "provider_note", "api_key", "unknown_count", "local_estimate", "choices"} {
		require.NotContains(t, string(encoded), forbidden)
	}

	CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"usage":{"prompt_tokens":99,"completion_tokens":1}}`), "stream")
	require.Equal(t, 2, info.ZTAPIMetering.UsageObservations)
	require.JSONEq(t, `{"prompt_tokens":99,"completion_tokens":1}`, string(info.ZTAPIMetering.RawUsage))
	before := info.ZTAPIMetering.RawUsageSHA256
	CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"choices":[],"usage":null}`), "stream")
	require.Equal(t, before, info.ZTAPIMetering.RawUsageSHA256)
}

func TestZTAPIMeteringResetsProviderEvidenceAtNextDispatch(t *testing.T) {
	info := meteringTestInfo()
	CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"usage":{"prompt_tokens":10}}`), "stream")
	CaptureZTAPIOutboundRequestMetering(info, 2048)
	require.EqualValues(t, 2048, *info.ZTAPIMetering.OutboundBodyBytes)
	require.Empty(t, info.ZTAPIMetering.RawUsage)
	require.Empty(t, info.ZTAPIMetering.UsageSource)
	require.Zero(t, info.ZTAPIMetering.UsageObservations)
	CaptureZTAPIOutboundRequestMetering(info, -1)
	require.Nil(t, info.ZTAPIMetering.OutboundBodyBytes, "unknown payload size is not an empty payload")
}

func TestZTAPIMeteringSkipsUnmanagedAndNonNumericPayloads(t *testing.T) {
	legacy := &RelayInfo{}
	CaptureZTAPIUpstreamUsageEvidence(legacy, []byte(`{"usage":{"prompt_tokens":10}}`), "stream")
	require.Nil(t, legacy.ZTAPIMetering)
	info := meteringTestInfo()
	CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"usage":{"prompt_tokens":"PRIVATE_KEY","prompt_tokens_details":{"cached_tokens":"PRIVATE_CODE"}}}`), "stream")
	require.Nil(t, info.ZTAPIMetering)
}
