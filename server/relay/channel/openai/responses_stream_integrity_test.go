package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestResponsesViaChatDoesNotCompleteTruncatedUpstream(t *testing.T) {
	for name, body := range map[string]string{
		"bare EOF":            `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\n",
		"done without finish": `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\ndata: [DONE]\n\n",
		"malformed chunk":     `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}` + "\n\ndata: not-json\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			c, recorder, info := usageRegressionContext(types.RelayFormatOpenAI, true)
			_, apiErr := OaiChatToResponsesStreamHandler(c, info, &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil)
			require.NotNil(t, apiErr)
			require.NotContains(t, recorder.Body.String(), "event: response.completed")
			require.Contains(t, recorder.Body.String(), "event: response.failed")
		})
	}
}
