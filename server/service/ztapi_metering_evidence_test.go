package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestZTAPIMeteringEvidencePersistsSettledAndPending(t *testing.T) {
	for _, name := range []string{"settled", "pending", "transport", "invalid"} {
		t.Run(name, func(t *testing.T) {
			db, user, token := setupServiceTokenQuotaTest(t)
			oldLogDB := model.LOG_DB
			model.LOG_DB = db
			t.Cleanup(func() { model.LOG_DB = oldLogDB })
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementLogOutbox{}, &model.ZTAPISettlementLogReceipt{}))
			require.NoError(t, model.MigrateZTAPISupplierRefund(db))
			channel := model.Channel{Name: "offline-metering", Type: 1, Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"PRIVATE_CUSTOMER_CODE"}]}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			info := &relaycommon.RelayInfo{UserId: user.Id, TokenId: token.Id, TokenUnlimited: token.UnlimitedQuota,
				RequestId: "metering-" + name, OriginModelName: "zt-metering", RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ApiKey: "PRIVATE_API_KEY"},
				ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{PublicName: "zt-metering", PublicationID: 1, Version: 1,
					PriceSourceID: 1, PriceSourceVersion: 1, Modality: "text", BillingDimensions: []string{"input_tokens", "output_tokens"},
					SaleUSD: map[string]string{"input_tokens": "2", "output_tokens": "4"}},
			}
			info.SetEstimatePromptTokens(1000)
			relaycommon.CaptureZTAPIRequestMetering(ctx, info, &types.TokenCountMeta{MessagesCount: 1, CombineText: "PRIVATE_CUSTOMER_CODE", MaxTokens: 64})
			defer common.CleanupBodyStorage(ctx)
			relaycommon.CaptureZTAPIOutboundRequestMetering(info, 256)
			require.Nil(t, PreConsumeBilling(ctx, 30, info))
			require.NoError(t, BeginZTAPIBillingAttempt(info, "chat"))
			require.NoError(t, ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"offline-upstream"}}}))
			usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, UsageSource: "upstream_reported"}
			usage.Cost = map[string]string{"provider_note": "PRIVATE_COST_PAYLOAD"}
			if name == "transport" {
				require.NoError(t, FinishZTAPIBilling(ctx, info))
			} else if name == "pending" {
				usage.UsageSource = "local_estimate"
				require.NoError(t, pendZTAPIBilling(info, usage, []string{"upstream_usage_missing"}))
			} else {
				relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"note":"PRIVATE_PROVIDER_NOTE"}}`), "nonstream")
				if name == "invalid" {
					info.ZTAPIMetering.UsageIntegrityComplete = true
					info.ZTAPIMetering.UsageIntegrityReasons = []string{"usage_total_mismatch", "PRIVATE_REASON"}
				}
				PostTextConsumeQuota(ctx, info, usage, nil)
				PostTextConsumeQuota(ctx, info, usage, nil)
			}
			var row model.ZTAPIRequestSettlement
			require.NoError(t, db.Where("request_id = ?", info.RequestId).Take(&row).Error)
			var saved map[string]any
			require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &saved))
			evidence, ok := saved["ztapi_metering_evidence"].(map[string]any)
			require.True(t, ok, "durable usage JSON must retain independent metering metadata")
			if name == "transport" {
				require.Equal(t, "missing", evidence["usage_source"])
			} else {
				require.Equal(t, usage.UsageSource, evidence["usage_source"])
			}
			require.EqualValues(t, 256, evidence["outbound_body_bytes"])
			require.NotContains(t, row.UsageJSON, "PRIVATE_")
			if name != "settled" {
				require.Equal(t, model.ZTAPISettlementPending, row.Status)
				require.Zero(t, row.ChargedQuota)
				var count int64
				require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", info.RequestId).Count(&count).Error)
				require.Zero(t, count, "unverified usage must not create a consumption log")
				if name == "invalid" {
					require.Contains(t, row.MissingDimensionsJSON, "usage_total_mismatch")
					require.Equal(t, []any{"usage_total_mismatch"}, evidence["usage_integrity_reasons"])
				}
			} else {
				require.Equal(t, model.ZTAPISettlementSettled, row.Status)
				require.EqualValues(t, 20, row.ChargedQuota)
				var log model.Log
				require.NoError(t, db.Where("request_id = ?", info.RequestId).Take(&log).Error)
				require.Contains(t, log.Other, `"ztapi_metering_evidence"`)
				require.Contains(t, log.Other, `"raw_numeric_usage"`)
				require.NotContains(t, log.Other, "PRIVATE_")
				var count int64
				require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", info.RequestId).Count(&count).Error)
				require.EqualValues(t, 1, count)
			}
		})
	}
}
