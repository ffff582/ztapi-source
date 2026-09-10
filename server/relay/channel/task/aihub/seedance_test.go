package aihub

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

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
