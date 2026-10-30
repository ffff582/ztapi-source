package finance

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

func directCostRef(c *DirectCost) string {
	// A duplicate bill ID is rejected even when supplier or account labels differ.
	return fmt.Sprintf("%x", sha256.Sum256([]byte("direct-bill-v1:"+strings.ToLower(c.BillID))))
}

func directVerificationMatches(s Snapshot, c *DirectCost) bool {
	if c == nil || c.OccurredAt.IsZero() {
		return false
	}
	matches := 0
	timeMatches := false
	for _, v := range s.Verifications {
		if v.ID == c.VerificationID && v.ChannelID == c.ChannelID {
			matches++
			at := time.Unix(v.VerifiedAt, 0)
			timeMatches = v.VerifiedAt > 0 && !c.OccurredAt.Before(at.Add(-15*time.Minute)) && !c.OccurredAt.After(at.Add(15*time.Minute))
		}
	}
	return matches == 1 && timeMatches
}

func validateDirectCost(c *DirectCost, now time.Time) error {
	if c == nil || c.VerificationID <= 0 || c.ChannelID <= 0 || c.OccurredAt.IsZero() || c.OccurredAt.After(now) {
		return ErrInvalid
	}
	if len(c.Model) > 128 || strings.TrimSpace(c.Model) != c.Model {
		return ErrInvalid
	}
	if c.InputTokenScope != "" && c.InputTokenScope != "unknown" && c.InputTokenScope != "inclusive" && c.InputTokenScope != "exclusive" {
		return ErrInvalid
	}
	for _, count := range []*int64{c.InputTokens, c.OutputTokens, c.CacheRead, c.CacheWrite5m, c.CacheWrite1h} {
		if count != nil && (*count < 0 || *count > 9007199254740991) {
			return ErrInvalid
		}
	}
	for _, s := range []string{c.Supplier, c.Account, c.BillID} {
		if len(s) < 1 || len(s) > 128 || strings.TrimSpace(s) != s {
			return ErrInvalid
		}
	}
	for _, s := range []string{c.Evidence, c.LinkEvidence} {
		if len(strings.TrimSpace(s)) < 3 || len(s) > 500 {
			return ErrInvalid
		}
	}
	if len(c.RateEvidence) > 500 || len(c.CoverageEvidence) > 500 {
		return ErrInvalid
	}
	if c.Currency != "XCLOUD_CREDIT" && c.Currency != "USDT" && c.Currency != "USD" && c.Currency != "CNY" {
		return ErrInvalid
	}
	if _, err := money(c.Amount, "USDT", "1"); err != nil {
		return ErrInvalid
	}
	if c.Rate != "" {
		currency := c.Currency
		if currency == "XCLOUD_CREDIT" {
			currency = "USD"
		}
		if _, err := money(c.Amount, currency, c.Rate); err != nil {
			return ErrInvalid
		}
		if c.Currency != "USDT" && len(strings.TrimSpace(c.RateEvidence)) < 3 {
			return ErrInvalid
		}
	} else if c.RateEvidence != "" {
		return ErrInvalid
	}
	switch c.Coverage {
	case "unknown":
		if c.MonthCostFactID != 0 || c.CoverageEvidence != "" {
			return ErrInvalid
		}
	case "included", "extra":
		if c.MonthCostFactID == 0 || len(strings.TrimSpace(c.CoverageEvidence)) < 3 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func normalizeDirectCost(c *DirectCost) {
	if c.InputTokenScope == "" {
		c.InputTokenScope = "unknown"
	}
}
