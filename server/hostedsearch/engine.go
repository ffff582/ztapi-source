package hostedsearch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var (
	ErrLimit = errors.New("hosted_search_limit_exceeded")
	ErrModel = errors.New("hosted_search_invalid_model_response")
	ErrTool  = errors.New("hosted_search_tool_failed")
)

const maxNativeBufferedEvents = 4096
const maxNativeBufferedEventBytes = 16 * 1024 * 1024

type nativeBufferedEvent struct {
	kind string
	data []byte
}

type ToolRequest struct {
	ID      string
	Action  string `json:"action"`
	Query   string `json:"query,omitempty"`
	URL     string `json:"url,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	Count   int    `json:"count,omitempty"`
}

type ModelRound func(context.Context, []byte) ([]byte, error)
type ToolDispatch func(context.Context, ToolRequest, func(context.Context) (Result, error)) (Result, error)
type Config struct {
	Model    ModelRound
	Provider Provider
	Dispatch ToolDispatch
	Pages    PageStore
}

type Event struct {
	Type string
	Data map[string]any
}

type RunResult struct {
	Body       []byte
	Rounds     int
	ToolCalls  int
	ResponseID string
}

func NewID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("cryptographic random source unavailable")
	}
	return prefix + hex.EncodeToString(value[:])
}

type engine struct {
	p                                      *PreparedRequest
	cfg                                    Config
	emit                                   func(Event) error
	id                                     string
	created                                int64
	seq                                    int
	output                                 []map[string]any
	pages                                  map[string]Result
	sources                                map[string]Source
	seen                                   map[string]bool
	rounds, calls, contentBytes            int
	input, outputTokens, cached, reasoning int64
	bufferedEvents                         []nativeBufferedEvent
	bufferedEventBytes                     int
	nativeEmittedEvents                    int
}

func Run(ctx context.Context, p *PreparedRequest, cfg Config, emit func(Event) error) (result *RunResult, runErr error) {
	if p == nil || !p.Hosted || cfg.Model == nil || cfg.Provider == nil || cfg.Dispatch == nil {
		return nil, ErrRequest
	}
	e := &engine{p: p, cfg: cfg, emit: emit, id: NewID("resp_"), created: time.Now().Unix(), output: []map[string]any{}, pages: map[string]Result{}, sources: map[string]Source{}, seen: map[string]bool{}}
	if p.NativeResponses {
		defer func() {
			if runErr != nil {
				e.emitNativeFailure()
			}
		}()
	}
	if cfg.Pages != nil {
		for _, source := range cfg.Pages.Sources() {
			if ValidatePublicURL(source.URL) == nil {
				e.sources[source.URL] = source
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.event("response.created", map[string]any{"response": e.envelope("in_progress", nil)}); err != nil {
		return nil, err
	}
	if err := e.event("response.in_progress", map[string]any{"response": e.envelope("in_progress", nil)}); err != nil {
		return nil, err
	}
	result, err := e.run(ctx)
	if err != nil {
		if p.NativeResponses {
			return nil, err
		}
		failure := e.envelope("failed", nil)
		failure["error"] = map[string]string{"code": "hosted_search_failed", "message": "Search could not be completed. Model usage already incurred remains recorded; uncertain tool outcomes are held for reconciliation."}
		_ = e.event("response.failed", map[string]any{"response": failure})
	}
	if err == nil && p.NativeResponses {
		if err := e.flushNativeEvents(ctx); err != nil {
			return nil, err
		}
	}
	return result, err
}

func (e *engine) run(ctx context.Context) (*RunResult, error) {
	limit := MaxRequestBytes
	if e.p.NativeResponses {
		limit = MaxNativeRoundBytes
	}
	if len(e.p.Body) > limit {
		return nil, ErrLimit
	}
	request := append([]byte(nil), e.p.Body...)
	for e.rounds < 4 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(request) > limit {
			return nil, ErrLimit
		}
		raw, err := e.cfg.Model(ctx, request)
		e.rounds++
		if err != nil {
			return nil, err
		}
		response, output, err := decodeModel(raw)
		if err != nil {
			return nil, err
		}
		if err := e.addUsage(response["usage"]); err != nil {
			return nil, err
		}
		if e.p.NativeResponses {
			if id, ok := response["id"].(string); !ok || !validNativeResponseID(id) {
				return nil, ErrModel
			}
		}
		var private []map[string]any
		var public []map[string]any
		localCalls := false
		for _, item := range output {
			kind, _ := item["type"].(string)
			name, _ := item["name"].(string)
			if kind == "function_call" || kind == "custom_tool_call" {
				callID, _ := item["call_id"].(string)
				if callID == "" || len(callID) > 256 || e.seen[callID] {
					return nil, ErrModel
				}
				e.seen[callID] = true
				if name == e.p.PrivateTool && kind == "function_call" {
					private = append(private, item)
					continue
				}
				if strings.HasPrefix(name, privateToolPrefix) {
					return nil, ErrModel
				}
				localCalls = true
			}
			public = append(public, item)
		}
		status, _ := response["status"].(string)
		if len(private) == 0 {
			return e.finish(response, public)
		}
		if status != "completed" {
			return nil, ErrModel
		}
		// Native stored history cannot hide an unresolved private result beside
		// a client call. Never return that provider ID as a resumable success.
		if e.p.NativeResponses && localCalls && !e.p.Stateless {
			return nil, ErrModel
		}
		if !localCalls && e.rounds >= 4 {
			return nil, ErrLimit
		}
		if e.calls+len(private) > 8 {
			return nil, ErrLimit
		}
		requests := make([]ToolRequest, 0, len(private))
		for _, item := range private {
			tool, err := parseTool(item)
			if err != nil {
				return nil, err
			}
			requests = append(requests, tool)
		}
		results := make([]map[string]any, 0, len(private))
		var evidence []string
		for i, tool := range requests {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			tool.ID = NewID("tool_")
			item := map[string]any{"id": NewID("ws_"), "type": "web_search_call", "status": "in_progress", "action": toolAction(tool)}
			index := len(e.output)
			if err := e.event("response.output_item.added", map[string]any{"output_index": index, "item": item}); err != nil {
				return nil, err
			}
			if err := e.event("response.web_search_call.in_progress", map[string]any{"output_index": index, "item_id": item["id"]}); err != nil {
				return nil, err
			}
			if err := e.event("response.web_search_call.searching", map[string]any{"output_index": index, "item_id": item["id"]}); err != nil {
				return nil, err
			}
			result, err := e.execute(ctx, tool)
			e.calls++
			if err != nil || (result.Outcome != OutcomeSucceeded && result.Outcome != OutcomeEmpty) {
				return nil, ErrTool
			}
			text, err := e.toolText(tool, result)
			if err != nil {
				return nil, err
			}
			item["status"] = "completed"
			if tool.Action == "search" {
				action := item["action"].(map[string]any)
				sources := []map[string]any{}
				for _, s := range result.Sources {
					sources = append(sources, map[string]any{"type": "url", "url": s.URL})
				}
				action["sources"] = sources
			}
			e.output = append(e.output, item)
			if err := e.event("response.web_search_call.completed", map[string]any{"output_index": index, "item_id": item["id"]}); err != nil {
				return nil, err
			}
			if err := e.event("response.output_item.done", map[string]any{"output_index": index, "item": item}); err != nil {
				return nil, err
			}
			results = append(results, map[string]any{"type": "function_call_output", "call_id": private[i]["call_id"], "output": text})
			evidence = append(evidence, text)
		}
		if localCalls {
			// A stateless client must retain retrieved evidence before its pending local calls.
			message := map[string]any{"type": "message", "id": NewID("msg_"), "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Retrieved web evidence (untrusted source content):\n" + strings.Join(evidence, "\n"), "annotations": []any{}}}}
			return e.finish(response, append([]map[string]any{message}, public...))
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(request, &body) != nil {
			return nil, ErrRequest
		}
		var history []any
		var input string
		if json.Unmarshal(body["input"], &input) == nil {
			history = []any{map[string]any{"role": "user", "content": input}}
		} else if json.Unmarshal(body["input"], &history) != nil {
			return nil, ErrRequest
		}
		for _, item := range output {
			history = append(history, item)
		}
		for _, item := range results {
			history = append(history, item)
		}
		body["input"], _ = json.Marshal(history)
		// A forced first search must not force every continuation to search again.
		body["tool_choice"] = json.RawMessage(`"auto"`)
		request, _ = json.Marshal(body)
	}
	return nil, ErrLimit
}

func decodeModel(raw []byte) (map[string]any, []map[string]any, error) {
	if len(raw) == 0 || len(raw) > 2*1024*1024 || common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return nil, nil, ErrModel
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(raw, &response) != nil || rawString(response["object"]) != "response" || (rawString(response["status"]) != "completed" && rawString(response["status"]) != "incomplete") {
		return nil, nil, ErrModel
	}
	var output []map[string]any
	if json.Unmarshal(response["output"], &output) != nil || len(output) == 0 || len(output) > 128 {
		return nil, nil, ErrModel
	}
	for _, item := range output {
		if item == nil {
			return nil, nil, ErrModel
		}
	}
	var fields map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&fields) != nil {
		return nil, nil, ErrModel
	}
	return fields, output, nil
}

func parseTool(item map[string]any) (ToolRequest, error) {
	var tool ToolRequest
	arguments, ok := item["arguments"].(string)
	if !ok || len(arguments) > 10000 || common.RejectDuplicateJsonObjectMembers(strings.NewReader(arguments)) != nil {
		return tool, ErrRequest
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(arguments), &fields) != nil || fields == nil {
		return tool, ErrRequest
	}
	for name := range fields {
		if name != "action" && name != "query" && name != "url" && name != "pattern" && name != "count" {
			return tool, ErrRequest
		}
	}
	if json.Unmarshal([]byte(arguments), &tool) != nil {
		return tool, ErrRequest
	}
	switch tool.Action {
	case "search":
		// The shared schema declares all action fields; only dispatch fields for
		// the selected action. Models may populate another action's optional fields.
		tool.URL, tool.Pattern = "", ""
		if strings.TrimSpace(tool.Query) == "" || utf8.RuneCountInString(tool.Query) > 128 {
			return tool, ErrRequest
		}
		if _, exists := fields["count"]; !exists {
			tool.Count = 5
		}
		if tool.Count < 1 || tool.Count > 10 {
			return tool, ErrRequest
		}
	case "open_page", "find_in_page":
		tool.Query, tool.Count = "", 0
		if ValidatePublicURL(tool.URL) != nil {
			return tool, ErrRequest
		}
		if tool.Action == "find_in_page" && (strings.TrimSpace(tool.Pattern) == "" || utf8.RuneCountInString(tool.Pattern) > 256) {
			return tool, ErrRequest
		}
		if tool.Action == "open_page" {
			tool.Pattern = ""
		}
	default:
		return tool, ErrRequest
	}
	return tool, nil
}

func toolAction(tool ToolRequest) map[string]any {
	action := map[string]any{"type": tool.Action}
	switch tool.Action {
	case "search":
		action["query"] = tool.Query
	case "open_page":
		action["url"] = tool.URL
	case "find_in_page":
		action["url"], action["pattern"] = tool.URL, tool.Pattern
	}
	return action
}

func (e *engine) execute(ctx context.Context, tool ToolRequest) (Result, error) {
	if tool.Action == "find_in_page" {
		page, ok := e.page(tool.URL)
		if !ok {
			fetch := tool
			fetch.Action = "open_page"
			fetch.Pattern = ""
			var err error
			page, err = e.cfg.Dispatch(ctx, fetch, func(ctx context.Context) (Result, error) { return e.cfg.Provider.Fetch(ctx, tool.URL) })
			if err != nil || page.Outcome != OutcomeSucceeded {
				return page, err
			}
			e.pages[tool.URL] = page
			e.sources[tool.URL] = Source{Title: page.Title, URL: tool.URL}
			if e.cfg.Pages != nil {
				e.cfg.Pages.Put(tool.URL, page)
				e.cfg.Pages.RememberSource(e.sources[tool.URL])
			}
		}
		lines := strings.Split(page.Content, "\n")
		var matches []string
		for i, line := range lines {
			if strings.Contains(strings.ToLower(line), strings.ToLower(tool.Pattern)) {
				matches = append(matches, fmt.Sprintf("L%d: %s", i+1, line))
				if len(matches) == 20 {
					break
				}
			}
		}
		if len(matches) == 0 {
			return Result{Outcome: OutcomeEmpty}, nil
		}
		return Result{Outcome: OutcomeSucceeded, Title: page.Title, Content: strings.Join(matches, "\n")}, nil
	}
	if tool.Action == "open_page" {
		if page, ok := e.page(tool.URL); ok {
			return page, nil
		}
	}
	return e.cfg.Dispatch(ctx, tool, func(ctx context.Context) (Result, error) {
		if tool.Action == "search" {
			return e.cfg.Provider.Search(ctx, tool.Query, tool.Count)
		}
		return e.cfg.Provider.Fetch(ctx, tool.URL)
	})
}

func (e *engine) toolText(tool ToolRequest, result Result) (string, error) {
	data := map[string]any{"action": tool.Action, "outcome": result.Outcome}
	if tool.Action == "search" {
		if len(result.Sources) > 10 {
			return "", ErrLimit
		}
		for _, source := range result.Sources {
			if ValidatePublicURL(source.URL) != nil {
				return "", ErrTool
			}
			e.sources[source.URL] = source
			if e.cfg.Pages != nil {
				e.cfg.Pages.RememberSource(source)
			}
		}
		data["sources"] = append([]Source(nil), result.Sources...)
	} else {
		data["url"], data["title"], data["content"] = tool.URL, result.Title, result.Content
		if tool.Action == "open_page" && result.Outcome == OutcomeSucceeded {
			e.pages[tool.URL] = result
			e.sources[tool.URL] = Source{Title: result.Title, URL: tool.URL}
			if e.cfg.Pages != nil {
				e.cfg.Pages.Put(tool.URL, result)
				e.cfg.Pages.RememberSource(Source{Title: result.Title, URL: tool.URL})
			}
		}
	}
	encoded, _ := json.Marshal(data)
	e.contentBytes += len(encoded)
	if e.contentBytes > 1024*1024 || len(encoded) > 384*1024 {
		return "", ErrLimit
	}
	// Full verified pages remain in the authenticated cache for find-in-page.
	// The model receives a bounded excerpt, explicitly marked when shortened.
	budget := e.p.ContextBytes
	for len(encoded) > budget {
		data["context_truncated"] = true
		if content, ok := data["content"].(string); ok && content != "" {
			data["content_truncated"] = true
			data["total_content_bytes"] = len(result.Content)
			data["content"] = excerpt(content, max(0, len(content)-(len(encoded)-budget)-128))
		} else if sources, ok := data["sources"].([]Source); ok {
			largest := -1
			title := false
			length := 0
			for i, source := range sources {
				if len(source.Summary) > length {
					largest, title, length = i, false, len(source.Summary)
				}
				if len(source.Title) > length {
					largest, title, length = i, true, len(source.Title)
				}
			}
			if largest < 0 {
				return "", ErrLimit
			}
			limit := max(0, length-(len(encoded)-budget)-128)
			if title {
				sources[largest].Title = excerpt(sources[largest].Title, limit)
			} else {
				sources[largest].Summary = excerpt(sources[largest].Summary, limit)
			}
			data["sources"] = sources
		} else {
			return "", ErrLimit
		}
		encoded, _ = json.Marshal(data)
	}
	return string(encoded), nil
}

func excerpt(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}

func (e *engine) page(url string) (Result, bool) {
	if page, ok := e.pages[url]; ok {
		return page, true
	}
	if e.cfg.Pages != nil {
		if page, ok := e.cfg.Pages.Get(url); ok {
			e.pages[url] = page
			e.sources[url] = Source{Title: page.Title, URL: url}
			return page, true
		}
	}
	return Result{}, false
}

func (e *engine) addUsage(raw any) error {
	usage, ok := raw.(map[string]any)
	if !ok {
		return ErrModel
	}
	read := func(m map[string]any, key string) (int64, error) {
		value, ok := m[key].(json.Number)
		if !ok {
			return 0, ErrModel
		}
		n, err := value.Int64()
		if err != nil || n < 0 || n > 100000000 {
			return 0, ErrModel
		}
		return n, nil
	}
	input, err := read(usage, "input_tokens")
	if err != nil {
		return err
	}
	output, err := read(usage, "output_tokens")
	if err != nil {
		return err
	}
	var cached, reasoning int64
	if detail, ok := usage["input_tokens_details"].(map[string]any); ok {
		cached, err = read(detail, "cached_tokens")
		if err != nil || cached > input {
			return ErrModel
		}
	}
	if detail, ok := usage["output_tokens_details"].(map[string]any); ok {
		reasoning, err = read(detail, "reasoning_tokens")
		if err != nil || reasoning > output {
			return ErrModel
		}
	}
	e.input += input
	e.outputTokens += output
	e.cached += cached
	e.reasoning += reasoning
	return nil
}

func (e *engine) envelope(status string, incomplete any) map[string]any {
	var tools any = []any{}
	if len(e.p.OriginalTools) > 0 {
		_ = json.Unmarshal(e.p.OriginalTools, &tools)
	}
	return map[string]any{"id": e.id, "object": "response", "created_at": e.created, "status": status, "model": e.p.Model, "output": e.output, "tools": tools, "tool_choice": "auto", "parallel_tool_calls": true, "error": nil, "incomplete_details": incomplete, "usage": map[string]any{"input_tokens": e.input, "output_tokens": e.outputTokens, "total_tokens": e.input + e.outputTokens, "input_tokens_details": map[string]any{"cached_tokens": e.cached}, "output_tokens_details": map[string]any{"reasoning_tokens": e.reasoning}}}
}

func (e *engine) finish(response map[string]any, output []map[string]any) (*RunResult, error) {
	if e.p.NativeResponses {
		e.id = response["id"].(string)
	}
	for _, item := range output {
		if err := e.publishItem(item); err != nil {
			return nil, err
		}
	}
	status, _ := response["status"].(string)
	envelope := e.envelope(status, response["incomplete_details"])
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrModel
	}
	if err := e.event("response."+status, map[string]any{"response": envelope}); err != nil {
		return nil, err
	}
	return &RunResult{Body: body, Rounds: e.rounds, ToolCalls: e.calls, ResponseID: e.id}, nil
}

func (e *engine) event(kind string, data map[string]any) error {
	if e.emit == nil {
		return nil
	}
	data["type"], data["sequence_number"] = kind, e.seq
	e.seq++
	if e.p.NativeResponses {
		raw, err := common.Marshal(data)
		if err != nil {
			return ErrModel
		}
		if len(e.bufferedEvents) >= maxNativeBufferedEvents || len(raw) > maxNativeBufferedEventBytes-e.bufferedEventBytes {
			return ErrLimit
		}
		// Serialize now: tool status and output slices are mutated in later rounds.
		e.bufferedEvents = append(e.bufferedEvents, nativeBufferedEvent{kind: kind, data: raw})
		e.bufferedEventBytes += len(raw)
		return nil
	}
	return e.emit(Event{Type: kind, Data: data})
}

func validNativeResponseID(id string) bool {
	if len(id) == 0 || len(id) > 256 || strings.HasPrefix(id, "hs_failed_") {
		return false
	}
	for _, c := range id {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

func (e *engine) flushNativeEvents(ctx context.Context) error {
	defer func() { e.bufferedEvents = nil; e.bufferedEventBytes = 0 }()
	for _, event := range e.bufferedEvents {
		if err := ctx.Err(); err != nil {
			return err
		}
		var data map[string]any
		if common.Unmarshal(event.data, &data) != nil {
			return ErrModel
		}
		if response, ok := data["response"].(map[string]any); ok {
			response["id"] = e.id
		}
		if err := e.emit(Event{Type: event.kind, Data: data}); err != nil {
			return err
		}
		e.nativeEmittedEvents++
	}
	return nil
}

func (e *engine) emitNativeFailure() {
	e.bufferedEvents, e.bufferedEventBytes = nil, 0
	if e.emit == nil {
		return
	}
	id := NewID("hs_failed_")
	if e.nativeEmittedEvents > 0 {
		id = e.id
	}
	// Even buffer exhaustion must yield a bounded terminal, not a fake provider ID.
	failure := map[string]any{
		"id": id, "object": "response", "created_at": e.created,
		"status": "failed", "model": e.p.Model, "output": []any{},
		"error": map[string]string{"code": "hosted_search_failed", "message": "Search could not be completed. Model usage already incurred remains recorded; uncertain tool outcomes are held for reconciliation."},
		"usage": map[string]any{"input_tokens": e.input, "output_tokens": e.outputTokens, "total_tokens": e.input + e.outputTokens},
	}
	_ = e.emit(Event{Type: "response.failed", Data: map[string]any{"type": "response.failed", "sequence_number": e.nativeEmittedEvents, "response": failure}})
}
