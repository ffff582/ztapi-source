package model

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ztapiMediaTaskCoreFixture struct {
	db         *gorm.DB
	settlement ZTAPIRequestSettlement
	task       ZTAPIMediaTask
	channel    Channel
	selector   ZTAPIMediaPriceSelector
	video      types.ZTAPIVideoSelector
}

func setupZTAPIMediaTaskCore(t *testing.T) ztapiMediaTaskCoreFixture {
	t.Helper()
	db, input := setupZTAPISettlement(t)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Task{}, &ZTAPIRequestAttempt{}, &ZTAPIMediaTask{}))
	require.NoError(t, registerZTAPIMediaTaskMutationGuard(db))
	selector := ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"contains_video_input": "false", "resolution": "720p"}}
	videoSelector := types.ZTAPIVideoSelector{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false}
	priceContract, err := canonicalizeZTAPIMediaPriceContract(seedanceContractForTest(t))
	require.NoError(t, err)
	snapshot, err := common.Marshal(struct {
		Version                uint64
		PublicName             string
		Modality               string
		PriceSourceID          int64
		PriceSourceVersion     uint64
		MediaPriceContractJSON string
	}{1, "zt-video", ZTAPIModalityVideo, 9, 3, priceContract})
	require.NoError(t, err)
	input.PublicModel = "zt-video"
	input.PriceSnapshotJSON = string(snapshot)
	input.OperationID = "media-op-" + common.GetUUID()
	input.RequestID = "media-request-" + common.GetUUID()
	protocol := ztapiVideoProtocolForMediaTaskTest(t)
	parsedProtocol, _, err := types.ParseZTAPIVideoProtocolContract(protocol)
	require.NoError(t, err)
	parsedPrice, err := types.ParseZTAPIMediaPriceContract(priceContract)
	require.NoError(t, err)
	_, maximumQuota, err := types.CalculateZTAPIVideoMaximumReservation(parsedPrice, parsedProtocol, types.ZTAPIVideoSelector{
		Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false,
	}, "500000")
	require.NoError(t, err)
	require.NoError(t, db.Model(&User{}).Where("id = ?", input.UserID).Update("quota", 100000).Error)
	require.NoError(t, db.Model(&Token{}).Where("id = ?", input.TokenID).Update("remain_quota", 100000).Error)
	input.ReservedQuota = maximumQuota
	settlement, err := BeginZTAPIRequestSettlement(input)
	require.NoError(t, err)
	channel := Channel{Id: 71, Name: "media-core", Type: constant.ChannelTypeOpenAI}
	require.NoError(t, db.Create(&channel).Error)
	task, err := BeginZTAPIMediaTask(ZTAPIMediaTaskInput{
		PublicTaskID: "task_" + common.GetUUID(), SettlementID: settlement.ID, Selector: selector, QuotaPerUnit: "500000",
		VideoSelector: videoSelector, Action: "generate", VideoProtocolContractJSON: protocol,
	})
	require.NoError(t, err)
	return ztapiMediaTaskCoreFixture{db: db, settlement: *settlement, task: *task, channel: channel, selector: selector, video: videoSelector}
}

func TestZTAPIMediaTaskBeginRejectsReservationNotDerivedFromFrozenContracts(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	settlement := f.settlement
	settlement.ID = 0
	settlement.OperationID = "wrong-reservation-op-" + common.GetUUID()
	settlement.RequestID = "wrong-reservation-request-" + common.GetUUID()
	settlement.ReservedQuota = f.settlement.ReservedQuota - 1
	created, err := BeginZTAPIRequestSettlement(settlement)
	require.NoError(t, err)

	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{
		PublicTaskID: "task_" + common.GetUUID(), SettlementID: created.ID, Selector: f.selector,
		VideoSelector: f.video, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON,
	})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
}

func (f ztapiMediaTaskCoreFixture) acceptedAttempt(t *testing.T) *ZTAPIRequestAttempt {
	t.Helper()
	attempt, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, f.channel.Id, "credential-v1", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, attempt.Attempt, attempt.ChannelID, 202, "submit-request-1"))
	return attempt
}

func TestZTAPIMediaTaskBeginFreezesIdentityAndReplaysExactly(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	replay, err := BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: f.task.PublicTaskID, SettlementID: f.settlement.ID, Selector: ZTAPIMediaPriceSelector{Modality: ZTAPIModalityVideo, Conditions: map[string]string{"resolution": "720p", "contains_video_input": "false"}}, VideoSelector: f.video, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON})
	require.NoError(t, err)
	require.Equal(t, f.task.ID, replay.ID)
	require.Equal(t, uint64(1), replay.Version)
	require.Equal(t, ZTAPIMediaTaskReserved, replay.State)
	require.Len(t, replay.PriceSnapshotHash, 64)
	require.Len(t, replay.SelectorHash, 64)
	require.Equal(t, f.settlement.RequestID, replay.RequestID)
	require.Equal(t, f.settlement.UserID, replay.UserID)
	require.Equal(t, f.settlement.TokenID, replay.TokenID)
	require.Equal(t, f.settlement.PublicModel, replay.PublicModel)
	require.Equal(t, "generate", replay.Action)
	require.Equal(t, f.task.VideoProtocolContractJSON, replay.VideoProtocolContractJSON)
	require.Contains(t, replay.VideoSelectorJSON, `"duration_seconds":5`)
	require.Len(t, replay.VideoProtocolContractHash, 64)

	changed := f.selector
	changed.Conditions = map[string]string{"contains_video_input": "false", "resolution": "1080p"}
	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: f.task.PublicTaskID, SettlementID: f.settlement.ID, Selector: changed, VideoSelector: f.video, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: f.task.PublicTaskID, SettlementID: f.settlement.ID, Selector: f.selector, VideoSelector: f.video, QuotaPerUnit: "500001", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: "task_other", SettlementID: f.settlement.ID, Selector: f.selector, VideoSelector: f.video, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)

	var settlements, ledgers int64
	require.NoError(t, f.db.Model(&ZTAPIRequestSettlement{}).Count(&settlements).Error)
	require.NoError(t, f.db.Model(&BalanceLedger{}).Where("request_id = ?", f.settlement.RequestID).Count(&ledgers).Error)
	require.EqualValues(t, 1, settlements)
	require.EqualValues(t, 1, ledgers)
}

func TestZTAPIMediaTaskSubmissionBindsAcceptedAttemptAndRejectsSwitch(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	attempt := f.acceptedAttempt(t)
	submitted, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskSubmitted, submitted.State)
	require.Equal(t, attempt.Attempt, submitted.Attempt)
	require.Equal(t, attempt.ChannelID, submitted.ChannelID)
	require.Equal(t, attempt.CredentialVersion, submitted.CredentialVersion)
	require.Equal(t, "upstream-task-1", submitted.UpstreamTaskID)
	require.Equal(t, uint64(2), submitted.Version)

	replay, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)
	require.Equal(t, submitted.Version, replay.Version)
	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-other", attempt.Attempt)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
}

func TestZTAPIMediaTaskSubmissionBindsOnlyLatestAcceptedAttempt(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	first, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, f.channel.Id, "credential-v1", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, first.Attempt, first.ChannelID, 503, "submit-request-1"))
	secondChannel := Channel{Id: 72, Name: "media-core-fallback"}
	require.NoError(t, f.db.Create(&secondChannel).Error)
	second, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, secondChannel.Id, "credential-v2", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, second.Attempt, second.ChannelID, 202, "submit-request-2"))

	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "stale-upstream-task", first.Attempt)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	submitted, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "accepted-upstream-task", second.Attempt)
	require.NoError(t, err)
	require.Equal(t, second.Attempt, submitted.Attempt)
	require.Equal(t, second.ChannelID, submitted.ChannelID)
	require.Equal(t, second.CredentialVersion, submitted.CredentialVersion)
}

func TestZTAPIMediaTaskSubmissionRejectsTwoAcceptedProviders(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	first := f.acceptedAttempt(t)
	secondChannel := Channel{Id: 72, Name: "media-core-second-success"}
	require.NoError(t, f.db.Create(&secondChannel).Error)
	second, err := BeginZTAPIRequestAttempt(f.settlement.OperationID, secondChannel.Id, "credential-v2", "synthetic-video")
	require.NoError(t, err)
	require.NoError(t, RecordZTAPIRequestAttemptResponse(f.settlement.OperationID, second.Attempt, second.ChannelID, 202, "submit-request-2"))

	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", first.Attempt)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-2", second.Attempt)
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskReserved, stored.State)
	require.Zero(t, stored.Attempt)
}

func TestZTAPIMediaTaskProcessingIsMonotonicAndTerminalFailsClosed(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	attempt := f.acceptedAttempt(t)
	_, err := ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1"})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	_, err = MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)

	processing, err := ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1"})
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, processing.State)
	require.Equal(t, uint64(3), processing.Version)
	replay, err := ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1"})
	require.NoError(t, err)
	require.Equal(t, processing.Version, replay.Version)
	submissionReplay, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)
	require.Equal(t, processing.Version, submissionReplay.Version)
	beginReplay, err := BeginZTAPIMediaTask(ZTAPIMediaTaskInput{PublicTaskID: f.task.PublicTaskID, SettlementID: f.settlement.ID, Selector: f.selector, VideoSelector: f.video, QuotaPerUnit: "500000", Action: "generate", VideoProtocolContractJSON: f.task.VideoProtocolContractJSON})
	require.NoError(t, err)
	require.Equal(t, processing.Version, beginReplay.Version)

	for _, terminal := range []ZTAPIMediaTaskState{ZTAPIMediaTaskSucceeded, ZTAPIMediaTaskFailed, ZTAPIMediaTaskUnknown} {
		_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: terminal, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1"})
		require.ErrorIs(t, err, ErrZTAPIMediaTaskFinancialPending)
	}
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, stored.State)
	_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-other"})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	_, err = ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskSucceeded, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-other"})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
}

func TestZTAPIMediaTaskRejectsChangedOrUnsealedVideoProtocol(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	changed, _, err := types.ParseZTAPIVideoProtocolContract(f.task.VideoProtocolContractJSON)
	require.NoError(t, err)
	changed.ProviderModel = "provider-video-other"
	_, changedJSON, err := types.SealZTAPIVideoProtocolContract(changed)
	require.NoError(t, err)

	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{
		PublicTaskID: f.task.PublicTaskID, SettlementID: f.settlement.ID, Selector: f.selector, QuotaPerUnit: f.task.QuotaPerUnit,
		VideoSelector: f.video, Action: "generate", VideoProtocolContractJSON: changedJSON,
	})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)

	tampered := f.task.VideoProtocolContractJSON[:len(f.task.VideoProtocolContractJSON)-1] + `,"tampered":true}`
	_, err = BeginZTAPIMediaTask(ZTAPIMediaTaskInput{
		PublicTaskID: "task_" + common.GetUUID(), SettlementID: f.settlement.ID, Selector: f.selector, QuotaPerUnit: f.task.QuotaPerUnit,
		VideoSelector: f.video, Action: "generate", VideoProtocolContractJSON: tampered,
	})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskInvalid)
}

func TestZTAPIMediaTaskRecoveryRecreatesLegacyPollRecordWithoutResubmission(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	attempt := f.acceptedAttempt(t)
	submitted, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-recover", attempt.Attempt)
	require.NoError(t, err)

	legacy, created, err := EnsureZTAPIMediaLegacyTask(submitted.PublicTaskID)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, submitted.PublicTaskID, legacy.TaskID)
	require.Equal(t, "upstream-task-recover", legacy.PrivateData.UpstreamTaskID)
	require.True(t, legacy.PrivateData.ZTAPIMediaManaged)
	require.Equal(t, submitted.SettlementID, legacy.PrivateData.ZTAPIMediaSettlementID)
	require.Equal(t, submitted.TokenID, legacy.PrivateData.TokenId)
	require.Equal(t, submitted.ChannelID, legacy.ChannelId)
	require.Equal(t, constant.TaskPlatform(constant.TaskPlatformZTAPIAIHubVideo), legacy.Platform)
	require.Equal(t, "generate", legacy.Action)
	require.Equal(t, submitted.PublicModel, legacy.Properties.OriginModelName)
	require.Equal(t, "provider-video-exact", legacy.Properties.UpstreamModelName)
	require.EqualValues(t, f.settlement.ReservedQuota, legacy.Quota)
	require.JSONEq(t, `{}`, string(legacy.Data))

	replay, created, err := EnsureZTAPIMediaLegacyTask(submitted.PublicTaskID)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, legacy.ID, replay.ID)
	var count int64
	require.NoError(t, f.db.Model(&Task{}).Where("task_id = ?", submitted.PublicTaskID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestZTAPIMediaTaskRecoveryListsOnlyPollableOrphans(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	attempt := f.acceptedAttempt(t)
	_, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-recover", attempt.Attempt)
	require.NoError(t, err)

	orphans, err := ListZTAPIMediaLegacyTaskOrphans(10)
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	require.Equal(t, f.task.PublicTaskID, orphans[0].PublicTaskID)

	_, _, err = EnsureZTAPIMediaLegacyTask(f.task.PublicTaskID)
	require.NoError(t, err)
	orphans, err = ListZTAPIMediaLegacyTaskOrphans(10)
	require.NoError(t, err)
	require.Empty(t, orphans)
}

func ztapiVideoProtocolForMediaTaskTest(t *testing.T) string {
	t.Helper()
	contract := types.ZTAPIVideoProtocolContract{
		Version: types.ZTAPIVideoProtocolContractVersion, Provider: "aihub", ProviderModel: "provider-video-exact",
		Auth:            types.ZTAPIVideoAuthContract{Method: "header", Header: "Authorization", Scheme: "Bearer"},
		Create:          types.ZTAPIVideoEndpointContract{Method: "POST", Path: "/hub/v1/video/tasks", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Fetch:           types.ZTAPIVideoEndpointContract{Method: "GET", Path: "/hub/v1/video/tasks/{task_id}", TaskIDField: "data.task_id", RequestIDField: "request_id"},
		Callback:        types.ZTAPIVideoCallbackContract{Enabled: false},
		Request:         types.ZTAPIVideoRequestContract{ModelField: "model", PromptField: "prompt", ResolutionField: "resolution", DurationField: "duration"},
		Capabilities:    types.ZTAPIVideoCapabilities{Resolutions: []string{"720p"}, DurationSeconds: []int{5}, SupportsVideoInput: false},
		States:          types.ZTAPIVideoStateContract{Field: "data.status", Accepted: []string{"queued"}, Processing: []string{"processing"}, Succeeded: []string{"completed"}, Failed: []string{"failed"}},
		Result:          types.ZTAPIVideoResultContract{URLField: "data.result.url", ResolutionField: "data.result.resolution", DurationField: "data.result.duration", FailureReasonField: "data.error.message"},
		Usage:           types.ZTAPIVideoUsageContract{Fields: map[string]string{"input_tokens": "data.usage.input_tokens"}},
		Reservations:    []types.ZTAPIVideoReservationAuthority{{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"input_tokens": "9000"}}},
		EvidenceVersion: types.ZTAPIVideoEvidenceVersion,
	}
	_, canonical, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	return canonical
}

func TestZTAPIMediaTaskRejectsDirectMutationAndDelete(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)

	err := f.db.Model(&ZTAPIMediaTask{}).Where("id = ?", f.task.ID).Update("public_model", "tampered-model").Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Model(&ZTAPIMediaTask{}).Where("id = ?", f.task.ID).Update("state", ZTAPIMediaTaskSucceeded).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Delete(&ZTAPIMediaTask{}, f.task.ID).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Session(&gorm.Session{SkipHooks: true}).Model(&ZTAPIMediaTask{}).Where("id = ?", f.task.ID).Update("state", ZTAPIMediaTaskSucceeded).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Table((ZTAPIMediaTask{}).TableName()).Where("id = ?", f.task.ID).UpdateColumn("public_model", "table-tamper").Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Session(&gorm.Session{SkipHooks: true}).Table("? AS media_task", clause.Table{Name: (ZTAPIMediaTask{}).TableName()}).
		Where("media_task.id = ?", f.task.ID).UpdateColumn("state", ZTAPIMediaTaskSucceeded).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Session(&gorm.Session{SkipHooks: true}).Table("/* bypass */ "+(ZTAPIMediaTask{}).TableName()).
		Where("id = ?", f.task.ID).UpdateColumn("state", ZTAPIMediaTaskSucceeded).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	err = f.db.Session(&gorm.Session{SkipHooks: true}).Delete(&ZTAPIMediaTask{}, f.task.ID).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)
	unauthorizedCreate := f.task
	unauthorizedCreate.ID = 0
	unauthorizedCreate.PublicTaskID = "task_unauthorized"
	unauthorizedCreate.SettlementID++
	unauthorizedCreate.RequestID = "request_unauthorized"
	err = f.db.Session(&gorm.Session{SkipHooks: true}).Create(&unauthorizedCreate).Error
	require.ErrorIs(t, err, ErrZTAPIMediaTaskMutationForbidden)

	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, f.task.PublicModel, stored.PublicModel)
	require.Equal(t, ZTAPIMediaTaskReserved, stored.State)
}

func TestZTAPIMediaTaskRejectsStaleVersionUpdate(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	err := ztapiSettlementTransaction(func(tx *gorm.DB) error {
		stored, err := lockZTAPIMediaTask(tx, f.task.PublicTaskID)
		if err != nil {
			return err
		}
		return updateZTAPIMediaTask(tx, stored, stored.Version+1, map[string]any{
			"state":   ZTAPIMediaTaskProcessing,
			"version": stored.Version + 2,
		})
	})
	require.ErrorIs(t, err, ErrZTAPIMediaTaskConflict)
	stored, err := GetZTAPIMediaTask(f.task.PublicTaskID)
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskReserved, stored.State)
	require.Equal(t, uint64(1), stored.Version)
}

func TestZTAPIMediaTaskRetryableRecognizesPostgresSerialization(t *testing.T) {
	require.True(t, ztapiMediaTaskRetryable(errors.New("could not serialize access due to concurrent update (SQLSTATE 40001)")))
	require.False(t, ztapiMediaTaskRetryable(errors.New("invalid input syntax for type bigint (SQLSTATE 22P02)")))
}

func TestZTAPIMediaTaskTableExpressionGuardHandlesRecursiveVariables(t *testing.T) {
	recursive := &clause.Expr{SQL: "?"}
	recursive.Vars = []any{recursive}
	require.False(t, ztapiMediaTaskTableExpressionTargets(recursive, (ZTAPIMediaTask{}).TableName(), make(map[ztapiMediaTaskExpressionVisit]struct{})))
	target := (ZTAPIMediaTask{}).TableName()
	for _, test := range []struct {
		expression string
		want       bool
	}{
		{target, true},
		{"`" + target + "`", true},
		{"/* guarded */ " + target, true},
		{"main." + target, true},
		{"`main`.`" + target + "`", true},
		{"ONLY " + target, true},
		{"channels AS " + target, false},
		{"'" + target + "' channels", false},
		{"/* unterminated " + target, false},
	} {
		require.Equal(t, test.want, ztapiMediaTaskTableNameMatches(test.expression, target), test.expression)
	}
	require.False(t, ztapiMediaTaskTableExpressionTargets(clause.Table{Name: "channels", Alias: target}, target, make(map[ztapiMediaTaskExpressionVisit]struct{})))
}

func TestZTAPIMediaTaskRetriesOneTransientUpdateWithoutDuplicateTransition(t *testing.T) {
	f := setupZTAPIMediaTaskCore(t)
	attempt := f.acceptedAttempt(t)
	_, err := MarkZTAPIMediaTaskSubmitted(f.task.PublicTaskID, "upstream-task-1", attempt.Attempt)
	require.NoError(t, err)
	var calls atomic.Int32
	const callback = "ztapi-media-task:retry-once"
	require.NoError(t, f.db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (ZTAPIMediaTask{}).TableName() && calls.Add(1) == 1 {
			_ = tx.AddError(errors.New("deadlock found when trying to get lock; try restarting transaction"))
		}
	}))
	t.Cleanup(func() { _ = f.db.Callback().Update().Remove(callback) })
	got, err := ApplyZTAPIMediaTaskObservation(ZTAPIMediaTaskObservation{PublicTaskID: f.task.PublicTaskID, State: ZTAPIMediaTaskProcessing, Attempt: attempt.Attempt, UpstreamTaskID: "upstream-task-1"})
	require.NoError(t, err)
	require.Equal(t, ZTAPIMediaTaskProcessing, got.State)
	require.Equal(t, uint64(3), got.Version)
	require.GreaterOrEqual(t, calls.Load(), int32(2))
	var count int64
	require.NoError(t, f.db.Model(&ZTAPIMediaTask{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
