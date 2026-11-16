package hostedsearch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEngineReturnsStandardRefusalWithoutProtocolFailure(t *testing.T) {
	p := enginePrepared(t)
	var events []string
	result, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Model: func(context.Context, []byte) ([]byte, error) {
		return engineReply(t, []map[string]any{{"type": "message", "id": "msg_refusal", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "Unable to help."}}}}), nil
	}, Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
		panic("unexpected tool")
	}}, func(event Event) error { events = append(events, event.Type); return nil })
	require.NoError(t, err)
	require.Contains(t, string(result.Body), "Unable to help.")
	require.Contains(t, events, "response.refusal.done")
	require.NotContains(t, events, "response.failed")
}

func TestEngineRetainsVerifiedSearchCitationsAcrossMixedClientTurns(t *testing.T) {
	cache := NewPageCache(1<<20, time.Minute).ForIdentity("authenticated-key")
	p, provider := enginePrepared(t), &engineProvider{}
	first := Config{Pages: cache, Provider: provider, Model: func(context.Context, []byte) ([]byte, error) {
		return engineReply(t, []map[string]any{engineCall("search", p.PrivateTool, `{"action":"search","query":"docs"}`), engineCall("local", "exec_command", `{"cmd":"pwd"}`)}), nil
	}, Dispatch: func(ctx context.Context, _ ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
		return call(ctx)
	}}
	_, err := Run(t.Context(), p, first, nil)
	require.NoError(t, err)
	first.Model = func(context.Context, []byte) ([]byte, error) {
		return engineReply(t, []map[string]any{engineText("Verified https://example.com/docs")}), nil
	}
	result, err := Run(t.Context(), p, first, nil)
	require.NoError(t, err)
	require.Contains(t, string(result.Body), "url_citation")
}

func TestEngineContextBudgetIsExplicitAndDoesNotDiscardCachedPage(t *testing.T) {
	for _, size := range []int{2048, 8192} {
		p, provider := enginePrepared(t), &engineProvider{}
		p.ContextBytes = size
		round := 0
		var secondInput string
		_, err := Run(t.Context(), p, Config{Provider: largeContentProvider{provider}, Model: func(_ context.Context, b []byte) ([]byte, error) {
			round++
			if round == 1 {
				return engineReply(t, []map[string]any{engineCall("open", p.PrivateTool, `{"action":"open_page","url":"https://example.com/docs"}`)}), nil
			}
			secondInput = string(b)
			return engineReply(t, []map[string]any{engineText("done")}), nil
		}, Dispatch: func(ctx context.Context, _ ToolRequest, call func(context.Context) (Result, error)) (Result, error) {
			return call(ctx)
		}}, nil)
		require.NoError(t, err)
		var body struct {
			Input []struct {
				Type   string `json:"type"`
				Output string `json:"output"`
			} `json:"input"`
		}
		require.NoError(t, json.Unmarshal([]byte(secondInput), &body))
		for _, item := range body.Input {
			if item.Type == "function_call_output" {
				require.LessOrEqual(t, len(item.Output), size)
				if size == 2048 {
					require.Contains(t, item.Output, "content_truncated")
				}
			}
		}
	}
}

type largeContentProvider struct{ *engineProvider }

func TestEngineDoesNotCiteUnverifiedURLByPrefix(t *testing.T) {
	p := enginePrepared(t)
	cache := NewPageCache(1<<20, time.Minute).ForIdentity("authenticated-key")
	cache.RememberSource(Source{Title: "known", URL: "https://example.com/"})
	result, err := Run(t.Context(), p, Config{Pages: cache, Provider: &engineProvider{}, Model: func(context.Context, []byte) ([]byte, error) {
		return engineReply(t, []map[string]any{engineText("Unverified https://example.com/other-page")}), nil
	}, Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
		panic("unexpected tool")
	}}, nil)
	require.NoError(t, err)
	require.NotContains(t, string(result.Body), "url_citation")
}

func TestEnginePreservesVerifiedStructuredCitationsWithoutLiteralURL(t *testing.T) {
	p := enginePrepared(t)
	cache := NewPageCache(1<<20, time.Minute).ForIdentity("authenticated-key")
	cache.RememberSource(Source{Title: "Official", URL: "https://example.com/docs"})
	message := engineText("\u53c2\u89c1\u5b98\u65b9\u6587\u6863")
	message["content"].([]map[string]any)[0]["annotations"] = []any{map[string]any{"type": "url_citation", "url": "https://example.com/docs", "title": "untrusted title", "start_index": 0, "end_index": 6}}
	result, err := Run(t.Context(), p, Config{Pages: cache, Provider: &engineProvider{}, Model: func(context.Context, []byte) ([]byte, error) { return engineReply(t, []map[string]any{message}), nil }, Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
		panic("unexpected tool")
	}}, nil)
	require.NoError(t, err)
	require.Contains(t, string(result.Body), "url_citation")
	require.Contains(t, string(result.Body), "Official")
	require.NotContains(t, string(result.Body), "untrusted title")
}

func (p largeContentProvider) Fetch(context.Context, string) (Result, error) {
	return Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Title: "docs", Content: strings.Repeat("verified page line\n", 200)}, nil
}
