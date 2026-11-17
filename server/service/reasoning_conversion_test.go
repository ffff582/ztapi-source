package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestZTAPIClaudeToOpenAIForwardsReasoningEffort(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta:              &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.6-sol"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.6-sol"},
	}
	request, err := ClaudeToOpenAIRequest(dto.ClaudeRequest{
		Model:        "gpt-5.6-sol",
		OutputConfig: []byte(`{"effort":"max"}`),
	}, info)
	require.NoError(t, err)
	require.Equal(t, "max", request.ReasoningEffort)
}

func TestZTAPIGeminiToOpenAIForwardsReasoningEffort(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ChannelMeta:              &relaycommon.ChannelMeta{UpstreamModelName: "gpt-5.6-sol"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{SourceModel: "gpt-5.6-sol"},
	}
	request, err := GeminiToOpenAIRequest(&dto.GeminiChatRequest{
		GenerationConfig: dto.GeminiChatGenerationConfig{
			ThinkingConfig: &dto.GeminiThinkingConfig{ThinkingLevel: "max"},
		},
	}, info)
	require.NoError(t, err)
	require.Equal(t, "max", request.ReasoningEffort)
}
