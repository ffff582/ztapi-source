package helper

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIReasoningAcceptanceLunaToolTransportIsScoped(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ApiType: constant.APITypeOpenAI}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.6-luna"}}
	require.True(t, ZTAPIManagedToolsRequireResponses(info, true))
	require.False(t, ZTAPIManagedToolsRequireResponses(info, false))
	require.False(t, ZTAPIManagedToolsRequireResponses(nil, true))
	require.False(t, ZTAPIManagedToolsRequireResponses(&relaycommon.RelayInfo{}, true))
	for _, source := range []string{"gpt-6.1-sol", "glm-5", "kimi-k2.6", "claude-opus-5", "gpt-5.6-luna-other"} {
		info.ZTAPIPublicationSnapshot.SourceModel = source
		require.False(t, ZTAPIManagedToolsRequireResponses(info, true), source)
	}
}
