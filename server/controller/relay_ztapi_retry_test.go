package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// All channels share the existing fixture's loopback-only transport, but have
// distinct identities and priorities. No external request can leave this test.
func configureZTAPIRetryChannels(t *testing.T, f *ztapiE2EFixture, count int) []int {
	t.Helper()
	previousDB := model.DB
	initModelListColumnNames(t)
	model.DB = previousDB
	var first model.Channel
	require.NoError(t, f.db.First(&first, 1).Error)
	ids := []int{first.Id}
	for i := 1; i < count; i++ {
		channel := first
		channel.Id = 0
		channel.Name = fmt.Sprintf("retry-loopback-%d", i)
		require.NoError(t, f.db.Create(&channel).Error)
		require.NoError(t, f.db.Create(&model.Ability{ChannelId: channel.Id, Model: f.sourceModel, Group: "default", Enabled: true, Weight: 100}).Error)
		ids = append(ids, channel.Id)
	}
	for i, id := range ids {
		priority := 100 - i*10
		require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", id).Update("priority", priority).Error)
		require.NoError(t, f.db.Model(&model.Ability{}).Where("channel_id = ?", id).Update("priority", priority).Error)
	}
	encoded, err := common.Marshal(ids)
	require.NoError(t, err)
	// Seed the multi-channel publication authority required by this retry fixture.
	require.NoError(t, f.db.Session(&gorm.Session{SkipHooks: true}).Model(&model.ZTAPIModelPublicationSnapshot{}).Where("id = ?", f.config.PublicationSnapshotID).Update("allowed_channel_ids", string(encoded)).Error)
	model.InitChannelCache()
	model.InvalidateZTAPIAliasCache()
	return ids
}

func TestZTAPIManagedRetryControllerLimit(t *testing.T) {
	for _, cache := range []bool{false, true} {
		for _, count := range []int{1, 3} {
			t.Run(fmt.Sprintf("cache=%t/channels=%d", cache, count), func(t *testing.T) {
				f := newZTAPIHealthE2EFixture(t)
				ids := configureZTAPIRetryChannels(t, f, count)
				common.MemoryCacheEnabled = cache
				common.RetryTimes = 5
				for i := 0; i < 6; i++ {
					f.queue(ztapiE2EFailure())
				}
				w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "managed-limit", false)
				require.Equal(t, 502, w.Code, w.Body.String())
				want := min(count, 2)
				require.EqualValues(t, want, f.calls.Load())
				events := f.events(t)
				require.Len(t, events, 1)
				var outcome types.ZTAPIHealthOutcome
				require.NoError(t, common.UnmarshalJsonStr(events[0].Outcome, &outcome))
				require.Len(t, outcome.Attempts, want)
				for i, attempt := range outcome.Attempts {
					require.Equal(t, ids[i], attempt.ChannelID)
				}
				f.assertSettlement(t, 0, model.ZTAPISettlementPending, ids[:want])
			})
		}
	}
}

func TestZTAPIManagedRetryUnavailableFallbackKeepsFailure(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			common.MemoryCacheEnabled = cache
			require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", ids[1]).Update("status", common.ChannelStatusAutoDisabled).Error)
			model.CacheUpdateChannelStatus(ids[1], common.ChannelStatusAutoDisabled)
			f.queue(ztapiE2EFailure())
			w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "fallback-unavailable", false)
			require.Equal(t, 502, w.Code, w.Body.String())
			require.EqualValues(t, 1, f.calls.Load())
			events := f.events(t)
			require.Len(t, events, 1)
			require.Equal(t, "failure", events[0].Result)
			require.Equal(t, "local-upstream-1", events[0].UpstreamRequestID)
			f.assertSettlement(t, 0, model.ZTAPISettlementPending, ids[:1])
		})
	}
}

type ztapiRetryTransport struct {
	base   http.RoundTripper
	before func()
}

func (r ztapiRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.before()
	return r.base.RoundTrip(req)
}

func TestZTAPIManagedRetryFreezesPriceAndReservation(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			common.MemoryCacheEnabled = cache
			f.queue(ztapiE2ESuccess("/v1/chat/completions"))
			w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "price-control", false)
			require.Equal(t, 200, w.Code, w.Body.String())
			before := f.balances(t)
			charged := ztapiE2EInitialQuota - before.UserQuota
			require.Positive(t, charged)
			control := f.assertSettlement(t, 0, model.ZTAPISettlementSettled, ids[:1])
			client := service.GetHttpClient()
			base := client.Transport
			mutated := false
			client.Transport = ztapiRetryTransport{base: base, before: func() {
				if mutated {
					return
				}
				mutated = true
				// Seed an in-flight authority rewrite solely to verify the request keeps its frozen price.
				require.NoError(t, f.db.Session(&gorm.Session{SkipHooks: true}).Model(&model.ZTAPIModelPublicationSnapshot{}).Where("id = ?", f.config.PublicationSnapshotID).Updates(map[string]any{"input_price_per_million": 999, "output_price_per_million": 999}).Error)
			}}
			f.queue(ztapiE2EFailure(), ztapiE2ESuccess("/v1/chat/completions"))
			w = f.request(t, "/v1/chat/completions", f.publicName, f.key, "price-retry", false)
			require.Equal(t, 200, w.Code, w.Body.String())
			after := f.balances(t)
			require.Equal(t, charged, before.UserQuota-after.UserQuota)
			require.Equal(t, charged, before.TokenRemain-after.TokenRemain)
			var reservations int64
			require.NoError(t, f.db.Model(&model.BalanceLedger{}).Where("source_type = ?", model.BalanceLedgerSourceUsageReservation).Count(&reservations).Error)
			require.EqualValues(t, 2, reservations, "one reservation for each logical request")
			var outcome types.ZTAPIHealthOutcome
			require.NoError(t, common.UnmarshalJsonStr(f.events(t)[1].Outcome, &outcome))
			require.Len(t, outcome.Attempts, 2)
			require.Equal(t, ids[0], outcome.Attempts[0].ChannelID)
			require.Equal(t, ids[1], outcome.Attempts[1].ChannelID)
			retried := f.assertSettlement(t, 1, model.ZTAPISettlementSettled, ids)
			require.Equal(t, control.PriceSnapshotJSON, retried.PriceSnapshotJSON, "in-flight publication edits must not reprice the retry")
			require.NotEqual(t, control.OperationID, retried.OperationID)
			require.EqualValues(t, 2, after.Logs, "one final log per logical request")
			var user model.User
			require.NoError(t, f.db.First(&user, f.user.Id).Error)
			require.Equal(t, 2, user.RequestCount)
			require.Equal(t, charged*2, user.UsedQuota)
		})
	}
}

func TestZTAPIManagedRetryDispatchSettlementLogAndTrustedRefundJourney(t *testing.T) {
	f := newZTAPIHealthE2EFixture(t)
	ids := configureZTAPIRetryChannels(t, f, 2)
	f.queue(ztapiE2EFailure(), ztapiE2ESuccess("/v1/chat/completions"))
	w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "controller-refund-journey", false)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.EqualValues(t, 2, f.calls.Load())
	row := f.assertSettlement(t, 0, model.ZTAPISettlementSettled, ids)
	var charge model.ZTAPISupplierRefundCharge
	require.NoError(t, f.db.Where("request_id = ?", row.RequestID).Take(&charge).Error)
	require.Equal(t, 2, charge.Attempt)
	require.Equal(t, ids[1], charge.ChannelID)
	require.Equal(t, "local-upstream-2", charge.UpstreamRequestID)
	operator := model.User{Username: "controller-refund-reviewer", Status: common.UserStatusEnabled, Role: common.RoleRootUser, AffCode: "refund-reviewer"}
	require.NoError(t, f.db.Create(&operator).Error)
	proof, err := model.SubmitZTAPISupplierRefund(model.ZTAPISupplierRefundSubmission{
		Source: "loopback-fixture", ProofID: "controller-full-refund", RequestID: row.RequestID,
		UserID: f.user.Id, Attempt: charge.Attempt, ChannelID: charge.ChannelID,
		CredentialVersion: charge.CredentialVersion, UpstreamRequestID: charge.UpstreamRequestID,
		Mode: "full", EvidenceReference: "synthetic-supplier-reversal",
	})
	require.NoError(t, err)
	beforeApproval := f.balances(t)
	require.Error(t, model.ProcessZTAPISupplierRefund(proof.ID), "unreviewed supplier evidence must not credit customer funds")
	require.Equal(t, beforeApproval, f.balances(t))
	require.NoError(t, model.ApproveZTAPISupplierRefund(proof.ID, operator.Id, "synthetic-reviewed-statement"))
	require.NoError(t, model.ProcessZTAPISupplierRefund(proof.ID))
	refunded := f.balances(t)
	require.NoError(t, model.ProcessZTAPISupplierRefund(proof.ID))
	require.Equal(t, refunded, f.balances(t), "redelivery must not refund twice")
	require.Equal(t, ztapiE2EInitialQuota, refunded.UserQuota)
	require.Equal(t, ztapiE2EInitialQuota, refunded.TokenRemain)
	require.Zero(t, refunded.TokenUsed)
	require.EqualValues(t, 1, refunded.Logs)
	require.EqualValues(t, 1, refunded.Settlements)
	require.EqualValues(t, 2, refunded.Attempts)
	var user model.User
	require.NoError(t, f.db.First(&user, f.user.Id).Error)
	require.Equal(t, 1, user.RequestCount)
	require.EqualValues(t, row.ChargedQuota, user.UsedQuota, "refund must not rewrite historical consumption")
	require.NoError(t, f.db.First(&row, row.ID).Error)
	require.Equal(t, row.ChargedQuota, row.RefundedQuota)
	processed, err := model.ProcessPendingZTAPISettlementLogs(10)
	require.NoError(t, err)
	require.Zero(t, processed, "log redelivery must not insert a duplicate")
}

func TestZTAPIManagedRetryRejectsUnsafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
	}{
		{"parameter", "invalid_request_error", 422},
		{"safety", "content_filter", 503},
		{"refusal", "safety_refusal", 403},
		{"ambiguous forbidden", "permission_denied", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			f.queue(ztapiE2EReply{status: tc.status, body: fmt.Sprintf(`{"error":{"type":%q,"code":%q,"message":"local rejection"}}`, tc.code, tc.code)}, ztapiE2ESuccess("/v1/chat/completions"))
			w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "unsafe-retry", false)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.EqualValues(t, 1, f.calls.Load())
			f.assertSettlement(t, 0, model.ZTAPISettlementPending, ids[:1])
		})
	}
}

func TestZTAPIManagedRetryKnownUpstreamFaults(t *testing.T) {
	oldRanges := operation_setting.AutomaticRetryStatusCodeRanges
	operation_setting.AutomaticRetryStatusCodeRanges = nil
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = oldRanges })
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"invalid_api_key", 401}, {"model_not_granted", 403}, {"insufficient_quota", 402}, {"rate_limit_exceeded", 429},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 2)
			f.queue(ztapiE2EReply{status: tc.status, body: fmt.Sprintf(`{"error":{"type":"invalid_request_error","code":%q,"message":"local upstream route fault"}}`, tc.code)}, ztapiE2ESuccess("/v1/chat/completions"))
			w := f.request(t, "/v1/chat/completions", f.publicName, f.key, "known-upstream-fault", false)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.EqualValues(t, 2, f.calls.Load())
			f.assertSettlement(t, 0, model.ZTAPISettlementSettled, ids)
		})
	}
}

func TestZTAPIManagedRetryExplicitPolicyKeepsVetoes(t *testing.T) {
	oldRanges := operation_setting.AutomaticRetryStatusCodeRanges
	operation_setting.AutomaticRetryStatusCodeRanges = nil
	t.Cleanup(func() { operation_setting.AutomaticRetryStatusCodeRanges = oldRanges })
	for _, mode := range []string{"managed", "legacy", "affinity", "specific", "skip", "exhausted", "timeout", "bad-body", "success", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if mode != "legacy" {
				relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{AllowedChannelIDs: []int{11, 22}})
			}
			err := types.NewError(errors.New("local upstream unavailable"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithStatusCode(502))
			remaining := 1
			switch mode {
			case "affinity":
				c.Set("channel_affinity_skip_retry_on_failure", true)
			case "specific":
				c.Set("specific_channel_id", 11)
			case "skip":
				err = types.NewError(errors.New("local failure"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithStatusCode(502), types.ErrOptionWithSkipRetry())
			case "exhausted":
				remaining = 0
			case "timeout":
				err.StatusCode = 504
			case "bad-body":
				err = types.NewError(errors.New("bad body"), types.ErrorCodeBadResponseBody, types.ErrOptionWithStatusCode(502))
			case "success":
				err.StatusCode = 200
			case "redirect":
				err.StatusCode = 302
			}
			require.Equal(t, mode == "managed", shouldRetry(c, err, remaining))
		})
	}
}

func TestZTAPIManagedRetryEmptyAuthorityFailsClosed(t *testing.T) {
	f := newZTAPIHealthE2EFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{OriginModelName: f.publicName, SelectionModelName: f.sourceModel, TokenGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{}}
	channel, err := getChannel(c, info, newRelayRetryParam(c, info))
	require.NotNil(t, err)
	require.Nil(t, channel)
}

func TestZTAPIManagedRetrySelectionBoundaries(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 3)
			common.MemoryCacheEnabled = cache
			// A higher-priority channel outside the snapshot must not be selected.
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{OriginModelName: f.publicName, SelectionModelName: f.sourceModel, TokenGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{AllowedGroups: []string{"default"}, AllowedChannelIDs: ids[1:]}}
			p := newRelayRetryParam(c, info)
			for _, want := range ids[1:] {
				channel, err := getChannel(c, info, p)
				require.Nil(t, err)
				require.Equal(t, want, channel.Id)
				p.SetRetry(0) // Auto-group counter resets cannot reset the attempt budget.
			}
			channel, err := getChannel(c, info, p)
			require.NotNil(t, err)
			require.Nil(t, channel)
			// Status exclusion remains effective even with a stale enabled ability.
			require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", ids[1]).Update("status", common.ChannelStatusAutoDisabled).Error)
			model.CacheUpdateChannelStatus(ids[1], common.ChannelStatusAutoDisabled)
			p = newRelayRetryParam(c, info)
			channel, err = getChannel(c, info, p)
			require.Nil(t, err)
			require.Equal(t, ids[2], channel.Id)
		})
	}
}

func TestZTAPIManagedRetryCancellationAndCommitBoundaries(t *testing.T) {
	for _, mode := range []string{"cancelled", "deadline", "headers", "body", "flush"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{AllowedChannelIDs: []int{11, 22}})
			switch mode {
			case "cancelled":
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			case "deadline":
				ctx, cancel := context.WithTimeout(c.Request.Context(), 0)
				defer cancel()
				c.Request = c.Request.WithContext(ctx)
			case "headers":
				c.Writer.WriteHeaderNow()
			case "body":
				_, _ = c.Writer.WriteString("partial")
			case "flush":
				c.Writer.Flush()
			}
			err := types.NewError(errors.New("upstream failed"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithStatusCode(502))
			require.False(t, shouldRetry(c, err, 1))
		})
	}
}

func TestZTAPIManagedRetrySkipFlagPrecedesChannelError(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	relaycommon.SetZTAPIPublicationSnapshot(c, &relaycommon.ZTAPIPublicationSnapshot{AllowedChannelIDs: []int{11, 22}})
	err := types.NewError(errors.New("do not retry"), types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
	require.False(t, shouldRetry(c, err, 1))
}

func TestZTAPINonManagedRetryKeepsLegacyRepeat(t *testing.T) {
	f := newZTAPIHealthE2EFixture(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	info := &relaycommon.RelayInfo{OriginModelName: f.sourceModel, SelectionModelName: f.sourceModel, TokenGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{}}
	p := newRelayRetryParam(c, info)
	for i := 0; i < 3; i++ {
		channel, err := getChannel(c, info, p)
		require.Nil(t, err)
		require.Equal(t, 1, channel.Id)
		p.IncreaseRetry()
	}
}

func TestZTAPIManagedRetryDoesNotChangeTaskPolicy(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{RelayFormat: types.RelayFormatTask, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{AllowedChannelIDs: []int{17, 29}}}
	p := newRelayRetryParam(c, info)
	require.False(t, p.Managed, "asynchronous task retry policy is outside this text change")
	require.Equal(t, []int{17, 29}, p.AllowedChannelIDs)
}

func TestTaskRelayDoesNotRetryLocalPostAcceptanceFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	taskErr := service.TaskErrorWrapperLocal(errors.New("accepted task persistence failed"), "media_task_persistence_failed", http.StatusServiceUnavailable)
	require.False(t, shouldRetryTaskRelay(c, 17, taskErr, 1))
}

func TestTaskRelayDoesNotRetryAcceptedResponseParseFailure(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	relaycommon.MarkZTAPITaskProviderAccepted(c)
	taskErr := service.TaskErrorWrapper(errors.New("accepted response did not contain a task id"), "invalid_response", http.StatusInternalServerError)
	require.False(t, shouldRetryTaskRelay(c, 17, taskErr, 1))
}

func TestZTAPIManagedTaskDataDoesNotPersistUpstreamIdentity(t *testing.T) {
	raw := []byte(`{"id":"upstream-secret","status":"queued"}`)
	require.JSONEq(t, `{}`, string(persistedTaskData(true, raw)))
	require.Equal(t, raw, persistedTaskData(false, raw))
}

func TestZTAPIManagedRetryAutoGroupsCannotRepeatOrResetBudget(t *testing.T) {
	for _, crossGroup := range []bool{false, true} {
		t.Run(fmt.Sprint(crossGroup), func(t *testing.T) {
			f := newZTAPIHealthE2EFixture(t)
			ids := configureZTAPIRetryChannels(t, f, 3)
			oldGroups := setting.AutoGroups2JsonString()
			require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
			t.Cleanup(func() { require.NoError(t, setting.UpdateAutoGroupsByJsonString(oldGroups)) })
			for _, id := range ids[1:] {
				require.NoError(t, f.db.Model(&model.Channel{}).Where("id = ?", id).Update("group", "vip").Error)
				require.NoError(t, f.db.Model(&model.Ability{}).Where("channel_id = ?", id).Update("group", "vip").Error)
			}
			model.InitChannelCache()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			common.SetContextKey(c, constant.ContextKeyUserGroup, "vip")
			common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, crossGroup)
			info := &relaycommon.RelayInfo{OriginModelName: f.publicName, SelectionModelName: f.sourceModel, TokenGroup: "auto", ChannelMeta: &relaycommon.ChannelMeta{}, ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{AllowedGroups: []string{"default", "vip"}, AllowedChannelIDs: ids}}
			p := newRelayRetryParam(c, info)
			first, err := getChannel(c, info, p)
			require.Nil(t, err)
			require.Equal(t, ids[0], first.Id)
			p.IncreaseRetry()
			second, err := getChannel(c, info, p)
			if !crossGroup {
				require.NotNil(t, err)
				require.Nil(t, second)
				return
			}
			require.Nil(t, err)
			require.Equal(t, ids[1], second.Id)
			require.Zero(t, p.GetRetry(), "cross-group selection exercised the legacy counter reset")
			third, err := getChannel(c, info, p)
			require.NotNil(t, err)
			require.Nil(t, third)
		})
	}
}
