package model

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ZTAPIMediaTaskState string

const (
	ZTAPIMediaTaskReserved   ZTAPIMediaTaskState = "reserved"
	ZTAPIMediaTaskSubmitted  ZTAPIMediaTaskState = "submitted"
	ZTAPIMediaTaskProcessing ZTAPIMediaTaskState = "processing"
	ZTAPIMediaTaskSucceeded  ZTAPIMediaTaskState = "succeeded"
	ZTAPIMediaTaskFailed     ZTAPIMediaTaskState = "failed"
	ZTAPIMediaTaskUnknown    ZTAPIMediaTaskState = "unknown"
)

type ZTAPIMediaChargeDisposition string

const (
	ZTAPIMediaChargeKnown    ZTAPIMediaChargeDisposition = "known"
	ZTAPIMediaChargeNoCharge ZTAPIMediaChargeDisposition = "no_charge"
	ZTAPIMediaChargeUnknown  ZTAPIMediaChargeDisposition = "unknown"
)

var (
	ErrZTAPIMediaTaskConflict          = errors.New("media task identity or transition conflicts")
	ErrZTAPIMediaTaskInvalid           = errors.New("invalid media task")
	ErrZTAPIMediaTaskFinancialPending  = errors.New("media task terminal transition requires durable financial handling")
	ErrZTAPIMediaTaskMutationForbidden = errors.New("media task mutations must use the durable state machine")
	ztapiPublicMediaTaskIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

const ztapiMediaTaskMutationScope = "ztapi_media_task:authorized_mutation"

const (
	ztapiMediaTaskCreateGuard = "ztapi_media_task:guard_create"
	ztapiMediaTaskUpdateGuard = "ztapi_media_task:guard_update"
	ztapiMediaTaskDeleteGuard = "ztapi_media_task:guard_delete"
)

type ZTAPIMediaTask struct {
	ID                        uint                        `gorm:"primaryKey" json:"id"`
	PublicTaskID              string                      `gorm:"type:varchar(128);not null;uniqueIndex" json:"public_task_id"`
	SettlementID              uint                        `gorm:"not null;uniqueIndex" json:"settlement_id"`
	RequestID                 string                      `gorm:"type:varchar(128);not null;uniqueIndex" json:"request_id"`
	UserID                    int                         `gorm:"not null;index" json:"user_id"`
	TokenID                   int                         `gorm:"not null" json:"token_id"`
	PublicModel               string                      `gorm:"type:varchar(200);not null" json:"public_model"`
	Action                    string                      `gorm:"type:varchar(32);not null" json:"action"`
	PriceSnapshotHash         string                      `gorm:"type:char(64);not null" json:"price_snapshot_hash"`
	VideoProtocolContractJSON string                      `gorm:"type:text;not null" json:"-"`
	VideoProtocolContractHash string                      `gorm:"type:char(64);not null" json:"video_protocol_contract_hash"`
	QuotaPerUnit              string                      `gorm:"type:varchar(32);not null" json:"quota_per_unit"`
	SelectorJSON              string                      `gorm:"type:text;not null" json:"selector"`
	SelectorHash              string                      `gorm:"type:char(64);not null" json:"selector_hash"`
	VideoSelectorJSON         string                      `gorm:"type:text;not null" json:"video_selector"`
	VideoSelectorHash         string                      `gorm:"type:char(64);not null" json:"video_selector_hash"`
	Attempt                   int                         `gorm:"not null;default:0" json:"attempt"`
	ChannelID                 int                         `gorm:"not null;default:0" json:"channel_id"`
	CredentialVersion         string                      `gorm:"type:varchar(64);not null" json:"-"`
	UpstreamTaskID            string                      `gorm:"type:varchar(200);not null" json:"-"`
	State                     ZTAPIMediaTaskState         `gorm:"type:varchar(16);not null;index" json:"state"`
	Version                   uint64                      `gorm:"not null" json:"version"`
	SettlementState           string                      `gorm:"type:varchar(16);not null" json:"settlement_state"`
	ChargeDisposition         ZTAPIMediaChargeDisposition `gorm:"type:varchar(16);not null" json:"charge_disposition"`
	ActualQuota               int64                       `gorm:"type:bigint;not null" json:"actual_quota"`
	ResultMetadataHash        string                      `gorm:"type:char(64);not null" json:"result_metadata_hash"`
	ResultMetadataJSON        string                      `gorm:"type:text;not null" json:"-"`
	UsageJSON                 string                      `gorm:"type:text;not null" json:"usage"`
	ChargeDimensionsJSON      string                      `gorm:"type:text;not null" json:"charge_dimensions"`
	FailureReason             string                      `gorm:"type:varchar(128);not null" json:"failure_reason"`
	SubmittedAt               *time.Time                  `json:"submitted_at,omitempty"`
	TerminalAt                *time.Time                  `json:"terminal_at,omitempty"`
	CreatedAt                 time.Time                   `json:"created_at"`
	UpdatedAt                 time.Time                   `gorm:"index" json:"updated_at"`
}

func (ZTAPIMediaTask) TableName() string { return "ztapi_media_tasks" }

func (*ZTAPIMediaTask) BeforeCreate(tx *gorm.DB) error {
	return rejectUnauthorizedZTAPIMediaTaskMutation(tx)
}

func (*ZTAPIMediaTask) BeforeUpdate(tx *gorm.DB) error {
	return rejectUnauthorizedZTAPIMediaTaskMutation(tx)
}

func (*ZTAPIMediaTask) BeforeDelete(tx *gorm.DB) error {
	return rejectUnauthorizedZTAPIMediaTaskMutation(tx)
}

type ZTAPIMediaTaskInput struct {
	PublicTaskID              string
	SettlementID              uint
	Selector                  ZTAPIMediaPriceSelector
	VideoSelector             types.ZTAPIVideoSelector
	QuotaPerUnit              string
	Action                    string
	VideoProtocolContractJSON string
}

type ZTAPIMediaTaskObservation struct {
	PublicTaskID         string
	State                ZTAPIMediaTaskState
	Attempt              int
	UpstreamTaskID       string
	ChargeDisposition    ZTAPIMediaChargeDisposition
	ActualQuota          int64
	UsageJSON            string
	ChargeDimensionsJSON string
	ResultMetadataJSON   string
	FailureReason        string
	SettlementEvidence   ZTAPISettlementEvidence
}

type ztapiMediaTaskPriceSnapshot struct {
	Version                uint64
	PublicName             string
	Modality               string
	PriceSourceID          int64
	PriceSourceVersion     uint64
	MediaPriceContractJSON string
}

func BeginZTAPIMediaTask(input ZTAPIMediaTaskInput) (*ZTAPIMediaTask, error) {
	quotaPerUnit, quotaErr := decimal.NewFromString(input.QuotaPerUnit)
	if !validZTAPIMediaTaskID(input.PublicTaskID) || input.SettlementID == 0 || input.Selector.Modality != ZTAPIModalityVideo ||
		quotaErr != nil || !quotaPerUnit.IsPositive() || quotaPerUnit.String() != input.QuotaPerUnit || input.Action != "generate" {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	protocol, canonicalProtocol, err := types.ParseZTAPIVideoProtocolContract(input.VideoProtocolContractJSON)
	if err != nil || canonicalProtocol != input.VideoProtocolContractJSON {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	if _, ok := protocol.FindReservationAuthority(input.VideoSelector); !ok {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	selectorJSON, err := common.Marshal(input.Selector)
	if err != nil || len(selectorJSON) > 8192 {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	videoSelectorJSON, err := common.Marshal(input.VideoSelector)
	if err != nil || len(videoSelectorJSON) > 8192 {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	selectorHash := ztapiMediaTaskHash(string(selectorJSON))
	videoSelectorHash := ztapiMediaTaskHash(string(videoSelectorJSON))
	var result *ZTAPIMediaTask
	err = ztapiMediaTaskTransaction(func(tx *gorm.DB) error {
		var settlement ZTAPIRequestSettlement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settlement, input.SettlementID).Error; err != nil {
			return err
		}
		if settlement.Status != ZTAPISettlementReserved {
			return ErrZTAPIMediaTaskConflict
		}
		var snapshot ztapiMediaTaskPriceSnapshot
		if err := common.UnmarshalJsonStr(settlement.PriceSnapshotJSON, &snapshot); err != nil || snapshot.Modality != ZTAPIModalityVideo || snapshot.PublicName != settlement.PublicModel {
			return ErrZTAPIMediaTaskInvalid
		}
		canonicalPrice, err := canonicalizeZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
		if err != nil || canonicalPrice != snapshot.MediaPriceContractJSON {
			return ErrZTAPIMediaTaskInvalid
		}
		if _, err = SelectZTAPIMediaPriceRule(snapshot.MediaPriceContractJSON, input.Selector); err != nil {
			return ErrZTAPIMediaTaskInvalid
		}
		price, err := types.ParseZTAPIMediaPriceContract(snapshot.MediaPriceContractJSON)
		if err != nil || !ztapiVideoSelectorMatchesPriceSelector(input.VideoSelector, input.Selector) {
			return ErrZTAPIMediaTaskInvalid
		}
		_, maximumQuota, err := types.CalculateZTAPIVideoMaximumReservation(price, protocol, input.VideoSelector, input.QuotaPerUnit)
		if err != nil || maximumQuota != settlement.ReservedQuota {
			return ErrZTAPIMediaTaskInvalid
		}
		candidate := ZTAPIMediaTask{
			PublicTaskID: input.PublicTaskID, SettlementID: settlement.ID, RequestID: settlement.RequestID,
			UserID: settlement.UserID, TokenID: settlement.TokenID, PublicModel: settlement.PublicModel,
			Action: input.Action, VideoProtocolContractJSON: canonicalProtocol, VideoProtocolContractHash: ztapiMediaTaskHash(canonicalProtocol),
			PriceSnapshotHash: ztapiMediaTaskHash(settlement.PriceSnapshotJSON), QuotaPerUnit: input.QuotaPerUnit, SelectorJSON: string(selectorJSON),
			SelectorHash: selectorHash, VideoSelectorJSON: string(videoSelectorJSON), VideoSelectorHash: videoSelectorHash,
			State: ZTAPIMediaTaskReserved, Version: 1,
			SettlementState: settlement.Status, UsageJSON: "{}", ChargeDimensionsJSON: "[]", ResultMetadataJSON: "{}",
		}
		var existing ZTAPIMediaTask
		e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("public_task_id = ? OR settlement_id = ?", input.PublicTaskID, input.SettlementID).Take(&existing).Error
		if e == nil {
			if !ztapiMediaTaskIdentityMatches(existing, candidate) {
				return ErrZTAPIMediaTaskConflict
			}
			result = &existing
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if err := authorizeZTAPIMediaTaskMutation(tx).Create(&candidate).Error; err != nil {
			return err
		}
		result = &candidate
		return nil
	})
	return result, err
}

func ztapiVideoSelectorMatchesPriceSelector(video types.ZTAPIVideoSelector, price ZTAPIMediaPriceSelector) bool {
	wantVideoInput := strconv.FormatBool(video.ContainsVideoInput)
	if price.Conditions["contains_video_input"] != wantVideoInput {
		return false
	}
	resolution, pricedByResolution := price.Conditions["resolution"]
	if pricedByResolution && resolution != video.Resolution {
		return false
	}
	return len(price.Conditions) == 1 || (len(price.Conditions) == 2 && pricedByResolution)
}

func MarkZTAPIMediaTaskSubmitted(publicTaskID, upstreamTaskID string, attemptIndex int) (*ZTAPIMediaTask, error) {
	if !validZTAPIMediaTaskID(publicTaskID) || !validZTAPIUpstreamTaskID(upstreamTaskID) || attemptIndex < 1 || attemptIndex > 2 {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	var result *ZTAPIMediaTask
	err := ztapiMediaTaskTransaction(func(tx *gorm.DB) error {
		task, err := lockZTAPIMediaTask(tx, publicTaskID)
		if err != nil {
			return err
		}
		if task.State != ZTAPIMediaTaskReserved {
			if task.Attempt == attemptIndex && task.UpstreamTaskID == upstreamTaskID {
				result = task
				return nil
			}
			return ErrZTAPIMediaTaskConflict
		}
		var accepted []ZTAPIRequestAttempt
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("settlement_id = ? AND http_status >= ? AND http_status < ?", task.SettlementID, 200, 300).
			Order("attempt ASC").Find(&accepted).Error; err != nil {
			return err
		}
		if len(accepted) != 1 || accepted[0].Attempt != attemptIndex {
			return ErrZTAPIMediaTaskConflict
		}
		attempt := accepted[0]
		now := time.Now()
		expectedVersion := task.Version
		err = updateZTAPIMediaTask(tx, task, expectedVersion, map[string]any{
			"attempt":            attempt.Attempt,
			"channel_id":         attempt.ChannelID,
			"credential_version": attempt.CredentialVersion,
			"upstream_task_id":   upstreamTaskID,
			"state":              ZTAPIMediaTaskSubmitted,
			"version":            expectedVersion + 1,
			"submitted_at":       &now,
		})
		if err != nil {
			return err
		}
		result = task
		return nil
	})
	return result, err
}

func ApplyZTAPIMediaTaskObservation(input ZTAPIMediaTaskObservation) (*ZTAPIMediaTask, error) {
	if !validZTAPIMediaTaskID(input.PublicTaskID) || input.Attempt < 1 || !validZTAPIUpstreamTaskID(input.UpstreamTaskID) {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	terminal := input.State == ZTAPIMediaTaskSucceeded || input.State == ZTAPIMediaTaskFailed || input.State == ZTAPIMediaTaskUnknown
	if input.State != ZTAPIMediaTaskProcessing && !terminal {
		return nil, ErrZTAPIMediaTaskConflict
	}
	if terminal {
		return applyZTAPIMediaTaskTerminalObservation(input)
	}
	var result *ZTAPIMediaTask
	err := ztapiMediaTaskTransaction(func(tx *gorm.DB) error {
		task, err := lockZTAPIMediaTask(tx, input.PublicTaskID)
		if err != nil {
			return err
		}
		if task.Attempt != input.Attempt || task.UpstreamTaskID != input.UpstreamTaskID {
			return ErrZTAPIMediaTaskConflict
		}
		if terminal {
			return ErrZTAPIMediaTaskFinancialPending
		}
		if task.State == ZTAPIMediaTaskProcessing {
			result = task
			return nil
		}
		if task.State != ZTAPIMediaTaskSubmitted {
			return ErrZTAPIMediaTaskConflict
		}
		expectedVersion := task.Version
		if err = updateZTAPIMediaTask(tx, task, expectedVersion, map[string]any{
			"state":   ZTAPIMediaTaskProcessing,
			"version": expectedVersion + 1,
		}); err != nil {
			return err
		}
		result = task
		return nil
	})
	return result, err
}

func applyZTAPIMediaTaskTerminalObservation(input ZTAPIMediaTaskObservation) (*ZTAPIMediaTask, error) {
	if !ztapiMediaTaskMissingFinancialEvidence(input) {
		if err := validateZTAPIMediaTerminalObservation(input); err != nil {
			return nil, err
		}
	}
	var result *ZTAPIMediaTask
	var err error
	for retry := 0; retry < 3; retry++ {
		result, err = applyZTAPIMediaTaskTerminalObservationOnce(input)
		if err == nil || !ztapiMediaTaskRetryable(err) {
			return result, err
		}
		time.Sleep(time.Duration(retry+1) * 10 * time.Millisecond)
	}
	return nil, err
}

func applyZTAPIMediaTaskTerminalObservationOnce(input ZTAPIMediaTaskObservation) (*ZTAPIMediaTask, error) {
	if DB == nil {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	var task ZTAPIMediaTask
	if err := DB.Where("public_task_id = ?", input.PublicTaskID).Take(&task).Error; err != nil {
		return nil, err
	}
	if task.Attempt != input.Attempt || task.UpstreamTaskID != input.UpstreamTaskID {
		return nil, ErrZTAPIMediaTaskConflict
	}
	if ztapiMediaTaskMissingFinancialEvidence(input) {
		return nil, ErrZTAPIMediaTaskFinancialPending
	}
	var settlement ZTAPIRequestSettlement
	if err := DB.First(&settlement, task.SettlementID).Error; err != nil {
		return nil, err
	}
	if task.PriceSnapshotHash != ztapiMediaTaskHash(settlement.PriceSnapshotJSON) {
		return nil, ErrZTAPIMediaTaskConflict
	}
	var attempt ZTAPIRequestAttempt
	if err := DB.Where("settlement_id = ? AND attempt = ?", task.SettlementID, task.Attempt).Take(&attempt).Error; err != nil {
		return nil, err
	}
	if attempt.ChannelID != task.ChannelID || attempt.CredentialVersion != task.CredentialVersion || attempt.HTTPStatus < 200 || attempt.HTTPStatus >= 300 {
		return nil, ErrZTAPIMediaTaskConflict
	}
	if input.ChargeDisposition == ZTAPIMediaChargeKnown {
		if err := validateZTAPIMediaTaskKnownCharge(task, settlement, input); err != nil {
			return nil, err
		}
	}
	var result *ZTAPIMediaTask
	lineage := ZTAPIPendingSettlementLineage{FinalAttempt: task.Attempt, ChannelID: task.ChannelID, UpstreamRequestID: attempt.UpstreamRequestID}
	precondition := func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		stored, err := lockZTAPIMediaTask(tx, input.PublicTaskID)
		if err != nil {
			return err
		}
		if stored.SettlementID != row.ID || stored.RequestID != row.RequestID || stored.UserID != row.UserID || stored.TokenID != row.TokenID ||
			stored.PublicModel != row.PublicModel || stored.Attempt != input.Attempt || stored.UpstreamTaskID != input.UpstreamTaskID ||
			stored.PriceSnapshotHash != ztapiMediaTaskHash(row.PriceSnapshotJSON) {
			return ErrZTAPIMediaTaskConflict
		}
		return nil
	}
	hook := func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		stored, err := applyZTAPIMediaTaskTerminalTx(tx, row, input)
		if err == nil {
			result = stored
		}
		return err
	}
	const mediaUnknownMissing = `["media_charge_unknown"]`
	switch input.ChargeDisposition {
	case ZTAPIMediaChargeKnown:
		evidence := input.SettlementEvidence
		evidence.UpstreamTaskID = input.UpstreamTaskID
		_, err := finalizeZTAPIRequestSettlementWithOptions(settlement.OperationID, input.ActualQuota, input.UsageJSON, input.ChargeDimensionsJSON, &evidence, false, ztapiSettlementFinalizeOptions{
			directEvidence: true, allowedPendingMissing: mediaUnknownMissing, precondition: precondition, hook: hook,
		})
		return result, err
	case ZTAPIMediaChargeNoCharge:
		_, err := releaseZTAPIDispatchedNoChargeWithLineageAndHook(settlement.OperationID, input.UsageJSON, input.ChargeDimensionsJSON, lineage, mediaUnknownMissing, precondition, hook)
		return result, err
	case ZTAPIMediaChargeUnknown:
		if settlement.Status == ZTAPISettlementPending && settlement.MissingDimensionsJSON == mediaUnknownMissing {
			_, err := refreshZTAPIMediaPendingObservation(settlement.OperationID, input.UsageJSON, mediaUnknownMissing, lineage, precondition, hook)
			return result, err
		}
		_, err := pendZTAPIRequestSettlementWithLineageAndHook(settlement.OperationID, input.UsageJSON, mediaUnknownMissing, lineage, precondition, hook)
		return result, err
	default:
		return nil, ErrZTAPIMediaTaskInvalid
	}
}

func refreshZTAPIMediaPendingObservation(op, usage, missing string, lineage ZTAPIPendingSettlementLineage, precondition, hook ztapiSettlementMutationHook) (*ZTAPIRequestSettlement, error) {
	if len(usage) > 65536 || len(missing) > 8192 || !validZTAPISettlementJSON(usage) || !validZTAPISettlementJSON(missing) {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	return mutateZTAPISettlementWithHooks(op, precondition, func(tx *gorm.DB, row *ZTAPIRequestSettlement) error {
		if row.Status != ZTAPISettlementPending || row.MissingDimensionsJSON != missing || row.FinalAttempt != lineage.FinalAttempt {
			return ErrZTAPIMediaTaskConflict
		}
		var attempt ZTAPIRequestAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("settlement_id = ? AND attempt = ?", row.ID, lineage.FinalAttempt).Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.ChannelID != lineage.ChannelID || attempt.HTTPStatus < 200 || attempt.HTTPStatus >= 300 ||
			(attempt.UpstreamRequestID != "" && lineage.UpstreamRequestID != "" && attempt.UpstreamRequestID != lineage.UpstreamRequestID) {
			return ErrZTAPIMediaTaskConflict
		}
		row.UsageJSON = usage
		return nil
	}, hook)
}

func validateZTAPIMediaTerminalObservation(input ZTAPIMediaTaskObservation) error {
	if len(input.UsageJSON) == 0 || len(input.UsageJSON) > 65536 || len(input.ChargeDimensionsJSON) == 0 || len(input.ChargeDimensionsJSON) > 65536 ||
		len(input.ResultMetadataJSON) == 0 || len(input.ResultMetadataJSON) > 65536 ||
		common.RejectDuplicateJsonObjectMembers(strings.NewReader(input.UsageJSON)) != nil ||
		common.RejectDuplicateJsonObjectMembers(strings.NewReader(input.ChargeDimensionsJSON)) != nil ||
		common.RejectDuplicateJsonObjectMembers(strings.NewReader(input.ResultMetadataJSON)) != nil {
		return ErrZTAPIMediaTaskInvalid
	}
	if input.FailureReason != strings.TrimSpace(input.FailureReason) || len(input.FailureReason) > 128 || strings.ContainsAny(input.FailureReason, "\r\n\x00") {
		return ErrZTAPIMediaTaskInvalid
	}
	if input.State == ZTAPIMediaTaskSucceeded {
		if input.ChargeDisposition != ZTAPIMediaChargeKnown && input.ChargeDisposition != ZTAPIMediaChargeUnknown || input.FailureReason != "" {
			return ErrZTAPIMediaTaskInvalid
		}
	} else if input.FailureReason == "" {
		return ErrZTAPIMediaTaskInvalid
	}
	if input.State == ZTAPIMediaTaskUnknown && input.ChargeDisposition != ZTAPIMediaChargeUnknown {
		return ErrZTAPIMediaTaskInvalid
	}
	if input.ChargeDisposition == ZTAPIMediaChargeKnown {
		if input.ActualQuota < 0 || input.ActualQuota > maxBalanceLedgerQuota || input.SettlementEvidence.FinalAttempt != input.Attempt {
			return ErrZTAPIMediaTaskInvalid
		}
	} else if input.ActualQuota != 0 || !reflect.DeepEqual(input.SettlementEvidence, ZTAPISettlementEvidence{}) {
		return ErrZTAPIMediaTaskInvalid
	}
	if input.ChargeDisposition == ZTAPIMediaChargeNoCharge && input.State != ZTAPIMediaTaskFailed {
		return ErrZTAPIMediaTaskInvalid
	}
	return nil
}

func validateZTAPIMediaTaskKnownCharge(task ZTAPIMediaTask, settlement ZTAPIRequestSettlement, input ZTAPIMediaTaskObservation) error {
	var snapshot ztapiMediaTaskPriceSnapshot
	var selector ZTAPIMediaPriceSelector
	var usage map[string]decimal.Decimal
	var dimensions []ZTAPISupplierRefundDimension
	if common.UnmarshalJsonStr(settlement.PriceSnapshotJSON, &snapshot) != nil ||
		common.UnmarshalJsonStr(task.SelectorJSON, &selector) != nil ||
		common.UnmarshalJsonStr(input.UsageJSON, &usage) != nil ||
		common.UnmarshalJsonStr(input.ChargeDimensionsJSON, &dimensions) != nil {
		return ErrZTAPIMediaTaskInvalid
	}
	rule, err := SelectZTAPIMediaPriceRule(snapshot.MediaPriceContractJSON, selector)
	if err != nil || rule.BillingUnit != "usd_per_million_tokens" || len(rule.SaleUSD) != 1 || len(dimensions) != 1 {
		return ErrZTAPIMediaTaskInvalid
	}
	quotaPerUnit, err := decimal.NewFromString(task.QuotaPerUnit)
	if err != nil || !quotaPerUnit.IsPositive() || quotaPerUnit.String() != task.QuotaPerUnit {
		return ErrZTAPIMediaTaskInvalid
	}
	for name, rawPrice := range rule.SaleUSD {
		if rawQuantity := gjson.Get(input.UsageJSON, name); rawQuantity.Type != gjson.Number {
			return ErrZTAPIMediaTaskInvalid
		}
		quantity, ok := usage[name]
		price, priceErr := decimal.NewFromString(rawPrice)
		if !ok || priceErr != nil || quantity.IsNegative() || !quantity.Equal(decimal.NewFromInt(quantity.IntPart())) || price.IsNegative() {
			return ErrZTAPIMediaTaskInvalid
		}
		unitQuota := price.Mul(quotaPerUnit).Div(decimal.NewFromInt(1_000_000))
		expected := quantity.Mul(unitQuota).Round(0).IntPart()
		dimension := dimensions[0]
		if expected != input.ActualQuota || dimension.Dimension != name || dimension.Units != quantity.String() ||
			dimension.UnitQuota != unitQuota.String() || dimension.ChargedQuota != expected || dimension.TokenChargedQuota != 0 {
			return ErrZTAPIMediaTaskInvalid
		}
	}
	return nil
}

func ztapiMediaTaskMissingFinancialEvidence(input ZTAPIMediaTaskObservation) bool {
	return input.ChargeDisposition == "" && input.ActualQuota == 0 && input.UsageJSON == "" && input.ChargeDimensionsJSON == "" &&
		input.ResultMetadataJSON == "" && input.FailureReason == "" && reflect.DeepEqual(input.SettlementEvidence, ZTAPISettlementEvidence{})
}

func applyZTAPIMediaTaskTerminalTx(tx *gorm.DB, settlement *ZTAPIRequestSettlement, input ZTAPIMediaTaskObservation) (*ZTAPIMediaTask, error) {
	task, err := lockZTAPIMediaTask(tx, input.PublicTaskID)
	if err != nil {
		return nil, err
	}
	if task.SettlementID != settlement.ID || task.RequestID != settlement.RequestID || task.UserID != settlement.UserID || task.TokenID != settlement.TokenID ||
		task.PublicModel != settlement.PublicModel || task.Attempt != input.Attempt || task.UpstreamTaskID != input.UpstreamTaskID {
		return nil, ErrZTAPIMediaTaskConflict
	}
	resultHash := ztapiMediaTaskHash(input.ResultMetadataJSON)
	if ztapiMediaTaskTerminalMatches(task, settlement, input, resultHash) {
		return task, nil
	}
	resolvingUnknown := task.ChargeDisposition == ZTAPIMediaChargeUnknown && input.ChargeDisposition != ZTAPIMediaChargeUnknown &&
		(task.State == ZTAPIMediaTaskUnknown || task.State == input.State)
	reobservingUnknown := task.State == ZTAPIMediaTaskUnknown && task.ChargeDisposition == ZTAPIMediaChargeUnknown &&
		input.ChargeDisposition == ZTAPIMediaChargeUnknown && settlement.Status == ZTAPISettlementPending
	if task.State != ZTAPIMediaTaskProcessing && !resolvingUnknown && !reobservingUnknown {
		return nil, ErrZTAPIMediaTaskConflict
	}
	expectedSettlementState := ztapiMediaTaskSettlementState(input.ChargeDisposition)
	if settlement.Status != expectedSettlementState {
		return nil, ErrZTAPIMediaTaskConflict
	}
	now := time.Now()
	terminalAt := task.TerminalAt
	if terminalAt == nil {
		terminalAt = &now
	}
	expectedVersion := task.Version
	if err = updateZTAPIMediaTask(tx, task, expectedVersion, map[string]any{
		"state": input.State, "settlement_state": settlement.Status, "charge_disposition": input.ChargeDisposition,
		"actual_quota": input.ActualQuota, "result_metadata_hash": resultHash, "result_metadata_json": input.ResultMetadataJSON,
		"usage_json": input.UsageJSON, "charge_dimensions_json": input.ChargeDimensionsJSON, "failure_reason": input.FailureReason,
		"terminal_at": terminalAt, "version": expectedVersion + 1,
	}); err != nil {
		return nil, err
	}
	switch settlement.Status {
	case ZTAPISettlementReleased:
		if err = EnsureZTAPIAttemptBillingReviewsTx(tx, settlement); err == nil {
			err = SyncZTAPIAttemptBillingReviewsTx(tx, settlement)
		}
	case ZTAPISettlementPending:
		err = EnsureZTAPIAttemptBillingReviewsTx(tx, settlement)
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

func ztapiMediaTaskTerminalMatches(task *ZTAPIMediaTask, settlement *ZTAPIRequestSettlement, input ZTAPIMediaTaskObservation, resultHash string) bool {
	return task.State == input.State && task.SettlementState == settlement.Status && task.ChargeDisposition == input.ChargeDisposition &&
		task.ActualQuota == input.ActualQuota && ztapiMediaTaskResultMatches(task, input.ResultMetadataJSON, resultHash) &&
		task.UsageJSON == input.UsageJSON && task.ChargeDimensionsJSON == input.ChargeDimensionsJSON && task.FailureReason == input.FailureReason
}

func ztapiMediaTaskResultMatches(task *ZTAPIMediaTask, metadata, hash string) bool {
	if task.ResultMetadataHash == hash && task.ResultMetadataJSON == metadata {
		return true
	}
	if task.ResultMetadataHash != ztapiMediaTaskHash(task.ResultMetadataJSON) {
		return false
	}
	var original, observed map[string]json.RawMessage
	if common.UnmarshalJsonStr(task.ResultMetadataJSON, &original) != nil || common.UnmarshalJsonStr(metadata, &observed) != nil {
		return false
	}
	// A new poll has a new request ID, not a new task outcome. Preserve the
	// first terminal evidence; every other result and financial field must match.
	for _, values := range []map[string]json.RawMessage{original, observed} {
		var requestID string
		if common.Unmarshal(values["upstream_request_id"], &requestID) != nil || requestID == "" || len(requestID) > 255 ||
			requestID != strings.TrimSpace(requestID) || strings.ContainsAny(requestID, "\r\n\x00") {
			return false
		}
		delete(values, "upstream_request_id")
	}
	return reflect.DeepEqual(original, observed)
}

func ztapiMediaTaskSettlementState(disposition ZTAPIMediaChargeDisposition) string {
	switch disposition {
	case ZTAPIMediaChargeKnown:
		return ZTAPISettlementSettled
	case ZTAPIMediaChargeNoCharge:
		return ZTAPISettlementReleased
	case ZTAPIMediaChargeUnknown:
		return ZTAPISettlementPending
	default:
		return ""
	}
}

func GetZTAPIMediaTask(publicTaskID string) (*ZTAPIMediaTask, error) {
	if DB == nil || !validZTAPIMediaTaskID(publicTaskID) {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	var task ZTAPIMediaTask
	if err := DB.Where("public_task_id = ?", publicTaskID).Take(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func ListZTAPIMediaLegacyTaskOrphans(limit int) ([]ZTAPIMediaTask, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrZTAPIMediaTaskInvalid
	}
	var tasks []ZTAPIMediaTask
	err := DB.Where("state IN ?", []ZTAPIMediaTaskState{ZTAPIMediaTaskSubmitted, ZTAPIMediaTaskProcessing, ZTAPIMediaTaskUnknown}).
		Where("upstream_task_id <> '' AND video_protocol_contract_json <> ''").
		Where("NOT EXISTS (SELECT 1 FROM tasks WHERE tasks.task_id = ztapi_media_tasks.public_task_id)").
		Order("id ASC").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func EnsureZTAPIMediaLegacyTask(publicTaskID string) (*Task, bool, error) {
	if !validZTAPIMediaTaskID(publicTaskID) {
		return nil, false, ErrZTAPIMediaTaskInvalid
	}
	var result *Task
	created := false
	err := ztapiMediaTaskTransaction(func(tx *gorm.DB) error {
		mediaTask, err := lockZTAPIMediaTask(tx, publicTaskID)
		if err != nil {
			return err
		}
		if mediaTask.State != ZTAPIMediaTaskSubmitted && mediaTask.State != ZTAPIMediaTaskProcessing && mediaTask.State != ZTAPIMediaTaskUnknown {
			return ErrZTAPIMediaTaskConflict
		}
		if mediaTask.ChannelID <= 0 || mediaTask.Attempt <= 0 || !validZTAPIUpstreamTaskID(mediaTask.UpstreamTaskID) || mediaTask.Action != "generate" {
			return ErrZTAPIMediaTaskInvalid
		}
		protocol, canonical, err := types.ParseZTAPIVideoProtocolContract(mediaTask.VideoProtocolContractJSON)
		if err != nil || canonical != mediaTask.VideoProtocolContractJSON || ztapiMediaTaskHash(canonical) != mediaTask.VideoProtocolContractHash {
			return ErrZTAPIMediaTaskInvalid
		}
		var settlement ZTAPIRequestSettlement
		if err := tx.First(&settlement, mediaTask.SettlementID).Error; err != nil {
			return err
		}
		if settlement.ID != mediaTask.SettlementID || settlement.RequestID != mediaTask.RequestID || settlement.UserID != mediaTask.UserID || settlement.TokenID != mediaTask.TokenID || settlement.PublicModel != mediaTask.PublicModel {
			return ErrZTAPIMediaTaskConflict
		}
		var existing Task
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ?", publicTaskID).Take(&existing).Error
		if err == nil {
			if !ztapiRecoveredLegacyTaskMatches(&existing, mediaTask, protocol.ProviderModel) {
				return ErrZTAPIMediaTaskConflict
			}
			result = &existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		status := TaskStatus(TaskStatusSubmitted)
		progress := "0%"
		if mediaTask.State == ZTAPIMediaTaskProcessing || mediaTask.State == ZTAPIMediaTaskUnknown {
			status = TaskStatusInProgress
			progress = "50%"
		}
		submitTime := mediaTask.CreatedAt.Unix()
		if mediaTask.SubmittedAt != nil {
			submitTime = mediaTask.SubmittedAt.Unix()
		}
		legacy := Task{
			TaskID: publicTaskID, UserId: mediaTask.UserID, SubmitTime: submitTime,
			Status: status, Progress: progress, ChannelId: mediaTask.ChannelID,
			Platform: constant.TaskPlatformZTAPIAIHubVideo, Action: mediaTask.Action,
			Quota: int(settlement.ReservedQuota), Data: json.RawMessage(`{}`),
			Properties: Properties{OriginModelName: mediaTask.PublicModel, UpstreamModelName: protocol.ProviderModel},
			PrivateData: TaskPrivateData{
				UpstreamTaskID: mediaTask.UpstreamTaskID, ZTAPIMediaManaged: true,
				ZTAPIMediaSettlementID: mediaTask.SettlementID, TokenId: mediaTask.TokenID,
			},
		}
		if err := tx.Create(&legacy).Error; err != nil {
			return err
		}
		result = &legacy
		created = true
		return nil
	})
	return result, created, err
}

func ztapiRecoveredLegacyTaskMatches(task *Task, mediaTask *ZTAPIMediaTask, providerModel string) bool {
	return task != nil && mediaTask != nil && task.TaskID == mediaTask.PublicTaskID && task.UserId == mediaTask.UserID &&
		task.ChannelId == mediaTask.ChannelID && task.Platform == constant.TaskPlatformZTAPIAIHubVideo && task.Action == mediaTask.Action &&
		task.Properties.OriginModelName == mediaTask.PublicModel && task.Properties.UpstreamModelName == providerModel &&
		task.PrivateData.UpstreamTaskID == mediaTask.UpstreamTaskID && task.PrivateData.ZTAPIMediaManaged &&
		task.PrivateData.ZTAPIMediaSettlementID == mediaTask.SettlementID && task.PrivateData.TokenId == mediaTask.TokenID
}

func lockZTAPIMediaTask(tx *gorm.DB, publicTaskID string) (*ZTAPIMediaTask, error) {
	var task ZTAPIMediaTask
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("public_task_id = ?", publicTaskID).Take(&task).Error
	return &task, err
}

func updateZTAPIMediaTask(tx *gorm.DB, task *ZTAPIMediaTask, expectedVersion uint64, updates map[string]any) error {
	result := authorizeZTAPIMediaTaskMutation(tx).Model(&ZTAPIMediaTask{}).
		Where("id = ? AND version = ?", task.ID, expectedVersion).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrZTAPIMediaTaskConflict
	}
	return tx.First(task, task.ID).Error
}

func authorizeZTAPIMediaTaskMutation(tx *gorm.DB) *gorm.DB {
	return tx.Set(ztapiMediaTaskMutationScope, true)
}

func rejectUnauthorizedZTAPIMediaTaskMutation(tx *gorm.DB) error {
	if ztapiMediaTaskMutationAuthorized(tx) {
		return nil
	}
	return ErrZTAPIMediaTaskMutationForbidden
}

func registerZTAPIMediaTaskMutationGuard(db *gorm.DB) error {
	if db == nil {
		return errors.New("media task mutation guard requires a database")
	}
	if callbacks := db.Callback().Create(); callbacks.Get(ztapiMediaTaskCreateGuard) == nil {
		if err := callbacks.Before("gorm:create").Register(ztapiMediaTaskCreateGuard, guardZTAPIMediaTaskMutation); err != nil {
			return err
		}
	}
	if callbacks := db.Callback().Update(); callbacks.Get(ztapiMediaTaskUpdateGuard) == nil {
		if err := callbacks.Before("gorm:update").Register(ztapiMediaTaskUpdateGuard, guardZTAPIMediaTaskMutation); err != nil {
			return err
		}
	}
	if callbacks := db.Callback().Delete(); callbacks.Get(ztapiMediaTaskDeleteGuard) == nil {
		if err := callbacks.Before("gorm:delete").Register(ztapiMediaTaskDeleteGuard, guardZTAPIMediaTaskMutation); err != nil {
			return err
		}
	}
	return nil
}

func guardZTAPIMediaTaskMutation(tx *gorm.DB) {
	if ztapiMediaTaskMutationAuthorized(tx) || !ztapiMediaTaskMutationTargetsTable(tx) {
		return
	}
	_ = tx.AddError(ErrZTAPIMediaTaskMutationForbidden)
}

func ztapiMediaTaskMutationAuthorized(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	allowed, ok := tx.Statement.Settings.Load(ztapiMediaTaskMutationScope)
	return ok && allowed == true
}

func ztapiMediaTaskMutationTargetsTable(tx *gorm.DB) bool {
	if tx == nil || tx.Statement == nil {
		return false
	}
	target := (ZTAPIMediaTask{}).TableName()
	if tx.Statement.TableExpr != nil {
		return ztapiMediaTaskTableExpressionTargets(tx.Statement.TableExpr, target, make(map[ztapiMediaTaskExpressionVisit]struct{}))
	}
	if ztapiMediaTaskTableNameMatches(tx.Statement.Table, target) {
		return true
	}
	if tx.Statement.Schema != nil && ztapiMediaTaskTableNameMatches(tx.Statement.Schema.Table, target) {
		return true
	}
	return false
}

func ztapiMediaTaskTableNameMatches(expression, target string) bool {
	position := skipBalanceLedgerSQLTrivia(expression, 0)
	name, next, ok := parseBalanceLedgerSQLIdentifier(expression, position)
	if !ok {
		return false
	}
	if strings.EqualFold(name, "only") {
		position = skipBalanceLedgerSQLTrivia(expression, next)
		name, next, ok = parseBalanceLedgerSQLIdentifier(expression, position)
		if !ok {
			return false
		}
	}
	position = skipBalanceLedgerSQLTrivia(expression, next)
	if position < len(expression) && expression[position] == '.' {
		position = skipBalanceLedgerSQLTrivia(expression, position+1)
		qualified, _, qualifiedOK := parseBalanceLedgerSQLIdentifier(expression, position)
		if !qualifiedOK {
			return false
		}
		name = qualified
	}
	return strings.EqualFold(name, target)
}

type ztapiMediaTaskExpressionVisit struct {
	kind    reflect.Kind
	typeOf  reflect.Type
	pointer uintptr
	length  int
}

func ztapiMediaTaskTableExpressionTargets(value any, target string, visited map[ztapiMediaTaskExpressionVisit]struct{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return ztapiMediaTaskTableNameMatches(typed, target)
	case clause.Table:
		return ztapiMediaTaskTableNameMatches(typed.Name, target)
	case *clause.Table:
		if typed == nil || ztapiMediaTaskExpressionVisited(reflect.ValueOf(typed), visited) {
			return false
		}
		return ztapiMediaTaskTableNameMatches(typed.Name, target)
	case clause.Expr:
		return ztapiMediaTaskTableNameMatches(typed.SQL, target) || ztapiMediaTaskTableExpressionTargets(typed.Vars, target, visited)
	case *clause.Expr:
		if typed == nil || ztapiMediaTaskExpressionVisited(reflect.ValueOf(typed), visited) {
			return false
		}
		return ztapiMediaTaskTableNameMatches(typed.SQL, target) || ztapiMediaTaskTableExpressionTargets(typed.Vars, target, visited)
	}

	valueOf := reflect.ValueOf(value)
	for valueOf.Kind() == reflect.Interface {
		if valueOf.IsNil() {
			return false
		}
		valueOf = valueOf.Elem()
	}
	switch valueOf.Kind() {
	case reflect.Pointer:
		if valueOf.IsNil() || ztapiMediaTaskExpressionVisited(valueOf, visited) || !valueOf.Elem().CanInterface() {
			return false
		}
		return ztapiMediaTaskTableExpressionTargets(valueOf.Elem().Interface(), target, visited)
	case reflect.Slice:
		if valueOf.IsNil() || ztapiMediaTaskExpressionVisited(valueOf, visited) {
			return false
		}
		fallthrough
	case reflect.Array:
		for index := 0; index < valueOf.Len(); index++ {
			if valueOf.Index(index).CanInterface() && ztapiMediaTaskTableExpressionTargets(valueOf.Index(index).Interface(), target, visited) {
				return true
			}
		}
	}
	return false
}

func ztapiMediaTaskExpressionVisited(value reflect.Value, visited map[ztapiMediaTaskExpressionVisit]struct{}) bool {
	visit := ztapiMediaTaskExpressionVisit{kind: value.Kind(), typeOf: value.Type(), pointer: value.Pointer()}
	if value.Kind() == reflect.Slice {
		visit.length = value.Len()
	}
	if _, ok := visited[visit]; ok {
		return true
	}
	visited[visit] = struct{}{}
	return false
}

func ztapiMediaTaskTransaction(fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = ztapiSettlementTransaction(fn)
		if err == nil || !ztapiMediaTaskRetryable(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}

func ztapiMediaTaskRetryable(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"deadlock", "database is locked", "serialization failure", "could not serialize access", "sqlstate 40001", "try restarting transaction"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func ztapiMediaTaskIdentityMatches(existing, candidate ZTAPIMediaTask) bool {
	return existing.PublicTaskID == candidate.PublicTaskID && existing.SettlementID == candidate.SettlementID &&
		existing.RequestID == candidate.RequestID && existing.UserID == candidate.UserID && existing.TokenID == candidate.TokenID &&
		existing.PublicModel == candidate.PublicModel && existing.PriceSnapshotHash == candidate.PriceSnapshotHash &&
		existing.Action == candidate.Action && existing.VideoProtocolContractJSON == candidate.VideoProtocolContractJSON &&
		existing.VideoProtocolContractHash == candidate.VideoProtocolContractHash && existing.QuotaPerUnit == candidate.QuotaPerUnit &&
		existing.SelectorJSON == candidate.SelectorJSON && existing.SelectorHash == candidate.SelectorHash &&
		existing.VideoSelectorJSON == candidate.VideoSelectorJSON && existing.VideoSelectorHash == candidate.VideoSelectorHash
}

func ztapiMediaTaskHash(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func validZTAPIMediaTaskID(value string) bool {
	return value == strings.TrimSpace(value) && ztapiPublicMediaTaskIDPattern.MatchString(value)
}

func validZTAPIUpstreamTaskID(value string) bool {
	return value != "" && len(value) <= 200 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}
