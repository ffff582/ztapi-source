package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func ListReconcilableUSDTTopUpOrders(cutoff time.Time) ([]USDTTopUpOrder, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	orders := make([]USDTTopUpOrder, 0)
	err := DB.Where(
		"status IN ? AND expires_at > ?",
		[]string{USDTTopUpStatusPending, USDTTopUpStatusConfirming},
		cutoff.UTC().Unix(),
	).
		Order("created_at ASC, id ASC").
		Find(&orders).Error
	return orders, err
}

func AcquireUSDTWatcherLease(name string, holderID string, now time.Time, ttl time.Duration) (bool, error) {
	name = strings.TrimSpace(name)
	holderID = strings.TrimSpace(holderID)
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if name == "" || holderID == "" || ttl <= 0 {
		return false, errors.New("invalid USDT watcher lease")
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		defer usdtTopUpSQLiteMu.Unlock()
	}

	nowUnix := now.UTC().Unix()
	acquired := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		seed := USDTWatcherLease{Name: name, HolderID: holderID, ExpiresAt: nowUnix, UpdatedAt: nowUnix}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
			return err
		}
		var lease USDTWatcherLease
		query := tx.Where("name = ?", name)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&lease).Error; err != nil {
			return err
		}
		if lease.HolderID != holderID && lease.ExpiresAt > nowUnix {
			return nil
		}
		result := tx.Model(&USDTWatcherLease{}).Where("name = ?", name).Updates(map[string]interface{}{
			"holder_id":  holderID,
			"expires_at": now.UTC().Add(ttl).Unix(),
			"updated_at": nowUnix,
		})
		if result.Error != nil {
			return result.Error
		}
		acquired = result.RowsAffected == 1
		return nil
	})
	return acquired, err
}
