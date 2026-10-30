package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
)

type ZTAPIPricePolicy string

const (
	ZTAPIPricePolicyEnterprise40Margin        ZTAPIPricePolicy = "enterprise_40_margin"
	ZTAPIPricePolicyPool60Margin              ZTAPIPricePolicy = "pool_60_margin"
	ZTAPIPricePolicyEnterprise20Margin        ZTAPIPricePolicy = "enterprise_20_margin"
	ZTAPIPricePolicyPool30Margin              ZTAPIPricePolicy = "pool_30_margin"
	ZTAPIPricePolicyPoolOfficial80            ZTAPIPricePolicy = "pool_official_80"
	ZTAPIPricePolicyPoolOfficial78Sep2026     ZTAPIPricePolicy = "pool_official_78_20260929"
	ZTAPIPricePolicyQuoteFrozenRetail20261019 ZTAPIPricePolicy = "quote_frozen_retail_20261019"
	ZTAPIPricePolicySupplierAdmission20261019 ZTAPIPricePolicy = types.ZTAPISupplierAdmissionPricePolicy20261019
	ZTAPIPricePolicyXcloudGLM53               ZTAPIPricePolicy = "xcloud_glm53_20261030"
	ZTAPIPricePolicyCashEnterprise20261030    ZTAPIPricePolicy = "cash_enterprise_20_20261030"
	ZTAPIPricePolicyCashPool20261030          ZTAPIPricePolicy = "cash_pool_30_20261030"
)

var (
	ztapiEnterpriseSaleCostShare = decimal.RequireFromString("0.60")
	ztapiPoolSaleCostShare       = decimal.RequireFromString("0.40")
	ztapiEnterprise20CostShare   = decimal.RequireFromString("0.80")
	ztapiPool30CostShare         = decimal.RequireFromString("0.70")
	ztapiPoolOfficialSaleShare   = decimal.RequireFromString("0.80")
)

func ztapiNormalizedSaleMultiplier(raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.NewFromInt(1), nil
	}
	multiplier, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !multiplier.IsPositive() || multiplier.GreaterThan(decimal.NewFromInt(1)) {
		return decimal.Zero, errors.New("ZTAPI sale multiplier must be greater than zero and at most one")
	}
	return multiplier.Round(10), nil
}

func CalculateZTAPISalePriceForPolicy(cost decimal.Decimal, policy ZTAPIPricePolicy) (decimal.Decimal, error) {
	if isZTAPICashMarginPolicy20261030(string(policy)) && cost.IsNegative() {
		return decimal.Zero, errors.New("cash margin cost cannot be negative")
	}
	switch policy {
	case ZTAPIPricePolicyCashEnterprise20261030:
		return decimal.RequireFromString(ztapiCashSale20261030(cost, decimal.NewFromInt(1), "0.20", 10)), nil
	case ZTAPIPricePolicyCashPool20261030:
		return decimal.RequireFromString(ztapiCashSale20261030(cost, decimal.NewFromInt(1), "0.30", 10)), nil
	case ZTAPIPricePolicyEnterprise40Margin:
		return cost.Div(ztapiEnterpriseSaleCostShare).Round(10), nil
	case ZTAPIPricePolicyPool60Margin:
		return cost.Div(ztapiPoolSaleCostShare).Round(10), nil
	case ZTAPIPricePolicyEnterprise20Margin:
		return cost.Div(ztapiEnterprise20CostShare).Round(10), nil
	case ZTAPIPricePolicyXcloudGLM53:
		return cost.Mul(decimal.RequireFromString("1.05")).Div(ztapiEnterprise20CostShare).Round(10), nil
	case ZTAPIPricePolicyPool30Margin:
		return cost.Div(ztapiPool30CostShare).Round(10), nil
	default:
		return decimal.Zero, errors.New("unsupported ZTAPI price policy")
	}
}

func CalculateZTAPIPoolSalePrice(officialPrice decimal.Decimal) (decimal.Decimal, error) {
	if officialPrice.IsNegative() {
		return decimal.Zero, errors.New("official price cannot be negative")
	}
	return officialPrice.Mul(ztapiPoolOfficialSaleShare).Round(10), nil
}

func ValidateZTAPIPricePolicy(resourceType string, policy ZTAPIPricePolicy) error {
	resourceType = strings.TrimSpace(resourceType)
	switch policy {
	case ZTAPIPricePolicyCashEnterprise20261030:
		if resourceType == "enterprise" {
			return nil
		}
	case ZTAPIPricePolicyCashPool20261030:
		if resourceType == "enterprise" || resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicyXcloudGLM53:
		if resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicySupplierAdmission20261019:
		if resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicyQuoteFrozenRetail20261019:
		if resourceType == "pool" || resourceType == "enterprise" {
			return nil
		}
	case ZTAPIPricePolicyEnterprise40Margin:
		if resourceType == "enterprise" || resourceType == "official" || resourceType == "original_resource" {
			return nil
		}
	case ZTAPIPricePolicyEnterprise20Margin:
		if resourceType == "enterprise" || resourceType == "official" || resourceType == "original_resource" {
			return nil
		}
	case ZTAPIPricePolicyPool60Margin:
		if resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicyPool30Margin:
		if resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicyPoolOfficial80:
		if resourceType == "pool" {
			return nil
		}
	case ZTAPIPricePolicyPoolOfficial78Sep2026:
		if resourceType == "pool" {
			return nil
		}
	}
	return errors.New("ZTAPI price policy does not match resource type")
}
