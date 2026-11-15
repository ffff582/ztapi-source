package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ReleaseZTAPIExpiredWorkbenchHolds only returns unused reservations after the
// scoped Key has expired. It never decides whether a provider charged a request.
func ReleaseZTAPIExpiredWorkbenchHolds(now time.Time, limit int) (int, error) {
	if DB == nil || now.IsZero() || limit < 1 || limit > 100 {
		return 0, ErrZTAPISettlementInvalid
	}
	cutoff := now.UTC().Add(-time.Minute)
	var operations []string
	err := DB.Model(&ZTAPIRequestSettlement{}).
		Joins("JOIN tokens ON tokens.id = ztapi_request_settlements.token_id AND tokens.user_id = ztapi_request_settlements.user_id").
		Where("ztapi_request_settlements.status = ? AND ztapi_request_settlements.dispatched = ? AND workbench_task_id <> '' AND workbench_outbound_max > 0 AND ztapi_request_settlements.created_at <= ?", ZTAPISettlementReserved, false, cutoff).
		Where("tokens.workbench_only = ? AND tokens.expired_time > 0 AND tokens.expired_time <= ?", true, cutoff.Unix()).
		Where("NOT EXISTS (SELECT 1 FROM ztapi_request_attempts WHERE settlement_id = ztapi_request_settlements.id)").
		Where("NOT EXISTS (SELECT 1 FROM ztapi_settlement_finalization_intents WHERE request_id = ztapi_request_settlements.request_id)").
		Order("ztapi_request_settlements.id").Limit(limit).Pluck("ztapi_request_settlements.operation_id", &operations).Error
	if err != nil {
		return 0, err
	}
	var firstErr error
	released := 0
	for _, op := range operations {
		_, err := releaseZTAPIRequestSettlement(op, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
			if row.Status != ZTAPISettlementReserved || row.Dispatched || row.WorkbenchTaskID == "" ||
				row.WorkbenchOutboundMax < 1 || row.CreatedAt.After(cutoff) {
				return ErrZTAPISettlementConflict
			}
			_, key, err := ztapiSettlementOwners(tx, row)
			if err != nil {
				return err
			}
			if key.UserId != row.UserID || !key.WorkbenchOnly || key.ExpiredTime < 1 || key.ExpiredTime > cutoff.Unix() {
				return ErrZTAPISettlementConflict
			}
			var attempts int64
			if err := tx.Model(&ZTAPIRequestAttempt{}).Where("settlement_id = ?", row.ID).Count(&attempts).Error; err != nil {
				return err
			}
			if attempts != 0 {
				return ErrZTAPISettlementConflict
			}
			return nil
		})
		switch {
		case err == nil:
			released++
		case errors.Is(err, ErrZTAPISettlementConflict), errors.Is(err, ErrZTAPISettlementPending):
			// Another writer changed the hold. Never treat it as unbilled.
		default:
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return released, firstErr
}
