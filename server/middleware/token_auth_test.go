package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type relaySecurityFixture struct {
	db        *gorm.DB
	plaintext string
	token     *model.Token
	user      *model.User
	channel   *model.Channel
}

func setupRelaySecurityFixture(t *testing.T, allowedModel string) relaySecurityFixture {
	t.Helper()

	gin.SetMode(gin.TestMode)
	initializeMiddlewareTestColumnNames(t)

	originalRedisEnabled := common.RedisEnabled
	originalMemoryCacheEnabled := common.MemoryCacheEnabled
	originalBatchUpdateEnabled := common.BatchUpdateEnabled
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	t.Cleanup(func() {
		common.RedisEnabled = originalRedisEnabled
		common.MemoryCacheEnabled = originalMemoryCacheEnabled
		common.BatchUpdateEnabled = originalBatchUpdateEnabled
	})

	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_pragma=busy_timeout(5000)",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(
		&model.User{},
		&model.Token{},
		&model.Channel{},
		&model.Ability{},
		&model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{},
		&model.ZTAPIModelPublicationSnapshot{},
	); err != nil {
		t.Fatalf("migrate relay security tables: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	ratio_setting.InitRatioSettings()
	if err := i18n.Init(); err != nil {
		t.Fatalf("initialize i18n: %v", err)
	}
	user := &model.User{
		Username: "relay-user-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Password: "not-used",
		Status:   common.UserStatusEnabled,
		Quota:    1_000_000,
		Group:    "default",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	plaintext, keyHash, keyPrefix, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate ZTAPI key: %v", err)
	}
	token := &model.Token{
		UserId:             user.Id,
		KeyHash:            keyHash,
		KeyPrefix:          keyPrefix,
		Status:             common.TokenStatusEnabled,
		Name:               "relay-security",
		CreatedTime:        common.GetTimestamp(),
		AccessedTime:       common.GetTimestamp(),
		ExpiredTime:        -1,
		RemainQuota:        100,
		UnlimitedQuota:     false,
		ModelLimitsEnabled: true,
		ModelLimits:        allowedModel,
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}

	priority := int64(0)
	weight := uint(100)
	channel := &model.Channel{
		Type:        constant.ChannelTypeOpenAI,
		Key:         "deterministic-local-test-key",
		Status:      common.ChannelStatusEnabled,
		Name:        "deterministic-local-test-channel",
		Weight:      &weight,
		CreatedTime: common.GetTimestamp(),
		Models:      allowedModel,
		Group:       "default",
		Priority:    &priority,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ability := &model.Ability{
		Group:     "default",
		Model:     allowedModel,
		ChannelId: channel.Id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}
	if err := db.Create(ability).Error; err != nil {
		t.Fatalf("create ability: %v", err)
	}

	return relaySecurityFixture{
		db:        db,
		plaintext: plaintext,
		token:     token,
		user:      user,
		channel:   channel,
	}
}

func publishRelaySecurityAlias(t *testing.T, db *gorm.DB, channelID int, publicName, sourceModel string) {
	t.Helper()
	publication := model.ZTAPIModelConfig{
		SourceModel:           sourceModel,
		PublicName:            &publicName,
		Family:                model.ZTAPIModelFamilyOpenAI,
		Protocol:              model.ZTAPIProtocolOpenAICompatible,
		ProviderFamily:        model.ZTAPIProviderOpenAI,
		InputCostPerMillion:   1,
		OutputCostPerMillion:  2,
		InputPricePerMillion:  1.6666666667,
		OutputPricePerMillion: 3.3333333333,
		CacheReadRatio:        0.1,
		CacheCreationRatio:    1.25,
		CacheCreation5mRatio:  1.25,
		CacheCreation1hRatio:  2,
		ImageRatio:            1,
		AudioRatio:            1,
		AudioCompletionRatio:  2,
		EnabledGroups:         `["default"]`,
		Published:             true,
		Version:               1,
		CreatedAt:             common.GetTimestamp(),
		UpdatedAt:             common.GetTimestamp(),
	}
	if err := db.Create(&publication).Error; err != nil {
		t.Fatalf("publish relay security alias: %v", err)
	}
	seedMiddlewarePublicationSnapshot(t, db, &publication, []int{channelID})
	model.InvalidateZTAPIAliasCache()
}

func initializeMiddlewareTestColumnNames(t *testing.T) {
	t.Helper()

	originalIsMasterNode := common.IsMasterNode
	originalSQLitePath := common.SQLitePath
	originalUsingSQLite := common.UsingSQLite
	originalUsingMySQL := common.UsingMySQL
	originalUsingPostgreSQL := common.UsingPostgreSQL
	originalSQLDSN, hadSQLDSN := os.LookupEnv("SQL_DSN")
	defer func() {
		common.IsMasterNode = originalIsMasterNode
		common.SQLitePath = originalSQLitePath
		common.UsingSQLite = originalUsingSQLite
		common.UsingMySQL = originalUsingMySQL
		common.UsingPostgreSQL = originalUsingPostgreSQL
		if hadSQLDSN {
			_ = os.Setenv("SQL_DSN", originalSQLDSN)
		} else {
			_ = os.Unsetenv("SQL_DSN")
		}
	}()

	common.IsMasterNode = false
	common.SQLitePath = fmt.Sprintf(
		"file:%s_init?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"),
	)
	common.UsingSQLite = false
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	if err := os.Setenv("SQL_DSN", "local"); err != nil {
		t.Fatalf("set SQL_DSN: %v", err)
	}
	if err := model.InitDB(); err != nil {
		t.Fatalf("initialize model column names: %v", err)
	}
	if model.DB != nil {
		sqlDB, err := model.DB.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	}
}

func performTokenAuthRequest(
	t *testing.T,
	method string,
	target string,
	remoteAddr string,
	configure func(*http.Request),
	readOnly bool,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	var called atomic.Bool
	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	auth := TokenAuth()
	if readOnly {
		auth = TokenAuthReadOnly()
	}
	engine.Handle(method, targetPath(target), auth, func(c *gin.Context) {
		called.Store(true)
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(method, target, nil)
	request.RemoteAddr = remoteAddr
	if configure != nil {
		configure(request)
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder, called.Load()
}

func targetPath(target string) string {
	if index := strings.IndexByte(target, '?'); index >= 0 {
		return target[:index]
	}
	return target
}

func performRelaySecurityRequest(
	t *testing.T,
	plaintext string,
	modelName string,
	remoteAddr string,
) (*httptest.ResponseRecorder, bool, string, string) {
	t.Helper()

	var called atomic.Bool
	var originalModel string
	var selectionModel string
	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	engine.POST(
		"/v1/chat/completions",
		TokenAuth(),
		Distribute(),
		func(c *gin.Context) {
			called.Store(true)
			originalModel = c.GetString("original_model")
			info := relaycommon.GenRelayInfoOpenAI(c, nil)
			selectionModel = info.SelectionModelName
			c.Status(http.StatusNoContent)
		},
	)

	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"test"}]}`, modelName))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+plaintext)
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder, called.Load(), originalModel, selectionModel
}

func TestTokenAuthAcceptsCompleteZTAPICredentialTransports(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		configure func(*http.Request, string)
	}{
		{
			name:   "authorization bearer",
			method: http.MethodPost,
			target: "/v1/chat/completions",
			configure: func(request *http.Request, key string) {
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:   "anthropic x-api-key",
			method: http.MethodPost,
			target: "/v1/messages",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
			},
		},
		{
			name:   "gemini x-goog-api-key",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-goog-api-key", key)
			},
		},
		{
			name:   "gemini query key",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent?key=placeholder",
			configure: func(request *http.Request, key string) {
				query := request.URL.Query()
				query.Set("key", key)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:   "websocket subprotocol",
			method: http.MethodGet,
			target: "/v1/realtime",
			configure: func(request *http.Request, key string) {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
				request.Header.Set(
					"Sec-WebSocket-Protocol",
					"realtime, openai-insecure-api-key."+key+", openai-beta.realtime-v1",
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")
			recorder, called := performTokenAuthRequest(
				t,
				test.method,
				test.target,
				"203.0.113.10:43123",
				func(request *http.Request) {
					test.configure(request, fixture.plaintext)
				},
				false,
			)
			if !called || recorder.Code != http.StatusNoContent {
				t.Fatalf("credential transport rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestTokenAuthScrubsCredentialsBeforeLoggerAndRelayMetadata(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		configure func(*http.Request, string)
	}{
		{
			name:   "authorization bearer",
			method: http.MethodPost,
			target: "/v1/chat/completions",
			configure: func(request *http.Request, key string) {
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:   "anthropic x-api-key",
			method: http.MethodPost,
			target: "/v1/messages",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
			},
		},
		{
			name:   "gemini x-goog-api-key",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-goog-api-key", key)
			},
		},
		{
			name:   "gemini query key",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent?alt=sse",
			configure: func(request *http.Request, key string) {
				query := request.URL.Query()
				query.Set("key", key)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:   "websocket subprotocol",
			method: http.MethodGet,
			target: "/v1/realtime",
			configure: func(request *http.Request, key string) {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
				request.Header.Set(
					"Sec-WebSocket-Protocol",
					"realtime, openai-insecure-api-key."+key+", openai-beta.realtime-v1",
				)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")

			var logs bytes.Buffer
			previousWriter := gin.DefaultWriter
			gin.DefaultWriter = &logs
			t.Cleanup(func() {
				gin.DefaultWriter = previousWriter
			})

			var captured string
			var sourcesScrubbed bool
			engine := gin.New()
			if err := engine.SetTrustedProxies(nil); err != nil {
				t.Fatalf("disable trusted proxies: %v", err)
			}
			SetUpLogger(engine)
			engine.Handle(test.method, targetPath(test.target), TokenAuth(), func(c *gin.Context) {
				info := relaycommon.GenRelayInfoOpenAI(c, nil)
				overrideContext := relaycommon.BuildParamOverrideContext(info)
				sourcesScrubbed =
					c.Request.Header.Get("Authorization") == "" &&
						c.Request.Header.Get("x-api-key") == "" &&
						c.Request.Header.Get("x-goog-api-key") == "" &&
						!strings.Contains(c.Request.Header.Get("Sec-WebSocket-Protocol"), "openai-insecure-api-key.")
				for name := range info.RequestHeaders {
					switch strings.ToLower(name) {
					case "authorization", "x-api-key", "x-goog-api-key":
						sourcesScrubbed = false
					}
				}
				if requestHeaders, ok := overrideContext["request_headers"].(map[string]interface{}); ok {
					for _, name := range []string{"authorization", "x-api-key", "x-goog-api-key"} {
						if _, exists := requestHeaders[name]; exists {
							sourcesScrubbed = false
						}
					}
				}
				serialized, err := common.Marshal(map[string]any{
					"url":              c.Request.URL.String(),
					"request_uri":      c.Request.RequestURI,
					"headers":          c.Request.Header,
					"context":          c.Keys,
					"relay_info":       info,
					"override_context": overrideContext,
				})
				if err != nil {
					t.Fatalf("serialize downstream metadata: %v", err)
				}
				captured = string(serialized)
				c.Status(http.StatusNoContent)
			})

			request := httptest.NewRequest(test.method, test.target, nil)
			request.RemoteAddr = "203.0.113.10:43123"
			test.configure(request, fixture.plaintext)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("credential transport rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			allDownstreamData := logs.String() + captured
			if strings.Contains(allDownstreamData, fixture.plaintext) {
				t.Fatalf("plaintext credential leaked downstream: %s", allDownstreamData)
			}
			if !sourcesScrubbed {
				t.Fatalf("credential transport metadata was not scrubbed: %s", captured)
			}
			if test.name == "gemini query key" {
				if strings.Contains(captured, "key=") || !strings.Contains(captured, "alt=sse") {
					t.Fatalf("Gemini URL query was not selectively scrubbed: %s", captured)
				}
			}
			if test.name == "websocket subprotocol" &&
				!strings.Contains(captured, "realtime") {
				t.Fatalf("non-secret websocket subprotocols were removed: %s", captured)
			}
		})
	}
}

func TestExtractZTAPICredentialRejectsMalformedShape(t *testing.T) {
	valid, _, _, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate valid key: %v", err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(alphabet, valid[len(valid)-1])
	nonCanonical := valid[:len(valid)-1] + string(alphabet[lastIndex+1])
	tests := []struct {
		name       string
		credential string
	}{
		{name: "short payload", credential: "sk-zt-short"},
		{name: "padding", credential: valid + "="},
		{name: "extra suffix", credential: valid + "suffix"},
		{name: "leading whitespace", credential: " " + valid},
		{name: "trailing whitespace", credential: valid + " "},
		{name: "invalid alphabet", credential: "sk-zt-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA*"},
		{name: "non-canonical trailing bits", credential: nonCanonical},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			ctx.Request.Header.Set("Authorization", "Bearer "+test.credential)
			if _, ok := extractZTAPICredential(ctx); ok {
				t.Fatal("malformed credential was accepted")
			}
		})
	}
}

func TestTokenAuthRejectsAmbiguousCredentialSources(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		configure func(*http.Request, string, string)
	}{
		{
			name:   "anthropic native conflicts with authorization",
			method: http.MethodPost,
			target: "/v1/messages",
			configure: func(request *http.Request, native string, fallback string) {
				request.Header.Set("x-api-key", native)
				request.Header.Set("Authorization", "Bearer "+fallback)
			},
		},
		{
			name:   "Gemini header conflicts with query",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent",
			configure: func(request *http.Request, native string, fallback string) {
				request.Header.Set("x-goog-api-key", native)
				query := request.URL.Query()
				query.Set("key", fallback)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:   "Gemini native conflicts with authorization",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent",
			configure: func(request *http.Request, native string, fallback string) {
				request.Header.Set("x-goog-api-key", native)
				request.Header.Set("Authorization", "Bearer "+fallback)
			},
		},
		{
			name:   "websocket native conflicts with authorization",
			method: http.MethodGet,
			target: "/v1/realtime",
			configure: func(request *http.Request, native string, fallback string) {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
				request.Header.Set("Sec-WebSocket-Protocol", "realtime, openai-insecure-api-key."+native)
				request.Header.Set("Authorization", "Bearer "+fallback)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")
			conflicting, _, _, err := common.GenerateZTAPIKey()
			if err != nil {
				t.Fatalf("generate conflicting key: %v", err)
			}
			recorder, called := performTokenAuthRequest(
				t,
				test.method,
				test.target,
				"203.0.113.10:43123",
				func(request *http.Request) {
					test.configure(request, fixture.plaintext, conflicting)
				},
				false,
			)
			if called {
				t.Fatalf("ambiguous credentials reached terminal handler: status=%d", recorder.Code)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("ambiguous credential status=%d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestTokenAuthAcceptsMatchingNativeAndFallbackCredentials(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		target    string
		configure func(*http.Request, string)
	}{
		{
			name:   "Anthropic native and Authorization",
			method: http.MethodPost,
			target: "/v1/messages",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-api-key", key)
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
		{
			name:   "Gemini header query and Authorization",
			method: http.MethodPost,
			target: "/v1beta/models/gemini-test:generateContent",
			configure: func(request *http.Request, key string) {
				request.Header.Set("x-goog-api-key", key)
				request.Header.Set("Authorization", "Bearer "+key)
				query := request.URL.Query()
				query.Set("key", key)
				request.URL.RawQuery = query.Encode()
			},
		},
		{
			name:   "websocket subprotocol and Authorization",
			method: http.MethodGet,
			target: "/v1/realtime",
			configure: func(request *http.Request, key string) {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
				request.Header.Set("Sec-WebSocket-Protocol", "realtime, openai-insecure-api-key."+key)
				request.Header.Set("Authorization", "Bearer "+key)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")
			recorder, called := performTokenAuthRequest(
				t,
				test.method,
				test.target,
				"203.0.113.10:43123",
				func(request *http.Request) {
					test.configure(request, fixture.plaintext)
				},
				false,
			)
			if !called || recorder.Code != http.StatusNoContent {
				t.Fatalf("matching native/fallback credentials rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestTokenAuthRejectsLegacyAndChannelSelectionCredentials(t *testing.T) {
	canonical, _, _, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate canonical key: %v", err)
	}
	preLaunch := "sk-" + "gan-" + strings.TrimPrefix(canonical, "sk-zt-")

	tests := []struct {
		name              string
		storedLookupValue string
		presented         string
	}{
		{
			name:              "pre-launch product key",
			storedLookupValue: preLaunch,
			presented:         preLaunch,
		},
		{
			name:              "imported sk key",
			storedLookupValue: "legacyvalue",
			presented:         "sk-legacyvalue",
		},
		{
			name:              "legacy channel suffix",
			storedLookupValue: "legacy",
			presented:         "sk-legacy-42",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")
			fixture.token.KeyHash = common.HashZTAPIKey(test.storedLookupValue)
			if err := fixture.db.Model(fixture.token).Update("key_hash", fixture.token.KeyHash).Error; err != nil {
				t.Fatalf("replace token lookup fixture: %v", err)
			}

			recorder, called := performTokenAuthRequest(
				t,
				http.MethodPost,
				"/v1/chat/completions",
				"203.0.113.10:43123",
				func(request *http.Request) {
					request.Header.Set("Authorization", "Bearer "+test.presented)
				},
				false,
			)
			if called {
				t.Fatalf("legacy credential reached terminal handler: status=%d", recorder.Code)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("legacy credential status=%d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}
}

func TestTokenAuthReadOnlyRejectsNonZTAPICredential(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-test")
	const legacyKey = "sk-imported-read-only-key"
	fixture.token.KeyHash = common.HashZTAPIKey(legacyKey)
	if err := fixture.db.Model(fixture.token).Update("key_hash", fixture.token.KeyHash).Error; err != nil {
		t.Fatalf("replace token lookup fixture: %v", err)
	}

	recorder, called := performTokenAuthRequest(
		t,
		http.MethodGet,
		"/api/usage/token",
		"203.0.113.10:43123",
		func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer "+legacyKey)
		},
		true,
	)
	if called {
		t.Fatalf("legacy read-only credential reached terminal handler: status=%d", recorder.Code)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("legacy read-only status=%d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
	}
}

func TestRelayRestrictionsBlockBeforeTerminalHandler(t *testing.T) {
	tests := []struct {
		name       string
		modelName  string
		remoteAddr string
		mutate     func(*relaySecurityFixture)
		wantStatus int
	}{
		{
			name:       "expired token",
			modelName:  "gpt-test",
			remoteAddr: "203.0.113.10:43123",
			mutate: func(fixture *relaySecurityFixture) {
				fixture.token.ExpiredTime = common.GetTimestamp() - 1
				if err := fixture.db.Model(fixture.token).Update("expired_time", fixture.token.ExpiredTime).Error; err != nil {
					t.Fatalf("expire token: %v", err)
				}
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "disabled token",
			modelName:  "gpt-test",
			remoteAddr: "203.0.113.10:43123",
			mutate: func(fixture *relaySecurityFixture) {
				fixture.token.Status = common.TokenStatusDisabled
				if err := fixture.db.Model(fixture.token).Update("status", fixture.token.Status).Error; err != nil {
					t.Fatalf("disable token: %v", err)
				}
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "model outside allowlist",
			modelName:  "gpt-other",
			remoteAddr: "203.0.113.10:43123",
			mutate:     func(_ *relaySecurityFixture) {},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "exhausted quota",
			modelName:  "gpt-test",
			remoteAddr: "203.0.113.10:43123",
			mutate: func(fixture *relaySecurityFixture) {
				fixture.token.RemainQuota = 0
				if err := fixture.db.Model(fixture.token).Update("remain_quota", 0).Error; err != nil {
					t.Fatalf("exhaust token quota: %v", err)
				}
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "source IP outside allowlist",
			modelName:  "gpt-test",
			remoteAddr: "203.0.113.10:43123",
			mutate: func(fixture *relaySecurityFixture) {
				allowIPs := "198.51.100.20/32"
				fixture.token.AllowIps = &allowIPs
				if err := fixture.db.Model(fixture.token).Update("allow_ips", allowIPs).Error; err != nil {
					t.Fatalf("restrict token IP: %v", err)
				}
			},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-test")
			test.mutate(&fixture)
			recorder, called, _, _ := performRelaySecurityRequest(
				t,
				fixture.plaintext,
				test.modelName,
				test.remoteAddr,
			)
			if called {
				t.Fatalf("restricted request reached terminal handler: status=%d", recorder.Code)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d, want %d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestRelayAllowedRequestUsesQuotedSourceForSelection(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-5.5")
	publishRelaySecurityAlias(t, fixture.db, fixture.channel.Id, "zt-gpt-5.5", "gpt-5.5")
	fixture.token.ModelLimits = "zt-gpt-5.5"
	if err := fixture.db.Model(fixture.token).Update("model_limits", fixture.token.ModelLimits).Error; err != nil {
		t.Fatalf("authorize public alias: %v", err)
	}
	allowIPs := "203.0.113.10/32"
	fixture.token.AllowIps = &allowIPs
	if err := fixture.db.Model(fixture.token).Update("allow_ips", allowIPs).Error; err != nil {
		t.Fatalf("restrict token IP: %v", err)
	}

	recorder, called, originalModel, selectionModel := performRelaySecurityRequest(
		t,
		fixture.plaintext,
		"zt-gpt-5.5",
		"203.0.113.10:43123",
	)
	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("allowed request rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if originalModel != "zt-gpt-5.5" {
		t.Fatalf("upstream attribution model=%q, want original request model", originalModel)
	}
	if selectionModel != "gpt-5.5" {
		t.Fatalf("selection model=%q, want exact quoted source model", selectionModel)
	}
}

func TestRelaySourceModelPermissionDoesNotAuthorizePublicAlias(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-5.5")
	publishRelaySecurityAlias(t, fixture.db, fixture.channel.Id, "zt-gpt-5.5", "gpt-5.5")

	recorder, called, _, _ := performRelaySecurityRequest(
		t,
		fixture.plaintext,
		"zt-gpt-5.5",
		"203.0.113.10:43123",
	)
	if called {
		t.Fatalf("source-only token reached terminal handler: status=%d", recorder.Code)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("source-only token status=%d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestRelayUnrestrictedTokenUsesQuotedSourceForSelection(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-5.5")
	publishRelaySecurityAlias(t, fixture.db, fixture.channel.Id, "zt-gpt-5.5", "gpt-5.5")
	fixture.token.ModelLimitsEnabled = false
	fixture.token.ModelLimits = ""
	if err := fixture.db.Model(fixture.token).Updates(map[string]any{
		"model_limits_enabled": false,
		"model_limits":         "",
	}).Error; err != nil {
		t.Fatalf("disable model limits: %v", err)
	}

	recorder, called, originalModel, selectionModel := performRelaySecurityRequest(
		t,
		fixture.plaintext,
		"zt-gpt-5.5",
		"203.0.113.10:43123",
	)
	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("unrestricted quoted request rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if originalModel != "zt-gpt-5.5" {
		t.Fatalf("upstream attribution model=%q, want original request model", originalModel)
	}
	if selectionModel != "gpt-5.5" {
		t.Fatalf("selection model=%q, want exact quoted source model", selectionModel)
	}
}

func TestRelayQuotationRejectsUnquotedWildcardPublication(t *testing.T) {
	for _, restricted := range []bool{true, false} {
		t.Run(fmt.Sprint(restricted), func(t *testing.T) {
			fixture := setupRelaySecurityFixture(t, "gpt-4o-gizmo-*")
			alias := "gpt-4o-gizmo-customer-model"
			publishRelaySecurityAlias(t, fixture.db, fixture.channel.Id, alias, "gpt-4o-gizmo-*")
			if err := fixture.db.Model(fixture.token).Updates(map[string]any{
				"model_limits_enabled": restricted, "model_limits": alias,
			}).Error; err != nil {
				t.Fatal(err)
			}
			recorder, called, _, _ := performRelaySecurityRequest(t, fixture.plaintext, alias, "203.0.113.10:43123")
			if called || (recorder.Code != http.StatusForbidden && recorder.Code != http.StatusNotFound) {
				t.Fatalf("unquoted wildcard reached relay: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestRelayUnknownAllowedModelIsRejectedBeforeSelection(t *testing.T) {
	fixture := setupRelaySecurityFixture(t, "gpt-test")
	fixture.token.ModelLimits = "unknown-public-model"
	if err := fixture.db.Model(fixture.token).Update("model_limits", fixture.token.ModelLimits).Error; err != nil {
		t.Fatalf("replace model limit fixture: %v", err)
	}

	recorder, called, _, _ := performRelaySecurityRequest(
		t,
		fixture.plaintext,
		"unknown-public-model",
		"203.0.113.10:43123",
	)
	if called {
		t.Fatalf("unknown model reached terminal handler: status=%d", recorder.Code)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unknown model status=%d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}
