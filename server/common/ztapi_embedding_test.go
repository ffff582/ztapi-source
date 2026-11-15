package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIEmbeddingResponseContract(t *testing.T) {
	valid := []byte(`{"object":"list","model":"text-embedding-ada-002","data":[{"object":"embedding","index":0,"embedding":[0.25,-0.5]},{"object":"embedding","index":1,"embedding":[0,1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	usage, err := ValidateZTAPIEmbeddingResponse(valid, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 3, usage)
	_, err = ValidateZTAPIEmbeddingResponse(valid, 1, 2)
	require.Error(t, err)
	_, err = ValidateZTAPIEmbeddingResponse(valid, 2, 1536)
	require.Error(t, err)
	for _, wire := range []string{
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]},{"object":"embedding","index":0,"embedding":[1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`,
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]},{"object":"embedding","index":1,"embedding":[1,2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`,
		`{"object":"list","data":[{"object":"embedding","embedding":[1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`,
		`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]}],"usage":{"prompt_tokens":3}}`,
	} {
		_, err = ValidateZTAPIEmbeddingResponse([]byte(wire), 0, 0)
		require.Error(t, err, wire)
	}
}

func TestZTAPIEmbeddingInputCount(t *testing.T) {
	for _, tc := range []struct {
		input any
		count int
	}{
		{"test", 1}, {[]any{"first", "second"}, 2}, {[]any{float64(1), float64(2)}, 1}, {[]any{[]any{float64(1)}, []any{float64(2)}}, 2},
	} {
		count, err := ZTAPIEmbeddingInputCount(tc.input)
		require.NoError(t, err)
		require.Equal(t, tc.count, count)
	}
	for _, input := range []any{nil, "", []any{}, []any{"text", float64(1)}, []any{float64(-1)}, []any{float64(1.5)}, []any{[]any{}}} {
		_, err := ZTAPIEmbeddingInputCount(input)
		require.Error(t, err)
	}
}
