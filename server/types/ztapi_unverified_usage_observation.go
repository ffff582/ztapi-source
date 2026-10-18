package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/QuantumNous/new-api/common"
)

const MaxZTAPIUnverifiedUsageBytes = 2048

type ZTAPIUnverifiedUsageObservation struct {
	Location       string          `json:"location,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	RawUsage       json.RawMessage `json:"raw_numeric_usage,omitempty"`
	RawUsageSHA256 string          `json:"raw_numeric_usage_sha256,omitempty"`
}

func (o *ZTAPIUnverifiedUsageObservation) Sanitized() *ZTAPIUnverifiedUsageObservation {
	if o == nil {
		return nil
	}
	copy := *o
	if copy.Location != "stream" && copy.Location != "nonstream" {
		copy.Location = ""
	}
	copy.Reason = ""
	if reasons := SanitizeZTAPIUsageIntegrityReasons([]string{o.Reason}); len(reasons) > 0 {
		copy.Reason = reasons[0]
	}
	filtered := FilterZTAPINumericUsage(o.RawUsage)
	if len(filtered) == 0 {
		return nil
	}
	encoded, err := common.Marshal(filtered)
	if err != nil {
		return nil
	}
	if len(encoded) > MaxZTAPIUnverifiedUsageBytes {
		// Prefer headline counters, then retain as many fixed whitelist fields
		// as fit. This is diagnostic evidence, never a complete billing receipt.
		bounded := make(map[string]json.RawMessage)
		keys := []string{"prompt_tokens", "completion_tokens", "input_tokens", "output_tokens", "total_tokens"}
		var remaining []string
		for key := range filtered {
			remaining = append(remaining, key)
		}
		sort.Strings(remaining)
		keys = append(keys, remaining...)
		for _, key := range keys {
			value, ok := filtered[key]
			if !ok {
				continue
			}
			bounded[key] = value
			candidate, marshalErr := common.Marshal(bounded)
			if marshalErr == nil && len(candidate) <= MaxZTAPIUnverifiedUsageBytes {
				continue
			}
			delete(bounded, key)
			if len(bounded) == 0 {
				var nested map[string]json.RawMessage
				if common.Unmarshal(value, &nested) == nil {
					var names []string
					for name := range nested {
						names = append(names, name)
					}
					sort.Strings(names)
					if len(names) > 0 {
						bounded[key], _ = common.Marshal(map[string]json.RawMessage{names[0]: nested[names[0]]})
					}
				}
			}
		}
		encoded, err = common.Marshal(bounded)
		if err != nil || len(bounded) == 0 || len(encoded) > MaxZTAPIUnverifiedUsageBytes {
			return nil
		}
	}
	copy.RawUsage = encoded
	hash := sha256.Sum256(encoded)
	copy.RawUsageSHA256 = hex.EncodeToString(hash[:])
	return &copy
}
