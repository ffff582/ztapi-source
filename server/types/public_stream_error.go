package types

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// Only failure envelopes are projected; successful model output is not rewritten.
func PublicStreamErrorPayload(data string) string {
	trimmed := strings.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return data
	}
	var frame struct {
		Type           string          `json:"type"`
		SequenceNumber *int64          `json:"sequence_number"`
		Error          json.RawMessage `json:"error"`
		Usage          json.RawMessage `json:"usage"`
		EventID        string          `json:"event_id"`
		OpenAIError
		Response *struct {
			ID                string          `json:"id"`
			Error             json.RawMessage `json:"error"`
			Usage             json.RawMessage `json:"usage"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if common.UnmarshalJsonStr(data, &frame) != nil {
		return `{"type":"error","error":{"type":"api_error","message":"Invalid upstream error response."}}`
	}
	failure := frame.Type == "error" || frame.Type == "response.error" || frame.Type == "response.failed" || frame.Type == "response.incomplete" || strings.HasSuffix(frame.Type, "_error")
	hasError := len(frame.Error) > 0 && string(frame.Error) != "null"
	if !failure && !hasError {
		return data
	}
	internal := frame.OpenAIError
	raw := frame.Error
	if frame.Response != nil && len(frame.Response.Error) > 0 && string(frame.Response.Error) != "null" {
		raw = frame.Response.Error
	}
	if len(raw) > 0 {
		_ = common.Unmarshal(raw, &internal)
	}
	public := WithOpenAIError(internal, 502).ToPublicOpenAIError()
	result := map[string]any{"error": public}
	if failure {
		result["type"] = "error"
		if frame.Type == "response.error" || frame.Type == "response.failed" || frame.Type == "response.incomplete" {
			result["type"] = frame.Type
		}
	}
	if frame.SequenceNumber != nil && *frame.SequenceNumber >= 0 {
		result["sequence_number"] = *frame.SequenceNumber
	}
	if frame.EventID != "" && len(frame.EventID) <= 128 && common.RedactSecrets(frame.EventID) == frame.EventID {
		result["event_id"] = frame.EventID
	}
	if usage := publicFailureUsage(frame.Usage); len(usage) > 0 {
		result["usage"] = usage
	}
	if frame.Type == "response.failed" || frame.Type == "response.incomplete" {
		status := "failed"
		if frame.Type == "response.incomplete" {
			status = "incomplete"
		}
		response := map[string]any{"object": "response", "status": status, "error": public}
		if frame.Response != nil && len(frame.Response.ID) <= 128 && common.RedactSecrets(frame.Response.ID) == frame.Response.ID {
			response["id"] = frame.Response.ID
			if usage := publicFailureUsage(frame.Response.Usage); len(usage) > 0 {
				response["usage"] = usage
			}
			if details := frame.Response.IncompleteDetails; details != nil && (details.Reason == "max_output_tokens" || details.Reason == "content_filter") {
				response["incomplete_details"] = map[string]string{"reason": details.Reason}
			}
		}
		result["response"] = response
		delete(result, "error")
	}
	encoded, err := common.Marshal(result)
	if err != nil {
		return `{"type":"error","error":{"type":"api_error","message":"Upstream request failed."}}`
	}
	return string(encoded)
}

func publicFailureUsage(raw json.RawMessage) map[string]any {
	return publicFailureUsageAtDepth(raw, 0)
}

func publicFailureUsageAtDepth(raw json.RawMessage, depth int) map[string]any {
	if depth > 4 {
		return nil
	}
	var fields map[string]json.RawMessage
	if common.Unmarshal(raw, &fields) != nil {
		return nil
	}
	result := make(map[string]any)
	for name, value := range fields {
		switch name {
		case "input_tokens_details", "output_tokens_details", "prompt_tokens_details", "completion_tokens_details", "cache_creation":
			if details := publicFailureUsageAtDepth(value, depth+1); len(details) > 0 {
				result[name] = details
			}
		case "input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "cached_tokens", "reasoning_tokens", "audio_tokens", "text_tokens", "accepted_prediction_tokens", "rejected_prediction_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens":
			if _, err := strconv.ParseUint(string(value), 10, 64); err == nil {
				result[name] = json.Number(string(value))
			}
		}
	}
	return result
}
