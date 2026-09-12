package model

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestBillingRefundPendingTextColumnHasNoMySQLDefault(t *testing.T) {
	field, ok := reflect.TypeOf(BillingRefundPending{}).FieldByName("LastError")
	if !ok {
		t.Fatal("BillingRefundPending.LastError field is missing")
	}
	if strings.Contains(strings.ToLower(field.Tag.Get("gorm")), "default") {
		t.Fatalf("MySQL does not allow a default value on TEXT columns: %q", field.Tag.Get("gorm"))
	}
}

func TestBillingRefundPendingRetriesIdempotentlyAfterTransientWalletFailure(t *testing.T) {
	common.RedisEnabled = false
	originalDB := DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		DB = originalDB
		sqlDB, sqlErr := db.DB()
		if sqlErr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&User{}, &BalanceLedger{}, &BillingRefundPending{}); err != nil {
		t.Fatalf("migrate billing refund tables: %v", err)
	}
	user := User{
		Username: "refund-user",
		Password: "not-used",
		Quota:    75,
		Status:   common.UserStatusEnabled,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	pending, err := EnsureBillingRefundPending(BillingRefundPending{
		UserID:         user.Id,
		Amount:         25,
		Kind:           BillingRefundKindWallet,
		IdempotencyKey: "billing-refund:req-1:wallet",
		RequestID:      "req-1",
	})
	if err != nil {
		t.Fatalf("create pending refund: %v", err)
	}

	callbackName := "test:fail-wallet-refund-once:" + strings.ReplaceAll(t.Name(), "/", "_")
	var failOnce atomic.Bool
	failOnce.Store(true)
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" && failOnce.CompareAndSwap(true, false) {
			_ = tx.AddError(errors.New("transient wallet refund failure"))
		}
	}); err != nil {
		t.Fatalf("register failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	if err := ProcessBillingRefundPending(pending.ID); err == nil {
		t.Fatal("transient wallet refund failure was not returned")
	}
	var storedPending BillingRefundPending
	if err := db.First(&storedPending, pending.ID).Error; err != nil {
		t.Fatalf("reload pending refund: %v", err)
	}
	if storedPending.Status != BillingRefundStatusPending || storedPending.Attempts != 1 {
		t.Fatalf("pending status=%q attempts=%d, want pending/1", storedPending.Status, storedPending.Attempts)
	}

	processed, err := ProcessPendingBillingRefunds(10)
	if err != nil {
		t.Fatalf("retry pending refunds: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed refunds=%d, want 1", processed)
	}
	if err := db.First(&user, user.Id).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if user.Quota != 100 {
		t.Fatalf("wallet quota=%d, want 100", user.Quota)
	}
	var ledgerCount int64
	if err := db.Model(&BalanceLedger{}).
		Where("idempotency_key = ?", pending.IdempotencyKey).
		Count(&ledgerCount).Error; err != nil {
		t.Fatalf("count refund ledger: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("refund ledger rows=%d, want 1", ledgerCount)
	}

	if err := ProcessBillingRefundPending(pending.ID); err != nil {
		t.Fatalf("repeat completed refund: %v", err)
	}
	if err := db.First(&user, user.Id).Error; err != nil {
		t.Fatalf("reload user after repeat: %v", err)
	}
	if user.Quota != 100 {
		t.Fatalf("repeat refund changed wallet quota to %d", user.Quota)
	}
}
