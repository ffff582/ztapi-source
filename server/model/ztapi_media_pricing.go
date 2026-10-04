package model

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

const ZTAPIMediaBillingUnitUSDPerMillionTokens = types.ZTAPIMediaBillingUnitUSDPerMillionTokens

type ZTAPIMediaPriceContract = types.ZTAPIMediaPriceContract
type ZTAPIMediaPriceRule = types.ZTAPIMediaPriceRule
type ZTAPIMediaPriceSelector = types.ZTAPIMediaPriceSelector

func ValidateZTAPIMediaPriceContract(raw string) error {
	return types.ValidateZTAPIMediaPriceContract(raw)
}

func SelectZTAPIMediaPriceRule(raw string, request ZTAPIMediaPriceSelector) (ZTAPIMediaPriceRule, error) {
	return types.SelectZTAPIMediaPriceRule(raw, request)
}

func canonicalizeZTAPIMediaPriceContract(raw string) (string, error) {
	return types.CanonicalizeZTAPIMediaPriceContract(raw)
}

func parseZTAPIMediaPriceContract(raw string) (ZTAPIMediaPriceContract, error) {
	return types.ParseZTAPIMediaPriceContract(raw)
}

func validateZTAPIMediaPriceContractPolicy(source *ZTAPIModelPriceSource) error {
	if source == nil || source.MediaPriceContractJSON == "" {
		return nil
	}
	policy := ZTAPIPricePolicy(source.PricePolicy)
	if policy == ZTAPIPricePolicyPoolOfficial80 {
		quoted, ok := ztapiMediaPriceRowFromManifest(ztapiQuotation, source.SourceModel)
		if !ok || quoted.PricePolicy != source.PricePolicy {
			return errors.New("pool media price contract does not match the exact quotation")
		}
		if quoted.MediaPriceContractJSON == source.MediaPriceContractJSON {
			return nil
		}
		if source.SourceModel == "gpt-image-2" {
			derived, err := deriveZTAPIGPTImage2OperationAwarePriceContract(quoted.MediaPriceContractJSON)
			if err == nil && derived == source.MediaPriceContractJSON {
				return nil
			}
		}
		return errors.New("pool media price contract does not match the exact quotation")
	}
	share := decimal.Zero
	switch policy {
	case ZTAPIPricePolicyEnterprise40Margin:
		share = decimal.RequireFromString("0.60")
	case ZTAPIPricePolicyEnterprise20Margin:
		share = decimal.RequireFromString("0.80")
	default:
		return errors.New("media price contract uses an unsupported price policy")
	}
	contract, err := parseZTAPIMediaPriceContract(source.MediaPriceContractJSON)
	if err != nil {
		return err
	}
	for _, rule := range contract.Rules {
		for dimension, rawCost := range rule.CostUSD {
			cost := decimal.RequireFromString(rawCost)
			sale := decimal.RequireFromString(rule.SaleUSD[dimension])
			if !sale.Equal(cost.Div(share).Round(10)) {
				return fmt.Errorf("media sale price for %s does not match %s", dimension, policy)
			}
		}
	}
	return nil
}

// deriveZTAPIGPTImage2OperationAwarePriceContract keeps the quotation's five
// frozen token buckets intact while binding the same prices to both admitted
// image operations. The base quotation remains generation-only; this derived
// form is used only after a V3 edit-capable provider contract is verified.
func deriveZTAPIGPTImage2OperationAwarePriceContract(raw string) (string, error) {
	base, err := parseZTAPIMediaPriceContract(raw)
	if err != nil || base.Modality != ZTAPIModalityImage || len(base.Rules) != 5 {
		return "", errors.New("gpt-image-2 quotation must contain the five frozen image buckets")
	}
	derived := types.ZTAPIMediaPriceContract{Version: base.Version, Modality: base.Modality, SaleMultiplier: base.SaleMultiplier}
	for _, operation := range []string{"generation", "edit"} {
		for _, baseRule := range base.Rules {
			bucket, ok := baseRule.Conditions["token_bucket"]
			if !ok || len(baseRule.Conditions) != 1 {
				return "", errors.New("gpt-image-2 quotation is not a generation-only token bucket matrix")
			}
			clone := func(values map[string]string) map[string]string {
				result := make(map[string]string, len(values))
				for key, value := range values {
					result[key] = value
				}
				return result
			}
			derived.Rules = append(derived.Rules, types.ZTAPIMediaPriceRule{
				ID:          operation + "_" + bucket,
				Conditions:  map[string]string{"image_operation": operation, "token_bucket": bucket},
				BillingUnit: baseRule.BillingUnit,
				CostUSD:     clone(baseRule.CostUSD), SaleUSD: clone(baseRule.SaleUSD), SourceCells: clone(baseRule.SourceCells),
			})
		}
	}
	encoded, err := json.Marshal(derived)
	if err != nil {
		return "", err
	}
	return canonicalizeZTAPIMediaPriceContract(string(encoded))
}

func validateZTAPIImagePriceProtocolCompatibility(price ZTAPIMediaPriceContract, protocol types.ZTAPIImageProtocolContract) error {
	return types.ValidateZTAPIImagePriceProtocolCompatibility(price, protocol)
}
