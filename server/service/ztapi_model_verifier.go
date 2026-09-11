package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
)

const (
	ztapiVerificationRequestTimeout     = 20 * time.Second
	ztapiVerificationSlowRequestTimeout = 60 * time.Second
	ztapiVerificationBodyLimit          = 1 << 20
	ztapiVerificationDefaultTokens      = 1024
)

type ztapiModelProbeResult struct {
	NonStreamingPassed            bool
	StreamingRequired             bool
	StreamingPassed               bool
	UsageReconciled               bool
	InvalidKeyClassified          bool
	InsufficientBalanceClassified bool
	RateLimitClassified           bool
	TimeoutClassified             bool
	StatusCategory                string
	LatencyMilliseconds           int64
	PromptTokens                  int
	CompletionTokens              int
	TotalTokens                   int
	MediaResultValid              bool
	ImageProtocolContractJSON     string
}

type ztapiProbeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ztapiResponsesProbeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func (u ztapiResponsesProbeUsage) normalized() ztapiProbeUsage {
	return ztapiProbeUsage{
		PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens,
	}
}

var ztapiModelVerificationProbeRunner = runZTAPIModelVerificationProbes

func ztapiVerificationMaxTokens(_ string) int {
	return ztapiVerificationDefaultTokens
}

func ztapiVerificationUsesResponses(sourceModel string) bool {
	return common.IsOpenAIResponseOnlyModel(strings.ToLower(strings.TrimSpace(sourceModel)))
}

func ztapiVerificationRequestTimeoutForModel(sourceModel string) time.Duration {
	normalized := strings.ToLower(strings.TrimSpace(sourceModel))
	if strings.HasPrefix(normalized, "gpt-5") && strings.Contains(normalized, "-pro") {
		return ztapiVerificationSlowRequestTimeout
	}
	return ztapiVerificationRequestTimeout
}

func classifyZTAPIVerificationFailure(status int, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "invalid_key"
	case http.StatusPaymentRequired:
		return "insufficient_balance"
	case http.StatusTooManyRequests:
		return "rate_limit"
	default:
		return "upstream_response"
	}
}

func ztapiVerificationEndpoint(channel *model.Channel, sourceModel string) (string, error) {
	if channel == nil {
		return "", errors.New("verification channel is nil")
	}
	baseURL := strings.TrimSpace(channel.GetBaseURL())
	if baseURL == "" && channel.Type >= 0 && channel.Type < len(constant.ChannelBaseURLs) {
		baseURL = constant.ChannelBaseURLs[channel.Type]
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("managed verification channel requires a safe HTTPS URL")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return "", errors.New("managed verification channel URL is not public")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	suffix := "/chat/completions"
	if model.ZTAPIModelModality(sourceModel) == model.ZTAPIModalityEmbedding {
		suffix = "/embeddings"
	} else if model.ZTAPIModelModality(sourceModel) == model.ZTAPIModalityImage {
		suffix = "/images/generations"
	} else if ztapiVerificationUsesResponses(sourceModel) {
		suffix = "/responses"
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + suffix, nil
	}
	return baseURL + "/v1" + suffix, nil
}

func ztapiGPTImage2ProtocolContract(requestIDHeader string) (types.ZTAPIImageProtocolContract, string, error) {
	contract := types.ZTAPIImageProtocolContract{
		Version:       types.ZTAPIImageProtocolContractVersionV2,
		ProviderModel: "gpt-image-2",
		EndpointType:  types.ZTAPIImageEndpointGeneration,
		Method:        http.MethodPost,
		Path:          "/v1/images/generations",
		WireProtocol:  types.ZTAPIImageWireProtocolOpenAIImages,
		ProviderPath:  "/v1/images/generations",
		Capabilities: types.ZTAPIImageCapabilities{
			Sizes: []string{"1024x1024"}, Qualities: []string{"low"},
			ResponseFormats: []string{"b64_json"}, MinCount: 1, MaxCount: 1,
		},
		Response: types.ZTAPIImageResponseContract{
			Schema: "object_results_array", ResultsField: "data",
			ResultFields: map[string]string{"b64_json": "b64_json"},
		},
		Usage: types.ZTAPIImageUsageContract{
			UsageField: "usage", TotalField: "total_tokens",
			Fields: map[string]string{
				"text_input": "input_tokens_details.text_tokens", "image_input": "input_tokens_details.image_tokens",
				"image_output": "output_tokens_details.image_tokens",
			},
			TotalSemantics: "sum_of_dimensions", CacheSemantics: "not_reported",
		},
		Reservations: []types.ZTAPIImageReservationAuthority{{
			Size: "1024x1024", Quality: "low", ResponseFormat: "b64_json", N: 1,
			MaximumDimensions: map[string]string{"text_input": "200000", "image_input": "0", "image_output": "196"},
		}},
		RequestIDSource: types.ZTAPIResponseIDSourceHeader, RequestIDKey: requestIDHeader,
		EvidenceVersion: types.ZTAPIImageEvidenceVersion,
		UpstreamRequestFields: map[string]string{
			"model": types.ZTAPIImageRequestFieldRequired, "prompt": types.ZTAPIImageRequestFieldRequired,
			"n": types.ZTAPIImageRequestFieldRequired, "size": types.ZTAPIImageRequestFieldRequired,
			"quality": types.ZTAPIImageRequestFieldRequired, "response_format": types.ZTAPIImageRequestFieldOmit,
		},
	}
	return types.SealZTAPIImageProtocolContract(contract)
}

func ztapiVerificationResponseIDHeader(header http.Header) (string, bool) {
	found := ""
	for _, name := range []string{"X-Request-ID", "Request-ID"} {
		values := header.Values(name)
		if len(values) == 0 {
			continue
		}
		if found != "" || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return "", false
		}
		found = name
	}
	return found, found != ""
}

func performZTAPIGPTImageProbe(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	key string,
) (ztapiProbeUsage, string, int, error) {
	payload := map[string]any{
		"model": "gpt-image-2", "prompt": "A simple blue circle centered on a white background.",
		"n": 1, "size": "1024x1024", "quality": "low",
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return ztapiProbeUsage{}, "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ztapiProbeUsage{}, "", 0, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ztapiProbeUsage{}, "", 0, err
	}
	defer response.Body.Close()
	const imageBodyLimit = 20 << 20
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, ztapiVerificationBodyLimit))
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream rejected image verification request")
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, imageBodyLimit+1))
	if err != nil || len(body) > imageBodyLimit || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(body)) != nil {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification response is incomplete or malformed")
	}
	requestIDHeader, ok := ztapiVerificationResponseIDHeader(response.Header)
	if !ok {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification response has no unambiguous request ID")
	}
	contract, canonical, err := ztapiGPTImage2ProtocolContract(requestIDHeader)
	if err != nil {
		return ztapiProbeUsage{}, "", response.StatusCode, err
	}
	root := gjson.ParseBytes(body)
	results := root.Get("data").Array()
	if len(results) != 1 || strings.TrimSpace(results[0].Get("b64_json").String()) == "" || results[0].Get("url").Exists() {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification response has an invalid result")
	}
	decoded, err := base64.StdEncoding.DecodeString(results[0].Get("b64_json").String())
	if err != nil {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification result is not valid base64")
	}
	imageConfig, format, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil || format != "png" || imageConfig.Width != 1024 || imageConfig.Height != 1024 {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification result is not the required 1024x1024 PNG")
	}
	usageRaw := []byte(root.Get("usage").Raw)
	if len(usageRaw) == 0 || relaycommon.ZTAPIGPTImage2UsagePendingReason(usageRaw, contract) != "" {
		return ztapiProbeUsage{}, "", response.StatusCode, errors.New("upstream image verification usage is not reconciled")
	}
	usage := ztapiProbeUsage{
		PromptTokens:     int(root.Get("usage.input_tokens").Int()),
		CompletionTokens: int(root.Get("usage.output_tokens").Int()),
		TotalTokens:      int(root.Get("usage.total_tokens").Int()),
	}
	return usage, canonical, response.StatusCode, nil
}

func performZTAPIOpenAIProbe(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	key string,
	sourceModel string,
	stream bool,
) (ztapiProbeUsage, int, error) {
	embedding := model.ZTAPIModelModality(sourceModel) == model.ZTAPIModalityEmbedding
	if embedding && stream {
		return ztapiProbeUsage{}, 0, errors.New("embedding verification does not support streaming")
	}
	usesResponses := ztapiVerificationUsesResponses(sourceModel)
	payload := map[string]any{"model": sourceModel, "stream": stream}
	const question = "What is 35+42? Reply with only the integer answer."
	const system = "You are a helpful assistant."
	if embedding {
		payload = map[string]any{"model": sourceModel, "input": "ZTAPI embedding verification", "encoding_format": "float"}
	} else if usesResponses {
		payload["input"] = question
		payload["instructions"] = system
		payload["max_output_tokens"] = ztapiVerificationMaxTokens(sourceModel)
	} else {
		payload["messages"] = []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": question}}
		payload["max_tokens"] = ztapiVerificationMaxTokens(sourceModel)
	}
	if stream && !usesResponses {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return ztapiProbeUsage{}, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ztapiProbeUsage{}, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return ztapiProbeUsage{}, 0, err
	}
	defer response.Body.Close()
	if embedding && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return ztapiProbeUsage{}, response.StatusCode, errors.New("embedding verification cannot accept a streaming response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, ztapiVerificationBodyLimit))
		return ztapiProbeUsage{}, response.StatusCode, errors.New("upstream rejected verification request")
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, ztapiVerificationBodyLimit+1))
	if err != nil || len(body) > ztapiVerificationBodyLimit {
		return ztapiProbeUsage{}, response.StatusCode, errors.New("upstream verification response is incomplete or too large")
	}
	if embedding {
		tokens, err := common.ValidateZTAPIEmbeddingResponse(body, 1, 1536)
		return ztapiProbeUsage{PromptTokens: tokens, TotalTokens: tokens}, response.StatusCode, err
	}
	usage, err := parseZTAPIVerificationProbe(body, usesResponses, stream)
	return usage, response.StatusCode, err
}

func ztapiUsageReconciled(usage ztapiProbeUsage) bool {
	return usage.TotalTokens > 0 && usage.PromptTokens > 0 && usage.CompletionTokens > 0 &&
		usage.CompletionTokens <= usage.TotalTokens && usage.PromptTokens == usage.TotalTokens-usage.CompletionTokens
}

func parseZTAPIVerificationProbe(body []byte, responses, stream bool) (ztapiProbeUsage, error) {
	invalid := errors.New("upstream verification requires a complete correct answer and reconciled usage")
	protocol := "chat"
	if responses {
		protocol = "responses"
	}
	// Reuse the health probe's bounded arithmetic-answer and native-terminal
	// checks. Acceptance additionally requires usage and rejects refusal metadata.
	result := parseZTAPIHealthProbe(body, protocol, stream, ztapiHealthProbeResult{})
	if !result.Complete || result.Code != "functional_pass" {
		return ztapiProbeUsage{}, invalid
	}
	if !stream {
		usage, ok := ztapiVerificationFrameUsage(gjson.ParseBytes(body), responses, false)
		if !ok || !ztapiUsageReconciled(usage) {
			return ztapiProbeUsage{}, invalid
		}
		return usage, nil
	}
	var usage ztapiProbeUsage
	var delta strings.Builder
	chatStopped := false
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), ztapiVerificationBodyLimit+1)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if line != "" || len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		data = nil
		if !responses && payload == "[DONE]" {
			continue
		}
		frame := gjson.Parse(payload)
		if responses && frame.Get("type").String() == "" {
			return ztapiProbeUsage{}, invalid
		}
		allowZeroPlaceholder := false
		if !responses {
			choices := frame.Get("choices").Array()
			for _, choice := range choices {
				if choice.Get("finish_reason").String() != "" {
					chatStopped = true
				}
			}
			allowZeroPlaceholder = !chatStopped && len(choices) > 0
		}
		frameUsage, ok := ztapiVerificationFrameUsage(frame, responses, allowZeroPlaceholder)
		if !ok {
			return ztapiProbeUsage{}, invalid
		}
		if responses && frame.Get("type").String() == "response.output_text.delta" {
			delta.WriteString(frame.Get("delta").String())
		}
		// Chat usage is authoritative only at stop or in the trailing usage frame.
		if frameUsage.TotalTokens > 0 && (responses || chatStopped) {
			usage = frameUsage
		}
	}
	if scanner.Err() != nil || !ztapiUsageReconciled(usage) || (delta.Len() > 0 && strings.TrimSpace(delta.String()) != "77") {
		return ztapiProbeUsage{}, invalid
	}
	return usage, nil
}

func ztapiVerificationFrameUsage(frame gjson.Result, responses, allowZeroPlaceholder bool) (ztapiProbeUsage, bool) {
	var usage ztapiProbeUsage
	readUsage := true
	if frame.Get("error").Type != gjson.Null {
		return usage, false
	}
	if responses {
		typ := frame.Get("type").String()
		readUsage = typ == "" || typ == "response.completed"
		if strings.Contains(typ, "refusal") || typ == "error" || typ == "response.failed" || typ == "response.incomplete" {
			return usage, false
		}
		if item := frame.Get("item"); item.Exists() && !ztapiVerificationOutputAllowed(item) {
			return usage, false
		}
		if frame.Get("part.type").String() == "refusal" {
			return usage, false
		}
		if response := frame.Get("response"); response.Exists() {
			frame = response
		}
		if frame.Get("error").Type != gjson.Null || frame.Get("incomplete_details").Type != gjson.Null {
			return usage, false
		}
		for _, item := range frame.Get("output").Array() {
			if !ztapiVerificationOutputAllowed(item) {
				return usage, false
			}
		}
	} else {
		for _, choice := range frame.Get("choices").Array() {
			for _, key := range []string{"message", "delta"} {
				message := choice.Get(key)
				if message.Get("refusal").String() != "" || len(message.Get("tool_calls").Array()) != 0 || message.Get("function_call").Type != gjson.Null {
					return usage, false
				}
			}
		}
	}
	if raw := frame.Get("usage"); readUsage && raw.Type != gjson.Null {
		if responses {
			var parsed ztapiResponsesProbeUsage
			if common.UnmarshalJsonStr(raw.Raw, &parsed) != nil {
				return usage, false
			}
			usage = parsed.normalized()
		} else if common.UnmarshalJsonStr(raw.Raw, &usage) != nil {
			return usage, false
		}
		if !responses && allowZeroPlaceholder && usage == (ztapiProbeUsage{}) {
			// Missing/null counters are not an explicit all-zero usage placeholder.
			for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
				if raw.Get(key).Type != gjson.Number {
					return usage, false
				}
			}
			return usage, true
		}
		return usage, ztapiUsageReconciled(usage)
	}
	return usage, true
}

func ztapiVerificationOutputAllowed(item gjson.Result) bool {
	if item.Get("type").String() == "reasoning" {
		return true
	}
	if item.Get("type").String() != "message" || item.Get("status").String() == "incomplete" || item.Get("status").String() == "failed" {
		return false
	}
	for _, part := range item.Get("content").Array() {
		if part.Get("type").String() != "output_text" {
			return false
		}
	}
	return true
}

func runZTAPIModelVerificationProbes(ctx context.Context, channel *model.Channel, sourceModel string) (ztapiModelProbeResult, error) {
	modality := model.ZTAPIModelModality(sourceModel)
	embedding := modality == model.ZTAPIModalityEmbedding
	result := ztapiModelProbeResult{StreamingRequired: !embedding}
	if channel.Type != constant.ChannelTypeOpenAI {
		return result, errors.New("pilot verifier currently supports OpenAI-compatible text channels only")
	}
	endpoint, err := ztapiVerificationEndpoint(channel, sourceModel)
	if err != nil {
		result.StatusCategory = "configuration"
		return result, err
	}
	key, _, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil || strings.TrimSpace(key) == "" {
		result.StatusCategory = "configuration"
		return result, errors.New("managed verification channel credential is unavailable")
	}
	client := &http.Client{Timeout: ztapiVerificationRequestTimeoutForModel(sourceModel)}
	if modality == model.ZTAPIModalityImage {
		result.StreamingRequired = false
		started := time.Now()
		usage, contractJSON, status, err := performZTAPIGPTImageProbe(ctx, client, endpoint, key)
		result.LatencyMilliseconds = time.Since(started).Milliseconds()
		if err != nil {
			result.StatusCategory = classifyZTAPIVerificationFailure(status, err)
			return result, fmt.Errorf("image verification failed: %s", result.StatusCategory)
		}
		result.NonStreamingPassed = true
		result.UsageReconciled = ztapiUsageReconciled(usage)
		result.MediaResultValid = true
		result.ImageProtocolContractJSON = contractJSON
		result.PromptTokens, result.CompletionTokens, result.TotalTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
		_, _, invalidStatus, invalidErr := performZTAPIGPTImageProbe(ctx, client, endpoint, "ztapi-deliberately-invalid-credential")
		result.InvalidKeyClassified = classifyZTAPIVerificationFailure(invalidStatus, invalidErr) == "invalid_key"
		result.InsufficientBalanceClassified = classifyZTAPIVerificationFailure(http.StatusPaymentRequired, nil) == "insufficient_balance"
		result.RateLimitClassified = classifyZTAPIVerificationFailure(http.StatusTooManyRequests, nil) == "rate_limit"
		result.TimeoutClassified = classifyZTAPIVerificationFailure(0, context.DeadlineExceeded) == "timeout"
		if !result.UsageReconciled || !result.MediaResultValid || !result.InvalidKeyClassified {
			result.StatusCategory = "verification_incomplete"
			return result, errors.New("image verification evidence is incomplete")
		}
		result.StatusCategory = "verified"
		return result, nil
	}
	started := time.Now()
	usage, status, err := performZTAPIOpenAIProbe(ctx, client, endpoint, key, sourceModel, false)
	result.LatencyMilliseconds = time.Since(started).Milliseconds()
	if err != nil {
		result.StatusCategory = classifyZTAPIVerificationFailure(status, err)
		return result, fmt.Errorf("non-streaming verification failed: %s", result.StatusCategory)
	}
	result.NonStreamingPassed = true
	result.PromptTokens = usage.PromptTokens
	result.CompletionTokens = usage.CompletionTokens
	result.TotalTokens = usage.TotalTokens
	result.UsageReconciled = ztapiUsageReconciled(usage)
	if embedding {
		result.UsageReconciled = usage.PromptTokens > 0 && usage.TotalTokens == usage.PromptTokens && usage.CompletionTokens == 0
	} else {
		streamUsage, status, err := performZTAPIOpenAIProbe(ctx, client, endpoint, key, sourceModel, true)
		if err != nil {
			result.StatusCategory = classifyZTAPIVerificationFailure(status, err)
			return result, fmt.Errorf("streaming verification failed: %s", result.StatusCategory)
		}
		result.StreamingPassed = true
		result.UsageReconciled = result.UsageReconciled && ztapiUsageReconciled(streamUsage)
	}

	_, invalidStatus, invalidErr := performZTAPIOpenAIProbe(
		ctx, client, endpoint, "ztapi-deliberately-invalid-credential", sourceModel, false,
	)
	result.InvalidKeyClassified = classifyZTAPIVerificationFailure(invalidStatus, invalidErr) == "invalid_key"
	result.InsufficientBalanceClassified = classifyZTAPIVerificationFailure(http.StatusPaymentRequired, nil) == "insufficient_balance"
	result.RateLimitClassified = classifyZTAPIVerificationFailure(http.StatusTooManyRequests, nil) == "rate_limit"
	result.TimeoutClassified = classifyZTAPIVerificationFailure(0, context.DeadlineExceeded) == "timeout"
	if !result.UsageReconciled || !result.InvalidKeyClassified {
		result.StatusCategory = "verification_incomplete"
		return result, errors.New("verification usage or error classification is incomplete")
	}
	result.StatusCategory = "verified"
	return result, nil
}

func VerifyZTAPIModel(
	ctx context.Context,
	channelID int,
	sourceModel string,
	operatorID int,
) (*model.ZTAPIModelVerification, error) {
	if model.DB == nil {
		return nil, errors.New("ZTAPI database is not initialized")
	}
	if channelID <= 0 || strings.TrimSpace(sourceModel) == "" || operatorID <= 0 {
		return nil, errors.New("verification channel, model, and operator are required")
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		return nil, err
	}
	if !channel.ZTAPIManaged {
		return nil, errors.New("verification requires a managed channel")
	}
	var config model.ZTAPIModelConfig
	if err := model.DB.Where("source_model = ?", strings.TrimSpace(sourceModel)).First(&config).Error; err != nil {
		return nil, err
	}
	if config.Protocol != model.ZTAPIProtocolOpenAICompatible {
		return nil, errors.New("pilot verifier supports mapped OpenAI-compatible text models only")
	}
	requestTimeout := ztapiVerificationRequestTimeoutForModel(config.SourceModel)
	boundedContext, cancel := context.WithTimeout(ctx, 2*requestTimeout)
	defer cancel()
	result, probeErr := ztapiModelVerificationProbeRunner(boundedContext, channel, config.SourceModel)
	verification := &model.ZTAPIModelVerification{
		Modality:      model.ZTAPIModelModality(config.SourceModel),
		ModelConfigID: config.ID, ChannelID: channel.Id, Protocol: config.Protocol,
		NonStreamingPassed: result.NonStreamingPassed,
		StreamingRequired:  result.StreamingRequired, StreamingPassed: result.StreamingPassed,
		UsageReconciled:               result.UsageReconciled,
		InvalidKeyClassified:          result.InvalidKeyClassified,
		InsufficientBalanceClassified: result.InsufficientBalanceClassified,
		RateLimitClassified:           result.RateLimitClassified, TimeoutClassified: result.TimeoutClassified,
		StatusCategory: result.StatusCategory, LatencyMilliseconds: result.LatencyMilliseconds,
		PromptTokens: result.PromptTokens, CompletionTokens: result.CompletionTokens,
		TotalTokens: result.TotalTokens, OperatorID: operatorID, VerifiedAt: time.Now().UTC().Unix(),
		MediaResultValid: result.MediaResultValid, ImageProtocolContractJSON: result.ImageProtocolContractJSON,
	}
	if err := model.DB.Create(verification).Error; err != nil {
		return nil, err
	}
	if probeErr != nil {
		return verification, fmt.Errorf("ZTAPI model verification failed: %s", verification.StatusCategory)
	}
	return verification, nil
}
