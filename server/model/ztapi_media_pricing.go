package model

import (
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
		if !ok || quoted.PricePolicy != source.PricePolicy || quoted.MediaPriceContractJSON != source.MediaPriceContractJSON {
			return errors.New("pool media price contract does not match the exact quotation")
		}
		return nil
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

func validateZTAPIImagePriceProtocolCompatibility(price ZTAPIMediaPriceContract, protocol types.ZTAPIImageProtocolContract) error {
	return types.ValidateZTAPIImagePriceProtocolCompatibility(price, protocol)
}
