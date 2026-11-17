package types

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// ParseZTAPIGPTImage2NativeUsage accepts only the approved, uncached three-bucket
// receipt. A valid JSON object alone is not evidence of a supplier Token count.
func ParseZTAPIGPTImage2NativeUsage(raw string) (map[string]int64, error) {
	invalid := errors.New("GPTImage2 native usage missing or inconsistent")
	if len(raw) > 65536 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)) != nil {
		return nil, invalid
	}
	object := func(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
		var fields map[string]json.RawMessage
		if common.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
			return nil, false
		}
		for _, key := range keys {
			if _, ok := fields[key]; !ok {
				return nil, false
			}
		}
		return fields, true
	}
	fields, ok := object(json.RawMessage(raw), "input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details", "total_tokens")
	if !ok {
		return nil, invalid
	}
	input, ok := object(fields["input_tokens_details"], "text_tokens", "image_tokens")
	if !ok {
		return nil, invalid
	}
	output, ok := object(fields["output_tokens_details"], "text_tokens", "image_tokens")
	if !ok {
		return nil, invalid
	}
	counts := []json.RawMessage{input["text_tokens"], input["image_tokens"], output["image_tokens"], output["text_tokens"], fields["input_tokens"], fields["output_tokens"], fields["total_tokens"]}
	values := make([]int64, len(counts))
	for i, rawCount := range counts {
		count := strings.TrimSpace(string(rawCount))
		if len(count) == 0 || len(count) > 19 {
			return nil, invalid
		}
		for _, ch := range count {
			if ch < '0' || ch > '9' {
				return nil, invalid
			}
		}
		value, err := strconv.ParseInt(count, 10, 64)
		if err != nil {
			return nil, invalid
		}
		values[i] = value
	}
	text, image, generated := values[0], values[1], values[2]
	if values[3] != 0 || text > math.MaxInt64-image || text+image != values[4] || generated != values[5] ||
		values[4] > math.MaxInt64-generated || values[4]+generated != values[6] {
		return nil, invalid
	}
	return map[string]int64{"text_input": text, "image_input": image, "image_output": generated}, nil
}
