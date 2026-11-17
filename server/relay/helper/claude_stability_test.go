package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClaudeStabilityMessageStartIsNotAnOpenAIError(t *testing.T) {
	for _, raw := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		frame := dto.ClaudeResponse{Type: "message_start", Message: &dto.ClaudeMediaMessage{Id: "msg_local", Model: "claude-local"}}
		expected, err := common.Marshal(frame)
		require.NoError(t, err)
		if raw {
			expected = []byte(`{"type":"message_start","message":{"id":"msg_local","model":"claude-local"},"vendor_meta":{"trace_id":"SYNTHETIC_TRACE"}}`)
			ClaudeChunkData(c, frame, string(expected))
		} else {
			require.NoError(t, ClaudeData(c, frame))
		}
		require.Contains(t, recorder.Body.String(), `"type":"message_start"`)
		require.Contains(t, recorder.Body.String(), `"id":"msg_local"`)
		require.NotContains(t, recorder.Body.String(), `"type":"error"`)
		require.Contains(t, recorder.Body.String(), "data: "+string(expected), "valid message_start payload must remain byte-for-byte unchanged")
	}
}

func TestClaudeStabilityMessageStartWithErrorIsProjected(t *testing.T) {
	for _, raw := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		frame := dto.ClaudeResponse{Type: "message_start", Message: &dto.ClaudeMediaMessage{Model: "sk-private"},
			Error: types.ClaudeError{Type: "api_error", Message: "SYNTHETIC_PRIVATE_UPSTREAM"}}
		if raw {
			ClaudeChunkData(c, frame, `{"type":"message_start","message":{"model":"sk-private"},"error":{"type":"api_error","message":"SYNTHETIC_PRIVATE_UPSTREAM"}}`)
		} else {
			require.NoError(t, ClaudeData(c, frame))
		}
		require.NotContains(t, recorder.Body.String(), "sk-private")
		require.NotContains(t, recorder.Body.String(), "SYNTHETIC_PRIVATE_UPSTREAM")
		require.Contains(t, recorder.Body.String(), `"type":"error"`)
	}
}

func TestClaudeStabilityErrorFramesRemainRedacted(t *testing.T) {
	for _, raw := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		frame := dto.ClaudeResponse{Type: "error", Error: types.ClaudeError{Type: "api_error", Message: "SYNTHETIC_PRIVATE_UPSTREAM"}}
		if raw {
			ClaudeChunkData(c, frame, `{"type":"error","error":{"type":"api_error","message":"SYNTHETIC_PRIVATE_UPSTREAM"}}`)
		} else {
			require.NoError(t, ClaudeData(c, frame))
		}
		require.Contains(t, recorder.Body.String(), `"type":"error"`)
		require.NotContains(t, recorder.Body.String(), "SYNTHETIC_PRIVATE_UPSTREAM")
	}
}

func TestClaudeStabilityUnknownMessageDoesNotLeak(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "typed_unknown_object_message"
		if raw {
			name = "raw_unknown_string_message"
		}
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if raw {
				ClaudeChunkData(c, dto.ClaudeResponse{Type: "unknown"}, `{"type":"unknown","message":"sk-private"}`)
			} else {
				// The typed DTO cannot hold a top-level string message; its malformed object must still be projected.
				frame := dto.ClaudeResponse{Type: "unknown", Message: &dto.ClaudeMediaMessage{Model: "sk-private"}}
				require.NoError(t, ClaudeData(c, frame))
			}
			require.NotContains(t, recorder.Body.String(), "sk-private")
			require.Contains(t, recorder.Body.String(), `"type":"error"`)
		})
	}
}

func TestClaudeStabilityInvalidMessageStartDoesNotBypassProjection(t *testing.T) {
	for _, typed := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		frame := dto.ClaudeResponse{Type: "message_start"}
		if typed {
			frame.Message = &dto.ClaudeMediaMessage{Id: "msg_local"}
		}
		ClaudeChunkData(c, frame, `{"type":"message_start","message":"sk-private"}`)
		require.NotContains(t, recorder.Body.String(), "sk-private")
		require.Contains(t, recorder.Body.String(), `"type":"error"`)
	}
}
