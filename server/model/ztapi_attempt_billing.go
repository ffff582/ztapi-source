package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

var (
	ErrZTAPIAttemptBillingPending  = errors.New("attempt billing requires reconciliation")
	ErrZTAPIAttemptBillingConflict = errors.New("attempt billing evidence conflicts")
	ErrZTAPIAttemptBillingInvalid  = errors.New("invalid attempt billing evidence")
)

// Quantities are normalized, mutually exclusive token buckets. input_tokens
// excludes cache buckets; output_tokens includes any billable reasoning once.
// Supported dimensions: input_tokens, output_tokens, cache_read, cache_write,
// cache_write_5m, cache_write_1h. No price, quota, currency or rate is submitted.
type ZTAPIAttemptBillingQuantity struct {
	Dimension string `json:"dimension"`
	Quantity  int64  `json:"quantity"`
}

type ZTAPIAttemptBillingSubmission struct {
	Source                 string                        `json:"source"`
	ProofID                string                        `json:"proof_id"`
	RequestID              string                        `json:"request_id"`
	UserID                 int                           `json:"user_id"`
	Attempt                int                           `json:"attempt"`
	ChannelID              int                           `json:"channel_id"`
	CredentialVersion      string                        `json:"credential_version"`
	UpstreamRequestID      string                        `json:"upstream_request_id"`
	UpstreamBillID         string                        `json:"upstream_bill_id"`
	Kind                   string                        `json:"kind"`
	UsageSemantic          string                        `json:"usage_semantic"`
	Usage                  []ZTAPIAttemptBillingQuantity `json:"usage"`
	EvidenceReference      string                        `json:"evidence_reference"`
	DistinctUsageReference string                        `json:"distinct_usage_reference"`
}

type ZTAPIAttemptBillingPriced struct {
	Dimensions []ZTAPISupplierRefundDimension `json:"dimensions"`
	ConsumeLog Log                            `json:"consume_log"`
}

// These callbacks are server code, never JSON inputs. Pricing runs once at
// approval against the locked parent's frozen snapshot and must do no I/O.
type ZTAPIAttemptBillingPricer func(parent ZTAPIRequestSettlement, submission ZTAPIAttemptBillingSubmission) (ZTAPIAttemptBillingPriced, error)
type ZTAPIAttemptBillingLogEnqueuer func(tx *gorm.DB, parent *ZTAPIRequestSettlement, charge *ZTAPISupplierRefundCharge, operationID string, log Log) error

type ZTAPIAttemptBillingReview struct {
	ID             uint   `gorm:"primaryKey"`
	SettlementID   uint   `gorm:"not null;index"`
	RequestID      string `gorm:"type:varchar(128);not null;uniqueIndex:ztapi_attempt_review_identity,priority:1"`
	Attempt        int    `gorm:"not null;uniqueIndex:ztapi_attempt_review_identity,priority:2"`
	Status         string `gorm:"type:varchar(32);not null;index"`
	PendingReason  string `gorm:"type:varchar(64);not null"`
	AppliedProofID uint   `gorm:"not null"`
	ChargeID       uint   `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (ZTAPIAttemptBillingReview) TableName() string { return "ztapi_attempt_billing_reviews" }

type ZTAPIAttemptBillingProof struct {
	ID             uint   `gorm:"primaryKey"`
	ProofKey       string `gorm:"type:char(64);not null;uniqueIndex"`
	Source         string `gorm:"type:varchar(128);not null"`
	ProofID        string `gorm:"type:varchar(256);not null"`
	RequestID      string `gorm:"type:varchar(128);not null;index"`
	Attempt        int    `gorm:"not null"`
	SubmissionJSON string `gorm:"type:text;not null"`
	PayloadHash    string `gorm:"type:char(64);not null"`
	Status         string `gorm:"type:varchar(16);not null;index"`
	PendingReason  string `gorm:"type:varchar(64);not null"`
	ChargeID       uint   `gorm:"not null"`
	LastAttemptAt  int64  `gorm:"type:bigint;not null;default:0;index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (ZTAPIAttemptBillingProof) TableName() string { return "ztapi_attempt_billing_proofs" }

type ZTAPIAttemptBillingApproval struct {
	ID                           uint    `gorm:"primaryKey"`
	ProofID                      uint    `gorm:"not null;uniqueIndex"`
	ReviewID                     uint    `gorm:"not null;uniqueIndex"`
	OperatorID                   int     `gorm:"not null"`
	VerificationReference        string  `gorm:"type:varchar(512);not null"`
	PayloadHash                  string  `gorm:"type:char(64);not null"`
	PriceSnapshotHash            string  `gorm:"type:char(64);not null"`
	PricedJSON                   string  `gorm:"type:text;not null"`
	PricedHash                   string  `gorm:"type:char(64);not null"`
	ApplyTarget                  string  `gorm:"type:varchar(32);not null"`
	UsageIdentityKey             *string `gorm:"type:char(64);uniqueIndex"`
	PendingUsageJSON             string  `gorm:"type:text;not null"`
	PendingMissingDimensionsJSON string  `gorm:"type:text;not null"`
	CreatedAt                    time.Time
}

func (ZTAPIAttemptBillingApproval) TableName() string { return "ztapi_attempt_billing_approvals" }
func (*ZTAPIAttemptBillingApproval) BeforeUpdate(*gorm.DB) error {
	return ErrZTAPIAttemptBillingConflict
}
func (*ZTAPIAttemptBillingApproval) BeforeDelete(*gorm.DB) error {
	return ErrZTAPIAttemptBillingConflict
}
