package model

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

//go:embed ztapi_quotation_ab_20260915.json
var ztapiQuotationABJSON []byte

type ZTAPIABQuotationEntry struct {
	ModelName         string                  `json:"model_name"`
	ModelOriginal     string                  `json:"model_original"`
	ModelCode         string                  `json:"model_code"`
	Grade             string                  `json:"grade"`
	ResourceType      string                  `json:"resource_type"`
	QuotedFraction    string                  `json:"quoted_fraction"`
	OfficialPriceText string                  `json:"official_price_text"`
	QuotationCell     string                  `json:"quotation_cell"`
	OfficialPriceCell string                  `json:"official_price_cell"`
	Status            string                  `json:"status,omitempty"`
	Active            bool                    `json:"active"`
	Modality          string                  `json:"modality"`
	PricingBasis      bool                    `json:"pricing_basis"`
	TokenPriceRules   []ZTAPIABTokenPriceRule `json:"token_price_rules"`
	PricingBlocker    string                  `json:"pricing_blocker"`
}

type ZTAPIABTokenPriceRule struct {
	Conditions    []string          `json:"conditions"`
	Currency      string            `json:"currency"`
	NotApplicable []string          `json:"not_applicable"`
	TemporaryFree []string          `json:"temporary_free"`
	Cost          map[string]string `json:"cost"`
	Sale          map[string]string `json:"sale"`
}

type ZTAPIABQuotationManifest struct {
	WorkbookSHA256 string                  `json:"workbook_sha256"`
	Entries        []ZTAPIABQuotationEntry `json:"entries"`
}

func (quote ZTAPIABQuotationManifest) EnterpriseBasis(modelName string) ([]ZTAPIABQuotationEntry, error) {
	var rows []ZTAPIABQuotationEntry
	for _, entry := range quote.Entries {
		if strings.EqualFold(entry.ModelName, modelName) && entry.Grade == "A" && entry.Active {
			rows = append(rows, entry)
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no active enterprise A quotation for %q", modelName)
	}
	return rows, nil
}

func (quote ZTAPIABQuotationManifest) PricingBasis(modelName string) ([]ZTAPIABQuotationEntry, ZTAPIPricePolicy, error) {
	if rows, err := quote.EnterpriseBasis(modelName); err == nil {
		return rows, ZTAPIPricePolicyEnterprise20Margin, nil
	}
	var rows []ZTAPIABQuotationEntry
	for _, entry := range quote.Entries {
		if strings.EqualFold(entry.ModelName, modelName) && entry.Grade == "B" && entry.Active {
			rows = append(rows, entry)
		}
	}
	if len(rows) == 0 {
		return nil, "", fmt.Errorf("no active A or B quotation for %q", modelName)
	}
	return rows, ZTAPIPricePolicyPool30Margin, nil
}

func (quote ZTAPIABQuotationManifest) PublishablePricingBasis(modelName string) ([]ZTAPIABQuotationEntry, ZTAPIPricePolicy, error) {
	rows, policy, err := quote.PricingBasis(modelName)
	if err != nil {
		return nil, "", err
	}
	for _, row := range rows {
		if !row.PricingBasis || row.PricingBlocker != "" || row.Modality != "text" || len(row.TokenPriceRules) == 0 {
			return nil, "", fmt.Errorf("quotation %s is not publishable: %s", row.QuotationCell, row.PricingBlocker)
		}
	}
	return rows, policy, nil
}

func ZTAPIQuotationABEntries() (ZTAPIABQuotationManifest, error) {
	var quote ZTAPIABQuotationManifest
	if err := json.Unmarshal(ztapiQuotationABJSON, &quote); err != nil {
		return quote, err
	}
	if len(quote.WorkbookSHA256) != 64 || len(quote.Entries) != 72 {
		return quote, errors.New("ZTAPI A/B quotation source is incomplete")
	}
	for _, entry := range quote.Entries {
		if entry.Grade != "A" && entry.Grade != "B" {
			return quote, fmt.Errorf("unsupported quotation grade %q", entry.Grade)
		}
		fraction, err := decimal.NewFromString(entry.QuotedFraction)
		if err != nil || !fraction.IsPositive() ||
			strings.TrimSpace(entry.ModelCode) == "" || strings.TrimSpace(entry.ModelName) == "" ||
			strings.TrimSpace(entry.OfficialPriceText) == "" {
			return quote, errors.New("ZTAPI A/B quotation row is incomplete")
		}
		if entry.PricingBasis {
			if !entry.Active || (len(entry.TokenPriceRules) == 0 && entry.PricingBlocker == "") ||
				(len(entry.TokenPriceRules) != 0 && entry.PricingBlocker != "") {
				return quote, fmt.Errorf("quotation %s pricing basis is incomplete", entry.QuotationCell)
			}
		}
	}
	return quote, nil
}
