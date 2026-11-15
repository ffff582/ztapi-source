package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		"UPSTREAM_MODELS_URL": "http://main-api:3000/v1/models",
		"MODEL_INFO_PATH":     "/config/model-info.json",
	}
	for k, v := range overrides {
		values[k] = v
	}
	return func(k string) string { return values[k] }
}

func TestConfigDefaultsAndTrustedPeers(t *testing.T) {
	cfg, err := readConfig(testEnv(map[string]string{
		"TRUSTED_PROXY_CIDRS": "172.20.0.7/32,2001:db8::7/128",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listenAddr != ":8080" || cfg.upstream.String() != "http://main-api:3000/v1/models" || len(cfg.trustedProxies) != 2 {
		t.Fatalf("unexpected configuration: %+v", cfg)
	}
	cfg, err = readConfig(testEnv(nil))
	if err != nil || len(cfg.trustedProxies) != 0 {
		t.Fatal("an empty trust list must trust no forwarding headers")
	}
}

func TestConfigRejectsUnsafeUpstreamAndProxyConfiguration(t *testing.T) {
	for _, raw := range []string{
		"", "ftp://main-api/v1/models", "//main-api/v1/models",
		"http://user:SECRET@main-api/v1/models", "http://main-api/v1/models?token=SECRET",
		"http://main-api/v1/models?", "http://main-api/v1/models#fragment",
		"http://main-api/v1/models#", "http://main-api/v1/chat/completions",
		"http://main-api/v1/responses", "http://main-api/v1/models/",
		"http://main-api/v1/%6dodels", "http://main-api/v1/../v1/models",
		"http://main-api:bad/v1/models", "http://main-api:0/v1/models",
		"http://main-api:65536/v1/models", "http://main-api:/v1/models",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := readConfig(testEnv(map[string]string{"UPSTREAM_MODELS_URL": raw}))
			if err == nil {
				t.Fatal("unsafe upstream accepted")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("configuration error leaked a credential")
			}
		})
	}
	for _, trust := range []string{"garbage", "172.20.0.7", "172.20.0.7/32,", "0.0.0.0/0", "::/0", "::ffff:172.20.0.7/128"} {
		if _, err := readConfig(testEnv(map[string]string{"TRUSTED_PROXY_CIDRS": trust})); err == nil {
			t.Errorf("unsafe trust list accepted: %q", trust)
		}
	}
	for _, overrides := range []map[string]string{
		{"MODEL_INFO_PATH": ""}, {"LISTEN_ADDR": "http://localhost:8080"},
		{"LISTEN_ADDR": ":0"}, {"LISTEN_ADDR": ":65536"},
	} {
		if _, err := readConfig(testEnv(overrides)); err == nil {
			t.Errorf("invalid configuration accepted: %v", overrides)
		}
	}
}

func TestConfigAcceptsHTTPSAndExplicitListenAddress(t *testing.T) {
	cfg, err := readConfig(testEnv(map[string]string{
		"UPSTREAM_MODELS_URL": "https://[fd00::2]:8443/v1/models",
		"LISTEN_ADDR":         "127.0.0.1:8090",
	}))
	if err != nil || cfg.listenAddr != "127.0.0.1:8090" {
		t.Fatalf("valid configuration rejected: %v", err)
	}
}

// This is deliberately a test-only fixture, not deployable model metadata.
const modelFixture = `{"slug":"zt-claude-opus-5","base_instructions":"TEST ONLY: full fixture instructions\nkeep unchanged","context_window":12345,"future_field":{"keep":true},"web_search_tool_type":"disabled"}`

func TestModelInfoPreservesSuppliedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.json")
	input := " \n" + modelFixture + "\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	entry, err := loadModelInfo(path)
	if err != nil || string(entry) != strings.TrimSpace(input) {
		t.Fatalf("metadata was rewritten or rejected: %v", err)
	}
}

func TestModelInfoFailsClosedWithoutFabricatedDefaults(t *testing.T) {
	for _, input := range []string{
		`null`, `[]`, `{}`, `{"slug":"other","base_instructions":"SECRET"}`,
		`{"slug":"zt-claude-opus-5"}`, `{"slug":"zt-claude-opus-5","base_instructions":" "}`,
		`{"slug":"zt-claude-opus-5","base_instructions":5}`, modelFixture + `{}`,
		`{"slug":"zt-claude-opus-5","base_instructions":"` + string([]byte{0xff}) + `"}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		path := filepath.Join(t.TempDir(), "model.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadModelInfo(path)
		if err == nil {
			t.Error("invalid metadata accepted")
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("metadata error leaked file contents")
		}
	}
	if _, err := loadModelInfo(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing metadata must prevent startup")
	}
}
