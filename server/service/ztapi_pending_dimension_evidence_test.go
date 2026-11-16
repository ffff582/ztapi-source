package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"gorm.io/gorm"
)

type savedPendingDimensionEvidence struct {
	Version           int                               `json:"version"`
	Pending           bool                              `json:"pending"`
	UsageMissing      bool                              `json:"usage_missing"`
	MissingDimensions []string                          `json:"missing_dimensions"`
	InvalidDimensions []string                          `json:"invalid_dimensions"`
	Reasons           []string                          `json:"reasons"`
	Dimensions        []relaycommon.ZTAPIUsageDimension `json:"dimensions"`
}

func setupPendingDimensionBilling(t *testing.T, configure ...func(*relaycommon.RelayInfo)) (*gorm.DB, *gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	db, user, token := setupServiceTokenQuotaTest(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Log{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPISettlementFinalizationIntent{}, &model.ZTAPIRequestAttempt{}))
	channel := model.Channel{Name: "offline-pending-dimensions", Type: 1, Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	info := &relaycommon.RelayInfo{
		UserId: user.Id, TokenId: token.Id, TokenUnlimited: token.UnlimitedQuota,
		RequestId: "pending-dimensions-" + common.GetUUID(), OriginModelName: "zt-offline",
		RelayFormat: types.RelayFormatOpenAIResponses,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channel.Id, ApiKey: "PRIVATE_API_KEY"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: "zt-offline", PublicationID: 1, Version: 1, PriceSourceID: 1, PriceSourceVersion: 1,
			Modality: "text", BillingDimensions: []string{"input_tokens", "output_tokens"},
			SaleUSD:             map[string]string{"input_tokens": "0.01", "output_tokens": "0.02"},
			TokenPriceRulesJSON: `[{"conditions":["\u8f93\u5165\u957f\u5ea6\u22642K"],"sale":{"input_tokens":"0.01","output_tokens":"0.02"}},{"conditions":["\u8f93\u5165\u957f\u5ea6>2K"],"sale":{"input_tokens":"0.02","output_tokens":"0.04"}}]`,
		},
	}
	for _, apply := range configure {
		apply(info)
	}
	require.Nil(t, PreConsumeBilling(ctx, 30, info))
	require.NoError(t, BeginZTAPIBillingAttempt(info, "/v1/responses"))
	require.NoError(t, ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": {"offline-evidence"}}}))
	return db, ctx, info
}

func readPendingDimensionEvidence(t *testing.T, db *gorm.DB, info *relaycommon.RelayInfo) (model.ZTAPIRequestSettlement, savedPendingDimensionEvidence) {
	t.Helper()
	var row model.ZTAPIRequestSettlement
	require.NoError(t, db.Where("request_id = ?", info.RequestId).Take(&row).Error)
	require.Equal(t, model.ZTAPISettlementPending, row.Status)
	require.Zero(t, row.ChargedQuota)
	require.JSONEq(t, "[]", row.ChargeDimensionsJSON, "observations must not become authorized charges")
	var saved struct {
		Evidence savedPendingDimensionEvidence `json:"ztapi_billing_dimension_evidence"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &saved))
	require.Equal(t, 1, saved.Evidence.Version, "pending must retain versioned billable-dimension observations")
	var user model.User
	require.NoError(t, db.First(&user, info.UserId).Error)
	require.Equal(t, 70, user.Quota, "the existing reservation must remain held")
	require.Zero(t, user.UsedQuota)
	require.Zero(t, user.RequestCount)
	var logs int64
	require.NoError(t, db.Model(&model.Log{}).Where("request_id = ?", info.RequestId).Count(&logs).Error)
	require.Zero(t, logs)
	return row, saved.Evidence
}

func TestZTAPIPendingDimensionEvidenceUsesSelectedFrozenTier(t *testing.T) {
	db, ctx, info := setupPendingDimensionBilling(t)
	info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
		"web_search": {ToolName: "PRIVATE_TOOL_METADATA", CallCount: 3, SearchContextSize: "PRIVATE_CONTEXT"},
	}}
	info.ZTAPIMetering = &relaycommon.ZTAPIMeteringEvidence{Version: 1, UsageIntegrityComplete: true,
		UsageIntegrityReasons: []string{"usage_total_mismatch", "PRIVATE_REASON"},
		RawUsage:              json.RawMessage(`{"input_tokens":2001,"output_tokens":5,"total_tokens":2006,"note":"PRIVATE_RAW_USAGE"}`)}
	info.ZTAPIPublicationSnapshot.SaleUSD["output_tokens"] = "999"
	info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON = "PRIVATE_MUTABLE_PRICE"
	usage := &dto.Usage{PromptTokens: 2001, CompletionTokens: 5, TotalTokens: 2006, UsageSource: "upstream_reported"}
	originalMetering := ztapiMeteringSnapshot(info, usage)
	PostTextConsumeQuota(ctx, info, usage, nil)
	require.NoError(t, FinishZTAPIBilling(ctx, info))
	row, evidence := readPendingDimensionEvidence(t, db, info)
	require.True(t, evidence.Pending)
	require.Equal(t, []string{"web_search"}, evidence.MissingDimensions)
	require.Equal(t, []string{"usage_total_mismatch"}, evidence.InvalidDimensions)
	require.Contains(t, evidence.Reasons, "missing_quote")
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 3, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "output_tokens", Quantity: 5, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.00000004"})
	var legacy dto.Usage
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &legacy))
	require.Equal(t, *usage, legacy, "old normalized usage readers must remain compatible")
	require.Contains(t, row.UsageJSON, `"ztapi_metering_evidence"`)
	var saved struct {
		Metering *relaycommon.ZTAPIMeteringEvidence `json:"ztapi_metering_evidence"`
	}
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &saved))
	require.Equal(t, originalMetering, saved.Metering, "raw numeric usage, hash and integrity reasons must remain intact")
	require.NotContains(t, row.UsageJSON, "PRIVATE_")
}

func TestZTAPIPendingDimensionEvidenceSurvivesTransportCleanupWithoutMetering(t *testing.T) {
	for _, source := range []string{"responses", "claude", "image"} {
		t.Run(source, func(t *testing.T) {
			db, ctx, info := setupPendingDimensionBilling(t)
			expected := relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 4, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing}
			if source == "responses" {
				info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{"web_search": {CallCount: 4}}}
			} else if source == "claude" {
				ctx.Set("claude_web_search_requests", 4)
			} else {
				ctx.Set("image_generation_call", true)
				expected = relaycommon.ZTAPIUsageDimension{Dimension: "image_generation::", Quantity: 1, Unit: "image", QuoteState: relaycommon.ZTAPIQuoteStateMissing}
			}
			require.NoError(t, FinishZTAPIBilling(ctx, info))
			row, evidence := readPendingDimensionEvidence(t, db, info)
			require.True(t, evidence.UsageMissing)
			require.Contains(t, evidence.Dimensions, expected)
			require.NoError(t, FinishZTAPIBilling(ctx, info))
			var repeated model.ZTAPIRequestSettlement
			require.NoError(t, db.First(&repeated, row.ID).Error)
			require.Equal(t, row.UsageJSON, repeated.UsageJSON)
		})
	}
}

func TestZTAPIPendingDimensionEvidenceRedactsUntrustedToolsAndUsageWithoutMetering(t *testing.T) {
	db, ctx, info := setupPendingDimensionBilling(t)
	info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{
		"web_search": {CallCount: 2},
	}}
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("https://PRIVATE_HOST/%d?key=PRIVATE_KEY%s", i, strings.Repeat("x", 200))
		info.ResponsesUsageInfo.BuiltInTools[name] = &relaycommon.BuildInToolInfo{ToolName: name, CallCount: 1}
	}
	usage := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, UsageSource: "PRIVATE_SOURCE", Cost: map[string]string{"url": "https://PRIVATE_COST?key=PRIVATE_KEY"}}
	PostTextConsumeQuota(ctx, info, usage, nil)
	row, evidence := readPendingDimensionEvidence(t, db, info)
	require.NotContains(t, row.UsageJSON, "PRIVATE_")
	require.NotContains(t, row.MissingDimensionsJSON, "PRIVATE_")
	require.Less(t, len(row.UsageJSON), 16384)
	require.LessOrEqual(t, len(evidence.Dimensions), 32)
	require.Contains(t, evidence.Reasons, "unrecognized_dimension")
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 2, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
}

func TestZTAPIPendingDimensionEvidenceRetainsContextOnInvalidFrozenPrice(t *testing.T) {
	for _, reason := range []string{"invalid_frozen_price", "token_tier_invalid"} {
		t.Run(reason, func(t *testing.T) {
			db, ctx, info := setupPendingDimensionBilling(t)
			ctx.Set("claude_web_search_requests", 3)
			ctx.Set("image_generation_call", true)
			ctx.Set("image_generation_call_quality", "high")
			ctx.Set("image_generation_call_size", "1024x1024")
			session := info.Billing.(*ztapiDurableBilling)
			if reason == "invalid_frozen_price" {
				session.row.PriceSnapshotJSON = "PRIVATE_BROKEN_SNAPSHOT"
			} else {
				var frozen relaycommon.ZTAPIPublicationSnapshot
				require.NoError(t, common.UnmarshalJsonStr(session.row.PriceSnapshotJSON, &frozen))
				frozen.TokenPriceRulesJSON = "PRIVATE_BROKEN_TIERS"
				raw, err := common.Marshal(frozen)
				require.NoError(t, err)
				session.row.PriceSnapshotJSON = string(raw)
			}
			PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, nil)
			row, evidence := readPendingDimensionEvidence(t, db, info)
			require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 3, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
			require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "image_generation:high:1024x1024", Quantity: 1, Unit: "image", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
			require.Contains(t, evidence.Reasons, reason)
			require.NotContains(t, row.UsageJSON, "PRIVATE_")
		})
	}
}

func TestZTAPIPendingDimensionEvidenceRetainsFreeQuoteAndHandoffReason(t *testing.T) {
	db, ctx, info := setupPendingDimensionBilling(t, func(info *relaycommon.RelayInfo) {
		info.ZTAPIPublicationSnapshot.BillingDimensions = append(info.ZTAPIPublicationSnapshot.BillingDimensions, "web_search")
		info.ZTAPIPublicationSnapshot.SaleUSD["web_search"] = "0"
		var rules []struct {
			Conditions []string          `json:"conditions"`
			Sale       map[string]string `json:"sale"`
		}
		require.NoError(t, common.UnmarshalJsonStr(info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON, &rules))
		for _, rule := range rules {
			rule.Sale["web_search"] = "0"
		}
		raw, err := common.Marshal(rules)
		require.NoError(t, err)
		info.ZTAPIPublicationSnapshot.TokenPriceRulesJSON = string(raw)
	})
	info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{"web_search": {CallCount: 2}}}
	info.Billing.(*ztapiDurableBilling).attempt = nil
	PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 2001, CompletionTokens: 5, TotalTokens: 2006}, nil)
	row, evidence := readPendingDimensionEvidence(t, db, info)
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 2, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateFree, UnitPriceUSD: "0"})
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "output_tokens", Quantity: 5, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.00000004"})
	require.Contains(t, evidence.Reasons, "dispatch_evidence_missing")
	require.JSONEq(t, `["dispatch_evidence_missing"]`, row.MissingDimensionsJSON)
}

func TestZTAPIPendingDimensionEvidenceSupportsNilAndValueUsage(t *testing.T) {
	for _, name := range []string{"nil", "value"} {
		t.Run(name, func(t *testing.T) {
			db, _, info := setupPendingDimensionBilling(t)
			info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{"web_search": {CallCount: 2}}}
			var usage any
			if name == "value" {
				usage = dto.Usage{PromptTokens: 2001, CompletionTokens: 5, TotalTokens: 2006}
			}
			require.NoError(t, pendZTAPIBilling(info, usage, []string{"upstream_usage_missing"}))
			_, evidence := readPendingDimensionEvidence(t, db, info)
			require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "web_search", Quantity: 2, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
			if name == "value" {
				require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "output_tokens", Quantity: 5, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.00000004"})
			}
		})
	}
}

func TestZTAPIPendingDimensionEvidencePreservesFinalizationIntentBytes(t *testing.T) {
	db, ctx, info := setupPendingDimensionBilling(t)
	callback := "pending-dimension-finalization-failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == "users" && ok {
			if _, counters := updates["used_quota"]; counters {
				tx.AddError(errors.New("offline finalization failure"))
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	PostTextConsumeQuota(ctx, info, &dto.Usage{PromptTokens: 2001, CompletionTokens: 5, TotalTokens: 2006}, nil)
	row, evidence := readPendingDimensionEvidence(t, db, info)
	require.NotEmpty(t, evidence.Dimensions)
	require.JSONEq(t, `["settlement_retry_required"]`, row.MissingDimensionsJSON)
	var intent model.ZTAPISettlementFinalizationIntent
	require.NoError(t, db.Where("operation_id = ?", row.OperationID).Take(&intent).Error)
	require.Equal(t, intent.UsageJSON, row.UsageJSON, "diagnostics must not invalidate an immutable financial retry")
	require.Zero(t, intent.CompletedAt)
	require.NoError(t, FinishZTAPIBilling(ctx, info))
	var after model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&after, row.ID).Error)
	require.Equal(t, row.UsageJSON, after.UsageJSON)
	require.Equal(t, model.ZTAPISettlementPending, after.Status)
}

func TestZTAPIPendingDimensionEvidenceDoesNotRewriteHistoricalPending(t *testing.T) {
	db, ctx, info := setupPendingDimensionBilling(t)
	legacy := &dto.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, UsageSource: "upstream_reported"}
	raw, err := common.Marshal(legacy)
	require.NoError(t, err)
	session := info.Billing.(*ztapiDurableBilling)
	session.row, err = model.PendZTAPIRequestSettlement(session.row.OperationID, string(raw), `["web_search"]`)
	require.NoError(t, err)
	info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{BuiltInTools: map[string]*relaycommon.BuildInToolInfo{"web_search": {CallCount: 99}}}
	require.NoError(t, FinishZTAPIBilling(ctx, info))
	var row model.ZTAPIRequestSettlement
	require.NoError(t, db.First(&row, session.row.ID).Error)
	require.Equal(t, string(raw), row.UsageJSON, "new observations cannot backfill or rewrite an old pending hold")
	require.JSONEq(t, `[]`, row.ChargeDimensionsJSON)
	require.Equal(t, model.ZTAPISettlementPending, row.Status)
	var decoded dto.Usage
	require.NoError(t, common.UnmarshalJsonStr(row.UsageJSON, &decoded))
	require.Equal(t, *legacy, decoded)
}

func TestZTAPIBillingDimensionEvidenceSanitizesAndBoundsUntrustedCheck(t *testing.T) {
	check := relaycommon.ZTAPIUsageDimensionResult{Managed: true, Pending: true, UsageSemantic: "PRIVATE_SEMANTIC",
		MissingDimensions: []string{"web_search", "https://PRIVATE_HOST?key=PRIVATE_KEY"},
		InvalidDimensions: []string{"usage_total_mismatch", "PRIVATE_REASON"},
		Dimensions: []relaycommon.ZTAPIUsageDimension{
			{Dimension: "web_search", Quantity: -1, Unit: "PRIVATE_UNIT", QuoteState: "PRIVATE_STATE", UnitPriceUSD: "PRIVATE_PRICE"},
			{Dimension: "cache_read", Quantity: math.MaxInt64, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: strings.Repeat("9", 10000)},
			{Dimension: "file_search", Quantity: 3, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing, UnitPriceUSD: "PRIVATE_MISSING_PRICE"},
			{Dimension: "cache_write_5m", Quantity: 5, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.00000001"},
			{Dimension: "cache_write_1h", Quantity: 7, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateFree, UnitPriceUSD: "0"},
			{Dimension: "PRIVATE_TOOL", Quantity: 1, Unit: "call"},
			{Dimension: "file_search", Quantity: 99, Unit: "call"},
		}}
	evidence := sanitizeZTAPIBillingDimensionEvidence(check, []string{"PRIVATE_PENDING_REASON"})
	raw, err := common.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE_")
	require.Contains(t, evidence.Reasons, "invalid_quantity")
	require.Contains(t, evidence.Reasons, "invalid_unit")
	require.Contains(t, evidence.Reasons, "invalid_quote")
	require.Contains(t, evidence.Reasons, "duplicate_dimension")
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "cache_write_5m", Quantity: 5, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateQuoted, UnitPriceUSD: "0.00000001"})
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "cache_write_1h", Quantity: 7, Unit: "token", QuoteState: relaycommon.ZTAPIQuoteStateFree, UnitPriceUSD: "0"})
	require.Contains(t, evidence.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: "file_search", Quantity: 3, Unit: "call", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
	require.Equal(t, "PRIVATE_UNIT", check.Dimensions[0].Unit, "sanitization must not alter the billing check")
	require.Equal(t, "PRIVATE_SEMANTIC", check.UsageSemantic)

	check.Dimensions = nil
	for _, quality := range []string{"auto", "low", "medium", "high", "standard", "hd"} {
		for _, size := range []string{"auto", "256x256", "512x512", "1024x1024", "1536x1024", "1024x1536", "1792x1024", "1024x1792"} {
			name := "image_generation:" + quality + ":" + size
			check.MissingDimensions = append(check.MissingDimensions, name)
			check.Dimensions = append(check.Dimensions, relaycommon.ZTAPIUsageDimension{Dimension: name, Quantity: 1, Unit: "image", QuoteState: relaycommon.ZTAPIQuoteStateMissing})
		}
	}
	evidence = sanitizeZTAPIBillingDimensionEvidence(check, nil)
	require.Len(t, evidence.Dimensions, 32)
	require.Equal(t, 16, evidence.OmittedDimensions)
	require.Contains(t, evidence.Reasons, "evidence_truncated")
	require.LessOrEqual(t, len(evidence.MissingDimensions), 32)
	require.Contains(t, evidence.MissingDimensions, "evidence_truncated")
	require.Equal(t, evidence.MissingDimensions, sanitizeZTAPIBillingEvidenceReasons(evidence.MissingDimensions), "bounded evidence must remain stable when sanitized again")
	raw, err = common.Marshal(evidence)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), 16384)
}
