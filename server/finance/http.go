// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const prefix = "/api/admin/finance-reporting/"

type handler struct {
	source Source
	store  *Store
	origin string
	now    func() time.Time
	slots  chan struct{}
}

func NewHandler(source Source, store *Store, origin string, now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return &handler{source: source, store: store, origin: origin, now: now, slots: make(chan struct{}, 2)}
}
func sendJSON(w http.ResponseWriter, status int, data any) {
	raw, e := common.Marshal(data)
	if e != nil {
		http.Error(w, "serialization failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func failure(w http.ResponseWriter, status int, code string) {
	sendJSON(w, status, map[string]any{"success": false, "message": code})
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		sendJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		failure(w, 401, "authentication_required")
		return
	}
	claims, e := service.ParseZTAPIAccessToken(strings.TrimPrefix(auth, "Bearer "), h.now())
	if e != nil {
		failure(w, 401, "invalid_access_token")
		return
	}
	id, e := strconv.Atoi(claims.Subject)
	if e != nil || r.Header.Get("New-Api-User") != claims.Subject {
		failure(w, 401, "identity_mismatch")
		return
	}
	user, e := h.source.Identity(ctx, id)
	if e != nil {
		failure(w, 503, "identity_unavailable")
		return
	}
	if user.ID != id || user.DeletedAt != nil || user.Status != common.UserStatusEnabled || !common.HasAdminPermission(user.Role, common.PermissionFinanceRead) {
		failure(w, 403, "finance_access_denied")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		failure(w, 429, "finance_busy")
		return
	}
	switch {
	case r.Method == "GET" && (r.URL.Path == prefix+"report" || r.URL.Path == prefix+"export"):
		month := r.URL.Query().Get("month")
		start, _, e := MonthBounds(month)
		now := h.now()
		if e != nil || start.After(now) {
			failure(w, 400, "invalid_month")
			return
		}
		before := now.Truncate(15 * time.Second)
		source, e := h.source.Load(ctx, before)
		if e != nil {
			failure(w, 503, "finance_source_unavailable")
			return
		}
		facts, e := h.store.List(ctx)
		if e != nil {
			failure(w, 503, "financial_records_unavailable")
			return
		}
		report, e := Calculate(source, facts, month, now)
		if e != nil {
			failure(w, 422, "finance_calculation_unavailable")
			return
		}
		if r.URL.Path == prefix+"export" {
			exportReport(w, report)
			return
		}
		sendJSON(w, 200, map[string]any{"success": true, "data": report})
	case r.Method == "GET" && r.URL.Path == prefix+"history":
		facts, e := h.store.List(ctx)
		if e != nil {
			failure(w, 503, "financial_records_unavailable")
			return
		}
		sendJSON(w, 200, map[string]any{"success": true, "data": facts})
	case r.Method == "POST" && r.URL.Path == prefix+"facts":
		if !common.HasAdminPermission(user.Role, common.PermissionFinanceWrite) || r.Header.Get("Origin") != h.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			failure(w, 403, "finance_write_denied")
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			failure(w, 415, "json_required")
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if e != nil {
			failure(w, 413, "financial_payload_too_large")
			return
		}
		var in Submission
		if common.RejectDuplicateJsonObjectMembers(strings.NewReader(string(raw))) != nil || common.DecodeJsonStrict(strings.NewReader(string(raw)), &in) != nil {
			failure(w, 400, "invalid_financial_record")
			return
		}
		if in.Kind == "direct_cost" && in.DirectCost != nil {
			in.Ref = directCostRef(in.DirectCost)
		}
		if validateSubmission(in, h.now()) != nil {
			failure(w, 400, "invalid_financial_record")
			return
		}
		source, e := h.source.Load(ctx, h.now().Truncate(15*time.Second))
		if e != nil {
			failure(w, 503, "finance_source_unavailable")
			return
		}
		if !validSourceReference(source, in) {
			failure(w, 400, "financial_source_mismatch")
			return
		}
		result, e := h.store.Append(ctx, in, id, h.now())
		if e != nil {
			if errors.Is(e, ErrConflict) {
				failure(w, 409, "financial_record_changed")
			} else {
				failure(w, 400, "invalid_financial_record")
			}
			return
		}
		sendJSON(w, 201, map[string]any{"success": true, "data": result})
	default:
		w.Header().Set("Allow", "GET, POST")
		failure(w, 405, "method_not_allowed")
	}
}
func validSourceReference(s Snapshot, in Submission) bool {
	if in.Kind == "direct_cost" {
		return directVerificationMatches(s, in.DirectCost)
	}
	if in.Kind == "test_cost" {
		matches := 0
		for _, settlement := range s.Settlements {
			if settlement.RequestID != in.TestCost.RequestID {
				continue
			}
			for _, attempt := range s.Attempts {
				if attempt.SettlementID == settlement.ID && attempt.Attempt == in.TestCost.Attempt && attempt.ChannelID == in.TestCost.ChannelID && !attempt.CreatedAt.IsZero() {
					matches++
				}
			}
		}
		return matches == 1
	}
	if in.Kind == "cost" {
		for _, c := range s.Channels {
			if c.ID == in.Cost.ChannelID {
				return true
			}
		}
		for _, a := range s.Attempts {
			if a.ChannelID == in.Cost.ChannelID {
				return true
			}
		}
		return false
	}
	for _, l := range s.Ledgers {
		q := l.Delta
		isLedger := in.Ref == fmt.Sprintf("ledger:%d", l.ID)
		isOpening := in.Ref == fmt.Sprintf("opening:%d", l.UserID)
		if isOpening {
			q = l.BalanceBefore
			for _, other := range s.Ledgers {
				if other.UserID == l.UserID && other.ID < l.ID {
					isOpening = false
					break
				}
			}
		}
		if !isLedger && !isOpening {
			continue
		}
		if isLedger && l.SourceType != "admin_adjustment" {
			return false
		}
		if q > 0 && in.Funding.Kind == "correction" || q < 0 && in.Funding.Kind != "correction" || q == 0 {
			return false
		}
		return in.Funding.Kind != "cash" || in.Funding.ReceivedAt <= l.CreatedAt.Unix()
	}
	return false
}
func csvSafe(s string) string {
	trim := strings.TrimLeft(s, " \t\r\n")
	if strings.ContainsAny(s[:len(s)-len(trim)], "\t\r\n") || len(trim) > 0 && strings.ContainsRune("=+-@", rune(trim[0])) {
		return "'" + s
	}
	return s
}
func exportReport(w http.ResponseWriter, r Report) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="ztapi-finance-%s.csv"`, r.Month))
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	cw := csv.NewWriter(w)
	row := func(values ...string) {
		for i, v := range values {
			values[i] = csvSafe(v)
		}
		_ = cw.Write(values)
	}
	val := func(p *string) string {
		if p == nil {
			return "PENDING"
		}
		return *p
	}
	row("month", "currency", "receipts", "earned_revenue", "actual_upstream_cost", "gross_profit", "confirmed", "as_of")
	row(r.Month, r.Currency, r.ReceiptsTotal, val(r.Revenue), val(r.ActualCost), val(r.GrossProfit), strconv.FormatBool(r.Confirmed), r.AsOf.Format(time.RFC3339))
	row()
	row("receipt_ref", "user_id", "username", "amount", "currency", "USDT_per_currency", "amount_USDT", "received_at", "evidence")
	for _, p := range r.Receipts {
		row(p.Ref, strconv.Itoa(p.UserID), p.Username, p.Amount, p.Currency, p.Rate, p.AmountUSDT, time.Unix(p.ReceivedAt, 0).In(shanghai).Format(time.RFC3339), p.Evidence)
	}
	row()
	row("channel_id", "supplier", "gross_consumption", "supplier_refund", "currency", "USDT_per_currency", "net_USDT", "complete_month", "evidence", "excluded_direct_bill_refs", "fact_id")
	for _, c := range r.Costs {
		row(strconv.Itoa(c.ChannelID), c.Supplier, c.Gross, c.Refund, c.Currency, c.Rate, c.NetUSDT, strconv.FormatBool(c.Complete), c.Evidence, strings.Join(c.ExcludedDirectBillRefs, " "), strconv.FormatUint(uint64(c.FactID), 10))
	}
	row()
	row("test_request_id", "attempt", "channel_id", "model", "input_tokens", "output_tokens", "attempt_at", "status", "coverage", "amount", "refund", "currency", "rate", "net_USDT", "net_xcloud_credit", "evidence_type", "evidence", "coverage_evidence", "refund_evidence", "fact_id", "rate_evidence", "supplier_bill_id")
	for _, c := range r.TestCosts {
		input, output := "", ""
		if c.InputTokens != nil {
			input = strconv.FormatInt(*c.InputTokens, 10)
		}
		if c.OutputTokens != nil {
			output = strconv.FormatInt(*c.OutputTokens, 10)
		}
		row(c.RequestID, strconv.Itoa(c.Attempt), strconv.Itoa(c.ChannelID), c.Model, input, output, c.AttemptAt.In(shanghai).Format(time.RFC3339), c.Status, c.Coverage, c.Amount, c.Refund, c.Currency, c.Rate, c.NetUSDT, c.NetCredit, c.EvidenceType, c.Evidence, c.CoverageEvidence, c.RefundEvidence, strconv.FormatUint(uint64(c.FactID), 10), c.RateEvidence, c.SupplierBillID)
	}
	row()
	row("direct_supplier", "direct_account", "direct_bill_id", "verification_id", "channel_id", "model", "input_tokens", "output_tokens", "input_token_scope", "cache_read", "cache_write_5m", "cache_write_1h", "occurred_at", "amount", "currency", "rate", "rate_evidence", "net_USDT", "evidence", "link_evidence", "coverage", "coverage_evidence", "month_cost_fact_id", "identity", "fact_id")
	for _, c := range r.DirectCosts {
		input, output := "", ""
		if c.InputTokens != nil {
			input = strconv.FormatInt(*c.InputTokens, 10)
		}
		if c.OutputTokens != nil {
			output = strconv.FormatInt(*c.OutputTokens, 10)
		}
		count := func(p *int64) string {
			if p == nil {
				return ""
			}
			return strconv.FormatInt(*p, 10)
		}
		scope := c.InputTokenScope
		if scope == "" {
			scope = "unknown"
		}
		row(c.Supplier, c.Account, c.BillID, strconv.FormatInt(c.VerificationID, 10), strconv.Itoa(c.ChannelID), c.Model, input, output, scope, count(c.CacheRead), count(c.CacheWrite5m), count(c.CacheWrite1h), c.OccurredAt.In(shanghai).Format(time.RFC3339), c.Amount, c.Currency, c.Rate, c.RateEvidence, c.NetUSDT, c.Evidence, c.LinkEvidence, c.Coverage, c.CoverageEvidence, strconv.FormatUint(uint64(c.MonthCostFactID), 10), c.Ref, strconv.FormatUint(uint64(c.FactID), 10))
	}
	row()
	row("xcloud_estimate_model", "channel_id", "requests", "revenue_USDT", "estimated_cost_USDT", "estimated_gross_profit_USDT", "quote_discount", "complete", "source_url")
	for _, e := range r.Estimates {
		row(e.SourceModel, strconv.Itoa(e.ChannelID), strconv.Itoa(e.Requests), e.RevenueUSDT, e.CostUSDT, e.GrossProfitUSDT, e.QuoteDiscount, strconv.FormatBool(e.Complete), e.SourceURL)
	}
	row()
	row("issue", "reference", "user_id")
	for _, i := range r.Issues {
		row(i.Code, i.Ref, strconv.Itoa(i.UserID))
	}
	cw.Flush()
}
