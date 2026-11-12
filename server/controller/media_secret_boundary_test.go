package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTaskErrorProjectionKeepsInternalEvidencePrivateAndUnchanged(t *testing.T) {
	for _, status := range []int{400, 429, 502} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(common.RequestIdKey, "req-media-public")
		original := &dto.TaskError{Code: "opaque-secret", Message: "opaque-secret", Data: map[string]any{"env": map[string]string{"ARBITRARY": "opaque-secret"}}, StatusCode: status}
		respondTaskError(c, original)
		require.Equal(t, status, recorder.Code)
		require.NotContains(t, recorder.Body.String(), "opaque-secret")
		require.Contains(t, recorder.Body.String(), "req-media-public")
		require.Equal(t, "opaque-secret", original.Message)
		require.NotNil(t, original.Data)
	}
}

func TestMidjourneyErrorProjectionDoesNotExposeOrMutateProviderEvidence(t *testing.T) {
	for _, code := range []int{4, 30} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(common.RequestIdKey, "req-media-public")
		original := &dto.MidjourneyResponse{Code: code, Description: "opaque-secret", Result: "opaque-secret", Properties: map[string]string{"environment": "opaque-secret"}}
		respondMidjourneyError(c, original)
		status := http.StatusBadRequest
		if code == 30 {
			status = http.StatusTooManyRequests
		}
		require.Equal(t, status, recorder.Code)
		require.NotContains(t, recorder.Body.String(), "opaque-secret")
		require.Contains(t, recorder.Body.String(), "req-media-public")
		require.Equal(t, "opaque-secret", original.Result)
	}
}
