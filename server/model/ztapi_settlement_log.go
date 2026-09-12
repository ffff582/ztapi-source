package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrZTAPISettlementLogInvalid  = errors.New("invalid settlement consumption log")
	ErrZTAPISettlementLogConflict = errors.New("settlement consumption log payload conflicts")
)

type ZTAPISettlementLogOutbox struct {
	OperationID         string `gorm:"type:varchar(64);primaryKey"`
	PayloadJSON         string `gorm:"type:text;not null"`
	PayloadSHA256       string `gorm:"type:varchar(64);not null"`
	ChannelStatsApplied bool   `gorm:"not null;default:false"`
	CreatedAt           int64  `gorm:"type:bigint;not null"`
	DeliveredAt         int64  `gorm:"type:bigint;not null;default:0;index"`
	LogID               int    `gorm:"not null;default:0"`
}

func (ZTAPISettlementLogOutbox) TableName() string { return "ztapi_settlement_log_outboxes" }

type ZTAPISettlementLogReceipt struct {
	OperationID   string `gorm:"type:varchar(64);primaryKey"`
	PayloadSHA256 string `gorm:"type:varchar(64);not null"`
	LogID         int    `gorm:"not null;default:0"`
	CreatedAt     int64  `gorm:"type:bigint;not null"`
}

func (ZTAPISettlementLogReceipt) TableName() string { return "ztapi_settlement_log_receipts" }

func MigrateZTAPISettlementLogOutbox(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPISettlementLogInvalid
	}
	return db.AutoMigrate(&ZTAPISettlementLogOutbox{})
}

func MigrateLogDBZTAPISettlementLogs(db *gorm.DB) error {
	if db == nil {
		return ErrZTAPISettlementLogInvalid
	}
	return db.AutoMigrate(&ZTAPISettlementLogReceipt{})
}

// EnqueueZTAPISettlementLogTx must run inside the canonical finalization
// transaction. The caller supplies sanitized billing metadata, never request
// prompts, generated output, credentials, or raw provider responses.
func EnqueueZTAPISettlementLogTx(tx *gorm.DB, row *ZTAPIRequestSettlement, log Log) error {
	if tx == nil || tx.Statement == nil || row == nil {
		return ErrZTAPISettlementLogInvalid
	}
	if _, transactional := tx.Statement.ConnPool.(gorm.TxCommitter); !transactional {
		return ErrZTAPISettlementLogInvalid
	}
	// The request's final attempt, not caller-supplied log metadata, owns the
	// upstream correlation ID. Absence remains explicit, never guessed.
	log.UpstreamRequestId = ""
	if row.FinalAttempt > 0 {
		var attempt ZTAPIRequestAttempt
		err := tx.Where("settlement_id = ? AND attempt = ?", row.ID, row.FinalAttempt).Take(&attempt).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			if attempt.ChannelID != log.ChannelId {
				return ErrZTAPISettlementLogConflict
			}
			log.UpstreamRequestId = attempt.UpstreamRequestID
		}
	}
	if strings.TrimSpace(row.OperationID) == "" || len(row.OperationID) > 64 ||
		row.Status != ZTAPISettlementSettled || row.UserID <= 0 || row.TokenID <= 0 ||
		row.RequestID == "" || len(row.RequestID) > 64 || row.PublicModel == "" ||
		row.ChargedQuota < 0 || row.ChargedQuota > maxBalanceLedgerQuota ||
		log.Id != 0 || log.Type != LogTypeConsume || log.UserId != row.UserID || log.TokenId != row.TokenID ||
		log.RequestId != row.RequestID || log.ModelName != row.PublicModel || int64(log.Quota) != row.ChargedQuota ||
		log.ChannelId <= 0 || log.CreatedAt <= 0 || log.PromptTokens < 0 || log.CompletionTokens < 0 ||
		log.UseTime < 0 || len(log.UpstreamRequestId) > 200 {
		return ErrZTAPISettlementLogInvalid
	}
	if err := sanitizeZTAPISettlementLogMetadata(&log); err != nil {
		return err
	}
	payload, err := common.Marshal(log)
	if err != nil {
		return err
	}
	if len(payload) > 65536 {
		return ErrZTAPISettlementLogInvalid
	}
	job := ZTAPISettlementLogOutbox{
		OperationID: row.OperationID, PayloadJSON: string(payload),
		PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), CreatedAt: common.GetTimestamp(),
	}
	// Do not infer insertion from RowsAffected: MySQL CLIENT_FOUND_ROWS can
	// report a matched duplicate as affected. Lock and inspect the durable flag.
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; err != nil {
		return err
	}
	var existing ZTAPISettlementLogOutbox
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", row.OperationID).Take(&existing).Error; err != nil {
		return err
	}
	if existing.PayloadSHA256 != job.PayloadSHA256 || existing.PayloadJSON != job.PayloadJSON {
		return ErrZTAPISettlementLogConflict
	}
	if existing.ChannelStatsApplied {
		return nil
	}
	if row.ChargedQuota > 0 {
		updated := tx.Model(&Channel{}).Where("id = ? AND used_quota >= 0 AND used_quota <= ?", log.ChannelId, maxBalanceLedgerQuota-row.ChargedQuota).
			UpdateColumn("used_quota", gorm.Expr("used_quota + ?", row.ChargedQuota))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrZTAPISettlementLogConflict
		}
	}
	return tx.Model(&ZTAPISettlementLogOutbox{}).Where("operation_id = ?", row.OperationID).
		UpdateColumn("channel_stats_applied", true).Error
}

// ProcessPendingZTAPISettlementLogs returns newly acknowledged main-DB rows.
// Delivery failures remain pending and are returned even if other rows succeed.
// Receipt records must outlive any possible replay, including log retention.
func ProcessPendingZTAPISettlementLogs(limit int) (int, error) {
	mainDB, logDB := DB, LOG_DB
	if mainDB == nil || logDB == nil || limit <= 0 || limit > 1000 {
		return 0, ErrZTAPISettlementLogInvalid
	}
	var jobs []ZTAPISettlementLogOutbox
	if err := mainDB.Where("delivered_at = ? AND channel_stats_applied = ?", 0, true).
		Order("created_at ASC, operation_id ASC").Limit(limit).Find(&jobs).Error; err != nil {
		return 0, err
	}
	delivered := 0
	var failures []error
	for _, job := range jobs {
		logID, err := deliverZTAPISettlementLog(logDB, job)
		if err != nil {
			failures = append(failures, fmt.Errorf("settlement log %s: %w", job.OperationID, err))
			continue
		}
		ack := mainDB.Model(&ZTAPISettlementLogOutbox{}).
			Where("operation_id = ? AND payload_sha256 = ? AND delivered_at = ?", job.OperationID, job.PayloadSHA256, 0).
			Updates(map[string]any{"delivered_at": common.GetTimestamp(), "log_id": logID})
		if ack.Error != nil {
			failures = append(failures, fmt.Errorf("settlement log acknowledgement %s: %w", job.OperationID, ack.Error))
			continue
		}
		delivered += int(ack.RowsAffected)
	}
	return delivered, errors.Join(failures...)
}

func deliverZTAPISettlementLog(logDB *gorm.DB, job ZTAPISettlementLogOutbox) (int, error) {
	if fmt.Sprintf("%x", sha256.Sum256([]byte(job.PayloadJSON))) != job.PayloadSHA256 {
		return 0, ErrZTAPISettlementLogConflict
	}
	var log Log
	if err := common.UnmarshalJsonStr(job.PayloadJSON, &log); err != nil {
		return 0, ErrZTAPISettlementLogInvalid
	}
	if log.Id != 0 || log.Type != LogTypeConsume {
		return 0, ErrZTAPISettlementLogInvalid
	}
	logID := 0
	err := logDB.Transaction(func(tx *gorm.DB) error {
		receipt := ZTAPISettlementLogReceipt{OperationID: job.OperationID, PayloadSHA256: job.PayloadSHA256, CreatedAt: common.GetTimestamp()}
		// Insert before reading: the unique key serializes concurrent writers,
		// including SQLite, without relying on process-local mutexes or leases.
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt).Error; err != nil {
			return err
		}
		var existing ZTAPISettlementLogReceipt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", job.OperationID).Take(&existing).Error; err != nil {
			return err
		}
		if existing.PayloadSHA256 != job.PayloadSHA256 {
			return ErrZTAPISettlementLogConflict
		}
		if existing.LogID > 0 {
			logID = existing.LogID
			return nil
		}
		if err := tx.Create(&log).Error; err != nil {
			return err
		}
		logID = log.Id
		updated := tx.Model(&ZTAPISettlementLogReceipt{}).Where("operation_id = ? AND log_id = ?", job.OperationID, 0).
			UpdateColumn("log_id", logID)
		if updated.Error != nil {
			return updated.Error
		}
		if logID <= 0 || updated.RowsAffected != 1 {
			return ErrZTAPISettlementLogConflict
		}
		return nil
	})
	return logID, err
}
