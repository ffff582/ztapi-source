package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func logZTAPIPublicationLookupFailure(c *gin.Context, requestedModel string, err error) {
	if errors.Is(err, model.ErrZTAPIHealthCircuitOpen) || errors.Is(err, model.ErrZTAPIModelNotPublic) || errors.Is(err, model.ErrZTAPIModelGroupForbidden) {
		return
	}
	cause := "lookup_error"
	switch {
	case errors.Is(err, model.ErrZTAPIModelVersionConflict):
		cause = "publication_cache_conflict"
	case errors.Is(err, context.DeadlineExceeded):
		cause = "lookup_deadline_exceeded"
	case errors.Is(err, context.Canceled):
		cause = "lookup_cancelled"
	}
	// Only identifiers and typed causes are logged; driver errors can contain credentials.
	if len(requestedModel) > 255 || strings.HasPrefix(strings.ToLower(requestedModel), "sk-") || strings.ContainsAny(requestedModel, " \t\r\n") {
		requestedModel = "<invalid-model-identifier>"
	}
	encoded, _ := common.Marshal(map[string]any{
		"event": "ztapi_publication_lookup_failed", "requested_model": requestedModel,
		"user_id": c.GetInt("id"), "request_id": c.GetString(common.RequestIdKey),
		"cause": cause, "error_type": fmt.Sprintf("%T", err),
	})
	logger.LogError(c.Request.Context(), string(encoded))
}
