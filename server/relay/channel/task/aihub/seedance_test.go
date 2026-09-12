package aihub

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

func TestZTAPISeedanceUnavailableVariantsCannotCreatePublicationEvidence(t *testing.T) {
	fixtures := []struct {
		label        string
		providerID   string
		requestID    string
		responseBody string
	}{
		{
			label:        "Seedance 2.0 Fast",
			providerID:   "doubao-seedance-2.0-fast",
			requestID:    "req_7c971ed07668385c",
			responseBody: `{"error":{"code":"model_route_unavailable","message":"model route unavailable","param":"model","reason_codes":["no_candidate"],"request_id":"req_7c971ed07668385c","type":"service_unavailable"}}`,
		},
		{
			label:        "Seedance 2.0 Mini",
			providerID:   "doubao-seedance-2.0-mini",
			requestID:    "req_cfd6341c0748c227",
			responseBody: `{"error":{"code":"model_route_unavailable","message":"model route unavailable","param":"model","reason_codes":["no_candidate"],"request_id":"req_cfd6341c0748c227","type":"service_unavailable"}}`,
		},
	}
	entries, err := model.ZTAPIQuotationEntries()
	require.NoError(t, err)
	for _, fixture := range fixtures {
		t.Run(fixture.providerID, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			response := &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(fixture.responseBody)),
			}
			providerError := gjson.Get(fixture.responseBody, "error")
			require.Equal(t, fixture.requestID, providerError.Get("request_id").String())
			require.Equal(t, "model", providerError.Get("param").String())
			require.Equal(t, "service_unavailable", providerError.Get("type").String())
			taskID, successBody, taskErr := (&TaskAdaptor{}).DoResponse(c, response, nil)
			require.Empty(t, taskID)
			require.Nil(t, successBody)
			require.NotNil(t, taskErr)
			require.Equal(t, http.StatusServiceUnavailable, taskErr.StatusCode)
			require.Equal(t, "model_route_unavailable", taskErr.Code)
			require.Equal(t, "model route unavailable", taskErr.Message)
			require.ErrorContains(t, taskErr.Error, "model route unavailable")
			reasonEvidence, marshalErr := common.Marshal(taskErr.Data)
			require.NoError(t, marshalErr)
			require.JSONEq(t, `{"reason_codes":["no_candidate"]}`, string(reasonEvidence))
			require.Empty(t, recorder.Body.String(), "an upstream error must not emit a successful task response")

			var quoted *model.ZTAPIQuotationEntry
			for index := range entries {
				if entries[index].Label == fixture.label {
					quoted = &entries[index]
					break
				}
			}
			require.NotNil(t, quoted)
			require.Equal(t, "mapping_pending", quoted.Status)
			require.Empty(t, quoted.SourceModel)
			require.Empty(t, quoted.PublicName)
			require.Empty(t, quoted.Protocol)
			require.Empty(t, quoted.ProviderFamily)
			require.ErrorIs(t, model.ValidateZTAPIQuotationIdentity(
				quoted.Label, "", "", "", model.ZTAPIQuotationSHA256,
			), model.ErrZTAPIQuotationMappingPending)
			require.ErrorIs(t, model.ValidateZTAPIQuotationIdentity(
				fixture.providerID, "", "", "", model.ZTAPIQuotationSHA256,
			), model.ErrZTAPIQuotationModelNotQuoted)
		})
	}
}

func TestZTAPISeedanceV2CreateUsesOnlyFrozenResponseIDSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		source  string
		key     string
		headers http.Header
		body    string
		want    string
	}{
		{"mixed case header", "header", "X-Offline-Create-ID", http.Header{"x-OFFLINE-create-id": {"create-header-1"}}, `{"data":{"task_id":"task-1","status":"queued"}}`, "create-header-1"},
		{"explicit body", "body_field", "data.trace", nil, `{"data":{"task_id":"task-1","status":"queued","trace":"body-v2"}}`, "body-v2"},
		{"no guessed default", "header", "X-Offline-Create-ID", http.Header{"X-Request-Id": {"wrong"}}, `{"request_id":"wrong","data":{"task_id":"task-1"}}`, ""},
		{"empty", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {" "}}, `{"data":{"task_id":"task-1"}}`, ""},
		{"multiple values", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {"one", "two"}}, `{"data":{"task_id":"task-1"}}`, ""},
		{"case collision", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {"one"}, "x-offline-create-id": {"two"}}, `{"data":{"task_id":"task-1"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract := ztapiAIHubVideoContract(t)
			contract.Version = types.ZTAPIVideoProtocolContractVersionV2
			contract.Create.RequestIDField = ""
			contract.Create.RequestIDSource, contract.Create.RequestIDKey = tc.source, tc.key
			contract.Fetch.RequestIDField = ""
			contract.Fetch.RequestIDSource, contract.Fetch.RequestIDKey = "header", "X-Offline-Fetch-ID"
			adaptor, err := NewTaskAdaptor(contract)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			resp := &http.Response{StatusCode: http.StatusAccepted, Header: tc.headers, Body: io.NopCloser(strings.NewReader(tc.body))}
			taskID, raw, taskErr := adaptor.DoResponse(c, resp, ztapiAIHubVideoInfo(t, "https://provider.invalid"))
			if tc.want == "" {
				require.NotNil(t, taskErr)
				require.Empty(t, taskID)
				require.Empty(t, recorder.Body.String())
				return
			}
			require.Nil(t, taskErr)
			require.Equal(t, "task-1", taskID)
			require.Equal(t, tc.body, string(raw))
			require.Equal(t, tc.want, c.GetString(common.UpstreamRequestIdKey))
			require.NotContains(t, recorder.Body.String(), tc.want)
		})
	}
}

func TestZTAPISeedanceV2FetchBindsHeaderToExactResponse(t *testing.T) {
	contract := ztapiAIHubVideoContract(t)
	contract.Version = types.ZTAPIVideoProtocolContractVersionV2
	contract.Create.RequestIDField, contract.Fetch.RequestIDField = "", ""
	contract.Create.RequestIDSource, contract.Create.RequestIDKey = "header", "X-Offline-Create-ID"
	contract.Fetch.RequestIDSource, contract.Fetch.RequestIDKey = "header", "X-Offline-Fetch-ID"
	adaptor, err := NewTaskAdaptor(contract)
	require.NoError(t, err)
	const body = `{"data":{"task_id":"task-1","status":"completed","result":{"url":"https://cdn.invalid/video.mp4","resolution":"720p","duration":5}}}`
	headers := http.Header{"x-OFFLINE-fetch-id": {"fetch-header-1"}}
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	previous := client.Transport
	client.Transport = seedanceOfflineTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://provider.invalid/hub/v1/video/tasks/task-1", r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { client.Transport = previous })
	fetch := func() []byte {
		resp, err := adaptor.FetchTask("https://provider.invalid", "offline", map[string]any{"task_id": "task-1"}, "")
		require.NoError(t, err)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(raw))
		return raw
	}
	result, err := adaptor.ParseTaskResult(fetch())
	require.NoError(t, err)
	require.Equal(t, "fetch-header-1", result.UpstreamRequestID)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	_, err = adaptor.ParseTaskResult([]byte(body))
	require.Error(t, err, "a consumed header must not authenticate a later body-only event")
	fetch()
	_, err = adaptor.ParseTaskResult([]byte(strings.Replace(body, "task-1", "other-task", 1)))
	require.Error(t, err, "header evidence must be bound to the fetched body")
	headers = http.Header{"X-Request-Id": {"guessed-default"}}
	_, err = adaptor.ParseTaskResult(fetch())
	require.Error(t, err, "a missing configured header must not reuse prior evidence")
}

type seedanceOfflineTransport func(*http.Request) (*http.Response, error)

func (f seedanceOfflineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZTAPISeedancePreservesUnresolvedProviderUsageWithoutTokenMapping(t *testing.T) {
	for _, rawUsage := range []string{
		`{ "completion_tokens": 108900, "total_tokens": 108900 }`,
		`{"completion_tokens":9007199254740993,"total_tokens":"00108900"}`,
	} {
		adaptor := newZTAPIAIHubVideoAdaptor(t)
		body := `{"request_id":"fetch-raw","data":{"task_id":"task-raw","status":"completed","result":{"url":"https://cdn.invalid/video.mp4","resolution":"720p","duration":5}},"provider_result":{"volcengine":{"usage":` + rawUsage + `}}}`
		result, err := adaptor.ParseTaskResult([]byte(body))
		require.NoError(t, err)
		require.Equal(t, rawUsage, result.ResultMetadata["raw_usage_json"])
		require.Empty(t, result.UsageDimensions)
		require.Zero(t, result.CompletionTokens)
		require.Zero(t, result.TotalTokens)
	}
}

func TestZTAPISeedanceRawUsageCannotBeRelabeledAsQuotationInput(t *testing.T) {
	for _, field := range []string{"completion_tokens", "total_tokens"} {
		t.Run(field, func(t *testing.T) {
			contract := ztapiAIHubVideoContract(t)
			contract.Usage.Fields["input_tokens"] = "provider_result.volcengine.usage." + field
			adaptor, err := NewTaskAdaptor(contract)
			require.NoError(t, err)
			result, err := adaptor.ParseTaskResult([]byte(`{"request_id":"fetch-raw","data":{"task_id":"task-raw","status":"completed","result":{"url":"https://cdn.invalid/video.mp4","resolution":"720p","duration":5}},"provider_result":{"volcengine":{"usage":{"completion_tokens":108900,"total_tokens":108900}}}}`))
			require.NoError(t, err)
			require.Empty(t, result.UsageDimensions, "a field binding is not evidence of quotation semantics")
			require.Equal(t, `{"completion_tokens":108900,"total_tokens":108900}`, result.ResultMetadata["raw_usage_json"])
		})
	}
}

func TestZTAPISeedanceCreatePersistsConfiguredIDBeforeReturningSuccess(t *testing.T) {
	previousRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = previousRedis })
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "seedance.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB; _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.BalanceLedger{}, &model.ZTAPIRequestSettlement{}, &model.ZTAPIRequestAttempt{}, &model.ZTAPISettlementFinalizationIntent{}))
	user := model.User{Username: "seedance-offline", AffCode: "seedance-offline", Quota: 10000, Status: common.UserStatusEnabled}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{UserId: user.Id, KeyHash: "offline-seedance", RemainQuota: 10000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, db.Create(&token).Error)
	info := ztapiAIHubVideoInfo(t, "https://provider.invalid")
	info.UserId, info.TokenId, info.RequestId, info.ChannelId = user.Id, token.Id, "create-offline", 7
	contract := info.ZTAPIPublicationSnapshot.VideoProtocolContract.Clone()
	contract.Version = types.ZTAPIVideoProtocolContractVersionV2
	contract.Create.RequestIDField, contract.Fetch.RequestIDField = "", ""
	contract.Create.RequestIDSource, contract.Create.RequestIDKey = "header", "X-Offline-Create-ID"
	contract.Fetch.RequestIDSource, contract.Fetch.RequestIDKey = "header", "X-Offline-Fetch-ID"
	info.ZTAPIPublicationSnapshot.VideoProtocolContract = &contract
	info.ZTAPIPublicationSnapshot.PublicName = info.OriginModelName
	adaptor, err := NewTaskAdaptor(contract)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	require.Nil(t, service.PreConsumeBilling(c, 100, info))
	require.NoError(t, service.BeginZTAPIBillingAttempt(info, "/hub/v1/video/tasks"))
	// Generic transport observation has no knowledge of this contract's header.
	require.NoError(t, service.ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: 202}))
	resp := &http.Response{StatusCode: 202, Header: http.Header{"x-OFFLINE-create-id": {"configured-create-id"}}, Body: io.NopCloser(strings.NewReader(`{"data":{"task_id":"provider-task-1"}}`))}
	_, _, taskErr := adaptor.DoResponse(c, resp, info)
	require.Nil(t, taskErr)
	var attempt model.ZTAPIRequestAttempt
	require.NoError(t, db.Take(&attempt).Error)
	require.Equal(t, "configured-create-id", attempt.UpstreamRequestID)
	require.Equal(t, 202, attempt.HTTPStatus)
	require.NotContains(t, recorder.Body.String(), "configured-create-id")
	resp.Body = io.NopCloser(strings.NewReader(`{"data":{"task_id":"provider-task-1"}}`))
	resp.Header = http.Header{"X-Offline-Create-ID": {"conflicting-create-id"}}
	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	_, _, taskErr = adaptor.DoResponse(c, resp, info)
	require.NotNil(t, taskErr)
	require.Empty(t, recorder.Body.String())
}

func TestZTAPISeedanceAdapterBuildsOnlyFrozenContractFields(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewBufferString(`{"model":"public-video","prompt":"draw a calm lake","size":"720p","duration":5}`))
	c.Request.Header.Set("Content-Type", "application/json")
	info := ztapiAIHubVideoInfo(t, "https://provider.invalid")
	adaptor.Init(info)

	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
	require.Equal(t, "generate", info.Action)
	url, err := adaptor.BuildRequestURL(info)
	require.NoError(t, err)
	require.Equal(t, "https://provider.invalid/hub/v1/video/tasks", url)
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	payload, err := io.ReadAll(body)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"provider-video-exact","prompt":"draw a calm lake","resolution":"720p","duration":5}`, string(payload))

	request := httptest.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, adaptor.BuildRequestHeader(c, request, info))
	require.Equal(t, "Bearer secret-redacted", request.Header.Get("Authorization"))
	require.Equal(t, "application/json", request.Header.Get("Content-Type"))
}

func TestZTAPISeedanceAdapterRejectsSelectorsOutsideFrozenContract(t *testing.T) {
	for _, body := range []string{
		`{"prompt":"x","size":"1080p","duration":5}`,
		`{"prompt":"x","size":"720p","duration":10}`,
		`{"prompt":"x","size":"720p","duration":5,"input_reference":"https://private.invalid/video.mp4"}`,
		`{"prompt":"x","size":"720p","duration":5,"metadata":{"guessed_option":"x"}}`,
	} {
		adaptor := newZTAPIAIHubVideoAdaptor(t)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		info := ztapiAIHubVideoInfo(t, "https://provider.invalid")
		adaptor.Init(info)
		require.NotNil(t, adaptor.ValidateRequestAndSetAction(c, info), body)
	}
}

func TestZTAPISeedanceAdapterSanitizesCreateResponse(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	info := ztapiAIHubVideoInfo(t, "https://provider.invalid")
	adaptor.Init(info)
	responseBody := `{"request_id":"provider-request-secret","data":{"task_id":"provider-task-secret","status":"queued"}}`
	resp := &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(bytes.NewBufferString(responseBody))}

	taskID, raw, taskErr := adaptor.DoResponse(c, resp, info)
	require.Nil(t, taskErr)
	require.Equal(t, "provider-task-secret", taskID)
	require.JSONEq(t, responseBody, string(raw))
	require.Equal(t, "task_public_only", gjson.Get(recorder.Body.String(), "id").String())
	require.NotContains(t, recorder.Body.String(), "provider-task-secret")
	require.NotContains(t, recorder.Body.String(), "provider-request-secret")
}

func TestZTAPISeedanceAdapterFetchesAndMapsExactProviderStates(t *testing.T) {
	var fetches int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/hub/v1/video/tasks/provider-task-1", r.URL.Path)
		require.Equal(t, "Bearer secret-redacted", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"fetch-request-1","data":{"task_id":"provider-task-1","status":"completed","result":{"url":"https://cdn.invalid/result.mp4","resolution":"720p","duration":5},"usage":{"input_tokens":321}}}`))
	}))
	defer provider.Close()

	adaptor := newZTAPIAIHubVideoAdaptor(t)
	info := ztapiAIHubVideoInfo(t, provider.URL)
	adaptor.Init(info)
	resp, err := adaptor.FetchTask(provider.URL, "secret-redacted", map[string]any{"task_id": "provider-task-1"}, "")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	result, err := adaptor.ParseTaskResult(body)
	require.NoError(t, err)
	require.Equal(t, 1, fetches)
	require.Equal(t, model.TaskStatusSuccess, result.Status)
	require.Equal(t, "completed", result.ProviderStatus)
	require.Equal(t, "provider-task-1", result.UpstreamTaskID)
	require.Equal(t, "fetch-request-1", result.UpstreamRequestID)
	require.Equal(t, "https://cdn.invalid/result.mp4", result.Url)
	require.Equal(t, "321", result.UsageDimensions["input_tokens"])
	require.Equal(t, "720p", result.ResultMetadata["resolution"])
	require.Equal(t, "5", result.ResultMetadata["duration"])
}

func TestZTAPISeedanceAdapterMapsUnknownStateToUnknown(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	result, err := adaptor.ParseTaskResult([]byte(`{"request_id":"r-1","data":{"task_id":"t-1","status":"new_provider_state"}}`))
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusUnknown, result.Status)
	require.Equal(t, "new_provider_state", result.ProviderStatus)
	require.Equal(t, "t-1", result.UpstreamTaskID)
}

func TestZTAPISeedanceAdapterFailsClosedOnMissingIdentityOrResult(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	for _, body := range []string{
		`{"request_id":"r-1","data":{"status":"queued"}}`,
		`{"request_id":"r-1","data":{"task_id":"t-1","status":"completed","result":{"resolution":"720p","duration":5},"usage":{"input_tokens":1}}}`,
		`{"request_id":"r-1","data":{"task_id":"t-1","status":"failed"}}`,
	} {
		_, err := adaptor.ParseTaskResult([]byte(body))
		require.Error(t, err, body)
	}
}

func TestZTAPISeedanceAdapterConvertsManagedTaskWithoutExposingProviderResultURL(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	task := &model.Task{
		TaskID: "task_public_only", Status: model.TaskStatusSuccess, Progress: "100%",
		CreatedAt: 10, UpdatedAt: 20,
		Properties:  model.Properties{OriginModelName: "public-video"},
		PrivateData: model.TaskPrivateData{ZTAPIMediaManaged: true, ResultURL: "https://private-provider.invalid/result.mp4"},
	}

	payload, err := adaptor.ConvertToOpenAIVideo(task)
	require.NoError(t, err)
	require.Equal(t, "task_public_only", gjson.GetBytes(payload, "id").String())
	require.Equal(t, "completed", gjson.GetBytes(payload, "status").String())
	require.Equal(t, taskcommon.BuildProxyURL(task.TaskID), gjson.GetBytes(payload, "metadata.url").String())
	require.NotContains(t, string(payload), "private-provider.invalid")
}

func newZTAPIAIHubVideoAdaptor(t *testing.T) *TaskAdaptor {
	t.Helper()
	contract := ztapiAIHubVideoContract(t)
	adaptor, err := NewTaskAdaptor(contract)
	require.NoError(t, err)
	return adaptor
}

func ztapiAIHubVideoInfo(t *testing.T, baseURL string) *relaycommon.RelayInfo {
	t.Helper()
	contract := ztapiAIHubVideoContract(t)
	return &relaycommon.RelayInfo{
		OriginModelName: "public-video", ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: baseURL, ApiKey: "secret-redacted", UpstreamModelName: contract.ProviderModel,
		},
		TaskRelayInfo:            &relaycommon.TaskRelayInfo{PublicTaskID: "task_public_only"},
		ZTAPIPublicationSnapshot: &relaycommon.ZTAPIPublicationSnapshot{Modality: "video", VideoProtocolContract: &contract},
	}
}

func ztapiAIHubVideoContract(t *testing.T) types.ZTAPIVideoProtocolContract {
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
