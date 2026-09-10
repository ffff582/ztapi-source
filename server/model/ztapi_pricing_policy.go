package model

import (
	"errors"
	"strings"

	"github.com/shopspring/decimal"
)

type ZTAPIPricePolicy string

const (
	ZTAPIPricePolicyEnterprise40Margin ZTAPIPricePolicy = "enterprise_40_margin"
	ZTAPIPricePolicyPool60Margin       ZTAPIPricePolicy = "pool_60_margin"
)

var (
	ztapiEnterpriseSaleCostShare = decimal.RequireFromString("0.60")
	ztapiPoolSaleCostShare       = decimal.RequireFromString("0.40")
)

func CalculateZTAPISalePriceForPolicy(cost decimal.Decimal, policy ZTAPIPricePolicy) (decimal.Decimal, error) {
	switch policy {
	case ZTAPIPricePolicyEnterprise40Margin:
		return cost.Div(ztapiEnterpriseSaleCostShare).Round(10), nil
	case ZTAPIPricePolicyPool60Margin:
		return cost.Div(ztapiPoolSaleCostShare).Round(10), nil
	default:
		return decimal.Zero, errors.New("unsupported ZTAPI price policy")
	}
}

func ValidateZTAPIPricePolicy(resourceType string, policy ZTAPIPricePolicy) error {
	resourceType = strings.TrimSpace(resourceType)
	switch policy {
	case ZTAPIPricePolicyEnterprise40Margin:
		if resourceType == "enterprise" || resourceType == "official" || resourceType == "original_resource" {
			return nil
		}
	case ZTAPIPricePolicyPool60Margin:
		if resourceType == "pool" {
			return nil
		}
	}
	return errors.New("ZTAPI price policy does not match resource type")
}
