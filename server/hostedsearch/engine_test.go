package hostedsearch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type engineProvider struct {
	searches, fetches int
}

func (p *engineProvider) Search(context.Context, string, int) (Result, error) {
	p.searches++
	return Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Sources: []Source{{Title: "Official", URL: "https://example.com/docs", Summary: "verified source"}}}, nil
}

func (p *engineProvider) Fetch(context.Context, string) (Result, error) {
	p.fetches++
	return Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Title: "Official", Content: "A verified searchable page."}, nil
}

func enginePrepared(t *testing.T) *PreparedRequest {
	t.Helper()
	p, err := PrepareRequest([]byte(`{"model":"zt-claude-opus-5","input":"Find official docs","tools":[{"type":"web_search"}],"stream":true,"max_output_tokens":1024}`), "ztapi_hosted_web_12345678")
	require.NoError(t, err)
	return p
}

func engineReply(t *testing.T, output []map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": "resp_model", "object": "response", "model": "zt-claude-opus-5", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120, "input_tokens_details": map[string]any{"cached_tokens": 40}}})
	require.NoError(t, err)
	return b
}

func engineCall(id, name, args string) map[string]any {
	return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": name, "arguments": args, "status": "completed"}
}

func engineText(text string) map[string]any {
	return map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "status": "completed", "content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func TestEngineSearchOpenFindUseRealResultsAndSeparateAccounting(t *testing.T) {
	p, provider := enginePrepared(t), &engineProvider{}
	rounds, accounted := 0, []string{}
	var requests []string
	result, err := Run(context.Background(), p, Config{
		Provider: provider,
		Model: func(_ context.Context, b []byte) ([]byte, error) {
			requests = append(requests, string(b))
			rounds++
			switch rounds {
			case 1:
				return engineReply(t, []map[string]any{engineCall("c1", p.PrivateTool, `{"action":"search","query":"official docs","count":3}`)}), nil
			case 2:
				return engineReply(t, []map[string]any{engineCall("c2", p.PrivateTool, `{"action":"open_page","url":"https://example.com/docs"}`)}), nil
			case 3:
				return engineReply(t, []map[string]any{engineCall("c3", p.PrivateTool, `{"action":"find_in_page","url":"https://example.com/docs","pattern":"searchable"}`)}), nil
			default:
				return engineReply(t, []map[string]any{engineText("Source: https://example.com/docs")}), nil
			}
		},
		Dispatch: func(ctx context.Context, tool ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
			accounted = append(accounted, tool.Action)
			return call(ctx)
		},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 4, rounds)
	require.Equal(t, []string{"search", "open_page"}, accounted)
	require.Equal(t, 1, provider.searches)
	require.Equal(t, 1, provider.fetches)
	require.Contains(t, requests[1], "verified source")
	require.Contains(t, requests[3], "searchable")
	require.NotContains(t, string(result.Body), p.PrivateTool)
	require.Contains(t, string(result.Body), "web_search_call")
	require.Contains(t, string(result.Body), "url_citation")
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(result.Body, &envelope))
	usage := envelope["usage"].(map[string]any)
	require.Equal(t, float64(400), usage["input_tokens"])
	require.Equal(t, float64(160), usage["input_tokens_details"].(map[string]any)["cached_tokens"])
}

func TestEngineMixedLocalToolsRetainSearchEvidenceBeforePendingCalls(t *testing.T) {
	p, provider := enginePrepared(t), &engineProvider{}
	rounds := 0
	result, err := Run(context.Background(), p, Config{Provider: provider,
		Model: func(context.Context, []byte) ([]byte, error) {
			rounds++
			return engineReply(t, []map[string]any{engineCall("search", p.PrivateTool, `{"action":"search","query":"docs"}`), engineCall("local", "exec_command", `{"cmd":"pwd"}`)}), nil
		}, Dispatch: func(ctx context.Context, _ ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
			return call(ctx)
		},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, rounds, "must return the local call, not fake its result")
	require.Contains(t, string(result.Body), "verified source")
	require.Contains(t, string(result.Body), "exec_command")
	require.NotContains(t, string(result.Body), p.PrivateTool)
	require.Less(t, strings.Index(string(result.Body), "verified source"), strings.Index(string(result.Body), "exec_command"))
}

func TestEngineStopsBeforeUnreservedOrInvalidDispatch(t *testing.T) {
	for _, args := range []string{`{"action":"search","query":"x","query":"y"}`, `{"action":"search","query":"x","count":51}`, `{"action":"open_page","url":"http://127.0.0.1/"}`, `{"action":"find_in_page","url":"https://example.com/","pattern":""}`} {
		p, provider := enginePrepared(t), &engineProvider{}
		_, err := Run(context.Background(), p, Config{Provider: provider, Model: func(context.Context, []byte) ([]byte, error) {
			return engineReply(t, []map[string]any{engineCall("bad", p.PrivateTool, args)}), nil
		}, Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
			t.Fatal("invalid tool was dispatched")
			return Result{}, nil
		}}, nil)
		require.Error(t, err)
		require.Zero(t, provider.searches+provider.fetches)
	}
	p, provider := enginePrepared(t), &engineProvider{}
	_, err := Run(context.Background(), p, Config{Provider: provider, Model: func(context.Context, []byte) ([]byte, error) {
		t.Fatal("missing accounting must fail before model I/O")
		return nil, nil
	}}, nil)
	require.Error(t, err)
}

func TestEngineFindOnNewPageBillsOneFetchThenUsesCache(t *testing.T) {
	p, provider := enginePrepared(t), &engineProvider{}
	round, charged := 0, 0
	result, err := Run(t.Context(), p, Config{Provider: provider, Model: func(context.Context, []byte) ([]byte, error) {
		round++
		if round <= 2 {
			return engineReply(t, []map[string]any{engineCall(strings.Repeat("f", round), p.PrivateTool, `{"action":"find_in_page","url":"https://example.com/docs","pattern":"searchable"}`)}), nil
		}
		return engineReply(t, []map[string]any{engineText("found https://example.com/docs")}), nil
	}, Dispatch: func(ctx context.Context, tool ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
		charged++
		require.Equal(t, "open_page", tool.Action)
		return call(ctx)
	}}, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, charged)
	require.Equal(t, 1, provider.fetches)
}

func TestEngineLimitsCancellationAndAccountingFailure(t *testing.T) {
	p, provider := enginePrepared(t), &engineProvider{}
	rounds := 0
	config := Config{Provider: provider, Model: func(context.Context, []byte) ([]byte, error) {
		rounds++
		return engineReply(t, []map[string]any{engineCall("c"+strings.Repeat("x", rounds), p.PrivateTool, `{"action":"search","query":"docs"}`)}), nil
	}, Dispatch: func(ctx context.Context, _ ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
		return call(ctx)
	}}
	_, err := Run(context.Background(), p, config, nil)
	require.ErrorIs(t, err, ErrLimit)
	require.Equal(t, 4, rounds)
	require.Equal(t, 3, provider.searches, "last allowed model round cannot trigger an unusable extra tool")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Run(ctx, p, config, nil)
	require.ErrorIs(t, err, context.Canceled)
	config.Dispatch = func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
		return Result{}, errors.New("reservation failed")
	}
	rounds, provider.searches = 0, 0
	_, err = Run(context.Background(), p, config, nil)
	require.Error(t, err)
	require.Zero(t, provider.searches)
}
