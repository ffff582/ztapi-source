package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIHealthEmbeddingVectorsAndUsage(t *testing.T) {
	valid := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,-0.5]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`
	for _, source := range []string{"real", "probe"} {
		out := healthFixture(t, "embeddings", source, valid, false, 200)
		require.Equal(t, "success", out.Result)
		require.False(t, out.HasText)
		require.Empty(t, out.FinishReasons)
	}
	for _, invalid := range []string{
		strings.Replace(valid, "[0.25,-0.5]", "[]", 1),
		strings.Replace(valid, "[0.25,-0.5]", "[null,1]", 1),
		strings.Replace(valid, "[0.25,-0.5]", `["0.25",1]`, 1),
		strings.Replace(valid, "[0.25,-0.5]", "[1e999,1]", 1),
		strings.Replace(valid, `"index":0`, `"index":1`, 1),
		strings.Replace(valid, `"total_tokens":3`, `"total_tokens":4`, 1),
		strings.Replace(valid, `"prompt_tokens":3`, `"prompt_tokens":3.5`, 1),
		strings.Replace(valid, `"prompt_tokens":3`, `"completion_tokens":1,"prompt_tokens":3`, 1),
		`{"choices":[{"message":{"content":"77"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"total_tokens":3}}`,
	} {
		out := healthFixture(t, "embeddings", "real", invalid, false, 200)
		require.Equal(t, "failure", out.Result, invalid)
	}
	out := healthFixture(t, "embeddings", "real", "data: "+valid+"\n\ndata: [DONE]\n\n", true, 200)
	require.Equal(t, "failure", out.Result)
	require.Equal(t, "embeddings", ztapiHealthProtocol("/v1/embeddings"))
}
