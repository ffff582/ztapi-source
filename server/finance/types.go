// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import "time"

type User struct {
	ID        int
	Username  string
	Role      int
	Status    int
	DeletedAt *time.Time
}
type Ledger struct {
	ID, UserID                            int
	Delta, BalanceBefore, BalanceAfter    int64
	SourceType, RequestID, IdempotencyKey string
	CreatedAt                             time.Time
}
type Settlement struct {
	ID                                          uint
	UserID, FinalAttempt                        int
	RequestID, OperationID, Status              string
	PublicModel, PriceSnapshotJSON              string
	ChargeDimensionsJSON, MissingDimensionsJSON string
	ReservedQuota, ChargedQuota, CompletedAt    int64
	CreatedAt                                   time.Time
}
type Attempt struct {
	SettlementID       uint
	Attempt, ChannelID int
	CreatedAt          time.Time
}
type Charge struct {
	ChannelID                int
	PostedAt                 int64
	ID                       uint
	RequestID                string
	UserID, OriginalLedgerID int
	ChargedQuota             int64
	BillingProofID           uint
}
type Refund struct {
	LedgerID      int
	ChargeID      uint
	RefundedQuota int64
}
type Payment struct {
	ID, UserID                                 int
	TradeNo, Status                            string
	CreditUnits, PayAmountMicros, CompleteTime int64
}
type Channel struct {
	ID      int
	Name    string
	BaseURL string
}
type Snapshot struct {
	SourceCommit  string
	QuotaPerUnit  int64
	Users         []User
	Ledgers       []Ledger
	Settlements   []Settlement
	Attempts      []Attempt
	Payments      []Payment
	Channels      []Channel
	Charges       []Charge
	Refunds       []Refund
	Verifications []Verification
}
type Verification struct {
	ID            int64
	ChannelID     int
	ModelConfigID int
	VerifiedAt    int64
}
type Funding struct {
	Kind       string `json:"kind"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	Rate       string `json:"rate"`
	ReceivedAt int64  `json:"received_at"`
	Evidence   string `json:"evidence"`
}
type Cost struct {
	Month                  string   `json:"month"`
	ChannelID              int      `json:"channel_id"`
	Supplier               string   `json:"supplier"`
	Gross                  string   `json:"gross"`
	Refund                 string   `json:"refund"`
	RefundEvidence         string   `json:"refund_evidence"`
	Currency               string   `json:"currency"`
	Rate                   string   `json:"rate"`
	Complete               bool     `json:"complete"`
	Evidence               string   `json:"evidence"`
	ExcludedDirectBillRefs []string `json:"excluded_direct_bill_refs,omitempty"`
}
type TestCost struct {
	RequestID        string `json:"request_id"`
	Attempt          int    `json:"attempt"`
	ChannelID        int    `json:"channel_id"`
	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	Status           string `json:"status"`
	EvidenceType     string `json:"evidence_type"`
	Coverage         string `json:"coverage"`
	Amount           string `json:"amount"`
	Refund           string `json:"refund"`
	RefundEvidence   string `json:"refund_evidence"`
	Currency         string `json:"currency"`
	Rate             string `json:"rate"`
	RateEvidence     string `json:"rate_evidence,omitempty"`
	Evidence         string `json:"evidence"`
	CoverageEvidence string `json:"coverage_evidence"`
	SupplierBillID   string `json:"supplier_bill_id,omitempty"`
}

// DirectCost identifies a supplier bill independently of platform requests.
type DirectCost struct {
	Supplier         string    `json:"supplier"`
	Account          string    `json:"account"`
	BillID           string    `json:"bill_id"`
	VerificationID   int64     `json:"verification_id"`
	ChannelID        int       `json:"channel_id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Model            string    `json:"model,omitempty"`
	InputTokens      *int64    `json:"input_tokens"`
	OutputTokens     *int64    `json:"output_tokens"`
	InputTokenScope  string    `json:"input_token_scope"`
	CacheRead        *int64    `json:"cache_read"`
	CacheWrite5m     *int64    `json:"cache_write_5m"`
	CacheWrite1h     *int64    `json:"cache_write_1h"`
	Amount           string    `json:"amount"`
	Currency         string    `json:"currency"`
	Rate             string    `json:"rate"`
	RateEvidence     string    `json:"rate_evidence"`
	Evidence         string    `json:"evidence"`
	LinkEvidence     string    `json:"link_evidence"`
	Coverage         string    `json:"coverage"`
	CoverageEvidence string    `json:"coverage_evidence"`
	MonthCostFactID  uint      `json:"month_cost_fact_id"`
}

type DirectCostRow struct {
	DirectCost
	FactID  uint   `json:"fact_id"`
	Ref     string `json:"ref"`
	NetUSDT string `json:"net_usdt,omitempty"`
}
type Fact struct {
	ID         uint      `json:"id"`
	Kind       string    `json:"kind"`
	Ref        string    `json:"ref"`
	Payload    any       `json:"payload"`
	OperatorID int       `json:"operator_id"`
	CreatedAt  time.Time `json:"created_at"`
	PreviousID uint      `json:"previous_id"`
}
type Issue struct {
	Code   string `json:"code"`
	Ref    string `json:"ref"`
	UserID int    `json:"user_id,omitempty"`
}
type FundingRow struct {
	Ref            string    `json:"ref"`
	UserID         int       `json:"user_id"`
	Username       string    `json:"username"`
	Quota          int64     `json:"quota"`
	Source         string    `json:"source"`
	CreatedAt      time.Time `json:"created_at"`
	FactID         uint      `json:"fact_id"`
	Classification *Funding  `json:"classification,omitempty"`
}
type Receipt struct {
	Ref        string `json:"ref"`
	UserID     int    `json:"user_id"`
	Username   string `json:"username"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	Rate       string `json:"rate"`
	AmountUSDT string `json:"amount_usdt"`
	Quota      int64  `json:"quota"`
	ReceivedAt int64  `json:"received_at"`
	Evidence   string `json:"evidence"`
}
type ChannelUsage struct {
	ChannelID int    `json:"channel_id"`
	Name      string `json:"name"`
	Attempts  int    `json:"attempts"`
}
type CostRow struct {
	Cost
	FactID  uint   `json:"fact_id"`
	NetUSDT string `json:"net_usdt"`
}
type TestCostRow struct {
	TestCost
	FactID    uint      `json:"fact_id"`
	AttemptAt time.Time `json:"attempt_at"`
	Model     string    `json:"model"`
	NetUSDT   string    `json:"net_usdt"`
	NetCredit string    `json:"net_credit,omitempty"`
}
type SupplierEstimateRow struct {
	ChannelID       int              `json:"channel_id"`
	Supplier        string           `json:"supplier"`
	SourceModel     string           `json:"source_model"`
	Requests        int              `json:"requests"`
	Dimensions      map[string]int64 `json:"dimensions"`
	RevenueUSDT     string           `json:"revenue_usdt"`
	CostUSDT        string           `json:"cost_usdt"`
	GrossProfitUSDT string           `json:"gross_profit_usdt"`
	SourceURL       string           `json:"source_url"`
	QuoteDiscount   string           `json:"quote_discount"`
	IsEstimate      bool             `json:"is_estimate"`
	Complete        bool             `json:"complete"`
}
type Report struct {
	SourceCommit            string                `json:"source_commit,omitempty"`
	Month                   string                `json:"month"`
	Timezone                string                `json:"timezone"`
	Currency                string                `json:"currency"`
	AsOf                    time.Time             `json:"as_of"`
	PeriodClosed            bool                  `json:"period_closed"`
	Confirmed               bool                  `json:"confirmed"`
	Revenue                 *string               `json:"revenue"`
	KnownRevenue            string                `json:"known_revenue"`
	ActualCost              *string               `json:"actual_cost"`
	RecordedCost            string                `json:"recorded_cost"`
	GrossProfit             *string               `json:"gross_profit"`
	Margin                  *string               `json:"margin"`
	EstimatedCost           *string               `json:"estimated_cost"`
	EstimatedGrossProfit    *string               `json:"estimated_gross_profit"`
	EstimateComplete        bool                  `json:"estimate_complete"`
	ReceiptsTotal           string                `json:"receipts_total"`
	ChargedQuota            int64                 `json:"charged_quota"`
	PendingRequests         int                   `json:"pending_requests"`
	PendingQuota            int64                 `json:"pending_quota"`
	QuotaPerUnit            int64                 `json:"quota_per_unit"`
	Receipts                []Receipt             `json:"receipts"`
	ManualFunding           []FundingRow          `json:"manual_funding"`
	Channels                []ChannelUsage        `json:"channels"`
	Costs                   []CostRow             `json:"costs"`
	TestCosts               []TestCostRow         `json:"test_costs"`
	DirectCosts             []DirectCostRow       `json:"direct_costs"`
	DirectXcloudCreditTotal string                `json:"direct_xcloud_credit_total"`
	DirectConfirmedTotal    string                `json:"direct_confirmed_total"`
	TestConfirmedTotal      string                `json:"test_confirmed_total"`
	TestEstimateTotal       string                `json:"test_estimate_total"`
	TestXcloudCreditTotal   string                `json:"test_xcloud_credit_total"`
	Estimates               []SupplierEstimateRow `json:"estimates"`
	EstimateIssues          []Issue               `json:"estimate_issues"`
	Issues                  []Issue               `json:"issues"`
	IssueCount              int                   `json:"issue_count"`
}
