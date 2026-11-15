package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIConversionAdmissionRejectsUnsafeAnthropicToolsWithoutCharge(t *testing.T) {
	cases := []struct {
		name     string
		choice   any
		messages []any
	}{
		{"undeclared forced tool", map[string]any{"type": "tool", "name": "not_declared"}, []any{map[string]any{"role": "user", "content": "Use the tool."}}},
		{"orphan tool result", nil, []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "unseen_call", "content": "result"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
			before := f.balances(t)
			f.queue(ztapiE2ESuccess("/v1/chat/completions"))
			body := map[string]any{"model": f.publicName, "max_tokens": 128, "messages": tc.messages,
				"tools": []any{map[string]any{"name": "lookup", "input_schema": map[string]any{"type": "object"}}}}
			if tc.choice != nil {
				body["tool_choice"] = tc.choice
			}
			response := f.postJSON(t, "/v1/messages", body)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			after := f.balances(t)
			require.Zero(t, f.calls.Load(), "invalid tool semantics must not reach a billed provider")
			require.Equal(t, before.UserQuota, after.UserQuota)
			require.Equal(t, before.TokenRemain, after.TokenRemain)
			require.Equal(t, before.TokenUsed, after.TokenUsed)
			require.Zero(t, after.Logs)
			var retained int64
			require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Where("status <> ?", model.ZTAPISettlementReleased).Count(&retained).Error)
			require.Zero(t, retained, "an unsupported request must not leave a reserved/pending/settled charge")
			f.assertPublishedPrice(t)
		})
	}
}

func TestZTAPIConversionAdmissionGeminiUsesRealConverterAndRejectsWithoutCharge(t *testing.T) {
	for _, tc := range []struct {
		name     string
		parts    []any
		stops    []string
		accepted bool
	}{
		{"valid text reaches provider", []any{map[string]any{"text": "Reply OK."}}, nil, true},
		{"orphan result", []any{map[string]any{"functionResponse": map[string]any{"name": "lookup", "response": map[string]any{"ok": true}}}}, nil, false},
		{"unsupported fifth stop", []any{map[string]any{"text": "Reply OK."}}, []string{"a", "b", "c", "d", "e"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			f.router.POST("/v1beta/models/*model", func(c *gin.Context) { Relay(c, types.RelayFormatGemini) })
			before := f.balances(t)
			f.queue(ztapiE2ESuccess("/v1/chat/completions"))
			body := map[string]any{
				"contents":         []any{map[string]any{"role": "user", "parts": tc.parts}},
				"generationConfig": map[string]any{"maxOutputTokens": 128, "stopSequences": tc.stops},
			}
			response := f.postJSON(t, "/v1beta/models/"+f.publicName+":generateContent", body)
			if tc.accepted {
				require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				require.EqualValues(t, 1, f.calls.Load(), "positive control must exercise the same converter route")
				require.Less(t, f.balances(t).UserQuota, before.UserQuota)
				return
			}
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			after := f.balances(t)
			require.Zero(t, f.calls.Load())
			require.Equal(t, before.UserQuota, after.UserQuota)
			require.Equal(t, before.TokenRemain, after.TokenRemain)
			require.Equal(t, before.TokenUsed, after.TokenUsed)
			require.Zero(t, after.Logs)
			var retained int64
			require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Where("status <> ?", model.ZTAPISettlementReleased).Count(&retained).Error)
			require.Zero(t, retained)
			f.assertPublishedPrice(t)
		})
	}
}

func TestZTAPIConversionAdmissionResponsesRejectsOrphanToolOutputWithoutCharge(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "nonstreaming"
		if stream {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
			before := f.balances(t)
			reply := ztapiE2ESuccess("/v1/chat/completions")
			if stream {
				reply = ztapiE2EReply{status: http.StatusOK, stream: true, body: `data: {"id":"chat-orphan","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello."},"finish_reason":null}]}` + "\n\n" +
					`data: {"id":"chat-orphan","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
					`data: {"id":"chat-orphan","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n" +
					"data: [DONE]\n\n"}
			}
			f.queue(reply)
			response := f.postJSON(t, "/v1/responses", map[string]any{
				"model": f.publicName, "max_output_tokens": 128, "stream": stream,
				"input": []any{
					map[string]any{"role": "user", "content": "Read the file."},
					map[string]any{"type": "function_call_output", "call_id": "unseen_call", "output": "result"},
				},
			})
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "tool result")
			require.Zero(t, f.calls.Load(), "orphan output with a call_id must not reach the provider")
			after := f.balances(t)
			require.Equal(t, before.UserQuota, after.UserQuota)
			require.Equal(t, before.TokenRemain, after.TokenRemain)
			require.Equal(t, before.TokenUsed, after.TokenUsed)
			require.Equal(t, before.Logs, after.Logs)
			var retained int64
			require.NoError(t, f.db.Model(&model.ZTAPIRequestSettlement{}).Where("status <> ?", model.ZTAPISettlementReleased).Count(&retained).Error)
			require.Zero(t, retained, "rejected history must not leave a pending or settled charge")
			f.assertPublishedPrice(t)
		})
	}
}
