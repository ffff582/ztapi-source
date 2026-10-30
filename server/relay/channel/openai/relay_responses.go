package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	usage := &dto.Usage{}
	if observed := relaycommon.ObserveZTAPIOpenAIUsage(c, info, responseBody); observed != nil {
		usage = observed
		relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, responseBody, "nonstream")
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}
	if (info == nil || info.ZTAPIPublicationSnapshot == nil) && responsesResponse.Usage != nil {
		usage.PromptTokens = responsesResponse.Usage.InputTokens
		usage.CompletionTokens = responsesResponse.Usage.OutputTokens
		usage.TotalTokens = responsesResponse.Usage.TotalTokens
		if responsesResponse.Usage.InputTokensDetails != nil {
			usage.PromptTokensDetails.CachedTokens = responsesResponse.Usage.InputTokensDetails.CachedTokens
		}
	}
	if responsesResponse.HasImageGenerationCall() {
		c.Set("image_generation_call", true)
		c.Set("image_generation_call_quality", responsesResponse.GetQuality())
		c.Set("image_generation_call_size", responsesResponse.GetSize())
	}
	captureNativeHostedCalls(info, responsesResponse.Output, make(map[string]string))

	// 解析 Tools 用量
	if info != nil && info.ZTAPIPublicationSnapshot == nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
		for _, tool := range responsesResponse.Tools {
			toolType := common.Interface2String(tool["type"])
			// Client-executed tools consume model tokens, not hosted-tool call fees.
			if toolType == "function" || toolType == "custom" {
				continue
			}
			buildToolinfo, ok := info.ResponsesUsageInfo.BuiltInTools[toolType]
			if !ok || buildToolinfo == nil {
				logger.LogError(c, fmt.Sprintf("BuiltInTools not found for tool type: %v", tool["type"]))
				continue
			}
			buildToolinfo.CallCount++
		}
	}
	if apiErr := nativeResponsesFailure(&responsesResponse); apiErr != nil {
		return usage, apiErr
	}
	if c.Writer != nil {
		for k, v := range resp.Header {
			if service.ShouldCopyUpstreamHeader(c, k, v) {
				c.Writer.Header().Set(k, v[0])
			}
		}
		c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(responseBody)))
		c.Writer.WriteHeader(resp.StatusCode)
		n, err := c.Writer.Write(responseBody)
		if err == nil && n != len(responseBody) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = helper.FlushWriter(c)
		}
		if err != nil {
			return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
	}
	return usage, nil
}

func captureNativeHostedCalls(info *relaycommon.RelayInfo, output []dto.ResponsesOutput, seen map[string]string) {
	if info == nil || info.ZTAPIPublicationSnapshot == nil {
		return
	}
	if info.ResponsesUsageInfo == nil {
		info.ResponsesUsageInfo = &relaycommon.ResponsesUsageInfo{}
	}
	if info.ResponsesUsageInfo.BuiltInTools == nil {
		info.ResponsesUsageInfo.BuiltInTools = make(map[string]*relaycommon.BuildInToolInfo)
	}
	for _, item := range output {
		name := ""
		switch item.Type {
		case "function_call", "custom_tool_call":
			continue
		case "file_search_call":
			name = "file_search"
		case "web_search_call":
			name = "web_search_preview"
			if _, ok := info.ResponsesUsageInfo.BuiltInTools["web_search"]; ok {
				name = "web_search"
			}
		default:
			if !strings.HasSuffix(item.Type, "_call") {
				continue
			}
			name = "responses_hosted_usage_unconfirmed"
		}
		identity, state := "state\x00"+item.ID, item.Type+"\x00"+item.Status
		if prior, ok := seen[identity]; item.ID != "" && ok {
			if prior == state {
				continue
			}
			name = "responses_hosted_usage_unconfirmed"
		}
		if item.ID == "" || item.Status != "completed" {
			name = "responses_hosted_usage_unconfirmed"
		}
		if name == "responses_hosted_usage_unconfirmed" {
			marker := "unconfirmed\x00" + item.ID
			if _, exists := seen[marker]; exists {
				continue
			}
			seen[marker] = "1"
		}
		if _, exists := seen[identity]; !exists {
			seen[identity] = state
		}
		tool := info.ResponsesUsageInfo.BuiltInTools[name]
		if tool == nil {
			tool = &relaycommon.BuildInToolInfo{ToolName: name}
			info.ResponsesUsageInfo.BuiltInTools[name] = tool
		}
		tool.CallCount++
	}
}

func nativeResponsesFailure(response *dto.OpenAIResponsesResponse) *types.NewAPIError {
	if response == nil {
		return nil
	}
	if oaiError := response.GetOpenAIError(); oaiError != nil && (oaiError.Type != "" || oaiError.Message != "" || oaiError.Code != nil) {
		return types.WithOpenAIError(*oaiError, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	var status string
	_ = common.Unmarshal(response.Status, &status)
	if status == "failed" || status == "incomplete" {
		return types.NewOpenAIError(fmt.Errorf("responses ended with status %s", status), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	return nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, "invalid response or response body")
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var usage = &dto.Usage{}
	var responseTextBuilder strings.Builder
	var streamErr, downstreamErr *types.NewAPIError
	var reportedUsage, completed bool
	seenHosted := make(map[string]string)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var event struct {
			Type string `json:"type"`
		}
		if err := common.UnmarshalJsonStr(data, &event); err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			sr.Error(err)
			return
		}
		// This provider extension is opaque, not an official Responses event or receipt.
		if event.Type == "response.metadata" {
			return
		}
		if observed := relaycommon.ObserveZTAPIOpenAIUsage(c, info, []byte(data)); observed != nil {
			usage, reportedUsage = observed, true
			relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, []byte(data), "stream")
		}
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			logger.LogError(c, "failed to unmarshal stream response: "+err.Error())
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			sr.Error(err)
			return
		}
		if streamResponse.Response != nil {
			captureNativeHostedCalls(info, streamResponse.Response.Output, seenHosted)
		}
		switch streamResponse.Type {
		case "response.completed":
			if streamResponse.Response != nil {
				if info.ZTAPIPublicationSnapshot == nil && streamResponse.Response.Usage != nil {
					reportedUsage = true
					usage.PromptTokens = streamResponse.Response.Usage.InputTokens
					usage.CompletionTokens = streamResponse.Response.Usage.OutputTokens
					usage.TotalTokens = streamResponse.Response.Usage.TotalTokens
					if streamResponse.Response.Usage.InputTokensDetails != nil {
						usage.PromptTokensDetails.CachedTokens = streamResponse.Response.Usage.InputTokensDetails.CachedTokens
					}
				}
				if streamResponse.Response.HasImageGenerationCall() {
					c.Set("image_generation_call", true)
					c.Set("image_generation_call_quality", streamResponse.Response.GetQuality())
					c.Set("image_generation_call_size", streamResponse.Response.GetSize())
				}
			}
			if apiErr := nativeResponsesFailure(streamResponse.Response); apiErr != nil {
				streamErr = apiErr
				sr.Stop(apiErr)
				return
			}
			var status string
			if streamResponse.Response == nil || (len(streamResponse.Response.Status) > 0 && (common.Unmarshal(streamResponse.Response.Status, &status) != nil || status != "completed")) {
				streamErr = types.NewOpenAIError(fmt.Errorf("invalid responses completion"), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
				sr.Stop(streamErr)
				return
			}
			completed = true
			sr.Done()
		case "error", "response.error", "response.failed", "response.incomplete":
			streamErr = nativeResponsesFailure(streamResponse.Response)
			if streamErr == nil {
				var failure struct {
					Error any `json:"error"`
					types.OpenAIError
				}
				_ = common.UnmarshalJsonStr(data, &failure)
				if oaiError := dto.GetOpenAIError(failure.Error); oaiError != nil {
					streamErr = types.WithOpenAIError(*oaiError, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
				} else {
					if failure.Message == "" {
						failure.Message = fmt.Sprintf("responses stream error: %s", streamResponse.Type)
					}
					streamErr = types.WithOpenAIError(failure.OpenAIError, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
				}
			}
			sr.Stop(streamErr)
		case "response.output_text.delta":
			// 处理输出文本
			responseTextBuilder.WriteString(streamResponse.Delta)
		case dto.ResponsesOutputTypeItemDone:
			// 函数调用处理
			if streamResponse.Item != nil {
				if info != nil && info.ZTAPIPublicationSnapshot != nil {
					captureNativeHostedCalls(info, []dto.ResponsesOutput{*streamResponse.Item}, seenHosted)
					break
				}
				switch streamResponse.Item.Type {
				case dto.BuildInCallWebSearchCall:
					if info != nil && info.ResponsesUsageInfo != nil && info.ResponsesUsageInfo.BuiltInTools != nil {
						if webSearchTool, exists := info.ResponsesUsageInfo.BuiltInTools[dto.BuildInToolWebSearchPreview]; exists && webSearchTool != nil {
							webSearchTool.CallCount++
						}
					}
				}
			}
		}
		forward := streamErr == nil
		switch streamResponse.Type {
		case "error", "response.error", "response.failed", "response.incomplete":
			forward = true
		}
		if downstreamErr == nil && forward {
			payload := fmt.Sprintf("event: %s\ndata: %s\n\n", streamResponse.Type, data)
			n, err := c.Writer.Write([]byte(payload))
			if err == nil && n != len(payload) {
				err = io.ErrShortWrite
			}
			if err == nil {
				err = helper.FlushWriter(c)
			}
			if err != nil {
				downstreamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
				sr.Error(err)
				// Read remaining billing receipts, but never send a later success terminal.
			}
		}
	})

	if streamErr != nil {
		return usage, streamErr
	}
	if downstreamErr != nil {
		return usage, downstreamErr
	}
	if !completed || info.StreamStatus != nil && (!info.StreamStatus.IsNormalEnd() || info.StreamStatus.HasErrors()) {
		return usage, types.NewOpenAIError(fmt.Errorf("responses stream ended without a valid completion"), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if !reportedUsage && info.ZTAPIPublicationSnapshot == nil {
		if usage.CompletionTokens == 0 {
			// 计算输出文本的 token 数量
			tempStr := responseTextBuilder.String()
			if len(tempStr) > 0 {
				// 非正常结束，使用输出文本的 token 数量
				completionTokens := service.CountTextToken(tempStr, info.UpstreamModelName)
				usage.CompletionTokens = completionTokens
			}
		}

		if usage.PromptTokens == 0 && usage.CompletionTokens != 0 {
			usage.PromptTokens = info.GetEstimatePromptTokens()
		}
	}
	if info.ZTAPIPublicationSnapshot == nil {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	return usage, nil
}
