package model

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BalanceLedgerSourceAdmin             = "admin_adjustment"
	BalanceLedgerSourceUsageReservation  = "usage_reservation"
	BalanceLedgerSourceUsageSettlement   = "usage_settlement"
	BalanceLedgerIdempotencyKeyMaxLength = 128
	maxBalanceLedgerQuota                = int64(math.MaxInt32)
	balanceLedgerUpdateGuardName         = "balance_ledger:reject_update"
	balanceLedgerDeleteGuardName         = "balance_ledger:reject_delete"
)

var (
	ErrBalanceLedgerReasonRequired         = errors.New("balance adjustment reason is required")
	ErrBalanceLedgerRequestIDRequired      = errors.New("usage balance adjustment request ID is required")
	ErrBalanceLedgerIdempotencyKeyRequired = errors.New("balance adjustment idempotency key is required")
	ErrBalanceLedgerIdempotencyKeyTooLong  = errors.New("balance adjustment idempotency key exceeds 128 bytes")
	ErrBalanceLedgerIdempotencyConflict    = errors.New("balance adjustment idempotency key was already used for a different request")
	ErrBalanceLedgerZeroDelta              = errors.New("balance adjustment delta must not be zero")
	ErrBalanceLedgerNegativeBalance        = errors.New("balance adjustment would produce a negative balance")
	ErrBalanceLedgerOverflow               = errors.New("balance adjustment would overflow the supported balance range")
	ErrBalanceLedgerImmutable              = errors.New("balance ledger entries are immutable")
	ErrBalanceLedgerCacheSync              = errors.New("balance adjustment committed but user cache synchronization failed")

	balanceLedgerSQLiteMu  sync.Mutex
	balanceLedgerCacheSync = InvalidateUserCache
)

// BalanceLedger is an append-only record of a user balance change.
type BalanceLedger struct {
	ID             int       `json:"id"`
	UserID         int       `json:"user_id" gorm:"not null;index:idx_balance_ledger_user"`
	OperatorID     int       `json:"operator_id" gorm:"not null"`
	Delta          int64     `json:"delta" gorm:"type:bigint;not null"`
	BalanceBefore  int64     `json:"balance_before" gorm:"type:bigint;not null"`
	BalanceAfter   int64     `json:"balance_after" gorm:"type:bigint;not null"`
	Reason         string    `json:"reason" gorm:"type:text;not null"`
	IdempotencyKey string    `json:"idempotency_key" gorm:"type:varchar(128);not null;uniqueIndex:idx_balance_ledger_idempotency_key"`
	RequestID      string    `json:"request_id" gorm:"type:varchar(128);not null;default:''"`
	SourceType     string    `json:"source_type" gorm:"type:varchar(32);not null"`
	CreatedAt      time.Time `json:"created_at" gorm:"not null;index:idx_balance_ledger_created_at"`
}

type BalanceAdjustment struct {
	UserID         int
	OperatorID     int
	Delta          int64
	Reason         string
	IdempotencyKey string
	RequestID      string
	SourceType     string
}

func (*BalanceLedger) BeforeUpdate(*gorm.DB) error {
	return ErrBalanceLedgerImmutable
}

func (*BalanceLedger) BeforeDelete(*gorm.DB) error {
	return ErrBalanceLedgerImmutable
}

// registerBalanceLedgerImmutability blocks mutation through GORM's update and
// delete callback pipelines, including APIs that skip model hooks. Direct SQL
// is intentionally outside this application-layer guard.
func registerBalanceLedgerImmutability(db *gorm.DB) error {
	if db == nil {
		return errors.New("balance ledger immutability requires a database")
	}
	updateCallbacks := db.Callback().Update()
	if updateCallbacks.Get(balanceLedgerUpdateGuardName) == nil {
		if err := updateCallbacks.Before("gorm:update").Register(balanceLedgerUpdateGuardName, rejectBalanceLedgerMutation); err != nil {
			return err
		}
	}
	deleteCallbacks := db.Callback().Delete()
	if deleteCallbacks.Get(balanceLedgerDeleteGuardName) == nil {
		if err := deleteCallbacks.Before("gorm:delete").Register(balanceLedgerDeleteGuardName, rejectBalanceLedgerMutation); err != nil {
			return err
		}
	}
	return nil
}

func rejectBalanceLedgerMutation(tx *gorm.DB) {
	targetsLedger := balanceLedgerTableExpressionTargetsLedger(tx.Statement.Table)
	if !targetsLedger && tx.Statement.Schema != nil {
		targetsLedger = balanceLedgerTableExpressionTargetsLedger(tx.Statement.Schema.Table)
	}
	if !targetsLedger && tx.Statement.TableExpr != nil {
		targetsLedger = balanceLedgerTableExpressionValueTargetsLedger(tx.Statement.TableExpr)
	}
	if targetsLedger {
		_ = tx.AddError(ErrBalanceLedgerImmutable)
	}
}

func balanceLedgerTableExpressionTargetsLedger(expression string) bool {
	position := 0
	for position < len(expression) {
		next := skipBalanceLedgerSQLTrivia(expression, position)
		if next != position {
			position = next
			continue
		}
		if position >= len(expression) {
			break
		}
		if expression[position] == '\'' {
			position = skipBalanceLedgerSQLString(expression, position)
			continue
		}

		identifier, next, ok := parseBalanceLedgerSQLIdentifier(expression, position)
		if ok {
			if strings.EqualFold(identifier, "balance_ledgers") {
				return true
			}
			position = next
			continue
		}
		if expression[position] == '`' || expression[position] == '"' || expression[position] == '[' {
			return false
		}
		position++
	}
	return false
}

type balanceLedgerTableExpressionVisit struct {
	kind    reflect.Kind
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

func balanceLedgerTableExpressionValueTargetsLedger(value interface{}) bool {
	return balanceLedgerTableExpressionValueTargetsLedgerRecursive(value, make(map[balanceLedgerTableExpressionVisit]struct{}))
}

func balanceLedgerTableExpressionValueTargetsLedgerRecursive(value interface{}, visited map[balanceLedgerTableExpressionVisit]struct{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return balanceLedgerTableExpressionTargetsLedger(typed)
	case clause.Table:
		return balanceLedgerTableExpressionTargetsLedger(typed.Name) || balanceLedgerTableExpressionTargetsLedger(typed.Alias)
	case *clause.Table:
		if typed == nil || balanceLedgerTableExpressionPointerVisited(reflect.ValueOf(typed), visited) {
			return false
		}
		return balanceLedgerTableExpressionTargetsLedger(typed.Name) || balanceLedgerTableExpressionTargetsLedger(typed.Alias)
	case clause.Expr:
		return balanceLedgerTableExpressionTargetsLedger(typed.SQL) || balanceLedgerTableExpressionValueTargetsLedgerRecursive(typed.Vars, visited)
	case *clause.Expr:
		if typed == nil || balanceLedgerTableExpressionPointerVisited(reflect.ValueOf(typed), visited) {
			return false
		}
		return balanceLedgerTableExpressionTargetsLedger(typed.SQL) || balanceLedgerTableExpressionValueTargetsLedgerRecursive(typed.Vars, visited)
	}

	reflected := reflect.ValueOf(value)
	for reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return false
		}
		reflected = reflected.Elem()
	}
	switch reflected.Kind() {
	case reflect.Slice:
		if reflected.IsNil() || balanceLedgerTableExpressionPointerVisited(reflected, visited) {
			return false
		}
		fallthrough
	case reflect.Array:
		for index := 0; index < reflected.Len(); index++ {
			if reflected.Index(index).CanInterface() && balanceLedgerTableExpressionValueTargetsLedgerRecursive(reflected.Index(index).Interface(), visited) {
				return true
			}
		}
	}
	return false
}

func balanceLedgerTableExpressionPointerVisited(value reflect.Value, visited map[balanceLedgerTableExpressionVisit]struct{}) bool {
	visit := balanceLedgerTableExpressionVisit{kind: value.Kind(), typeOf: value.Type(), pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.length = value.Len()
	}
	if _, ok := visited[visit]; ok {
		return true
	}
	visited[visit] = struct{}{}
	return false
}

func parseBalanceLedgerSQLIdentifier(input string, position int) (string, int, bool) {
	if position >= len(input) {
		return "", position, false
	}
	opening := input[position]
	if opening == '`' || opening == '"' || opening == '[' {
		closing := opening
		if opening == '[' {
			closing = ']'
		}
		position++
		var identifier strings.Builder
		for position < len(input) {
			if input[position] == closing {
				if position+1 < len(input) && input[position+1] == closing {
					identifier.WriteByte(closing)
					position += 2
					continue
				}
				return identifier.String(), position + 1, identifier.Len() > 0
			}
			identifier.WriteByte(input[position])
			position++
		}
		return "", position, false
	}

	start := position
	for position < len(input) && isBalanceLedgerSQLIdentifierByte(input[position]) {
		position++
	}
	if start == position {
		return "", position, false
	}
	return input[start:position], position, true
}

func skipBalanceLedgerSQLTrivia(input string, position int) int {
	for position < len(input) {
		switch input[position] {
		case ' ', '\t', '\r', '\n':
			position++
		case '#':
			for position < len(input) && input[position] != '\n' {
				position++
			}
		default:
			if position+1 < len(input) && input[position] == '-' && input[position+1] == '-' {
				position += 2
				for position < len(input) && input[position] != '\n' {
					position++
				}
				continue
			}
			if position+1 < len(input) && input[position] == '/' && input[position+1] == '*' {
				end := strings.Index(input[position+2:], "*/")
				if end < 0 {
					return len(input)
				}
				position += end + 4
				continue
			}
			return position
		}
	}
	return position
}

func skipBalanceLedgerSQLString(input string, position int) int {
	position++
	for position < len(input) {
		if input[position] == '\\' && position+1 < len(input) {
			position += 2
			continue
		}
		if input[position] == '\'' {
			if position+1 < len(input) && input[position+1] == '\'' {
				position += 2
				continue
			}
			return position + 1
		}
		position++
	}
	return position
}

func isBalanceLedgerSQLIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '_' || value == '$'
}

// AdjustUserBalance atomically updates a user balance and appends its ledger row.
// The bool result is true only when this call applied the adjustment.
func AdjustUserBalance(adjustment BalanceAdjustment) (*BalanceLedger, bool, error) {
	return adjustUserBalance(adjustment, false)
}

// ReserveUserBalanceDebit atomically reserves prepaid wallet quota and records
// the debit so the complete request lifecycle can be reconstructed by request ID.
func ReserveUserBalanceDebit(userID int, amount int64, idempotencyKey string, requestID string) (*BalanceLedger, bool, error) {
	if amount <= 0 {
		return nil, false, ErrBalanceLedgerZeroDelta
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, false, ErrBalanceLedgerRequestIDRequired
	}
	entry, applied, err := adjustUserBalance(BalanceAdjustment{
		UserID:         userID,
		Delta:          -amount,
		Reason:         "prepaid wallet usage reservation",
		IdempotencyKey: idempotencyKey,
		RequestID:      requestID,
		SourceType:     BalanceLedgerSourceUsageReservation,
	}, false)
	if errors.Is(err, ErrBalanceLedgerNegativeBalance) {
		return nil, false, ErrInsufficientUserQuota
	}
	return entry, applied, err
}

// SettleUserBalanceDebit records usage that exceeded the prepaid reservation.
// Unlike an administrative adjustment, a settlement debit may make the balance
// negative so the debt remains auditable. The user is disabled in the same
// transaction when that happens.
func SettleUserBalanceDebit(userID int, amount int64, idempotencyKey string, requestID string) (*BalanceLedger, bool, error) {
	if amount <= 0 {
		return nil, false, ErrBalanceLedgerZeroDelta
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, false, ErrBalanceLedgerRequestIDRequired
	}
	return adjustUserBalance(BalanceAdjustment{
		UserID:         userID,
		Delta:          -amount,
		Reason:         "actual usage exceeded the prepaid wallet reservation",
		IdempotencyKey: idempotencyKey,
		RequestID:      requestID,
		SourceType:     BalanceLedgerSourceUsageSettlement,
	}, true)
}

func adjustUserBalance(adjustment BalanceAdjustment, allowNegative bool) (*BalanceLedger, bool, error) {
	if adjustment.Delta == 0 {
		return nil, false, ErrBalanceLedgerZeroDelta
	}
	adjustment.Reason = strings.TrimSpace(adjustment.Reason)
	if adjustment.Reason == "" {
		return nil, false, ErrBalanceLedgerReasonRequired
	}
	adjustment.IdempotencyKey = strings.TrimSpace(adjustment.IdempotencyKey)
	if adjustment.IdempotencyKey == "" {
		return nil, false, ErrBalanceLedgerIdempotencyKeyRequired
	}
	if len(adjustment.IdempotencyKey) > BalanceLedgerIdempotencyKeyMaxLength {
		return nil, false, ErrBalanceLedgerIdempotencyKeyTooLong
	}
	if adjustment.SourceType == "" {
		adjustment.SourceType = BalanceLedgerSourceAdmin
	}

	if DB.Dialector.Name() == "sqlite" {
		balanceLedgerSQLiteMu.Lock()
		defer balanceLedgerSQLiteMu.Unlock()
	}

	var entry BalanceLedger
	applied := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("idempotency_key = ?", adjustment.IdempotencyKey).Take(&entry).Error; err == nil {
			if !balanceLedgerRequestMatches(entry, adjustment) {
				return ErrBalanceLedgerIdempotencyConflict
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var user User
		userQuery := tx.Where("id = ?", adjustment.UserID)
		if tx.Dialector.Name() != "sqlite" {
			userQuery = userQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := userQuery.First(&user).Error; err != nil {
			return err
		}

		before := int64(user.Quota)
		after, err := checkedBalanceLedgerSum(before, adjustment.Delta)
		if allowNegative {
			after, err = checkedSettlementBalanceLedgerSum(before, adjustment.Delta)
		}
		if err != nil {
			return err
		}
		updates := balanceLedgerUserUpdates(user, after, allowNegative)
		if err := tx.Model(&User{}).Where("id = ?", user.Id).Updates(updates).Error; err != nil {
			return err
		}

		entry = BalanceLedger{
			UserID:         user.Id,
			OperatorID:     adjustment.OperatorID,
			Delta:          adjustment.Delta,
			BalanceBefore:  before,
			BalanceAfter:   after,
			Reason:         adjustment.Reason,
			IdempotencyKey: adjustment.IdempotencyKey,
			RequestID:      adjustment.RequestID,
			SourceType:     adjustment.SourceType,
			CreatedAt:      time.Now().UTC(),
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrBalanceLedgerIdempotencyConflict) {
			return nil, false, err
		}
		var existing BalanceLedger
		if lookupErr := DB.Where("idempotency_key = ?", adjustment.IdempotencyKey).Take(&existing).Error; lookupErr == nil {
			if !balanceLedgerRequestMatches(existing, adjustment) {
				return nil, false, ErrBalanceLedgerIdempotencyConflict
			}
			entry = existing
			applied = false
		} else {
			return nil, false, err
		}
	}
	if syncErr := balanceLedgerCacheSync(entry.UserID); syncErr != nil {
		return &entry, applied, fmt.Errorf("%w: ledger %d is committed; retry with the same idempotency key: %v", ErrBalanceLedgerCacheSync, entry.ID, syncErr)
	}
	return &entry, applied, nil
}

func checkedSettlementBalanceLedgerSum(balance int64, delta int64) (int64, error) {
	if balance < -maxBalanceLedgerQuota || balance > maxBalanceLedgerQuota {
		return 0, ErrBalanceLedgerOverflow
	}
	if delta < -maxBalanceLedgerQuota || delta > maxBalanceLedgerQuota {
		return 0, ErrBalanceLedgerOverflow
	}
	after := balance + delta
	if after < -maxBalanceLedgerQuota || after > maxBalanceLedgerQuota {
		return 0, ErrBalanceLedgerOverflow
	}
	return after, nil
}

func balanceLedgerRequestMatches(entry BalanceLedger, adjustment BalanceAdjustment) bool {
	if entry.UserID != adjustment.UserID ||
		entry.OperatorID != adjustment.OperatorID ||
		entry.Delta != adjustment.Delta ||
		entry.Reason != adjustment.Reason ||
		entry.SourceType != adjustment.SourceType {
		return false
	}
	if adjustment.SourceType == BalanceLedgerSourceUsageReservation ||
		adjustment.SourceType == BalanceLedgerSourceUsageSettlement {
		return entry.RequestID == adjustment.RequestID
	}
	return true
}

func checkedBalanceLedgerSum(balance int64, delta int64) (int64, error) {
	if balance < -maxBalanceLedgerQuota || balance > maxBalanceLedgerQuota {
		return 0, ErrBalanceLedgerOverflow
	}
	if delta > 0 && delta > maxBalanceLedgerQuota-balance {
		return 0, ErrBalanceLedgerOverflow
	}
	if delta < 0 {
		if balance < 0 || delta < -balance {
			return 0, ErrBalanceLedgerNegativeBalance
		}
	}
	return balance + delta, nil
}

// balanceLedgerUserUpdates preserves the distinction between debt suspension
// and an explicit administrative disable. Only debt-suspended users are
// automatically enabled after their balance is fully restored.
func balanceLedgerUserUpdates(user User, after int64, settlement bool) map[string]any {
	updates := map[string]any{"quota": int(after)}
	if settlement && after < 0 {
		if user.Status == common.UserStatusEnabled || user.DebtSuspended {
			updates["status"] = common.UserStatusDisabled
			updates["debt_suspended"] = true
		}
		return updates
	}
	if after >= 0 && user.DebtSuspended {
		updates["debt_suspended"] = false
		if user.Status == common.UserStatusDisabled {
			updates["status"] = common.UserStatusEnabled
		}
	}
	return updates
}

func GetBalanceLedger(userID *int, startIdx int, pageSize int) ([]BalanceLedger, int64, error) {
	entries := make([]BalanceLedger, 0)
	var total int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&BalanceLedger{})
		if userID != nil {
			query = query.Where("user_id = ?", *userID)
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return query.Order("created_at DESC, id DESC").Offset(startIdx).Limit(pageSize).Find(&entries).Error
	})
	return entries, total, err
}
