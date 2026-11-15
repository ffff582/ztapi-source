package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIWorkbenchNativeBillingAdoptsHoldAndRecordsSingleAttempt(t *testing.T) {
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.Model(token).Updates(map[string]any{"unlimited_quota": false, "expired_time": time.Now().Add(10 * time.Minute).Unix(), "model_limits_enabled": true, "model_limits": "zt-model"}).Error)
	require.NoError(t, db.AutoMigrate(&model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIWorkbenchTask{}))
	const task = "9f1039cf-53b8-442d-af65-550f68a156fe"
	const hold = "8194d472-e6be-4453-87b3-abfc60db22b1"
	const body = `{"model":"zt-model","input":[{"role":"user","content":"hello"}],"stream":true,"store":false,"max_output_tokens":4}`
	snapshot := &relaycommon.ZTAPIPublicationSnapshot{PublicationID: 1, Version: 1, PublicName: "zt-model", SourceModel: "gpt-5.5", Modality: "text", InputPricePerMillion: 1, OutputPricePerMillion: 2, AllowedChannelIDs: []int{7, 8}}
	price, err := common.Marshal(snapshot)
	require.NoError(t, err)
	rows, err := model.BeginZTAPIWorkbenchBatch(task, 100, []model.ZTAPIWorkbenchLeg{{Stage: "main",
		Native:     &model.ZTAPIWorkbenchNativeBounds{BodySHA256: common.HashZTAPIKey(body), OutboundBytes: 4096, MaxOutput: 4},
		Settlement: model.ZTAPIRequestSettlement{OperationID: hold, RequestID: hold, UserID: user.Id, TokenID: token.Id, PublicModel: "zt-model", PriceSnapshotJSON: string(price), ReservedQuota: 20}}})
	require.NoError(t, err)
	ctxFor := func(raw string, price *relaycommon.ZTAPIPublicationSnapshot) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(raw))
		ctx.Request.Header.Set("Content-Type", "application/json")
		BindZTAPIWorkbenchRelayHold(ctx, task, hold)
		return ctx, &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, OriginModelName: "zt-model", RelayMode: relayconstant.RelayModeResponses, ZTAPIPublicationSnapshot: price,
			ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 7, ApiKey: "offline-fixture"}}
	}
	changed := snapshot.Clone()
	changed.Version++
	badCtx, bad := ctxFor(body, changed)
	require.NotNil(t, PreConsumeBilling(badCtx, 10, bad))
	require.Nil(t, bad.Billing)
	badCtx, bad = ctxFor(strings.Replace(body, "hello", "other", 1), snapshot)
	require.NotNil(t, PreConsumeBilling(badCtx, 10, bad))
	badCtx, bad = ctxFor(body, snapshot)
	require.NotNil(t, PreConsumeBilling(badCtx, 21, bad))
	ctx, info := ctxFor(body, snapshot)
	require.Nil(t, PreConsumeBilling(ctx, 10, info))
	require.Equal(t, rows[0].RequestID, info.RequestId)
	require.Equal(t, 20, info.Billing.GetPreConsumedQuota())
	info.ChannelBaseUrl = "https://provider.invalid/hub"
	info.UpstreamModelName = "gpt-5.5-mapped"
	for _, tc := range []struct {
		path, model string
		allowed     bool
	}{
		{"/hub/v1/responses", info.UpstreamModelName, true},
		{"/hub/v1/responses", snapshot.SourceModel, false},
		{"/v1/responses", info.UpstreamModelName, false},
		{"/hub/v1/responses?extra=1", info.UpstreamModelName, false},
	} {
		out := httptest.NewRequest("POST", "https://provider.invalid"+tc.path,
			strings.NewReader(strings.Replace(body, "zt-model", tc.model, 1)))
		out.Header.Set("Content-Type", "application/json")
		if tc.allowed {
			require.NoError(t, CheckZTAPIWorkbenchOutbound(info, out))
		} else {
			require.Error(t, CheckZTAPIWorkbenchOutbound(info, out))
		}
	}
	var count int64
	require.NoError(t, db.Model(&model.ZTAPIRequestSettlement{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	otherCtx, other := ctxFor(body, snapshot)
	require.Nil(t, PreConsumeBilling(otherCtx, 10, other))
	other.ChannelId = 8
	require.NoError(t, db.Model(token).Update("model_limits", "zt-unrelated").Error)
	require.ErrorIs(t, BeginZTAPIBillingAttempt(info, "/v1/responses"), model.ErrZTAPISettlementConflict)
	require.NoError(t, db.Model(token).Update("model_limits", "zt-model").Error)
	require.NoError(t, BeginZTAPIBillingAttempt(info, "/v1/responses"))
	require.ErrorIs(t, BeginZTAPIBillingAttempt(other, "/v1/responses"), model.ErrZTAPISettlementConflict)
	require.NoError(t, db.Model(&model.ZTAPIRequestAttempt{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"loopback-native-receipt"}}}))
	require.NoError(t, FinishZTAPIBilling(ctx, info))
	var persisted model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&persisted, rows[0].ID).Error)
	require.Equal(t, model.ZTAPISettlementPending, persisted.Status)
	var wallet model.User
	require.NoError(t, db.First(&wallet, user.Id).Error)
	require.Equal(t, 80, wallet.Quota)
}
