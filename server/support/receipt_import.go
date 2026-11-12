// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package support

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ReceiptImportManifest carries only isolated acceptance usage, never customer
// identity, model dialogue, platform settlements or supplier debit claims.
type ReceiptImportManifest struct {
	Release string           `json:"release"`
	Usages  []FinancialUsage `json:"usages"`
}

type receiptImportAudit struct {
	CallID             string    `gorm:"primaryKey;size:32"`
	Hash               string    `gorm:"size:64;not null"`
	Release            string    `gorm:"size:40;not null"`
	OperatorTelegramID int       `gorm:"not null"`
	CreatedAt          time.Time `gorm:"not null"`
}

func (receiptImportAudit) TableName() string            { return "support_receipt_import_audits" }
func (*receiptImportAudit) BeforeUpdate(*gorm.DB) error { return ErrConflict }
func (*receiptImportAudit) BeforeDelete(*gorm.DB) error { return ErrConflict }

const receiptImportNotice = "Administrative evidence only: administrative acceptance receipt import; not a customer conversation; no original model words, customer settlement, actual debit or refund asserted."
const receiptImportClockTolerance = 30 * time.Second

var receiptImportIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var receiptImportReleasePattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Match the existing finance reader without rewriting or rounding evidence.
var receiptImportAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
var receiptImportOutcomePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func receiptImportMetadata(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func receiptImportAmount(value *string, max string) (decimal.Decimal, bool) {
	if value == nil || !receiptImportAmountPattern.MatchString(*value) {
		return decimal.Zero, false
	}
	amount, err := decimal.NewFromString(*value)
	return amount, err == nil && amount.LessThanOrEqual(decimal.RequireFromString(max))
}

func validReceiptImportUsage(u FinancialUsage, now time.Time) bool {
	if !receiptImportIDPattern.MatchString(u.CallID) || u.Environment != "telegram-acceptance-v2" ||
		u.ChannelID != 8 || u.Supplier != "xcloud" || u.Model != "deepseek-v4.1-flash" ||
		u.Purpose != "customer_service" || u.InputTokenScope != "inclusive" || !u.Dispatched ||
		u.ActualCostU != nil || u.ActualEvidence != "" || !receiptImportOutcomePattern.MatchString(u.Outcome) ||
		!receiptImportMetadata(u.UpstreamRequestID, 128) || !receiptImportMetadata(u.QuoteEvidence, 8192) ||
		u.StartedAt.IsZero() || u.StartedAt.Before(now.Add(-24*time.Hour)) || u.StartedAt.After(now.Add(receiptImportClockTolerance)) {
		return false
	}
	if u.CompletedAt == nil {
		return u.Outcome == "unconfirmed" && u.InputTokens == nil && u.OutputTokens == nil && u.CacheReadTokens == nil &&
			u.QuoteCostU == nil && u.UpstreamCredit == nil && u.UpstreamRequestID == "" && u.QuoteEvidence == ""
	}
	if u.CompletedAt.Before(u.StartedAt) || u.CompletedAt.Before(now.Add(-24*time.Hour)) || u.CompletedAt.After(now.Add(receiptImportClockTolerance)) {
		return false
	}
	for _, tokens := range []*int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens} {
		if tokens != nil && *tokens < 0 {
			return false
		}
	}
	if u.CacheReadTokens != nil && u.InputTokens != nil && *u.CacheReadTokens > *u.InputTokens {
		return false
	}
	if u.UpstreamCredit != nil {
		if _, ok := receiptImportAmount(u.UpstreamCredit, "0.2"); !ok {
			return false
		}
	}
	if u.QuoteCostU != nil {
		quote, ok := receiptImportAmount(u.QuoteCostU, "0.1")
		credit, creditOK := receiptImportAmount(u.UpstreamCredit, "0.2")
		if !ok || !creditOK || !credit.Mul(decimal.RequireFromString("0.5")).Equal(quote) || u.QuoteEvidence == "" {
			return false
		}
		if u.CacheReadTokens == nil && !strings.HasSuffix(u.QuoteEvidence, UnknownCacheUpperBoundSuffix) {
			return false
		}
	}
	return true
}

func receiptImportHash(u FinancialUsage, release string, operator int) (string, error) {
	u.StartedAt = u.StartedAt.UTC()
	if u.CompletedAt != nil {
		completed := u.CompletedAt.UTC()
		u.CompletedAt = &completed
	}
	raw, err := common.Marshal(struct {
		Usage    FinancialUsage `json:"usage"`
		Release  string         `json:"release"`
		Operator int            `json:"operator_telegram_id"`
	}{u, release, operator})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func receiptImportStoredHash(tx *gorm.DB, call callRecord, release string, operator int) (string, error) {
	u := FinancialUsage{CallID: call.ID, StartedAt: call.StartedAt, ChannelID: call.ChannelID,
		Supplier: call.Supplier, Model: call.Model, Environment: call.Environment, Dispatched: call.Dispatched,
		Purpose: "customer_service", InputTokenScope: "inclusive", Outcome: "unconfirmed"}
	var receipt callReceipt
	err := tx.Where("call_id = ?", call.ID).Take(&receipt).Error
	if err == nil {
		u.CompletedAt, u.Outcome = &receipt.CreatedAt, receipt.Outcome
		u.InputTokens, u.OutputTokens, u.CacheReadTokens = receipt.InputTokens, receipt.OutputTokens, receipt.CacheReadTokens
		u.QuoteCostU, u.ActualCostU, u.UpstreamCredit = receipt.QuoteCostU, receipt.ActualCostU, receipt.UpstreamCredit
		u.UpstreamRequestID, u.QuoteEvidence, u.ActualEvidence = receipt.UpstreamRequestID, receipt.QuoteEvidence, receipt.ActualEvidence
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	return receiptImportHash(u, release, operator)
}

// ImportCustomerServiceReceipts is append-only. Callers must additionally hold
// the bot's runtime file lease for the lifetime of their private DB connection.
func (s *Store) ImportCustomerServiceReceipts(ctx context.Context, manifest ReceiptImportManifest, operatorTelegramID int, now time.Time) (int, error) {
	if s == nil || s.db == nil || ctx == nil || now.IsZero() || operatorTelegramID <= 0 ||
		!receiptImportReleasePattern.MatchString(manifest.Release) || len(manifest.Usages) < 1 || len(manifest.Usages) > 5 {
		return 0, ErrConflict
	}
	now = now.UTC()
	hashes := make(map[string]string, len(manifest.Usages))
	for _, usage := range manifest.Usages {
		if !validReceiptImportUsage(usage, now) {
			return 0, ErrConflict
		}
		if _, exists := hashes[usage.CallID]; exists {
			return 0, ErrConflict
		}
		hash, err := receiptImportHash(usage, manifest.Release, operatorTelegramID)
		if err != nil {
			return 0, err
		}
		hashes[usage.CallID] = hash
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	added := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		hasAudit := tx.Migrator().HasTable(&receiptImportAudit{})
		pending := make([]FinancialUsage, 0, len(manifest.Usages))
		// Preflight the whole batch before creating even the private audit table.
		for _, usage := range manifest.Usages {
			var call callRecord
			callErr := tx.Where("id = ?", usage.CallID).Take(&call).Error
			if callErr != nil && !errors.Is(callErr, gorm.ErrRecordNotFound) {
				return callErr
			}
			var audit receiptImportAudit
			auditErr := gorm.ErrRecordNotFound
			if hasAudit {
				auditErr = tx.Where("call_id = ?", usage.CallID).Take(&audit).Error
			}
			if auditErr != nil && !errors.Is(auditErr, gorm.ErrRecordNotFound) {
				return auditErr
			}
			if callErr == nil || auditErr == nil {
				if callErr != nil || auditErr != nil || audit.Hash != hashes[usage.CallID] || audit.Release != manifest.Release ||
					audit.OperatorTelegramID != operatorTelegramID || call.UserID != operatorTelegramID ||
					call.RequestHash != audit.Hash || call.IdempotencyKey != keyFor(operatorTelegramID, "acceptance_receipt_import", usage.CallID) {
					return ErrConflict
				}
				storedHash, err := receiptImportStoredHash(tx, call, manifest.Release, operatorTelegramID)
				if err != nil {
					return err
				}
				if storedHash != audit.Hash {
					return ErrConflict
				}
				continue
			}
			var orphanReceipt int64
			if err := tx.Model(&callReceipt{}).Where("call_id = ?", usage.CallID).Count(&orphanReceipt).Error; err != nil {
				return err
			}
			if orphanReceipt != 0 {
				return ErrConflict
			}
			pending = append(pending, usage)
		}
		if len(pending) == 0 {
			return nil
		}
		if !hasAudit {
			if err := tx.Migrator().CreateTable(&receiptImportAudit{}); err != nil {
				return err
			}
			if tx.Dialector.Name() == "sqlite" {
				for _, sql := range []string{
					"CREATE TRIGGER support_receipt_import_audits_no_replace BEFORE INSERT ON support_receipt_import_audits WHEN EXISTS (SELECT 1 FROM support_receipt_import_audits WHERE call_id=NEW.call_id) BEGIN SELECT RAISE(ABORT, 'support import audit is immutable'); END",
					"CREATE TRIGGER support_receipt_import_audits_no_update BEFORE UPDATE ON support_receipt_import_audits BEGIN SELECT RAISE(ABORT, 'support import audit is immutable'); END",
					"CREATE TRIGGER support_receipt_import_audits_no_delete BEFORE DELETE ON support_receipt_import_audits BEGIN SELECT RAISE(ABORT, 'support import audit is immutable'); END",
				} {
					if err := tx.Exec(sql).Error; err != nil {
						return err
					}
				}
			}
		}
		for _, usage := range pending {
			id, err := newID()
			if err != nil {
				return err
			}
			conversation := Conversation{ID: id, UserID: operatorTelegramID, Title: "Administrative acceptance receipt import",
				IdempotencyKey: keyFor(operatorTelegramID, "acceptance_receipt_conversation", usage.CallID), CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&conversation).Error; err != nil {
				return err
			}
			message := Message{ConversationID: id, Role: "system", Content: receiptImportNotice, CreatedAt: now}
			if err := tx.Create(&message).Error; err != nil {
				return err
			}
			call := callRecord{ID: usage.CallID, UserID: operatorTelegramID, ConversationID: id, UserMessageID: message.ID,
				IdempotencyKey: keyFor(operatorTelegramID, "acceptance_receipt_import", usage.CallID), RequestHash: hashes[usage.CallID],
				ChannelID: usage.ChannelID, Supplier: usage.Supplier, Model: usage.Model, Environment: usage.Environment,
				Dispatched: usage.Dispatched, StartedAt: usage.StartedAt.UTC()}
			if err := tx.Create(&call).Error; err != nil {
				return err
			}
			if usage.CompletedAt != nil {
				reply := Message{ConversationID: id, Role: "system", Content: receiptImportNotice, CreatedAt: now}
				if err := tx.Create(&reply).Error; err != nil {
					return err
				}
				receipt := callReceipt{CallID: usage.CallID, ReplyMessageID: reply.ID, Outcome: usage.Outcome,
					InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadTokens: usage.CacheReadTokens,
					QuoteCostU: usage.QuoteCostU, UpstreamCredit: usage.UpstreamCredit, UpstreamRequestID: usage.UpstreamRequestID,
					QuoteEvidence: usage.QuoteEvidence, CreatedAt: usage.CompletedAt.UTC()}
				if err := tx.Create(&receipt).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&receiptImportAudit{CallID: usage.CallID, Hash: hashes[usage.CallID], Release: manifest.Release,
				OperatorTelegramID: operatorTelegramID, CreatedAt: now}).Error; err != nil {
				return err
			}
			added++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}
