package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

// The handlers below answer a Responses client from a Chat Completions
// upstream. Items are built as plain maps so the wire shape follows the
// Responses API exactly; clients such as Codex parse it strictly.

type responsesViaChatItem struct {
	kind      string // "reasoning", "message", "function_call", "custom_tool_call"
	id        string
	callID    string
	name      string
	namespace string
	text      strings.Builder
	arguments strings.Builder
}

func (item *responsesViaChatItem) render(status string) map[string]any {
	var rendered map[string]any
	switch item.kind {
	case "reasoning":
		return map[string]any{"type": "reasoning", "id": item.id, "summary": []map[string]any{{"type": "summary_text", "text": item.text.String()}}}
	case "message":
		return map[string]any{"type": "message", "id": item.id, "status": status, "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": item.text.String(), "annotations": []any{}}}}
	case "custom_tool_call":
		rendered = map[string]any{"type": "custom_tool_call", "id": item.id, "status": status, "call_id": item.callID, "name": item.name,
			"input": customToolInput(item.arguments.String())}
	default:
		rendered = map[string]any{"type": "function_call", "id": item.id, "status": status, "call_id": item.callID, "name": item.name,
			"arguments": item.arguments.String()}
	}
	if item.namespace != "" {
		rendered["namespace"] = item.namespace
	}
	return rendered
}

func restoreResponsesToolName(item *responsesViaChatItem, names map[string]openaicompat.ResponsesToolName) {
	if original, ok := names[item.name]; ok {
		item.name, item.namespace = original.Name, original.Namespace
	}
}

// customToolInput recovers the raw input of a freeform tool that the model
// called as a function with a single string argument.
func customToolInput(arguments string) string {
	var wrapped map[string]any
	if err := common.UnmarshalJsonStr(arguments, &wrapped); err == nil {
		if input, ok := wrapped[openaicompat.CustomToolInputArgument].(string); ok {
			return input
		}
	}
	return arguments
}

func responsesViaChatUsage(usage *dto.Usage) map[string]any {
	if usage == nil {
		usage = &dto.Usage{}
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	return map[string]any{
		"input_tokens":          usage.PromptTokens,
		"input_tokens_details":  map[string]any{"cached_tokens": usage.PromptTokensDetails.CachedTokens},
		"output_tokens":         usage.CompletionTokens,
		"output_tokens_details": map[string]any{"reasoning_tokens": usage.CompletionTokenDetails.ReasoningTokens},
		"total_tokens":          total,
	}
}

// responsesViaChatStatus maps a chat finish reason onto a Responses status.
func responsesViaChatStatus(finishReason string) (string, map[string]any) {
	switch finishReason {
	case "length":
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case constant.FinishReasonContentFilter:
		return "incomplete", map[string]any{"reason": "content_filter"}
	default:
		return "completed", nil
	}
}

func responsesViaChatEnvelope(info *relaycommon.RelayInfo, id string, createdAt int64, status string, incomplete map[string]any, output []map[string]any, usage *dto.Usage) map[string]any {
	response := map[string]any{
		"id": id, "object": "response", "created_at": createdAt, "status": status, "model": info.OriginModelName,
		"output": output, "parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
		"error": nil, "incomplete_details": incomplete,
	}
	if output == nil {
		response["output"] = []any{}
	}
	if usage != nil {
		response["usage"] = responsesViaChatUsage(usage)
	} else {
		response["usage"] = nil
	}
	return response
}

func newResponsesViaChatID(prefix string) string {
	return prefix + strings.ReplaceAll(common.GetUUID(), "-", "")
}

func OaiChatToResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, customTools map[string]bool, names ...map[string]openaicompat.ResponsesToolName) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	var chat dto.OpenAITextResponse
	observed := relaycommon.ObserveZTAPIOpenAIChatUsage(c, info, body, resp.Header)
	if err := common.Unmarshal(body, &chat); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := chat.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	var output []map[string]any
	var text strings.Builder
	finishReason := ""
	for _, choice := range chat.Choices {
		if choice.Index != 0 {
			continue
		}
		finishReason = choice.FinishReason
		if reasoning := choice.Message.GetReasoningContent(); reasoning != "" {
			item := &responsesViaChatItem{kind: "reasoning", id: newResponsesViaChatID("rs_")}
			item.text.WriteString(reasoning)
			output = append(output, item.render("completed"))
		}
		if content := choice.Message.StringContent(); content != "" {
			text.WriteString(content)
			item := &responsesViaChatItem{kind: "message", id: newResponsesViaChatID("msg_")}
			item.text.WriteString(content)
			output = append(output, item.render("completed"))
		}
		for _, call := range choice.Message.ParseToolCalls() {
			kind := "function_call"
			if customTools[call.Function.Name] {
				kind = "custom_tool_call"
			}
			item := &responsesViaChatItem{kind: kind, id: newResponsesViaChatID("fc_"), callID: call.ID, name: call.Function.Name}
			if len(names) > 0 {
				restoreResponsesToolName(item, names[0])
			}
			item.arguments.WriteString(call.Function.Arguments)
			text.WriteString(call.Function.Name)
			text.WriteString(call.Function.Arguments)
			output = append(output, item.render("completed"))
		}
	}
	if finishReason == constant.FinishReasonContentFilter {
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "openai_finish_reason=content_filter")
	}

	usage := &chat.Usage
	if observed != nil || (info.ZTAPIPublicationSnapshot == nil && service.ValidUsage(usage)) {
		if observed != nil {
			usage = observed
		}
		normalizeChatUsage(info, usage)
		relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, body, "nonstream")
	} else {
		// Flagged as a local count, so managed billing parks it for review
		// instead of charging an estimate.
		usage = service.ResponseText2Usage(c, text.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	status, incomplete := responsesViaChatStatus(finishReason)
	createdAt := time.Now().Unix()
	response := responsesViaChatEnvelope(info, newResponsesViaChatID("resp_"), createdAt, status, incomplete, output, usage)
	encoded, err := common.Marshal(response)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeJsonMarshalFailed, http.StatusInternalServerError)
	}
	service.IOCopyBytesGracefully(c, resp, encoded)
	return usage, nil
}

// responsesViaChatStream turns Chat Completions chunks into the Responses
// event sequence: created, in_progress, one added/delta/done group per output
// item, then a terminal completed, incomplete or failed event.
type responsesViaChatStream struct {
	c           *gin.Context
	info        *relaycommon.RelayInfo
	customTools map[string]bool
	toolNames   map[string]openaicompat.ResponsesToolName
	id          string
	createdAt   int64
	sequence    int
	started     bool
	output      []map[string]any
	open        *responsesViaChatItem
	calls       map[int]*responsesViaChatItem
	lastCall    int
	usageText   strings.Builder
}

func (s *responsesViaChatStream) emit(eventType string, fields map[string]any) {
	fields["type"] = eventType
	fields["sequence_number"] = s.sequence
	s.sequence++
	data, err := common.Marshal(fields)
	if err != nil {
		return
	}
	helper.ResponseChunkData(s.c, dto.ResponsesStreamResponse{Type: eventType}, string(data))
}

func (s *responsesViaChatStream) start() {
	if s.started {
		return
	}
	s.started = true
	response := responsesViaChatEnvelope(s.info, s.id, s.createdAt, "in_progress", nil, nil, nil)
	s.emit("response.created", map[string]any{"response": response})
	s.emit("response.in_progress", map[string]any{"response": response})
}

func (s *responsesViaChatStream) outputIndex() int { return len(s.output) }

// closeOpen finishes the item currently receiving text, if any.
func (s *responsesViaChatStream) closeOpen() {
	item := s.open
	if item == nil {
		return
	}
	s.open = nil
	index := s.outputIndex()
	switch item.kind {
	case "reasoning":
		text := item.text.String()
		s.emit("response.reasoning_summary_text.done", map[string]any{"item_id": item.id, "output_index": index, "summary_index": 0, "text": text})
		s.emit("response.reasoning_summary_part.done", map[string]any{"item_id": item.id, "output_index": index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": text}})
	case "message":
		text := item.text.String()
		s.emit("response.output_text.done", map[string]any{"item_id": item.id, "output_index": index, "content_index": 0, "text": text})
		s.emit("response.content_part.done", map[string]any{"item_id": item.id, "output_index": index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}}})
	}
	rendered := item.render("completed")
	s.emit("response.output_item.done", map[string]any{"output_index": index, "item": rendered})
	s.output = append(s.output, rendered)
}

func (s *responsesViaChatStream) openText(kind string) *responsesViaChatItem {
	if s.open != nil && s.open.kind == kind {
		return s.open
	}
	s.closeOpen()
	s.closeCalls()
	prefix := "msg_"
	if kind == "reasoning" {
		prefix = "rs_"
	}
	item := &responsesViaChatItem{kind: kind, id: newResponsesViaChatID(prefix)}
	s.open = item
	index := s.outputIndex()
	if kind == "reasoning" {
		s.emit("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"type": "reasoning", "id": item.id, "summary": []any{}}})
		s.emit("response.reasoning_summary_part.added", map[string]any{"item_id": item.id, "output_index": index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	} else {
		s.emit("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"type": "message", "id": item.id, "status": "in_progress", "role": "assistant", "content": []any{}}})
		s.emit("response.content_part.added", map[string]any{"item_id": item.id, "output_index": index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
	}
	return item
}

func (s *responsesViaChatStream) text(kind, delta string) {
	if delta == "" {
		return
	}
	item := s.openText(kind)
	item.text.WriteString(delta)
	s.usageText.WriteString(delta)
	index := s.outputIndex()
	if kind == "reasoning" {
		s.emit("response.reasoning_summary_text.delta", map[string]any{"item_id": item.id, "output_index": index, "summary_index": 0, "delta": delta})
	} else {
		s.emit("response.output_text.delta", map[string]any{"item_id": item.id, "output_index": index, "content_index": 0, "delta": delta})
	}
}

// toolCall accumulates one streamed tool call. Calls are announced when they
// first appear and finished together once the model stops calling tools,
// because chat chunks may interleave their argument fragments.
func (s *responsesViaChatStream) toolCall(call dto.ToolCallResponse) {
	position := s.callPosition(call)
	item, ok := s.calls[position]
	if !ok {
		s.closeOpen()
		kind := "function_call"
		if s.customTools[call.Function.Name] {
			kind = "custom_tool_call"
		}
		item = &responsesViaChatItem{kind: kind, id: newResponsesViaChatID("fc_"), callID: call.ID, name: call.Function.Name}
		restoreResponsesToolName(item, s.toolNames)
		if item.callID == "" {
			item.callID = newResponsesViaChatID("call_")
		}
		s.calls[position] = item
		added := item.render("in_progress")
		if kind == "custom_tool_call" {
			added["input"] = ""
		} else {
			added["arguments"] = ""
		}
		s.emit("response.output_item.added", map[string]any{"output_index": s.outputIndex() + len(s.calls) - 1, "item": added})
	}
	if call.Function.Arguments == "" {
		return
	}
	item.arguments.WriteString(call.Function.Arguments)
	s.usageText.WriteString(call.Function.Arguments)
	if item.kind == "function_call" {
		s.emit("response.function_call_arguments.delta", map[string]any{"item_id": item.id, "output_index": s.callOutputIndex(position), "delta": call.Function.Arguments})
	}
}

// callPosition identifies which call a chunk belongs to. Most suppliers send an
// index; some send only the call id on the first fragment and nothing after.
func (s *responsesViaChatStream) callPosition(call dto.ToolCallResponse) int {
	if call.Index != nil {
		s.lastCall = *call.Index
		return s.lastCall
	}
	if call.ID != "" {
		for position, item := range s.calls {
			if item.callID == call.ID {
				s.lastCall = position
				return position
			}
		}
		next := 0
		for position := range s.calls {
			if position >= next {
				next = position + 1
			}
		}
		s.lastCall = next
		return next
	}
	return s.lastCall
}

func (s *responsesViaChatStream) callOutputIndex(position int) int {
	offset := 0
	for other := range s.calls {
		if other < position {
			offset++
		}
	}
	return s.outputIndex() + offset
}

func (s *responsesViaChatStream) closeCalls() {
	if len(s.calls) == 0 {
		return
	}
	positions := make([]int, 0, len(s.calls))
	for position := range s.calls {
		positions = append(positions, position)
	}
	for i := 1; i < len(positions); i++ {
		for j := i; j > 0 && positions[j] < positions[j-1]; j-- {
			positions[j], positions[j-1] = positions[j-1], positions[j]
		}
	}
	for _, position := range positions {
		item := s.calls[position]
		index := s.outputIndex()
		if item.kind == "custom_tool_call" {
			s.emit("response.custom_tool_call_input.done", map[string]any{"item_id": item.id, "output_index": index, "input": customToolInput(item.arguments.String())})
		} else {
			s.emit("response.function_call_arguments.done", map[string]any{"item_id": item.id, "output_index": index, "arguments": item.arguments.String()})
		}
		rendered := item.render("completed")
		s.emit("response.output_item.done", map[string]any{"output_index": index, "item": rendered})
		s.output = append(s.output, rendered)
		s.usageText.WriteString(item.name)
	}
	s.calls = map[int]*responsesViaChatItem{}
}

func OaiChatToResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, customTools map[string]bool, names ...map[string]openaicompat.ResponsesToolName) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		return nil, types.NewOpenAIError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse, http.StatusInternalServerError)
	}
	defer service.CloseResponseBodyGracefully(resp)

	stream := &responsesViaChatStream{
		c: c, info: info, customTools: customTools, id: newResponsesViaChatID("resp_"),
		createdAt: time.Now().Unix(), calls: map[int]*responsesViaChatItem{},
	}
	if len(names) > 0 {
		stream.toolNames = names[0]
	}
	var (
		usage        *dto.Usage
		finishReason string
		streamErr    *types.NewAPIError
	)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		observed := relaycommon.ObserveZTAPIOpenAIChatUsage(c, info, []byte(data), resp.Header)
		var chunk struct {
			Choices []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
			Usage   json.RawMessage                           `json:"usage"`
			Error   any                                       `json:"error"`
		}
		if err := common.UnmarshalJsonStr(data, &chunk); err != nil {
			sr.Error(err)
			return
		}
		if oaiErr := dto.GetOpenAIError(chunk.Error); oaiErr != nil && (oaiErr.Type != "" || oaiErr.Message != "") {
			streamErr = types.WithOpenAIError(*oaiErr, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		stream.start()
		var legacyUsage *dto.Usage
		if info.ZTAPIPublicationSnapshot == nil && len(chunk.Usage) > 0 {
			_ = common.Unmarshal(chunk.Usage, &legacyUsage)
		}
		if observed != nil || (info.ZTAPIPublicationSnapshot == nil && service.ValidUsage(legacyUsage)) {
			selected := legacyUsage
			if observed != nil {
				selected = observed
			}
			normalizeChatUsage(info, selected)
			relaycommon.CaptureZTAPIUpstreamUsageEvidence(info, []byte(data), "stream")
			usage = selected
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			delta := choice.Delta
			if delta.ReasoningContent != nil {
				stream.text("reasoning", *delta.ReasoningContent)
			} else if delta.Reasoning != nil {
				stream.text("reasoning", *delta.Reasoning)
			}
			if delta.Content != nil {
				stream.text("message", *delta.Content)
			}
			for _, call := range delta.ToolCalls {
				stream.toolCall(call)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
	})

	if streamErr == nil && (finishReason == "" || info.StreamStatus != nil && (!info.StreamStatus.IsNormalEnd() || info.StreamStatus.HasErrors())) {
		streamErr = types.NewErrorWithStatusCode(fmt.Errorf("upstream stream ended without a valid completion"), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	if streamErr != nil {
		if stream.started {
			failed := responsesViaChatEnvelope(info, stream.id, stream.createdAt, "failed", nil, stream.output, usage)
			failed["error"] = map[string]any{"code": string(streamErr.GetErrorCode()), "message": "The upstream stream did not complete successfully."}
			stream.emit("response.failed", map[string]any{"response": failed})
		}
		if finishReason != "" && usage != nil {
			return usage, streamErr
		}
		return nil, streamErr
	}

	stream.start()
	stream.closeOpen()
	stream.closeCalls()
	if usage == nil {
		// Flagged as a local count, so managed billing parks it for review
		// instead of charging an estimate.
		usage = service.ResponseText2Usage(c, stream.usageText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
	}
	if finishReason == constant.FinishReasonContentFilter {
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "openai_finish_reason=content_filter")
	}
	status, incomplete := responsesViaChatStatus(finishReason)
	terminal := "response.completed"
	if status == "incomplete" {
		terminal = "response.incomplete"
	}
	stream.emit(terminal, map[string]any{"response": responsesViaChatEnvelope(info, stream.id, stream.createdAt, status, incomplete, stream.output, usage)})
	return usage, nil
}
