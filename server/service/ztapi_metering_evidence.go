package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
)

func ztapiMeteringSnapshot(info *relaycommon.RelayInfo, usage *dto.Usage) *relaycommon.ZTAPIMeteringEvidence {
	if info == nil || info.ZTAPIMetering == nil {
		return nil
	}
	evidence := *info.ZTAPIMetering
	if usage != nil && usage.UsageSource != "" {
		evidence.UsageSource = usage.UsageSource
	} else if evidence.UsageSource == "" {
		evidence.UsageSource = "unverified"
		if usage == nil {
			evidence.UsageSource = "missing"
		}
	}
	return evidence.Sanitized()
}

func marshalZTAPIBillingUsage(info *relaycommon.RelayInfo, usage any) ([]byte, error) {
	textUsage, isText := usage.(*dto.Usage)
	if !isText || info == nil || info.ZTAPIMetering == nil {
		return common.Marshal(usage)
	}
	evidence := ztapiMeteringSnapshot(info, textUsage)
	if textUsage != nil {
		copyUsage := *textUsage
		textUsage = &copyUsage
		if cost, err := common.Marshal(copyUsage.Cost); err != nil || len(cost) > 64 || gjson.ParseBytes(cost).Type != gjson.Number {
			textUsage.Cost = nil
		}
		switch textUsage.UsageSemantic {
		case "", "openai", "anthropic":
		default:
			textUsage.UsageSemantic = "invalid"
		}
		if evidence != nil {
			textUsage.UsageSource = evidence.UsageSource
		}
	}
	// Keep normalized usage compatible with existing replay/refund readers, and
	// persist the independently captured counters without changing client JSON.
	return common.Marshal(struct {
		*dto.Usage
		Evidence *relaycommon.ZTAPIMeteringEvidence `json:"ztapi_metering_evidence"`
	}{textUsage, evidence})
}
