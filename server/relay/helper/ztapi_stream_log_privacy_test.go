package helper

import (
	"bytes"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIStreamDebugLogOmitsPayload(t *testing.T) {
	var logs bytes.Buffer
	oldDebug := common.DebugEnabled
	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	common.DebugEnabled = true
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})
	body := "data: {\"text\":\"PRIVATE_STREAM_CANARY\",\"arguments\":\"PRIVATE_ARGUMENT_CANARY\"}\n\ndata: [DONE]\n\n"
	c, resp, info := setupStreamTest(t, strings.NewReader(body))
	StreamScannerHandler(c, resp, info, func(string, *StreamResult) {})
	require.NotContains(t, logs.String(), "PRIVATE_STREAM_CANARY")
	require.NotContains(t, logs.String(), "PRIVATE_ARGUMENT_CANARY")
	require.Contains(t, logs.String(), "stream scanner bytes:")
}
