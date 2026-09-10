package aihub

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type TaskAdaptor struct {
	contract     types.ZTAPIVideoProtocolContract
	baseURL      string
	apiKey       string
	initErr      error
	fetchBody    []byte
	fetchHeaders http.Header
}

func NewTaskAdaptor(contract types.ZTAPIVideoProtocolContract) (*TaskAdaptor, error) {
	sealed, _, err := types.SealZTAPIVideoProtocolContract(contract)
	if err != nil {
		return nil, err
	}
	return &TaskAdaptor{contract: sealed}, nil
}

func (a *TaskAdaptor) Init(info *relaycommon.RelayInfo) {
	a.fetchBody, a.fetchHeaders = nil, nil
	a.initErr = nil
	if info == nil || info.ChannelMeta == nil || info.ZTAPIPublicationSnapshot == nil || info.ZTAPIPublicationSnapshot.VideoProtocolContract == nil {
		a.initErr = errors.New("frozen AIHub video protocol contract is required")
		return
	}
	sealed, _, err := types.SealZTAPIVideoProtocolContract(*info.ZTAPIPublicationSnapshot.VideoProtocolContract)
	if err != nil {
		a.initErr = errors.New("frozen AIHub video protocol contract is invalid")
		return
	}
	if info.UpstreamModelName == "" || sealed.ProviderModel != info.UpstreamModelName {
		a.initErr = errors.New("selected upstream model does not match frozen AIHub video contract")
		return
	}
	a.contract = sealed
	a.baseURL = strings.TrimRight(info.ChannelBaseUrl, "/")
	a.apiKey = info.ApiKey
	if a.baseURL == "" || a.apiKey == "" {
		a.initErr = errors.New("AIHub video channel is incomplete")
	}
}

func (a *TaskAdaptor) ValidateRequestAndSetAction(c *gin.Context, info *relaycommon.RelayInfo) *dto.TaskError {
	if a.initErr != nil {
		return service.TaskErrorWrapperLocal(a.initErr, "video_contract_unavailable", http.StatusServiceUnavailable)
	}
	if taskErr := relaycommon.ValidateBasicTaskRequest(c, info, constant.TaskActionGenerate); taskErr != nil {
		return taskErr
	}
	request, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return service.TaskErrorWrapperLocal(err, "invalid_request", http.StatusBadRequest)
	}
	if _, err := a.selector(request); err != nil {
		return service.TaskErrorWrapperLocal(err, "video_selector_invalid", http.StatusBadRequest)
	}
	return nil
}

func (a *TaskAdaptor) EstimateBilling(*gin.Context, *relaycommon.RelayInfo) map[string]float64 {
	return nil
}

func (a *TaskAdaptor) AdjustBillingOnSubmit(*relaycommon.RelayInfo, []byte) map[string]float64 {
	return nil
}

func (a *TaskAdaptor) AdjustBillingOnComplete(*model.Task, *relaycommon.TaskInfo) int {
	return 0
}

func (a *TaskAdaptor) BuildRequestURL(*relaycommon.RelayInfo) (string, error) {
	if a.initErr != nil {
		return "", a.initErr
	}
	return a.baseURL + a.contract.Create.Path, nil
}

func (a *TaskAdaptor) BuildRequestHeader(_ *gin.Context, request *http.Request, _ *relaycommon.RelayInfo) error {
	if a.initErr != nil {
		return a.initErr
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(a.contract.Auth.Header, a.contract.Auth.Scheme+" "+a.apiKey)
	return nil
}

func (a *TaskAdaptor) BuildRequestBody(c *gin.Context, _ *relaycommon.RelayInfo) (io.Reader, error) {
	if a.initErr != nil {
		return nil, a.initErr
	}
	request, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil, err
	}
	if _, err := a.selector(request); err != nil {
		return nil, err
	}
	payload := []byte(`{}`)
	bindings := []struct {
		path  string
		value any
	}{
		{a.contract.Request.ModelField, a.contract.ProviderModel},
		{a.contract.Request.PromptField, request.Prompt},
		{a.contract.Request.ResolutionField, strings.ToLower(strings.TrimSpace(request.Size))},
		{a.contract.Request.DurationField, videoDuration(request)},
	}
	if resolution, ok := request.Metadata["resolution"].(string); ok && strings.TrimSpace(resolution) != "" {
		bindings[2].value = strings.ToLower(strings.TrimSpace(resolution))
	}
	if a.contract.Capabilities.SupportsVideoInput && strings.TrimSpace(request.InputReference) != "" {
		bindings = append(bindings, struct {
			path  string
			value any
		}{a.contract.Request.VideoInputField, strings.TrimSpace(request.InputReference)})
	}
	for _, binding := range bindings {
		payload, err = sjson.SetBytes(payload, binding.path, binding.value)
		if err != nil {
			return nil, err
		}
	}
	return bytes.NewReader(payload), nil
}

func (a *TaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	if a.initErr != nil {
		return nil, a.initErr
	}
	return channel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *TaskAdaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (string, []byte, *dto.TaskError) {
	if a.initErr != nil {
		return "", nil, service.TaskErrorWrapperLocal(a.initErr, "video_contract_unavailable", http.StatusServiceUnavailable)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, service.TaskErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if taskErr := observedAIHubVideoUnavailableError(resp.StatusCode, body); taskErr != nil {
			return "", nil, taskErr
		}
		return "", nil, service.TaskErrorWrapper(errors.New("AIHub video request failed"), "upstream_error", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) {
		return "", nil, service.TaskErrorWrapper(errors.New("AIHub video response is invalid JSON"), "invalid_response", http.StatusBadGateway)
	}
	taskID := strings.TrimSpace(gjson.GetBytes(body, a.contract.Create.TaskIDField).String())
	requestID := a.responseRequestID(a.contract.Create, body, resp.Header)
	if !validProviderIdentity(taskID) || !validProviderIdentity(requestID) {
		return "", nil, service.TaskErrorWrapper(errors.New("AIHub video response identity is missing"), "invalid_response", http.StatusBadGateway)
	}
	if err := service.RecordZTAPIMediaSubmissionIdentity(info, resp.StatusCode, requestID); err != nil {
		return "", nil, service.TaskErrorWrapperLocal(err, "media_task_identity_persistence_failed", http.StatusServiceUnavailable)
	}
	c.Set(common.UpstreamRequestIdKey, requestID)
	response := dto.NewOpenAIVideo()
	response.ID = info.PublicTaskID
	response.TaskID = info.PublicTaskID
	response.Model = info.OriginModelName
	c.JSON(http.StatusOK, response)
	return taskID, body, nil
}

func observedAIHubVideoUnavailableError(status int, body []byte) *dto.TaskError {
	if status != http.StatusServiceUnavailable || !gjson.ValidBytes(body) {
		return nil
	}
	providerError := gjson.GetBytes(body, "error")
	code := providerError.Get("code").String()
	message := providerError.Get("message").String()
	reasons := providerError.Get("reason_codes").Array()
	if code != "model_route_unavailable" || message != "model route unavailable" || len(reasons) != 1 || reasons[0].String() != "no_candidate" {
		return nil
	}
	return &dto.TaskError{
		Code:       code,
		Message:    message,
		Data:       map[string]any{"reason_codes": []string{"no_candidate"}},
		StatusCode: status,
		Error:      errors.New(message),
	}
}

func (a *TaskAdaptor) FetchTask(baseURL, key string, body map[string]any, proxy string) (*http.Response, error) {
	a.fetchBody, a.fetchHeaders = nil, nil
	if a.initErr != nil {
		return nil, a.initErr
	}
	taskID, ok := body["task_id"].(string)
	if !ok || !validProviderIdentity(taskID) {
		return nil, errors.New("invalid provider task ID")
	}
	path := strings.Replace(a.contract.Fetch.Path, "{task_id}", url.PathEscape(taskID), 1)
	request, err := http.NewRequest(a.contract.Fetch.Method, strings.TrimRight(baseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(a.contract.Auth.Header, a.contract.Auth.Scheme+" "+key)
	client, err := service.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("create proxy client: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(request)
	if err != nil || a.contract.Fetch.RequestIDSource != types.ZTAPIResponseIDSourceHeader {
		return resp, err
	}
	// The polling interface hands only bytes to ParseTaskResult. Retain a
	// single response-bound header snapshot, never a last-seen request ID.
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	a.fetchBody, a.fetchHeaders = bytes.Clone(raw), resp.Header.Clone()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

func (a *TaskAdaptor) ParseTaskResult(body []byte) (*relaycommon.TaskInfo, error) {
	fetchBody, fetchHeaders := a.fetchBody, a.fetchHeaders
	a.fetchBody, a.fetchHeaders = nil, nil
	if a.initErr != nil {
		return nil, a.initErr
	}
	if !gjson.ValidBytes(body) {
		return nil, errors.New("AIHub video task response is invalid JSON")
	}
	taskID := strings.TrimSpace(gjson.GetBytes(body, a.contract.Fetch.TaskIDField).String())
	if a.contract.Fetch.RequestIDSource == types.ZTAPIResponseIDSourceHeader && !bytes.Equal(body, fetchBody) {
		return nil, errors.New("AIHub video response header evidence does not match body")
	}
	requestID := a.responseRequestID(a.contract.Fetch, body, fetchHeaders)
	providerStatus := strings.TrimSpace(gjson.GetBytes(body, a.contract.States.Field).String())
	if !validProviderIdentity(taskID) || !validProviderIdentity(requestID) || providerStatus == "" {
		return nil, errors.New("AIHub video task response identity or status is missing")
	}
	result := &relaycommon.TaskInfo{
		Code: 0, ProviderStatus: providerStatus, UpstreamTaskID: taskID, UpstreamRequestID: requestID,
		UsageDimensions: map[string]string{}, ResultMetadata: map[string]string{},
	}
	// This observed provider usage is audit evidence, not the quotation's
	// input_tokens dimension. Keep its original JSON representation intact.
	if usage := gjson.GetBytes(body, "provider_result.volcengine.usage"); usage.Exists() {
		result.ResultMetadata["raw_usage_json"] = usage.Raw
	}
	switch {
	case contains(a.contract.States.Accepted, providerStatus):
		result.Status = model.TaskStatusQueued
		result.Progress = "10%"
	case contains(a.contract.States.Processing, providerStatus):
		result.Status = model.TaskStatusInProgress
		result.Progress = "50%"
	case contains(a.contract.States.Succeeded, providerStatus):
		result.Status = model.TaskStatusSuccess
		result.Progress = "100%"
		result.Url = strings.TrimSpace(gjson.GetBytes(body, a.contract.Result.URLField).String())
		resolution := strings.TrimSpace(gjson.GetBytes(body, a.contract.Result.ResolutionField).String())
		duration := strings.TrimSpace(gjson.GetBytes(body, a.contract.Result.DurationField).Raw)
		if result.Url == "" || resolution == "" || duration == "" {
			return nil, errors.New("AIHub video successful task result is incomplete")
		}
		result.ResultMetadata["resolution"] = resolution
		result.ResultMetadata["duration"] = duration
	case contains(a.contract.States.Failed, providerStatus):
		result.Status = model.TaskStatusFailure
		result.Progress = "100%"
		result.Reason = strings.TrimSpace(gjson.GetBytes(body, a.contract.Result.FailureReasonField).String())
		if result.Reason == "" {
			return nil, errors.New("AIHub video failed task reason is missing")
		}
	default:
		result.Status = model.TaskStatusUnknown
		result.Progress = ""
		result.Reason = "unknown_provider_state"
	}
	if _, unresolved := result.ResultMetadata["raw_usage_json"]; unresolved {
		return result, nil
	}
	for dimension, field := range a.contract.Usage.Fields {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		raw := value.Raw
		if value.Type == gjson.String {
			raw = value.String()
		}
		result.UsageDimensions[dimension] = strings.TrimSpace(raw)
	}
	return result, nil
}

func (a *TaskAdaptor) responseRequestID(endpoint types.ZTAPIVideoEndpointContract, body []byte, headers http.Header) string {
	if a.contract.Version == types.ZTAPIVideoProtocolContractVersion {
		return strings.TrimSpace(gjson.GetBytes(body, endpoint.RequestIDField).String())
	}
	if endpoint.RequestIDSource == types.ZTAPIResponseIDSourceBodyField {
		value := gjson.GetBytes(body, endpoint.RequestIDKey)
		if value.Type == gjson.String && validProviderIdentity(value.String()) {
			return value.String()
		}
		return ""
	}
	if endpoint.RequestIDSource != types.ZTAPIResponseIDSourceHeader {
		return ""
	}
	var values []string
	for name, entries := range headers {
		if strings.EqualFold(name, endpoint.RequestIDKey) {
			values = append(values, entries...)
		}
	}
	if len(values) != 1 || !validProviderIdentity(values[0]) {
		return ""
	}
	return values[0]
}

func (a *TaskAdaptor) ConvertToOpenAIVideo(task *model.Task) ([]byte, error) {
	if task == nil || !task.PrivateData.ZTAPIMediaManaged {
		return nil, errors.New("managed AIHub video task is required")
	}
	response := task.ToOpenAIVideo()
	response.Metadata = nil
	if task.Status == model.TaskStatusSuccess {
		response.SetMetadata("url", taskcommon.BuildProxyURL(task.TaskID))
	}
	if task.Status == model.TaskStatusFailure {
		response.Error = &dto.OpenAIVideoError{Code: "provider_task_failed", Message: "video generation failed"}
	}
	return common.Marshal(response)
}

func (a *TaskAdaptor) GetModelList() []string {
	if a.contract.ProviderModel == "" {
		return nil
	}
	return []string{a.contract.ProviderModel}
}

func (*TaskAdaptor) GetChannelName() string { return "ztapi-aihub-video" }

func (a *TaskAdaptor) selector(request relaycommon.TaskSubmitReq) (types.ZTAPIVideoSelector, error) {
	for key := range request.Metadata {
		if key != "resolution" {
			return types.ZTAPIVideoSelector{}, fmt.Errorf("unsupported video option %q", key)
		}
	}
	if strings.TrimSpace(request.Image) != "" || len(request.Images) > 0 {
		return types.ZTAPIVideoSelector{}, errors.New("image input is not an evidenced video input")
	}
	resolution := strings.ToLower(strings.TrimSpace(request.Size))
	if value, ok := request.Metadata["resolution"].(string); ok && strings.TrimSpace(value) != "" {
		resolution = strings.ToLower(strings.TrimSpace(value))
	}
	duration := videoDuration(request)
	selector := types.ZTAPIVideoSelector{Resolution: resolution, DurationSeconds: duration, ContainsVideoInput: strings.TrimSpace(request.InputReference) != ""}
	if _, ok := a.contract.FindReservationAuthority(selector); !ok {
		return types.ZTAPIVideoSelector{}, errors.New("video selector is not admitted by the frozen contract")
	}
	return selector, nil
}

func videoDuration(request relaycommon.TaskSubmitReq) int {
	if request.Duration > 0 {
		return request.Duration
	}
	duration, _ := strconv.Atoi(strings.TrimSpace(request.Seconds))
	return duration
}

func validProviderIdentity(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
