package hostedsearch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPageCacheBoundedAndIsolatedByAuthenticatedIdentity(t *testing.T) {
	cache := NewPageCache(1024, time.Minute)
	one, two := cache.ForIdentity("user:1:keyhash:1"), cache.ForIdentity("user:2:keyhash:2")
	page := Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Title: "docs", Content: "verified page"}
	one.Put("https://example.com/docs", page)
	one.RememberSource(Source{Title: "docs", URL: "https://example.com/docs"})
	require.Len(t, one.Sources(), 1)
	require.Empty(t, two.Sources())
	got, ok := one.Get("https://example.com/docs")
	require.True(t, ok)
	require.Equal(t, page.Content, got.Content)
	_, ok = two.Get("https://example.com/docs")
	require.False(t, ok)
	for i := 0; i < 100; i++ {
		one.Put("https://example.com/"+NewID(""), page)
	}
	require.LessOrEqual(t, cache.bytes, 1024)
	require.LessOrEqual(t, len(cache.entries), 256)
	one.Put("https://example.com/bad", Result{Outcome: OutcomeUnknown, Content: "unverified"})
	_, ok = one.Get("https://example.com/bad")
	require.False(t, ok)
}

func TestPageCacheExpiresAndRejectsOversizedOrUnsafePages(t *testing.T) {
	cache := NewPageCache(1024, time.Millisecond)
	view := cache.ForIdentity("owned")
	view.Put("http://127.0.0.1/", Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Content: "x"})
	require.Empty(t, cache.entries)
	view.Put("https://example.com/", Result{Outcome: OutcomeSucceeded, HTTPStatus: 200, Content: "x"})
	require.Len(t, cache.entries, 1)
	cache.now = func() time.Time { return time.Now().Add(time.Hour) }
	_, ok := view.Get("https://example.com/")
	require.False(t, ok)
	require.Zero(t, cache.bytes)
}

func TestEngineEventsUseEmptyAddedItemsAndExactlyOneTerminal(t *testing.T) {
	p := enginePrepared(t)
	var events []map[string]any
	_, err := Run(t.Context(), p, Config{Provider: &engineProvider{}, Model: func(_ context.Context, _ []byte) ([]byte, error) {
		return engineReply(t, []map[string]any{engineText("OK")}), nil
	}, Dispatch: func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error) {
		t.Fatal("no tool requested")
		return Result{}, nil
	}}, func(event Event) error {
		b, _ := json.Marshal(event.Data)
		var copy map[string]any
		_ = json.Unmarshal(b, &copy)
		events = append(events, copy)
		return nil
	})
	require.NoError(t, err)
	terminals := 0
	for i, event := range events {
		require.Equal(t, float64(i), event["sequence_number"])
		if event["type"] == "response.completed" {
			terminals++
		}
		if event["type"] == "response.output_item.added" {
			item := event["item"].(map[string]any)
			require.Equal(t, "in_progress", item["status"])
			require.Empty(t, item["content"])
		}
	}
	require.Equal(t, 1, terminals)
}
