package controller

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestClassifyZTAPIChannelFailureUsesSafeOperationalCategories(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		statusCode  int
		want        string
		wantMessage string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "secret.example"}, 0, "dns", "DNS 解析失败"},
		{"connect timeout", contextDeadlineExceeded{}, 0, "connect_timeout", "连接上游超时"},
		{"authentication", errors.New("credential rejected SECRET_BODY"), http.StatusUnauthorized, "authentication", "上游身份验证失败"},
		{"authentication status wins over crafted timeout text", errors.New("timeout SECRET_BODY"), http.StatusUnauthorized, "authentication", "上游身份验证失败"},
		{"rate limit", errors.New("rate limit body SECRET_BODY"), http.StatusTooManyRequests, "rate_limit", "上游触发限流"},
		{"rate-limit status wins over crafted DNS text", errors.New("no such host SECRET_BODY"), http.StatusTooManyRequests, "rate_limit", "上游触发限流"},
		{"upstream response", errors.New("raw upstream body SECRET_BODY"), http.StatusBadGateway, "upstream_response", "上游响应异常"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			category, message := classifyZTAPIChannelFailure(test.err, test.statusCode)
			require.Equal(t, test.want, category)
			require.Equal(t, test.wantMessage, message)
			require.NotContains(t, message, "SECRET_BODY")
			require.NotContains(t, message, "secret.example")
		})
	}
}

func TestBuildZTAPIChannelTestFailureNeverSerializesRawErrors(t *testing.T) {
	result := testResult{
		localErr: errors.New("local failure Authorization: Bearer LOCAL_SECRET"),
		newAPIError: types.NewOpenAIError(
			errors.New("raw upstream body UPSTREAM_SECRET"),
			types.ErrorCodeBadResponse,
			http.StatusBadGateway,
		),
	}

	response := buildZTAPIChannelTestFailure(result, 1.25)
	require.Equal(t, http.StatusBadGateway, response.UpstreamStatusCode)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "LOCAL_SECRET")
	require.NotContains(t, body, "UPSTREAM_SECRET")
	require.Contains(t, body, `"error_category":"upstream_response"`)
	require.Contains(t, body, `"error_code":"bad_response"`)
	require.Contains(t, body, `"upstream_status_code":502`)
	require.Contains(t, body, `"time":1.25`)
}

func TestBuildZTAPIChannelTestFailureWhitelistsErrorCode(t *testing.T) {
	result := testResult{
		newAPIError: types.NewOpenAIError(
			errors.New("raw upstream body UPSTREAM_SECRET"),
			types.ErrorCode("attacker-controlled-UPSTREAM_SECRET"),
			http.StatusBadGateway,
		),
	}

	response := buildZTAPIChannelTestFailure(result, 0.5)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "UPSTREAM_SECRET")
	require.Contains(t, body, `"error_code":"bad_response"`)
}

func TestBuildZTAPIChannelTestFailureClassifiesLocalPricingErrorAsConfiguration(t *testing.T) {
	result := testResult{
		localErr: errors.New("local pricing failure SECRET_MODEL_NAME"),
		newAPIError: types.NewError(
			errors.New("local pricing failure SECRET_MODEL_NAME"),
			types.ErrorCodeModelPriceError,
			types.ErrOptionWithStatusCode(http.StatusBadRequest),
		),
	}

	response := buildZTAPIChannelTestFailure(result, 0.25)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "SECRET_MODEL_NAME")
	require.Contains(t, body, `"error_category":"configuration"`)
	require.Contains(t, body, `"message":"本地模型定价配置异常"`)
	require.Contains(t, body, `"error_code":"model_price_error"`)
	require.NotContains(t, body, "upstream_status_code")
	require.NotContains(t, body, "upstream_signals")
}

func TestBuildZTAPIChannelTestFailureExposesOnlyAllowlistedUpstreamDiagnostics(t *testing.T) {
	result := testResult{
		newAPIError: types.WithOpenAIError(types.OpenAIError{
			Message: "Unsupported parameter: 'max_completion_tokens'. sk-RAW_UPSTREAM_SECRET",
			Type:    "invalid_request_error",
			Param:   "max_completion_tokens",
			Code:    "unsupported_parameter",
		}, http.StatusBadRequest),
	}

	response := buildZTAPIChannelTestFailure(result, 0.25)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "RAW_UPSTREAM_SECRET")
	require.Contains(t, body, `"upstream_error_type":"invalid_request_error"`)
	require.Contains(t, body, `"upstream_error_code":"unsupported_parameter"`)
	require.Contains(t, body, `"upstream_error_param":"max_completion_tokens"`)
	require.Contains(t, body, `"upstream_issue":"max_completion_tokens_rejected"`)
}

func TestBuildZTAPIChannelTestFailureDropsUntrustedDiagnosticFields(t *testing.T) {
	result := testResult{
		newAPIError: types.WithOpenAIError(types.OpenAIError{
			Message: "raw body sk-MESSAGE_SECRET",
			Type:    "sk-TYPE_SECRET",
			Param:   "Authorization: Bearer PARAM_SECRET",
			Code:    "sk-CODE_SECRET",
		}, http.StatusBadRequest),
	}

	response := buildZTAPIChannelTestFailure(result, 0.25)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "SECRET")
	require.NotContains(t, body, "upstream_error_type")
	require.NotContains(t, body, "upstream_error_code")
	require.NotContains(t, body, "upstream_error_param")
	require.NotContains(t, body, "upstream_issue")
}

func TestBuildZTAPIChannelTestFailureReturnsOnlyFixedUpstreamSignals(t *testing.T) {
	result := testResult{
		newAPIError: types.NewOpenAIError(
			errors.New("No available channel route for model gpt-5.4-pro. sk-SIGNAL_SECRET"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusBadRequest,
		),
	}

	response := buildZTAPIChannelTestFailure(result, 0.25)
	encoded, err := common.Marshal(response)
	require.NoError(t, err)
	body := string(encoded)
	require.NotContains(t, body, "SIGNAL_SECRET")
	require.JSONEq(t, `[
		"model",
		"channel",
		"route",
		"unavailable"
	]`, string(mustJSONMarshal(t, response.UpstreamSignals)))
}

func mustJSONMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := common.Marshal(value)
	require.NoError(t, err)
	return encoded
}

func TestBuildTestRequestUsesMaxCompletionTokensForGPT5Pro(t *testing.T) {
	request, ok := buildTestRequest("gpt-5.4-pro", string(constant.EndpointTypeOpenAI), &model.Channel{}, false).(*dto.GeneralOpenAIRequest)
	require.True(t, ok)
	require.Nil(t, request.MaxTokens)
	require.NotNil(t, request.MaxCompletionTokens)
	require.Equal(t, uint(16), *request.MaxCompletionTokens)
}

func TestNormalizeChannelTestEndpointUsesResponsesForGPT5Pro(t *testing.T) {
	channel := &model.Channel{Type: constant.ChannelTypeOpenAI}
	require.Equal(t, string(constant.EndpointTypeOpenAIResponse), normalizeChannelTestEndpoint(channel, "gpt-5.4-pro", ""))
	require.Equal(t, string(constant.EndpointTypeOpenAIResponse), normalizeChannelTestEndpoint(channel, "gpt-5.4-pro-2026-03-05", ""))
	require.Empty(t, normalizeChannelTestEndpoint(channel, "gpt-5.4", ""))
}

type contextDeadlineExceeded struct{}

func (contextDeadlineExceeded) Error() string   { return "connection timed out" }
func (contextDeadlineExceeded) Timeout() bool   { return true }
func (contextDeadlineExceeded) Temporary() bool { return true }

func TestSettleTestQuotaUsesTieredBilling(t *testing.T) {
	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode:   "tiered_expr",
			ExprString:    `param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`,
			ExprHash:      billingexpr.ExprHashString(`param("stream") == true ? tier("stream", p * 3) : tier("base", p * 2)`),
			GroupRatio:    1,
			EstimatedTier: "stream",
			QuotaPerUnit:  common.QuotaPerUnit,
			ExprVersion:   1,
		},
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{"stream":true}`),
		},
	}

	quota, result := settleTestQuota(info, types.PriceData{
		ModelRatio:      1,
		CompletionRatio: 2,
	}, &dto.Usage{
		PromptTokens: 1000,
	})

	require.Equal(t, 1500, quota)
	require.NotNil(t, result)
	require.Equal(t, "stream", result.MatchedTier)
}

func TestBuildTestLogOtherInjectsTieredInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())

	info := &relaycommon.RelayInfo{
		TieredBillingSnapshot: &billingexpr.BillingSnapshot{
			BillingMode: "tiered_expr",
			ExprString:  `tier("base", p * 2)`,
		},
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	priceData := types.PriceData{
		GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 12,
		},
	}

	other := buildTestLogOther(ctx, info, priceData, usage, &billingexpr.TieredResult{
		MatchedTier: "base",
	})

	require.Equal(t, "tiered_expr", other["billing_mode"])
	require.Equal(t, "base", other["matched_tier"])
	require.NotEmpty(t, other["expr_b64"])
}

func TestResolveChannelTestUserIDUsesRequestUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("id", 2)

	userID, err := resolveChannelTestUserID(ctx)

	require.NoError(t, err)
	require.Equal(t, 2, userID)
}

func TestZTAPIChannelRejectsUnsafePersistedURLBeforeRunner(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-channel-test-ssrf.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	previousDB := model.DB
	model.DB = db
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		_ = sqlDB.Close()
	})

	unsafeURL := "https://169.254.169.254"
	channel := model.Channel{
		Name:    "persisted unsafe upstream",
		Type:    constant.ChannelTypeOpenAI,
		Status:  common.ChannelStatusEnabled,
		BaseURL: &unsafeURL,
		Models:  "gpt-test",
		Group:   "default",
	}
	require.NoError(t, db.Create(&channel).Error)

	runnerCalls := 0
	previousRunner := ztapiChannelTestRunner
	ztapiChannelTestRunner = func(*model.Channel, int, string, string, bool) testResult {
		runnerCalls++
		return testResult{}
	}
	t.Cleanup(func() {
		ztapiChannelTestRunner = previousRunner
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	ctx.Set("id", 123)
	TestZTAPIChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Zero(t, runnerCalls)
	require.Contains(t, recorder.Body.String(), `"error_category":"configuration"`)
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "169.254.169.254")
}

func setupFetchZTAPIUpstreamModelsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("ZTAPI_UPSTREAM_MASTER_KEY", "controller-discovery-test-master-key-0123456789")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ztapi-fetch-models.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(
		&model.Channel{}, &model.Ability{}, &model.ZTAPIModelConfig{},
		&model.ZTAPIAuditEvent{}, &model.ZTAPICatalogLock{},
		&model.ZTAPIDiscoverySnapshot{}, &model.ZTAPIDiscoveredModel{},
	))
	return db
}

func createFetchZTAPIChannel(t *testing.T, db *gorm.DB, withCredential bool) model.Channel {
	t.Helper()
	weight := uint(100)
	priority := int64(0)
	channel := model.Channel{
		Name: "yunxin-test", Type: constant.ChannelTypeOpenAI,
		Status: common.ChannelStatusManuallyDisabled, Group: "default",
		ZTAPIManaged: true, Weight: &weight, Priority: &priority,
	}
	if withCredential {
		channel.Key = "synthetic-upstream-test-key"
	}
	require.NoError(t, db.Create(&channel).Error)
	return channel
}

func performFetchZTAPIUpstreamModels(t *testing.T, channelID int, importModels bool) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/channel/ztapi/fetch_models/%d", channelID)
	if importModels {
		path += "?import=true"
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, path, nil)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channelID)}}
	FetchZTAPIUpstreamModels(ctx)
	return recorder
}

func TestFetchZTAPIUpstreamModelsImportsDisabledManagedChannelPrivately(t *testing.T) {
	db := setupFetchZTAPIUpstreamModelsTestDB(t)
	channel := createFetchZTAPIChannel(t, db, true)

	previousFetcher := fetchZTAPIChannelUpstreamModelIDs
	fetchZTAPIChannelUpstreamModelIDs = func(got *model.Channel) ([]string, error) {
		require.Equal(t, channel.Id, got.Id)
		require.Equal(t, "synthetic-upstream-test-key", got.Key)
		return []string{"model-b", "model-a"}, nil
	}
	t.Cleanup(func() { fetchZTAPIChannelUpstreamModelIDs = previousFetcher })

	recorder := performFetchZTAPIUpstreamModels(t, channel.Id, true)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"imported_count":2`)
	require.NotContains(t, recorder.Body.String(), "synthetic-upstream-test-key")

	var snapshots []model.ZTAPIDiscoverySnapshot
	require.NoError(t, db.Find(&snapshots).Error)
	require.Len(t, snapshots, 1)
	var abilities []model.Ability
	require.NoError(t, db.Order("model ASC").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	for _, ability := range abilities {
		require.False(t, ability.Enabled)
	}
}

func TestFetchZTAPIUpstreamModelsRejectsBlankManagedCredential(t *testing.T) {
	db := setupFetchZTAPIUpstreamModelsTestDB(t)
	channel := createFetchZTAPIChannel(t, db, false)

	fetchCalls := 0
	previousFetcher := fetchZTAPIChannelUpstreamModelIDs
	fetchZTAPIChannelUpstreamModelIDs = func(*model.Channel) ([]string, error) {
		fetchCalls++
		return []string{"model-a"}, nil
	}
	t.Cleanup(func() { fetchZTAPIChannelUpstreamModelIDs = previousFetcher })

	recorder := performFetchZTAPIUpstreamModels(t, channel.Id, true)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, fetchCalls)
	require.Contains(t, recorder.Body.String(), "凭据不可用")
}

func TestFetchZTAPIUpstreamModelsDoesNotPersistFailedDiscovery(t *testing.T) {
	db := setupFetchZTAPIUpstreamModelsTestDB(t)
	channel := createFetchZTAPIChannel(t, db, true)

	previousFetcher := fetchZTAPIChannelUpstreamModelIDs
	fetchZTAPIChannelUpstreamModelIDs = func(*model.Channel) ([]string, error) {
		return nil, errors.New("raw upstream response SECRET_PAYLOAD")
	}
	t.Cleanup(func() { fetchZTAPIChannelUpstreamModelIDs = previousFetcher })

	recorder := performFetchZTAPIUpstreamModels(t, channel.Id, true)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "SECRET_PAYLOAD")

	var count int64
	require.NoError(t, db.Model(&model.ZTAPIDiscoverySnapshot{}).Count(&count).Error)
	require.Zero(t, count)
}
