package hostedsearch

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func nativePrepared(t *testing.T, input, previous string) *PreparedRequest {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"model": "zt-gpt-6.1-sol", "input": input, "stream": true,
		"previous_response_id": previous, "store": true,
		"tools": []any{map[string]any{"type": "web_search", "search_context_size": "high"}},
	})
	require.NoError(t, err)
	p, err := PrepareAuthorizedRequest(raw, "ztapi_hosted_web_12345678")
	require.NoError(t, err)
	return p
}

func nativeReply(t *testing.T, id string, output []map[string]any) []byte {
	t.Helper()
	raw, err := common.Marshal(map[string]any{
		"id": id, "object": "response", "model": "gpt-6.1-sol", "status": "completed", "output": output,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
	})
	require.NoError(t, err)
	return raw
}

func nativeDispatch(ctx context.Context, _ ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
	return call(ctx)
}

func TestNativeResponsesPreviousIDReachesProviderAndReturnsFinalID(t *testing.T) {
	p, provider := nativePrepared(t, "Find the guide", "resp_previous"), &engineProvider{}
	rounds := 0
	result, err := Run(t.Context(), p, Config{Provider: provider, Dispatch: nativeDispatch,
		Model: func(_ context.Context, body []byte) ([]byte, error) {
			rounds++
			var request map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(body, &request))
			require.JSONEq(t, `"resp_previous"`, string(request["previous_response_id"]))
			require.JSONEq(t, `"zt-gpt-6.1-sol"`, string(request["model"]))
			require.JSONEq(t, `true`, string(request["store"]))
			require.JSONEq(t, `false`, string(request["stream"]))
			if rounds == 1 {
				return nativeReply(t, "resp_provider_search", []map[string]any{engineCall("search", p.PrivateTool, `{"action":"search","query":"guide"}`)}), nil
			}
			require.Contains(t, string(request["input"]), "function_call_output")
			return nativeReply(t, "resp_provider_final", []map[string]any{engineText("Verified guide")}), nil
		}}, nil)
	require.NoError(t, err)
	require.Equal(t, "resp_provider_final", result.ResponseID)
	require.Contains(t, string(result.Body), `"id":"resp_provider_final"`)
	require.Equal(t, 2, rounds)
	require.Equal(t, 1, provider.searches)

	next := nativePrepared(t, "Continue", result.ResponseID)
	_, err = Run(t.Context(), next, Config{Provider: provider, Dispatch: nativeDispatch,
		Model: func(_ context.Context, body []byte) ([]byte, error) {
			var request map[string]json.RawMessage
			require.NoError(t, common.Unmarshal(body, &request))
			require.JSONEq(t, `"resp_provider_final"`, string(request["previous_response_id"]))
			return nativeReply(t, "resp_provider_next", []map[string]any{engineText("Continued")}), nil
		}}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, provider.searches, "continuation must not repeat the settled search")
}

func TestNativeResponsesBuffersStreamUntilFinalProviderID(t *testing.T) {
	p, provider := nativePrepared(t, "Find guide", ""), &engineProvider{}
	var events []Event
	rounds := 0
	result, err := Run(t.Context(), p, Config{Provider: provider,
		Dispatch: func(ctx context.Context, tool ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
			require.Empty(t, events, "do not publish a provisional response ID")
			return nativeDispatch(ctx, tool, call)
		}, Model: func(context.Context, []byte) ([]byte, error) {
			require.Empty(t, events, "native events must wait for the final provider ID")
			rounds++
			if rounds == 1 {
				return nativeReply(t, "resp_search", []map[string]any{engineCall("search", p.PrivateTool, `{"action":"search","query":"guide"}`)}), nil
			}
			return nativeReply(t, "resp_final", []map[string]any{engineText("Guide")}), nil
		}}, func(event Event) error { events = append(events, event); return nil })
	require.NoError(t, err)
	require.NotEmpty(t, events)
	require.Equal(t, "response.created", events[0].Type)
	require.Equal(t, "response.completed", events[len(events)-1].Type)
	for index, event := range events {
		require.EqualValues(t, index, event.Data["sequence_number"])
		if response, ok := event.Data["response"].(map[string]any); ok {
			require.Equal(t, result.ResponseID, response["id"], event.Type)
		}
		if event.Type == "response.output_item.added" && event.Data["item"].(map[string]any)["type"] == "web_search_call" {
			require.Equal(t, "in_progress", event.Data["item"].(map[string]any)["status"], "buffered events must be immutable snapshots")
		}
	}
	require.Equal(t, "resp_final", result.ResponseID)
}

func TestNativeResponsesInitialAndInternalSizeBudgets(t *testing.T) {
	t.Run("native_above_legacy_limit", func(t *testing.T) {
		p := nativePrepared(t, strings.Repeat("x", 600*1024), "")
		require.Greater(t, len(p.Body), MaxRequestBytes)
		result, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
			Model: func(context.Context, []byte) ([]byte, error) {
				return nativeReply(t, "resp_large", []map[string]any{engineText("done")}), nil
			}}, nil)
		require.NoError(t, err)
		require.Equal(t, "resp_large", result.ResponseID)
	})
	t.Run("initial_above_16MiB_rejected", func(t *testing.T) {
		raw, err := common.Marshal(map[string]any{"model": "zt-gpt-6.1-sol", "input": strings.Repeat("x", 16<<20), "tools": []any{map[string]any{"type": "web_search"}}})
		require.NoError(t, err)
		_, err = PrepareAuthorizedRequest(raw, "ztapi_hosted_web_12345678")
		require.ErrorIs(t, err, ErrRequest)
	})
	for _, size := range []int{17 << 20, 20 << 20} {
		t.Run(strconv.Itoa(size>>20)+"MiB", func(t *testing.T) {
			p := nativePrepared(t, "Find guide", "")
			var err error
			p.Body, err = common.Marshal(map[string]any{"input": strings.Repeat("x", size)})
			require.NoError(t, err)
			calls := 0
			_, err = Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
				Model: func(context.Context, []byte) ([]byte, error) {
					calls++
					return nativeReply(t, "resp_internal", []map[string]any{engineText("done")}), nil
				}}, nil)
			if size == 17<<20 {
				require.NoError(t, err)
				require.Equal(t, 1, calls)
			} else {
				require.ErrorIs(t, err, ErrLimit)
				require.Zero(t, calls)
			}
		})
	}
}

func TestNativeResponsesAppendedResultsMayCrossLegacyLimit(t *testing.T) {
	p := nativePrepared(t, strings.Repeat("x", MaxRequestBytes-4096), "")
	require.Less(t, len(p.Body), MaxRequestBytes)
	rounds := 0
	_, err := Run(t.Context(), p, Config{Provider: largeContentProvider{&engineProvider{}}, Dispatch: nativeDispatch,
		Model: func(_ context.Context, body []byte) ([]byte, error) {
			rounds++
			if rounds == 1 {
				return nativeReply(t, "resp_fetch", []map[string]any{engineCall("fetch", p.PrivateTool, `{"action":"open_page","url":"https://example.com/docs"}`)}), nil
			}
			require.Greater(t, len(body), MaxRequestBytes)
			return nativeReply(t, "resp_after_fetch", []map[string]any{engineText("done")}), nil
		}}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, rounds)
}

func TestNativeResponsesLegacyChatLimitsAndStateRestrictionsUnchanged(t *testing.T) {
	raw, err := common.Marshal(map[string]any{"model": "zt-claude-opus-5", "input": strings.Repeat("x", 600*1024), "tools": []any{map[string]any{"type": "web_search"}}})
	require.NoError(t, err)
	for _, prepare := range []func([]byte, string) (*PreparedRequest, error){PrepareRequest, PrepareAuthorizedRequest} {
		_, err = prepare(raw, "ztapi_hosted_web_12345678")
		require.ErrorIs(t, err, ErrRequest)
	}
	for _, field := range []string{"conversation", "prompt"} {
		_, err := PrepareAuthorizedRequest([]byte(`{"model":"zt-gpt-6.1-sol","input":"hi","tools":[{"type":"web_search"}],"`+field+`":"stored"}`), "ztapi_hosted_web_12345678")
		require.ErrorIs(t, err, ErrRequest)
	}
	_, err = PrepareAuthorizedRequest([]byte(`{"model":"zt-claude-opus-5","input":"hi","tools":[{"type":"web_search"}],"previous_response_id":"resp_previous"}`), "ztapi_hosted_web_12345678")
	require.ErrorIs(t, err, ErrRequest)
	p := enginePrepared(t)
	p.Body = append(p.Body, []byte(strings.Repeat(" ", MaxRequestBytes))...)
	_, err = Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
		Model: func(context.Context, []byte) ([]byte, error) {
			t.Fatal("legacy oversized request dispatched")
			return nil, nil
		}}, nil)
	require.ErrorIs(t, err, ErrLimit)
}

func TestNativeResponsesFailureHasNoResumableProviderID(t *testing.T) {
	p := nativePrepared(t, "Find guide", "")
	var events []Event
	result, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
		Model: func(context.Context, []byte) ([]byte, error) { return nil, errors.New("upstream unavailable") }},
		func(event Event) error { events = append(events, event); return nil })
	require.Error(t, err)
	require.Nil(t, result)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "response.failed", last.Type)
	response := last.Data["response"].(map[string]any)
	require.Equal(t, "failed", response["status"])
	require.True(t, strings.HasPrefix(response["id"].(string), "hs_failed_"), "a local failure ID is not a resumable provider response")
}

func TestNativeResponsesCancellationAlwaysAttemptsFailedTerminal(t *testing.T) {
	for _, when := range []string{"before_model", "after_model", "during_flush"} {
		t.Run(when, func(t *testing.T) {
			p := nativePrepared(t, "Find guide", "")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if when == "before_model" {
				cancel()
			}
			calls := 0
			var events []Event
			result, err := Run(ctx, p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
				Model: func(context.Context, []byte) ([]byte, error) {
					calls++
					if when == "after_model" {
						cancel()
					}
					return nativeReply(t, "resp_paid", []map[string]any{engineText("done")}), nil
				}}, func(event Event) error {
				events = append(events, event)
				if when == "during_flush" && event.Type == "response.created" {
					cancel()
				}
				return nil
			})
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, result)
			require.NotEmpty(t, events, "even cancellation must attempt the failed event")
			last := events[len(events)-1]
			require.Equal(t, "response.failed", last.Type)
			require.EqualValues(t, len(events)-1, last.Data["sequence_number"])
			for _, event := range events {
				require.NotEqual(t, "response.completed", event.Type)
			}
			if when == "before_model" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			if when == "during_flush" {
				require.Equal(t, "resp_paid", last.Data["response"].(map[string]any)["id"], "a partially flushed stream keeps the already published real ID")
			}
		})
	}
}

func TestNativeResponsesBufferExhaustionAlwaysAttemptsFailedTerminal(t *testing.T) {
	for _, beforeModel := range []bool{false, true} {
		p := nativePrepared(t, "Find guide", "")
		if beforeModel {
			var err error
			p.OriginalTools, err = common.Marshal([]any{map[string]any{"type": "function", "name": "local", "description": strings.Repeat("x", 9<<20)}})
			require.NoError(t, err)
		}
		calls := 0
		var events []Event
		result, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
			Model: func(context.Context, []byte) ([]byte, error) {
				calls++
				parts := make([]any, 1500)
				for index := range parts {
					parts[index] = map[string]any{"type": "output_text", "text": "x", "annotations": []any{}}
				}
				return nativeReply(t, "resp_paid", []map[string]any{{"type": "message", "id": "msg_many", "content": parts}}), nil
			}}, func(event Event) error { events = append(events, event); return nil })
		require.ErrorIs(t, err, ErrLimit)
		require.Nil(t, result)
		require.Len(t, events, 1)
		require.Equal(t, "response.failed", events[0].Type)
		if beforeModel {
			require.Zero(t, calls)
		} else {
			require.Equal(t, 1, calls)
			require.EqualValues(t, 100, events[0].Data["response"].(map[string]any)["usage"].(map[string]any)["input_tokens"])
		}
	}
}

func TestNativeResponsesSettledSearchAndWriteFailureRetainFailedTerminal(t *testing.T) {
	for _, afterSearch := range []bool{false, true} {
		p, provider := nativePrepared(t, "Find guide", ""), &engineProvider{}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var events []Event
		writesFailed := errors.New("downstream write failed")
		result, err := Run(ctx, p, Config{Provider: provider,
			Dispatch: func(ctx context.Context, tool ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
				result, err := nativeDispatch(ctx, tool, call)
				cancel()
				return result, err
			}, Model: func(context.Context, []byte) ([]byte, error) {
				if afterSearch {
					return nativeReply(t, "resp_search", []map[string]any{engineCall("search", p.PrivateTool, `{"action":"search","query":"guide"}`)}), nil
				}
				return nativeReply(t, "resp_final", []map[string]any{engineText("done")}), nil
			}}, func(event Event) error {
			if !afterSearch && event.Type == "response.output_text.delta" {
				return writesFailed
			}
			events = append(events, event)
			return nil
		})
		require.Nil(t, result)
		if afterSearch {
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 1, provider.searches)
		} else {
			require.ErrorIs(t, err, writesFailed)
		}
		require.NotEmpty(t, events)
		require.Equal(t, "response.failed", events[len(events)-1].Type)
		for _, event := range events {
			require.NotEqual(t, "response.completed", event.Type)
		}
	}
}

func TestNativeResponsesRejectsUnfinishedMixedToolHistory(t *testing.T) {
	p, provider := nativePrepared(t, "Find guide", ""), &engineProvider{}
	result, err := Run(t.Context(), p, Config{Provider: provider, Dispatch: nativeDispatch,
		Model: func(context.Context, []byte) ([]byte, error) {
			return nativeReply(t, "resp_unfinished", []map[string]any{
				engineCall("search", p.PrivateTool, `{"action":"search","query":"guide"}`),
				engineCall("local", "exec_command", `{"cmd":"pwd"}`),
			}), nil
		}}, nil)
	require.ErrorIs(t, err, ErrModel)
	require.Nil(t, result)
	require.Zero(t, provider.searches, "do not buy search results that cannot enter resumable native history")
}

func TestNativeResponsesMixedRequiresExplicitStateless(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]any
		allow  bool
	}{
		{name: "explicit_false", fields: map[string]any{"store": false}, allow: true},
		{name: "store_true", fields: map[string]any{"store": true}},
		{name: "store_omitted", fields: map[string]any{}},
		{name: "store_null", fields: map[string]any{"store": nil}},
		{name: "store_string_false", fields: map[string]any{"store": "false"}},
		{name: "false_with_previous", fields: map[string]any{"store": false, "previous_response_id": "resp_previous"}},
		{name: "false_with_empty_previous", fields: map[string]any{"store": false, "previous_response_id": ""}},
		{name: "false_with_null_previous", fields: map[string]any{"store": false, "previous_response_id": nil}},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(test.name+"/stream_"+strconv.FormatBool(stream), func(t *testing.T) {
				body := map[string]any{
					"model": "zt-gpt-6.1-sol", "input": "Find the guide and run local tools", "stream": stream,
					"tools": []any{map[string]any{"type": "web_search"},
						map[string]any{"type": "function", "name": "exec_command", "parameters": map[string]any{"type": "object"}},
						map[string]any{"type": "custom", "name": "apply_patch", "format": map[string]any{"type": "text"}}},
				}
				for name, value := range test.fields {
					body[name] = value
				}
				raw, err := common.Marshal(body)
				require.NoError(t, err)
				p, err := PrepareAuthorizedRequest(raw, "ztapi_hosted_web_12345678")
				require.NoError(t, err)
				provider, rounds, charges := &engineProvider{}, 0, 0
				var events []Event
				var emit func(Event) error
				if stream {
					emit = func(event Event) error { events = append(events, event); return nil }
				}
				result, err := Run(t.Context(), p, Config{Provider: provider,
					Dispatch: func(ctx context.Context, tool ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
						charges++
						require.Empty(t, events)
						return nativeDispatch(ctx, tool, call)
					}, Model: func(_ context.Context, raw []byte) ([]byte, error) {
						rounds++
						var forwarded map[string]json.RawMessage
						require.NoError(t, common.Unmarshal(raw, &forwarded))
						if test.allow {
							require.JSONEq(t, "false", string(forwarded["store"]))
							require.NotContains(t, forwarded, "previous_response_id")
						}
						return nativeReply(t, "resp_mixed", []map[string]any{
							engineCall("search", p.PrivateTool, `{"action":"search","query":"guide"}`),
							engineCall("local", "exec_command", `{"cmd":"pwd"}`),
							{"id": "ct_patch", "type": "custom_tool_call", "call_id": "patch", "name": "apply_patch", "input": "patch", "status": "completed"},
						}), nil
					}}, emit)
				require.Equal(t, 1, rounds)
				if !test.allow {
					require.ErrorIs(t, err, ErrModel)
					require.Nil(t, result)
					require.Zero(t, charges)
					require.Zero(t, provider.searches+provider.fetches)
					if stream {
						require.Equal(t, "response.failed", events[len(events)-1].Type)
					}
					return
				}
				require.NoError(t, err)
				require.Equal(t, "resp_mixed", result.ResponseID)
				require.Equal(t, 1, charges)
				require.Equal(t, 1, provider.searches)
				require.NotContains(t, string(result.Body), p.PrivateTool)
				require.Contains(t, string(result.Body), "verified source")
				require.Contains(t, string(result.Body), `"call_id":"local"`)
				require.Contains(t, string(result.Body), `"call_id":"patch"`)
				require.Contains(t, string(result.Body), `"type":"custom_tool_call"`)
				require.NotContains(t, string(result.Body), `"type":"function_call_output"`)
				require.Less(t, strings.Index(string(result.Body), "Retrieved web evidence"), strings.Index(string(result.Body), "exec_command"))
				if stream {
					require.Equal(t, "response.created", events[0].Type)
					require.Equal(t, "response.completed", events[len(events)-1].Type)
					for _, event := range events {
						if response, ok := event.Data["response"].(map[string]any); ok {
							require.Equal(t, "resp_mixed", response["id"])
						}
					}
				}
				var response struct {
					Output []any `json:"output"`
				}
				require.NoError(t, common.Unmarshal(result.Body, &response))
				require.Len(t, response.Output, 4)
				history := append([]any{map[string]any{"role": "user", "content": body["input"]}}, response.Output...)
				history = append(history,
					map[string]any{"type": "function_call_output", "call_id": "local", "output": "actual command result"},
					map[string]any{"type": "custom_tool_call_output", "call_id": "patch", "output": "actual patch result"})
				body["input"] = history
				raw, err = common.Marshal(body)
				require.NoError(t, err)
				next, err := PrepareAuthorizedRequest(raw, p.PrivateTool)
				require.NoError(t, err)
				_, err = Run(t.Context(), next, Config{Provider: provider,
					Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
						t.Fatal("stateless local continuation must not buy search again")
						return Result{}, ErrTool
					}, Model: func(_ context.Context, raw []byte) ([]byte, error) {
						require.Contains(t, string(raw), "verified source")
						require.Contains(t, string(raw), "actual command result")
						require.Contains(t, string(raw), "actual patch result")
						require.NotContains(t, string(raw), "previous_response_id")
						return nativeReply(t, "resp_after_stateless", []map[string]any{engineText("done")}), nil
					}}, nil)
				require.NoError(t, err)
				require.Equal(t, 1, provider.searches)
			})
		}
	}
}

func TestNativeResponsesRequiresRealProviderID(t *testing.T) {
	for _, id := range []string{"", " ", "resp_bad\nvalue", strings.Repeat("x", 257)} {
		p := nativePrepared(t, "Find guide", "")
		result, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Dispatch: nativeDispatch,
			Model: func(context.Context, []byte) ([]byte, error) {
				return nativeReply(t, id, []map[string]any{engineText("done")}), nil
			}}, nil)
		require.ErrorIs(t, err, ErrModel, id)
		require.Nil(t, result)
	}
}

func TestNativeResponsesLocalCallsRemainResumableWithoutSearchCharges(t *testing.T) {
	for _, custom := range []bool{false, true} {
		p, provider := nativePrepared(t, "Run the local tool", ""), &engineProvider{}
		call := engineCall("local", "exec_command", `{"cmd":"pwd"}`)
		if custom {
			call = map[string]any{"id": "ct_local", "type": "custom_tool_call", "call_id": "local", "name": "apply_patch", "input": "patch", "status": "completed"}
		}
		result, err := Run(t.Context(), p, Config{Provider: provider, Dispatch: nativeDispatch,
			Model: func(context.Context, []byte) ([]byte, error) {
				return nativeReply(t, "resp_client_call", []map[string]any{call}), nil
			}}, nil)
		require.NoError(t, err)
		require.Equal(t, "resp_client_call", result.ResponseID)
		body, err := common.Marshal(map[string]any{
			"model": p.Model, "previous_response_id": result.ResponseID,
			"input": []any{map[string]any{"type": map[bool]string{false: "function_call_output", true: "custom_tool_call_output"}[custom], "call_id": "local", "output": "actual client result"}},
			"tools": []any{map[string]any{"type": "web_search"}},
		})
		require.NoError(t, err)
		next, err := PrepareAuthorizedRequest(body, p.PrivateTool)
		require.NoError(t, err)
		_, err = Run(t.Context(), next, Config{Provider: provider, Dispatch: nativeDispatch,
			Model: func(_ context.Context, body []byte) ([]byte, error) {
				require.Contains(t, string(body), `"previous_response_id":"resp_client_call"`)
				require.Contains(t, string(body), "actual client result")
				return nativeReply(t, "resp_after_client", []map[string]any{engineText("done")}), nil
			}}, nil)
		require.NoError(t, err)
		require.Zero(t, provider.searches+provider.fetches)
	}
}

func TestNativeResponsesEventBufferIsBounded(t *testing.T) {
	for _, byBytes := range []bool{false, true} {
		p := nativePrepared(t, "Find guide", "")
		emitted := 0
		e := &engine{p: p, emit: func(Event) error { emitted++; return nil }}
		if byBytes {
			require.ErrorIs(t, e.event("response.output_text.delta", map[string]any{"delta": strings.Repeat("x", 16<<20)}), ErrLimit)
		} else {
			for i := 0; i < 4096; i++ {
				require.NoError(t, e.event("response.output_text.delta", map[string]any{"delta": "x"}))
			}
			require.ErrorIs(t, e.event("response.output_text.delta", map[string]any{"delta": "x"}), ErrLimit)
		}
		require.Zero(t, emitted)
	}
}
