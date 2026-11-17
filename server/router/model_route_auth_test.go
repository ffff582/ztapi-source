package router

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

type modelRouteAuthFixture struct {
	engine    *gin.Engine
	plaintext string
	logs      *bytes.Buffer
	protocol  string
	metadata  string
}

func setupModelRouteAuthFixture(t *testing.T) *modelRouteAuthFixture {
	t.Helper()

	initializeRouterRelayTestColumnNames(t)
	db, plaintext := setupTrustedProxyTokenTest(t, "203.0.113.10/32")
	if err := db.AutoMigrate(
		&model.Channel{},
		&model.Ability{},
		&model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{},
		&model.ZTAPIModelPublicationSnapshot{},
	); err != nil {
		t.Fatalf("migrate model route tables: %v", err)
	}
	ratio_setting.InitRatioSettings()
	priority := int64(0)
	weight := uint(100)
	channel := &model.Channel{
		Type: constant.ChannelTypeOpenAI, Key: "model-route-publication-test-key", Status: common.ChannelStatusEnabled,
		Name: "model-route-publication-test", Weight: &weight, CreatedTime: common.GetTimestamp(),
		Models: "gpt-5.5", Group: "default", Priority: &priority,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create model route publication channel: %v", err)
	}
	if err := db.Create(&model.Ability{
		Group: "default", Model: "gpt-5.5", ChannelId: channel.Id,
		Enabled: true, Priority: &priority, Weight: weight,
	}).Error; err != nil {
		t.Fatalf("create model route publication ability: %v", err)
	}
	seedRouterPublicModel(t, db, channel.Id, "gpt-5.5", "zt-gpt-5.5")

	var token model.Token
	if err := db.Where("key_hash = ?", common.HashZTAPIKey(plaintext)).First(&token).Error; err != nil {
		t.Fatalf("load model route token: %v", err)
	}
	if err := db.Model(&token).Updates(map[string]any{
		"model_limits_enabled": true,
		"model_limits":         "zt-gpt-5.5",
	}).Error; err != nil {
		t.Fatalf("set model route token limit: %v", err)
	}

	logs := &bytes.Buffer{}
	previousWriter := gin.DefaultWriter
	gin.DefaultWriter = logs
	t.Cleanup(func() {
		gin.DefaultWriter = previousWriter
	})

	fixture := &modelRouteAuthFixture{
		engine:    gin.New(),
		plaintext: plaintext,
		logs:      logs,
	}
	if err := ConfigureTrustedProxies(fixture.engine, "10.20.0.0/16", true); err != nil {
		t.Fatalf("configure model route proxies: %v", err)
	}
	middleware.SetUpLogger(fixture.engine)
	fixture.engine.Use(func(c *gin.Context) {
		c.Next()

		if value, exists := c.Get("ztapi_authenticated_protocol"); exists {
			fixture.protocol = fmt.Sprint(value)
		}
		info := relaycommon.GenRelayInfoOpenAI(c, nil)
		overrideContext := relaycommon.BuildParamOverrideContext(info)
		serialized, err := common.Marshal(map[string]any{
			"url":              c.Request.URL.String(),
			"request_uri":      c.Request.RequestURI,
			"headers":          c.Request.Header,
			"context":          c.Keys,
			"relay_info":       info,
			"override_context": overrideContext,
		})
		if err != nil {
			t.Fatalf("serialize model route metadata: %v", err)
		}
		fixture.metadata = string(serialized)
	})
	SetRelayRouter(fixture.engine)
	return fixture
}

func (fixture *modelRouteAuthFixture) request(
	t *testing.T,
	target string,
	configure func(*http.Request, string),
) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.RemoteAddr = "203.0.113.10:43123"
	if configure != nil {
		configure(request, fixture.plaintext)
	}
	recorder := httptest.NewRecorder()
	fixture.engine.ServeHTTP(recorder, request)
	return recorder
}

func assertModelRouteSecretsAbsent(
	t *testing.T,
	fixture *modelRouteAuthFixture,
	recorder *httptest.ResponseRecorder,
	secrets ...string,
) {
	t.Helper()

	serialized := fixture.logs.String() + fixture.metadata + recorder.Body.String()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(serialized, secret) {
			t.Fatalf("model route leaked plaintext credential")
		}
	}
}

func TestModelRoutesPreserveAuthenticatedProtocolAfterCredentialScrubbing(t *testing.T) {
	tests := []struct {
		name          string
		target        string
		wantProtocol  string
		wantBody      string
		forbiddenBody string
		configure     func(*http.Request, string)
	}{
		{
			name:         "Anthropic native list",
			target:       "/v1/models",
			wantProtocol: "anthropic",
			wantBody:     `"first_id"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
				request.Header.Set("anthropic-version", "2023-06-01")
			},
		},
		{
			name:          "Anthropic native retrieve",
			target:        "/v1/models/zt-gpt-5.5",
			wantProtocol:  "anthropic",
			wantBody:      `"display_name"`,
			forbiddenBody: `"owned_by"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
				request.Header.Set("anthropic-version", "2023-06-01")
			},
		},
		{
			name:         "Gemini native list query",
			target:       "/v1/models",
			wantProtocol: "gemini",
			wantBody:     `"models"`,
			configure: func(request *http.Request, key string) {
				query := request.URL.Query()
				query.Set("key", key)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:          "Gemini native retrieve header",
			target:        "/v1/models/zt-gpt-5.5",
			wantProtocol:  "gemini",
			wantBody:      `"displayName"`,
			forbiddenBody: `"owned_by"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-goog-api-key", key)
			},
		},
		{
			name:         "Authorization fallback list",
			target:       "/v1/models",
			wantProtocol: "openai",
			wantBody:     `"object":"list"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:         "Authorization fallback retrieve",
			target:       "/v1/models/zt-gpt-5.5",
			wantProtocol: "openai",
			wantBody:     `"owned_by"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:         "matching Anthropic duplicate",
			target:       "/v1/models",
			wantProtocol: "anthropic",
			wantBody:     `"first_id"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
				request.Header.Set("anthropic-version", "2023-06-01")
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:          "matching Gemini duplicates",
			target:        "/v1/models/zt-gpt-5.5",
			wantProtocol:  "gemini",
			wantBody:      `"displayName"`,
			forbiddenBody: `"owned_by"`,
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-goog-api-key", key)
				request.Header.Set("Authorization", "Bearer "+key)
				query := request.URL.Query()
				query.Set("key", key)
				request.URL.RawQuery = query.Encode()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupModelRouteAuthFixture(t)
			recorder := fixture.request(t, test.target, test.configure)

			if recorder.Code != http.StatusOK {
				t.Fatalf("model route status=%d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			if fixture.protocol != test.wantProtocol {
				t.Fatalf("authenticated protocol=%q, want %q", fixture.protocol, test.wantProtocol)
			}
			if !strings.Contains(recorder.Body.String(), test.wantBody) {
				t.Fatalf("model route body=%s, want marker %s", recorder.Body.String(), test.wantBody)
			}
			if test.forbiddenBody != "" && strings.Contains(recorder.Body.String(), test.forbiddenBody) {
				t.Fatalf("model route body=%s, forbids marker %s", recorder.Body.String(), test.forbiddenBody)
			}
			assertModelRouteSecretsAbsent(t, fixture, recorder, fixture.plaintext)
		})
	}
}

func TestModelRoutesRejectConflictingApplicableCredentials(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		configure func(*http.Request, string, string)
	}{
		{
			name:   "exact model list Gemini query conflicts with Authorization",
			target: "/v1/models",
			configure: func(request *http.Request, valid string, conflicting string) {
				request.Header.Set("Authorization", "Bearer "+valid)
				query := request.URL.Query()
				query.Set("key", conflicting)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:   "model retrieve Anthropic native conflicts with Authorization",
			target: "/v1/models/zt-gpt-5.5",
			configure: func(request *http.Request, valid string, conflicting string) {
				request.Header.Set("Authorization", "Bearer "+valid)
				request.Header.Set("x-api-key", conflicting)
				request.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupModelRouteAuthFixture(t)
			conflicting, _, _, err := common.GenerateZTAPIKey()
			if err != nil {
				t.Fatalf("generate conflicting key: %v", err)
			}
			recorder := fixture.request(t, test.target, func(request *http.Request, valid string) {
				test.configure(request, valid, conflicting)
			})

			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("conflicting model route status=%d, want 401: %s", recorder.Code, recorder.Body.String())
			}
			assertModelRouteSecretsAbsent(t, fixture, recorder, fixture.plaintext, conflicting)
		})
	}
}

func TestExactModelsQueryCredentialIsScrubbedBeforeLoggerAndMetadata(t *testing.T) {
	fixture := setupModelRouteAuthFixture(t)
	recorder := fixture.request(t, "/v1/models?alt=json", func(request *http.Request, key string) {
		request.Header.Set("Authorization", "Bearer "+key)
		query := request.URL.Query()
		query.Set("key", key)
		request.URL.RawQuery = query.Encode()
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("matching query credential status=%d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if fixture.protocol != "gemini" {
		t.Fatalf("authenticated protocol=%q, want Gemini", fixture.protocol)
	}
	if !strings.Contains(recorder.Body.String(), `"models"`) {
		t.Fatalf("Gemini list handler was not selected: %s", recorder.Body.String())
	}
	if strings.Contains(fixture.metadata, "key=") || !strings.Contains(fixture.metadata, "alt=json") {
		t.Fatalf("exact model query was not selectively scrubbed: %s", fixture.metadata)
	}
	assertModelRouteSecretsAbsent(t, fixture, recorder, fixture.plaintext)
}
