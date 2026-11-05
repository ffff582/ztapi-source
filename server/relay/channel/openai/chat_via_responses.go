package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func responsesStreamIndexKey(itemID string, idx *int) string {
	if itemID == "" {
		return ""
	}
	if idx == nil {
		return itemID
	}
	return fmt.Sprintf("%s:%d", itemID, *idx)
}

func stringDeltaFromPrefix(prev string, next string) string {
	if next == "" {
		return ""
	}
	if prev != "" && strings.HasPrefix(next, prev) {
		return next[len(prev):]
	}
	return next
}

// Gin render helpers may record a write error without returning it to the caller.
type responsesTranslationWriter struct {
	gin.ResponseWriter
	err error
}

func (w *responsesTranslationWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.ResponseWriter.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func (w *responsesTranslationWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func OaiResponsesToChatHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	defer service.CloseResponseBodyGracefully(resp)

	var responsesResp dto.OpenAIResponsesResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}

	observed := relaycommon.ObserveZTAPIOpenAIUsage(c, info, body)
	if observed != nil {
		relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, body, "nonstream")
	}
	if err := common.Unmarshal(body, &responsesResp); err != nil {
		return observed, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}

	if apiErr := nativeResponsesFailure(&responsesResp); apiErr != nil {
		return observed, apiErr
	}

	chatId := helper.GetResponseID(c)
	chatResp, usage, err := service.ResponsesResponseToChatCompletionsResponse(&responsesResp, chatId)
	if err != nil {
		return observed, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}

	if observed != nil {
		usage = observed
	}
	if observed == nil && (info.ZTAPIPublicationSnapshot != nil || !service.ValidUsage(usage)) {
		text := service.ExtractOutputTextFromResponses(&responsesResp)
		usage = service.ResponseText2Usage(c, text, info.UpstreamModelName, info.GetEstimatePromptTokens())
		chatResp.Usage = *usage
	} else {
		normalizeChatUsage(info, usage)
		relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, body, "nonstream")
		chatResp.Usage = *usage
	}

	var responseBody []byte
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		claudeResp := service.ResponseOpenAI2Claude(chatResp, info)
		responseBody, err = common.Marshal(claudeResp)
	case types.RelayFormatGemini:
		geminiResp := service.ResponseOpenAI2Gemini(chatResp, info)
		responseBody, err = common.Marshal(geminiResp)
	default:
		responseBody, err = common.Marshal(chatResp)
	}
	if err != nil {
		return usage, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	}

	service.CopyUpstreamResponseHeaders(c, resp.Header)
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
	return usage, nil
}

func OaiResponsesToChatStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}

	defer service.CloseResponseBodyGracefully(resp)
	originalWriter := c.Writer
	writer := &responsesTranslationWriter{ResponseWriter: originalWriter}
	c.Writer = writer
	defer func() { c.Writer = originalWriter }()

	responseId := helper.GetResponseID(c)
	createAt := time.Now().Unix()
	model := info.UpstreamModelName

	var (
		usage         = &dto.Usage{}
		outputText    strings.Builder
		usageText     strings.Builder
		sentStart     bool
		sentStop      bool
		sawToolCall   bool
		streamErr     *types.NewAPIError
		downstreamErr *types.NewAPIError
		managedUsage  bool
		completed     bool
	)

	toolCallIndexByID := make(map[string]int)
	toolCallNameByID := make(map[string]string)
	toolCallArgsByID := make(map[string]string)
	toolCallNameSent := make(map[string]bool)
	toolCallCanonicalIDByItemID := make(map[string]string)
	hasSentReasoningSummary := false
	needsReasoningSummarySeparator := false
	//reasoningSummaryTextByKey := make(map[string]string)

	if info.RelayFormat == types.RelayFormatClaude && info.ClaudeConvertInfo == nil {
		info.ClaudeConvertInfo = &relaycommon.ClaudeConvertInfo{LastMessagesType: relaycommon.LastMessageTypeNone}
	}

	sendChatChunk := func(chunk *dto.ChatCompletionsStreamResponse) bool {
		if downstreamErr != nil || streamErr != nil {
			return false
		}
		if chunk == nil {
			return true
		}
		if info.RelayFormat == types.RelayFormatOpenAI {
			err := helper.ObjectData(c, chunk)
			if err == nil {
				err = writer.err
			}
			if err != nil {
				downstreamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
				return false
			}
			return true
		}

		chunkData, err := common.Marshal(chunk)
		if err != nil {
			streamErr = types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
			return false
		}
		err = HandleStreamFormat(c, info, string(chunkData), false, false)
		if err == nil {
			err = writer.err
		}
		if err != nil {
			downstreamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			return false
		}
		return true
	}

	sendStartIfNeeded := func() bool {
		if sentStart {
			return true
		}
		if !sendChatChunk(helper.GenerateStartEmptyResponse(responseId, createAt, model, nil)) {
			return false
		}
		sentStart = true
		return true
	}

	//sendReasoningDelta := func(delta string) bool {
	//	if delta == "" {
	//		return true
	//	}
	//	if !sendStartIfNeeded() {
	//		return false
	//	}
	//
	//	usageText.WriteString(delta)
	//	chunk := &dto.ChatCompletionsStreamResponse{
	//		Id:      responseId,
	//		Object:  "chat.completion.chunk",
	//		Created: createAt,
	//		Model:   model,
	//		Choices: []dto.ChatCompletionsStreamResponseChoice{
	//			{
	//				Index: 0,
	//				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
	//					ReasoningContent: &delta,
	//				},
	//			},
	//		},
	//	}
	//	if err := helper.ObjectData(c, chunk); err != nil {
	//		streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusInternalServerError)
	//		return false
	//	}
	//	return true
	//}

	sendReasoningSummaryDelta := func(delta string) bool {
		if delta == "" {
			return true
		}
		if needsReasoningSummarySeparator {
			if strings.HasPrefix(delta, "\n\n") {
				needsReasoningSummarySeparator = false
			} else if strings.HasPrefix(delta, "\n") {
				delta = "\n" + delta
				needsReasoningSummarySeparator = false
			} else {
				delta = "\n\n" + delta
				needsReasoningSummarySeparator = false
			}
		}
		if !sendStartIfNeeded() {
			return false
		}

		usageText.WriteString(delta)
		chunk := &dto.ChatCompletionsStreamResponse{
			Id:      responseId,
			Object:  "chat.completion.chunk",
			Created: createAt,
			Model:   model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{
				{
					Index: 0,
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
						ReasoningContent: &delta,
					},
				},
			},
		}
		if !sendChatChunk(chunk) {
			return false
		}
		hasSentReasoningSummary = true
		return true
	}

	sendToolCallDelta := func(callID string, name string, argsDelta string) bool {
		if callID == "" {
			return true
		}
		if !sendStartIfNeeded() {
			return false
		}

		idx, ok := toolCallIndexByID[callID]
		if !ok {
			idx = len(toolCallIndexByID)
			toolCallIndexByID[callID] = idx
		}
		if name != "" {
			toolCallNameByID[callID] = name
		}
		if toolCallNameByID[callID] != "" {
			name = toolCallNameByID[callID]
		}

		tool := dto.ToolCallResponse{
			ID:   callID,
			Type: "function",
			Function: dto.FunctionResponse{
				Arguments: argsDelta,
			},
		}
		tool.SetIndex(idx)
		if name != "" && !toolCallNameSent[callID] {
			tool.Function.Name = name
			toolCallNameSent[callID] = true
		}

		chunk := &dto.ChatCompletionsStreamResponse{
			Id:      responseId,
			Object:  "chat.completion.chunk",
			Created: createAt,
			Model:   model,
			Choices: []dto.ChatCompletionsStreamResponseChoice{
				{
					Index: 0,
					Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
						ToolCalls: []dto.ToolCallResponse{tool},
					},
				},
			},
		}
		if !sendChatChunk(chunk) {
			return false
		}
		sawToolCall = true

		// Include tool call data in the local builder for fallback token estimation.
		if tool.Function.Name != "" {
			usageText.WriteString(tool.Function.Name)
		}
		if argsDelta != "" {
			usageText.WriteString(argsDelta)
		}
		return true
	}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		observed := relaycommon.ObserveZTAPIOpenAIUsage(c, info, []byte(data))
		if observed != nil {
			usage, managedUsage = observed, true
			relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, []byte(data), "stream")
		}

		var streamResp dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResp); err != nil {
			logger.LogError(c, "failed to unmarshal responses stream event: "+err.Error())
			streamErr = types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			sr.Error(err)
			return
		}
		// Keep reading confirmed receipts after a downstream failure, without further writes.
		if downstreamErr != nil && streamResp.Type != "response.completed" &&
			streamResp.Type != "response.failed" && streamResp.Type != "response.incomplete" &&
			streamResp.Type != "response.error" && streamResp.Type != "error" {
			return
		}

		switch streamResp.Type {
		case "response.created":
			if streamResp.Response != nil {
				if streamResp.Response.Model != "" {
					model = streamResp.Response.Model
				}
				if streamResp.Response.CreatedAt != 0 {
					createAt = int64(streamResp.Response.CreatedAt)
				}
			}

		//case "response.reasoning_text.delta":
		//if !sendReasoningDelta(streamResp.Delta) {
		//	sr.Stop(streamErr)
		//	return
		//}

		//case "response.reasoning_text.done":

		case "response.reasoning_summary_text.delta":
			if !sendReasoningSummaryDelta(streamResp.Delta) {
				sr.Error(downstreamErr)
				return
			}

		case "response.reasoning_summary_text.done":
			if hasSentReasoningSummary {
				needsReasoningSummarySeparator = true
			}

		//case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		//	key := responsesStreamIndexKey(strings.TrimSpace(streamResp.ItemID), streamResp.SummaryIndex)
		//	if key == "" || streamResp.Part == nil {
		//		break
		//	}
		//	// Only handle summary text parts, ignore other part types.
		//	if streamResp.Part.Type != "" && streamResp.Part.Type != "summary_text" {
		//		break
		//	}
		//	prev := reasoningSummaryTextByKey[key]
		//	next := streamResp.Part.Text
		//	delta := stringDeltaFromPrefix(prev, next)
		//	reasoningSummaryTextByKey[key] = next
		//	if !sendReasoningSummaryDelta(delta) {
		//		sr.Stop(streamErr)
		//		return
		//	}

		case "response.output_text.delta":
			if !sendStartIfNeeded() {
				sr.Error(downstreamErr)
				return
			}

			if streamResp.Delta != "" {
				outputText.WriteString(streamResp.Delta)
				usageText.WriteString(streamResp.Delta)
				delta := streamResp.Delta
				chunk := &dto.ChatCompletionsStreamResponse{
					Id:      responseId,
					Object:  "chat.completion.chunk",
					Created: createAt,
					Model:   model,
					Choices: []dto.ChatCompletionsStreamResponseChoice{
						{
							Index: 0,
							Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
								Content: &delta,
							},
						},
					},
				}
				if !sendChatChunk(chunk) {
					sr.Error(downstreamErr)
					return
				}
			}

		case "response.output_item.added", "response.output_item.done":
			if streamResp.Item == nil {
				break
			}
			if streamResp.Item.Type != "function_call" {
				break
			}

			itemID := strings.TrimSpace(streamResp.Item.ID)
			callID := strings.TrimSpace(streamResp.Item.CallId)
			if callID == "" {
				callID = itemID
			}
			if itemID != "" && callID != "" {
				toolCallCanonicalIDByItemID[itemID] = callID
			}
			name := strings.TrimSpace(streamResp.Item.Name)
			if name != "" {
				toolCallNameByID[callID] = name
			}

			newArgs := streamResp.Item.ArgumentsString()
			prevArgs := toolCallArgsByID[callID]
			argsDelta := ""
			if newArgs != "" {
				if strings.HasPrefix(newArgs, prevArgs) {
					argsDelta = newArgs[len(prevArgs):]
				} else {
					argsDelta = newArgs
				}
				toolCallArgsByID[callID] = newArgs
			}

			if !sendToolCallDelta(callID, name, argsDelta) {
				sr.Error(downstreamErr)
				return
			}

		case "response.function_call_arguments.delta":
			itemID := strings.TrimSpace(streamResp.ItemID)
			callID := toolCallCanonicalIDByItemID[itemID]
			if callID == "" {
				callID = itemID
			}
			if callID == "" {
				break
			}
			toolCallArgsByID[callID] += streamResp.Delta
			if !sendToolCallDelta(callID, "", streamResp.Delta) {
				sr.Error(downstreamErr)
				return
			}

		case "response.function_call_arguments.done":

		case "response.completed":
			if streamResp.Response != nil {
				if streamResp.Response.Model != "" {
					model = streamResp.Response.Model
				}
				if streamResp.Response.CreatedAt != 0 {
					createAt = int64(streamResp.Response.CreatedAt)
				}
				if info.ZTAPIPublicationSnapshot == nil && streamResp.Response.Usage != nil {
					if streamResp.Response.Usage.InputTokens != 0 {
						usage.PromptTokens = streamResp.Response.Usage.InputTokens
						usage.InputTokens = streamResp.Response.Usage.InputTokens
					}
					if streamResp.Response.Usage.OutputTokens != 0 {
						usage.CompletionTokens = streamResp.Response.Usage.OutputTokens
						usage.OutputTokens = streamResp.Response.Usage.OutputTokens
					}
					if streamResp.Response.Usage.TotalTokens != 0 {
						usage.TotalTokens = streamResp.Response.Usage.TotalTokens
					} else {
						usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
					}
					if streamResp.Response.Usage.InputTokensDetails != nil {
						usage.PromptTokensDetails.CachedTokens = streamResp.Response.Usage.InputTokensDetails.CachedTokens
						usage.PromptTokensDetails.ImageTokens = streamResp.Response.Usage.InputTokensDetails.ImageTokens
						usage.PromptTokensDetails.AudioTokens = streamResp.Response.Usage.InputTokensDetails.AudioTokens
					}
					if streamResp.Response.Usage.CompletionTokenDetails.ReasoningTokens != 0 {
						usage.CompletionTokenDetails.ReasoningTokens = streamResp.Response.Usage.CompletionTokenDetails.ReasoningTokens
					}
					if service.ValidUsage(usage) {
						normalizeChatUsage(info, usage)
						relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, []byte(data), "stream")
					}
				}
			}

			if streamErr == nil {
				streamErr = nativeResponsesFailure(streamResp.Response)
			}
			var status string
			if streamErr == nil && (streamResp.Response == nil || (len(streamResp.Response.Status) > 0 &&
				(common.Unmarshal(streamResp.Response.Status, &status) != nil || status != "completed"))) {
				streamErr = types.NewOpenAIError(fmt.Errorf("invalid responses completion"), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			}
			completed = streamErr == nil
			if streamErr != nil {
				sr.Stop(streamErr)
			} else {
				sr.Done()
			}

		case "error", "response.error", "response.failed", "response.incomplete":
			if streamErr == nil {
				streamErr = nativeResponsesFailure(streamResp.Response)
			}
			if streamErr == nil {
				streamErr = types.NewOpenAIError(fmt.Errorf("responses stream error: %s", streamResp.Type), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
			}
			sr.Stop(streamErr)
			return

		default:
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

	if info.ZTAPIPublicationSnapshot == nil && !managedUsage && !service.ValidUsage(usage) {
		usage = service.ResponseText2Usage(c, usageText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}

	if !sentStart {
		if !sendChatChunk(helper.GenerateStartEmptyResponse(responseId, createAt, model, nil)) {
			if downstreamErr != nil {
				return usage, downstreamErr
			}
			return usage, streamErr
		}
	}
	if !sentStop {
		if info.RelayFormat == types.RelayFormatClaude && info.ClaudeConvertInfo != nil {
			info.ClaudeConvertInfo.Usage = usage
		}
		finishReason := "stop"
		if sawToolCall {
			finishReason = "tool_calls"
		}
		stop := helper.GenerateStopResponse(responseId, createAt, model, finishReason)
		if info.RelayFormat == types.RelayFormatClaude {
			stop.Usage = usage
		}
		if !sendChatChunk(stop) {
			if downstreamErr != nil {
				return usage, downstreamErr
			}
			return usage, streamErr
		}
	}
	if info.RelayFormat == types.RelayFormatOpenAI && info.ShouldIncludeUsage && usage != nil {
		err := helper.ObjectData(c, helper.GenerateFinalUsageResponse(responseId, createAt, model, *usage))
		if err == nil {
			err = writer.err
		}
		if err != nil {
			return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
	}

	if info.RelayFormat == types.RelayFormatOpenAI {
		err := helper.StringData(c, "[DONE]")
		if err == nil {
			err = writer.err
		}
		if err != nil {
			return usage, types.NewOpenAIError(err, types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
	}
	return usage, nil
}
