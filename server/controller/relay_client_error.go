package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/types"
)

// relayClientError maps only the public response. Retry, health and billing
// retain the original supplier status and error rather than the mapped 503.
func relayClientError(err *types.NewAPIError, format types.RelayFormat) *types.NewAPIError {
	if err == nil || (err.StatusCode != http.StatusUnauthorized && err.StatusCode != http.StatusForbidden) {
		return err
	}
	switch err.GetErrorType() {
	case types.ErrorTypeOpenAIError, types.ErrorTypeClaudeError, types.ErrorTypeUpstreamError:
	default:
		return err
	}
	if ztapiSafetyOutcomeError(err) {
		return err
	}
	if err.StatusCode == http.StatusForbidden {
		// A 403 can also be a safety refusal or ambiguous request permission
		// error; only explicit supplier authorization faults are retryable.
		wire := err.ToOpenAIError()
		knownAuthorizationFault := false
		for _, code := range []string{string(err.GetErrorCode()), fmt.Sprint(wire.Code), wire.Type} {
			switch strings.ToLower(strings.TrimSpace(code)) {
			case "invalid_api_key", "authentication_error", "unauthenticated", "invalid_key", "key_expired", "key_disabled", "model_not_granted":
				knownAuthorizationFault = true
			}
		}
		if !knownAuthorizationFault {
			return err
		}
	}
	message := "The upstream model service is temporarily unavailable. This is not an error with your ZTAPI API key. Please retry later."
	if format == types.RelayFormatClaude {
		return types.WithClaudeError(types.ClaudeError{Type: "overloaded_error", Message: message}, http.StatusServiceUnavailable)
	}
	return types.WithOpenAIError(types.OpenAIError{
		Type: "server_error", Code: "upstream_unavailable", Message: message,
	}, http.StatusServiceUnavailable)
}
