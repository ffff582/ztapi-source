package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/hostedsearch"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func parallelTurnInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "zt-claude-opus-5", RelayMode: relayconstant.RelayModeChatCompletions,
		RelayFormat:              types.RelayFormatOpenAIResponses,
		ChannelMeta:              &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenAI, UpstreamModelName: "claude-opus-5"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicName: "zt-claude-opus-5", Modality: "text"},
	}
}

func parallelTurnConvert(t *testing.T, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (*dto.GeneralOpenAIRequest, error) {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	converted, err := (&Adaptor{ChannelType: info.ChannelType}).ConvertOpenAIRequest(c, info, request)
	if err != nil {
		return nil, err
	}
	return converted.(*dto.GeneralOpenAIRequest), nil
}

func parallelTurnRequest(t *testing.T) *dto.GeneralOpenAIRequest {
	t.Helper()
	var request dto.GeneralOpenAIRequest
	require.NoError(t, common.UnmarshalJsonStr(`{"model":"claude-opus-5","parallel_tool_calls":true,"tool_choice":"auto","cache_control":{"type":"ephemeral"},"tools":[{"type":"function","function":{"name":"private_search","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"question"},{"role":"assistant","content":"checking","tool_calls":[{"id":"s","type":"function","function":{"name":"private_search","arguments":"{\"action\":\"search\"}"}},{"id":"o","type":"function","function":{"name":"private_search","arguments":"{\"action\":\"open_page\"}"}},{"id":"f","type":"function","function":{"name":"private_search","arguments":"{\"action\":\"find_in_page\"}"}}]},{"role":"tool","tool_call_id":"s","content":"search result"},{"role":"tool","tool_call_id":"o","content":"page result"},{"role":"tool","tool_call_id":"f","content":"find result"}]}`, &request))
	return &request
}

// This deliberately models the suspected vendor bridge, not captured vendor wire:
// each Chat tool message becomes its own native user message, without merging.
func parallelTurnNativePairing(messages []dto.Message) error {
	var native []dto.ClaudeMessage
	for _, message := range messages {
		if message.Role == "tool" {
			native = append(native, dto.ClaudeMessage{Role: "user", Content: []dto.ClaudeMediaMessage{{Type: "tool_result", ToolUseId: message.ToolCallId, Content: message.Content}}})
			continue
		}
		if len(message.ToolCalls) == 0 {
			native = append(native, dto.ClaudeMessage{Role: message.Role, Content: message.Content})
			continue
		}
		var calls []dto.ToolCallRequest
		if err := common.Unmarshal(message.ToolCalls, &calls); err != nil {
			return err
		}
		var blocks []dto.ClaudeMediaMessage
		if text, ok := message.Content.(string); ok && text != "" {
			blocks = append(blocks, dto.ClaudeMediaMessage{Type: "text", Text: &text})
		}
		for _, call := range calls {
			blocks = append(blocks, dto.ClaudeMediaMessage{Type: "tool_use", Id: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments), CacheControl: call.CacheControl})
		}
		native = append(native, dto.ClaudeMessage{Role: message.Role, Content: blocks})
	}
	wire, err := common.Marshal(native)
	if err != nil {
		return err
	}
	var decoded []dto.ClaudeMessage
	if err := common.Unmarshal(wire, &decoded); err != nil {
		return err
	}
	return parallelTurnValidateNativePairing(decoded)
}

func parallelTurnValidateNativePairing(messages []dto.ClaudeMessage) error {
	pending := map[string]bool{}
	for _, message := range messages {
		var blocks []dto.ClaudeMediaMessage
		if !message.IsStringContent() && message.Content != nil {
			var err error
			blocks, err = message.ParseContent()
			if err != nil {
				return err
			}
		}
		if len(pending) > 0 && message.Role != "user" {
			return fmt.Errorf("TOOL_USE_RESULT_MISMATCH: immediately next native message must be user")
		}
		for _, block := range blocks {
			if block.Type == "tool_result" {
				if message.Role != "user" || !pending[block.ToolUseId] {
					return fmt.Errorf("orphan or duplicate native tool_result %q", block.ToolUseId)
				}
				delete(pending, block.ToolUseId)
			}
		}
		if len(pending) > 0 {
			return fmt.Errorf("TOOL_USE_RESULT_MISMATCH: immediately next native user is missing tool_result %v", pending)
		}
		for _, block := range blocks {
			if block.Type == "tool_use" {
				if message.Role != "assistant" || block.Id == "" || pending[block.Id] {
					return fmt.Errorf("invalid native tool_use %q", block.Id)
				}
				pending[block.Id] = true
			}
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("missing native tool_result %v", pending)
	}
	return nil
}

func TestZTAPIParallelToolTurnsNativePairingAcceptsGroupedResults(t *testing.T) {
	var calls, results []dto.ClaudeMediaMessage
	for _, id := range []string{"s", "o", "f"} {
		calls = append(calls, dto.ClaudeMediaMessage{Type: "tool_use", Id: id, Name: "same_function"})
		results = append(results, dto.ClaudeMediaMessage{Type: "tool_result", ToolUseId: id, Content: "result " + id})
	}
	grouped := []dto.ClaudeMessage{{Role: "assistant", Content: calls}, {Role: "user", Content: results}}
	require.NoError(t, parallelTurnValidateNativePairing(grouped), "Anthropic permits parallel calls when all their results occupy the immediately next user message")
	grouped[1].Content = results[:1]
	require.ErrorContains(t, parallelTurnValidateNativePairing(grouped), "TOOL_USE_RESULT_MISMATCH")
	grouped[1].Content = append(results, results[0])
	require.ErrorContains(t, parallelTurnValidateNativePairing(grouped), "duplicate")
}

func TestZTAPIParallelToolTurnsNormalRequestsRemainImmutable(t *testing.T) {
	for _, raw := range []string{
		`{"model":"claude-opus-5","messages":[{"role":"system","content":"scope"},{"role":"user","content":"hello"},{"role":"assistant","content":"reply"}],"parallel_tool_calls":true}`,
		`{"model":"claude-opus-5","messages":[{"role":"assistant","content":"checking","tool_calls":[{"id":"a","type":"function","function":{"name":"local","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"result"}],"parallel_tool_calls":false}`,
	} {
		var request dto.GeneralOpenAIRequest
		require.NoError(t, common.UnmarshalJsonStr(raw, &request))
		before, err := common.Marshal(request)
		require.NoError(t, err)
		converted, err := parallelTurnConvert(t, parallelTurnInfo(), &request)
		require.NoError(t, err)
		after, err := common.Marshal(converted)
		require.NoError(t, err)
		require.Equal(t, string(before), string(after))
		unchanged, err := common.Marshal(request)
		require.NoError(t, err)
		require.Equal(t, string(before), string(unchanged))
	}
}

func TestZTAPIParallelToolTurnsResponsesWirePairsAllThreeResults(t *testing.T) {
	request := parallelTurnRequest(t)
	before, err := common.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, openaicompat.ValidateChatToolHistory(request.Messages))
	require.ErrorContains(t, parallelTurnNativePairing(request.Messages), "TOOL_USE_RESULT_MISMATCH")
	converted, err := parallelTurnConvert(t, parallelTurnInfo(), request)
	require.NoError(t, err)
	wire, err := common.Marshal(converted)
	require.NoError(t, err)
	var decoded dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal(wire, &decoded))
	require.NoError(t, parallelTurnNativePairing(decoded.Messages))
	require.NoError(t, openaicompat.ValidateChatToolHistory(decoded.Messages))
	require.Len(t, decoded.Messages, 7)
	for i, id := range []string{"s", "o", "f"} {
		require.Equal(t, id, decoded.Messages[2+2*i].ToolCallId)
		require.Equal(t, request.Messages[2+i], decoded.Messages[2+2*i])
	}
	// Compare every non-history request field, including parallel execution and caches.
	decoded.Messages = request.Messages
	require.Equal(t, *request, decoded)
	after, err := common.Marshal(request)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "adaptor must not modify the input request")
}

func TestZTAPIParallelToolTurnsScopeLeavesNativeAndOtherRoutesUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*relaycommon.RelayInfo)
	}{
		{"native_chat", func(i *relaycommon.RelayInfo) { i.RelayFormat = types.RelayFormatOpenAI }},
		{"native_claude", func(i *relaycommon.RelayInfo) { i.RelayFormat = types.RelayFormatClaude }},
		{"gemini_ingress", func(i *relaycommon.RelayInfo) { i.RelayFormat = types.RelayFormatGemini }},
		{"unmanaged", func(i *relaycommon.RelayInfo) { i.ZTAPIPublicationSnapshot = nil }},
		{"other_public_model", func(i *relaycommon.RelayInfo) {
			i.OriginModelName = "zt-kimi-k2.6"
			i.ZTAPIPublicationSnapshot.PublicName = i.OriginModelName
		}},
		{"private_alias", func(i *relaycommon.RelayInfo) {
			i.OriginModelName = "claude-opus-5"
			i.ZTAPIPublicationSnapshot.PublicName = i.OriginModelName
		}},
		{"snapshot_mismatch", func(i *relaycommon.RelayInfo) { i.ZTAPIPublicationSnapshot.PublicName = "zt-kimi-k2.6" }},
		{"azure", func(i *relaycommon.RelayInfo) { i.ChannelType = constant.ChannelTypeAzure }},
		{"openrouter", func(i *relaycommon.RelayInfo) { i.ChannelType = constant.ChannelTypeOpenRouter }},
		{"native_responses_route", func(i *relaycommon.RelayInfo) { i.RelayMode = relayconstant.RelayModeResponses }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, request := parallelTurnInfo(), parallelTurnRequest(t)
			tc.edit(info)
			before, err := common.Marshal(request.Messages)
			require.NoError(t, err)
			converted, err := parallelTurnConvert(t, info, request)
			require.NoError(t, err)
			after, err := common.Marshal(converted.Messages)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestZTAPIParallelToolTurnsRejectsInvalidHistoryBeforeReordering(t *testing.T) {
	request := parallelTurnRequest(t)
	request.Messages = append(request.Messages[:3], append([]dto.Message{{Role: "user", Content: "interrupt"}}, request.Messages[3:]...)...)
	before, err := common.Marshal(request)
	require.NoError(t, err)
	converted, err := parallelTurnConvert(t, parallelTurnInfo(), request)
	require.Error(t, err)
	require.Nil(t, converted)
	after, marshalErr := common.Marshal(request)
	require.NoError(t, marshalErr)
	require.Equal(t, string(before), string(after))
}

type parallelTurnProvider struct{ searches, fetches int }

func (p *parallelTurnProvider) Search(context.Context, string, int) (hostedsearch.Result, error) {
	p.searches++
	return hostedsearch.Result{Outcome: hostedsearch.OutcomeSucceeded, HTTPStatus: 200, Sources: []hostedsearch.Source{{Title: "Fixture", URL: "https://example.com/docs", Summary: "offline search evidence"}}}, nil
}

func (p *parallelTurnProvider) Fetch(context.Context, string) (hostedsearch.Result, error) {
	p.fetches++
	return hostedsearch.Result{Outcome: hostedsearch.OutcomeSucceeded, HTTPStatus: 200, Title: "Fixture", Content: "offline searchable page evidence"}, nil
}

func TestZTAPIParallelToolTurnsActualEngineThreeSameFunctionCalls(t *testing.T) {
	p, err := hostedsearch.PrepareRequest([]byte(`{"model":"zt-claude-opus-5","input":"Find official docs","tools":[{"type":"web_search"}],"parallel_tool_calls":true,"max_output_tokens":1024}`), "ztapi_hosted_web_12345678")
	require.NoError(t, err)
	provider, rounds := &parallelTurnProvider{}, 0
	var continuation []byte
	result, err := hostedsearch.Run(t.Context(), p, hostedsearch.Config{
		Provider: provider,
		Dispatch: func(ctx context.Context, _ hostedsearch.ToolRequest, call func(context.Context) (hostedsearch.Result, error)) (hostedsearch.Result, error) {
			return call(ctx)
		},
		Model: func(_ context.Context, body []byte) ([]byte, error) {
			rounds++
			var output []map[string]any
			if rounds == 1 {
				for i, args := range []string{`{"action":"search","query":"docs"}`, `{"action":"open_page","url":"https://example.com/docs"}`, `{"action":"find_in_page","url":"https://example.com/docs","pattern":"searchable"}`} {
					output = append(output, map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", i), "call_id": fmt.Sprintf("call_%d", i), "name": p.PrivateTool, "arguments": args, "status": "completed"})
				}
			} else {
				require.Equal(t, 2, rounds)
				continuation = append([]byte(nil), body...)
				var request dto.OpenAIResponsesRequest
				require.NoError(t, common.Unmarshal(body, &request))
				conversion, err := openaicompat.ResponsesRequestToChatCompletionsRequest(&request)
				require.NoError(t, err)
				chat := conversion.Request
				require.Len(t, chat.Messages, 5, "the real engine and Responses bridge retain a parallel assistant turn plus three results")
				require.NoError(t, openaicompat.ValidateChatToolHistory(chat.Messages))
				require.ErrorContains(t, parallelTurnNativePairing(chat.Messages), "TOOL_USE_RESULT_MISMATCH")
				chat.Model = "claude-opus-5"
				original, err := common.Marshal(chat)
				require.NoError(t, err)
				converted, err := parallelTurnConvert(t, parallelTurnInfo(), chat)
				require.NoError(t, err)
				wire, err := common.Marshal(converted)
				require.NoError(t, err)
				var decoded dto.GeneralOpenAIRequest
				require.NoError(t, common.Unmarshal(wire, &decoded))
				require.NoError(t, parallelTurnNativePairing(decoded.Messages))
				for i := 0; i < 3; i++ {
					require.Equal(t, chat.Messages[i+2], decoded.Messages[2+2*i])
				}
				after, err := common.Marshal(chat)
				require.NoError(t, err)
				require.Equal(t, original, after)
				output = []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "Source https://example.com/docs"}}}}
			}
			return common.Marshal(map[string]any{"id": "resp_fixture", "object": "response", "model": p.Model, "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120}})
		},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, rounds)
	require.Equal(t, 3, result.ToolCalls)
	require.Equal(t, 1, provider.searches)
	require.Equal(t, 1, provider.fetches, "find uses the real engine's cached page")
	var history struct {
		Input []json.RawMessage `json:"input"`
	}
	require.NoError(t, common.Unmarshal(continuation, &history))
	require.Len(t, history.Input, 7, "one user, three model calls, three actual engine results")
	require.Contains(t, string(continuation), "offline search evidence")
	require.Contains(t, string(continuation), "offline searchable page evidence")
	require.NotContains(t, string(result.Body), p.PrivateTool)
}
