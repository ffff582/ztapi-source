package service

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func claudeParallelStreamChunk(t *testing.T, calls []dto.ToolCallResponse, finish *string, usage *dto.Usage) *dto.ChatCompletionsStreamResponse {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"id": "chat_local", "model": "claude-local", "usage": usage,
		"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "delta": map[string]any{"tool_calls": calls}}},
	})
	require.NoError(t, err)
	var chunk dto.ChatCompletionsStreamResponse
	require.NoError(t, common.Unmarshal(raw, &chunk))
	return &chunk
}

func TestClaudeParallelStreamFirstChunk(t *testing.T) {
	for _, indices := range []struct {
		name string
		ids  []*int
	}{
		{"indexed", []*int{common.GetPointer(0), common.GetPointer(1)}},
		{"implicit", []*int{nil, nil}},
		{"reversed", []*int{common.GetPointer(1), common.GetPointer(0)}},
	} {
		for _, finishFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/finish_first_%t", indices.name, finishFirst), func(t *testing.T) {
				info := &relaycommon.RelayInfo{SendResponseCount: 1,
					ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}}
				calls := make([]dto.ToolCallResponse, 2)
				expectedIDs, expectedArguments := map[int]string{}, map[int]string{}
				for i := range calls {
					arguments := `{"value":`
					if finishFirst {
						arguments = fmt.Sprintf(`{"value":%d}`, i+1)
					}
					calls[i] = dto.ToolCallResponse{Index: indices.ids[i], ID: fmt.Sprintf("call_%d", i), Type: "function",
						Function: dto.FunctionResponse{Name: "lookup", Arguments: arguments}}
					index := i
					if indices.ids[i] != nil {
						index = *indices.ids[i]
					}
					expectedIDs[index] = calls[i].ID
					expectedArguments[index] = fmt.Sprintf(`{"value":%d}`, i+1)
				}
				usage := &dto.Usage{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24}
				var finish *string
				var firstUsage *dto.Usage
				if finishFirst {
					finish, firstUsage = common.GetPointer("tool_calls"), usage
				}
				first := claudeParallelStreamChunk(t, calls, finish, firstUsage)
				original, err := common.Marshal(first)
				require.NoError(t, err)
				frames := StreamResponseOpenAI2Claude(first, info)
				require.NotEmpty(t, frames)
				require.Equal(t, "message_start", frames[0].Type)
				after, err := common.Marshal(first)
				require.NoError(t, err)
				require.Equal(t, string(original), string(after), "conversion must not mutate the provider chunk")
				if !finishFirst {
					continuation := make([]dto.ToolCallResponse, 2)
					for i := range continuation {
						continuation[i] = dto.ToolCallResponse{Index: indices.ids[i], Function: dto.FunctionResponse{Arguments: fmt.Sprintf("%d}", i+1)}}
					}
					info.SendResponseCount++
					frames = append(frames, StreamResponseOpenAI2Claude(claudeParallelStreamChunk(t, continuation, nil, nil), info)...)
					info.SendResponseCount++
					frames = append(frames, StreamResponseOpenAI2Claude(claudeParallelStreamChunk(t, nil, common.GetPointer("tool_calls"), usage), info)...)
				}
				starts, arguments, stops := map[int]string{}, map[int]string{}, map[int]int{}
				var startOrder []int
				for _, frame := range frames {
					switch frame.Type {
					case "content_block_start":
						require.NotNil(t, frame.Index)
						require.NotContains(t, starts, *frame.Index, "each tool must start once")
						starts[*frame.Index] = frame.ContentBlock.Id
						startOrder = append(startOrder, *frame.Index)
					case "content_block_delta":
						require.NotNil(t, frame.Index)
						require.Contains(t, starts, *frame.Index, "arguments need a matching tool start")
						require.Zero(t, stops[*frame.Index])
						arguments[*frame.Index] += *frame.Delta.PartialJson
					case "content_block_stop":
						require.NotNil(t, frame.Index)
						require.Contains(t, starts, *frame.Index)
						stops[*frame.Index]++
					case "message_delta":
						require.Equal(t, map[int]int{0: 1, 1: 1}, stops, "both tools must close before the terminal delta")
						require.Equal(t, "tool_use", *frame.Delta.StopReason)
						require.Equal(t, 4, frame.Usage.OutputTokens)
					}
				}
				require.Equal(t, []int{0, 1}, startOrder, "SDK snapshots append starts before applying indexed deltas")
				require.Equal(t, expectedIDs, starts, "first-frame parallel tool identities must all survive")
				require.Equal(t, expectedArguments, arguments)
				require.Equal(t, map[int]int{0: 1, 1: 1}, stops)
				require.Equal(t, "message_stop", frames[len(frames)-1].Type)
				require.True(t, info.ClaudeConvertInfo.Done)
				require.Empty(t, StreamResponseOpenAI2Claude(first, info), "a completed turn must not replay tools")
			})
		}
	}
}

func TestClaudeParallelStreamFirstChunkThenText(t *testing.T) {
	info := &relaycommon.RelayInfo{SendResponseCount: 1,
		ClaudeConvertInfo: &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}}
	first := claudeParallelStreamChunk(t, []dto.ToolCallResponse{
		{Index: common.GetPointer(0), ID: "call_0", Function: dto.FunctionResponse{Name: "lookup", Arguments: `{}`}},
		{Index: common.GetPointer(1), ID: "call_1", Function: dto.FunctionResponse{Name: "lookup", Arguments: `{}`}},
	}, nil, nil)
	StreamResponseOpenAI2Claude(first, info)
	info.SendResponseCount++
	var text dto.ChatCompletionsStreamResponse
	require.NoError(t, common.UnmarshalJsonStr(`{"choices":[{"index":0,"delta":{"content":"done"}}]}`, &text))
	frames := StreamResponseOpenAI2Claude(&text, info)
	require.Len(t, frames, 4, "close both tool blocks before starting text")
	for i, want := range []struct {
		typeName string
		index    int
	}{{"content_block_stop", 0}, {"content_block_stop", 1}, {"content_block_start", 2}, {"content_block_delta", 2}} {
		require.Equal(t, want.typeName, frames[i].Type)
		require.NotNil(t, frames[i].Index)
		require.Equal(t, want.index, *frames[i].Index)
	}
	require.Equal(t, "done", *frames[3].Delta.Text)
}
