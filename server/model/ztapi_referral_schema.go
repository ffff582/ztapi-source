package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZTAPIReferralFreezeSeconds = int64(604800)
	ZTAPIReferralMinimumMicros = int64(20_000_000)
	ZTAPIReferralRuleVersion   = "usdt-principal-5pct-v1"
)

var (
	ErrZTAPIReferralInvalid      = errors.New("invalid referral request or evidence")
	ErrZTAPIReferralConflict     = errors.New("referral state or request identity conflict")
	ErrZTAPIReferralNotFound     = errors.New("referral record not found")
	ErrZTAPIReferralInsufficient = errors.New("insufficient unfrozen referral commission")
	ErrZTAPIReferralNotReady     = errors.New("referral campaign readiness is blocked")
	ErrZTAPIReferralImmutable    = errors.New("referral evidence is append-only")
	ErrZTAPIReferralOverflow     = errors.New("referral amount overflow")
	// This mutex belongs only to referral transactions, never API balance locks.
	ztapiReferralSQLiteMu sync.Mutex
)

type ZTAPIReferralCampaign struct {
	ID              int64  `gorm:"primaryKey" json:"id"`
	Version         int64  `gorm:"not null" json:"version"`
	Enabled         bool   `gorm:"not null" json:"enabled"`
	StartedAt       int64  `gorm:"not null" json:"started_at"`
	UpdatedAt       int64  `gorm:"not null;autoUpdateTime:false" json:"updated_at"`
	RuleVersion     string `gorm:"size:64;not null" json:"rule_version"`
	ReconcileCursor int64  `gorm:"not null" json:"-"`
}

func (ZTAPIReferralCampaign) TableName() string { return "ztapi_referral_campaigns" }

type ZTAPIReferralBinding struct {
	ID                int64  `gorm:"primaryKey" json:"id"`
	UserID            int    `gorm:"not null;uniqueIndex" json:"user_id"`
	InviterID         int    `gorm:"not null;index" json:"inviter_id"`
	CampaignID        int64  `gorm:"not null" json:"campaign_id"`
	BoundAt           int64  `gorm:"not null" json:"bound_at"`
	ExpiresAt         int64  `gorm:"not null" json:"expires_at"`
	CampaignStartedAt int64  `gorm:"not null" json:"campaign_started_at"`
	RuleVersion       string `gorm:"size:64;not null" json:"rule_version"`
}

func (ZTAPIReferralBinding) TableName() string            { return "ztapi_referral_bindings" }
func (*ZTAPIReferralBinding) BeforeUpdate(*gorm.DB) error { return ErrZTAPIReferralImmutable }
func (*ZTAPIReferralBinding) BeforeDelete(*gorm.DB) error { return ErrZTAPIReferralImmutable }

type ZTAPIReferralAccountLock struct {
	UserID int `gorm:"primaryKey;autoIncrement:false"`
}

func (ZTAPIReferralAccountLock) TableName() string { return "ztapi_referral_account_locks" }

type ZTAPIReferralCommission struct {
	ID                      int64  `gorm:"primaryKey" json:"id"`
	UserID                  int    `gorm:"not null;index" json:"user_id"`
	InviteeID               int    `gorm:"not null" json:"-"`
	BindingID               int64  `gorm:"not null;index" json:"binding_id"`
	OrderID                 int64  `gorm:"uniqueIndex;default:null" json:"order_id"`
	TopUpID                 int    `gorm:"uniqueIndex;default:null" json:"-"`
	CNYOrderID              *int64 `gorm:"uniqueIndex" json:"-"`
	CNYMethod               string `gorm:"size:16" json:"-"`
	CNYRefundBlocked        bool   `gorm:"-" json:"-"`
	BalanceLedgerID         int    `gorm:"not null;uniqueIndex" json:"-"`
	PrincipalMicros         int64  `gorm:"type:bigint;not null" json:"principal_micros"`
	CommissionMicros        int64  `gorm:"type:bigint;not null" json:"commission_micros"`
	RefundedPrincipalMicros int64  `gorm:"type:bigint;not null" json:"refunded_principal_micros"`
	ReversedMicros          int64  `gorm:"type:bigint;not null" json:"reversed_micros"`
	ManualLossMicros        int64  `gorm:"type:bigint;not null" json:"manual_loss_micros"`
	BlockTimestampMS        int64  `gorm:"not null" json:"block_timestamp_ms"`
	EarnedAt                int64  `gorm:"not null" json:"earned_at"`
	AvailableAt             int64  `gorm:"not null;index" json:"available_at"`
	RuleVersion             string `gorm:"size:64;not null" json:"rule_version"`
	EvidenceIdentity        string `gorm:"size:64;not null" json:"-"`
}

func (ZTAPIReferralCommission) TableName() string { return "ztapi_referral_commissions" }

type ZTAPIReferralLedgerEntry struct {
	ID             int64  `gorm:"primaryKey" json:"id"`
	UserID         int    `gorm:"not null;index" json:"user_id"`
	BindingID      int64  `gorm:"not null" json:"binding_id"`
	CommissionID   int64  `gorm:"not null;index" json:"commission_id"`
	WithdrawalID   int64  `gorm:"not null" json:"withdrawal_id"`
	Kind           string `gorm:"size:32;not null" json:"kind"`
	AmountMicros   int64  `gorm:"type:bigint;not null" json:"amount_micros"`
	AvailableAt    int64  `gorm:"not null" json:"available_at"`
	CreatedAt      int64  `gorm:"not null;index;autoCreateTime:false" json:"created_at"`
	IdempotencyKey string `gorm:"size:128;not null;uniqueIndex" json:"idempotency_key"`
	Identity       string `gorm:"size:64;not null" json:"-"`
}

func (ZTAPIReferralLedgerEntry) TableName() string            { return "ztapi_referral_ledger_entries" }
func (*ZTAPIReferralLedgerEntry) BeforeUpdate(*gorm.DB) error { return ErrZTAPIReferralImmutable }
func (*ZTAPIReferralLedgerEntry) BeforeDelete(*gorm.DB) error { return ErrZTAPIReferralImmutable }

type ZTAPIReferralWithdrawal struct {
	ID              int64   `gorm:"primaryKey" json:"id"`
	Version         int64   `gorm:"not null" json:"version"`
	UserID          int     `gorm:"not null;index" json:"user_id"`
	AmountMicros    int64   `gorm:"type:bigint;not null" json:"amount_micros"`
	FeeMicros       int64   `gorm:"type:bigint;not null" json:"fee_micros"`
	NetAmountMicros int64   `gorm:"type:bigint;not null" json:"net_amount_micros"`
	Address         string  `gorm:"size:64;not null" json:"address"`
	Status          string  `gorm:"size:32;not null;index" json:"status"`
	CreatedAt       int64   `gorm:"not null;index;autoCreateTime:false" json:"created_at"`
	UpdatedAt       int64   `gorm:"not null;autoUpdateTime:false" json:"updated_at"`
	FeeExpiresAt    int64   `gorm:"not null" json:"fee_expires_at"`
	PaidAt          int64   `gorm:"not null" json:"paid_at"`
	HolderID        int     `gorm:"not null" json:"holder_id"`
	TxID            string  `gorm:"size:64;not null" json:"tx_id"`
	TransferKey     *string `gorm:"size:96;uniqueIndex" json:"-"`
	PaymentEvidence string  `gorm:"type:text;not null" json:"-"`
	Note            string  `gorm:"size:500;not null" json:"note"`
	IdempotencyKey  string  `gorm:"size:64;not null;uniqueIndex" json:"-"`
	RequestIdentity string  `gorm:"size:64;not null" json:"-"`
}

func (ZTAPIReferralWithdrawal) TableName() string { return "ztapi_referral_withdrawals" }

type ZTAPIReferralAllocation struct {
	ID                int64  `gorm:"primaryKey"`
	UserID            int    `gorm:"not null;index"`
	WithdrawalID      int64  `gorm:"not null;uniqueIndex:idx_referral_allocation,priority:1"`
	CommissionID      int64  `gorm:"not null;uniqueIndex:idx_referral_allocation,priority:2;index"`
	AmountMicros      int64  `gorm:"type:bigint;not null"`
	InvalidatedMicros int64  `gorm:"type:bigint;not null"`
	State             string `gorm:"size:16;not null"`
}

func (ZTAPIReferralAllocation) TableName() string { return "ztapi_referral_allocations" }

type ZTAPIReferralAudit struct {
	ID             int64   `gorm:"primaryKey"`
	UserID         int     `gorm:"not null;index"`
	OperatorID     int     `gorm:"not null"`
	RelatedID      int64   `gorm:"not null"`
	Action         string  `gorm:"size:32;not null"`
	IdempotencyKey string  `gorm:"size:128;not null;uniqueIndex"`
	Identity       string  `gorm:"size:64;not null"`
	Evidence       string  `gorm:"type:text;not null"`
	EvidenceKey    *string `gorm:"size:128;uniqueIndex"`
	CreatedAt      int64   `gorm:"not null;autoCreateTime:false"`
}

func (ZTAPIReferralAudit) TableName() string            { return "ztapi_referral_audits" }
func (*ZTAPIReferralAudit) BeforeUpdate(*gorm.DB) error { return ErrZTAPIReferralImmutable }
func (*ZTAPIReferralAudit) BeforeDelete(*gorm.DB) error { return ErrZTAPIReferralImmutable }

func ZTAPIReferralModels() []any {
	return []any{&ZTAPIReferralCampaign{}, &ZTAPIReferralBinding{}, &ZTAPIReferralAccountLock{}, &ZTAPIReferralCommission{}, &ZTAPIReferralLedgerEntry{}, &ZTAPIReferralWithdrawal{}, &ZTAPIReferralAllocation{}, &ZTAPIReferralAudit{}}
}

// RegisterZTAPIReferralImmutability must also be called when migrations are owned by main.
// Guards include SkipHooks/Table operations; raw SQL remains a database-permission boundary.
func RegisterZTAPIReferralImmutability(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPIReferralInvalid
	}
	guard := func(tx *gorm.DB) {
		target := referralImmutableTable(tx.Statement.Table)
		if tx.Statement.Schema != nil {
			target = target || referralImmutableTable(tx.Statement.Schema.Table)
		}
		if tx.Statement.TableExpr != nil {
			target = target || referralImmutableTable(tx.Statement.TableExpr.SQL)
		}
		if target {
			tx.AddError(ErrZTAPIReferralImmutable)
		}
	}
	if db.Callback().Update().Get("ztapi_referral:immutable") == nil {
		if err := db.Callback().Update().Before("gorm:update").Register("ztapi_referral:immutable", guard); err != nil {
			return err
		}
	}
	if db.Callback().Delete().Get("ztapi_referral:immutable") == nil {
		return db.Callback().Delete().Before("gorm:delete").Register("ztapi_referral:immutable", guard)
	}
	return nil
}

func referralImmutableTable(expression string) bool {
	for position := 0; position < len(expression); {
		next := skipBalanceLedgerSQLTrivia(expression, position)
		if next != position {
			position = next
			continue
		}
		if expression[position] == '\'' {
			position = skipBalanceLedgerSQLString(expression, position)
			continue
		}
		identifier, next, ok := parseBalanceLedgerSQLIdentifier(expression, position)
		if ok {
			for _, name := range []string{(ZTAPIReferralLedgerEntry{}).TableName(), (ZTAPIReferralAudit{}).TableName(), (ZTAPIReferralBinding{}).TableName()} {
				if strings.EqualFold(identifier, name) {
					return true
				}
			}
			position = next
		} else {
			position++
		}
	}
	return false
}

func MigrateZTAPIReferral(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPIReferralInvalid
	}
	var err error
	if db.Dialector.Name() == "sqlite" {
		err = db.Transaction(migrateSQLiteZTAPIReferral)
	} else {
		err = migrateZTAPIReferralTables(db)
	}
	if err != nil {
		return err
	}
	return RegisterZTAPIReferralImmutability(db)
}

func migrateSQLiteZTAPIReferral(db *gorm.DB) error {
	var objects []struct{ Name, SQL string }
	table := (&ZTAPIReferralCommission{}).TableName()
	if err := db.Raw("SELECT name, sql FROM sqlite_master WHERE tbl_name = ? AND type IN ('index', 'trigger') AND sql IS NOT NULL", table).Scan(&objects).Error; err != nil {
		return err
	}
	migrator := db.Migrator()
	// SQLite cannot add a column with an inline UNIQUE constraint. Create the
	// nullable column first; AutoMigrate will create its separate unique index.
	if migrator.HasTable(table) && !migrator.HasColumn(&ZTAPIReferralCommission{}, "CNYOrderID") {
		if err := db.Exec("ALTER TABLE `ztapi_referral_commissions` ADD COLUMN `cny_order_id` integer").Error; err != nil {
			return err
		}
	}
	if err := migrateZTAPIReferralTables(db); err != nil {
		return err
	}
	// AlterColumn copies the table without its separately defined indexes and
	// triggers. Restore them before committing the schema change.
	for _, object := range objects {
		var count int64
		if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE tbl_name = ? AND name = ?", table, object.Name).Scan(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := db.Exec(object.SQL).Error; err != nil {
				return err
			}
		}
	}
	statement := &gorm.Statement{DB: db}
	commissions := &ZTAPIReferralCommission{}
	if err := statement.Parse(commissions); err != nil {
		return err
	}
	for name := range statement.Schema.ParseIndexes() {
		if !migrator.HasIndex(commissions, name) {
			if err := migrator.CreateIndex(commissions, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateZTAPIReferralTables(db *gorm.DB) error {
	if err := db.AutoMigrate(ZTAPIReferralModels()...); err != nil {
		return err
	}
	// GORM does not relax legacy NOT NULL constraints during MySQL AutoMigrate.
	// NULL leaves the existing unique USDT identifiers intact for mixed funding.
	commissions := &ZTAPIReferralCommission{}
	columns, err := db.Migrator().ColumnTypes(commissions)
	if err != nil {
		return err
	}
	for _, column := range columns {
		if column.Name() != "order_id" && column.Name() != "top_up_id" {
			continue
		}
		if nullable, known := column.Nullable(); !known || !nullable {
			if err = db.Migrator().AlterColumn(commissions, column.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func referralDB(ctx context.Context) (*gorm.DB, error) {
	if DB == nil || ctx == nil {
		return nil, ErrZTAPIReferralInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return DB.WithContext(ctx), nil
}

func referralTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	db, err := referralDB(ctx)
	if err != nil {
		return err
	}
	if db.Dialector.Name() == "sqlite" {
		ztapiReferralSQLiteMu.Lock()
		defer ztapiReferralSQLiteMu.Unlock()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return db.Transaction(fn)
}

func referralForUpdate(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() != "sqlite" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

func referralLockAccount(tx *gorm.DB, userID int) error {
	if userID <= 0 {
		return ErrZTAPIReferralInvalid
	}
	row := ZTAPIReferralAccountLock{UserID: userID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return referralForUpdate(tx).Where("user_id = ?", userID).First(&row).Error
}

func referralHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func referralIdentity(v any) (string, string, error) {
	b, err := common.Marshal(v)
	if err != nil {
		return "", "", err
	}
	return referralHash(string(b)), string(b), nil
}

func referralAdd(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrZTAPIReferralOverflow
	}
	return a + b, nil
}

func referralValidKey(key string) bool {
	return key != "" && len(key) <= 128 && strings.TrimSpace(key) == key
}

func referralAuditReplay(tx *gorm.DB, key, identity string) (*ZTAPIReferralAudit, error) {
	var a ZTAPIReferralAudit
	err := tx.Where("idempotency_key = ?", key).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.Identity != identity {
		return nil, ErrZTAPIReferralConflict
	}
	return &a, nil
}

func referralAudit(tx *gorm.DB, user, operator int, related int64, action, key, identity, evidence string, now int64) error {
	return tx.Create(&ZTAPIReferralAudit{UserID: user, OperatorID: operator, RelatedID: related, Action: action, IdempotencyKey: key, Identity: identity, Evidence: evidence, CreatedAt: now}).Error
}

func referralAppend(tx *gorm.DB, c *ZTAPIReferralCommission, withdrawal int64, kind string, amount int64, key, identity string, at int64) error {
	if amount == 0 {
		return nil
	}
	return tx.Create(&ZTAPIReferralLedgerEntry{UserID: c.UserID, BindingID: c.BindingID, CommissionID: c.ID, WithdrawalID: withdrawal, Kind: kind, AmountMicros: amount, AvailableAt: c.AvailableAt, CreatedAt: at, IdempotencyKey: key, Identity: identity}).Error
}

func referralLoadWithdrawal(tx *gorm.DB, id int64) (*ZTAPIReferralWithdrawal, error) {
	var w ZTAPIReferralWithdrawal
	err := tx.First(&w, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrZTAPIReferralNotFound
	}
	return &w, err
}

// Route by the immutable owner outside the financial transaction. A preliminary
// non-locking read inside it would pin a stale MySQL REPEATABLE READ snapshot.
func referralWithdrawalOwner(ctx context.Context, id int64) (int, error) {
	db, err := referralDB(ctx)
	if err != nil {
		return 0, err
	}
	var row ZTAPIReferralWithdrawal
	err = db.Select("id", "user_id").First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrZTAPIReferralNotFound
	}
	return row.UserID, err
}

func referralNow(now time.Time) error {
	if now.IsZero() || now.Unix() <= 0 || now.Unix() > math.MaxInt64/1000-ZTAPIReferralFreezeSeconds {
		return ErrZTAPIReferralInvalid
	}
	return nil
}

func referralSaveWithdrawal(tx *gorm.DB, w *ZTAPIReferralWithdrawal, previous int64, now time.Time) error {
	if previous == math.MaxInt64 {
		return ErrZTAPIReferralOverflow
	}
	w.Version = previous + 1
	w.UpdatedAt = now.Unix()
	r := tx.Model(&ZTAPIReferralWithdrawal{}).Where("id = ? AND version = ?", w.ID, previous).Select("*").Omit("id").Updates(w)
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return ErrZTAPIReferralConflict
	}
	return nil
}

func referralKey(action string, id, version int64, actor int, supplied string) (string, error) {
	if supplied != "" {
		if !referralValidKey(supplied) {
			return "", ErrZTAPIReferralInvalid
		}
		return "action:" + referralHash(supplied), nil
	}
	return fmt.Sprintf("%s:%d:%d:%d", action, id, version, actor), nil
}
