// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/types"
)

type result struct {
	RetailJSON string                             `json:"image_sale_contract_json"`
	Items      []model.ZTAPIReferralPriceGateItem `json:"items"`
}

// This offline helper produces quotes, not supplier debit confirmations or a
// model-mapping/platform-fee authorization. It cannot connect to any service.
func review(sourceCostJSON string) (result, error) {
	output := result{RetailJSON: types.ZTAPIImageSalePartial20261109JSON(), Items: []model.ZTAPIReferralPriceGateItem{}}
	for _, operation := range []string{"generation", "edit"} {
		quote, err := types.QuoteImageSaleReferralCosts(output.RetailJSON, sourceCostJSON, operation)
		if err != nil {
			return result{}, err
		}
		for _, item := range quote.Items {
			evidence, err := types.BuildZTAPIImageSaleReferralEvidence(quote, operation, item.Key)
			if err != nil {
				return result{}, err
			}
			raw, err := common.Marshal(evidence)
			if err != nil {
				return result{}, err
			}
			dimension, err := types.ZTAPIImageSaleReferralDimension(operation, item.Key)
			if err != nil {
				return result{}, err
			}
			output.Items = append(output.Items, model.ZTAPIReferralPriceGateItem{
				Key: "gpt-image-2/openai_compatible/channel:1/" + dimension, CustomerRevenue: item.CustomerRevenueUSD,
				BonusRate: "0.05", GroupMultiplier: "1", CashMultiplier: "1", Evidence: string(raw), CostKnown: true, UsageKnown: true,
			})
		}
	}
	if len(output.Items) != 3 {
		return result{}, errors.New("partial image cost review must contain exactly three items")
	}
	sort.Slice(output.Items, func(i, j int) bool { return output.Items[i].Key < output.Items[j].Key })
	return output, nil
}

func main() {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		fmt.Fprintln(os.Stderr, "bounded procurement input required")
		os.Exit(1)
	}
	output, err := review(string(raw))
	if err != nil {
		fmt.Fprintln(os.Stderr, "partial image procurement review failed")
		os.Exit(1)
	}
	raw, err = common.Marshal(output)
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(string(raw))
}
