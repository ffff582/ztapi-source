package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type ztapiMediaTerminalFixture struct {
	ztapiMediaTaskCoreFixture
	attempt ZTAPIRequestAttempt
	held    int
}

func setupZTAPIMediaTerminal(t *testing.T) ztapiMediaTerminalFixture {
	t.Helper()
	f := setupZTAPIMediaTaskCore(t)
	require.NoError(t, f.db.AutoMigrate(&ZTAPISupplierRefundCharge{}, &ZTAPIPendingResolution{}, &ZTAPISettlementLogOutbox{}, &ZTAPIFinanceAlertOutbox{}, &Log{}))
	require.NoError(t, MarkZTAPIRequestDispatched(f.settlement.OperationID))
	attempt := f.acceptedAttempt(t)
	_, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)
	_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{
		PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing,
		Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1",
	})
	require.NoError(t, err)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	return ztapiMediaTerminalFixture{ztapiMediaTaskCoreFixture: f, attempt: *attempt, held: user.Quota}
}

func (f ztapiMediaTerminalFixture) knownObservation(t *testing.T, state ZTAPIMediaTaskState) ZTAPIMediaTaskObservation {
	t.Helper()
	dimensions, err := common.Marshal([]ZTAPISupplierRefundDimension{{
		Dimension: "input_tokens", Units: "6", UnitQuota: "3.90720486115", ChargedQuota: 23,
	}})
	require.NoError(t, err)
	return ZTAPIMediaTaskObservation{
		PublicTaskID: f.task.PublicTaskID, State: state, Attempt: f.attempt.Attempt,
		UpstreamTaskID: "upstream-task-1", ChargeDisposition: ZTAPIMediaChargeKnown,
		ActualQuota: 23, UsageJSON: `{"input_tokens":6,"total_tokens":6}`,
		ChargeDimensionsJSON: string(dimensions),
		ResultMetadataJSON:   `{"resolution":"720p","url":"https://example.invalid/result.mp4"}`,
		SettlementEvidence: ZTAPISettlementEvidence{FinalAttempt: f.attempt.Attempt, ConsumeLog: Log{
			Type: LogTypeConsume, UserId: f.settlement.UserID, TokenId: f.settlement.TokenID,
			RequestId: f.settlement.RequestID, ModelName: f.settlement.PublicModel, Quota: 23,
			ChannelId: f.attempt.ChannelID, CreatedAt: 1, UpstreamRequestId: f.attempt.UpstreamRequestID,
		}},
	}
}

func (f ztapiMediaTerminalFixture) unchargedObservation(state ZTAPIMediaTaskState, disposition ZTAPIMediaChargeDisposition) ZTAPIMediaTaskObservation {
	return ZTAPIMediaTaskObservation{
		PublicTaskID: f.task.PublicTaskID, State: state, Attempt: f.attempt.Attempt,
		UpstreamTaskID: "upstream-task-1", ChargeDisposition: disposition,
		UsageJSON: `{"provider_state":"failed"}`, ChargeDimensionsJSON: "[]",
		ResultMetadataJSON: `{}`, FailureReason: "provider_failed",
	}
}

func TestZTAPIMediaTaskKnownSuccessSettlesAndReplaysExactly(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	completed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskSucceeded, completed.State)
	require.Equal(t, ZTAPIMediaChargeKnown, completed.ChargeDisposition)
	require.EqualValues(t, 23, completed.ActualQuota)
	require.Equal(t, ZTAPISettlementSettled, completed.SettlementState)
	require.NotEmpty(t, completed.ResultMetadataHash)
	require.Equal(t, observation.ResultMetadataJSON, completed.ResultMetadataJSON)

	replayed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, completed.Version, replayed.Version)

	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementSettled, settlement.Status)
	require.EqualValues(t, 23, settlement.ChargedQuota)
	require.Equal(t, f.attempt.Attempt, settlement.FinalAttempt)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	require.Equal(t, f.held+int(f.settlement.ReservedQuota-23), user.Quota)
	var charges []ZTAPISupplierRefundCharge
	require.NoError(t, f.db.Where("request_id = ?", f.settlement.RequestID).Find(&charges).Error)
	require.Len(t, charges, 1)
	require.Equal(t, "upstream-task-1", charges[0].UpstreamTaskID)

	conflict := observation
	conflict.ResultMetadataJSON = `{"resolution":"720p","url":"https://example.invalid/other.mp4"}`
	_, err = ApplyZTAPIMediaTaskObservation(conflict)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
}

func TestZTAPIMediaTaskKnownChargeRejectsAmountOutsideFrozenPrice(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	observation.ActualQuota++
	observation.SettlementEvidence.ConsumeLog.Quota++
	dimensions, marshalErr := common.Marshal([]ZTAPISupplierRefundDimension{{
		Dimension: "input_tokens", Units: "6", UnitQuota: "4", ChargedQuota: 24,
	}})
	require.NoError(t, marshalErr)
	observation.ChargeDimensionsJSON = string(dimensions)

	_, err := ApplyZTAPIMediaTaskObservation(observation)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, settlement.Status)
}

func TestZTAPIMediaTaskKnownChargeRejectsStringEncodedUsage(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	observation.UsageJSON = `{"input_tokens":"6","total_tokens":6}`

	_, err := ApplyZTAPIMediaTaskObservation(observation)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
}

func TestZTAPIMediaTaskExplicitNoChargeReleasesReservation(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.unchargedObservation(ZTAPIMediaTaskFailed, ZTAPIMediaChargeNoCharge)
	completed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskFailed, completed.State)
	require.Equal(t, ZTAPISettlementReleased, completed.SettlementState)
	replayed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, completed.Version, replayed.Version)

	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReleased, settlement.Status)
	require.Zero(t, settlement.ChargedQuota)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	require.Equal(t, f.held+int(f.settlement.ReservedQuota), user.Quota)
	var review ZTAPIAttemptBillingReview
	require.NoError(t, f.db.Where("request_id = ? AND attempt = ?", f.settlement.RequestID, f.attempt.Attempt).Take(&review).Error)
	require.Equal(t, "verified_nocharge", review.Status)
	require.Empty(t, review.PendingReason)
}

func TestZTAPIMediaTaskNoChargeClosesOnlyTheProvenAttempt(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	require.NoError(t, f.db.AutoMigrate(&ZTAPISupplierRefundCharge{}, &ZTAPIPendingResolution{}, &ZTAPISettlementLogOutbox{}, &ZTAPIFinanceAlertOutbox{}, &Log{}))
	require.NoError(t, MarkZTAPIRequestDispatched(f.settlement.OperationID))
	first, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, f.channel.Id, "credential-v1", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, first.Attempt, first.ChannelID, 502, "submit-request-1"))
	secondChannel := Channel{Id: 72, Name: "media-terminal-fallback"}
	require.NoError(t, f.db.Create(&secondChannel).Error)
	second, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, secondChannel.Id, "credential-v2", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, second.Attempt, second.ChannelID, 202, "submit-request-2"))
	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-2", second.Attempt)
	require.NoError(t, err)
	_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{
		PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing,
		Attempt: second.Attempt, UpstreamTaskID: "upstream-task-2",
	})
	require.NoError(t, err)

	_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{
		PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskFailed,
		Attempt: second.Attempt, UpstreamTaskID: "upstream-task-2",
		ChargeDisposition: ZTAPIMediaChargeNoCharge, UsageJSON: `{"provider_state":"failed"}`,
		ChargeDimensionsJSON: "[]", ResultMetadataJSON: `{}`, FailureReason: "provider_failed",
	})
	require.NoError(t, err)

	var reviews []ZTAPIAttemptBillingReview
	require.NoError(t, f.db.Where("request_id = ?", f.settlement.RequestID).Order("attempt").Find(&reviews).Error)
	require.Len(t, reviews, 2)
	require.Equal(t, first.Attempt, reviews[0].Attempt)
	require.Equal(t, "pending", reviews[0].Status)
	require.Equal(t, "upstream_attempt_billing_unconfirmed", reviews[0].PendingReason)
	require.Equal(t, second.Attempt, reviews[1].Attempt)
	require.Equal(t, "verified_nocharge", reviews[1].Status)
	require.Empty(t, reviews[1].PendingReason)
}

func TestZTAPIMediaTaskUnknownChargeStaysPending(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.unchargedObservation(ZTAPIMediaTaskFailed, ZTAPIMediaChargeUnknown)
	completed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskFailed, completed.State)
	require.Equal(t, ZTAPISettlementPending, completed.SettlementState)
	replayed, err := ApplyZTAPIMediaTaskObservation(observation)
	require.NoError(t, err)
	require.Equal(t, completed.Version, replayed.Version)

	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementPending, settlement.Status)
	require.Equal(t, `["media_charge_unknown"]`, settlement.MissingDimensionsJSON)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	require.Equal(t, f.held, user.Quota)
	var review ZTAPIAttemptBillingReview
	require.NoError(t, f.db.Where("request_id = ? AND attempt = ?", f.settlement.RequestID, f.attempt.Attempt).Take(&review).Error)
	require.Equal(t, "pending", review.Status)
	require.Equal(t, "upstream_attempt_billing_unconfirmed", review.PendingReason)
}

func TestZTAPIMediaTaskUnknownTimeoutCanLaterSettleSuccess(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	unknown := f.unchargedObservation(ZTAPIMediaTaskUnknown, ZTAPIMediaChargeUnknown)
	first, err := ApplyZTAPIMediaTaskObservation(unknown)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskUnknown, first.State)
	require.Equal(t, ZTAPISettlementPending, first.SettlementState)

	completed, err := ApplyZTAPIMediaTaskObservation(f.knownObservation(t, ZTAPIMediaTaskSucceeded))
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskSucceeded, completed.State)
	require.Equal(t, ZTAPISettlementSettled, completed.SettlementState)
}

func TestZTAPIMediaTaskConcurrentTerminalObservationsHaveOneFinancialOutcome(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	success := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	failure := f.unchargedObservation(ZTAPIMediaTaskFailed, ZTAPIMediaChargeNoCharge)
	inputs := []ZTAPIMediaTaskObservation{success, success, success, failure, failure}
	errs := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func(observation ZTAPIMediaTaskObservation) {
			defer wg.Done()
			_, err := ApplyZTAPIMediaTaskObservation(observation)
			errs <- err
		}(input)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.True(t, err == nil || errors.Is(err, ErrZTAPIMediaTaskConflict) || errors.Is(err, ErrZTAPISettlementConflict) || errors.Is(err, ErrZTAPISettlementPending), err)
	}

	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Contains(t, []ZTAPIMediaTaskState{ZTAPIMediaTaskSucceeded, ZTAPIMediaTaskFailed}, stored.State)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	if stored.State == ZTAPIMediaTaskSucceeded {
		require.Equal(t, ZTAPISettlementSettled, settlement.Status)
	} else {
		require.Equal(t, ZTAPISettlementReleased, settlement.Status)
	}
	var ledgers int64
	require.NoError(t, f.db.Model(&BalanceLedger{}).Where("request_id = ?", f.settlement.RequestID).Count(&ledgers).Error)
	require.EqualValues(t, 2, ledgers)
}

func TestZTAPIMediaTaskTerminalTaskWriteFailureRollsBackFinance(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	const callback = "ztapi-media-task:terminal-rollback"
	injected := errors.New("injected media task terminal write failure")
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (ZTAPIMediaTask{}).TableName() {
			_ = tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })

	_, err := ApplyZTAPIMediaTaskObservation(f.knownObservation(t, ZTAPIMediaTaskSucceeded))
	require.ErrorIs(t, err, injected)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, stored.State)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, settlement.Status)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	require.Equal(t, f.held, user.Quota)
	for _, model := range []any{&ZTAPISupplierRefundCharge{}, &ZTAPISettlementLogOutbox{}} {
		var count int64
		require.NoError(t, f.db.Model(model).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestZTAPIMediaTaskRejectsAmbiguousTerminalJSONWithoutChangingFinance(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	observation.UsageJSON = `{"input_tokens":6,"input_tokens":7}`
	_, err := ApplyZTAPIMediaTaskObservation(observation)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, stored.State)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, settlement.Status)
}

func TestZTAPIMediaTaskRejectsUncanonicalSettlementEvidence(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	observation := f.knownObservation(t, ZTAPIMediaTaskSucceeded)
	observation.SettlementEvidence.ConsumeLog.CreatedAt = 0
	_, err := ApplyZTAPIMediaTaskObservation(observation)
	require.ErrorIs(t, err, ErrZTAPISettlementConflict)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, stored.State)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, settlement.Status)
}

func TestZTAPIMediaTaskRejectsChangedFrozenPriceSnapshot(t *testing.T) {
	f := setupZTAPIMediaTerminal(t)
	require.NoError(t, f.db.Model(&ZTAPIRequestSettlement{}).Where("id = ?", f.settlement.ID).
		Update("price_snapshot_json", `{"tampered":true}`).Error)

	_, err := ApplyZTAPIMediaTaskObservation(f.knownObservation(t, ZTAPIMediaTaskSucceeded))
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, stored.State)
	var settlement ZTAPIRequestSettlement
	require.NoError(t, f.db.First(&settlement, f.settlement.ID).Error)
	require.Equal(t, ZTAPISettlementReserved, settlement.Status)
	var user User
	require.NoError(t, f.db.First(&user, f.settlement.UserID).Error)
	require.Equal(t, f.held, user.Quota)
}
