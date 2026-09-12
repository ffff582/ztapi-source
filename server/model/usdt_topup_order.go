package model

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	USDTTopUpNetworkTronMainnet = "tron-mainnet"
	USDTTopUpAssetUSDT          = "USDT"
	USDTTopUpContractAddress    = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"

	USDTTopUpStatusPending      = "pending"
	USDTTopUpStatusConfirming   = "confirming"
	USDTTopUpStatusSettled      = "settled"
	USDTTopUpStatusExpired      = "expired"
	USDTTopUpStatusManualReview = "manual_review"
)

var (
	ErrUSDTTopUpInvalidAmount = errors.New("invalid USDT topup amount")
	ErrUSDTTopUpDisabled      = errors.New("USDT topup is disabled")
	ErrUSDTTopUpCapacity      = errors.New("no safe USDT payment suffix is available")
	ErrUSDTTopUpNotFound      = errors.New("USDT topup order not found")
	ErrUSDTTopUpStateConflict = errors.New("USDT topup order state conflict")
)

var usdtTopUpSQLiteMu sync.Mutex

type USDTTopUpOrder struct {
	ID               int64   `json:"id" gorm:"primaryKey"`
	TopUpID          int     `json:"top_up_id" gorm:"not null;uniqueIndex"`
	UserID           int     `json:"user_id" gorm:"not null;index"`
	TradeNo          string  `json:"trade_no" gorm:"size:64;not null;uniqueIndex"`
	Network          string  `json:"network" gorm:"size:16;not null"`
	Asset            string  `json:"asset" gorm:"size:16;not null"`
	ContractAddress  string  `json:"contract_address" gorm:"size:64;not null"`
	ReceivingAddress string  `json:"receiving_address" gorm:"size:64;not null"`
	CreditUnits      int64   `json:"credit_units" gorm:"not null;index:idx_usdt_credit_cooldown,priority:1"`
	PayAmountMicros  int64   `json:"pay_amount_micros" gorm:"not null;index"`
	SuffixCents      int     `json:"suffix_cents" gorm:"not null"`
	Status           string  `json:"status" gorm:"size:32;not null;index"`
	ExpiresAt        int64   `json:"expires_at" gorm:"not null;index"`
	CooldownUntil    int64   `json:"cooldown_until" gorm:"not null;index;index:idx_usdt_credit_cooldown,priority:2"`
	TxID             *string `json:"tx_id,omitempty" gorm:"size:128;uniqueIndex"`
	TxFrom           *string `json:"tx_from,omitempty" gorm:"size:64"`
	BlockTimestampMS *int64  `json:"block_timestamp_ms,omitempty"`
	SettledAt        *int64  `json:"settled_at,omitempty"`
	ReviewReason     string  `json:"review_reason,omitempty" gorm:"size:500"`
	CreatedAt        int64   `json:"created_at" gorm:"not null"`
	UpdatedAt        int64   `json:"updated_at" gorm:"not null"`
}

type USDTWatcherLease struct {
	Name      string `gorm:"primaryKey;size:64"`
	HolderID  string `gorm:"size:128;not null"`
	ExpiresAt int64  `gorm:"not null;index"`
	UpdatedAt int64  `gorm:"not null"`
}

type USDTTopUpAmountLock struct {
	CreditUnits int64 `gorm:"primaryKey"`
	UpdatedAt   int64 `gorm:"not null"`
}

func (order USDTTopUpOrder) ValidateExactPayAmount(minTopUp int64) (int64, error) {
	if minTopUp <= 0 || order.CreditUnits < minTopUp || order.SuffixCents < 1 || order.SuffixCents > 99 {
		return 0, ErrUSDTTopUpInvalidAmount
	}
	suffixMicros := int64(order.SuffixCents) * 10_000
	if order.CreditUnits > (math.MaxInt64-suffixMicros)/1_000_000 {
		return 0, ErrUSDTTopUpInvalidAmount
	}
	return order.CreditUnits*1_000_000 + suffixMicros, nil
}

func (order USDTTopUpOrder) ExactPayAmountMicros() int64 {
	amount, err := order.ValidateExactPayAmount(1)
	if err != nil {
		return 0
	}
	return amount
}

func (order USDTTopUpOrder) ExactPayAmountString() string {
	if order.ExactPayAmountMicros() == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", order.CreditUnits, order.SuffixCents)
}

func CreateUSDTTopUpOrder(userID int, creditUnits int64, now time.Time, config setting.USDTTopUpConfig) (*USDTTopUpOrder, error) {
	if err := validateUSDTTopUpOrderInput(userID, creditUnits, config); err != nil {
		return nil, err
	}
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		defer usdtTopUpSQLiteMu.Unlock()
	}

	now = now.UTC()
	var order USDTTopUpOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUSDTTopUpAmount(tx, creditUnits, now.Unix()); err != nil {
			return err
		}

		var reserved []USDTTopUpOrder
		query := tx.Where("credit_units = ? AND cooldown_until > ?", creditUnits, now.Unix())
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&reserved).Error; err != nil {
			return err
		}
		used := make(map[int]struct{}, len(reserved))
		for _, existing := range reserved {
			if existing.SuffixCents >= 1 && existing.SuffixCents <= 99 {
				used[existing.SuffixCents] = struct{}{}
			}
		}
		suffixes, err := shuffledUSDTSuffixes()
		if err != nil {
			return err
		}
		selected := 0
		for _, suffix := range suffixes {
			if _, exists := used[suffix]; !exists {
				selected = suffix
				break
			}
		}
		if selected == 0 {
			return ErrUSDTTopUpCapacity
		}

		tradeNo := fmt.Sprintf("USDT%d%s", userID, common.GetUUID())
		order = USDTTopUpOrder{
			UserID:           userID,
			TradeNo:          tradeNo,
			Network:          USDTTopUpNetworkTronMainnet,
			Asset:            USDTTopUpAssetUSDT,
			ContractAddress:  USDTTopUpContractAddress,
			ReceivingAddress: strings.TrimSpace(config.ReceivingAddress),
			CreditUnits:      creditUnits,
			SuffixCents:      selected,
			Status:           USDTTopUpStatusPending,
			ExpiresAt:        now.Add(config.OrderTTL).Unix(),
			CooldownUntil:    now.Add(config.SuffixCooldown).Unix(),
			CreatedAt:        now.Unix(),
			UpdatedAt:        now.Unix(),
		}
		payAmountMicros, err := order.ValidateExactPayAmount(config.MinTopUp)
		if err != nil {
			return err
		}
		order.PayAmountMicros = payAmountMicros

		topUp := TopUp{
			UserId:          userID,
			Amount:          creditUnits,
			Money:           float64(payAmountMicros) / 1_000_000,
			TradeNo:         tradeNo,
			PaymentMethod:   PaymentMethodUSDTTRC20,
			PaymentProvider: PaymentProviderUSDTTRC20,
			CreateTime:      now.Unix(),
			Status:          common.TopUpStatusPending,
		}
		if err := tx.Create(&topUp).Error; err != nil {
			return err
		}
		order.TopUpID = topUp.Id
		return tx.Create(&order).Error
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func GetUserUSDTTopUpOrder(userID int, tradeNo string) (*USDTTopUpOrder, error) {
	if userID <= 0 || strings.TrimSpace(tradeNo) == "" || DB == nil {
		return nil, ErrUSDTTopUpNotFound
	}
	var order USDTTopUpOrder
	if err := DB.Where("user_id = ? AND trade_no = ?", userID, strings.TrimSpace(tradeNo)).First(&order).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUSDTTopUpNotFound
		}
		return nil, err
	}
	return &order, nil
}

func CancelUSDTTopUpOrder(userID int, tradeNo string, now time.Time) (*USDTTopUpOrder, error) {
	if userID <= 0 || strings.TrimSpace(tradeNo) == "" || DB == nil {
		return nil, ErrUSDTTopUpNotFound
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		defer usdtTopUpSQLiteMu.Unlock()
	}
	var order USDTTopUpOrder
	err := DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("user_id = ? AND trade_no = ?", userID, strings.TrimSpace(tradeNo))
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&order).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrUSDTTopUpNotFound
			}
			return err
		}
		if order.Status != USDTTopUpStatusPending {
			return ErrUSDTTopUpStateConflict
		}
		updatedAt := now.UTC().Unix()
		result := tx.Model(&USDTTopUpOrder{}).Where("id = ? AND status = ?", order.ID, USDTTopUpStatusPending).Updates(map[string]interface{}{
			"status": USDTTopUpStatusExpired, "updated_at": updatedAt,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUSDTTopUpStateConflict
		}
		result = tx.Model(&TopUp{}).Where("id = ? AND status = ?", order.TopUpID, common.TopUpStatusPending).Updates(map[string]interface{}{
			"status": common.TopUpStatusExpired,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUSDTTopUpStateConflict
		}
		order.Status = USDTTopUpStatusExpired
		order.UpdatedAt = updatedAt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func AdvanceUSDTTopUpExpiryStates(now time.Time, confirmationGrace time.Duration) (int64, int64, error) {
	if DB == nil {
		return 0, 0, errors.New("database is not initialized")
	}
	if confirmationGrace <= 0 {
		return 0, 0, errors.New("USDT confirmation grace must be positive")
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		defer usdtTopUpSQLiteMu.Unlock()
	}

	now = now.UTC()
	nowUnix := now.Unix()
	finalExpiryCutoff := now.Add(-confirmationGrace).Unix()
	var confirming int64
	var expired int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		var orders []USDTTopUpOrder
		query := tx.Where(
			"status IN ? AND expires_at <= ?",
			[]string{USDTTopUpStatusPending, USDTTopUpStatusConfirming},
			nowUnix,
		)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&orders).Error; err != nil {
			return err
		}

		confirmingIDs := make([]int64, 0, len(orders))
		expiredOrderIDs := make([]int64, 0, len(orders))
		expiredTopUpIDs := make([]int, 0, len(orders))
		for _, order := range orders {
			if order.ExpiresAt <= finalExpiryCutoff {
				expiredOrderIDs = append(expiredOrderIDs, order.ID)
				expiredTopUpIDs = append(expiredTopUpIDs, order.TopUpID)
				continue
			}
			if order.Status == USDTTopUpStatusPending {
				confirmingIDs = append(confirmingIDs, order.ID)
			}
		}

		if len(expiredOrderIDs) > 0 {
			result := tx.Model(&USDTTopUpOrder{}).
				Where("id IN ? AND status IN ?", expiredOrderIDs, []string{USDTTopUpStatusPending, USDTTopUpStatusConfirming}).
				Updates(map[string]interface{}{"status": USDTTopUpStatusExpired, "updated_at": nowUnix})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(expiredOrderIDs)) {
				return ErrUSDTTopUpStateConflict
			}
			expired = result.RowsAffected
			if err := tx.Model(&TopUp{}).
				Where("id IN ? AND status = ?", expiredTopUpIDs, common.TopUpStatusPending).
				Update("status", common.TopUpStatusExpired).Error; err != nil {
				return err
			}
		}

		if len(confirmingIDs) > 0 {
			result := tx.Model(&USDTTopUpOrder{}).
				Where("id IN ? AND status = ?", confirmingIDs, USDTTopUpStatusPending).
				Updates(map[string]interface{}{"status": USDTTopUpStatusConfirming, "updated_at": nowUnix})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != int64(len(confirmingIDs)) {
				return ErrUSDTTopUpStateConflict
			}
			confirming = result.RowsAffected
		}
		return nil
	})
	return confirming, expired, err
}

func ExpireUSDTTopUpOrders(now time.Time) (int64, error) {
	if DB == nil {
		return 0, errors.New("database is not initialized")
	}
	if DB.Dialector.Name() == "sqlite" {
		usdtTopUpSQLiteMu.Lock()
		defer usdtTopUpSQLiteMu.Unlock()
	}
	var expired int64
	nowUnix := now.UTC().Unix()
	err := DB.Transaction(func(tx *gorm.DB) error {
		var orders []USDTTopUpOrder
		query := tx.Where("status = ? AND expires_at <= ?", USDTTopUpStatusPending, nowUnix)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.Find(&orders).Error; err != nil {
			return err
		}
		if len(orders) == 0 {
			return nil
		}
		orderIDs := make([]int64, 0, len(orders))
		topUpIDs := make([]int, 0, len(orders))
		for _, order := range orders {
			orderIDs = append(orderIDs, order.ID)
			topUpIDs = append(topUpIDs, order.TopUpID)
		}
		result := tx.Model(&USDTTopUpOrder{}).Where("id IN ? AND status = ?", orderIDs, USDTTopUpStatusPending).Updates(map[string]interface{}{
			"status": USDTTopUpStatusExpired, "updated_at": nowUnix,
		})
		if result.Error != nil {
			return result.Error
		}
		expired = result.RowsAffected
		if expired == 0 {
			return nil
		}
		return tx.Model(&TopUp{}).Where("id IN ? AND status = ?", topUpIDs, common.TopUpStatusPending).Update("status", common.TopUpStatusExpired).Error
	})
	return expired, err
}

func CountPendingUSDTTopUpOrders(now time.Time) (int64, error) {
	if DB == nil {
		return 0, errors.New("database is not initialized")
	}
	var count int64
	err := DB.Model(&USDTTopUpOrder{}).
		Where("status = ? AND expires_at > ?", USDTTopUpStatusPending, now.UTC().Unix()).
		Count(&count).Error
	return count, err
}

func validateUSDTTopUpOrderInput(userID int, creditUnits int64, config setting.USDTTopUpConfig) error {
	if !config.Enabled {
		return ErrUSDTTopUpDisabled
	}
	if userID <= 0 || creditUnits < config.MinTopUp || config.MinTopUp < 10 {
		return ErrUSDTTopUpInvalidAmount
	}
	if strings.TrimSpace(config.ReceivingAddress) == "" || strings.TrimSpace(config.TronGridAPIKey) == "" || config.OrderTTL <= 0 || config.SuffixCooldown < config.OrderTTL {
		return errors.New("incomplete USDT topup configuration")
	}
	return nil
}

func lockUSDTTopUpAmount(tx *gorm.DB, creditUnits int64, nowUnix int64) error {
	lock := USDTTopUpAmountLock{CreditUnits: creditUnits, UpdatedAt: nowUnix}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&lock).Error; err != nil {
		return err
	}
	query := tx.Where("credit_units = ?", creditUnits)
	if tx.Dialector.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return query.First(&lock).Error
}

func shuffledUSDTSuffixes() ([]int, error) {
	suffixes := make([]int, 99)
	for i := range suffixes {
		suffixes[i] = i + 1
	}
	for i := len(suffixes) - 1; i > 0; i-- {
		index, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, fmt.Errorf("generate USDT payment suffix: %w", err)
		}
		j := int(index.Int64())
		suffixes[i], suffixes[j] = suffixes[j], suffixes[i]
	}
	return suffixes, nil
}
