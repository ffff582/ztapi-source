package model

import "github.com/QuantumNous/new-api/types"

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

func validateZTAPIImagePriceProtocolCompatibility(price ZTAPIMediaPriceContract, protocol types.ZTAPIImageProtocolContract) error {
	return types.ValidateZTAPIImagePriceProtocolCompatibility(price, protocol)
}
