package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
)

const (
	ztapiHealthMaxBody  = 1 << 20
	ztapiHealthMaxEvent = 256 << 10
	ztapiHealthMaxTools = 128
)

type ZTAPIHealthObservedAttempt = types.ZTAPIHealthAttempt
type ZTAPIHealthObservation = types.ZTAPIHealthOutcome

type ZTAPIHealthCollector struct {
	mu             sync.Mutex
	source         string
	sealed         bool
	attempts       []*ZTAPIHealthWireAttempt
	current        *ZTAPIHealthWireAttempt
	requestContext context.Context
}

type ztapiHealthTool struct {
	name      bool
	args      string
	fragments bool
	valid     bool
}

type ZTAPIHealthWireAttempt struct {
	collector                                             *ZTAPIHealthCollector
	summary                                               ZTAPIHealthObservedAttempt
	stream, eof, closed, transportError, limit, malformed bool
	text, media, refusal, reasoning, native, done, failed bool
	terminal                                              string
	finishes                                              []string
	choices                                               map[string]bool
	tools                                                 map[string]*ztapiHealthTool
	providerStatus                                        int
	buffer, event                                         []byte
	probeText                                             string
	probeOverflow                                         bool
	readBytes, toolBytes                                  int
	responseTextDeltas                                    bool
	eofCancelled, deliveryComplete, deliveryFailed        bool
}

func NewZTAPIHealthCollector(source string) *ZTAPIHealthCollector {
	return &ZTAPIHealthCollector{source: source}
}

func (c *ZTAPIHealthCollector) BeginAttempt(channelID int, protocol string) *ZTAPIHealthWireAttempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed || len(c.attempts) >= 64 {
		return nil
	}
	a := &ZTAPIHealthWireAttempt{collector: c, summary: ZTAPIHealthObservedAttempt{Index: len(c.attempts) + 1, ChannelID: channelID, Protocol: protocol}, choices: map[string]bool{}, tools: map[string]*ztapiHealthTool{}}
	c.attempts = append(c.attempts, a)
	c.current = a
	return a
}

func (a *ZTAPIHealthWireAttempt) update(f func()) {
	if a == nil {
		return
	}
	a.collector.mu.Lock()
	defer a.collector.mu.Unlock()
	if !a.collector.sealed {
		f()
	}
}

func (a *ZTAPIHealthWireAttempt) Dispatched() { a.update(func() { a.summary.Dispatched = true }) }
func (a *ZTAPIHealthWireAttempt) TransportError(err error) {
	if err != nil {
		a.update(func() { a.transportError = true })
	}
}

func (a *ZTAPIHealthWireAttempt) WrapResponse(resp *http.Response) {
	if a == nil || resp == nil {
		return
	}
	a.update(func() {
		a.summary.HTTPStatus = resp.StatusCode
		a.stream = strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
		for _, key := range []string{"x-request-id", "request-id", "x-goog-request-id", "x-amzn-requestid"} {
			if id := healthMetadata(resp.Header.Get(key)); id != "" {
				a.summary.UpstreamRequestID = id
				break
			}
		}
	})
	if resp.Body != nil {
		resp.Body = &ztapiHealthBody{ReadCloser: resp.Body, attempt: a}
	}
}

type ztapiHealthBody struct {
	io.ReadCloser
	attempt *ZTAPIHealthWireAttempt
}

func (b *ztapiHealthBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.attempt.update(func() {
		a := b.attempt
		if n > 0 && !a.closed {
			a.feed(p[:n])
		}
		if err == io.EOF {
			if !a.eof && a.collector.requestContext != nil {
				a.eofCancelled = errors.Is(a.collector.requestContext.Err(), context.Canceled)
			}
			a.eof = true
			a.endBody()
		} else if err != nil && !a.closed {
			a.transportError = true
		}
	})
	return n, err
}
func (b *ztapiHealthBody) Close() error {
	b.attempt.update(func() { b.attempt.closed = true })
	return b.ReadCloser.Close()
}

func (a *ZTAPIHealthWireAttempt) feed(p []byte) {
	a.readBytes += len(p)
	if a.limit {
		return
	}
	// Sniff SSE as well: some compatible upstreams omit the content type.
	if !a.stream && len(a.buffer) == 0 && (bytes.HasPrefix(p, []byte("data:")) || bytes.HasPrefix(p, []byte("event:"))) {
		a.stream = true
	}
	if !a.stream {
		if len(a.buffer)+len(p) > ztapiHealthMaxBody {
			a.limit = true
			a.buffer = nil
			return
		}
		a.buffer = append(a.buffer, p...)
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		n := len(p)
		if i >= 0 {
			n = i + 1
		}
		if len(a.buffer)+n > ztapiHealthMaxEvent {
			a.limit = true
			a.buffer = nil
			a.event = nil
			return
		}
		a.buffer = append(a.buffer, p[:n]...)
		p = p[n:]
		if i < 0 {
			return
		}
		line := bytes.TrimSuffix(bytes.TrimSuffix(a.buffer, []byte("\n")), []byte("\r"))
		if len(line) == 0 {
			a.endEvent()
		} else if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimPrefix(data, []byte(" "))
			if len(a.event)+len(data)+1 > ztapiHealthMaxEvent {
				a.limit = true
				a.event = nil
				a.buffer = nil
				return
			}
			a.event = append(a.event, data...)
			a.event = append(a.event, '\n')
		}
		a.buffer = a.buffer[:0]
	}
}

func (a *ZTAPIHealthWireAttempt) endEvent() {
	data := bytes.TrimSpace(a.event)
	if len(data) > 0 {
		if bytes.Equal(data, []byte("[DONE]")) {
			a.done = true
		} else {
			a.parse(data)
		}
	}
	a.event = nil
}

func (a *ZTAPIHealthWireAttempt) endBody() {
	if a.limit {
		return
	}
	if a.stream {
		// Incomplete SSE events are not dispatched by the wire protocol.
		if len(bytes.TrimSpace(a.buffer)) > 0 || len(bytes.TrimSpace(a.event)) > 0 {
			a.malformed = true
		}
	} else {
		a.parse(a.buffer)
		a.buffer = nil
	}
}

func healthMetadata(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.:-/", r)) {
			return ""
		}
	}
	return s
}

func (a *ZTAPIHealthWireAttempt) addText(s string) {
	if strings.TrimSpace(s) != "" {
		a.text = true
	}
	if a.collector.source == "probe" && !a.probeOverflow {
		if len(a.probeText)+len(s) > 128 {
			a.probeOverflow = true
			a.probeText = ""
		} else {
			a.probeText += s
		}
	}
}

func (a *ZTAPIHealthWireAttempt) finish(s string) {
	if s == "" {
		return
	}
	s = healthMetadata(s)
	if s == "" {
		a.malformed = true
		return
	}
	for _, f := range a.finishes {
		if f == s {
			return
		}
	}
	if len(a.finishes) >= 64 {
		a.limit = true
		return
	}
	a.finishes = append(a.finishes, s)
}

func (a *ZTAPIHealthWireAttempt) tool(key, name, args string, fragments bool, valid bool) {
	t := a.tools[key]
	if t == nil {
		if len(a.tools) >= ztapiHealthMaxTools {
			a.limit = true
			return
		}
		t = &ztapiHealthTool{}
		a.tools[key] = t
	}
	t.name = t.name || name != ""
	if fragments {
		if !t.fragments {
			t.args = ""
			t.valid = false
			t.fragments = true
		}
		if len(t.args)+len(args) > ztapiHealthMaxEvent || a.toolBytes+len(args) > ztapiHealthMaxBody {
			a.limit = true
			t.args = ""
			return
		}
		t.args += args
		a.toolBytes += len(args)
		t.valid = gjson.Valid(t.args)
	} else {
		t.valid = valid
	}
}

func (a *ZTAPIHealthWireAttempt) content(v gjson.Result) {
	if v.Type == gjson.String {
		a.addText(v.String())
		return
	}
	for _, item := range v.Array() {
		switch item.Get("type").String() {
		case "text", "output_text", "text_delta":
			a.addText(item.Get("text").String())
		case "refusal":
			a.refusal = true
		case "image", "image_url", "output_image", "audio", "output_audio":
			a.media = a.media || item.Get("source.data").String() != "" || item.Get("image_url.url").String() != "" || item.Get("data").String() != "" || item.Get("url").String() != ""
		case "thinking", "redacted_thinking":
			a.reasoning = true
		case "tool_use":
			a.tool(item.Get("id").String(), item.Get("name").String(), "", false, item.Get("input").IsObject())
		}
	}
}

func (a *ZTAPIHealthWireAttempt) parseError(v gjson.Result) {
	if !v.Exists() || v.Type == gjson.Null {
		return
	}
	a.failed = true
	if code := healthMetadata(v.Get("code").String()); code != "" {
		a.summary.ProviderErrorCode = code
	} else if code := healthMetadata(v.Get("type").String()); code != "" {
		a.summary.ProviderErrorCode = code
	}
	if id := healthMetadata(v.Get("request_id").String()); id != "" {
		a.summary.UpstreamRequestID = id
	}
	fields := v.Get("provider_specific_fields")
	if fields.Exists() {
		for _, key := range []string{"status_code", "http_status", "status"} {
			if status := int(fields.Get(key).Int()); status >= 100 && status <= 599 {
				a.providerStatus = status
				break
			}
		}
		if id := healthMetadata(fields.Get("request_id").String()); id != "" {
			a.summary.UpstreamRequestID = id
		}
		if code := healthMetadata(fields.Get("code").String()); code != "" {
			a.summary.ProviderErrorCode = code
		}
	}
}

func (a *ZTAPIHealthWireAttempt) parse(data []byte) {
	if !gjson.ValidBytes(data) {
		a.malformed = true
		return
	}
	v := gjson.ParseBytes(data)
	if v.IsArray() && a.summary.Protocol == "gemini" {
		for _, item := range v.Array() {
			a.gemini(item)
		}
		return
	}
	if !v.IsObject() {
		a.malformed = true
		return
	}
	a.parseError(v.Get("error"))
	if id := healthMetadata(v.Get("request_id").String()); id != "" {
		a.summary.UpstreamRequestID = id
	}
	switch a.summary.Protocol {
	case "embeddings":
		if !a.failed {
			_, err := common.ValidateZTAPIEmbeddingResponse(data, 0, 0)
			a.malformed = a.malformed || err != nil || a.stream
			a.native = err == nil && !a.stream
		}
	case "chat":
		a.chat(v)
	case "responses":
		a.responses(v)
	case "claude":
		a.claude(v)
	case "gemini":
		a.gemini(v)
	case "images":
		a.images(v)
	}
}

func (a *ZTAPIHealthWireAttempt) images(v gjson.Result) {
	data := v.Get("data")
	if !data.Exists() || !data.IsArray() {
		a.malformed = true
		return
	}
	a.native = true
	results := data.Array()
	for _, result := range results {
		if !result.IsObject() || (strings.TrimSpace(result.Get("url").String()) == "" && strings.TrimSpace(result.Get("b64_json").String()) == "") {
			a.malformed = true
			continue
		}
		a.media = true
	}
}

func (a *ZTAPIHealthWireAttempt) chat(v gjson.Result) {
	for i, choice := range v.Get("choices").Array() {
		index := strconv.Itoa(i)
		if choice.Get("index").Exists() {
			index = choice.Get("index").String()
		}
		if len(a.choices) >= 128 {
			a.limit = true
			return
		}
		f := choice.Get("finish_reason").String()
		a.finish(f)
		a.choices[index] = a.choices[index] || f != ""
		m := choice.Get("message")
		// Streaming output belongs to delta; a compatibility message snapshot
		// may be null, empty, or repeat text already delivered in earlier chunks.
		if a.stream || !m.Exists() {
			m = choice.Get("delta")
		}
		a.content(m.Get("content"))
		a.addText(choice.Get("text").String())
		a.refusal = a.refusal || choice.Get("message.refusal").String() != "" || choice.Get("delta.refusal").String() != ""
		a.reasoning = a.reasoning || m.Get("reasoning_content").String() != "" || m.Get("reasoning").String() != ""
		a.media = a.media || m.Get("audio.data").String() != ""
		for j, tool := range m.Get("tool_calls").Array() {
			key := index + ":" + strconv.Itoa(j)
			if tool.Get("index").Exists() {
				key = index + ":" + tool.Get("index").String()
			}
			args := tool.Get("function.arguments").String()
			a.tool(key, tool.Get("function.name").String(), args, a.stream, gjson.Valid(args))
		}
		if tool := m.Get("function_call"); tool.Exists() {
			args := tool.Get("arguments").String()
			a.tool(index+":legacy", tool.Get("name").String(), args, a.stream, gjson.Valid(args))
		}
	}
}

func (a *ZTAPIHealthWireAttempt) responseOutput(v gjson.Result) {
	for i, item := range v.Array() {
		switch item.Get("type").String() {
		case "message":
			a.content(item.Get("content"))
		case "function_call":
			args := item.Get("arguments").String()
			key := healthMetadata(item.Get("id").String())
			if key == "" {
				key = strconv.Itoa(i)
			}
			a.tool("item:"+key, item.Get("name").String(), args, false, gjson.Valid(args))
		case "reasoning":
			a.reasoning = true
		case "image_generation_call":
			a.media = a.media || item.Get("result").String() != ""
		}
	}
}

func (a *ZTAPIHealthWireAttempt) responses(v gjson.Result) {
	typ := v.Get("type").String()
	switch typ {
	case "response.output_text.delta":
		a.responseTextDeltas = true
		a.addText(v.Get("delta").String())
	case "response.refusal.delta", "response.refusal.done":
		a.refusal = true
	case "response.output_item.added":
		item := v.Get("item")
		if item.Get("type").String() == "function_call" {
			key := healthMetadata(item.Get("id").String())
			if key == "" {
				key = v.Get("output_index").String()
			}
			a.tool("item:"+key, item.Get("name").String(), item.Get("arguments").String(), true, false)
		}
	case "response.function_call_arguments.delta":
		key := healthMetadata(v.Get("item_id").String())
		if key == "" {
			key = v.Get("output_index").String()
		}
		a.tool("item:"+key, "", v.Get("delta").String(), true, false)
	case "response.output_item.done":
		text, overflow := a.probeText, a.probeOverflow
		a.responseOutput(gjson.Parse("[" + v.Get("item").Raw + "]"))
		if a.responseTextDeltas {
			a.probeText = text
			a.probeOverflow = overflow
		}
	case "error":
		a.parseError(v)
	}
	r := v
	if strings.HasPrefix(typ, "response.") {
		if typ != "response.completed" && typ != "response.incomplete" && typ != "response.failed" {
			return
		}
		r = v.Get("response")
		if typ != "response."+r.Get("status").String() {
			a.malformed = true
		}
	}
	status := r.Get("status").String()
	if status != "" {
		a.terminal = healthMetadata(status)
	}
	if status == "completed" || status == "incomplete" || status == "failed" {
		a.native = true
	}
	if status == "failed" {
		a.failed = true
	}
	if r.Get("output").Exists() {
		// Terminal snapshots repeat streamed text; probe matching uses one copy.
		if a.collector.source == "probe" {
			a.probeText = ""
			a.probeOverflow = false
		}
		a.responseOutput(r.Get("output"))
	}
	a.parseError(r.Get("error"))
	if reason := r.Get("incomplete_details.reason").String(); reason != "" {
		a.finish(reason)
	}
}

func (a *ZTAPIHealthWireAttempt) claude(v gjson.Result) {
	typ := v.Get("type").String()
	if !a.stream {
		a.content(v.Get("content"))
		a.finish(v.Get("stop_reason").String())
		a.native = len(a.finishes) > 0
	}
	switch typ {
	case "content_block_start":
		block := v.Get("content_block")
		if block.Get("type").String() == "tool_use" {
			a.tool("block:"+v.Get("index").String(), block.Get("name").String(), "", false, block.Get("input").IsObject())
		} else {
			a.content(gjson.Parse("[" + block.Raw + "]"))
		}
	case "content_block_delta":
		d := v.Get("delta")
		switch d.Get("type").String() {
		case "text_delta":
			a.addText(d.Get("text").String())
		case "thinking_delta":
			a.reasoning = true
		case "input_json_delta":
			a.tool("block:"+v.Get("index").String(), "", d.Get("partial_json").String(), true, false)
		}
	case "message_delta":
		a.finish(v.Get("delta.stop_reason").String())
	case "message_stop":
		a.native = true
		a.terminal = "message_stop"
	case "error":
		a.parseError(v.Get("error"))
	}
}

func (a *ZTAPIHealthWireAttempt) gemini(v gjson.Result) {
	if v.Get("promptFeedback.blockReason").String() != "" {
		a.refusal = true
		a.finish(v.Get("promptFeedback.blockReason").String())
	}
	for i, candidate := range v.Get("candidates").Array() {
		index := strconv.Itoa(i)
		if candidate.Get("index").Exists() {
			index = candidate.Get("index").String()
		}
		if len(a.choices) >= 128 {
			a.limit = true
			return
		}
		f := candidate.Get("finishReason").String()
		a.finish(f)
		a.choices[index] = a.choices[index] || f != ""
		for j, part := range candidate.Get("content.parts").Array() {
			if part.Get("thought").Bool() {
				a.reasoning = true
			} else {
				a.addText(part.Get("text").String())
			}
			if tool := part.Get("functionCall"); tool.Exists() {
				a.tool(index+":"+strconv.Itoa(j), tool.Get("name").String(), "", false, tool.Get("args").IsObject())
			}
			a.media = a.media || part.Get("inlineData.data").String() != "" || part.Get("fileData.fileUri").String() != ""
		}
	}
}

func (a *ZTAPIHealthWireAttempt) supported() bool {
	switch a.summary.Protocol {
	case "chat", "responses", "claude", "gemini", "embeddings", "images":
		return true
	}
	return false
}

func (a *ZTAPIHealthWireAttempt) nativeTerminal() bool {
	if a.summary.Protocol == "embeddings" {
		return a.native && !a.stream
	}
	if a.summary.Protocol == "images" {
		return a.native && !a.stream
	}
	all := len(a.choices) > 0
	for _, finished := range a.choices {
		all = all && finished
	}
	switch a.summary.Protocol {
	case "chat":
		return all && (!a.stream || a.done)
	case "responses":
		return a.native
	case "claude":
		return a.native && len(a.finishes) > 0
	case "gemini":
		return all
	}
	return false
}

// PermitCompletion only concerns synthesized transport markers, never billing.
func (c *ZTAPIHealthCollector) PermitCompletion() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return true
	}
	a := c.current
	return !a.supported() || a.limit || (a.nativeTerminal() && !a.malformed && !a.transportError && !a.failed)
}

func (c *ZTAPIHealthCollector) endedWithoutTerminal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.current
	return a != nil && a.supported() && !a.limit && (a.closed || a.eof) && (!a.nativeTerminal() || a.malformed || a.transportError || a.failed)
}

func (c *ZTAPIHealthCollector) Seal(cancelled, relayFailed bool) (ZTAPIHealthObservation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return ZTAPIHealthObservation{}, false
	}
	c.sealed = true
	if a := c.current; a != nil && a.deliveryFailed {
		relayFailed = true
	}
	// A client can close a fully delivered response while billing is still
	// completing. Only a complete, uncancelled delivery freezes that boundary.
	if a := c.current; a != nil && a.deliveryComplete && !a.deliveryFailed && !a.eofCancelled {
		cancelled = false
	}
	out := ZTAPIHealthObservation{Result: "unknown", Reason: "not_dispatched", ClientCancelled: cancelled}
	if c.current != nil {
		a := c.current
		s := a.summary
		out.ChannelID = s.ChannelID
		out.UpstreamProtocol = s.Protocol
		out.HTTPStatus = s.HTTPStatus
		out.UpstreamRequestID = s.UpstreamRequestID
		out.ProviderErrorCode = s.ProviderErrorCode
		out.Dispatched = s.Dispatched
		out.FinishReasons = append([]string(nil), a.finishes...)
		out.TerminalStatus = a.terminal
		out.HasText = a.text
		out.HasMedia = a.media
		for _, tool := range a.tools {
			out.HasTool = out.HasTool || (tool.name && tool.valid)
		}
		out.TransportComplete = !a.transportError && !a.malformed && (a.eof || a.stream && a.nativeTerminal())
		out.Result, out.Reason = a.classify(out, relayFailed)
	}
	if cancelled {
		out.Result = "excluded"
		out.Reason = "client_cancelled"
	}
	for _, a := range c.attempts {
		out.Attempts = append(out.Attempts, a.summary)
		// Discard transient customer/probe content before publishing any outcome.
		a.buffer = nil
		a.event = nil
		a.probeText = ""
		a.tools = nil
	}
	return out, true
}

func (a *ZTAPIHealthWireAttempt) classify(out ZTAPIHealthObservation, relayFailed bool) (string, string) {
	if !out.Dispatched {
		return "unknown", "not_dispatched"
	}
	status := out.HTTPStatus
	if a.providerStatus != 0 {
		status = a.providerStatus
	}
	if status >= 500 && status <= 599 {
		return "failure", "upstream_http_error"
	}
	code := strings.ToLower(out.ProviderErrorCode)
	refusal := a.refusal
	inputError := false
	serverError := false
	upstreamFault := ""
	switch code {
	case "content_filter", "content_policy_violation", "safety", "prompt_blocked", "refusal":
		refusal = true
	case "invalid_request", "invalid_request_error":
		inputError = a.collector.source != "probe" && (status == http.StatusBadRequest || status == http.StatusUnprocessableEntity)
	case "invalid_argument", "invalid_parameter", "invalid_parameters", "context_length_exceeded":
		inputError = true
	case "invalid_api_key", "authentication_error", "unauthenticated", "invalid_key", "key_expired", "key_disabled":
		upstreamFault = "upstream_auth"
	case "model_not_granted":
		upstreamFault = "upstream_model_permission"
	case "insufficient_quota", "resource_exhausted", "billing_hard_limit_reached", "insufficient_balance", "credit_balance_too_low", "quota_exceeded":
		upstreamFault = "upstream_quota"
	case "rate_limit_exceeded", "rate_limit_error", "too_many_requests":
		upstreamFault = "upstream_rate_limit"
	case "server_error", "internal_error", "internal", "overloaded_error", "api_error", "upstream_error", "upstream_unavailable":
		serverError = true
	}
	// Gateway auth/quota rejection happens before dispatch. These wire statuses
	// therefore describe the upstream route, not the customer's gateway account.
	switch status {
	case http.StatusUnauthorized:
		return "failure", "upstream_auth"
	case http.StatusPaymentRequired:
		return "failure", "upstream_quota"
	case http.StatusTooManyRequests:
		return "failure", "upstream_rate_limit"
	}
	length, invalid := false, false
	for _, f := range a.finishes {
		switch f {
		case "stop", "tool_calls", "function_call", "end_turn", "stop_sequence", "tool_use", "STOP":
		case "length", "max_tokens", "max_output_tokens", "MAX_TOKENS":
			length = true
		case "refusal", "content_filter", "SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "RECITATION", "SPII", "IMAGE_SAFETY":
			refusal = true
		default:
			invalid = true
		}
	}
	if refusal {
		if a.collector.source == "probe" {
			return "failure", "probe_refusal"
		}
		return "excluded", "safety_refusal"
	}
	if upstreamFault != "" {
		return "failure", upstreamFault
	}
	if inputError {
		return "excluded", "invalid_input"
	}
	if status >= 400 && status < 500 {
		if a.collector.source == "probe" {
			return "failure", "probe_upstream_4xx"
		}
		if serverError {
			return "failure", "upstream_error"
		}
		return "unknown", "ambiguous_upstream_4xx"
	}
	if a.transportError {
		return "failure", "upstream_transport_error"
	}
	if !a.supported() {
		return "unknown", "unsupported_protocol"
	}
	if a.limit {
		return "unknown", "observation_limit"
	}
	if a.failed {
		if serverError || out.TerminalStatus == "failed" {
			return "failure", "upstream_error"
		}
		return "unknown", "unclassified_upstream_error"
	}
	if a.malformed {
		return "failure", "malformed_upstream_response"
	}
	if relayFailed && a.readBytes == 0 {
		return "unknown", "relay_processing_error"
	}
	if !out.TransportComplete || !a.nativeTerminal() {
		return "failure", "missing_native_terminal"
	}
	if invalid || out.TerminalStatus == "incomplete" && !length {
		if a.collector.source == "probe" {
			return "failure", "probe_invalid_terminal"
		}
		return "unknown", "unrecognized_terminal"
	}
	if relayFailed {
		return "unknown", "relay_processing_error"
	}
	if a.summary.Protocol == "embeddings" {
		return "success", "valid_embedding"
	}
	if a.collector.source == "probe" {
		if a.summary.Protocol == "images" {
			if out.HasMedia {
				return "success", "probe_valid_output"
			}
			return "failure", "probe_incorrect_output"
		}
		if length {
			return "failure", "probe_invalid_terminal"
		}
		if a.probeOverflow || strings.TrimSpace(a.probeText) != "77" || out.HasTool || out.HasMedia {
			return "failure", "probe_incorrect_output"
		}
		return "success", "probe_valid_output"
	}
	if out.HasText || out.HasTool || out.HasMedia {
		return "success", "valid_output"
	}
	if length {
		return "unknown", "length_without_visible_output"
	}
	return "failure", "empty_output"
}
