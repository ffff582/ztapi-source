package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeStabilityAdaptorFailurePersistsPendingMetering(t *testing.T) {
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		UserId: f.user.Id, TokenId: f.token.Id, RequestId: "local-claude-interrupted",
		OriginModelName: f.publicName, RelayFormat: types.RelayFormatClaude, IsStream: true, DisablePing: true,
		ChannelMeta:              &relaycommon.ChannelMeta{ChannelId: 1, ApiKey: "local-e2e-upstream-key", UpstreamModelName: f.sourceModel},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "text"},
	}
	require.Nil(t, service.PreConsumeBilling(c, 1000, info))
	require.NoError(t, service.BeginZTAPIBillingAttempt(info, "/v1/messages"))
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"type":"message_start","message":{"id":"msg_local","model":"claude-sonnet-5","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}

`))}
	require.NoError(t, service.ObserveZTAPIBillingResponse(info, response))
	_, apiErr := (&claude.Adaptor{}).DoResponse(c, response, info)
	require.NotNil(t, apiErr)
	// The caller discards the returned usage; pending must read the retained info instead.
	info.LastError = apiErr
	held := f.balances(t)
	require.NoError(t, service.FinishZTAPIBilling(c, info))
	require.Equal(t, held, f.balances(t), "error handling must retain the existing hold without another money mutation")
	var row model.ZTAPIRequestSettlement
	require.NoError(t, f.db.Where("request_id = ?", info.RequestId).First(&row).Error)
	require.Equal(t, model.ZTAPISettlementPending, row.Status)
	require.Zero(t, row.ChargedQuota)
	var stored struct {
		Evidence *types.ZTAPIMeteringEvidence `json:"ztapi_metering_evidence"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &stored))
	require.NotNil(t, stored.Evidence, "pending evidence must not depend on the discarded usage return value")
	require.Equal(t, string(types.RelayFormatClaude), stored.Evidence.UpstreamProtocol)
	require.Equal(t, 2, stored.Evidence.UsageObservations)
	require.False(t, stored.Evidence.UsageIntegrityComplete)
	var receipt map[string]int
	require.NoError(t, common.Unmarshal(stored.Evidence.RawUsage, &receipt))
	require.Equal(t, map[string]int{"input_tokens": 2, "output_tokens": 7, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 4}, receipt)
	require.NoError(t, service.FinishZTAPIBilling(c, info))
	require.Equal(t, held, f.balances(t), "repeated cleanup must not release or charge the hold")
}
