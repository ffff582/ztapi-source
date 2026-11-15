package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestZTAPIImageSaleNativeUsageExactThreeBuckets(t *testing.T) {
	raw := `{"input_tokens":6096,"input_tokens_details":{"text_tokens":2000,"image_tokens":4096},"output_tokens":8192,"output_tokens_details":{"image_tokens":8192,"text_tokens":0},"total_tokens":14288}`
	counts, err := ParseZTAPIGPTImage2NativeUsage(raw)
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"text_input": 2000, "image_input": 4096, "image_output": 8192}, counts)
	for _, raw := range []string{
		strings.Replace(raw, `"total_tokens":14288`, `"total_tokens":14289`, 1),
		strings.Replace(raw, `"text_tokens":0`, `"text_tokens":1`, 1),
		strings.Replace(raw, `"image_tokens":4096`, `"image_tokens":4096,"Image_Tokens":0`, 1),
		`{"input_tokens":9223372036854775807,"input_tokens_details":{"text_tokens":9223372036854775807,"image_tokens":1},"output_tokens":0,"output_tokens_details":{"image_tokens":0,"text_tokens":0},"total_tokens":0}`,
		`{"input_tokens":9223372036854775807,"input_tokens_details":{"text_tokens":9223372036854775807,"image_tokens":0},"output_tokens":1,"output_tokens_details":{"image_tokens":1,"text_tokens":0},"total_tokens":0}`,
	} {
		counts, err := ParseZTAPIGPTImage2NativeUsage(raw)
		require.Error(t, err)
		require.Nil(t, counts)
	}
}
