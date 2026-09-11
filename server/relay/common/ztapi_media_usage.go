package common

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	basecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

var ErrZTAPIMediaUsagePending = errors.New("ZTAPI media usage pending")

type ZTAPIMediaUsageEvidence struct {
	UpstreamRequestID string
	ResultCount       int
	ResultAvailable   bool
	Pending           bool
	Reason            string
	RawUsageJSON      string
	SelectedRuleID    string
	dimensions        map[string]decimal.Decimal
	priceRuleIDs      map[string]string
}

func (evidence *ZTAPIMediaUsageEvidence) setDimensions(dimensions map[string]decimal.Decimal) {
	if len(dimensions) == 0 {
		evidence.dimensions = nil
		return
	}
	clone := make(map[string]decimal.Decimal, len(dimensions))
	for dimension, quantity := range dimensions {
		clone[dimension] = quantity
	}
	evidence.dimensions = clone
}

func (evidence ZTAPIMediaUsageEvidence) GetDimensions() map[string]decimal.Decimal {
	if len(evidence.dimensions) == 0 {
		return nil
	}
	clone := make(map[string]decimal.Decimal, len(evidence.dimensions))
	for dimension, quantity := range evidence.dimensions {
		clone[dimension] = quantity
	}
	return clone
}

func (evidence *ZTAPIMediaUsageEvidence) setPriceRuleIDs(ruleIDs map[string]string) {
	if len(ruleIDs) == 0 {
		evidence.priceRuleIDs = nil
		return
	}
	evidence.priceRuleIDs = make(map[string]string, len(ruleIDs))
	for dimension, ruleID := range ruleIDs {
		evidence.priceRuleIDs[dimension] = ruleID
	}
}

func (evidence ZTAPIMediaUsageEvidence) GetPriceRuleIDs() map[string]string {
	if len(evidence.priceRuleIDs) == 0 {
		return nil
	}
	clone := make(map[string]string, len(evidence.priceRuleIDs))
	for dimension, ruleID := range evidence.priceRuleIDs {
		clone[dimension] = ruleID
	}
	return clone
}

func (evidence ZTAPIMediaUsageEvidence) Clone() ZTAPIMediaUsageEvidence {
	clone := evidence
	clone.setDimensions(evidence.dimensions)
	clone.setPriceRuleIDs(evidence.priceRuleIDs)
	clone.RawUsageJSON = string(append([]byte(nil), evidence.RawUsageJSON...))
	return clone
}

func (evidence *ZTAPIMediaUsageEvidence) SetRawUsageJSON(rawUsage []byte) {
	evidence.RawUsageJSON = string(append([]byte(nil), rawUsage...))
}

func (evidence ZTAPIMediaUsageEvidence) GetRawUsageJSON() []byte {
	return append([]byte(nil), evidence.RawUsageJSON...)
}

func NormalizeZTAPIImageUsage(info *RelayInfo, response []byte) (ZTAPIMediaUsageEvidence, error) {
	if info == nil {
		return ZTAPIMediaUsageEvidence{}, errors.New("verified ZTAPI image response handoff is required")
	}
	handoff := info.clonePublishedZTAPIImageResponse()
	return NormalizeZTAPIImageUsageCandidate(info, handoff, response)
}

func NormalizeZTAPIImageUsageCandidate(info *RelayInfo, handoff *ZTAPIValidatedImageResponse, response []byte) (ZTAPIMediaUsageEvidence, error) {
	if info == nil || handoff == nil {
		return ZTAPIMediaUsageEvidence{}, errors.New("verified ZTAPI image response handoff is required")
	}
	if !bytes.Equal(response, handoff.RawResponse) {
		return ZTAPIMediaUsageEvidence{}, errors.New("ZTAPI image response does not match the verified handoff")
	}
	if handoff.ResultCount <= 0 {
		return ZTAPIMediaUsageEvidence{}, errors.New("verified ZTAPI image response has no result")
	}
	evidence := ZTAPIMediaUsageEvidence{
		UpstreamRequestID: handoff.UpstreamRequestID,
		ResultCount:       handoff.ResultCount,
		ResultAvailable:   true,
	}
	contract, err := validateZTAPIMediaUsageAuthority(info, handoff)
	if err != nil {
		if errors.Is(err, ErrZTAPIMediaUsagePending) {
			return pendingZTAPIMediaUsageEvidence(evidence, err)
		}
		return ZTAPIMediaUsageEvidence{}, err
	}
	if handoff.UsagePendingReason != "" {
		evidence.SetRawUsageJSON(handoff.RawUsageJSON)
		return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: %s", ErrZTAPIMediaUsagePending, handoff.UsagePendingReason))
	}
	usageResult := gjson.GetBytes(handoff.RawResponse, contract.Usage.UsageField)
	if !usageResult.Exists() {
		if len(handoff.RawUsageJSON) != 0 {
			return ZTAPIMediaUsageEvidence{}, errors.New("ZTAPI image usage handoff does not match the response")
		}
		return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: usage is missing", ErrZTAPIMediaUsagePending))
	}
	if !bytes.Equal([]byte(usageResult.Raw), handoff.RawUsageJSON) {
		return ZTAPIMediaUsageEvidence{}, errors.New("ZTAPI image usage handoff is not byte-exact")
	}
	evidence.SetRawUsageJSON(handoff.RawUsageJSON)
	if !usageResult.IsObject() || basecommon.RejectDuplicateJsonObjectMembers(bytes.NewReader(handoff.RawUsageJSON)) != nil {
		return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: usage is malformed", ErrZTAPIMediaUsagePending))
	}
	if reason := ZTAPIGPTImage2UsagePendingReason(handoff.RawUsageJSON, contract); reason != "" {
		return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: %s", ErrZTAPIMediaUsagePending, reason))
	}
	dimensions := make(map[string]decimal.Decimal, len(contract.Usage.Fields))
	quantities := make(map[string]int64, len(contract.Usage.Fields))
	var sum int64
	for dimension, field := range contract.Usage.Fields {
		quantity, ok := exactZTAPIMediaUsageInteger(gjson.GetBytes(handoff.RawUsageJSON, field))
		if !ok || sum > math.MaxInt64-quantity {
			return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: usage dimension %q is missing or malformed", ErrZTAPIMediaUsagePending, dimension))
		}
		quantities[dimension] = quantity
		dimensions[dimension] = decimal.NewFromInt(quantity)
		sum += quantity
	}
	total, ok := exactZTAPIMediaUsageInteger(gjson.GetBytes(handoff.RawUsageJSON, contract.Usage.TotalField))
	if !ok || total != sum || total == 0 {
		return pendingZTAPIMediaUsageEvidence(evidence, fmt.Errorf("%w: usage total does not equal the declared dimension sum", ErrZTAPIMediaUsagePending))
	}
	selectedRuleID, priceRuleIDs, err := selectZTAPIImageUsageRules(info.ZTAPIPublicationSnapshot.MediaPriceContractJSON, quantities)
	if err != nil {
		return ZTAPIMediaUsageEvidence{}, err
	}
	evidence.SelectedRuleID = selectedRuleID
	evidence.setDimensions(dimensions)
	evidence.setPriceRuleIDs(priceRuleIDs)
	return evidence, nil
}

// ZTAPIGPTImage2UsagePendingReason validates the exact three-dimensional
// provider evidence. Empty means the contract is not GPT Image 2 or the usage
// is valid; any visible cache member requires reconciliation rather than charge.
func ZTAPIGPTImage2UsagePendingReason(raw []byte, contract types.ZTAPIImageProtocolContract) string {
	if !types.IsZTAPIGPTImage2NotReportedUsageProtocol(contract) {
		return ""
	}
	if basecommon.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return "gpt-image-2 usage is malformed"
	}
	var usage any
	if err := basecommon.Unmarshal(raw, &usage); err != nil {
		return "gpt-image-2 usage is malformed"
	}
	if ztapiUsageContainsCacheMember(usage) {
		return "gpt-image-2 usage contains cache metadata outside the frozen protocol"
	}
	fields := []string{
		"input_tokens", "output_tokens", "output_tokens_details.text_tokens", "total_tokens",
		contract.Usage.Fields["text_input"], contract.Usage.Fields["image_input"], contract.Usage.Fields["image_output"],
	}
	values := make([]int64, len(fields))
	for index, field := range fields {
		quantity, ok := exactZTAPIMediaUsageInteger(gjson.GetBytes(raw, field))
		if !ok {
			return fmt.Sprintf("gpt-image-2 usage field %q is missing or malformed", field)
		}
		values[index] = quantity
	}
	input, output, textOutput, total := values[0], values[1], values[2], values[3]
	textInput, imageInput, imageOutput := values[4], values[5], values[6]
	if textInput > math.MaxInt64-imageInput {
		return "gpt-image-2 input usage buckets overflow"
	}
	if textOutput != 0 || input != textInput+imageInput || output != imageOutput ||
		input > math.MaxInt64-output || total != input+output {
		return "gpt-image-2 usage aggregates or output text conflict with image-only billing"
	}
	return ""
}

func ztapiUsageContainsCacheMember(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.Contains(strings.ToLower(key), "cache") || ztapiUsageContainsCacheMember(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if ztapiUsageContainsCacheMember(child) {
				return true
			}
		}
	}
	return false
}

func pendingZTAPIMediaUsageEvidence(evidence ZTAPIMediaUsageEvidence, err error) (ZTAPIMediaUsageEvidence, error) {
	evidence.Pending = true
	evidence.Reason = err.Error()
	return evidence, err
}

func selectZTAPIImageUsageRules(mediaPriceContractJSON string, quantities map[string]int64) (string, map[string]string, error) {
	contract, err := types.ParseZTAPIMediaPriceContract(mediaPriceContractJSON)
	if err != nil {
		return "", nil, fmt.Errorf("frozen ZTAPI media price contract is invalid: %w", err)
	}
	if _, tiered := contract.Rules[0].Conditions["prompt_tokens_tier"]; !tiered {
		ruleIDs := make(map[string]string, len(quantities))
		for dimension := range quantities {
			rule, selectErr := types.SelectZTAPIMediaPriceRule(mediaPriceContractJSON, types.ZTAPIMediaPriceSelector{
				Modality: "image", Conditions: map[string]string{"token_bucket": dimension},
			})
			if selectErr != nil {
				return "", nil, fmt.Errorf("frozen ZTAPI image media price rule is invalid: %w", selectErr)
			}
			ruleIDs[dimension] = rule.ID
		}
		return "", ruleIDs, nil
	}
	tier := "lte_200k"
	if quantities["input_tokens"] > 200000 {
		tier = "gt_200k"
	}
	rule, err := types.SelectZTAPIMediaPriceRule(mediaPriceContractJSON, types.ZTAPIMediaPriceSelector{
		Modality:   "image",
		Conditions: map[string]string{"prompt_tokens_tier": tier},
	})
	if err != nil {
		return "", nil, fmt.Errorf("frozen ZTAPI image media price rule is invalid: %w", err)
	}
	ruleIDs := make(map[string]string, len(quantities))
	for dimension := range quantities {
		ruleIDs[dimension] = rule.ID
	}
	return rule.ID, ruleIDs, nil
}

func validateZTAPIMediaUsageAuthority(info *RelayInfo, handoff *ZTAPIValidatedImageResponse) (types.ZTAPIImageProtocolContract, error) {
	if info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.Modality != "image" || info.ZTAPIPublicationSnapshot.ImageProtocolContract == nil {
		return types.ZTAPIImageProtocolContract{}, errors.New("frozen ZTAPI image contracts are required")
	}
	contract := info.ZTAPIPublicationSnapshot.ImageProtocolContract.Clone()
	sealed, _, err := types.SealZTAPIImageProtocolContract(contract)
	if err != nil || sealed.EvidenceHash != contract.EvidenceHash {
		return types.ZTAPIImageProtocolContract{}, errors.New("frozen ZTAPI image protocol contract is invalid")
	}
	if handoff.ContractVersion != contract.Version || handoff.EvidenceHash != contract.EvidenceHash {
		return types.ZTAPIImageProtocolContract{}, errors.New("ZTAPI image handoff contract identity mismatch")
	}
	mediaRaw := info.ZTAPIPublicationSnapshot.MediaPriceContractJSON
	mediaContract, err := types.ParseZTAPIMediaPriceContract(mediaRaw)
	if err != nil {
		return types.ZTAPIImageProtocolContract{}, fmt.Errorf("frozen ZTAPI media price contract is invalid: %w", err)
	}
	canonical, err := types.CanonicalizeZTAPIMediaPriceContract(mediaRaw)
	if err != nil || canonical != mediaRaw {
		return types.ZTAPIImageProtocolContract{}, errors.New("frozen ZTAPI media price contract is not canonical")
	}
	if contract.Usage.CacheSemantics == "included_in_input" {
		return contract, fmt.Errorf("%w: included cache usage cannot be separated for pricing", ErrZTAPIMediaUsagePending)
	}
	if err := types.ValidateZTAPIImagePriceProtocolCompatibility(mediaContract, contract); err != nil {
		return types.ZTAPIImageProtocolContract{}, fmt.Errorf("frozen ZTAPI image contracts mismatch: %w", err)
	}
	return contract, nil
}

func exactZTAPIMediaUsageInteger(result gjson.Result) (int64, bool) {
	if !result.Exists() || result.Type != gjson.Number {
		return 0, false
	}
	value, err := strconv.ParseInt(result.Raw, 10, 64)
	return value, err == nil && value >= 0
}
