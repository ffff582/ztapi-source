// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/support"
	"github.com/shopspring/decimal"
)

const (
	SupportSourceAvailable     = "available"
	SupportSourceUnavailable   = "unavailable"
	SupportSourceNotConfigured = "not_configured"
)

// SupportUsageReader reads only the independent support store, never wallet data.
type SupportUsageReader func(context.Context, time.Time, time.Time) ([]support.FinancialUsage, error)

func supportAmount(raw *string) (decimal.Decimal, bool) {
	if raw == nil || !amountPattern.MatchString(*raw) {
		return zero, false
	}
	value, err := decimal.NewFromString(*raw)
	return value, err == nil && !value.IsNegative()
}

func validSupportUsage(u support.FinancialUsage, cutoff time.Time) bool {
	if strings.TrimSpace(u.CallID) == "" || u.ChannelID <= 0 || strings.TrimSpace(u.Supplier) == "" ||
		strings.TrimSpace(u.Model) == "" || u.Purpose != customerServicePurpose || !u.Dispatched ||
		u.CompletedAt == nil || u.CompletedAt.Before(u.StartedAt) || u.CompletedAt.After(cutoff) ||
		u.InputTokens == nil || u.OutputTokens == nil || u.CacheReadTokens == nil || *u.InputTokens < 0 || *u.OutputTokens < 0 ||
		(u.InputTokenScope != "inclusive" && u.InputTokenScope != "uncached") {
		return false
	}
	return *u.CacheReadTokens >= 0 &&
		(u.InputTokenScope != "inclusive" || *u.CacheReadTokens <= *u.InputTokens)
}

// Support calls are a read-only projection, not synthetic financial facts or
// settlements. Frozen quote amounts already include all upstream conversions.
func (r *replay) buildSupportUsage(s Snapshot, totalCost *decimal.Decimal, complete *bool, includedActual map[int]decimal.Decimal) {
	status := s.SupportSourceStatus
	if status == "" {
		status = SupportSourceNotConfigured
	}
	r.report.SupportSourceStatus = status
	if status != SupportSourceAvailable && status != SupportSourceNotConfigured {
		r.report.SupportSourceStatus = SupportSourceUnavailable
		r.issue("support_source_unavailable", "support", 0)
		*complete = false
		r.report.EstimateComplete = false
		r.report.EstimatedCost, r.report.EstimatedGrossProfit = nil, nil
		return
	}
	usages := map[string]support.FinancialUsage{}
	conflicts := map[string]bool{}
	for i, u := range s.SupportUsages {
		at := u.StartedAt
		if u.CompletedAt != nil {
			at = *u.CompletedAt
		}
		if !r.inMonth(at) {
			continue
		}
		key := u.CallID
		if key == "" {
			key = fmt.Sprintf("invalid:%d", i)
		}
		if old, exists := usages[key]; exists && !reflect.DeepEqual(old, u) {
			conflicts[key] = true
		} else {
			usages[key] = u
		}
	}
	if len(usages) == 0 {
		return
	}
	keys := make([]string, 0, len(usages))
	for key := range usages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	confirmed, estimated, credits, estimateCost := zero, zero, zero, zero
	estimateComplete := true
	for _, key := range keys {
		u := usages[key]
		row := TestCostRow{SourceRef: "support:" + key, CallID: u.CallID, AttemptAt: u.StartedAt, Model: u.Model,
			TestCost: TestCost{Purpose: customerServicePurpose, RequestID: u.CallID, ChannelID: u.ChannelID,
				InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, InputTokenScope: u.InputTokenScope,
				CacheReadTokens: u.CacheReadTokens, Status: "unconfirmed", Coverage: "unknown", Currency: "USDT", Rate: "1"}}
		if !conflicts[key] && !u.Dispatched && u.Outcome == "not_dispatched" && u.ChannelID > 0 && u.Model != "" && u.Supplier != "" &&
			u.Purpose == customerServicePurpose && u.CompletedAt != nil && !u.CompletedAt.Before(u.StartedAt) && !u.CompletedAt.After(r.end) &&
			u.QuoteCostU != nil && *u.QuoteCostU == "0" && u.QuoteEvidence == "local_dispatch_log:not_dispatched" {
			row.Status, row.Amount, row.NetUSDT, row.Coverage = "confirmed", "0", "0.000000", "extra"
			row.EvidenceType, row.Evidence = "local_execution", u.QuoteEvidence
			r.report.CustomerServiceCosts = append(r.report.CustomerServiceCosts, row)
			continue
		}
		valid := !conflicts[key] && validSupportUsage(u, r.end)
		actual, hasActual := supportAmount(u.ActualCostU)
		quote, hasQuote := supportAmount(u.QuoteCostU)
		hasActual = hasActual && strings.TrimSpace(u.ActualEvidence) != ""
		hasQuote = hasQuote && strings.TrimSpace(u.QuoteEvidence) != ""
		if valid && (hasActual || hasQuote) {
			value := quote
			row.Status, row.EvidenceType, row.Evidence = "estimate", "quote", u.QuoteEvidence
			if hasActual {
				value = actual
				row.Status, row.EvidenceType, row.Evidence = "confirmed", "supplier_statement", u.ActualEvidence
				confirmed = confirmed.Add(value)
			} else {
				estimated = estimated.Add(value)
			}
			row.Amount, row.NetUSDT = value.String(), fixed(value)
			estimateCost = estimateCost.Add(value)
			if credit, ok := supportAmount(u.UpstreamCredit); ok {
				credits = credits.Add(credit)
				row.NetCredit = credit.StringFixed(8)
			}
			for _, bill := range r.report.Costs {
				if bill.ChannelID == u.ChannelID && bill.Complete && strings.TrimSpace(bill.Evidence) != "" {
					row.Coverage, row.CoverageEvidence = "included", bill.Evidence
					break
				}
			}
			if row.Coverage == "included" && hasActual {
				includedActual[u.ChannelID] = includedActual[u.ChannelID].Add(actual)
			} else if row.Coverage != "included" {
				if hasActual {
					// Without a complete channel invoice, only an evidenced debit
					// can be recorded as actual. A partial invoice may overlap it.
					partial := false
					for _, bill := range r.report.Costs {
						if bill.ChannelID == u.ChannelID {
							partial = true
						}
					}
					if !partial {
						*totalCost = totalCost.Add(actual)
						row.Coverage = "extra"
					} else {
						*complete = false
					}
				} else {
					*complete = false
				}
			}
		} else {
			*complete, estimateComplete = false, false
			r.issue("support_usage_unconfirmed", row.SourceRef, 0)
		}
		r.report.CustomerServiceCosts = append(r.report.CustomerServiceCosts, row)
	}
	for _, bill := range r.report.Costs {
		if bill.Complete {
			gross, _ := money(bill.Gross, bill.Currency, bill.Rate)
			refund, _ := money(bill.Refund, bill.Currency, bill.Rate)
			net := gross.Sub(refund)
			if includedActual[bill.ChannelID].GreaterThan(net) {
				*complete = false
				r.issue("support_cost_coverage_conflict", fmt.Sprintf("channel:%d", bill.ChannelID), 0)
			}
		}
	}
	add := func(old string, value decimal.Decimal, places int32) string {
		base, _ := decimal.NewFromString(old)
		return base.Add(value).StringFixed(places)
	}
	r.report.CustomerServiceConfirmedTotal = add(r.report.CustomerServiceConfirmedTotal, confirmed, 6)
	r.report.CustomerServiceEstimateTotal = add(r.report.CustomerServiceEstimateTotal, estimated, 6)
	r.report.CustomerServiceXcloudCreditTotal = add(r.report.CustomerServiceXcloudCreditTotal, credits, 8)
	base := zero
	baseProfit := r.report.EstimatedGrossProfit
	hasBase := r.report.EstimatedCost != nil
	if r.report.EstimatedCost != nil {
		base, _ = decimal.NewFromString(*r.report.EstimatedCost)
		estimateComplete = estimateComplete && r.report.EstimateComplete
	} else {
		// Never claim a whole-report estimate when customer usage has no priced
		// base. Support-only reports do have a known zero customer-cost base.
		for _, settlement := range r.settlements {
			if settlement.Status == "settled" && r.inMonth(time.Unix(settlement.CompletedAt, 0)) {
				estimateComplete = false
			}
		}
	}
	r.report.EstimateComplete = estimateComplete
	r.report.EstimatedCost, r.report.EstimatedGrossProfit = nil, nil
	if estimateComplete {
		value := base.Add(estimateCost)
		r.report.EstimatedCost = stringPtr(fixed(value))
		if hasBase && baseProfit != nil {
			profit, err := decimal.NewFromString(*baseProfit)
			if err == nil {
				r.report.EstimatedGrossProfit = stringPtr(fixed(profit.Sub(estimateCost)))
			}
		} else if !hasBase && !r.unknownRevenue {
			r.report.EstimatedGrossProfit = stringPtr(fixed(r.known.Sub(value)))
		}
	}
}
