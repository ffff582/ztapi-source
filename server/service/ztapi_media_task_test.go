package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type ztapiMediaServiceFixture struct {
	info       *relaycommon.RelayInfo
	settlement model.ZTAPIRequestSettlement
	attempt    model.ZTAPIRequestAttempt
	legacy     model.Task
	user       model.User
	token      model.Token
}

func setupZTAPIMediaServiceFixture(t *testing.T) ztapiMediaServiceFixture {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "media-service.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(
		&model.Task{},
		&model.User{},
		&model.Token{},
		&model.BalanceLedger{},
		&model.Log{},
		&model.Channel{},
		&model.ZTAPIRequestSettlement{},
		&model.ZTAPISettlementFinalizationIntent{},
		&model.ZTAPIRequestAttempt{},
		&model.ZTAPIMediaTask{},
		&model.ZTAPIPendingResolution{},
		&model.ZTAPIAttemptBillingReview{},
		&model.ZTAPIAttemptBillingProof{},
		&model.ZTAPIAttemptBillingApproval{},
		&model.ZTAPISupplierRefundCharge{},
		&model.ZTAPISettlementLogOutbox{},
		&model.ZTAPIFinanceAlertOutbox{},
	))

	suffix := common.GetUUID()
	user := model.User{Username: "media-service-" + suffix, AffCode: suffix, Quota: 10000, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&user).Error)
	token := model.Token{UserId: user.Id, Name: "media-service-token", KeyHash: "media-service-" + suffix, Status: common.TokenStatusEnabled, RemainQuota: 10000, ExpiredTime: -1}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Id: 701, Name: "media-service-channel", Type: 1, Status: common.ChannelStatusEnabled}
	require.NoError(t, model.DB.Create(&channel).Error)

	priceContract, err := types.CanonicalizeZTAPIMediaPriceContract(`{
		"version":1,
		"modality":"video",
		"rules":[
			{"id":"without_video_input","conditions":{"contains_video_input":"false"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1"},"sale_usd":{"input_tokens":"1.6666666667"},"source_cells":{"input_tokens":"F5"}},
			{"id":"with_video_input","conditions":{"contains_video_input":"true"},"billing_unit":"usd_per_million_tokens","cost_usd":{"input_tokens":"1.5"},"sale_usd":{"input_tokens":"2.5"},"source_cells":{"input_tokens":"G5"}}
		]
	}`)
	require.NoError(t, err)
	snapshot := &relaycommon.ZTAPIPublicationSnapshot{
		PublicationID: 77, Version: 3, PublicName: "Seedance 2.0 Fast", Modality: model.ZTAPIModalityVideo,
		PriceSourceID: 91, PriceSourceVersion: 4, MediaPriceContractJSON: priceContract,
	}
	protocol := ztapiVideoProtocolForServiceTest(t)
	snapshot.VideoProtocolContract = &protocol
	snapshotJSON, err := common.Marshal(snapshot)
	require.NoError(t, err)
	settlement := model.ZTAPIRequestSettlement{
		OperationID: "media-op-" + suffix, RequestID: "media-request-" + suffix,
		UserID: user.Id, TokenID: token.Id, PublicModel: snapshot.PublicName,
		PriceSnapshotJSON: string(snapshotJSON), Status: model.ZTAPISettlementReserved,
		ReservedQuota: 7500, InitialReservedQuota: 7500, TokenReservedQuota: 7500,
		UsageJSON: "{}", ChargeDimensionsJSON: "[]", MissingDimensionsJSON: "[]",
	}
	createdSettlement, err := model.BeginZTAPIRequestSettlement(settlement)
	require.NoError(t, err)
	settlement = *createdSettlement
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	attempt, err := model.BeginZTAPIRequestAttempt(settlement.OperationID, 701, "credential-v1", "/hub/v1/videos")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(settlement.OperationID, attempt.Attempt, attempt.ChannelID, 202, "submit-request-1"))

	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: token.Id, RequestId: settlement.RequestID,
		OriginModelName: snapshot.PublicName, ZTAPIPublicationSnapshot: snapshot,
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelId: attempt.ChannelID, UpstreamModelName: protocol.ProviderModel},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_" + suffix},
	}
	billing := &ztapiDurableBilling{row: &settlement, info: info, attempt: attempt}
	info.Billing = billing

	managed, err := BindZTAPIMediaTaskSubmission(info, relaycommon.TaskSubmitReq{Model: snapshot.PublicName, Size: "720p", Duration: 5}, "upstream-task-1")
	require.NoError(t, err)
	require.Equal(t, model.ZTAPIMediaTaskSubmitted, managed.State)

	legacy := *model.InitTask("video", info)
	legacy.UserId = user.Id
	legacy.ChannelId = attempt.ChannelID
	legacy.Quota = 7500
	legacy.PrivateData.UpstreamTaskID = "upstream-task-1"
	legacy.PrivateData.TokenId = token.Id
	legacy.PrivateData.BillingContext = &model.TaskBillingContext{OriginModelName: snapshot.PublicName}
	require.NoError(t, TagZTAPIMediaLegacyTask(&legacy, info))

	return ztapiMediaServiceFixture{info: info, settlement: settlement, attempt: *attempt, legacy: legacy, user: user, token: token}
}

func (f ztapiMediaServiceFixture) reload(t *testing.T) (model.ZTAPIMediaTask, model.ZTAPIRequestSettlement, model.User, model.Token) {
	t.Helper()
	var media model.ZTAPIMediaTask
	var settlement model.ZTAPIRequestSettlement
	var user model.User
	var token model.Token
	require.NoError(t, model.DB.Where("public_task_id = ?", f.legacy.TaskID).Take(&media).Error)
	require.NoError(t, model.DB.First(&settlement, f.settlement.ID).Error)
	require.NoError(t, model.DB.First(&user, f.user.Id).Error)
	require.NoError(t, model.DB.First(&token, f.token.Id).Error)
	return media, settlement, user, token
}

func TestZTAPIMediaTaskSubmissionBindsDurableTaskWithoutEarlySettlement(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	media, settlement, _, _ := f.reload(t)
	require.True(t, IsZTAPIMediaBilling(f.info))
	require.True(t, f.legacy.PrivateData.ZTAPIMediaManaged)
	require.Equal(t, settlement.ID, f.legacy.PrivateData.ZTAPIMediaSettlementID)
	require.Equal(t, f.attempt.Attempt, media.Attempt)
	require.Equal(t, "upstream-task-1", media.UpstreamTaskID)
	require.Equal(t, model.ZTAPISettlementReserved, settlement.Status)
}

func TestZTAPIVideoMaximumReservationUsesFrozenProtocolInsteadOfLegacyEstimate(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.info.PriceData.Quota = 1
	quota, err := ZTAPIVideoMaximumReservation(f.info, relaycommon.TaskSubmitReq{Size: "720p", Duration: 5})
	require.NoError(t, err)
	require.Equal(t, 7500, quota)
}

func TestZTAPIMediaTaskPreparationPersistsReservedIdentityBeforeProviderAttempt(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	requestID := "prepare-" + common.GetUUID()
	publicTaskID := "task_" + common.GetUUID()
	settlement := f.settlement
	settlement.ID = 0
	settlement.OperationID = "prepare-op-" + common.GetUUID()
	settlement.RequestID = requestID
	require.NoError(t, model.DB.Create(&settlement).Error)
	info := &relaycommon.RelayInfo{
		UserId: f.info.UserId, TokenId: f.info.TokenId, RequestId: requestID,
		OriginModelName: f.info.OriginModelName, ZTAPIPublicationSnapshot: f.info.ZTAPIPublicationSnapshot.Clone(),
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelId: 703, UpstreamModelName: f.info.UpstreamModelName},
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: publicTaskID},
	}
	info.Billing = &ztapiDurableBilling{row: &settlement, info: info}

	prepared, err := PrepareZTAPIMediaTaskSubmission(info, relaycommon.TaskSubmitReq{Size: "720p", Duration: 5})
	require.NoError(t, err)
	require.Equal(t, model.ZTAPIMediaTaskReserved, prepared.State)
	require.Zero(t, prepared.Attempt)
	require.Empty(t, prepared.UpstreamTaskID)
	require.Equal(t, "generate", prepared.Action)
	_, canonicalProtocol, err := types.SealZTAPIVideoProtocolContract(*info.ZTAPIPublicationSnapshot.VideoProtocolContract)
	require.NoError(t, err)
	require.Equal(t, canonicalProtocol, prepared.VideoProtocolContractJSON)

	attempt, err := model.BeginZTAPIRequestAttempt(settlement.OperationID, 703, "credential-v3", "/hub/v1/videos")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(settlement.OperationID, attempt.Attempt, attempt.ChannelID, 202, "submit-request-3"))
	info.Billing.(*ztapiDurableBilling).attempt = attempt
	bound, err := BindZTAPIMediaTaskSubmission(info, relaycommon.TaskSubmitReq{Size: "720p", Duration: 5}, "upstream-task-3")
	require.NoError(t, err)
	require.Equal(t, prepared.ID, bound.ID)
	require.Equal(t, model.ZTAPIMediaTaskSubmitted, bound.State)
}

func TestZTAPIMediaTaskPreparationRejectsMissingFrozenVideoProtocol(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	requestID := "missing-protocol-" + common.GetUUID()
	settlement := f.settlement
	settlement.ID = 0
	settlement.OperationID = "missing-protocol-op-" + common.GetUUID()
	settlement.RequestID = requestID
	require.NoError(t, model.DB.Create(&settlement).Error)
	info := &relaycommon.RelayInfo{
		UserId: f.info.UserId, TokenId: f.info.TokenId, RequestId: requestID,
		OriginModelName: f.info.OriginModelName, ZTAPIPublicationSnapshot: f.info.ZTAPIPublicationSnapshot.Clone(),
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_" + common.GetUUID()},
	}
	info.ZTAPIPublicationSnapshot.VideoProtocolContract = nil
	info.Billing = &ztapiDurableBilling{row: &settlement, info: info}

	_, err := PrepareZTAPIMediaTaskSubmission(info, relaycommon.TaskSubmitReq{Size: "720p", Duration: 5})
	require.Error(t, err)
}

func TestZTAPIMediaTaskRecoveryWorkerRestoresAcceptedOrphan(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	var before int64
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", f.legacy.TaskID).Count(&before).Error)
	require.Zero(t, before)

	recovered, err := RecoverZTAPIMediaLegacyTasks(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	stored, exists, err := model.GetByOnlyTaskId(f.legacy.TaskID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "upstream-task-1", stored.GetUpstreamTaskID())
	require.True(t, IsZTAPIMediaLegacyTask(stored))
	require.Equal(t, constant.TaskPlatform(constant.TaskPlatformZTAPIAIHubVideo), stored.Platform)

	recovered, err = RecoverZTAPIMediaLegacyTasks(context.Background(), 100)
	require.NoError(t, err)
	require.Zero(t, recovered)
}

func TestZTAPIMediaTaskPollTerminalsWithoutBillingEvidenceStayPending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status model.TaskStatus
		want   model.ZTAPIMediaTaskState
	}{
		{name: "success", status: model.TaskStatusSuccess, want: model.ZTAPIMediaTaskSucceeded},
		{name: "failure", status: model.TaskStatusFailure, want: model.ZTAPIMediaTaskFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setupZTAPIMediaServiceFixture(t)
			result := &relaycommon.TaskInfo{
				Status: string(tc.status), ProviderStatus: strings.ToLower(string(tc.status)),
				UpstreamRequestID: "provider-fetch-request-no-usage", UpstreamTaskID: "upstream-task-1",
				Reason: "untrusted provider detail", Url: "https://provider.invalid/private-result",
			}
			if tc.status == model.TaskStatusSuccess {
				result.ProviderStatus = "completed"
				result.ResultMetadata = map[string]string{"resolution": "720p", "duration": "5"}
			}
			managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, result)
			require.NoError(t, err)
			require.True(t, managed)
			media, settlement, user, token := f.reload(t)
			require.Equal(t, tc.want, media.State)
			require.Equal(t, model.ZTAPIMediaChargeUnknown, media.ChargeDisposition)
			require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
			require.Equal(t, f.user.Quota, user.Quota)
			require.Equal(t, f.token.RemainQuota, token.RemainQuota)
			require.NotContains(t, media.ResultMetadataJSON, "provider.invalid")
			require.NotContains(t, media.FailureReason, "untrusted")
		})
	}
}

func TestZTAPIMediaTaskUnknownProviderStateStaysPendingWithExactSafeEvidence(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusUnknown),
		ProviderStatus:    "provider_new_state",
		UpstreamRequestID: "provider-request-unknown-1",
		UpstreamTaskID:    "upstream-task-1",
		Reason:            "untrusted provider detail sk-secret",
	})
	require.NoError(t, err)
	require.True(t, managed)

	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskUnknown, media.State)
	require.Equal(t, model.ZTAPIMediaChargeUnknown, media.ChargeDisposition)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.JSONEq(t, `{}`, media.UsageJSON)
	require.JSONEq(t, `{"provider_status":"provider_new_state","upstream_request_id":"provider-request-unknown-1"}`, media.ResultMetadataJSON)
	require.Equal(t, "provider_state_unknown", media.FailureReason)
	require.NotContains(t, media.ResultMetadataJSON, "sk-secret")
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskRejectsSuccessWithoutUsableResult(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{Status: string(model.TaskStatusSuccess)})
	require.Error(t, err)
	require.True(t, managed)
	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSubmitted, media.State)
	require.Equal(t, model.ZTAPISettlementReserved, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskSuccessSettlesExactFrozenUsageAndCustomerBalance(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusSuccess),
		ProviderStatus:    "completed",
		UpstreamRequestID: "provider-fetch-request-1",
		UpstreamTaskID:    "upstream-task-1",
		Url:               "https://provider.invalid/result.mp4",
		UsageDimensions:   map[string]string{"input_tokens": "6"},
		ResultMetadata:    map[string]string{"resolution": "720p", "duration": "5"},
	})
	require.NoError(t, err)
	require.True(t, managed)

	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSucceeded, media.State)
	require.Equal(t, model.ZTAPIMediaChargeKnown, media.ChargeDisposition)
	require.EqualValues(t, 5, media.ActualQuota)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status)
	require.EqualValues(t, 5, settlement.ChargedQuota)
	require.Equal(t, 9995, user.Quota)
	require.Equal(t, 9995, token.RemainQuota)
	require.JSONEq(t, `{"input_tokens":6}`, media.UsageJSON)
	require.Contains(t, media.ChargeDimensionsJSON, `"dimension":"input_tokens"`)
	require.Contains(t, media.ResultMetadataJSON, `"upstream_request_id":"provider-fetch-request-1"`)
	require.NotContains(t, media.ResultMetadataJSON, "provider.invalid")
}

func TestZTAPIMediaTaskSuccessWithoutUsageStaysPendingAndDoesNotRefund(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusSuccess),
		ProviderStatus:    "completed",
		UpstreamRequestID: "provider-fetch-request-no-usage",
		UpstreamTaskID:    "upstream-task-1",
		Url:               "https://provider.invalid/result.mp4",
		ResultMetadata:    map[string]string{"resolution": "720p", "duration": "5"},
	})
	require.NoError(t, err)
	require.True(t, managed)

	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSucceeded, media.State)
	require.Equal(t, model.ZTAPIMediaChargeUnknown, media.ChargeDisposition)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
	require.JSONEq(t, `{}`, media.UsageJSON)
}

func TestZTAPIMediaTaskFailureWithBillableUsageStillChargesCustomer(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusFailure),
		ProviderStatus:    "failed",
		UpstreamRequestID: "provider-fetch-request-failed",
		UpstreamTaskID:    "upstream-task-1",
		UsageDimensions:   map[string]string{"input_tokens": "6"},
		Reason:            "untrusted provider detail",
	})
	require.NoError(t, err)
	require.True(t, managed)

	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskFailed, media.State)
	require.Equal(t, model.ZTAPIMediaChargeKnown, media.ChargeDisposition)
	require.EqualValues(t, 5, media.ActualQuota)
	require.Equal(t, "provider_failed", media.FailureReason)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status)
	require.EqualValues(t, 5, settlement.ChargedQuota)
	require.Equal(t, 9995, user.Quota)
	require.Equal(t, 9995, token.RemainQuota)
	require.NotContains(t, media.FailureReason, "untrusted")
}

func TestZTAPIMediaTaskUsageBeyondFrozenAuthorityStaysPending(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusFailure),
		ProviderStatus:    "failed",
		UpstreamRequestID: "provider-fetch-request-over-limit",
		UpstreamTaskID:    "upstream-task-1",
		UsageDimensions:   map[string]string{"input_tokens": "9001"},
	})
	require.NoError(t, err)
	require.True(t, managed)

	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskFailed, media.State)
	require.Equal(t, model.ZTAPIMediaChargeUnknown, media.ChargeDisposition)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskCorruptFrozenContractFailsHardInsteadOfHidingAsUnknownUsage(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	require.NoError(t, model.DB.Exec(
		"UPDATE ztapi_media_tasks SET video_protocol_contract_json = ? WHERE public_task_id = ?",
		`{"version":999}`, f.legacy.TaskID,
	).Error)

	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status:            string(model.TaskStatusSuccess),
		ProviderStatus:    "completed",
		UpstreamRequestID: "provider-fetch-request-corrupt",
		UpstreamTaskID:    "upstream-task-1",
		Url:               "https://provider.invalid/result.mp4",
		ResultMetadata:    map[string]string{"resolution": "720p", "duration": "5"},
	})
	require.ErrorIs(t, err, model.ErrZTAPIMediaTaskInvalid)
	require.True(t, managed)

	_, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPISettlementReserved, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskTerminalPollingReplayIsIdempotent(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	result := &relaycommon.TaskInfo{
		Status: string(model.TaskStatusFailure), ProviderStatus: "failed",
		UpstreamRequestID: "provider-fetch-request-replay", UpstreamTaskID: "upstream-task-1",
		Reason: "untrusted provider detail",
	}

	managed, err := ApplyZTAPIMediaPollingObservation(&f.legacy, result)
	require.NoError(t, err)
	require.True(t, managed)
	first, settlement, user, token := f.reload(t)

	managed, err = ApplyZTAPIMediaPollingObservation(&f.legacy, result)
	require.NoError(t, err)
	require.True(t, managed)
	second, replayedSettlement, replayedUser, replayedToken := f.reload(t)
	require.Equal(t, first.Version, second.Version)
	require.Equal(t, settlement.Status, replayedSettlement.Status)
	require.Equal(t, user.Quota, replayedUser.Quota)
	require.Equal(t, token.RemainQuota, replayedToken.RemainQuota)
}

func TestZTAPIMediaTaskTimeoutStaysPendingAndDoesNotRefund(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaTimeout(&f.legacy, "worker timeout with untrusted upstream text")
	require.NoError(t, err)
	require.True(t, managed)
	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskUnknown, media.State)
	require.Equal(t, model.ZTAPIMediaChargeUnknown, media.ChargeDisposition)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
	require.NotContains(t, media.FailureReason, "worker timeout")
}

func TestZTAPIMediaTaskTimeoutRemainsPollableForLaterRecovery(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.legacy.Status = model.TaskStatusInProgress
	f.legacy.Progress = "50%"
	f.legacy.SubmitTime = time.Now().Add(-2 * time.Minute).Unix()
	require.NoError(t, f.legacy.Insert())
	previousTimeout := constant.TaskTimeoutMinutes
	constant.TaskTimeoutMinutes = 1
	t.Cleanup(func() { constant.TaskTimeoutMinutes = previousTimeout })

	sweepTimedOutTasks(context.Background())

	var legacy model.Task
	require.NoError(t, model.DB.Where("task_id = ?", f.legacy.TaskID).Take(&legacy).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusInProgress), legacy.Status)
	require.NotEqual(t, "100%", legacy.Progress)
	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskUnknown, media.State)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskTimeoutCanLaterResolveToSuccess(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	managed, err := ApplyZTAPIMediaTimeout(&f.legacy, "timeout")
	require.NoError(t, err)
	require.True(t, managed)

	managed, err = ApplyZTAPIMediaPollingObservation(&f.legacy, &relaycommon.TaskInfo{
		Status: string(model.TaskStatusSuccess), ProviderStatus: "completed",
		UpstreamRequestID: "provider-fetch-request-after-timeout", UpstreamTaskID: "upstream-task-1",
		Url: "https://provider.invalid/result.mp4", ResultMetadata: map[string]string{"resolution": "720p", "duration": "5"},
	})
	require.NoError(t, err)
	require.True(t, managed)
	media, settlement, _, _ := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSucceeded, media.State)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
}

func TestZTAPIMediaTaskChannelLookupFailureStaysPollable(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	require.NoError(t, model.DB.Delete(&model.Channel{}, f.legacy.ChannelId).Error)
	f.legacy.Status = model.TaskStatusSubmitted
	f.legacy.Progress = taskcommon.ProgressSubmitted
	require.NoError(t, f.legacy.Insert())

	err := updateVideoTasks(context.Background(), f.legacy.Platform, f.legacy.ChannelId,
		[]string{f.legacy.GetUpstreamTaskID()}, map[string]*model.Task{f.legacy.GetUpstreamTaskID(): &f.legacy})
	require.Error(t, err)

	var stored model.Task
	require.NoError(t, model.DB.First(&stored, f.legacy.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusSubmitted), stored.Status)
	require.NotEqual(t, taskcommon.ProgressComplete, stored.Progress)
	media, settlement, _, _ := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSubmitted, media.State)
	require.Equal(t, model.ZTAPISettlementReserved, settlement.Status)
}

func TestZTAPIMediaTaskPollingInitializesAdaptorFromFrozenTaskContract(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	baseURL := "https://aihub.example.test"
	adaptor := &capturingMediaPollingAdaptor{
		result: &relaycommon.TaskInfo{Status: string(model.TaskStatusInProgress), Progress: taskcommon.ProgressInProgress},
	}
	channel := &model.Channel{Id: f.legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}

	err := updateVideoSingleTask(context.Background(), adaptor, channel, f.legacy.GetUpstreamTaskID(), map[string]*model.Task{
		f.legacy.GetUpstreamTaskID(): &f.legacy,
	})
	require.NoError(t, err)
	require.NotNil(t, adaptor.initialized)
	require.Equal(t, baseURL, adaptor.initialized.ChannelBaseUrl)
	require.Equal(t, "provider-secret", adaptor.initialized.ApiKey)
	require.Equal(t, "provider-video-exact", adaptor.initialized.UpstreamModelName)
	require.NotNil(t, adaptor.initialized.ZTAPIPublicationSnapshot)
	require.NotNil(t, adaptor.initialized.ZTAPIPublicationSnapshot.VideoProtocolContract)
	require.Equal(t, "provider-video-exact", adaptor.initialized.ZTAPIPublicationSnapshot.VideoProtocolContract.ProviderModel)
	require.True(t, adaptor.fetchObservedFrozenContract)
}

func TestZTAPIMediaTaskUnknownThroughPollingWorkerRemainsPollable(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.legacy.Status = model.TaskStatusInProgress
	f.legacy.Progress = taskcommon.ProgressInProgress
	require.NoError(t, f.legacy.Insert())
	baseURL := "https://aihub.example.test"
	adaptor := &capturingMediaPollingAdaptor{result: &relaycommon.TaskInfo{
		Status: string(model.TaskStatusUnknown), ProviderStatus: "provider_future_state",
		UpstreamRequestID: "provider-fetch-request-unknown-worker", UpstreamTaskID: "upstream-task-1",
	}}
	channel := &model.Channel{Id: f.legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}

	err := updateVideoSingleTask(context.Background(), adaptor, channel, f.legacy.GetUpstreamTaskID(), map[string]*model.Task{
		f.legacy.GetUpstreamTaskID(): &f.legacy,
	})
	require.NoError(t, err)

	var legacy model.Task
	require.NoError(t, model.DB.Where("task_id = ?", f.legacy.TaskID).Take(&legacy).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusInProgress), legacy.Status)
	require.NotEqual(t, taskcommon.ProgressComplete, legacy.Progress)
	media, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskUnknown, media.State)
	require.Equal(t, model.ZTAPISettlementPending, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
}

func TestZTAPIMediaTaskRecoveryPollsAcceptedProviderTaskWithoutSecondCreate(t *testing.T) {
	createCount, fetchCount := 0, 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/hub/v1/video/tasks":
			createCount++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"request_id":"submit-request-1","data":{"task_id":"upstream-task-1","status":"queued"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/hub/v1/video/tasks/upstream-task-1":
			fetchCount++
			_, _ = w.Write([]byte(`{"request_id":"fetch-request-after-restart","data":{"task_id":"upstream-task-1","status":"completed","result":{"url":"https://cdn.invalid/result.mp4","resolution":"720p","duration":5},"usage":{"input_tokens":6}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	response, err := http.Post(provider.URL+"/hub/v1/video/tasks", "application/json", strings.NewReader(`{"prompt":"calm lake"}`))
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.Equal(t, 1, createCount)

	// The accepted provider identity is durable before the buffered client
	// response is committed; simulate process loss before the legacy row exists.
	f := setupZTAPIMediaServiceFixture(t)
	var legacyCount int64
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", f.legacy.TaskID).Count(&legacyCount).Error)
	require.Zero(t, legacyCount)

	recovered, err := RecoverZTAPIMediaLegacyTasks(context.Background(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	legacy, exists, err := model.GetByOnlyTaskId(f.legacy.TaskID)
	require.NoError(t, err)
	require.True(t, exists)

	baseURL := provider.URL
	adaptor := &recoveryProviderPollingAdaptor{}
	channel := &model.Channel{Id: legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}
	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, legacy.GetUpstreamTaskID(), map[string]*model.Task{
		legacy.GetUpstreamTaskID(): legacy,
	}))
	require.Equal(t, 1, createCount)
	require.Equal(t, 1, fetchCount)
	media, settlement, _, _ := f.reload(t)
	require.Equal(t, model.ZTAPIMediaTaskSucceeded, media.State)
	require.Equal(t, model.ZTAPISettlementSettled, settlement.Status)
}

func TestZTAPIMediaTaskPollingDataDoesNotPersistUpstreamIdentity(t *testing.T) {
	result := &relaycommon.TaskInfo{Status: string(model.TaskStatusInProgress), Progress: taskcommon.ProgressInProgress}
	raw := []byte(`{"id":"upstream-secret","status":"running","internal":"private"}`)
	sanitized := persistedPollingTaskData(true, result, raw)
	require.NotContains(t, string(sanitized), "upstream-secret")
	require.NotContains(t, string(sanitized), "private")
	require.JSONEq(t, `{"progress":"30%","status":"IN_PROGRESS"}`, string(sanitized))
	require.JSONEq(t, string(raw), string(persistedPollingTaskData(false, result, raw)))
}

func TestZTAPIMediaTaskPollingLogsDoNotExposeUpstreamIdentityOrResult(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.legacy.Status = model.TaskStatusSubmitted
	require.NoError(t, f.legacy.Insert())
	baseURL := "https://aihub.example.test"
	adaptor := &capturingMediaPollingAdaptor{
		responseBody: `{"request_id":"private-request-id","data":{"task_id":"private-task-id","status":"processing","result":{"url":"https://private.example/video.mp4"}}}`,
		result: &relaycommon.TaskInfo{
			Status:            string(model.TaskStatusInProgress),
			Progress:          taskcommon.ProgressInProgress,
			ProviderStatus:    "processing",
			UpstreamRequestID: "private-request-id",
			UpstreamTaskID:    "private-task-id",
			Url:               "https://private.example/video.mp4",
		},
	}
	channel := &model.Channel{Id: f.legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}

	previousDebug := common.DebugEnabled
	previousWriter := gin.DefaultErrorWriter
	var debug bytes.Buffer
	common.DebugEnabled = true
	common.LogWriterMu.Lock()
	gin.DefaultErrorWriter = &debug
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previousWriter
		common.LogWriterMu.Unlock()
		common.DebugEnabled = previousDebug
	})

	err := updateVideoSingleTask(context.Background(), adaptor, channel, f.legacy.GetUpstreamTaskID(), map[string]*model.Task{
		f.legacy.GetUpstreamTaskID(): &f.legacy,
	})
	require.NoError(t, err)
	logOutput := debug.String()
	require.NotContains(t, logOutput, "private-request-id")
	require.NotContains(t, logOutput, "private-task-id")
	require.NotContains(t, logOutput, "private.example")
	require.Contains(t, logOutput, "managed ZTAPI media task")
	require.Contains(t, logOutput, string(model.TaskStatusInProgress))
}

func TestZTAPIMediaTaskFailureStoresOnlyNormalizedPublicReason(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	f.legacy.Status = model.TaskStatusSubmitted
	require.NoError(t, f.legacy.Insert())
	baseURL := "https://aihub.example.test"
	adaptor := &capturingMediaPollingAdaptor{
		responseBody: `{"request_id":"private-failure-request","data":{"task_id":"upstream-task-1","status":"failed","error":{"message":"private provider account exhausted"}}}`,
		result: &relaycommon.TaskInfo{
			Status:            string(model.TaskStatusFailure),
			Progress:          taskcommon.ProgressComplete,
			ProviderStatus:    "failed",
			UpstreamRequestID: "private-failure-request",
			UpstreamTaskID:    "upstream-task-1",
			Reason:            "private provider account exhausted",
			UsageDimensions:   map[string]string{"input_tokens": "3"},
		},
	}
	channel := &model.Channel{Id: f.legacy.ChannelId, Key: "provider-secret", BaseURL: &baseURL}

	require.NoError(t, updateVideoSingleTask(context.Background(), adaptor, channel, f.legacy.GetUpstreamTaskID(), map[string]*model.Task{
		f.legacy.GetUpstreamTaskID(): &f.legacy,
	}))

	var stored model.Task
	require.NoError(t, model.DB.Where("task_id = ?", f.legacy.TaskID).Take(&stored).Error)
	require.Equal(t, "provider_task_failed", stored.FailReason)
	require.NotContains(t, string(stored.Data), "private")
	require.NotContains(t, stored.FailReason, "account")
}

func TestZTAPIMediaTaskLegacyFinanceFunctionsFailClosed(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	beforeLogs := countLogs(t)

	RefundTaskQuota(context.Background(), &f.legacy, "must not refund")
	RecalculateTaskQuota(context.Background(), &f.legacy, 500, "must not reprice")
	RecalculateTaskQuotaByTokens(context.Background(), &f.legacy, 1234)
	settleTaskBillingOnComplete(context.Background(), &mockAdaptor{adjustReturn: 250}, &f.legacy, &relaycommon.TaskInfo{Status: string(model.TaskStatusSuccess), TotalTokens: 999})

	_, settlement, user, token := f.reload(t)
	require.Equal(t, model.ZTAPISettlementReserved, settlement.Status)
	require.Equal(t, f.user.Quota, user.Quota)
	require.Equal(t, f.token.RemainQuota, token.RemainQuota)
	require.Equal(t, beforeLogs, countLogs(t))
	require.Equal(t, 7500, f.legacy.Quota)
}

func TestZTAPIMediaTaskSelectorUsesExactQuotedShape(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	otherInfo := relaycommon.RelayInfo{
		UserId: f.info.UserId, TokenId: f.info.TokenId, RequestId: "selector-" + common.GetUUID(),
		OriginModelName: f.info.OriginModelName, ZTAPIPublicationSnapshot: f.info.ZTAPIPublicationSnapshot.Clone(),
		ChannelMeta:   f.info.ChannelMeta,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{PublicTaskID: "task_" + common.GetUUID()},
	}
	otherSettlement := f.settlement
	otherSettlement.ID = 0
	otherSettlement.OperationID = "selector-op-" + common.GetUUID()
	otherSettlement.RequestID = otherInfo.RequestId
	require.NoError(t, model.DB.Create(&otherSettlement).Error)
	attempt, err := model.BeginZTAPIRequestAttempt(otherSettlement.OperationID, 702, "credential-v2", "/hub/v1/videos")
	require.NoError(t, err)
	require.NoError(t, model.RecordZTAPIRequestAttemptResponse(otherSettlement.OperationID, attempt.Attempt, attempt.ChannelID, 202, "submit-request-2"))
	otherInfo.Billing = &ztapiDurableBilling{row: &otherSettlement, info: &otherInfo, attempt: attempt}

	media, err := BindZTAPIMediaTaskSubmission(&otherInfo, relaycommon.TaskSubmitReq{Size: "720p", Duration: 5}, "upstream-task-2")
	require.NoError(t, err)
	require.Contains(t, media.SelectorJSON, `"contains_video_input":"false"`)
	require.NotContains(t, media.SelectorJSON, "resolution")
}

func TestZTAPIMediaTaskSelectorRejectsUnverifiedVideoInputClassification(t *testing.T) {
	f := setupZTAPIMediaServiceFixture(t)
	err := ValidateZTAPIMediaTaskSubmission(f.info, relaycommon.TaskSubmitReq{Images: []string{"data:image/png;base64,redacted"}})
	require.Error(t, err)
}

func TestZTAPIMediaTaskRejectsMissingDurableSubmissionIdentity(t *testing.T) {
	_, err := BindZTAPIMediaTaskSubmission(&relaycommon.RelayInfo{}, relaycommon.TaskSubmitReq{}, "upstream-task")
	require.Error(t, err)
	require.Contains(t, fmt.Sprint(err), "durable")
}

func TestZTAPIMediaTaskRequestCannotFallBackToLegacyBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: model.ZTAPIModalityVideo}}
	require.True(t, IsZTAPIMediaRequest(info))
	require.False(t, IsZTAPIMediaBilling(info))
	info.IsChannelTest = true
	require.False(t, IsZTAPIMediaRequest(info))
}

func ztapiVideoProtocolForServiceTest(t *testing.T) types.ZTAPIVideoProtocolContract {
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
	sealed, _, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	return sealed
}

type capturingMediaPollingAdaptor struct {
	initialized                 *relaycommon.RelayInfo
	fetchObservedFrozenContract bool
	responseBody                string
	result                      *relaycommon.TaskInfo
}

func (a *capturingMediaPollingAdaptor) Init(info *relaycommon.RelayInfo) {
	a.initialized = info
}

func (a *capturingMediaPollingAdaptor) FetchTask(string, string, map[string]any, string) (*http.Response, error) {
	a.fetchObservedFrozenContract = a.initialized != nil && a.initialized.ZTAPIPublicationSnapshot != nil &&
		a.initialized.ZTAPIPublicationSnapshot.VideoProtocolContract != nil
	body := a.responseBody
	if body == "" {
		body = `{}`
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func (a *capturingMediaPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return a.result, nil
}

func (*capturingMediaPollingAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

type recoveryProviderPollingAdaptor struct {
	baseURL string
}

func (a *recoveryProviderPollingAdaptor) Init(info *relaycommon.RelayInfo) {
	a.baseURL = info.ChannelBaseUrl
}

func (a *recoveryProviderPollingAdaptor) FetchTask(_ string, _ string, body map[string]any, _ string) (*http.Response, error) {
	taskID, _ := body["task_id"].(string)
	return http.Get(a.baseURL + "/hub/v1/video/tasks/" + taskID)
}

func (*recoveryProviderPollingAdaptor) ParseTaskResult([]byte) (*relaycommon.TaskInfo, error) {
	return &relaycommon.TaskInfo{
		Status: string(model.TaskStatusSuccess), ProviderStatus: "completed",
		UpstreamRequestID: "fetch-request-after-restart", UpstreamTaskID: "upstream-task-1",
		Url: "https://cdn.invalid/result.mp4", UsageDimensions: map[string]string{"input_tokens": "6"},
		ResultMetadata: map[string]string{"resolution": "720p", "duration": "5"},
	}, nil
}

func (*recoveryProviderPollingAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}
