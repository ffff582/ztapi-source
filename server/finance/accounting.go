// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*3600)
var zero = decimal.Zero
var amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var settlementUsageUnitsPattern = regexp.MustCompile(`^[0-9]{1,19}$`)

func MonthBounds(month string) (time.Time, time.Time, error) {
	start, e := time.ParseInLocation("2006-01", month, shanghai)
	if e != nil || start.Format("2006-01") != month {
		return time.Time{}, time.Time{}, errors.New("invalid calendar month")
	}
	return start, start.AddDate(0, 1, 0), nil
}
func money(amount, currency, rate string) (decimal.Decimal, error) {
	if !amountPattern.MatchString(amount) || !amountPattern.MatchString(rate) {
		return zero, errors.New("invalid decimal format")
	}
	a, e := decimal.NewFromString(amount)
	if e != nil || a.IsNegative() || len(amount) > 32 || a.Exponent() < -8 {
		return zero, errors.New("invalid amount")
	}
	r, e := decimal.NewFromString(rate)
	if e != nil || !r.IsPositive() || len(rate) > 32 || r.Exponent() < -12 {
		return zero, errors.New("invalid exchange rate")
	}
	switch currency {
	case "USDT":
		if !r.Equal(decimal.NewFromInt(1)) {
			return zero, errors.New("USDT rate must be one")
		}
	case "CNY", "USD":
	default:
		return zero, errors.New("unsupported currency")
	}
	return a.Mul(r), nil
}
func fixed(v decimal.Decimal) string { return v.StringFixed(6) }
func stringPtr(s string) *string     { return &s }

type basis struct {
	quota, cash decimal.Decimal
	unverified  bool
}

func (p *basis) add(b basis) {
	if p.quota.IsNegative() && b.quota.IsPositive() {
		remaining := decimal.Max(zero, b.quota.Add(p.quota))
		b.cash = b.cash.Mul(remaining.DivRound(b.quota, 24)).Round(24)
	}
	p.quota = p.quota.Add(b.quota)
	p.cash = p.cash.Add(b.cash).Round(24)
	p.unverified = p.unverified || b.unverified
	if !p.quota.IsPositive() {
		p.cash = zero
		p.unverified = p.quota.IsNegative()
	}
}
func (p *basis) take(q decimal.Decimal) basis {
	if !q.IsPositive() {
		return basis{}
	}
	if !p.quota.IsPositive() {
		return basis{quota: q, unverified: true}
	}
	available := decimal.Min(q, p.quota)
	ratio := available.DivRound(p.quota, 24)
	// Bound scale after every allocation. Multiplying 24-place ratios without
	// rounding grows integer coefficients on every request. Missing evidence is
	// a persistent flag, never a tiny fraction that could round away.
	b := basis{quota: q, cash: p.cash.Mul(ratio).Round(24), unverified: p.unverified || q.GreaterThan(available)}
	p.quota = p.quota.Sub(available)
	p.cash = p.cash.Sub(b.cash).Round(24)
	if p.quota.IsZero() {
		p.cash = zero
		p.unverified = false
	}
	return b
}

type replay struct {
	report                *Report
	start, end            time.Time
	known                 decimal.Decimal
	unknownRevenue        bool
	pools                 map[int]*basis
	holds                 map[string]*basis
	consumed              map[string]*basis
	consumedCharges       map[uint]*basis
	charges               []Charge
	primaryCharges        map[string][]Charge
	refunds               map[int]Refund
	finalized             map[string]bool
	settlements           map[string]Settlement
	funding               map[string]Fact
	usernames             map[int]string
	attemptChannels       map[uint]map[int]int
	requiredChannels      map[int]bool
	holdOwners            map[string]int
	chargeChannels        map[uint]int
	revenueByChannelModel map[int]map[string]decimal.Decimal
	costUnknown           bool
	testRequests          map[string]bool
}

func (r *replay) inMonth(t time.Time) bool { return !t.Before(r.start) && t.Before(r.end) }
func (r *replay) issue(code, ref string, user int) {
	r.report.IssueCount++
	if len(r.report.Issues) < 500 {
		r.report.Issues = append(r.report.Issues, Issue{code, ref, user})
	}
}
func (r *replay) channelFor(s Settlement) int { return r.attemptChannels[s.ID][s.FinalAttempt] }
func (r *replay) recognize(s Settlement, b basis, t time.Time, refund bool, channel int) {
	if !r.inMonth(t) {
		return
	}
	if channel <= 0 {
		r.costUnknown = true
		r.issue("cost_lineage_missing", s.RequestID, s.UserID)
	} else {
		r.requiredChannels[channel] = true
	}
	sign := int64(1)
	if refund {
		sign = -1
	}
	r.report.ChargedQuota += sign * b.quota.IntPart()
	if r.testRequests[s.RequestID] {
		return
	}
	r.known = r.known.Add(b.cash.Mul(decimal.NewFromInt(sign)))
	if channel > 0 && b.cash.IsPositive() {
		modelName := settlementSourceModel(s)
		if modelName == "" {
			modelName = s.PublicModel
		}
		if r.revenueByChannelModel[channel] == nil {
			r.revenueByChannelModel[channel] = map[string]decimal.Decimal{}
		}
		r.revenueByChannelModel[channel][modelName] = r.revenueByChannelModel[channel][modelName].Add(b.cash.Mul(decimal.NewFromInt(sign)))
	}
	if b.unverified {
		r.unknownRevenue = true
		r.issue("unknown_revenue_basis", s.RequestID, s.UserID)
	}
}

func validReportTestCost(f Fact, now time.Time) (TestCost, bool) {
	c, ok := f.Payload.(TestCost)
	if !ok {
		return TestCost{}, false
	}
	// Facts retain payloads, not submission keys. Reuse the existing payload
	// contract without introducing a new schema or writing a submission.
	err := validateSubmission(Submission{Kind: f.Kind, Ref: f.Ref, TestCost: &c, IdempotencyKey: "report-test-cost-validation"}, now)
	return c, err == nil
}

func testRequestsForReport(s Snapshot, latest map[string]Fact, cutoff, now time.Time) map[string]bool {
	requests := map[string]Settlement{}
	requestCounts := map[string]int{}
	settlementCounts := map[uint]int{}
	for _, settlement := range s.Settlements {
		requests[settlement.RequestID] = settlement
		requestCounts[settlement.RequestID]++
		settlementCounts[settlement.ID]++
	}
	type attemptKey struct {
		settlement uint
		attempt    int
	}
	attempts := map[attemptKey][]Attempt{}
	for _, attempt := range s.Attempts {
		key := attemptKey{attempt.SettlementID, attempt.Attempt}
		attempts[key] = append(attempts[key], attempt)
	}
	result := map[string]bool{}
	for _, f := range latest {
		if f.Kind != "test_cost" {
			continue
		}
		c, valid := validReportTestCost(f, now)
		if !valid {
			continue
		}
		settlement, exists := requests[c.RequestID]
		if !exists || settlement.ID == 0 || settlement.UserID <= 0 || requestCounts[c.RequestID] != 1 || settlementCounts[settlement.ID] != 1 ||
			(!settlement.CreatedAt.IsZero() && !settlement.CreatedAt.Before(cutoff)) {
			continue
		}
		matches := attempts[attemptKey{settlement.ID, c.Attempt}]
		if len(matches) != 1 || matches[0].ChannelID != c.ChannelID || matches[0].CreatedAt.IsZero() || !matches[0].CreatedAt.Before(cutoff) {
			continue
		}
		result[c.RequestID] = true
	}
	return result
}

func settlementSourceModel(s Settlement) string {
	var snapshot struct {
		SourceModel string `json:"source_model"`
	}
	if strings.TrimSpace(s.PriceSnapshotJSON) != "" {
		if err := json.Unmarshal([]byte(s.PriceSnapshotJSON), &snapshot); err == nil && strings.TrimSpace(snapshot.SourceModel) != "" {
			return strings.TrimSpace(snapshot.SourceModel)
		}
	}
	public := strings.TrimPrefix(strings.TrimSpace(s.PublicModel), "zt-")
	known := map[string]string{
		"claude-haiku-4.5":  "claude-haiku-4-5-20251001",
		"claude-opus-4.6":   "claude-opus-4-6",
		"claude-opus-4.7":   "claude-opus-4-7",
		"claude-opus-4.8":   "claude-opus-4-8",
		"claude-sonnet-4.6": "claude-sonnet-4-6",
		"claude-sonnet-5":   "claude-sonnet-5",
	}
	if source, ok := known[public]; ok {
		return source
	}
	return public
}

func isXcloudChannel(channel Channel) bool {
	return strings.Contains(strings.ToLower(channel.Name), "xcloud") ||
		strings.Contains(strings.ToLower(channel.BaseURL), "xcloudtoken.com")
}

func parseSettlementUsageDimensions(raw string) ([]UsageDimension, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "[]" {
		return nil, errors.New("settlement charge dimensions are missing")
	}
	if err := common.RejectDuplicateJsonObjectMembers(strings.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("settlement charge dimensions are invalid: %w", err)
	}
	var values []map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(raw, &values); err != nil || len(values) == 0 {
		return nil, errors.New("settlement charge dimensions are invalid")
	}
	dimensions := make([]UsageDimension, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		for name := range value {
			canonical := strings.ToLower(name)
			if (canonical == "dimension" || canonical == "units" || canonical == "quantity") && canonical != name {
				return nil, fmt.Errorf("noncanonical settlement usage field %q", name)
			}
		}
		var dimension string
		if err := common.Unmarshal(value["dimension"], &dimension); err != nil || dimension == "" || dimension != strings.TrimSpace(dimension) || seen[dimension] {
			return nil, errors.New("settlement usage dimension is missing, invalid or duplicated")
		}
		quantityRaw, hasQuantity := value["quantity"]
		unitsRaw, hasUnits := value["units"]
		if !hasQuantity && !hasUnits {
			return nil, errors.New("settlement usage quantity is missing")
		}
		var quantity int64
		if hasQuantity {
			if common.GetJsonType(quantityRaw) != "number" || common.Unmarshal(quantityRaw, &quantity) != nil || quantity < 0 {
				return nil, errors.New("settlement usage quantity must be a nonnegative int64")
			}
		}
		if hasUnits {
			var rawUnits string
			if err := common.Unmarshal(unitsRaw, &rawUnits); err != nil || !settlementUsageUnitsPattern.MatchString(rawUnits) {
				return nil, errors.New("settlement usage units must be a nonnegative integer string")
			}
			units, err := decimal.NewFromString(rawUnits)
			if err != nil || units.IsNegative() || !units.IsInteger() || units.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
				return nil, errors.New("settlement usage units exceed nonnegative int64 range")
			}
			if hasQuantity && units.IntPart() != quantity {
				return nil, errors.New("settlement usage units conflict with quantity")
			}
			quantity = units.IntPart()
		}
		seen[dimension] = true
		dimensions = append(dimensions, UsageDimension{Dimension: dimension, Quantity: quantity})
	}
	if seen["cache_write"] && (seen["cache_write_5m"] || seen["cache_write_1h"]) {
		return nil, errors.New("overlapping settlement cache write dimensions")
	}
	return dimensions, nil
}

func (r *replay) estimateIssue(code, ref string, userID int) {
	if len(r.report.EstimateIssues) < 500 {
		r.report.EstimateIssues = append(r.report.EstimateIssues, Issue{Code: code, Ref: ref, UserID: userID})
	}
}

func (r *replay) buildXcloudEstimates(s Snapshot, names, baseURLs map[int]string, cutoff time.Time) {
	xcloudChannels := map[int]string{}
	for _, channel := range s.Channels {
		if isXcloudChannel(channel) {
			xcloudChannels[channel.ID] = channel.Name
		}
	}
	if len(xcloudChannels) == 0 {
		return
	}
	type estimateKey struct {
		channel int
		model   string
	}
	rows := map[estimateKey]*SupplierEstimateRow{}
	costs := map[estimateKey]decimal.Decimal{}
	complete := true
	for _, settlement := range r.settlements {
		if settlement.Status != "settled" || settlement.CompletedAt <= 0 || !r.inMonth(time.Unix(settlement.CompletedAt, 0)) || time.Unix(settlement.CompletedAt, 0).After(cutoff) {
			continue
		}
		channelID := r.channelFor(settlement)
		supplier, ok := xcloudChannels[channelID]
		if !ok {
			continue
		}
		sourceModel := settlementSourceModel(settlement)
		key := estimateKey{channel: channelID, model: sourceModel}
		row := rows[key]
		if row == nil {
			row = &SupplierEstimateRow{ChannelID: channelID, Supplier: supplier, SourceModel: sourceModel, Dimensions: map[string]int64{}, Complete: true}
			rows[key] = row
		}
		row.Requests++
		if settlement.MissingDimensionsJSON != "" && settlement.MissingDimensionsJSON != "[]" {
			row.Complete = false
			complete = false
			r.estimateIssue("xcloud_usage_incomplete", settlement.RequestID, settlement.UserID)
			continue
		}
		dimensions, err := parseSettlementUsageDimensions(settlement.ChargeDimensionsJSON)
		if err != nil {
			row.Complete = false
			complete = false
			r.estimateIssue("xcloud_usage_missing", settlement.RequestID, settlement.UserID)
			continue
		}
		for _, dimension := range dimensions {
			row.Dimensions[dimension.Dimension] += dimension.Quantity
		}
		estimate, err := EstimateXcloudUsage(sourceModel, dimensions)
		if err != nil {
			row.Complete = false
			complete = false
			r.estimateIssue("xcloud_quote_missing", settlement.RequestID, settlement.UserID)
			continue
		}
		row.SourceURL = estimate.SourceURL
		row.QuoteDiscount = estimate.DiscountFactor
		row.IsEstimate = estimate.IsEstimate
		value, _ := decimal.NewFromString(estimate.CostUSDT)
		costs[key] = costs[key].Add(value)
	}
	if len(rows) == 0 {
		return
	}
	totalCost, totalRevenue := zero, zero
	for key, row := range rows {
		rowCost := costs[key]
		row.CostUSDT = fixed(rowCost)
		revenue := r.revenueByChannelModel[key.channel][key.model]
		row.RevenueUSDT = fixed(revenue)
		if row.Complete && !r.unknownRevenue {
			row.GrossProfitUSDT = fixed(revenue.Sub(rowCost))
			totalCost = totalCost.Add(rowCost)
			totalRevenue = totalRevenue.Add(revenue)
		} else {
			complete = false
		}
		if row.Supplier == "" {
			row.Supplier = names[key.channel]
			if row.Supplier == "" {
				row.Supplier = baseURLs[key.channel]
			}
		}
	}
	for _, row := range rows {
		r.report.Estimates = append(r.report.Estimates, *row)
	}
	sort.Slice(r.report.Estimates, func(i, j int) bool {
		if r.report.Estimates[i].ChannelID != r.report.Estimates[j].ChannelID {
			return r.report.Estimates[i].ChannelID < r.report.Estimates[j].ChannelID
		}
		return r.report.Estimates[i].SourceModel < r.report.Estimates[j].SourceModel
	})
	r.report.EstimateComplete = complete
	if complete {
		r.report.EstimatedCost = stringPtr(fixed(totalCost))
		r.report.EstimatedGrossProfit = stringPtr(fixed(totalRevenue.Sub(totalCost)))
	}
}

func (r *replay) finalize(req string) {
	s, ok := r.settlements[req]
	if !ok || r.finalized[req] || s.Status != "settled" || s.CompletedAt <= 0 {
		return
	}
	h := r.holds[req]
	if h == nil {
		h = &basis{}
	}
	if !h.quota.Equal(decimal.NewFromInt(s.ChargedQuota)) {
		r.issue("settlement_basis_mismatch", req, s.UserID)
		r.unknownRevenue = true
		h = &basis{quota: decimal.NewFromInt(s.ChargedQuota), unverified: true}
	}
	b := *h
	r.consumed[req] = &b
	for _, c := range r.primaryCharges[req] {
		r.consumedCharges[c.ID] = &b
		r.chargeChannels[c.ID] = r.channelFor(s)
	}
	r.holds[req] = &basis{}
	r.finalized[req] = true
	r.recognize(s, b, time.Unix(s.CompletedAt, 0), false, r.channelFor(s))
}
func (r *replay) classification(ref string, user int, q int64, at time.Time, source string) (basis, *Funding) {
	row := FundingRow{Ref: ref, UserID: user, Username: r.usernames[user], Quota: q, CreatedAt: at, Source: source}
	f, ok := r.funding[ref]
	b := basis{quota: decimal.NewFromInt(q)}
	if ok {
		v, valid := f.Payload.(Funding)
		if valid {
			row.FactID = f.ID
			row.Classification = &v
			switch v.Kind {
			case "cash":
				amount, e := money(v.Amount, v.Currency, v.Rate)
				if e == nil && q > 0 {
					b.cash = amount
					r.report.ManualFunding = append(r.report.ManualFunding, row)
					return b, &v
				}
			case "gift", "internal":
				if q > 0 {
					r.report.ManualFunding = append(r.report.ManualFunding, row)
					return b, &v
				}
			case "correction":
				if q < 0 {
					r.report.ManualFunding = append(r.report.ManualFunding, row)
					return b, &v
				}
			}
		}
	}
	b.unverified = true
	r.report.ManualFunding = append(r.report.ManualFunding, row)
	r.issue("unclassified_funding", ref, user)
	return b, nil
}

// Calculate replays immutable wallet entries. Holds retain their original cash
// basis; future top-ups cannot revalue an already-dispatched request.
func Calculate(s Snapshot, facts []Fact, month string, now time.Time) (Report, error) {
	start, end, e := MonthBounds(month)
	if e != nil {
		return Report{}, e
	}
	if s.QuotaPerUnit <= 0 {
		return Report{}, errors.New("invalid quota unit")
	}
	cutoff := end
	if now.Before(cutoff) {
		cutoff = now
	}
	if cutoff.Before(start) {
		return Report{}, errors.New("future month")
	}
	out := Report{Month: month, Timezone: "Asia/Shanghai", Currency: "USDT", AsOf: now.UTC(), PeriodClosed: !now.Before(end), QuotaPerUnit: s.QuotaPerUnit, Receipts: []Receipt{}, ManualFunding: []FundingRow{}, Channels: []ChannelUsage{}, Costs: []CostRow{}, TestCosts: []TestCostRow{}, Issues: []Issue{}}
	out.SourceCommit = s.SourceCommit
	r := &replay{report: &out, start: start, end: cutoff, pools: map[int]*basis{}, holds: map[string]*basis{}, consumed: map[string]*basis{}, finalized: map[string]bool{}, settlements: map[string]Settlement{}, funding: map[string]Fact{}, usernames: map[int]string{}, revenueByChannelModel: map[int]map[string]decimal.Decimal{}}
	r.consumedCharges = map[uint]*basis{}
	r.attemptChannels = map[uint]map[int]int{}
	r.requiredChannels = map[int]bool{}
	r.holdOwners = map[string]int{}
	r.chargeChannels = map[uint]int{}
	for _, a := range s.Attempts {
		if r.attemptChannels[a.SettlementID] == nil {
			r.attemptChannels[a.SettlementID] = map[int]int{}
		}
		r.attemptChannels[a.SettlementID][a.Attempt] = a.ChannelID
	}
	r.charges = s.Charges
	r.primaryCharges = map[string][]Charge{}
	for _, c := range s.Charges {
		if c.BillingProofID == 0 {
			r.primaryCharges[c.RequestID] = append(r.primaryCharges[c.RequestID], c)
		}
	}
	r.refunds = map[int]Refund{}
	for _, f := range s.Refunds {
		r.refunds[f.LedgerID] = f
	}
	latest := map[string]Fact{}
	for _, f := range facts {
		if (f.Kind == "direct_cost" || f.Kind == "test_cost") && f.CreatedAt.After(now) {
			continue
		}
		key := f.Kind + ":" + f.Ref
		if f.ID > latest[key].ID {
			latest[key] = f
		}
	}
	r.testRequests = testRequestsForReport(s, latest, cutoff, now)
	for _, f := range latest {
		if f.Kind == "funding" {
			r.funding[f.Ref] = f
		}
	}
	for _, u := range s.Users {
		r.usernames[u.ID] = u.Username
	}
	for _, row := range s.Settlements {
		if !row.CreatedAt.IsZero() && !row.CreatedAt.Before(cutoff) {
			continue
		}
		r.settlements[row.RequestID] = row
	}
	payments := map[string]Payment{}
	for _, p := range s.Payments {
		if p.Status == "success" && p.CompleteTime > 0 && p.PayAmountMicros > 0 {
			payments[p.TradeNo] = p
			if r.inMonth(time.Unix(p.CompleteTime, 0)) {
				cash := decimal.NewFromInt(p.PayAmountMicros).Div(decimal.NewFromInt(1000000))
				out.Receipts = append(out.Receipts, Receipt{fmt.Sprintf("topup:%d", p.ID), p.UserID, r.usernames[p.UserID], fixed(cash), "USDT", "1", fixed(cash), p.CreditUnits * s.QuotaPerUnit, p.CompleteTime, p.TradeNo})
			}
		}
	}
	rows := append([]Ledger(nil), s.Ledgers...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	openingSeen := map[int]bool{}
	for _, l := range rows {
		refs := []struct {
			ref string
			q   int64
		}{{fmt.Sprintf("ledger:%d", l.ID), l.Delta}}
		if !openingSeen[l.UserID] {
			openingSeen[l.UserID] = true
			refs = append(refs, struct {
				ref string
				q   int64
			}{fmt.Sprintf("opening:%d", l.UserID), l.BalanceBefore})
		}
		for _, target := range refs {
			f, ok := r.funding[target.ref]
			v, valid := f.Payload.(Funding)
			if !ok || !valid || v.Kind != "cash" || target.q <= 0 || !r.inMonth(time.Unix(v.ReceivedAt, 0)) {
				continue
			}
			a, e := money(v.Amount, v.Currency, v.Rate)
			if e == nil {
				out.Receipts = append(out.Receipts, Receipt{target.ref, l.UserID, r.usernames[l.UserID], v.Amount, v.Currency, v.Rate, fixed(a), target.q, v.ReceivedAt, v.Evidence})
			}
		}
	}
	for _, l := range rows {
		if !l.CreatedAt.Before(cutoff) {
			continue
		}
		pool := r.pools[l.UserID]
		if pool == nil {
			pool = &basis{}
			r.pools[l.UserID] = pool
			if l.BalanceBefore < 0 {
				r.issue("unknown_revenue_basis", fmt.Sprintf("opening:%d", l.UserID), l.UserID)
				r.unknownRevenue = true
			}
			if l.BalanceBefore != 0 {
				opening, _ := r.classification(fmt.Sprintf("opening:%d", l.UserID), l.UserID, l.BalanceBefore, l.CreatedAt, "opening")
				pool.add(opening)
			}
		}
		if l.BalanceAfter != l.BalanceBefore+l.Delta || !pool.quota.Equal(decimal.NewFromInt(l.BalanceBefore)) {
			r.issue("ledger_gap", fmt.Sprintf("ledger:%d", l.ID), l.UserID)
			pool.quota = decimal.NewFromInt(l.BalanceBefore)
			pool.cash = zero
			pool.unverified = !pool.quota.IsZero()
			r.unknownRevenue = true
		}
		h := r.holds[l.RequestID]
		if h == nil {
			h = &basis{}
			r.holds[l.RequestID] = h
		}
		q := decimal.NewFromInt(l.Delta)
		switch l.SourceType {
		case "usdt_topup_settlement":
			p, ok := payments[l.RequestID]
			if !ok || p.UserID != l.UserID || p.CreditUnits*s.QuotaPerUnit != l.Delta {
				pool.add(basis{quota: q, unverified: true})
				r.issue("payment_lineage_missing", fmt.Sprintf("ledger:%d", l.ID), l.UserID)
				break
			}
			cash := decimal.NewFromInt(p.PayAmountMicros).Div(decimal.NewFromInt(1000000))
			pool.add(basis{quota: q, cash: cash})
		case "usdt_topup_bonus":
			p, ok := payments[l.RequestID]
			if !ok || p.UserID != l.UserID || l.Delta <= 0 {
				pool.add(basis{quota: q, unverified: true})
				r.issue("bonus_lineage_missing", fmt.Sprintf("ledger:%d", l.ID), l.UserID)
			} else {
				pool.add(basis{quota: q})
			}
		case "usage_reservation":
			if l.Delta >= 0 {
				r.issue("invalid_reservation", l.RequestID, l.UserID)
				r.unknownRevenue = true
				break
			}
			h.add(pool.take(q.Neg()))
			r.holdOwners[l.RequestID] = l.UserID
		case "usage_settlement":
			s, ok := r.settlements[l.RequestID]
			if strings.HasPrefix(l.IdempotencyKey, "attempt-billing:") && ok && l.Delta < 0 {
				found := false
				for _, c := range r.charges {
					if c.OriginalLedgerID == l.ID && c.BillingProofID > 0 && c.RequestID == l.RequestID && c.UserID == l.UserID && c.ChargedQuota == -l.Delta {
						b := pool.take(q.Neg())
						r.consumedCharges[c.ID] = &b
						r.chargeChannels[c.ID] = c.ChannelID
						if c.PostedAt <= 0 {
							r.issue("retry_posting_missing", l.RequestID, l.UserID)
							r.unknownRevenue = true
							r.costUnknown = true
						} else {
							r.recognize(s, b, time.Unix(c.PostedAt, 0), false, c.ChannelID)
						}
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !ok || l.UserID != s.UserID || l.IdempotencyKey != "ztapi:"+s.OperationID+":settle" || s.CompletedAt <= 0 || time.Unix(s.CompletedAt, 0).After(cutoff) {
				r.issue("settlement_lineage_missing", l.RequestID, l.UserID)
				r.unknownRevenue = true
				if l.Delta > 0 {
					pool.add(basis{quota: q, unverified: true})
				} else {
					pool.take(q.Neg())
				}
				break
			}
			if l.Delta > 0 {
				pool.add(h.take(q))
			} else if l.Delta < 0 {
				h.add(pool.take(q.Neg()))
			}
			r.finalize(l.RequestID)
		case "billing_refund":
			if l.Delta <= 0 || q.GreaterThan(h.quota) {
				r.issue("release_lineage_missing", l.RequestID, l.UserID)
				pool.add(basis{quota: q, unverified: true})
				r.unknownRevenue = true
			} else {
				pool.add(h.take(q))
			}
		case "supplier_refund":
			r.finalize(l.RequestID)
			b := r.consumed[l.RequestID]
			s := r.settlements[l.RequestID]
			channel := r.channelFor(s)
			if f, ok := r.refunds[l.ID]; ok {
				b = r.consumedCharges[f.ChargeID]
				channel = r.chargeChannels[f.ChargeID]
				if f.RefundedQuota != l.Delta {
					b = nil
				}
			} else if len(r.charges) > 0 {
				b = nil
			}
			if b == nil || l.UserID != s.UserID || l.Delta <= 0 || q.GreaterThan(b.quota) {
				r.issue("refund_lineage_missing", l.RequestID, l.UserID)
				pool.add(basis{quota: q, unverified: true})
				r.unknownRevenue = true
			} else {
				restored := b.take(q)
				pool.add(restored)
				r.recognize(s, restored, l.CreatedAt, true, channel)
			}
		default:
			b, f := r.classification(fmt.Sprintf("ledger:%d", l.ID), l.UserID, l.Delta, l.CreatedAt, l.SourceType)
			if l.Delta > 0 {
				pool.add(b)
			} else {
				pool.take(q.Neg())
				if f == nil && r.inMonth(l.CreatedAt) {
					r.unknownRevenue = true
				}
			}
		}
	}
	for req, s := range r.settlements {
		if s.Status == "settled" && s.CompletedAt > 0 && time.Unix(s.CompletedAt, 0).Before(cutoff) {
			r.finalize(req)
		} else if s.Status == "settled" && s.CompletedAt == 0 {
			r.issue("finalization_missing", req, s.UserID)
			r.unknownRevenue = true
		}
	}
	for req, h := range r.holds {
		if h.quota.IsPositive() {
			out.PendingRequests++
			out.PendingQuota += h.quota.IntPart()
			if _, ok := r.settlements[req]; !ok {
				r.issue("orphan_reservation", req, r.holdOwners[req])
				r.costUnknown = true
			}
		}
	}
	out.KnownRevenue = fixed(r.known)
	if !r.unknownRevenue {
		out.Revenue = stringPtr(out.KnownRevenue)
	}
	names := map[int]string{}
	baseURLs := map[int]string{}
	for _, c := range s.Channels {
		names[c.ID] = c.Name
		baseURLs[c.ID] = c.BaseURL
	}
	used := map[int]int{}
	for _, a := range s.Attempts {
		if !a.CreatedAt.Before(start) && a.CreatedAt.Before(cutoff) {
			used[a.ChannelID]++
		}
	}
	for id := range r.requiredChannels {
		if _, ok := used[id]; !ok {
			used[id] = 0
		}
	}
	for id, n := range used {
		out.Channels = append(out.Channels, ChannelUsage{id, names[id], n})
	}
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].ChannelID < out.Channels[j].ChannelID })
	r.buildXcloudEstimates(s, names, baseURLs, cutoff)
	totalCost := zero
	covered := map[int]bool{}
	for _, f := range latest {
		c, ok := f.Payload.(Cost)
		if f.Kind != "cost" || !ok || c.Month != month {
			continue
		}
		gross, ge := money(c.Gross, c.Currency, c.Rate)
		refund, re := money(c.Refund, c.Currency, c.Rate)
		if ge != nil || re != nil || refund.IsPositive() && len(c.RefundEvidence) < 3 {
			r.issue("invalid_cost", f.Ref, 0)
			continue
		}
		net := gross.Sub(refund)
		totalCost = totalCost.Add(net)
		out.Costs = append(out.Costs, CostRow{c, f.ID, fixed(net)})
		covered[c.ChannelID] = c.Complete
	}
	complete := out.PeriodClosed && !r.costUnknown
	if len(out.Costs) == 0 {
		complete = false
		r.issue("supplier_cost_missing", "month:"+month, 0)
	}
	for id := range used {
		if id <= 0 || !covered[id] {
			complete = false
			r.issue("supplier_cost_missing", fmt.Sprintf("channel:%d", id), 0)
		}
	}
	for _, c := range out.Costs {
		if !c.Complete {
			complete = false
			r.issue("supplier_cost_partial", fmt.Sprintf("channel:%d", c.ChannelID), 0)
		}
	}
	confirmedTests, estimatedTests, xcloudCredits := zero, zero, zero
	directCredits, directConfirmed := zero, zero
	includedTestsByChannel := map[int]decimal.Decimal{}
	requestBySettlement := map[uint]string{}
	modelByRequest := map[string]string{}
	for _, settlement := range s.Settlements {
		requestBySettlement[settlement.ID] = settlement.RequestID
		modelByRequest[settlement.RequestID] = settlement.PublicModel
	}
	attemptTimes := map[string]time.Time{}
	attemptCounts := map[string]int{}
	for _, attempt := range s.Attempts {
		request := requestBySettlement[attempt.SettlementID]
		if request == "" {
			continue
		}
		key := fmt.Sprintf("test:%s:%d:%d", request, attempt.Attempt, attempt.ChannelID)
		attemptCounts[key]++
		attemptTimes[key] = attempt.CreatedAt
	}
	for _, f := range latest {
		if f.Kind != "test_cost" {
			continue
		}
		c, ok := validReportTestCost(f, now)
		if !ok {
			complete = false
			r.issue("test_cost_invalid", f.Ref, 0)
			continue
		}
		key := fmt.Sprintf("test:%s:%d:%d", c.RequestID, c.Attempt, c.ChannelID)
		at := attemptTimes[key]
		if at.IsZero() || attemptCounts[key] != 1 || key != f.Ref {
			complete = false
			r.issue("test_cost_lineage_missing", f.Ref, 0)
			continue
		}
		if !r.inMonth(at) {
			continue
		}
		netCredit := ""
		if c.Currency == "XCLOUD_CREDIT" {
			amount, amountErr := money(c.Amount, "USDT", "1")
			refund, refundErr := money(c.Refund, "USDT", "1")
			if amountErr != nil || refundErr != nil || refund.GreaterThan(amount) || c.Status != "confirmed" || c.EvidenceType != "supplier_statement" || len(strings.TrimSpace(c.Evidence)) < 3 || (refund.IsPositive() && len(strings.TrimSpace(c.RefundEvidence)) < 3) || (c.Rate != "" && (len(strings.TrimSpace(c.RateEvidence)) < 3 || len(c.RateEvidence) > 500)) || (c.Rate == "" && c.Coverage != "unknown") {
				complete = false
				r.issue("test_cost_invalid", f.Ref, 0)
				continue
			}
			xcloudCredits = xcloudCredits.Add(amount.Sub(refund))
			netCredit = amount.Sub(refund).StringFixed(8)
			if c.Rate == "" {
				out.TestCosts = append(out.TestCosts, TestCostRow{TestCost: c, FactID: f.ID, AttemptAt: at, Model: modelByRequest[c.RequestID], NetCredit: netCredit})
				complete = false
				r.issue("test_cost_exchange_missing", f.Ref, 0)
				continue
			}
		}
		currency := c.Currency
		if currency == "XCLOUD_CREDIT" {
			currency = "USD"
		}
		amount, ae := money(c.Amount, currency, c.Rate)
		refund, re := money(c.Refund, currency, c.Rate)
		if ae != nil || re != nil || refund.GreaterThan(amount) || (refund.IsPositive() && (c.Status != "confirmed" || len(strings.TrimSpace(c.RefundEvidence)) < 3)) || len(strings.TrimSpace(c.Evidence)) < 3 || (c.Status != "confirmed" && c.Status != "estimate") || (c.Status == "confirmed" && c.EvidenceType != "supplier_statement") || (c.Status == "estimate" && c.EvidenceType != "quote" && c.EvidenceType != "other") {
			complete = false
			r.issue("test_cost_invalid", f.Ref, 0)
			continue
		}
		net := amount.Sub(refund)
		out.TestCosts = append(out.TestCosts, TestCostRow{TestCost: c, FactID: f.ID, AttemptAt: at, Model: modelByRequest[c.RequestID], NetUSDT: fixed(net), NetCredit: netCredit})
		if c.Status == "confirmed" {
			confirmedTests = confirmedTests.Add(net)
		} else {
			estimatedTests = estimatedTests.Add(net)
		}
		switch c.Coverage {
		case "included":
			matched := false
			for _, bill := range out.Costs {
				if bill.ChannelID == c.ChannelID && bill.Complete && bill.Evidence == c.CoverageEvidence {
					matched = true
				}
			}
			if !matched {
				complete = false
				r.issue("test_cost_coverage_missing", f.Ref, 0)
			} else if c.Status == "confirmed" {
				includedTestsByChannel[c.ChannelID] = includedTestsByChannel[c.ChannelID].Add(net)
			}
		case "extra":
			conflicts := false
			for _, bill := range out.Costs {
				if bill.ChannelID == c.ChannelID && bill.Evidence == c.CoverageEvidence {
					conflicts = true
				}
			}
			if c.Status != "confirmed" || len(strings.TrimSpace(c.CoverageEvidence)) < 3 || conflicts {
				complete = false
				r.issue("test_cost_unconfirmed", f.Ref, 0)
			} else {
				totalCost = totalCost.Add(net)
			}
		default:
			complete = false
			r.issue("test_cost_coverage_missing", f.Ref, 0)
		}
	}
	for _, f := range latest {
		if f.Kind != "direct_cost" {
			continue
		}
		c, ok := f.Payload.(DirectCost)
		if ok {
			normalizeDirectCost(&c)
		}
		if !ok || validateDirectCost(&c, now) != nil || directCostRef(&c) != f.Ref {
			complete = false
			r.issue("direct_cost_invalid", f.Ref, 0)
			continue
		}
		if !r.inMonth(c.OccurredAt) {
			continue
		}
		amount, _ := money(c.Amount, "USDT", "1")
		if c.Currency == "XCLOUD_CREDIT" {
			directCredits = directCredits.Add(amount)
		}
		row := DirectCostRow{DirectCost: c, FactID: f.ID, Ref: f.Ref}
		overlap := false
		for _, test := range out.TestCosts {
			// Missing legacy identities cannot prove these are distinct bills.
			if test.SupplierBillID == "" || strings.EqualFold(test.SupplierBillID, c.BillID) {
				overlap = true
			}
		}
		if !directVerificationMatches(s, &c) {
			overlap = true
			complete = false
			r.issue("direct_cost_lineage_missing", f.Ref, 0)
		}
		if c.Rate == "" {
			complete = false
			r.issue("direct_cost_exchange_missing", f.Ref, 0)
		} else {
			currency := c.Currency
			if currency == "XCLOUD_CREDIT" {
				currency = "USD"
			}
			net, _ := money(c.Amount, currency, c.Rate)
			row.NetUSDT = fixed(net)
			if directVerificationMatches(s, &c) {
				directConfirmed = directConfirmed.Add(net)
			}
			matched := false
			allExcluded := true
			otherExcluded := true
			for _, bill := range out.Costs {
				if !strings.EqualFold(bill.Supplier, c.Supplier) {
					continue
				}
				excluded := false
				for _, ref := range bill.ExcludedDirectBillRefs {
					if ref == f.Ref {
						excluded = true
					}
				}
				if !bill.Complete || !excluded {
					allExcluded = false
					if bill.FactID != c.MonthCostFactID {
						otherExcluded = false
					}
				}
			}
			for _, bill := range out.Costs {
				if bill.FactID == c.MonthCostFactID && bill.ChannelID == c.ChannelID && strings.EqualFold(bill.Supplier, c.Supplier) && bill.Complete {
					excluded := false
					for _, ref := range bill.ExcludedDirectBillRefs {
						if ref == f.Ref {
							excluded = true
						}
					}
					if c.Coverage == "included" && !excluded && otherExcluded && c.CoverageEvidence == bill.Evidence && !overlap {
						matched = true
						includedTestsByChannel[c.ChannelID] = includedTestsByChannel[c.ChannelID].Add(net)
					}
					if c.Coverage == "extra" && excluded && allExcluded && c.CoverageEvidence != bill.Evidence && !overlap {
						matched = true
						totalCost = totalCost.Add(net)
					}
				}
			}
			if !matched {
				complete = false
				r.issue("direct_cost_coverage_missing", f.Ref, 0)
			}
		}
		out.DirectCosts = append(out.DirectCosts, row)
	}
	for _, bill := range out.Costs {
		included := includedTestsByChannel[bill.ChannelID]
		gross, grossErr := money(bill.Gross, bill.Currency, bill.Rate)
		refund, refundErr := money(bill.Refund, bill.Currency, bill.Rate)
		if included.IsPositive() && (grossErr != nil || refundErr != nil || included.GreaterThan(gross.Sub(refund))) {
			complete = false
			r.issue("test_cost_coverage_conflict", fmt.Sprintf("channel:%d", bill.ChannelID), 0)
		}
	}
	out.TestConfirmedTotal = fixed(confirmedTests)
	out.TestEstimateTotal = fixed(estimatedTests)
	out.TestXcloudCreditTotal = xcloudCredits.StringFixed(8)
	out.DirectXcloudCreditTotal = directCredits.StringFixed(8)
	out.DirectConfirmedTotal = fixed(directConfirmed)
	sort.Slice(out.DirectCosts, func(i, j int) bool { return out.DirectCosts[i].FactID < out.DirectCosts[j].FactID })
	sort.Slice(out.TestCosts, func(i, j int) bool { return out.TestCosts[i].FactID < out.TestCosts[j].FactID })
	out.RecordedCost = fixed(totalCost)
	if complete {
		out.ActualCost = stringPtr(out.RecordedCost)
	}
	if out.Revenue != nil && out.ActualCost != nil && out.PeriodClosed && out.IssueCount == 0 {
		out.Confirmed = true
		profit := r.known.Sub(totalCost)
		out.GrossProfit = stringPtr(fixed(profit))
		if r.known.IsPositive() {
			out.Margin = stringPtr(profit.Div(r.known).Mul(decimal.NewFromInt(100)).StringFixed(2))
		}
	}
	receiptTotal := zero
	for _, p := range out.Receipts {
		v, _ := decimal.NewFromString(p.AmountUSDT)
		receiptTotal = receiptTotal.Add(v)
	}
	out.ReceiptsTotal = fixed(receiptTotal)
	sort.Slice(out.Costs, func(i, j int) bool { return out.Costs[i].ChannelID < out.Costs[j].ChannelID })
	sort.Slice(out.Receipts, func(i, j int) bool { return out.Receipts[i].ReceivedAt > out.Receipts[j].ReceivedAt })
	sort.Slice(out.ManualFunding, func(i, j int) bool { return strings.Compare(out.ManualFunding[i].Ref, out.ManualFunding[j].Ref) < 0 })
	return out, nil
}
