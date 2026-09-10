package relay

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestZTAPIEmbeddingResponseValidatedBeforeDelivery(t *testing.T) {
	vector := make([]float64, 1536)
	wire, err := common.Marshal(map[string]any{"object": "list", "data": []any{map[string]any{"object": "embedding", "index": 0, "embedding": vector}}, "usage": map[string]int{"prompt_tokens": 3, "total_tokens": 3}})
	require.NoError(t, err)
	request := &dto.EmbeddingRequest{Input: "test"}
	response := func(body string) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	}
	resp := response(string(wire))
	require.NoError(t, validateZTAPIEmbeddingHTTPResponse(resp, request))
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, wire, got, "validation must preserve raw upstream result")
	request.Input = []any{"first", "second"}
	require.Error(t, validateZTAPIEmbeddingHTTPResponse(response(string(wire)), request))
	request.Input = "test"
	require.Error(t, validateZTAPIEmbeddingHTTPResponse(response(`{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}]}`), request))
	resp = response(string(wire))
	resp.Header.Set("Content-Type", "text/event-stream")
	require.Error(t, validateZTAPIEmbeddingHTTPResponse(resp, request))
}
