package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/openaicompat"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type ztapiClientBridgeConfig struct {
	OutputDir      string `json:"output_dir"`
	Profile        string `json:"profile"`
	MaxDispatches  int    `json:"max_dispatches"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func TestZTAPIClientBridgeConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	valid := map[string]any{"output_dir": "run", "profile": "sdk", "max_dispatches": 16, "timeout_seconds": 180}
	write := func(v map[string]any) {
		raw, err := common.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
	write(valid)
	cfg, err := ztapiLoadClientBridgeConfig(path)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "run"), cfg.OutputDir)
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"escape", "output_dir", "../escape"}, {"root", "output_dir", "."},
		{"profile", "profile", "live"}, {"zero", "max_dispatches", 0},
		{"excess", "max_dispatches", 17}, {"fraction", "max_dispatches", 1.5},
		{"deadline", "timeout_seconds", 181}, {"negative", "timeout_seconds", -1},
		{"unknown", "api_key", "DO_NOT_ECHO_CONFIG_SECRET"}, {"null", "profile", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{}
			for k, x := range valid {
				v[k] = x
			}
			v[tc.field] = tc.value
			write(v)
			_, err := ztapiLoadClientBridgeConfig(path)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "DO_NOT_ECHO_CONFIG_SECRET")
		})
	}
	for _, profile := range []string{"sdk", "codex", "claude-code"} {
		valid["profile"] = profile
		write(valid)
		_, err := ztapiLoadClientBridgeConfig(path)
		require.NoError(t, err)
	}
	_, err = ztapiLoadClientBridgeConfig(filepath.Join(root, "missing.json"))
	require.Error(t, err)
}

func TestZTAPIClientBridgeConfigRejectsDuplicateMembersAndStaleFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"output_dir":"run","profile":"sdk","profile":"codex","max_dispatches":1,"timeout_seconds":1}`), 0600))
	_, err := ztapiLoadClientBridgeConfig(path)
	require.EqualError(t, err, "bridge_config_schema")
	require.NoError(t, os.WriteFile(path, []byte(`{"output_dir":"run","profile":"sdk","max_dispatches":1,"timeout_seconds":1}`), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "run"), 0700))
	for _, name := range []string{"ready.json", "bridge-report.json", "stop"} {
		p := filepath.Join(root, "run", name)
		require.NoError(t, os.WriteFile(p, []byte("DO_NOT_OVERWRITE"), 0600))
		_, err = ztapiLoadClientBridgeConfig(path)
		require.EqualError(t, err, "bridge_output_not_fresh")
		require.NoError(t, os.Remove(p))
	}
}

func TestZTAPIClientBridgeUpstreamGuard(t *testing.T) {
	allowed := "127.0.0.1:12345"
	u, err := url.Parse("http://" + allowed + "/v1/chat/completions")
	require.NoError(t, err)
	require.NoError(t, ztapiBridgeUpstreamGuard(u, allowed))
	for _, target := range []string{"https://127.0.0.1:12345/v1/chat/completions", "http://127.0.0.1:12346/v1/chat/completions", "http://supplier.invalid/v1/chat/completions", "http://127.0.0.1:12345/v1/responses", "http://key@127.0.0.1:12345/v1/chat/completions", "http://127.0.0.1:12345/v1/chat/completions?target=live"} {
		u, err := url.Parse(target)
		require.NoError(t, err)
		require.Error(t, ztapiBridgeUpstreamGuard(u, allowed))
	}
	u, err = url.Parse("http://supplier.invalid:12345/v1/chat/completions")
	require.NoError(t, err)
	require.Error(t, ztapiBridgeUpstreamGuard(u, u.Host))
}

func TestZTAPIClientBridgeClientResultMustMatchIssuedCall(t *testing.T) {
	s := &ztapiBridgeScript{profile: "codex", outputDir: t.TempDir()}
	r := map[string]any{"tools": []any{ztapiBridgeTestTool("apply_patch", "input", "string")}}
	d, err := s.next(r)
	require.NoError(t, err)
	// A result with the right ID but an altered tool call must not advance.
	history := ztapiBridgeTestHistory(d, "Success. Updated client-result.txt")
	history[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] = "undeclared"
	r["messages"] = history
	_, err = s.next(r)
	require.EqualError(t, err, "bridge_tool_result_mismatch")
}

func TestZTAPIClientBridgeEndpointGuard(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages?beta=true"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"zt-claude-sonnet-5"}`))
		_, err := ztapiClientBridgeRequest(r)
		require.NoError(t, err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v1/responses", `{}`}, {"POST", "/v1/models", `{}`},
		{"POST", "/v1/responses?url=https://supplier.invalid", `{"model":"zt-claude-sonnet-5"}`},
		{"POST", "/v1/messages?beta=false", `{"model":"zt-claude-sonnet-5"}`},
		{"POST", "/v1/messages?beta=true&beta=true", `{"model":"zt-claude-sonnet-5"}`},
		{"POST", "/v1/messages?beta=true&key=hidden", `{"model":"zt-claude-sonnet-5"}`},
		{"POST", "http://supplier.invalid/v1/responses", `{"model":"zt-claude-sonnet-5"}`},
		{"POST", "/v1/responses", `{"model":"live-model"}`},
		{"POST", "/v1/responses", `{"model":"zt-claude-sonnet-5","input":"` + strings.Repeat("x", 1<<20) + `"}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		_, err := ztapiClientBridgeRequest(r)
		require.Error(t, err)
	}
}

func ztapiBridgeTestTool(name, field, kind string) map[string]any {
	p := map[string]any{"type": kind}
	if kind == "array" {
		p["items"] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "strict": true,
		"parameters": map[string]any{"type": "object", "properties": map[string]any{field: p}, "required": []any{field}}}}
}

func ztapiBridgeTestHistory(d ztapiBridgeDecision, result string) []any {
	args, _ := common.Marshal(d.Arguments)
	return []any{
		map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": d.ID, "type": "function", "function": map[string]any{"name": d.Name, "arguments": string(args)}}}},
		map[string]any{"role": "tool", "tool_call_id": d.ID, "content": result},
	}
}

func TestZTAPIClientBridgeScriptSDK(t *testing.T) {
	s := &ztapiBridgeScript{profile: "sdk"}
	request := map[string]any{"model": "claude-sonnet-5", "messages": []any{map[string]any{"role": "user", "content": "synthetic"}}}
	d, err := s.next(request)
	require.NoError(t, err)
	require.Equal(t, "ZTAPI_SDK_OK", d.Text)
	request["tools"] = []any{ztapiBridgeTestTool("lookup", "value", "string")}
	request["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}
	d, err = s.next(request)
	require.NoError(t, err)
	require.Equal(t, "lookup", d.Name)
	require.Equal(t, map[string]any{"value": "ZTAPI_TOOL_VALUE"}, d.Arguments)
	request["messages"] = ztapiBridgeTestHistory(d, `{"value":"ZTAPI_TOOL_VALUE"}`)
	done, err := s.next(request)
	require.NoError(t, err)
	require.Equal(t, "ZTAPI_TOOL_DONE", done.Text)
	request["messages"].([]any)[1].(map[string]any)["tool_call_id"] = "orphan"
	_, err = s.next(request)
	require.EqualError(t, err, "bridge_tool_result_mismatch")
	request["messages"] = []any{map[string]any{"role": "user", "content": "synthetic"}}
	request["tools"] = []any{ztapiBridgeTestTool("unknown", "value", "string")}
	_, err = s.next(request)
	require.EqualError(t, err, "bridge_tool_shape")
}

func TestZTAPIClientBridgeScriptCodex(t *testing.T) {
	for _, command := range []struct{ field, kind string }{{"cmd", "string"}, {"command", "string"}, {"command", "array"}} {
		t.Run(command.field+command.kind, func(t *testing.T) {
			s := &ztapiBridgeScript{profile: "codex", outputDir: t.TempDir(), aliases: map[string]string{"declared_patch": "apply_patch"}}
			r := map[string]any{"tools": []any{ztapiBridgeTestTool("declared_patch", "input", "string"), ztapiBridgeTestTool("exec_command", command.field, command.kind)}}
			d, err := s.next(r)
			require.NoError(t, err)
			require.Equal(t, "declared_patch", d.Name)
			require.Contains(t, d.Arguments["input"], "*** Add File: "+filepath.ToSlash(filepath.Join(s.outputDir, "project", "client-result.txt")))
			require.Contains(t, d.Arguments["input"], "+ZTAPI_CLIENT_FILE_OK")
			r["messages"] = ztapiBridgeTestHistory(d, "Success. Updated client-result.txt")
			exec, err := s.next(r)
			require.NoError(t, err)
			require.Equal(t, "exec_command", exec.Name)
			if command.kind == "array" {
				require.Equal(t, []any{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Write-Output ZTAPI_CLIENT_EXEC_OK"}, exec.Arguments[command.field])
			} else {
				require.Equal(t, "echo ZTAPI_CLIENT_EXEC_OK", exec.Arguments[command.field])
			}
			r["messages"] = ztapiBridgeTestHistory(exec, "ZTAPI_CLIENT_EXEC_OK")
			final, err := s.next(r)
			require.NoError(t, err)
			require.Equal(t, "ZTAPI_CLIENT_OK", final.Text)
		})
	}
	s := &ztapiBridgeScript{profile: "codex", outputDir: t.TempDir()}
	_, err := s.next(map[string]any{"tools": []any{ztapiBridgeTestTool("arbitrary", "cmd", "string")}})
	require.EqualError(t, err, "bridge_tool_shape")
}

func TestZTAPIClientBridgeScriptClaude(t *testing.T) {
	s := &ztapiBridgeScript{profile: "claude-code", outputDir: t.TempDir()}
	write := ztapiBridgeTestTool("Write", "file_path", "string")
	write["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)["content"] = map[string]any{"type": "string"}
	write["function"].(map[string]any)["parameters"].(map[string]any)["required"] = []any{"file_path", "content"}
	r := map[string]any{"tools": []any{ztapiBridgeTestTool("Read", "file_path", "string"), write}}
	d, err := s.next(r)
	require.NoError(t, err)
	require.Equal(t, "Read", d.Name)
	require.Equal(t, filepath.Join(s.outputDir, "project", "fixture.txt"), d.Arguments["file_path"])
	r["messages"] = ztapiBridgeTestHistory(d, "ZTAPI_CLIENT_FIXTURE_OK")
	d, err = s.next(r)
	require.NoError(t, err)
	require.Equal(t, "Write", d.Name)
	require.Equal(t, "ZTAPI_CLIENT_FILE_OK\n", d.Arguments["content"])
	r["messages"] = ztapiBridgeTestHistory(d, "File created successfully: client-result.txt")
	d, err = s.next(r)
	require.NoError(t, err)
	require.Equal(t, "ZTAPI_CLIENT_OK", d.Text)
}

type ztapiBridgeDecision struct {
	Text, Name, ID string
	Arguments      map[string]any
}
type ztapiBridgeScript struct {
	profile, outputDir string
	aliases            map[string]string
	sequence           int
	pending            ztapiBridgeDecision
	stage              int
}

func (s *ztapiBridgeScript) next(r map[string]any) (ztapiBridgeDecision, error) {
	if s.profile != "sdk" {
		return s.nextClient(r)
	}
	messages, _ := r["messages"].([]any)
	for i := len(messages) - 1; i >= 0; i-- {
		result, _ := messages[i].(map[string]any)
		if result["role"] != "tool" {
			continue
		}
		for j := 0; j < i; j++ {
			m, _ := messages[j].(map[string]any)
			calls, _ := m["tool_calls"].([]any)
			for _, raw := range calls {
				call, _ := raw.(map[string]any)
				function, _ := call["function"].(map[string]any)
				var args map[string]any
				encoded, _ := function["arguments"].(string)
				_ = common.UnmarshalJsonStr(encoded, &args)
				if call["id"] == result["tool_call_id"] && function["name"] == "lookup" && args["value"] == "ZTAPI_TOOL_VALUE" && strings.Contains(ztapiBridgeText(result["content"]), "ZTAPI_TOOL_VALUE") {
					return ztapiBridgeDecision{Text: "ZTAPI_TOOL_DONE"}, nil
				}
			}
		}
		return ztapiBridgeDecision{}, errors.New("bridge_tool_result_mismatch")
	}
	tools, _ := r["tools"].([]any)
	if len(tools) == 0 {
		return ztapiBridgeDecision{Text: "ZTAPI_SDK_OK"}, nil
	}
	if len(tools) != 1 {
		return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
	}
	tool, _ := tools[0].(map[string]any)
	function, _ := tool["function"].(map[string]any)
	params, _ := function["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	value, _ := props["value"].(map[string]any)
	if function["name"] != "lookup" || params["type"] != "object" || len(props) != 1 || value["type"] != "string" {
		return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
	}
	s.sequence++
	return ztapiBridgeDecision{Name: "lookup", ID: fmt.Sprintf("call_bridge_%d", s.sequence), Arguments: map[string]any{"value": "ZTAPI_TOOL_VALUE"}}, nil
}

func (s *ztapiBridgeScript) nextClient(r map[string]any) (ztapiBridgeDecision, error) {
	if s.profile != "codex" && s.profile != "claude-code" {
		return ztapiBridgeDecision{}, errors.New("bridge_profile_rejected")
	}
	if s.pending.ID != "" {
		messages, _ := r["messages"].([]any)
		matched := false
		for i, raw := range messages {
			m, _ := raw.(map[string]any)
			if m["role"] != "tool" || m["tool_call_id"] != s.pending.ID {
				continue
			}
			text := ztapiBridgeText(m["content"])
			marker := "client-result.txt"
			if s.profile == "claude-code" && s.stage == 1 {
				marker = "ZTAPI_CLIENT_FIXTURE_OK"
			}
			if s.profile == "codex" && s.stage == 2 {
				marker = "ZTAPI_CLIENT_EXEC_OK"
			}
			callMatches := false
			for _, prior := range messages[:i] {
				assistant, _ := prior.(map[string]any)
				calls, _ := assistant["tool_calls"].([]any)
				for _, rawCall := range calls {
					call, _ := rawCall.(map[string]any)
					fn, _ := call["function"].(map[string]any)
					encoded, _ := fn["arguments"].(string)
					var args map[string]any
					if common.UnmarshalJsonStr(encoded, &args) != nil {
						continue
					}
					want, _ := common.Marshal(s.pending.Arguments)
					got, _ := common.Marshal(args)
					callMatches = callMatches || call["id"] == s.pending.ID && fn["name"] == s.pending.Name && bytes.Equal(want, got)
				}
			}
			matched = callMatches && strings.Contains(text, marker) && !strings.Contains(strings.ToLower(text), "error:") && !strings.Contains(strings.ToLower(text), "failed")
		}
		if !matched {
			return ztapiBridgeDecision{}, errors.New("bridge_tool_result_mismatch")
		}
	}
	if s.stage >= 2 {
		s.stage = 3
		s.pending = ztapiBridgeDecision{}
		return ztapiBridgeDecision{Text: "ZTAPI_CLIENT_OK"}, nil
	}
	canonical := "apply_patch"
	values := map[string]any{"input": "*** Begin Patch\n*** Add File: " + filepath.ToSlash(filepath.Join(s.outputDir, "project", "client-result.txt")) + "\n+ZTAPI_CLIENT_FILE_OK\n*** End Patch"}
	optional := false
	if s.profile == "claude-code" {
		canonical = "Read"
		values = map[string]any{"file_path": filepath.Join(s.outputDir, "project", "fixture.txt")}
		if s.stage == 1 {
			canonical = "Write"
			values = map[string]any{"file_path": filepath.Join(s.outputDir, "project", "client-result.txt"), "content": "ZTAPI_CLIENT_FILE_OK\n"}
		}
	} else if s.stage == 1 {
		canonical = "exec_command"
		optional = true
		values = nil
	}
	tools, _ := r["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		name, _ := fn["name"].(string)
		actual := name
		if alias := s.aliases[name]; alias != "" {
			actual = alias
		}
		if actual != canonical && !(optional && actual == "shell") {
			continue
		}
		params, _ := fn["parameters"].(map[string]any)
		props, _ := params["properties"].(map[string]any)
		if optional {
			values = map[string]any{}
			for _, field := range []string{"cmd", "command"} {
				shape, _ := props[field].(map[string]any)
				switch shape["type"] {
				case "string":
					values[field] = "echo ZTAPI_CLIENT_EXEC_OK"
				case "array":
					items, _ := shape["items"].(map[string]any)
					if items["type"] == "string" {
						values[field] = []any{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Write-Output ZTAPI_CLIENT_EXEC_OK"}
					}
				}
				if len(values) > 0 {
					break
				}
			}
		}
		if params["type"] != "object" || len(values) == 0 {
			return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
		}
		for field, value := range values {
			shape, _ := props[field].(map[string]any)
			if _, ok := value.(string); ok && shape["type"] != "string" {
				return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
			}
		}
		required, _ := params["required"].([]any)
		for _, field := range required {
			key, ok := field.(string)
			if !ok || values[key] == nil {
				return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
			}
		}
		s.sequence++
		d := ztapiBridgeDecision{Name: name, ID: fmt.Sprintf("call_bridge_%d", s.sequence), Arguments: values}
		s.stage++
		s.pending = d
		return d, nil
	}
	if optional {
		s.stage = 3
		s.pending = ztapiBridgeDecision{}
		return ztapiBridgeDecision{Text: "ZTAPI_CLIENT_OK"}, nil
	}
	return ztapiBridgeDecision{}, errors.New("bridge_tool_shape")
}

// Use the converter's alias, never suffix-match an undeclared function.
func ztapiBridgeAliases(raw []byte) map[string]string {
	var request struct {
		Tools []struct {
			Type, Name string
			Tools      []struct{ Name string }
		} `json:"tools"`
		Input []struct {
			Type  string
			Tools []struct {
				Type, Name string
				Tools      []struct{ Name string }
			}
		} `json:"input"`
	}
	names := map[string]string{}
	if common.Unmarshal(raw, &request) != nil {
		return names
	}
	declared := request.Tools
	for _, item := range request.Input {
		if item.Type == "additional_tools" {
			declared = append(declared, item.Tools...)
		}
	}
	for _, tool := range declared {
		if tool.Type == "namespace" {
			for _, nested := range tool.Tools {
				names[openaicompat.ResponsesChatToolName(tool.Name, nested.Name)] = nested.Name
			}
		} else {
			names[tool.Name] = tool.Name
		}
	}
	return names
}

func ztapiBridgeText(v any) string {
	if text, ok := v.(string); ok {
		return text
	}
	parts, _ := v.([]any)
	var text strings.Builder
	for _, part := range parts {
		if p, ok := part.(map[string]any); ok {
			if t, ok := p["text"].(string); ok {
				text.WriteString(t)
			}
		}
	}
	return text.String()
}

func TestZTAPIClientBridgeOwnsFixture(t *testing.T) {
	t.Run("creates synthetic fixture", func(t *testing.T) {
		cfg := ztapiClientBridgeConfig{OutputDir: filepath.Join(t.TempDir(), "run"), Profile: "claude-code", MaxDispatches: 12, TimeoutSeconds: 30}
		_, err := ztapiNewClientBridge(t, cfg)
		require.NoError(t, err)
		raw, err := os.ReadFile(filepath.Join(cfg.OutputDir, "project", "fixture.txt"))
		require.NoError(t, err)
		require.Equal(t, "ZTAPI_CLIENT_FIXTURE_OK\n", string(raw))
	})
	t.Run("never overwrites an existing fixture", func(t *testing.T) {
		cfg := ztapiClientBridgeConfig{OutputDir: filepath.Join(t.TempDir(), "run"), Profile: "claude-code", MaxDispatches: 12, TimeoutSeconds: 30}
		fixture := filepath.Join(cfg.OutputDir, "project", "fixture.txt")
		require.NoError(t, os.MkdirAll(filepath.Dir(fixture), 0700))
		require.NoError(t, os.WriteFile(fixture, []byte("preserve existing data"), 0600))
		_, err := ztapiNewClientBridge(t, cfg)
		require.Error(t, err)
		raw, err := os.ReadFile(fixture)
		require.NoError(t, err)
		require.Equal(t, "preserve existing data", string(raw))
	})
}

func TestZTAPIClientBridgeActualHTTP(t *testing.T) {
	b, err := ztapiNewClientBridge(t, ztapiClientBridgeConfig{OutputDir: filepath.Join(t.TempDir(), "run"), Profile: "sdk", MaxDispatches: 12, TimeoutSeconds: 30})
	require.NoError(t, err)
	require.NotNil(t, b)
	defer b.server.Close()
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			r := map[string]any{"model": "zt-claude-sonnet-5", "stream": stream, "max_tokens": 128, "messages": []any{map[string]any{"role": "user", "content": "synthetic"}}}
			if path == "/v1/responses" {
				delete(r, "messages")
				delete(r, "max_tokens")
				r["input"] = "synthetic"
				r["max_output_tokens"] = 128
			}
			status, body := ztapiBridgeHTTPPost(t, b, path, r, b.fixture.key)
			require.Equal(t, 200, status, "local HTTP case failed")
			require.Contains(t, body, "ZTAPI_SDK_OK")
			if stream {
				terminal := "data: [DONE]"
				if path == "/v1/responses" {
					terminal = "event: response.completed"
				}
				if path == "/v1/messages" {
					terminal = "event: message_stop"
				}
				require.Contains(t, body, terminal)
			}
		}
	}
	report := b.report()
	require.Empty(t, report.Errors)
	require.Equal(t, 6, report.Dispatches)
	require.True(t, report.AggregateChecks["wallet"])
	require.True(t, report.AggregateChecks["token"])
}

func TestZTAPIClientBridgeIdentityIsNonbillableAndMatchesReady(t *testing.T) {
	b, err := ztapiNewClientBridge(t, ztapiClientBridgeConfig{OutputDir: filepath.Join(t.TempDir(), "run"), Profile: "sdk", MaxDispatches: 1, TimeoutSeconds: 30})
	require.NoError(t, err)
	before := b.fixture.balances(t)
	require.NoError(t, ztapiBridgeWriteJSON(b.cfg.OutputDir, "ready.json", b.readiness()))
	rawReady, err := os.ReadFile(filepath.Join(b.cfg.OutputDir, "ready.json"))
	require.NoError(t, err)
	var ready struct {
		ProviderKind string `json:"provider_kind"`
		PID          int    `json:"pid"`
		RunNonce     string `json:"run_nonce"`
	}
	require.NoError(t, common.Unmarshal(rawReady, &ready))
	for _, tc := range []struct {
		name, method, path, auth string
		status                   int
	}{
		{"identity", "GET", "/__ztapi_client_bridge", "valid", http.StatusOK},
		{"repeat", "GET", "/__ztapi_client_bridge", "valid", http.StatusOK},
		{"post", "POST", "/__ztapi_client_bridge", "valid", http.StatusMethodNotAllowed},
		{"head", "HEAD", "/__ztapi_client_bridge", "valid", http.StatusMethodNotAllowed},
		{"missing-auth", "GET", "/__ztapi_client_bridge", "", http.StatusUnauthorized},
		{"bad-auth", "GET", "/__ztapi_client_bridge", "bad", http.StatusUnauthorized},
		{"query", "GET", "/__ztapi_client_bridge?model=zt-claude-sonnet-5", "valid", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"zt-claude-sonnet-5"}`))
			if tc.auth == "valid" {
				r.Header.Set("Authorization", "Bearer "+b.fixture.key)
			} else if tc.auth != "" {
				r.Header.Set("Authorization", "Bearer invalid-local-key")
			}
			w := httptest.NewRecorder()
			b.server.Config.Handler.ServeHTTP(w, r)
			require.Equal(t, tc.status, w.Code)
			require.False(t, strings.Contains(w.Body.String(), b.fixture.key), "identity response must never contain the fixture key")
			if w.Code == http.StatusOK {
				require.Equal(t, "application/json", w.Header().Get("Content-Type"))
				var identity map[string]any
				require.NoError(t, common.Unmarshal(w.Body.Bytes(), &identity))
				require.Len(t, identity, 3)
				require.Equal(t, "simulated", identity["provider_kind"])
				require.EqualValues(t, os.Getpid(), identity["pid"])
				require.Regexp(t, `^[0-9a-f]{32}$`, identity["run_nonce"])
				require.Equal(t, ready.ProviderKind, identity["provider_kind"])
				require.EqualValues(t, ready.PID, identity["pid"])
				require.Equal(t, ready.RunNonce, identity["run_nonce"])
			}
			require.Equal(t, before, b.fixture.balances(t), "identity must bypass Relay, reservations and settlement")
			require.Zero(t, b.dispatches)
			require.Zero(t, b.requests)
			require.Empty(t, b.paths)
			require.Empty(t, b.requestIDs)
		})
	}
	// Identity stays available after the inference budget is exhausted.
	b.dispatches = b.cfg.MaxDispatches
	r := httptest.NewRequest(http.MethodGet, "/__ztapi_client_bridge", nil)
	r.Header.Set("Authorization", "Bearer "+b.fixture.key)
	w := httptest.NewRecorder()
	b.server.Config.Handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, before, b.fixture.balances(t))
}

func TestZTAPIClientBridgeIdentityNonceIsPerInstance(t *testing.T) {
	var nonces []string
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			b, err := ztapiNewClientBridge(t, ztapiClientBridgeConfig{OutputDir: filepath.Join(t.TempDir(), "run"), Profile: "sdk", MaxDispatches: 1, TimeoutSeconds: 30})
			require.NoError(t, err)
			nonce, _ := b.readiness()["run_nonce"].(string)
			require.Regexp(t, `^[0-9a-f]{32}$`, nonce)
			require.Equal(t, nonce, b.readiness()["run_nonce"])
			nonces = append(nonces, nonce)
		})
	}
	require.Len(t, nonces, 2)
	require.NotEqual(t, nonces[0], nonces[1])
}

func ztapiBridgeHTTPPost(t *testing.T, b *ztapiClientBridge, path string, body any, key string) (int, string) {
	t.Helper()
	raw, err := common.Marshal(body)
	require.NoError(t, err)
	r, err := http.NewRequest(http.MethodPost, b.server.URL+path, bytes.NewReader(raw))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-Api-Key", key)
	r.Header.Set("Anthropic-Version", "2023-06-01")
	response, err := b.server.Client().Do(r)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(data)
}

type ztapiBridgeReport struct {
	ProviderKind     string          `json:"provider_kind"`
	Profile          string          `json:"profile"`
	Dispatches       int             `json:"dispatches"`
	Paths            []string        `json:"paths"`
	RequestIDs       []string        `json:"request_ids"`
	SettlementCounts map[string]int  `json:"settlement_counts"`
	AggregateChecks  map[string]bool `json:"aggregate_checks"`
	Errors           []string        `json:"errors"`
}

type ztapiClientBridge struct {
	server                               *httptest.Server
	fixture                              *ztapiE2EFixture
	cfg                                  ztapiClientBridgeConfig
	mu                                   sync.Mutex
	script                               ztapiBridgeScript
	dispatches                           int
	paths, requestIDs, toolNames, errors []string
	requests                             int
	ctx                                  context.Context
	cancel                               context.CancelFunc
	upstream                             string
	runNonce                             string
}

type ztapiBridgeTransport struct {
	bridge *ztapiClientBridge
	base   http.RoundTripper
}

func (tr ztapiBridgeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b := tr.bridge
	if err := ztapiBridgeUpstreamGuard(r.URL, b.upstream); err != nil {
		b.errors = append(b.errors, err.Error())
		return nil, err
	}
	if b.dispatches >= b.cfg.MaxDispatches {
		b.errors = append(b.errors, "bridge_dispatch_limit")
		return nil, errors.New("bridge_dispatch_limit")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		b.errors = append(b.errors, "bridge_upstream_size")
		return nil, errors.New("bridge_upstream_size")
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]any
	if common.Unmarshal(raw, &body) != nil || body["model"] != "claude-sonnet-5" {
		b.errors = append(b.errors, "bridge_upstream_model")
		return nil, errors.New("bridge_upstream_model")
	}
	d, err := b.script.next(body)
	if err != nil {
		b.errors = append(b.errors, err.Error())
		// Synthetic client metadata only, never request text, arguments or headers.
		tools, _ := body["tools"].([]any)
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			fn, _ := tool["function"].(map[string]any)
			name, _ := fn["name"].(string)
			canonical := b.script.aliases[name]
			if canonical != "apply_patch" && canonical != "exec_command" && canonical != "shell" && canonical != "shell_command" {
				continue
			}
			params, _ := fn["parameters"].(map[string]any)
			props, _ := params["properties"].(map[string]any)
			shape := map[string]any{"canonical": canonical, "parameter_type": params["type"], "required": params["required"]}
			fields := map[string]any{}
			for _, field := range []string{"input", "patch", "code", "cmd", "command", "workdir", "yield_time_ms", "max_output_tokens"} {
				if property, ok := props[field].(map[string]any); ok {
					fields[field] = property["type"]
				}
			}
			shape["fields"] = fields
			encoded, _ := common.Marshal(shape)
			b.errors = append(b.errors, "synthetic_tool_shape:"+string(encoded))
		}
		return nil, err
	}
	reply, err := ztapiBridgeReply(d, body["stream"] == true)
	if err != nil {
		b.errors = append(b.errors, "bridge_reply_encode")
		return nil, errors.New("bridge_reply_encode")
	}
	b.fixture.queue(reply)
	b.dispatches++
	if d.Name != "" {
		b.toolNames = append(b.toolNames, d.Name)
	}
	return tr.base.RoundTrip(r)
}

func ztapiBridgeUpstreamGuard(u *url.URL, allowed string) error {
	if u == nil || u.Scheme != "http" || u.Host != allowed || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Path != "/v1/chat/completions" {
		return errors.New("bridge_upstream_rejected")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return errors.New("bridge_upstream_rejected")
	}
	return nil
}

func ztapiBridgeReply(d ztapiBridgeDecision, stream bool) (ztapiE2EReply, error) {
	message := map[string]any{"role": "assistant", "content": d.Text}
	finish := "stop"
	if d.Name != "" {
		args, err := common.Marshal(d.Arguments)
		if err != nil {
			return ztapiE2EReply{}, err
		}
		message["tool_calls"] = []any{map[string]any{"id": d.ID, "type": "function", "function": map[string]any{"name": d.Name, "arguments": string(args)}}}
		finish = "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": 20, "completion_tokens": 2, "total_tokens": 22}
	base := func(object string, choices []any) map[string]any {
		return map[string]any{"id": "chatcmpl-bridge", "object": object, "created": 1, "model": "claude-sonnet-5", "choices": choices}
	}
	if !stream {
		body := base("chat.completion", []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}})
		body["usage"] = usage
		raw, err := common.Marshal(body)
		return ztapiE2EReply{status: 200, body: string(raw)}, err
	}
	var out strings.Builder
	chunk := func(delta map[string]any, end any) error {
		raw, err := common.Marshal(base("chat.completion.chunk", []any{map[string]any{"index": 0, "delta": delta, "finish_reason": end}}))
		if err == nil {
			out.WriteString("data: " + string(raw) + "\n\n")
		}
		return err
	}
	if err := chunk(map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
		return ztapiE2EReply{}, err
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		call := calls[0].(map[string]any)
		call["index"] = 0
		if err := chunk(map[string]any{"tool_calls": calls}, nil); err != nil {
			return ztapiE2EReply{}, err
		}
	} else if err := chunk(map[string]any{"content": d.Text}, nil); err != nil {
		return ztapiE2EReply{}, err
	}
	if err := chunk(map[string]any{}, finish); err != nil {
		return ztapiE2EReply{}, err
	}
	terminal := base("chat.completion.chunk", []any{})
	terminal["usage"] = usage
	raw, err := common.Marshal(terminal)
	if err != nil {
		return ztapiE2EReply{}, err
	}
	out.WriteString("data: " + string(raw) + "\n\ndata: [DONE]\n\n")
	return ztapiE2EReply{status: 200, stream: true, body: out.String()}, nil
}

func ztapiNewClientBridge(t *testing.T, cfg ztapiClientBridgeConfig) (*ztapiClientBridge, error) {
	t.Helper()
	for _, name := range []string{"ready.json", "bridge-report.json", "stop", "project", "project/fixture.txt", "project/client-result.txt"} {
		if err := ztapiBridgeOwnedPath(filepath.Dir(cfg.OutputDir), filepath.Join(cfg.OutputDir, name)); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Join(cfg.OutputDir, "project"), 0700); err != nil {
		return nil, errors.New("bridge_output_create")
	}
	fixtureFile, err := os.OpenFile(filepath.Join(cfg.OutputDir, "project", "fixture.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("bridge_fixture_exists_or_denied")
	}
	_, writeErr := fixtureFile.WriteString("ZTAPI_CLIENT_FIXTURE_OK\n")
	closeErr := fixtureFile.Close()
	if writeErr != nil || closeErr != nil {
		return nil, errors.New("bridge_fixture_write")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("bridge_nonce_generation")
	}
	f := newZTAPIHealthE2EFixtureForModel(t, "zt-claude-sonnet-5", "claude-sonnet-5")
	f.router.POST("/v1/messages", func(c *gin.Context) { Relay(c, types.RelayFormatClaude) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	b := &ztapiClientBridge{fixture: f, cfg: cfg, ctx: ctx, cancel: cancel, runNonce: hex.EncodeToString(nonce[:]), script: ztapiBridgeScript{profile: cfg.Profile, outputDir: cfg.OutputDir}}
	t.Cleanup(cancel)
	var channel model.Channel
	if f.db.First(&channel, 1).Error != nil || channel.BaseURL == nil {
		return nil, errors.New("bridge_fixture_channel")
	}
	u, err := url.Parse(*channel.BaseURL)
	if err != nil {
		return nil, errors.New("bridge_fixture_upstream")
	}
	b.upstream = u.Host
	if err := ztapiBridgeUpstreamGuard(&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/v1/chat/completions"}, b.upstream); err != nil {
		return nil, err
	}
	client := service.GetHttpClient()
	client.Transport = ztapiBridgeTransport{bridge: b, base: client.Transport}
	b.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.ctx.Err() != nil {
			http.Error(w, "bridge_stopped", 503)
			return
		}
		// Identity never enters authentication/distribution/Relay or its billing path.
		if r.URL.Path == "/__ztapi_client_bridge" {
			if r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.RawQuery != "" {
				http.Error(w, "bridge_endpoint_rejected", http.StatusBadRequest)
				return
			}
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, "bridge_identity_method", http.StatusMethodNotAllowed)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+b.fixture.key {
				http.Error(w, "bridge_identity_auth", http.StatusUnauthorized)
				return
			}
			raw, err := common.Marshal(map[string]any{"provider_kind": "simulated", "pid": os.Getpid(), "run_nonce": b.runNonce})
			if err != nil {
				http.Error(w, "bridge_identity_encode", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(raw)
			return
		}
		raw, err := ztapiClientBridgeRequest(r)
		if err != nil {
			pathClass := "other"
			if r.URL.Path == "/v1/messages" {
				pathClass = "messages"
			}
			b.errors = append(b.errors, fmt.Sprintf("request_rejected:%s:query_count=%d:beta_true=%t", pathClass, len(r.URL.Query()), r.URL.Query().Get("beta") == "true"))
			http.Error(w, err.Error(), 400)
			return
		}
		if b.dispatches >= cfg.MaxDispatches {
			http.Error(w, "bridge_dispatch_limit", 429)
			return
		}
		b.requests++
		b.script.aliases = ztapiBridgeAliases(raw)
		requestID := fmt.Sprintf("bridge-request-%d", b.requests)
		r.Header.Set("X-Request-ID", requestID)
		b.paths = append(b.paths, r.URL.Path)
		b.requestIDs = append(b.requestIDs, requestID)
		f.router.ServeHTTP(w, r)
	}))
	b.server.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	b.server.Config.ReadHeaderTimeout = 2 * time.Second
	b.server.Config.ReadTimeout = 5 * time.Second
	b.server.Config.WriteTimeout = 8 * time.Second
	b.server.Config.IdleTimeout = 2 * time.Second
	address, ok := b.server.Listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		_ = b.server.Listener.Close()
		return nil, errors.New("bridge_listener_not_loopback")
	}
	b.server.Start()
	t.Cleanup(b.server.Close)
	return b, nil
}

func (b *ztapiClientBridge) report() ztapiBridgeReport {
	b.mu.Lock()
	defer b.mu.Unlock()
	report := ztapiBridgeReport{ProviderKind: "simulated", Profile: b.cfg.Profile, Dispatches: b.dispatches, Paths: append([]string{}, b.paths...), RequestIDs: append([]string{}, b.requestIDs...), Errors: append([]string{}, b.errors...), SettlementCounts: map[string]int{}, AggregateChecks: map[string]bool{}}
	var rows []model.ZTAPIRequestSettlement
	var user model.User
	var token model.Token
	if b.fixture.db.Order("id").Find(&rows).Error != nil || b.fixture.db.First(&user, b.fixture.user.Id).Error != nil || b.fixture.db.First(&token, b.fixture.token.Id).Error != nil {
		report.Errors = append(report.Errors, "bridge_accounting_read")
		return report
	}
	charged := int64(0)
	dispatched, settled := 0, 0
	valid := true
	for _, row := range rows {
		report.SettlementCounts[row.Status]++
		charged += row.ChargedQuota
		if row.Dispatched {
			dispatched++
		}
		if row.Status == model.ZTAPISettlementSettled {
			settled++
			valid = valid && row.ChargedQuota == 20 && row.TokenChargedQuota == row.ChargedQuota && row.FinalAttempt == 1
		} else {
			valid = valid && row.Status == model.ZTAPISettlementReleased && row.ChargedQuota == 0 && row.TokenChargedQuota == 0
		}
		var reservations int64
		if b.fixture.db.Model(&model.BalanceLedger{}).Where("request_id = ? AND source_type = ?", row.RequestID, model.BalanceLedgerSourceUsageReservation).Count(&reservations).Error != nil || reservations != 1 {
			valid = false
		}
	}
	var attempts, logs int64
	if b.fixture.db.Model(&model.ZTAPIRequestAttempt{}).Count(&attempts).Error != nil || b.fixture.db.Model(&model.Log{}).Where("type = ?", model.LogTypeConsume).Count(&logs).Error != nil {
		valid = false
	}
	report.AggregateChecks["dispatches"] = int64(b.dispatches) == b.fixture.calls.Load() && dispatched == b.dispatches && attempts == int64(b.dispatches)
	report.AggregateChecks["settlements"] = valid && settled == b.dispatches && logs == int64(settled)
	report.AggregateChecks["wallet"] = int64(user.Quota) == ztapiE2EInitialQuota-charged
	report.AggregateChecks["token"] = token.RemainQuota == user.Quota && int64(token.UsedQuota) == charged
	for name, ok := range report.AggregateChecks {
		if !ok {
			report.Errors = append(report.Errors, "bridge_aggregate_"+name)
		}
	}
	return report
}

func ztapiBridgeWriteJSON(root, name string, v any) error {
	path := filepath.Join(root, name)
	if err := ztapiBridgeOwnedPath(root, path); err != nil {
		return err
	}
	raw, err := common.Marshal(v)
	if err != nil {
		return errors.New("bridge_output_encode")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("bridge_output_exists_or_denied")
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("bridge_output_write")
	}
	return nil
}

func (b *ztapiClientBridge) readiness() map[string]any {
	return map[string]any{"base_url": b.server.URL, "api_key": b.fixture.key, "model": "zt-claude-sonnet-5", "pid": os.Getpid(), "provider_kind": "simulated", "run_nonce": b.runNonce}
}

func TestZTAPIClientAcceptanceBridge(t *testing.T) {
	path := os.Getenv("ZTAPI_CLIENT_BRIDGE_CONFIG")
	if path == "" {
		t.Skip("opt-in local simulated HTTP bridge")
	}
	cfg, err := ztapiLoadClientBridgeConfig(path)
	require.NoError(t, err)
	b, err := ztapiNewClientBridge(t, cfg)
	require.NoError(t, err)
	require.NoError(t, ztapiBridgeWriteJSON(cfg.OutputDir, "ready.json", b.readiness()))
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	stopped := false
	for !stopped {
		select {
		case <-b.ctx.Done():
			b.mu.Lock()
			b.errors = append(b.errors, "bridge_deadline")
			b.mu.Unlock()
			stopped = true
		case <-ticker.C:
			stop := filepath.Join(cfg.OutputDir, "stop")
			if err := ztapiBridgeOwnedPath(cfg.OutputDir, stop); err != nil {
				b.mu.Lock()
				b.errors = append(b.errors, "bridge_stop_path")
				b.mu.Unlock()
				stopped = true
				break
			}
			if info, err := os.Lstat(stop); err == nil {
				if !info.Mode().IsRegular() {
					b.mu.Lock()
					b.errors = append(b.errors, "bridge_stop_not_file")
					b.mu.Unlock()
				}
				stopped = true
			} else if !os.IsNotExist(err) {
				b.mu.Lock()
				b.errors = append(b.errors, "bridge_stop_stat")
				b.mu.Unlock()
				stopped = true
			}
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if b.server.Config.Shutdown(shutdown) != nil {
		b.server.CloseClientConnections()
	}
	b.cancel()
	b.server.Close()
	report := b.report()
	require.NoError(t, ztapiBridgeWriteJSON(cfg.OutputDir, "bridge-report.json", report))
	require.Empty(t, report.Errors, "simulated bridge receipt failed")
}

func ztapiLoadClientBridgeConfig(path string) (ztapiClientBridgeConfig, error) {
	var cfg ztapiClientBridgeConfig
	abs, err := filepath.Abs(path)
	if err != nil {
		return cfg, errors.New("bridge_config_path")
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return cfg, errors.New("bridge_config_file")
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return cfg, errors.New("bridge_config_root")
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return cfg, errors.New("bridge_config_read")
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil {
		return cfg, errors.New("bridge_config_schema")
	}
	var fields map[string]any
	if common.Unmarshal(raw, &fields) != nil || len(fields) != 4 {
		return cfg, errors.New("bridge_config_schema")
	}
	for _, key := range []string{"output_dir", "profile", "max_dispatches", "timeout_seconds"} {
		if fields[key] == nil {
			return cfg, errors.New("bridge_config_schema")
		}
	}
	if common.DecodeJsonStrict(bytes.NewReader(raw), &cfg) != nil {
		return cfg, errors.New("bridge_config_schema")
	}
	if cfg.Profile != "sdk" && cfg.Profile != "codex" && cfg.Profile != "claude-code" {
		return cfg, errors.New("bridge_config_profile")
	}
	if cfg.MaxDispatches < 1 || cfg.MaxDispatches > 16 || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 180 {
		return cfg, errors.New("bridge_config_limits")
	}
	if strings.TrimSpace(cfg.OutputDir) == "" {
		return cfg, errors.New("bridge_output_path")
	}
	if !filepath.IsAbs(cfg.OutputDir) {
		cfg.OutputDir = filepath.Join(root, cfg.OutputDir)
	}
	cfg.OutputDir = filepath.Clean(cfg.OutputDir)
	if err := ztapiBridgeOwnedPath(root, cfg.OutputDir); err != nil {
		return cfg, err
	}
	for _, name := range []string{"ready.json", "bridge-report.json", "stop", "project", "project/fixture.txt", "project/client-result.txt"} {
		if err := ztapiBridgeOwnedPath(cfg.OutputDir, filepath.Join(cfg.OutputDir, name)); err != nil {
			return cfg, err
		}
	}
	for _, name := range []string{"ready.json", "bridge-report.json", "stop"} {
		if _, err := os.Lstat(filepath.Join(cfg.OutputDir, name)); err == nil || !os.IsNotExist(err) {
			return cfg, errors.New("bridge_output_not_fresh")
		}
	}
	return cfg, nil
}

// Check existing ancestors too: a lexical child may cross a symlink/junction.
func ztapiBridgeOwnedPath(root, path string) error {
	root, path = filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("bridge_output_escape")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(p)
			same := resolved == p
			if runtime.GOOS == "windows" {
				same = strings.EqualFold(resolved, p)
			}
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !same {
				return errors.New("bridge_output_link")
			}
		} else if !os.IsNotExist(err) {
			return errors.New("bridge_output_stat")
		}
		if p == root {
			break
		}
		if filepath.Dir(p) == p {
			return errors.New("bridge_output_escape")
		}
	}
	return nil
}

func ztapiClientBridgeRequest(r *http.Request) ([]byte, error) {
	if r.Method != http.MethodPost || r.URL.IsAbs() || r.URL.RawPath != "" {
		return nil, errors.New("bridge_endpoint_rejected")
	}
	if r.URL.RawQuery != "" {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.URL.Path != "/v1/messages" || len(query) != 1 || len(query["beta"]) != 1 || query.Get("beta") != "true" {
			return nil, errors.New("bridge_endpoint_rejected")
		}
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages":
	default:
		return nil, errors.New("bridge_endpoint_rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, errors.New("bridge_request_size")
	}
	var body struct {
		Model string `json:"model"`
	}
	if common.Unmarshal(raw, &body) != nil || body.Model != "zt-claude-sonnet-5" {
		return nil, errors.New("bridge_model_rejected")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}
