package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"
)

// A watched address only widens what the site can credit. The address an order
// asks a customer to pay stays a deployment input, so nothing reachable from
// the administration site can send a customer's money somewhere else.
type USDTWatchedReceivingAddress struct {
	ID         int    `json:"id" gorm:"primaryKey"`
	Address    string `json:"address" gorm:"size:64;not null;uniqueIndex"`
	Label      string `json:"label" gorm:"size:128;not null;default:''"`
	Enabled    bool   `json:"enabled" gorm:"not null;default:true"`
	OperatorID int    `json:"operator_id" gorm:"not null"`
	CreatedAt  int64  `json:"created_at" gorm:"bigint;not null"`
	UpdatedAt  int64  `json:"updated_at" gorm:"bigint;not null"`
}

func (USDTWatchedReceivingAddress) TableName() string {
	return "usdt_watched_receiving_addresses"
}

// Every enabled address costs one upstream query per poll, so the set stays
// small enough that watching them cannot exhaust the provider's rate limit.
const MaxEnabledUSDTWatchedAddresses = 8

var (
	ErrUSDTWatchedAddressInvalid   = errors.New("usdt_watched_address_invalid")
	ErrUSDTWatchedAddressDuplicate = errors.New("usdt_watched_address_duplicate")
	ErrUSDTWatchedAddressLimit     = errors.New("usdt_watched_address_limit")
)

const tronBase58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ValidateTronBase58Address accepts only an address whose own checksum proves
// it was not mistyped. A watched address that nobody can pay is useless, and a
// mistyped one would be watched forever without ever crediting anyone.
func ValidateTronBase58Address(address string) error {
	trimmed := strings.TrimSpace(address)
	if len(trimmed) != 34 || !strings.HasPrefix(trimmed, "T") {
		return fmt.Errorf("%w: a TRON address is 34 characters and starts with T", ErrUSDTWatchedAddressInvalid)
	}
	decoded := new(big.Int)
	for _, symbol := range trimmed {
		index := strings.IndexRune(tronBase58Alphabet, symbol)
		if index < 0 {
			return fmt.Errorf("%w: %q is not a base58 character", ErrUSDTWatchedAddressInvalid, symbol)
		}
		decoded.Mul(decoded, big.NewInt(58))
		decoded.Add(decoded, big.NewInt(int64(index)))
	}
	raw := decoded.Bytes()
	for index := 0; index < len(trimmed) && trimmed[index] == '1'; index++ {
		raw = append([]byte{0}, raw...)
	}
	if len(raw) != 25 {
		return fmt.Errorf("%w: address does not decode to 25 bytes", ErrUSDTWatchedAddressInvalid)
	}
	if raw[0] != 0x41 {
		return fmt.Errorf("%w: address is not on the TRON main network", ErrUSDTWatchedAddressInvalid)
	}
	first := sha256.Sum256(raw[:21])
	second := sha256.Sum256(first[:])
	for index := 0; index < 4; index++ {
		if second[index] != raw[21+index] {
			return fmt.Errorf("%w: address checksum does not match", ErrUSDTWatchedAddressInvalid)
		}
	}
	return nil
}

func ListUSDTWatchedReceivingAddresses() ([]USDTWatchedReceivingAddress, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var addresses []USDTWatchedReceivingAddress
	if err := DB.Order("id ASC").Find(&addresses).Error; err != nil {
		return nil, err
	}
	return addresses, nil
}

// ListEnabledUSDTWatchedAddresses returns the addresses the watcher polls in
// addition to the deployed receiving address.
func ListEnabledUSDTWatchedAddresses() ([]string, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var rows []USDTWatchedReceivingAddress
	if err := DB.Where("enabled = ?", true).Order("id ASC").
		Limit(MaxEnabledUSDTWatchedAddresses).Find(&rows).Error; err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(rows))
	for _, row := range rows {
		addresses = append(addresses, row.Address)
	}
	return addresses, nil
}

func CreateUSDTWatchedReceivingAddress(address, label string, operatorID int, now time.Time) (*USDTWatchedReceivingAddress, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if operatorID <= 0 {
		return nil, fmt.Errorf("%w: an operator is required", ErrUSDTWatchedAddressInvalid)
	}
	trimmed := strings.TrimSpace(address)
	if err := ValidateTronBase58Address(trimmed); err != nil {
		return nil, err
	}
	record := &USDTWatchedReceivingAddress{
		Address: trimmed, Label: strings.TrimSpace(label), Enabled: true,
		OperatorID: operatorID, CreatedAt: now.UTC().Unix(), UpdatedAt: now.UTC().Unix(),
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&USDTWatchedReceivingAddress{}).
			Where("address = ?", trimmed).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return ErrUSDTWatchedAddressDuplicate
		}
		var enabled int64
		if err := tx.Model(&USDTWatchedReceivingAddress{}).
			Where("enabled = ?", true).Count(&enabled).Error; err != nil {
			return err
		}
		if enabled >= MaxEnabledUSDTWatchedAddresses {
			return ErrUSDTWatchedAddressLimit
		}
		return tx.Create(record).Error
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

func SetUSDTWatchedReceivingAddressEnabled(id int, enabled bool, operatorID int, now time.Time) (*USDTWatchedReceivingAddress, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if id <= 0 || operatorID <= 0 {
		return nil, fmt.Errorf("%w: an address and an operator are required", ErrUSDTWatchedAddressInvalid)
	}
	var record USDTWatchedReceivingAddress
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&record, id).Error; err != nil {
			return err
		}
		if record.Enabled == enabled {
			return nil
		}
		if enabled {
			var active int64
			if err := tx.Model(&USDTWatchedReceivingAddress{}).
				Where("enabled = ?", true).Count(&active).Error; err != nil {
				return err
			}
			if active >= MaxEnabledUSDTWatchedAddresses {
				return ErrUSDTWatchedAddressLimit
			}
		}
		record.Enabled = enabled
		record.OperatorID = operatorID
		record.UpdatedAt = now.UTC().Unix()
		return tx.Model(&USDTWatchedReceivingAddress{}).Where("id = ?", id).
			Updates(map[string]any{
				"enabled": enabled, "operator_id": operatorID, "updated_at": record.UpdatedAt,
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return &record, nil
}
