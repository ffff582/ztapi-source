package common

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ValidateZTAPIEmbeddingResponse returns authoritative input tokens. Zero count
// or dimensions means unknown expectations, not permission for empty vectors.
func ValidateZTAPIEmbeddingResponse(body []byte, expectedCount, expectedDimensions int) (int, error) {
	invalid := errors.New("embedding response requires finite indexed vectors and reconciled input-only usage")
	if !gjson.ValidBytes(body) {
		return 0, invalid
	}
	v := gjson.ParseBytes(body)
	if !v.IsObject() || v.Get("object").String() != "list" || v.Get("error").Type != gjson.Null || !v.Get("data").IsArray() {
		return 0, invalid
	}
	items := v.Get("data").Array()
	if len(items) == 0 || expectedCount > 0 && len(items) != expectedCount {
		return 0, invalid
	}
	seen := make(map[int]bool, len(items))
	dimensions := expectedDimensions
	for _, item := range items {
		index, ok := ztapiEmbeddingInteger(item.Get("index"))
		if !ok || index >= len(items) || seen[index] || item.Get("object").String() != "embedding" || !item.Get("embedding").IsArray() {
			return 0, invalid
		}
		seen[index] = true
		vector := item.Get("embedding").Array()
		if dimensions == 0 {
			dimensions = len(vector)
		}
		if len(vector) == 0 || len(vector) != dimensions {
			return 0, invalid
		}
		for _, number := range vector {
			if number.Type != gjson.Number || math.IsNaN(number.Float()) || math.IsInf(number.Float(), 0) {
				return 0, invalid
			}
		}
	}
	input, inputOK := ztapiEmbeddingInteger(v.Get("usage.prompt_tokens"))
	total, totalOK := ztapiEmbeddingInteger(v.Get("usage.total_tokens"))
	if !inputOK || !totalOK || input <= 0 || total != input {
		return 0, invalid
	}
	if output := v.Get("usage.completion_tokens"); output.Exists() {
		n, ok := ztapiEmbeddingInteger(output)
		if !ok || n != 0 {
			return 0, invalid
		}
	}
	return input, nil
}

func ztapiEmbeddingInteger(v gjson.Result) (int, bool) {
	if v.Type != gjson.Number {
		return 0, false
	}
	n, err := strconv.Atoi(v.Raw)
	return n, err == nil && n >= 0
}

// ZTAPIEmbeddingInputCount accepts one string, strings, one token sequence,
// or token sequences. Mixed or empty batches fail before upstream dispatch.
func ZTAPIEmbeddingInputCount(input any) (int, error) {
	invalid := errors.New("embedding input must contain nonempty strings or nonnegative integer token sequences")
	raw, err := Marshal(input)
	if err != nil {
		return 0, invalid
	}
	v := gjson.ParseBytes(raw)
	if v.Type == gjson.String {
		if strings.TrimSpace(v.String()) != "" {
			return 1, nil
		}
		return 0, invalid
	}
	if !v.IsArray() || len(v.Array()) == 0 {
		return 0, invalid
	}
	items := v.Array()
	tokens := func(v gjson.Result) bool {
		if !v.IsArray() || len(v.Array()) == 0 {
			return false
		}
		for _, item := range v.Array() {
			if _, ok := ztapiEmbeddingInteger(item); !ok {
				return false
			}
		}
		return true
	}
	if items[0].Type == gjson.Number {
		if tokens(v) {
			return 1, nil
		}
		return 0, invalid
	}
	for _, item := range items {
		if items[0].Type == gjson.String {
			if item.Type != gjson.String || strings.TrimSpace(item.String()) == "" {
				return 0, invalid
			}
		} else if !tokens(item) {
			return 0, invalid
		}
	}
	return len(items), nil
}
