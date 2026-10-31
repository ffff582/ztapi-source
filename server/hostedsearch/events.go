package hostedsearch

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (e *engine) publishItem(item map[string]any) error {
	index := len(e.output)
	id, _ := item["id"].(string)
	if id == "" {
		id = NewID("item_")
		item["id"] = id
	}
	kind, _ := item["type"].(string)
	added := make(map[string]any, len(item))
	for key, value := range item {
		added[key] = value
	}
	added["status"] = "in_progress"
	switch kind {
	case "message":
		added["content"] = []any{}
	case "function_call":
		added["arguments"] = ""
	case "custom_tool_call":
		added["input"] = ""
	case "reasoning":
		added["summary"] = []any{}
	}
	if err := e.event("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
		return err
	}
	switch kind {
	case "message":
		content, ok := item["content"].([]any)
		if !ok {
			return ErrModel
		}
		for contentIndex, raw := range content {
			part, ok := raw.(map[string]any)
			if !ok {
				return ErrModel
			}
			base := map[string]any{"item_id": id, "output_index": index, "content_index": contentIndex}
			if part["type"] == "refusal" {
				refusal, ok := part["refusal"].(string)
				if !ok {
					return ErrModel
				}
				if err := e.event("response.content_part.added", with(base, "part", map[string]any{"type": "refusal", "refusal": ""})); err != nil {
					return err
				}
				if err := e.event("response.refusal.delta", with(base, "delta", refusal)); err != nil {
					return err
				}
				if err := e.event("response.refusal.done", with(base, "refusal", refusal)); err != nil {
					return err
				}
				if err := e.event("response.content_part.done", with(base, "part", part)); err != nil {
					return err
				}
				continue
			}
			if part["type"] != "output_text" {
				return ErrModel
			}
			text, ok := part["text"].(string)
			if !ok {
				return ErrModel
			}
			annotations := e.annotations(text, part["annotations"])
			part["annotations"] = annotations
			if err := e.event("response.content_part.added", with(base, "part", map[string]any{"type": "output_text", "text": "", "annotations": []any{}})); err != nil {
				return err
			}
			if err := e.event("response.output_text.delta", with(base, "delta", text)); err != nil {
				return err
			}
			for i, annotation := range annotations {
				data := with(base, "annotation", annotation)
				data["annotation_index"] = i
				if err := e.event("response.output_text.annotation.added", data); err != nil {
					return err
				}
			}
			if err := e.event("response.output_text.done", with(base, "text", text)); err != nil {
				return err
			}
			if err := e.event("response.content_part.done", with(base, "part", part)); err != nil {
				return err
			}
		}
	case "function_call":
		base := map[string]any{"item_id": id, "output_index": index}
		if err := e.event("response.function_call_arguments.delta", with(base, "delta", item["arguments"])); err != nil {
			return err
		}
		if err := e.event("response.function_call_arguments.done", with(base, "arguments", item["arguments"])); err != nil {
			return err
		}
	case "custom_tool_call":
		base := map[string]any{"item_id": id, "output_index": index}
		if err := e.event("response.custom_tool_call_input.delta", with(base, "delta", item["input"])); err != nil {
			return err
		}
		if err := e.event("response.custom_tool_call_input.done", with(base, "input", item["input"])); err != nil {
			return err
		}
	}
	e.output = append(e.output, item)
	return e.event("response.output_item.done", map[string]any{"output_index": index, "item": item})
}

func (e *engine) annotations(text string, raw any) []any {
	result := []any{}
	type key struct {
		start, end int
		url        string
	}
	seen := map[key]bool{}
	length := utf8.RuneCountInString(text)
	add := func(url string, start, end int) {
		source, ok := e.sources[url]
		k := key{start, end, url}
		if !ok || start < 0 || end <= start || end > length || seen[k] {
			return
		}
		seen[k] = true
		result = append(result, map[string]any{"type": "url_citation", "url": url, "title": source.Title, "start_index": start, "end_index": end})
	}
	if original, ok := raw.([]any); ok {
		for _, value := range original {
			annotation, ok := value.(map[string]any)
			if !ok || annotation["type"] != "url_citation" {
				continue
			}
			url, _ := annotation["url"].(string)
			start, a := citationIndex(annotation["start_index"])
			end, b := citationIndex(annotation["end_index"])
			if a && b {
				add(url, start, end)
			}
		}
	}
	urls := make([]string, 0, len(e.sources))
	for url := range e.sources {
		urls = append(urls, url)
	}
	sort.Strings(urls)
	for _, url := range urls {
		start := citationStart(text, url)
		if start >= 0 {
			add(url, utf8.RuneCountInString(text[:start]), utf8.RuneCountInString(text[:start+len(url)]))
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i].(map[string]any), result[j].(map[string]any)
		if a["start_index"] != b["start_index"] {
			return a["start_index"].(int) < b["start_index"].(int)
		}
		return a["url"].(string) < b["url"].(string)
	})
	return result
}

func citationIndex(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		if number >= 0 && number <= 2*1024*1024 && number == float64(int(number)) {
			return int(number), true
		}
	case json.Number:
		n, err := number.Int64()
		if err == nil && n >= 0 && n <= 2*1024*1024 {
			return int(n), true
		}
	case int:
		if number >= 0 && number <= 2*1024*1024 {
			return number, true
		}
	}
	return 0, false
}

func citationStart(text, url string) int {
	for offset := 0; offset < len(text); {
		match := strings.Index(text[offset:], url)
		if match < 0 {
			return -1
		}
		start := offset + match
		end := start + len(url)
		valid := true
		if start > 0 {
			previous, _ := utf8.DecodeLastRuneInString(text[:start])
			if unicode.IsLetter(previous) || unicode.IsDigit(previous) || strings.ContainsRune("/._-", previous) {
				valid = false
			}
		}
		if end < len(text) {
			next, size := utf8.DecodeRuneInString(text[end:])
			delimiter := unicode.IsSpace(next) || strings.ContainsRune(")]}>\"',;!", next)
			after, _ := utf8.DecodeRuneInString(text[end+size:])
			if next == '.' && (end+size == len(text) || unicode.IsSpace(after)) {
				delimiter = true
			}
			if !delimiter {
				valid = false
			}
		}
		if valid {
			return start
		}
		offset = end
	}
	return -1
}

func with(base map[string]any, key string, value any) map[string]any {
	data := make(map[string]any, len(base)+1)
	for k, v := range base {
		data[k] = v
	}
	data[key] = value
	return data
}
