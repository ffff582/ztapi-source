package common

import (
	"net/http/httptest"
	"testing"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoChannelRetryResetsOnlyAttemptProtocolAndOutputBudget(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	basecommon.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeOpenAI)
	snapshot := &ZTAPIPublicationSnapshot{PublicName: "zt-claude-sonnet-fixture", Version: 7}
	info := &RelayInfo{
		RelayFormat:                types.RelayFormatClaude,
		RequestConversionChain:     []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAIResponses},
		FinalRequestRelayFormat:    types.RelayFormatOpenAIResponses,
		ZTAPIReasoningOutputBudget: 1024,
		ZTAPIPublicationSnapshot:   snapshot,
		FinalPreConsumedQuota:      6120,
	}
	info.InitChannelMeta(ctx)
	require.Empty(t, info.FinalRequestRelayFormat)
	require.Equal(t, []types.RelayFormat{types.RelayFormatClaude}, info.RequestConversionChain)
	require.Zero(t, info.ZTAPIReasoningOutputBudget)
	require.Same(t, snapshot, info.ZTAPIPublicationSnapshot)
	require.Equal(t, 6120, info.FinalPreConsumedQuota, "a retry must not forget already reserved funds")
	info.AppendRequestConversion(types.RelayFormatOpenAI)
	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAI), info.GetFinalRequestRelayFormat())
}

func TestZTAPIManagedImageDispatchPreservesMultipartContentType(t *testing.T) {
	info := &RelayInfo{}
	dispatch := &ZTAPIManagedImageDispatch{
		Body: []byte("multipart-body"), ProviderPath: "/v1/images/edits",
		WireProtocol: "openai_images_edit_multipart", ContentType: "multipart/form-data; boundary=upstream-boundary",
	}
	require.True(t, info.SetZTAPIManagedImageDispatch(dispatch))
	got := info.GetZTAPIManagedImageDispatch()
	require.Equal(t, dispatch.ContentType, got.ContentType)
	dispatch.ContentType = "mutated"
	require.Equal(t, "multipart/form-data; boundary=upstream-boundary", info.GetZTAPIManagedImageDispatch().ContentType)
}
