package model

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrZTAPIWorkbenchBudget = errors.New("workbench task budget unavailable")
var ErrZTAPIWorkbenchTaskLimit = fmt.Errorf("fixed task limit reached: %w", ErrZTAPIWorkbenchBudget)

var workbenchTaskPattern = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
var workbenchHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type ZTAPIWorkbenchTask struct {
	ID           string `gorm:"type:varchar(36);primaryKey"`
	UserID       int    `gorm:"not null"`
	Model        string `gorm:"type:varchar(200);not null"`
	LimitQuota   int64  `gorm:"type:bigint;not null"`
	PilotBatchID string `gorm:"type:varchar(36);not null;default:'';index"`
}

type ZTAPIWorkbenchLeg struct {
	Stage      string
	Settlement ZTAPIRequestSettlement
	Native     *ZTAPIWorkbenchNativeBounds
}

type ZTAPIWorkbenchNativeBounds struct {
	BodySHA256    string
	OutboundBytes int
	MaxOutput     int
}

// BeginZTAPIWorkbenchBatch takes authenticated, server-priced inputs. It is
// not a request decoder and does not grant permission to dispatch or retry.
func BeginZTAPIWorkbenchBatch(taskID string, limit int64, legs []ZTAPIWorkbenchLeg, pilot ...ZTAPIWorkbenchPilotPolicy) ([]ZTAPIRequestSettlement, error) {
	if len(pilot) > 1 || !workbenchTaskPattern.MatchString(taskID) || limit < 1 || limit > maxBalanceLedgerQuota || !validWorkbenchStages(legs) {
		return nil, ErrZTAPISettlementInvalid
	}
	first := legs[0].Settlement
	inputs := make([]ZTAPIRequestSettlement, len(legs))
	ids := make([]string, len(legs))
	for i, leg := range legs {
		row := leg.Settlement
		if !validZTAPIRequestSettlementInput(row) || row.ReservedQuota == 0 || row.TokenUnlimited ||
			row.UserID != first.UserID || row.TokenID != first.TokenID || row.PublicModel != first.PublicModel ||
			(i > 0 && (row.OperationID == first.OperationID || row.RequestID == first.RequestID)) {
			return nil, ErrZTAPISettlementInvalid
		}
		row.WorkbenchTaskID, row.WorkbenchBatchID, row.WorkbenchStage = taskID, first.OperationID, leg.Stage
		if bounds := leg.Native; bounds != nil {
			if (!workbenchHashPattern.MatchString(bounds.BodySHA256) && !(leg.Stage == "continuation" && bounds.BodySHA256 == "")) || bounds.OutboundBytes < 1 || bounds.OutboundBytes > 196608 || bounds.MaxOutput < 1 || bounds.MaxOutput > 4096 {
				return nil, ErrZTAPISettlementInvalid
			}
			row.WorkbenchBodySHA256, row.WorkbenchOutboundMax, row.WorkbenchOutputMax = bounds.BodySHA256, bounds.OutboundBytes, bounds.MaxOutput
		}
		inputs[i], ids[i] = row, row.OperationID
	}
	result := make([]ZTAPIRequestSettlement, 0, len(legs))
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		want := ZTAPIWorkbenchTask{ID: taskID, UserID: first.UserID, Model: first.PublicModel, LimitQuota: limit}
		if len(pilot) == 1 {
			p := pilot[0]
			if p.UserID != first.UserID || !p.allowsModel(first.PublicModel) {
				return ErrZTAPISettlementConflict
			}
			for _, leg := range legs {
				if leg.Native == nil {
					return ErrZTAPISettlementInvalid
				}
			}
			if _, err := lockZTAPIWorkbenchPilot(tx, p); err != nil {
				return err
			}
			want.PilotBatchID = p.BatchID
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&want).Error; err != nil {
			return err
		}
		var scope ZTAPIWorkbenchTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", taskID).Take(&scope).Error; err != nil {
			return err
		}
		if scope.UserID != want.UserID || scope.Model != want.Model || scope.LimitQuota != want.LimitQuota || scope.PilotBatchID != want.PilotBatchID {
			return ErrZTAPISettlementConflict
		}
		// Read the request rows after acquiring the task lock. Different keys
		// owned by the same user share the task limit across session renewal.
		var found []ZTAPIRequestSettlement
		if err := tx.Where("operation_id IN ?", ids).Find(&found).Error; err != nil {
			return err
		}
		if len(found) > 0 {
			if len(found) != len(inputs) {
				return ErrZTAPISettlementConflict
			}
			for _, input := range inputs {
				for _, row := range found {
					if row.OperationID == input.OperationID {
						if input.WorkbenchStage == "continuation" && input.WorkbenchBodySHA256 == "" && input.WorkbenchOutboundMax > 0 {
							input.WorkbenchBodySHA256 = row.WorkbenchBodySHA256
						}
						if err := ztapiSettlementMatches(row, input); err != nil {
							return err
						}
						result = append(result, row)
					}
				}
			}
			if len(result) != len(inputs) {
				return ErrZTAPISettlementConflict
			}
			return nil
		}
		var usage struct {
			SpentQuota   int64
			OverrunCount int64
		}
		if err := tx.Model(&ZTAPIRequestSettlement{}).
			Select(`COALESCE(SUM(CASE WHEN status = ? THEN charged_quota ELSE reserved_quota END), 0) AS spent_quota,
				COUNT(CASE WHEN status = ? AND charged_quota > reserved_quota THEN 1 END) AS overrun_count`, ZTAPISettlementSettled, ZTAPISettlementSettled).
			Where("workbench_task_id = ? AND status <> ?", taskID, ZTAPISettlementReleased).Scan(&usage).Error; err != nil {
			return err
		}
		if usage.OverrunCount != 0 {
			return ErrZTAPIWorkbenchBudget
		}
		if len(pilot) == 1 {
			if err := checkZTAPIWorkbenchPilotWallet(tx, pilot[0], inputs); err != nil {
				return err
			}
		}
		// Temporary holds from other legs do not make an otherwise valid request permanently oversized.
		maxBatch := limit
		for _, input := range inputs {
			if input.ReservedQuota > maxBatch {
				return ErrZTAPIWorkbenchTaskLimit
			}
			maxBatch -= input.ReservedQuota
		}
		remaining := limit - usage.SpentQuota
		for _, input := range inputs {
			if input.ReservedQuota > remaining {
				return ErrZTAPIWorkbenchBudget
			}
			remaining -= input.ReservedQuota
			row, err := createZTAPIRequestSettlementTx(tx, input)
			if err != nil {
				return err
			}
			result = append(result, *row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range result {
		syncZTAPISettlementCaches(&result[i])
	}
	return result, nil
}

// BindZTAPIWorkbenchContinuation pins the previously unknown continuation body.
func BindZTAPIWorkbenchContinuation(taskID, hold string, userID, tokenID int, hash, snapshot string, bytes int64) error {
	if !workbenchTaskPattern.MatchString(taskID) || !workbenchTaskPattern.MatchString(hold) || !workbenchHashPattern.MatchString(hash) || bytes < 1 {
		return ErrZTAPISettlementInvalid
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var task ZTAPIWorkbenchTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", taskID, userID).Take(&task).Error; err != nil {
			return err
		}
		row, err := ztapiSettlementLock(tx, hold)
		if err != nil {
			return err
		}
		if row.WorkbenchTaskID != taskID || row.UserID != userID || row.TokenID != tokenID || row.WorkbenchStage != "continuation" || row.PublicModel != task.Model ||
			row.PriceSnapshotJSON != snapshot || row.Status != ZTAPISettlementReserved || row.Dispatched || bytes > int64(row.WorkbenchOutboundMax) {
			return ErrZTAPISettlementConflict
		}
		summary, err := ztapiSettlementLock(tx, row.WorkbenchBatchID)
		if err != nil {
			return err
		}
		if summary.WorkbenchTaskID != taskID || summary.WorkbenchStage != "summary" || summary.Status != ZTAPISettlementSettled || !summary.Dispatched {
			return ErrZTAPISettlementPending
		}
		if row.WorkbenchBodySHA256 != "" {
			if row.WorkbenchBodySHA256 != hash {
				return ErrZTAPISettlementConflict
			}
			return nil
		}
		return tx.Model(row).Update("workbench_body_sha256", hash).Error
	})
}

// ClaimZTAPIWorkbenchDispatch persists the one-time dispatch before provider I/O.
// Callers must authenticate the key and validate the exact request before claiming.
func ClaimZTAPIWorkbenchDispatch(taskID string, userID, tokenID int, operationID string) error {
	return claimZTAPIWorkbenchDispatch(taskID, userID, tokenID, operationID, nil, nil)
}

func ClaimZTAPIWorkbenchNativeAttempt(taskID string, userID, tokenID int, operationID string, channelID int, credential, path string, pilot ...ZTAPIWorkbenchPilotPolicy) (*ZTAPIRequestAttempt, error) {
	const yunxinSolPath = "/hub/v1/responses"
	const messagesPath = ZTAPIWorkbenchMessagesPath
	if len(pilot) > 1 || channelID <= 0 || !workbenchHashPattern.MatchString(credential) ||
		(path != "/v1/responses" && path != ZTAPIWorkbenchChatPath && path != yunxinSolPath &&
			path != messagesPath && path != ZTAPIWorkbenchHubChatPath) {
		return nil, ErrZTAPISettlementInvalid
	}
	if len(pilot) == 0 && (path == messagesPath || path == ZTAPIWorkbenchHubChatPath) {
		return nil, ErrZTAPISettlementConflict
	}
	if len(pilot) == 1 {
		p := pilot[0]
		if p.UserID != userID || p.ChannelID != channelID || path != p.ExpectedWirePath() {
			return nil, ErrZTAPISettlementConflict
		}
	}
	var attempt ZTAPIRequestAttempt
	err := claimZTAPIWorkbenchDispatch(taskID, userID, tokenID, operationID, pilot, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if path == yunxinSolPath && row.PublicModel != "zt-gpt-6.1-sol" {
			return ErrZTAPISettlementInvalid
		}
		attempt = ZTAPIRequestAttempt{SettlementID: row.ID, Attempt: 1, ChannelID: channelID, CredentialVersion: credential, Protocol: path}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func claimZTAPIWorkbenchDispatch(taskID string, userID, tokenID int, operationID string, pilot []ZTAPIWorkbenchPilotPolicy, recordAttempt func(*gorm.DB, *ZTAPIRequestSettlement) error) error {
	if !workbenchTaskPattern.MatchString(taskID) || userID < 1 || tokenID < 1 || operationID == "" || len(operationID) > 64 {
		return ErrZTAPISettlementInvalid
	}
	return ztapiSettlementTransaction(func(tx *gorm.DB) error {
		var batch *ZTAPIWorkbenchPilotSpend
		var batchID string
		if len(pilot) == 1 {
			var err error
			batch, err = lockZTAPIWorkbenchPilot(tx, pilot[0])
			if err != nil {
				return err
			}
			batchID = batch.ID
		}
		var scope ZTAPIWorkbenchTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", taskID, userID).Take(&scope).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrZTAPISettlementConflict
			}
			return err
		}
		row, err := ztapiSettlementLock(tx, operationID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrZTAPISettlementConflict
			}
			return err
		}
		if scope.PilotBatchID != batchID || row.OperationID != operationID || row.WorkbenchTaskID != taskID || row.UserID != userID || row.TokenID != tokenID ||
			row.PublicModel != scope.Model || row.Status != ZTAPISettlementReserved || row.Dispatched || (row.WorkbenchOutboundMax > 0) != (recordAttempt != nil) ||
			(recordAttempt != nil && !workbenchHashPattern.MatchString(row.WorkbenchBodySHA256)) {
			return ErrZTAPISettlementConflict
		}
		if row.WorkbenchStage == "continuation" {
			summary, err := ztapiSettlementLock(tx, row.WorkbenchBatchID)
			if err != nil {
				return err
			}
			if summary.WorkbenchTaskID != taskID || summary.WorkbenchBatchID != row.WorkbenchBatchID || summary.WorkbenchStage != "summary" ||
				summary.Status != ZTAPISettlementSettled || !summary.Dispatched {
				return ErrZTAPISettlementPending
			}
		}
		user, token, err := ztapiSettlementOwners(tx, row)
		if err != nil {
			return err
		}
		// An exact reservation exhausts the key, but this row has already paid for dispatch.
		fundedKey := token.Status == common.TokenStatusEnabled || (token.Status == common.TokenStatusExhausted && token.RemainQuota == 0 && row.TokenReservedQuota == row.ReservedQuota)
		if user.Status != common.UserStatusEnabled || user.DeletedAt.Valid || token.DeletedAt.Valid || !fundedKey ||
			token.UnlimitedQuota || (token.ExpiredTime != -1 && token.ExpiredTime <= time.Now().Unix()) {
			return ErrZTAPISettlementConflict
		}
		if recordAttempt != nil && (!token.ModelLimitsEnabled || token.ModelLimits != row.PublicModel || token.ExpiredTime < 1 || token.ExpiredTime > time.Now().Add(30*time.Minute).Unix()) {
			return ErrZTAPISettlementConflict
		}
		var overruns int64
		if err = tx.Model(&ZTAPIRequestSettlement{}).Where("workbench_task_id = ? AND status = ? AND charged_quota > reserved_quota", taskID, ZTAPISettlementSettled).Count(&overruns).Error; err != nil {
			return err
		}
		if overruns != 0 {
			return ErrZTAPIWorkbenchBudget
		}
		if batch != nil {
			if !pilot[0].allowsModel(row.PublicModel) {
				return ErrZTAPISettlementConflict
			}
			if err := checkZTAPIWorkbenchPilotWallet(tx, pilot[0], nil); err != nil {
				return err
			}
			if err := recordZTAPIWorkbenchPilotClaim(tx, batch, row, pilot[0]); err != nil {
				return err
			}
		}
		if recordAttempt != nil {
			if err = recordAttempt(tx, row); err != nil {
				return err
			}
		}
		return tx.Model(row).Update("dispatched", true).Error
	})
}

func validWorkbenchStages(legs []ZTAPIWorkbenchLeg) bool {
	switch len(legs) {
	case 1:
		return legs[0].Stage == "main" || legs[0].Stage == "child"
	case 2:
		return legs[0].Stage == "summary" && legs[1].Stage == "continuation"
	default:
		return false
	}
}
