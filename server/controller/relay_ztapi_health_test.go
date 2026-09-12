package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func TestZTAPIHealthNoRetryAfterResponseCommitted(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Writer.WriteHeaderNow()
	err := types.NewError(errors.New("upstream failed"), types.ErrorCodeDoRequestFailed)
	err.StatusCode = 502
	if shouldRetry(c, err, 3) {
		t.Fatal("retry permitted after response headers were committed")
	}
}

func TestZTAPIHealthActualStreamHelperWritesDoNotForgeCompletion(t *testing.T) {
	collector := relaycommon.NewZTAPIHealthCollector("real")
	a := collector.BeginAttempt(1, "responses")
	a.Dispatched()
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))}
	a.WrapResponse(resp)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Writer = relaycommon.NewZTAPIHealthResponseWriter(c.Writer, collector)
	helper.Done(c)
	_ = helper.ClaudeData(c, dto.ClaudeResponse{Type: "message_stop"})
	if strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), "message_stop") {
		t.Fatalf("actual helpers bypassed guard: %s", w.Body.String())
	}
}

func TestZTAPIHealthRelayDoesNotAppendJSONAfterStreamStarted(t *testing.T) {
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":`))
	c.Request.Header.Set("Content-Type", "application/json")
	_, _ = c.Writer.WriteString("data: partial\n\n")
	Relay(c, types.RelayFormatOpenAI)
	if r.Body.String() != "data: partial\n\n" {
		t.Fatalf("error rewrote live stream: %s", r.Body.String())
	}
}
