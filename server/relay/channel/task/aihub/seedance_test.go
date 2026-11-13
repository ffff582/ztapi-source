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

func TestZTAPISeedanceQuotationUsesOnlyPaidVerifiedProviderIDs(t *testing.T) {
	fixtures := []struct{ label, providerID, publicName string }{
		{"seedance-2.0", "doubao-seedance-2.0", "zt-seedance-2.0"},
		{"Seedance 2.0 Fast", "doubao-seedance-2-0-fast", "zt-seedance-2.0-fast"},
		{"Seedance 2.0 Mini", "doubao-seedance-2-0-mini", "zt-seedance-2.0-mini"},
	}
	entries, err := model.ZTAPIQuotationEntries()
	require.NoError(t, err)
	for _, fixture := range fixtures {
		t.Run(fixture.providerID, func(t *testing.T) {
			var quoted *model.ZTAPIQuotationEntry
			for index := range entries {
				if entries[index].Label == fixture.label {
					quoted = &entries[index]
					break
				}
			}
			require.NotNil(t, quoted)
			require.Equal(t, "mapped", quoted.Status)
			require.Equal(t, fixture.providerID, quoted.SourceModel)
			require.Equal(t, fixture.publicName, quoted.PublicName)
			require.Equal(t, model.ZTAPIProtocolOpenAICompatible, quoted.Protocol)
			require.Equal(t, model.ZTAPIProviderSeedance, quoted.ProviderFamily)
			require.NoError(t, model.ValidateZTAPIQuotationIdentity(
				fixture.providerID, fixture.publicName, model.ZTAPIProtocolOpenAICompatible,
				model.ZTAPIProviderSeedance, model.ZTAPIQuotationSHA256,
			))
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
		{"mixed case header", "header", "X-Offline-Create-ID", http.Header{"x-OFFLINE-create-id": {"create-header-1"}}, `{"id":"task-1","status":"queued"}`, "create-header-1"},
		{"explicit body", "body_field", "trace", nil, `{"id":"task-1","status":"queued","trace":"body-v2"}`, "body-v2"},
		{"no guessed default", "header", "X-Offline-Create-ID", http.Header{"X-Request-Id": {"wrong"}}, `{"request_id":"wrong","id":"task-1"}`, ""},
		{"empty", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {" "}}, `{"id":"task-1"}`, ""},
		{"multiple values", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {"one", "two"}}, `{"id":"task-1"}`, ""},
		{"case collision", "header", "X-Offline-Create-ID", http.Header{"X-Offline-Create-ID": {"one"}, "x-offline-create-id": {"two"}}, `{"id":"task-1"}`, ""},
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
	const body = `{"id":"task-1","status":"completed","provider_result":{"volcengine":{"content":{"video_url":"https://cdn.invalid/video.mp4"},"resolution":"720p","duration":5,"usage":{"completion_tokens":1,"total_tokens":1}}}}`
	headers := http.Header{"x-OFFLINE-fetch-id": {"fetch-header-1"}}
	client := service.GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	previous := client.Transport
	client.Transport = seedanceOfflineTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https://provider.invalid/hub/v1/videos/task-1", r.URL.String())
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

func TestZTAPISeedanceMapsConfirmedQuotationTokensAndPreservesRawUsage(t *testing.T) {
	adaptor := newZTAPIAIHubVideoBodyIDAdaptor(t)
	body := `{"request_id":"fetch-raw","id":"task-raw","status":"completed","provider_result":{"volcengine":{"content":{"video_url":"https://cdn.invalid/video.mp4"},"resolution":"720p","duration":5,"usage":{"completion_tokens":108900,"total_tokens":108900}}}}`
	result, err := adaptor.ParseTaskResult([]byte(body))
	require.NoError(t, err)
	require.Equal(t, "108900", result.UsageDimensions["input_tokens"])
	require.Equal(t, `{"completion_tokens":108900,"total_tokens":108900}`, result.ResultMetadata["raw_usage_json"])
}

func TestZTAPISeedanceRejectsNonCanonicalOrMissingConfirmedUsage(t *testing.T) {
	for _, usage := range []string{
		`{"total_tokens":108900}`,
		`{"completion_tokens":"108900","total_tokens":108900}`,
		`{"completion_tokens":-1,"total_tokens":-1}`,
		`{"completion_tokens":108900.5,"total_tokens":108900.5}`,
	} {
		adaptor := newZTAPIAIHubVideoBodyIDAdaptor(t)
		body := `{"request_id":"fetch-raw","id":"task-raw","status":"completed","provider_result":{"volcengine":{"content":{"video_url":"https://cdn.invalid/video.mp4"},"resolution":"720p","duration":5,"usage":` + usage + `}}}`
		result, err := adaptor.ParseTaskResult([]byte(body))
		require.NoError(t, err)
		require.Empty(t, result.UsageDimensions)
		require.Equal(t, usage, result.ResultMetadata["raw_usage_json"])
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
	require.NoError(t, service.BeginZTAPIBillingAttempt(info, "/hub/v1/videos"))
	// Generic transport observation has no knowledge of this contract's header.
	require.NoError(t, service.ObserveZTAPIBillingResponse(info, &http.Response{StatusCode: 202}))
	resp := &http.Response{StatusCode: 202, Header: http.Header{"x-OFFLINE-create-id": {"configured-create-id"}}, Body: io.NopCloser(strings.NewReader(`{"id":"provider-task-1"}`))}
	_, _, taskErr := adaptor.DoResponse(c, resp, info)
	require.Nil(t, taskErr)
	var attempt model.ZTAPIRequestAttempt
	require.NoError(t, db.Take(&attempt).Error)
	require.Equal(t, "configured-create-id", attempt.UpstreamRequestID)
	require.Equal(t, 202, attempt.HTTPStatus)
	require.NotContains(t, recorder.Body.String(), "configured-create-id")
	resp.Body = io.NopCloser(strings.NewReader(`{"id":"provider-task-1"}`))
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
	require.Equal(t, "https://provider.invalid/hub/v1/videos", url)
	body, err := adaptor.BuildRequestBody(c, info)
	require.NoError(t, err)
	payload, err := io.ReadAll(body)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"provider-video-exact","prompt":"draw a calm lake","provider_options":{"volcengine":{"resolution":"720p","duration":5}}}`, string(payload))

	request := httptest.NewRequest(http.MethodPost, url, nil)
	require.NoError(t, adaptor.BuildRequestHeader(c, request, info))
	require.Equal(t, "Bearer secret-redacted", request.Header.Get("Authorization"))
	require.Equal(t, "application/json", request.Header.Get("Content-Type"))
}

func TestZTAPISeedanceAdapterAcceptsPublishedAliasBeforeModelMapping(t *testing.T) {
	adaptor := newZTAPIAIHubVideoAdaptor(t)
	info := ztapiAIHubVideoInfo(t, "https://provider.invalid")
	contract := info.ZTAPIPublicationSnapshot.VideoProtocolContract
	info.OriginModelName = "zt-seedance-2.0"
	info.UpstreamModelName = info.OriginModelName
	info.ZTAPIPublicationSnapshot.PublicName = info.OriginModelName
	info.ZTAPIPublicationSnapshot.SourceModel = contract.ProviderModel

	adaptor.Init(info)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", bytes.NewBufferString(`{"model":"zt-seedance-2.0","prompt":"test","size":"720p","duration":5}`))
	c.Request.Header.Set("Content-Type", "application/json")
	require.Nil(t, adaptor.ValidateRequestAndSetAction(c, info))
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
	responseBody := `{"id":"provider-task-secret","status":"queued"}`
	resp := &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"X-Request-Id": {"provider-request-secret"}}, Body: io.NopCloser(bytes.NewBufferString(responseBody))}

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
		require.Equal(t, "/hub/v1/videos/provider-task-1", r.URL.Path)
		require.Equal(t, "Bearer secret-redacted", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "fetch-request-1")
		_, _ = w.Write([]byte(`{"id":"provider-task-1","status":"completed","provider_result":{"volcengine":{"content":{"video_url":"https://cdn.invalid/result.mp4"},"resolution":"720p","duration":5,"usage":{"completion_tokens":321,"total_tokens":321}}}}`))
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
	adaptor := newZTAPIAIHubVideoBodyIDAdaptor(t)
	result, err := adaptor.ParseTaskResult([]byte(`{"request_id":"r-1","id":"t-1","status":"new_provider_state"}`))
	require.NoError(t, err)
	require.Equal(t, model.TaskStatusUnknown, result.Status)
	require.Equal(t, "new_provider_state", result.ProviderStatus)
	require.Equal(t, "t-1", result.UpstreamTaskID)
}

func TestZTAPISeedanceAdapterFailsClosedOnMissingIdentityOrResult(t *testing.T) {
	adaptor := newZTAPIAIHubVideoBodyIDAdaptor(t)
	for _, body := range []string{
		`{"request_id":"r-1","status":"queued"}`,
		`{"request_id":"r-1","id":"t-1","status":"completed","provider_result":{"volcengine":{"resolution":"720p","duration":5,"usage":{"completion_tokens":1}}}}`,
		`{"request_id":"r-1","id":"t-1","status":"failed"}`,
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

func newZTAPIAIHubVideoBodyIDAdaptor(t *testing.T) *TaskAdaptor {
	t.Helper()
	contract := ztapiAIHubVideoContract(t)
	contract.Create.RequestIDSource, contract.Create.RequestIDKey = types.ZTAPIResponseIDSourceBodyField, "request_id"
	contract.Fetch.RequestIDSource, contract.Fetch.RequestIDKey = types.ZTAPIResponseIDSourceBodyField, "request_id"
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
		Version: types.ZTAPIVideoProtocolContractVersionV2, Provider: "aihub", ProviderModel: "provider-video-exact",
		Auth:            types.ZTAPIVideoAuthContract{Method: "header", Header: "Authorization", Scheme: "Bearer"},
		Create:          types.ZTAPIVideoEndpointContract{Method: "POST", Path: "/hub/v1/videos", TaskIDField: "id", RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-Id"},
		Fetch:           types.ZTAPIVideoEndpointContract{Method: "GET", Path: "/hub/v1/videos/{task_id}", TaskIDField: "id", RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: "X-Request-Id"},
		Callback:        types.ZTAPIVideoCallbackContract{Enabled: false},
		Request:         types.ZTAPIVideoRequestContract{ModelField: "model", PromptField: "prompt", ResolutionField: "provider_options.volcengine.resolution", DurationField: "provider_options.volcengine.duration"},
		Capabilities:    types.ZTAPIVideoCapabilities{Resolutions: []string{"720p"}, DurationSeconds: []int{5}, SupportsVideoInput: false},
		States:          types.ZTAPIVideoStateContract{Field: "status", Accepted: []string{"queued"}, Processing: []string{"in_progress"}, Succeeded: []string{"completed"}, Failed: []string{"failed"}},
		Result:          types.ZTAPIVideoResultContract{URLField: "provider_result.volcengine.content.video_url", ResolutionField: "provider_result.volcengine.resolution", DurationField: "provider_result.volcengine.duration", FailureReasonField: "error.message"},
		Usage:           types.ZTAPIVideoUsageContract{Fields: map[string]string{"input_tokens": "provider_result.volcengine.usage.completion_tokens"}},
		Reservations:    []types.ZTAPIVideoReservationAuthority{{Resolution: "720p", DurationSeconds: 5, ContainsVideoInput: false, MaximumDimensions: map[string]string{"input_tokens": "250000"}}},
		EvidenceVersion: types.ZTAPIVideoEvidenceVersion,
	}
	sealed, _, err := types.SealZTAPIVideoProtocolContract(contract)
	require.NoError(t, err)
	return sealed
}
