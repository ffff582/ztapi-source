package types

import (
	"fmt"
	"strings"
)

var publicUpstreamCodes = map[string]string{
	"context_length_exceeded":  "The request exceeds the model context limit. Reduce the prompt, history or output budget.",
	"max_tokens_exceeded":      "The requested output budget exceeds the model limit.",
	"request_too_large":        "The request exceeds the model size limit.",
	"model_not_found":          "The requested model is unavailable. Check the model ID.",
	"model_unavailable":        "The requested model is temporarily unavailable.",
	"model_not_available":      "The requested model is unavailable for this request.",
	"invalid_api_key":          "The upstream model could not be authorized. This is not a request to replace your ZTAPI key.",
	"authentication_error":     "The upstream model could not be authorized.",
	"model_not_granted":        "The upstream model is not authorized for this route.",
	"upstream_unavailable":     "The upstream model service is temporarily unavailable. This is not an error with your ZTAPI API key.",
	"rate_limit_exceeded":      "The upstream rate limit was reached. Please retry later.",
	"rate_limit_error":         "The upstream rate limit was reached. Please retry later.",
	"insufficient_quota":       "The upstream model quota is unavailable. Check the request in usage logs.",
	"content_filter":           "The upstream model declined this request under its safety policy.",
	"content_policy_violation": "The upstream model declined this request under its safety policy.",
	"safety_violation":         "The upstream model declined this request under its safety policy.",
	"prompt_blocked":           "The upstream model declined this prompt under its safety policy.",
	"violation_fee.grok.csam":  "The upstream model declined this request under its safety policy. Check the request's settlement record.",
	"invalid_request_error":    "The upstream model could not accept the request. Check the request format and usage-log request ID.",
	"invalid_request":          "The upstream model could not accept the request. Check the request format and usage-log request ID.",
	"overloaded_error":         "The upstream model is temporarily overloaded. Please retry later.",
	"api_error":                "The upstream model service failed. Check the request ID in usage logs.",
	"server_error":             "The upstream model service failed. Check the request ID in usage logs.",
}

func publicErrorCode(value any, status int, local ErrorCode) any {
	if value == nil {
		return nil
	}
	code := fmt.Sprint(value)
	if _, ok := publicUpstreamCodes[code]; ok {
		return code
	}
	if _, ok := value.(ErrorCode); ok && code == string(local) && isLocalPublicCode(local) {
		return code
	}
	if code == string(local) && isLocalPublicCode(local) {
		return code
	}
	if code == fmt.Sprint(status) && status >= 400 && status <= 599 {
		return value
	}
	return "upstream_error"
}

func isLocalPublicCode(code ErrorCode) bool {
	switch code {
	case ErrorCodeInvalidRequest, ErrorCodeSensitiveWordsDetected, ErrorCodeViolationFeeGrokCSAM,
		ErrorCode("invalid_reasoning_effort"),
		ErrorCodeCountTokenFailed, ErrorCodeModelPriceError, ErrorCodeInvalidApiType,
		ErrorCodeJsonMarshalFailed, ErrorCodeDoRequestFailed, ErrorCodeGetChannelFailed,
		ErrorCodeGenRelayInfoFailed, ErrorCodeChannelNoAvailableKey,
		ErrorCodeChannelParamOverrideInvalid, ErrorCodeChannelHeaderOverrideInvalid,
		ErrorCodeChannelModelMappedError, ErrorCodeChannelAwsClientError,
		ErrorCodeChannelInvalidKey, ErrorCodeChannelResponseTimeExceeded,
		ErrorCodeReadRequestBodyFailed, ErrorCodeConvertRequestFailed,
		ErrorCodeAccessDenied, ErrorCodeBadRequestBody, ErrorCodeReadResponseBodyFailed,
		ErrorCodeBadResponseStatusCode, ErrorCodeBadResponse, ErrorCodeBadResponseBody,
		ErrorCodeEmptyResponse, ErrorCodeAwsInvokeError, ErrorCodeModelNotFound,
		ErrorCodePromptBlocked, ErrorCodeQueryDataError, ErrorCodeUpdateDataError,
		ErrorCodeInsufficientUserQuota, ErrorCodePreConsumeTokenQuotaFailed:
		return true
	}
	return false
}

func safePublicErrorType(value string, status int) string {
	switch value {
	case "invalid_request_error", "rate_limit_error", "server_error", "authentication_error",
		"permission_error", "insufficient_quota", "overloaded_error", "api_error", "not_found_error",
		"timeout_error", "content_policy_violation", "new_api_error", "upstream_error", "error":
		return value
	}
	if status == 429 {
		return "rate_limit_error"
	}
	if status == 401 {
		return "authentication_error"
	}
	if status == 403 {
		return "permission_error"
	}
	if status >= 500 {
		return "server_error"
	}
	return "invalid_request_error"
}

func publicUpstreamMessage(code any, status int) string {
	if message, ok := publicUpstreamCodes[fmt.Sprint(code)]; ok {
		return message
	}
	switch {
	case status == 413:
		return "The request exceeds the upstream size limit. Reduce the prompt or attachments."
	case status == 429:
		return "The upstream rate limit was reached. Please retry later."
	case status == 401 || status == 403:
		return "The upstream model could not authorize this request. Check the request ID in usage logs."
	case status >= 500:
		return "The upstream model service is temporarily unavailable. Check the request ID in usage logs."
	default:
		return "The upstream model could not accept this request. Check the request format and usage-log request ID."
	}
}

func publicErrorParam(value string) string {
	switch value {
	case "model", "messages", "prompt", "stream", "max_tokens", "max_output_tokens", "input", "tools",
		"tool_choice", "temperature", "top_p", "n", "size", "quality", "response_format":
		return value
	}
	return ""
}

// Public conversions are intentionally separate from internal error evidence:
// health, retry and settlement retain the original supplier categories.
func (e *NewAPIError) ToPublicOpenAIError() OpenAIError {
	if e == nil {
		return OpenAIError{Type: "server_error", Code: "upstream_error", Message: "Request failed."}
	}
	result := e.ToOpenAIError()
	result.Metadata = nil
	result.Param = publicErrorParam(result.Param)
	result.Type = safePublicErrorType(result.Type, e.StatusCode)
	result.Code = publicErrorCode(result.Code, e.StatusCode, e.errorCode)
	if e.localOrigin {
		result.Message = "The request could not be validated or prepared locally. Check the request parameters and usage-log request ID."
	} else if e.errorType != ErrorTypeNewAPIError {
		result.Message = publicUpstreamMessage(result.Code, e.StatusCode)
	} else {
		// Adapters also wrap arbitrary supplier messages as local errors.
		// An enum, unlike the wrapped text, is a trusted public vocabulary.
		result.Message = publicLocalErrorMessage(e.errorCode, e.StatusCode, e.Error())
	}
	return result
}

func publicLocalErrorMessage(code ErrorCode, status int, internal string) string {
	if status == 401 {
		return "The API key is invalid or unavailable for this request."
	}
	if code == ErrorCodeInvalidRequest || code == ErrorCodeConvertRequestFailed {
		// Classify local validation hints, but never echo the wrapped text:
		// adapters can place arbitrary supplier diagnostics in the same error type.
		for _, hint := range []struct{ prefix, message string }{
			{"messages: orphan or duplicate tool result", "messages: each tool result must match one preceding tool call."},
			{"tools:", "tools: this model does not support the requested tool configuration."},
			{"tool_choice:", "tool_choice: the requested tool choice is unsupported or invalid."},
			{"custom tool format:", "format: the custom tool format is unsupported or invalid."},
			{"previous_response_id is not supported", "previous_response_id is not supported for this model; send the full conversation in input."},
		} {
			if strings.HasPrefix(internal, hint.prefix) {
				return hint.message
			}
		}
	}
	switch code {
	case ErrorCodeInvalidRequest, ErrorCodeBadRequestBody, ErrorCodeReadRequestBodyFailed:
		return "The request is invalid or exceeds the request size limit. Check the format, prompt and attachments."
	case ErrorCodeConvertRequestFailed, ErrorCodeInvalidApiType:
		return "This request cannot be represented by the selected model protocol. Check the model's supported inputs and tools."
	case ErrorCode("invalid_reasoning_effort"), ErrorCodeChannelParamOverrideInvalid, ErrorCodeChannelHeaderOverrideInvalid:
		return "The request parameters could not be prepared locally. Check the request format and usage-log request ID."
	case ErrorCodeCountTokenFailed:
		return "The request token budget could not be validated. Check the model, prompt and output budget."
	case ErrorCodeInsufficientUserQuota, ErrorCodePreConsumeTokenQuotaFailed:
		return "The available account quota is insufficient for this request."
	case ErrorCodeAccessDenied:
		return "This request is not permitted."
	case ErrorCodeSensitiveWordsDetected, ErrorCodePromptBlocked, ErrorCodeViolationFeeGrokCSAM:
		return "The request was declined under the model safety policy. Check the request's usage and settlement record."
	case ErrorCodeModelNotFound, ErrorCodeGetChannelFailed, ErrorCodeModelPriceError, ErrorCodeChannelNoAvailableKey:
		return "The selected model is currently unavailable for this request. Check the model ID and request ID in usage logs."
	default:
		if status >= 400 && status < 500 {
			return "The request could not be validated or prepared locally. Check the request parameters and usage-log request ID."
		}
		return publicUpstreamMessage(nil, status)
	}
}

func (e *NewAPIError) ToPublicClaudeError() ClaudeError {
	if e == nil {
		return ClaudeError{Type: "api_error", Message: "Request failed."}
	}
	result := e.ToPublicOpenAIError()
	value := e.ToClaudeError().Type
	switch value {
	case "invalid_request_error", "authentication_error", "permission_error", "not_found_error", "request_too_large", "rate_limit_error", "api_error", "overloaded_error":
	default:
		switch {
		case e.StatusCode >= 500:
			value = "api_error"
		case e.StatusCode == 429:
			value = "rate_limit_error"
		case e.StatusCode == 413:
			value = "request_too_large"
		case e.StatusCode == 401:
			value = "authentication_error"
		case e.StatusCode == 403:
			value = "permission_error"
		default:
			value = "invalid_request_error"
		}
	}
	return ClaudeError{Type: strings.TrimSpace(value), Message: result.Message}
}
