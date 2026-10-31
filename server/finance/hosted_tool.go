// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// HostedToolRow is a subset of wallet revenue and test costs, not another cost posting.
type HostedToolRow struct {
	Tool                     string  `json:"tool"`
	PublicModel              string  `json:"public_model"`
	ChannelID                int     `json:"channel_id"`
	Unit                     string  `json:"unit"`
	SuccessfulCalls          int     `json:"successful_calls"`
	ZeroChargeCalls          int     `json:"zero_charge_calls"`
	UnknownCalls             int     `json:"unknown_calls"`
	TestSuccessfulCalls      int     `json:"test_successful_calls"`
	TestUnknownCalls         int     `json:"test_unknown_calls"`
	ChargedQuota             int64   `json:"charged_quota"`
	CustomerChargedQuota     int64   `json:"customer_charged_quota"`
	TestChargedQuota         int64   `json:"test_charged_quota"`
	CustomerKnownRevenueUSDT string  `json:"customer_known_revenue_usdt"`
	CustomerRevenueUSDT      *string `json:"customer_revenue_usdt"`
	CustomerQuotedSaleCNY    string  `json:"customer_quoted_sale_cny"`
	QuotedCostCNY            string  `json:"quoted_cost_cny"`
	QuotedCostUSDT           string  `json:"quoted_cost_usdt"`
	TestQuotedCostCNY        string  `json:"test_quoted_cost_cny"`
	TestQuotedCostUSDT       string  `json:"test_quoted_cost_usdt"`
	TestConfirmedCostUSDT    string  `json:"test_confirmed_cost_usdt"`
	TestEstimateCostUSDT     string  `json:"test_estimate_cost_usdt"`
	QuotedCostIsEstimate     bool    `json:"quoted_cost_is_estimate"`
	QuoteEstimateComplete    bool    `json:"quote_estimate_complete"`
}

type hostedToolQuote struct {
	channel             int
	sale, cost, cnyPerU decimal.Decimal
}

type hostedToolCash struct {
	quota        int64
	customerCash decimal.Decimal
	unknown      bool
}

func hostedToolName(s Settlement) string {
	switch s.PublicModel {
	case "ztapi-tool-search":
		return "search"
	case "ztapi-tool-fetch":
		return "fetch"
	}
	return ""
}

func parseHostedToolQuote(s Settlement, channel int) (hostedToolQuote, error) {
	invalid := errors.New("invalid hosted tool quote snapshot")
	if len(s.PriceSnapshotJSON) > 64<<10 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(s.PriceSnapshotJSON)) != nil {
		return hostedToolQuote{}, invalid
	}
	var fields map[string]json.RawMessage
	if common.UnmarshalJsonStr(s.PriceSnapshotJSON, &fields) != nil || fields == nil {
		return hostedToolQuote{}, invalid
	}
	var version, channelID, fxPolicyID int
	var quotaPerUnit int64
	var kind, provider, tool, unit, currency, sale, cost, fx, credential, parent string
	values := map[string]any{"schema_version": &version, "billing_kind": &kind, "provider": &provider,
		"tool": &tool, "unit": &unit, "currency": &currency, "sale_per_call_cny": &sale,
		"quoted_cost_per_call_cny": &cost, "cny_per_u": &fx, "quota_per_unit": &quotaPerUnit,
		"fx_policy_id": &fxPolicyID, "channel_id": &channelID, "credential_version": &credential, "parent_request_id": &parent}
	for key, value := range values {
		if common.Unmarshal(fields[key], value) != nil {
			return hostedToolQuote{}, invalid
		}
		for supplied := range fields {
			if supplied != key && strings.EqualFold(supplied, key) {
				return hostedToolQuote{}, invalid
			}
		}
	}
	if version != 1 || kind != "hosted_tool" || provider != "deepseek_official" || tool != hostedToolName(s) || unit != "call" || currency != "CNY" ||
		channelID <= 0 || (channel > 0 && channelID != channel) || fxPolicyID <= 0 || quotaPerUnit <= 0 || strings.TrimSpace(credential) == "" || strings.TrimSpace(parent) == "" {
		return hostedToolQuote{}, invalid
	}
	quotedSale, saleErr := money(sale, "CNY", "1")
	quotedCost, costErr := money(cost, "CNY", "1")
	_, fxErr := money("1", "CNY", fx)
	cnyPerU, _ := decimal.NewFromString(fx)
	if saleErr != nil || costErr != nil || fxErr != nil || !quotedSale.Equal(decimal.RequireFromString("0.01")) || !quotedCost.Equal(decimal.RequireFromString("0.005")) {
		return hostedToolQuote{}, invalid
	}
	return hostedToolQuote{channel: channelID, sale: quotedSale, cost: quotedCost, cnyPerU: cnyPerU}, nil
}

func (r *replay) recordHostedToolCash(s Settlement, b basis, sign int64) {
	if hostedToolName(s) == "" {
		return
	}
	if r.hostedToolCash == nil {
		r.hostedToolCash = map[string]hostedToolCash{}
	}
	value := r.hostedToolCash[s.RequestID]
	value.quota += sign * b.quota.IntPart()
	if !r.testRequests[s.RequestID] {
		value.customerCash = value.customerCash.Add(b.cash.Mul(decimal.NewFromInt(sign)))
		value.unknown = value.unknown || b.unverified
	}
	r.hostedToolCash[s.RequestID] = value
}

func (r *replay) buildHostedToolReport(snapshot Snapshot) {
	type key struct {
		channel int
		tool    string
	}
	type totals struct {
		row                                      HostedToolRow
		cash, sale, cost, costU, testCost, testU decimal.Decimal
		testConfirmed, testEstimate              decimal.Decimal
		unknownRevenue                           bool
	}
	rows := map[key]*totals{}
	byRequest := map[string]*totals{}
	requestCounts, settlementCounts := map[string]int{}, map[uint]int{}
	for _, settlement := range snapshot.Settlements {
		requestCounts[settlement.RequestID]++
		settlementCounts[settlement.ID]++
	}
	type attemptKey struct {
		settlement uint
		number     int
	}
	attempts := map[attemptKey][]int{}
	for _, attempt := range snapshot.Attempts {
		key := attemptKey{attempt.SettlementID, attempt.Attempt}
		attempts[key] = append(attempts[key], attempt.ChannelID)
		all := attemptKey{settlement: attempt.SettlementID}
		attempts[all] = append(attempts[all], attempt.ChannelID)
	}
	testCostRequests := map[string]bool{}
	for _, cost := range r.report.TestCosts {
		testCostRequests[cost.RequestID] = r.testRequests[cost.RequestID]
	}
	for request, settlement := range r.settlements {
		tool := hostedToolName(settlement)
		if tool == "" {
			continue
		}
		cash, cashInMonth := r.hostedToolCash[request]
		at := settlement.CreatedAt
		final := settlement.Status == "released" || (settlement.Status == "settled" && settlement.CompletedAt > 0 && time.Unix(settlement.CompletedAt, 0).Before(r.end))
		if final && settlement.CompletedAt > 0 {
			at = time.Unix(settlement.CompletedAt, 0)
		}
		callInMonth := !at.IsZero() && r.inMonth(at)
		if !callInMonth && !cashInMonth && !testCostRequests[request] {
			continue
		}
		channel := 0
		channels := attempts[attemptKey{settlement.ID, settlement.FinalAttempt}]
		if len(channels) == 1 {
			channel = channels[0]
		}
		lineage := channel > 0 && settlement.ID > 0 && requestCounts[request] == 1 && settlementCounts[settlement.ID] == 1
		quote, quoteErr := parseHostedToolQuote(settlement, channel)
		if quoteErr == nil {
			channel = quote.channel
		}
		group := key{channel: channel, tool: tool}
		entry := rows[group]
		if entry == nil {
			entry = &totals{row: HostedToolRow{Tool: tool, PublicModel: settlement.PublicModel, ChannelID: channel, Unit: "call", QuotedCostIsEstimate: true, QuoteEstimateComplete: true}}
			rows[group] = entry
		}
		byRequest[request] = entry
		if quoteErr != nil {
			entry.row.QuoteEstimateComplete = false
			r.estimateIssue("hosted_tool_snapshot_invalid", request, settlement.UserID)
		}
		if !lineage {
			entry.row.QuoteEstimateComplete = false
			r.estimateIssue("hosted_tool_lineage_missing", request, settlement.UserID)
		}
		entry.row.ChargedQuota += cash.quota
		if r.testRequests[request] {
			entry.row.TestChargedQuota += cash.quota
		} else {
			entry.row.CustomerChargedQuota += cash.quota
			entry.cash = entry.cash.Add(cash.customerCash)
			entry.unknownRevenue = entry.unknownRevenue || cash.unknown
		}
		if !callInMonth {
			continue
		}
		switch {
		case !final || settlement.ChargedQuota < 0 || !lineage:
			entry.row.UnknownCalls++
			entry.row.QuoteEstimateComplete = false
			if r.testRequests[request] {
				entry.row.TestUnknownCalls++
			}
		case settlement.Status == "released" || settlement.ChargedQuota == 0:
			entry.row.ZeroChargeCalls++
		default:
			entry.row.SuccessfulCalls++
			if r.testRequests[request] {
				entry.row.TestSuccessfulCalls++
			}
			if quoteErr == nil {
				quotedU := quote.cost.DivRound(quote.cnyPerU, 24)
				entry.cost = entry.cost.Add(quote.cost)
				entry.costU = entry.costU.Add(quotedU)
				if r.testRequests[request] {
					entry.testCost = entry.testCost.Add(quote.cost)
					entry.testU = entry.testU.Add(quotedU)
				} else {
					entry.sale = entry.sale.Add(quote.sale)
				}
			}
		}
	}
	// TestCost already handles immutable revisions and monthly invoice coverage.
	for _, cost := range r.report.TestCosts {
		entry := byRequest[cost.RequestID]
		if entry == nil || !r.testRequests[cost.RequestID] || cost.ChannelID != entry.row.ChannelID || cost.NetUSDT == "" {
			continue
		}
		currency := cost.Currency
		if currency == "XCLOUD_CREDIT" {
			currency = "USD"
		}
		amount, amountErr := money(cost.Amount, currency, cost.Rate)
		refund, refundErr := money(cost.Refund, currency, cost.Rate)
		if amountErr != nil || refundErr != nil {
			continue
		}
		net := amount.Sub(refund)
		if cost.Status == "confirmed" {
			entry.testConfirmed = entry.testConfirmed.Add(net)
		} else {
			entry.testEstimate = entry.testEstimate.Add(net)
		}
	}
	for _, entry := range rows {
		row := entry.row
		row.CustomerKnownRevenueUSDT = fixed(entry.cash)
		if !entry.unknownRevenue {
			row.CustomerRevenueUSDT = stringPtr(row.CustomerKnownRevenueUSDT)
		}
		row.CustomerQuotedSaleCNY, row.QuotedCostCNY, row.QuotedCostUSDT = fixed(entry.sale), fixed(entry.cost), fixed(entry.costU)
		row.TestQuotedCostCNY, row.TestQuotedCostUSDT = fixed(entry.testCost), fixed(entry.testU)
		row.TestConfirmedCostUSDT, row.TestEstimateCostUSDT = fixed(entry.testConfirmed), fixed(entry.testEstimate)
		r.report.HostedTools = append(r.report.HostedTools, row)
	}
	sort.Slice(r.report.HostedTools, func(i, j int) bool {
		if r.report.HostedTools[i].ChannelID != r.report.HostedTools[j].ChannelID {
			return r.report.HostedTools[i].ChannelID < r.report.HostedTools[j].ChannelID
		}
		return r.report.HostedTools[i].Tool < r.report.HostedTools[j].Tool
	})
}
