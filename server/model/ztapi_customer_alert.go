package model

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

// Called under the model health write lock. Notification evidence is separate
// from circuit evidence: it cannot unpublish a model or authorize a paid probe.
func enqueueZTAPICustomerAlertTx(tx *gorm.DB, r *ZTAPIHealthRequest, event ZTAPIHealthEvent, outcome types.ZTAPIHealthOutcome, now int64) error {
	if r.Source != "real" || r.UserID <= 0 || outcome.Result != "suspected" || !outcome.Dispatched || outcome.ClientCancelled {
		return nil
	}
	prefix := fmt.Sprintf("customer:%d:%d:%d:", r.ModelID, r.UserID, event.ChannelID)
	var existing ZTAPIHealthOutbox
	err := tx.Where("kind = ? AND dedup_key LIKE ? AND created_at > ?", "customer_alert", prefix+"%", now-600).
		Order("id DESC").First(&existing).Error
	if err == nil {
		return tx.Model(&existing).UpdateColumn("occurrences", gorm.Expr("occurrences + 1")).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&ZTAPIHealthOutbox{
		DedupKey: fmt.Sprintf("%s%d", prefix, event.ID), Kind: "customer_alert",
		ModelID: r.ModelID, Generation: r.Generation, EventID: event.ID,
		Status: "pending", CreatedAt: now, NextAttemptAt: now, Occurrences: 1,
	}).Error
}
