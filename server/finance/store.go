// Copyright (C) 2026 QuantumNous. SPDX-License-Identifier: AGPL-3.0-or-later
package finance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrConflict = errors.New("financial record conflict")
var ErrInvalid = errors.New("invalid financial record")
var fundingRef = regexp.MustCompile(`^(ledger|opening):[1-9][0-9]{0,10}$`)
var directIdentityPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Submission struct {
	Kind           string      `json:"kind"`
	Ref            string      `json:"ref"`
	Funding        *Funding    `json:"funding,omitempty"`
	Cost           *Cost       `json:"cost,omitempty"`
	TestCost       *TestCost   `json:"test_cost,omitempty"`
	DirectCost     *DirectCost `json:"direct_cost,omitempty"`
	PreviousID     uint        `json:"previous_id"`
	IdempotencyKey string      `json:"idempotency_key"`
}
type factRecord struct {
	ID             uint      `gorm:"primaryKey"`
	Kind           string    `gorm:"size:16;not null;index:finance_identity,priority:1"`
	Ref            string    `gorm:"size:64;not null;index:finance_identity,priority:2"`
	PreviousID     uint      `gorm:"not null"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex"`
	Payload        string    `gorm:"type:text;not null"`
	PayloadHash    string    `gorm:"size:64;not null"`
	OperatorID     int       `gorm:"not null"`
	CreatedAt      time.Time `gorm:"not null"`
}

func (factRecord) TableName() string            { return "financial_facts" }
func (*factRecord) BeforeUpdate(*gorm.DB) error { return errors.New("financial facts are immutable") }
func (*factRecord) BeforeDelete(*gorm.DB) error { return errors.New("financial facts are immutable") }

type directCostIdentity struct {
	Identity string `gorm:"primaryKey;size:64"`
	LatestID uint   `gorm:"not null"`
}

func (directCostIdentity) TableName() string { return "financial_direct_cost_identities" }

type Store struct {
	db *gorm.DB
	mu sync.Mutex
}

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	if e := db.AutoMigrate(&factRecord{}, &directCostIdentity{}); e != nil {
		return nil, e
	}
	return &Store{db: db}, nil
}
func validateSubmission(in Submission, now time.Time) error {
	if len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 128 || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey {
		return ErrInvalid
	}
	if in.Kind != "direct_cost" && in.DirectCost != nil {
		return ErrInvalid
	}
	switch in.Kind {
	case "funding":
		f := in.Funding
		if f == nil || in.Cost != nil || in.TestCost != nil || !fundingRef.MatchString(in.Ref) || len(f.Evidence) < 3 || len(f.Evidence) > 500 {
			return ErrInvalid
		}
		switch f.Kind {
		case "cash":
			a, e := money(f.Amount, f.Currency, f.Rate)
			if e != nil || !a.IsPositive() || f.ReceivedAt <= 0 || f.ReceivedAt > now.Unix() {
				return ErrInvalid
			}
		case "gift", "internal", "correction":
			if f.Amount != "" && f.Amount != "0" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	case "cost":
		c := in.Cost
		if c == nil || in.Funding != nil || in.TestCost != nil || c.ChannelID <= 0 || len(c.Supplier) < 2 || len(c.Supplier) > 128 || len(c.Evidence) < 3 || len(c.Evidence) > 500 || in.Ref != fmt.Sprintf("%s:%d", c.Month, c.ChannelID) {
			return ErrInvalid
		}
		seen := map[string]bool{}
		if len(c.ExcludedDirectBillRefs) > 100 {
			return ErrInvalid
		}
		for _, ref := range c.ExcludedDirectBillRefs {
			if !directIdentityPattern.MatchString(ref) || seen[ref] {
				return ErrInvalid
			}
			seen[ref] = true
		}
		_, end, e := MonthBounds(c.Month)
		if e != nil || c.Complete && now.Before(end) {
			return ErrInvalid
		}
		g, ge := money(c.Gross, c.Currency, c.Rate)
		r, re := money(c.Refund, c.Currency, c.Rate)
		if ge != nil || re != nil || r.IsPositive() && (len(c.RefundEvidence) < 3 || len(c.RefundEvidence) > 500) {
			return ErrInvalid
		}
		_ = g
	case "test_cost":
		c := in.TestCost
		if c == nil || in.Funding != nil || in.Cost != nil || len(c.RequestID) == 0 || len(c.RequestID) > 128 || strings.TrimSpace(c.RequestID) != c.RequestID || c.Attempt <= 0 || c.ChannelID <= 0 || in.Ref != fmt.Sprintf("test:%s:%d:%d", c.RequestID, c.Attempt, c.ChannelID) {
			return ErrInvalid
		}
		if (c.InputTokens != nil && *c.InputTokens < 0) || (c.OutputTokens != nil && *c.OutputTokens < 0) {
			return ErrInvalid
		}
		if len(c.SupplierBillID) > 128 || strings.TrimSpace(c.SupplierBillID) != c.SupplierBillID {
			return ErrInvalid
		}
		if (c.Status != "estimate" && c.Status != "confirmed") || (c.Coverage != "included" && c.Coverage != "extra" && c.Coverage != "unknown") || len(strings.TrimSpace(c.Evidence)) < 3 || len(c.Evidence) > 500 || len(c.CoverageEvidence) > 500 || (c.Coverage != "unknown" && len(strings.TrimSpace(c.CoverageEvidence)) < 3) {
			return ErrInvalid
		}
		if c.Status == "confirmed" && c.EvidenceType != "supplier_statement" || c.Status == "estimate" && c.EvidenceType != "quote" && c.EvidenceType != "other" {
			return ErrInvalid
		}
		currency, rate := c.Currency, c.Rate
		if currency == "XCLOUD_CREDIT" {
			if c.Status != "confirmed" || c.EvidenceType != "supplier_statement" {
				return ErrInvalid
			}
			if rate == "" {
				if c.Coverage != "unknown" {
					return ErrInvalid
				}
				currency, rate = "USDT", "1"
			} else {
				if len(strings.TrimSpace(c.RateEvidence)) < 3 || len(c.RateEvidence) > 500 {
					return ErrInvalid
				}
				currency = "USD"
			}
		}
		a, ae := money(c.Amount, currency, rate)
		refund, re := money(c.Refund, currency, rate)
		if ae != nil || re != nil || refund.GreaterThan(a) || (refund.IsPositive() && (c.Status != "confirmed" || len(strings.TrimSpace(c.RefundEvidence)) < 3)) || len(c.RefundEvidence) > 500 {
			return ErrInvalid
		}
	case "direct_cost":
		if in.Funding != nil || in.Cost != nil || in.TestCost != nil || validateDirectCost(in.DirectCost, now) != nil || in.Ref != directCostRef(in.DirectCost) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func unpack(row factRecord) (Fact, error) {
	f := Fact{ID: row.ID, Kind: row.Kind, Ref: row.Ref, PreviousID: row.PreviousID, OperatorID: row.OperatorID, CreatedAt: row.CreatedAt}
	switch row.Kind {
	case "funding":
		var p Funding
		if e := common.UnmarshalJsonStr(row.Payload, &p); e != nil {
			return f, e
		}
		f.Payload = p
	case "cost":
		var p Cost
		if e := common.UnmarshalJsonStr(row.Payload, &p); e != nil {
			return f, e
		}
		f.Payload = p
	case "test_cost":
		var p TestCost
		if e := common.UnmarshalJsonStr(row.Payload, &p); e != nil {
			return f, e
		}
		f.Payload = p
	case "direct_cost":
		var p DirectCost
		if e := common.UnmarshalJsonStr(row.Payload, &p); e != nil {
			return f, e
		}
		normalizeDirectCost(&p)
		f.Payload = p
	default:
		return f, ErrInvalid
	}
	return f, nil
}
func (s *Store) List(ctx context.Context) ([]Fact, error) {
	var rows []factRecord
	if e := s.db.WithContext(ctx).Order("id").Limit(10001).Find(&rows).Error; e != nil {
		return nil, e
	}
	if len(rows) > 10000 {
		return nil, errors.New("financial fact limit exceeded")
	}
	facts := make([]Fact, 0, len(rows))
	for _, row := range rows {
		f, e := unpack(row)
		if e != nil {
			return nil, e
		}
		facts = append(facts, f)
	}
	return facts, nil
}
func (s *Store) Append(ctx context.Context, in Submission, operator int, now time.Time) (Fact, error) {
	if operator <= 0 {
		return Fact{}, ErrInvalid
	}
	if in.Kind == "direct_cost" && in.DirectCost != nil {
		copy := *in.DirectCost
		normalizeDirectCost(&copy)
		in.DirectCost = &copy
		in.Ref = directCostRef(in.DirectCost)
	}
	if e := validateSubmission(in, now); e != nil {
		return Fact{}, e
	}
	var payload any = in.Funding
	if in.Kind == "cost" {
		payload = in.Cost
	}
	if in.Kind == "test_cost" {
		payload = in.TestCost
	}
	if in.Kind == "direct_cost" {
		payload = in.DirectCost
	}
	raw, e := common.Marshal(payload)
	if e != nil {
		return Fact{}, e
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d\n%s", in.Kind, in.Ref, in.PreviousID, raw))))
	s.mu.Lock()
	defer s.mu.Unlock()
	var saved factRecord
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing factRecord
		err := tx.Where("idempotency_key = ?", in.IdempotencyKey).Take(&existing).Error
		if err == nil {
			if existing.PayloadHash != digest || existing.OperatorID != operator {
				if existing.OperatorID != operator || in.Kind != "direct_cost" || existing.Kind != in.Kind || existing.Ref != in.Ref || existing.PreviousID != in.PreviousID {
					return ErrConflict
				}
				var previous DirectCost
				if common.UnmarshalJsonStr(existing.Payload, &previous) != nil {
					return ErrConflict
				}
				normalizeDirectCost(&previous)
				current := *in.DirectCost
				previous.OccurredAt = previous.OccurredAt.UTC()
				current.OccurredAt = current.OccurredAt.UTC()
				if !reflect.DeepEqual(previous, current) {
					return ErrConflict
				}
			}
			saved = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var latest factRecord
		err = tx.Where("kind = ? AND ref = ?", in.Kind, in.Ref).Order("id DESC").Take(&latest).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if latest.ID != in.PreviousID {
			return ErrConflict
		}
		identity := ""
		if in.Kind == "direct_cost" {
			identity = in.Ref
		} else if in.Kind == "test_cost" && in.TestCost.SupplierBillID != "" {
			identity = directCostRef(&DirectCost{BillID: in.TestCost.SupplierBillID})
			// Bills recorded before the identity table also remain reserved.
			var legacy factRecord
			err = tx.Where("kind = ? AND ref = ?", "direct_cost", identity).Take(&legacy).Error
			if err == nil {
				return ErrConflict
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if identity != "" {
			head := directCostIdentity{Identity: identity, LatestID: latest.ID}
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&head).Error; err != nil {
				return err
			}
			if err = tx.Where("identity = ?", identity).Take(&head).Error; err != nil {
				return err
			}
			if head.LatestID != in.PreviousID {
				return ErrConflict
			}
		}
		if in.Kind == "cost" {
			var costs []factRecord
			if err = tx.Where("kind = ?", "cost").Limit(10001).Find(&costs).Error; err != nil {
				return err
			}
			if len(costs) > 10000 {
				return ErrInvalid
			}
			latestCosts := map[string]factRecord{}
			for _, row := range costs {
				if row.ID > latestCosts[row.Ref].ID {
					latestCosts[row.Ref] = row
				}
			}
			for _, row := range latestCosts {
				if row.Ref == in.Ref {
					continue
				}
				var c Cost
				if common.UnmarshalJsonStr(row.Payload, &c) != nil {
					return ErrInvalid
				}
				if c.Month == in.Cost.Month && c.Evidence == in.Cost.Evidence {
					return ErrConflict
				}
				if in.Cost.RefundEvidence != "" && c.RefundEvidence == in.Cost.RefundEvidence {
					return ErrConflict
				}
			}
		}
		var credit string
		if in.Kind == "cost" && in.Cost.Refund != "0" {
			credit = in.Cost.RefundEvidence
		}
		if in.Kind == "test_cost" && in.TestCost.Coverage == "extra" && in.TestCost.Refund != "0" {
			credit = in.TestCost.RefundEvidence
		}
		if credit != "" {
			var rows []factRecord
			if err = tx.Where("kind IN ?", []string{"cost", "test_cost"}).Limit(10001).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) > 10000 {
				return ErrInvalid
			}
			current := map[string]factRecord{}
			for _, row := range rows {
				key := row.Kind + ":" + row.Ref
				if row.ID > current[key].ID {
					current[key] = row
				}
			}
			for _, row := range current {
				if row.Kind == in.Kind && row.Ref == in.Ref {
					continue
				}
				if row.Kind == "cost" {
					var c Cost
					if common.UnmarshalJsonStr(row.Payload, &c) != nil {
						return ErrInvalid
					}
					if c.RefundEvidence == credit {
						return ErrConflict
					}
				}
				if row.Kind == "test_cost" {
					var c TestCost
					if common.UnmarshalJsonStr(row.Payload, &c) != nil {
						return ErrInvalid
					}
					if c.Coverage == "extra" && c.RefundEvidence == credit {
						return ErrConflict
					}
				}
			}
		}
		saved = factRecord{Kind: in.Kind, Ref: in.Ref, PreviousID: in.PreviousID, IdempotencyKey: in.IdempotencyKey, Payload: string(raw), PayloadHash: digest, OperatorID: operator, CreatedAt: now.UTC()}
		if err = tx.Create(&saved).Error; err != nil {
			return err
		}
		if identity != "" {
			res := tx.Model(&directCostIdentity{}).Where("identity = ? AND latest_id = ?", identity, in.PreviousID).Update("latest_id", saved.ID)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrConflict
			}
		}
		return nil
	})
	if e != nil {
		return Fact{}, e
	}
	return unpack(saved)
}
