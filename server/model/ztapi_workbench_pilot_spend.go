package model

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZTAPIWorkbenchFableMaxRequests = 14
	ZTAPIWorkbenchFableMaxNanoU    = 3_000_000_000
)

var ErrZTAPIWorkbenchPilotLimit = errors.New("workbench pilot dispatch limit reached")

type ZTAPIWorkbenchPilotSpend struct {
	ID           string `gorm:"type:varchar(36);primaryKey"`
	PolicySHA256 string `gorm:"type:varchar(64);not null"`
	ClaimsJSON   string `gorm:"type:text;not null"`
}

type ZTAPIWorkbenchPilotPolicy struct {
	BatchID      string
	PolicySHA256 string
	ExpiresAt    int64
	MaxRequests  int
	BudgetQuota  int64
	UserID       int
	ChannelID    int
}

type workbenchPilotClaim struct {
	SettlementID uint  `json:"settlement_id"`
	ReserveQuota int64 `json:"reserve_quota"`
}

func ZTAPIWorkbenchPilotWalletQuota(nanoU int64) (int64, bool) {
	unit := common.QuotaPerUnit
	if nanoU < 1 || nanoU > ZTAPIWorkbenchFableMaxNanoU || unit <= 0 || math.IsNaN(unit) || math.IsInf(unit, 0) {
		return 0, false
	}
	// Floor the maximum authorized debit; never round the cap up.
	q := decimal.NewFromInt(nanoU).Mul(decimal.NewFromFloat(unit)).Shift(-9).Floor()
	if !q.IsPositive() || q.GreaterThan(decimal.NewFromInt(maxBalanceLedgerQuota)) {
		return 0, false
	}
	return q.IntPart(), true
}

func (p ZTAPIWorkbenchPilotPolicy) ValidAt(now time.Time) bool {
	maxQuota, ok := ZTAPIWorkbenchPilotWalletQuota(ZTAPIWorkbenchFableMaxNanoU)
	return ok && workbenchTaskPattern.MatchString(p.BatchID) &&
		p.BatchID != "00000000-0000-0000-0000-000000000000" &&
		workbenchHashPattern.MatchString(p.PolicySHA256) && p.PolicySHA256 != strings.Repeat("0", 64) &&
		p.ExpiresAt > now.Unix() && p.ExpiresAt <= now.Add(30*time.Minute).Unix() &&
		p.MaxRequests > 0 && p.MaxRequests <= ZTAPIWorkbenchFableMaxRequests &&
		p.BudgetQuota > 0 && p.BudgetQuota <= maxQuota &&
		p.UserID > 0 && p.ChannelID > 0
}

// Take this lock before task, settlement, or owner locks in both admission paths.
func lockZTAPIWorkbenchPilot(tx *gorm.DB, p ZTAPIWorkbenchPilotPolicy) (*ZTAPIWorkbenchPilotSpend, error) {
	if !p.ValidAt(time.Now()) {
		return nil, ErrZTAPIWorkbenchPilotLimit
	}
	batch := ZTAPIWorkbenchPilotSpend{ID: p.BatchID, PolicySHA256: p.PolicySHA256, ClaimsJSON: "[]"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error; err != nil {
		return nil, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", p.BatchID).Take(&batch).Error; err != nil {
		return nil, err
	}
	if batch.PolicySHA256 != p.PolicySHA256 {
		return nil, ErrZTAPISettlementConflict
	}
	if p.ExpiresAt <= time.Now().Unix() {
		return nil, ErrZTAPIWorkbenchPilotLimit
	}
	return &batch, nil
}

func checkZTAPIWorkbenchPilotWallet(tx *gorm.DB, p ZTAPIWorkbenchPilotPolicy, legs []ZTAPIRequestSettlement) error {
	var spent int64
	tasks := tx.Model(&ZTAPIWorkbenchTask{}).Select("id").Where("pilot_batch_id = ?", p.BatchID)
	if err := tx.Model(&ZTAPIRequestSettlement{}).
		Select("COALESCE(SUM(CASE WHEN status = ? THEN charged_quota ELSE reserved_quota END), 0)", ZTAPISettlementSettled).
		Where("workbench_task_id IN (?) AND status <> ?", tasks, ZTAPISettlementReleased).Scan(&spent).Error; err != nil {
		return err
	}
	if spent < 0 || spent > p.BudgetQuota {
		return ErrZTAPIWorkbenchBudget
	}
	remaining := p.BudgetQuota - spent
	for _, leg := range legs {
		if leg.ReservedQuota <= 0 || leg.ReservedQuota > remaining {
			return ErrZTAPIWorkbenchBudget
		}
		remaining -= leg.ReservedQuota
	}
	return nil
}

func recordZTAPIWorkbenchPilotClaim(tx *gorm.DB, batch *ZTAPIWorkbenchPilotSpend, row *ZTAPIRequestSettlement, p ZTAPIWorkbenchPilotPolicy) error {
	if row.ReservedQuota < 1 || row.ReservedQuota > p.BudgetQuota {
		return ErrZTAPIWorkbenchBudget
	}
	var claims []workbenchPilotClaim
	if common.Unmarshal([]byte(batch.ClaimsJSON), &claims) != nil || claims == nil {
		return ErrZTAPISettlementConflict
	}
	seen := make(map[uint]bool, len(claims))
	for _, claim := range claims {
		if claim.SettlementID == 0 || claim.SettlementID == row.ID || seen[claim.SettlementID] ||
			claim.ReserveQuota <= 0 || claim.ReserveQuota > p.BudgetQuota {
			return ErrZTAPISettlementConflict
		}
		seen[claim.SettlementID] = true
	}
	if len(claims) >= p.MaxRequests || p.ExpiresAt <= time.Now().Unix() {
		return ErrZTAPIWorkbenchPilotLimit
	}
	updated, err := common.Marshal(append(claims, workbenchPilotClaim{SettlementID: row.ID, ReserveQuota: row.ReservedQuota}))
	if err != nil {
		return err
	}
	result := tx.Model(batch).Where("claims_json = ?", batch.ClaimsJSON).Update("claims_json", string(updated))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrZTAPISettlementConflict
	}
	return nil
}

func checkZTAPIWorkbenchPilotFinalDebit(tx *gorm.DB, row *ZTAPIRequestSettlement, actual int64) error {
	if row.WorkbenchTaskID == "" || actual <= row.ReservedQuota {
		return nil
	}
	var task ZTAPIWorkbenchTask
	if err := tx.Select("pilot_batch_id").Where("id = ?", row.WorkbenchTaskID).Take(&task).Error; err != nil {
		return err
	}
	if task.PilotBatchID != "" {
		return ErrZTAPIWorkbenchBudget
	}
	return nil
}
