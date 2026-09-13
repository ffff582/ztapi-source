package relay

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestZTAPITaskUpstreamErrorPreservesRouteHealthCode(t *testing.T) {
	upstream := types.NewErrorWithStatusCode(
		errors.New("route is open"), types.ErrorCode("ztapi_route_temporarily_unavailable"), http.StatusServiceUnavailable,
	)
	taskErr := taskErrorFromUpstreamError(upstream, "do_request_failed", http.StatusInternalServerError)
	require.Equal(t, "ztapi_route_temporarily_unavailable", taskErr.Code)
	require.Equal(t, http.StatusServiceUnavailable, taskErr.StatusCode)
	require.True(t, taskErr.LocalError, "local health admission rejection must not auto-disable an upstream channel")
}

type quotationTaskTransport func(*http.Request) (*http.Response, error)

func (f quotationTaskTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type acceptedParseFailureTaskAdaptor struct {
	channel.TaskAdaptor
	acceptedBeforeParse bool
}

func (a *acceptedParseFailureTaskAdaptor) DoResponse(c *gin.Context, _ *http.Response, _ *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	a.acceptedBeforeParse = relaycommon.ZTAPITaskProviderAccepted(c)
	return "", nil, service.TaskErrorWrapper(errors.New("accepted response missing task id"), "invalid_response", http.StatusBadGateway)
}

func TestZTAPIManagedTaskMarksProviderAcceptedBeforeParsingResponse(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: model.ZTAPIModalityVideo}}
	adaptor := &acceptedParseFailureTaskAdaptor{}
	response := &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"accepted":true}`))}

	_, _, taskErr := processTaskSubmissionResponse(c, info, adaptor, response)

	require.NotNil(t, taskErr)
	require.True(t, adaptor.acceptedBeforeParse, "a provider-accepted task must become non-retryable before response parsing")
	require.True(t, relaycommon.ZTAPITaskProviderAccepted(c))
}

func setupQuotationTaskDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "quotation-task.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Task{}, &model.Channel{}, &model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}, &model.ZTAPIHealthState{}))
	previous := model.DB
	model.DB = db
	model.InvalidateZTAPIAliasCache()
	t.Cleanup(func() {
		model.DB = previous
		model.InvalidateZTAPIAliasCache()
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func seedQuotationTaskPublication(t *testing.T, db *gorm.DB, source, alias string) {
	t.Helper()
	config := model.ZTAPIModelConfig{SourceModel: source, PublicName: &alias,
		Protocol: model.ZTAPIProtocolOpenAICompatible, ProviderFamily: model.ZTAPIProviderOpenAI,
		Family: model.ZTAPIModelFamilyOpenAI, Published: true, Version: 1, EnabledGroups: `["default"]`}
	require.NoError(t, db.Create(&config).Error)
	price := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: source, ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1", OutputPerMillion: "2", CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0", ImageUnitCost: "0", AudioUnitCost: "0",
		RequestUnitCost: "0", CNYPerUSD: "0", QuotationEffectiveAt: 1,
		SourceDocumentChecksum: model.ZTAPIQuotationSHA256, OperatorID: 1, Version: 1, CreatedAt: 1,
	}
	require.NoError(t, db.Create(&price).Error)
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version, SourceModel: source, PublicName: alias,
		Protocol: config.Protocol, ProviderFamily: config.ProviderFamily, EnabledGroups: config.EnabledGroups,
		AllowedChannelIDs: `[11]`, PriceSourceID: price.ID,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 1, CacheCreationRatio: 1, CacheCreation5mRatio: 1, CacheCreation1hRatio: 1,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1,
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error)
	model.InvalidateZTAPIAliasCache()
}

func quotationTaskContext(t *testing.T, path, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeSora)
	common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, "https://offline.invalid")
	return c
}

func TestZTAPIQuotationTaskOriginRejectsBeforeChannelKey(t *testing.T) {
	db := setupQuotationTaskDB(t)
	seedQuotationTaskPublication(t, db, "gpt-5.5", "zt-gpt-5.5")
	_, err := model.GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err, "text fixture must be a valid runtime publication")
	seedQuotationTaskPublication(t, db, "unquoted-video", "zt-unquoted-video")
	cases := []struct{ name, origin, upstream, data, supplied, code string }{
		{"legacy_unquoted", "zt-unquoted-video", "", `{}`, "", "quotation_task_not_authorized"},
		{"published_text", "zt-gpt-5.5", "", `{}`, "", "quotation_task_modality_mismatch"},
		{"upstream_fallback", "", "gpt-5.5", `{}`, "", "quotation_task_not_authorized"},
		{"data_fallback", "", "", `{"model":"zt-gpt-5.5"}`, "", "quotation_task_modality_mismatch"},
		{"missing_model", "", "", `{}`, "", "quotation_task_not_authorized"},
		{"supplied_text", "unquoted-video", "", `{}`, "zt-gpt-5.5", "quotation_task_modality_mismatch"},
	}
	entries, err := model.ZTAPIQuotationEntries()
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Status == "mapping_pending" {
			cases = append(cases, struct{ name, origin, upstream, data, supplied, code string }{
				entry.Label, entry.Label, "", `{}`, "", "quotation_task_not_authorized"})
			seedQuotationTaskPublication(t, db, entry.Label, "zt-pending-"+entry.Label)
			cases = append(cases, struct{ name, origin, upstream, data, supplied, code string }{
				"legacy_" + entry.Label, "zt-pending-" + entry.Label, "", `{}`, "", "quotation_task_not_authorized"})
		}
	}
	channelReads := 0
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("quotation_channel_read", func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" {
			channelReads++
		}
	}))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := model.Task{TaskID: tc.name, UserId: 7, ChannelId: 11,
				Properties: model.Properties{OriginModelName: tc.origin, UpstreamModelName: tc.upstream}, Data: []byte(tc.data)}
			require.NoError(t, db.Create(&task).Error)
			c := quotationTaskContext(t, "/v1/videos/old/remix", `{"prompt":"test"}`)
			c.Params = gin.Params{{Key: "video_id", Value: tc.name}}
			info := &relaycommon.RelayInfo{UserId: 7, OriginModelName: tc.supplied,
				ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
			channelReads = 0
			err := ResolveOriginTask(c, info)
			require.NotNil(t, err)
			require.Equal(t, tc.code, err.Code)
			require.Equal(t, http.StatusForbidden, err.StatusCode)
			require.Zero(t, channelReads)
			require.Nil(t, info.LockedChannel)
			require.Empty(t, info.ApiKey)
			require.Empty(t, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
			require.Nil(t, info.Billing)
			require.False(t, info.ForcePreConsume)
		})
	}
}

func TestZTAPIQuotationTaskSubmitRejectsBeforeBillingAndOutbound(t *testing.T) {
	db := setupQuotationTaskDB(t)
	seedQuotationTaskPublication(t, db, "gpt-5.5", "zt-gpt-5.5")
	_, err := model.GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err)
	if service.GetHttpClient() == nil {
		service.InitHttpClient()
	}
	client := service.GetHttpClient()
	previous := client.Transport
	calls := 0
	client.Transport = quotationTaskTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("offline test: outbound forbidden")
	})
	t.Cleanup(func() { client.Transport = previous })
	previousPrices, err := common.Marshal(ratio_setting.GetModelPriceCopy())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(previousPrices))) })
	writes := 0
	countWrites := func(*gorm.DB) { writes++ }
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("quotation_create", countWrites))
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("quotation_update", countWrites))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("quotation_delete", countWrites))
	cases := []struct{ name, model, code string }{
		{"unquoted", "unquoted-video", "quotation_task_not_authorized"},
		{"text_alias", "zt-gpt-5.5", "quotation_task_modality_mismatch"},
		{"direct_source", "gpt-5.5", "quotation_task_not_authorized"},
		{"near_alias", " zt-gpt-5.5 ", "quotation_task_not_authorized"},
		{"derived_from_action", "", "quotation_task_not_authorized"},
	}
	entries, err := model.ZTAPIQuotationEntries()
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Status == "mapping_pending" {
			cases = append(cases, struct{ name, model, code string }{entry.Label, entry.Label, "quotation_task_not_authorized"})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prices, marshalErr := common.Marshal(map[string]float64{tc.model: 0.1, "55_remix": 0.1})
			require.NoError(t, marshalErr)
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(prices)))
			c := quotationTaskContext(t, "/v1/videos/old/remix", `{"prompt":"test"}`)
			info := &relaycommon.RelayInfo{OriginModelName: tc.model,
				TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: constant.TaskActionRemix},
				UsingGroup:    "default", UserGroup: "default"}
			result, err := RelayTaskSubmit(c, info)
			require.Nil(t, result)
			require.NotNil(t, err)
			require.Equal(t, tc.code, err.Code)
			require.Equal(t, http.StatusForbidden, err.StatusCode)
			require.Nil(t, info.Billing)
			require.False(t, info.ForcePreConsume)
			require.Zero(t, info.FinalPreConsumedQuota)
			require.Empty(t, info.PublicTaskID)
			require.Zero(t, calls)
			require.Zero(t, writes)
		})
	}
}

func TestZTAPIQuotationTaskRechecksPublicationInsteadOfTrustingCarriedSnapshot(t *testing.T) {
	db := setupQuotationTaskDB(t)
	seedQuotationTaskPublication(t, db, "gpt-5.5", "zt-gpt-5.5")
	publication, err := model.GetZTAPIRuntimePublication("zt-gpt-5.5")
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.ZTAPIModelConfig{}).Where("id = ?", publication.ModelConfigID).
		Update("published", false).Error)
	c := quotationTaskContext(t, "/v1/videos/old/remix", `{"prompt":"test"}`)
	info := &relaycommon.RelayInfo{OriginModelName: publication.PublicName,
		TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: constant.TaskActionRemix},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{
			PublicName: publication.PublicName, SourceModel: publication.SourceModel,
			PublicationID: publication.ModelConfigID, Version: publication.Version,
		}}
	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	require.Equal(t, "quotation_task_not_authorized", taskErr.Code)
	require.Nil(t, info.Billing)
	require.False(t, info.ForcePreConsume)
	require.Empty(t, info.PublicTaskID)
}

func TestZTAPIQuotationTaskRuntimeFailureIsSanitized(t *testing.T) {
	db := setupQuotationTaskDB(t)
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("quotation_read_error", func(tx *gorm.DB) {
		tx.AddError(errors.New("sensitive database detail https://private.invalid/token"))
	}))
	c := quotationTaskContext(t, "/v1/videos/old/remix", `{"prompt":"test"}`)
	info := &relaycommon.RelayInfo{OriginModelName: "zt-gpt-5.5",
		TaskRelayInfo: &relaycommon.TaskRelayInfo{Action: constant.TaskActionRemix}}
	_, taskErr := RelayTaskSubmit(c, info)
	require.NotNil(t, taskErr)
	require.Equal(t, "quotation_task_not_authorized", taskErr.Code)
	require.NotContains(t, taskErr.Message, "sensitive")
	require.NotContains(t, taskErr.Message, "private.invalid")
	require.NotContains(t, taskErr.Error.Error(), "sensitive")
	require.Nil(t, info.Billing)
	require.False(t, info.ForcePreConsume)
}

func TestTaskSubmissionAcceptsEverySuccessfulHTTPStatus(t *testing.T) {
	for status := 200; status < 300; status++ {
		require.True(t, isTaskSubmissionAcceptedStatus(status), status)
	}
	for _, status := range []int{0, 199, 300, 400, 500} {
		require.False(t, isTaskSubmissionAcceptedStatus(status), status)
	}
}

func TestZTAPIManagedTaskRealtimeFetchCannotBypassDurableStateMachine(t *testing.T) {
	task := &model.Task{
		TaskID: "task_public", Status: model.TaskStatusSubmitted, Progress: taskcommon.ProgressSubmitted,
		PrivateData: model.TaskPrivateData{ZTAPIMediaManaged: true, ZTAPIMediaSettlementID: 42, UpstreamTaskID: "upstream-private"},
	}
	require.Nil(t, tryRealtimeFetch(task, false))
	require.Equal(t, model.TaskStatus(model.TaskStatusSubmitted), task.Status)
	require.Equal(t, taskcommon.ProgressSubmitted, task.Progress)
}

func TestZTAPITaskResponseBufferDefersCustomerCommit(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	buffer := newZTAPITaskResponseBuffer(c.Writer)
	c.Writer = buffer
	c.JSON(http.StatusOK, gin.H{"id": "task_public"})

	require.Empty(t, recorder.Body.String())
	require.NoError(t, buffer.Commit())
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"id":"task_public"}`, recorder.Body.String())
}
