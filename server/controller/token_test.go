package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type tokenAPIResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type tokenCreateResponse struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	KeyPrefix string `json:"key_prefix"`
}

type tokenPageResponse struct {
	Items []tokenResponseItem `json:"items"`
}

type tokenResponseItem struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	Status    int    `json:"status"`
}

type sqliteColumnInfo struct {
	Name string `gorm:"column:name"`
}

func setupTokenControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.Token{}, &model.Ability{}); err != nil {
		t.Fatalf("migrate token table: %v", err)
	}
	if err := db.AutoMigrate(&model.ZTAPIModelConfig{}, &model.ZTAPIModelPriceSource{}, &model.ZTAPIModelPublicationSnapshot{}); err != nil {
		t.Fatalf("migrate public model catalog: %v", err)
	}
	model.InvalidateZTAPIAliasCache()

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		model.InvalidateZTAPIAliasCache()
	})

	return db
}

func seedToken(t *testing.T, db *gorm.DB, userID int, name string) (*model.Token, string) {
	t.Helper()

	plaintext, hash, prefix, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	token := &model.Token{
		UserId:         userID,
		Name:           name,
		KeyHash:        hash,
		KeyPrefix:      prefix,
		Status:         common.TokenStatusEnabled,
		CreatedTime:    1,
		AccessedTime:   1,
		ExpiredTime:    -1,
		RemainQuota:    100,
		UnlimitedQuota: true,
		Group:          "default",
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create token: %v", err)
	}
	return token, plaintext
}

func newAuthenticatedContext(t *testing.T, method string, target string, body any, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()

	requestBody := bytes.NewReader(nil)
	if body != nil {
		payload, err := common.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		requestBody = bytes.NewReader(payload)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, requestBody)
	if body != nil {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	ctx.Set("id", userID)
	return ctx, recorder
}

func decodeAPIResponse(t *testing.T, recorder *httptest.ResponseRecorder) tokenAPIResponse {
	t.Helper()

	var response tokenAPIResponse
	if err := common.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func assertNoSecretTokenFields(t *testing.T, body []byte, plaintext string, hash string) {
	t.Helper()

	text := string(body)
	if strings.Contains(text, plaintext) {
		t.Fatalf("response leaked plaintext key: %s", text)
	}
	if strings.Contains(text, hash) {
		t.Fatalf("response leaked key hash: %s", text)
	}

	var payload any
	if err := common.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response for secret field audit: %v", err)
	}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "key" || key == "key_hash" {
					t.Fatalf("response exposed forbidden field %q: %s", key, text)
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(payload)
}

func tokenTableColumns(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()

	var columns []sqliteColumnInfo
	if err := db.Raw("PRAGMA table_info(tokens)").Scan(&columns).Error; err != nil {
		t.Fatalf("inspect token schema: %v", err)
	}
	result := make(map[string]bool, len(columns))
	for _, column := range columns {
		result[column.Name] = true
	}
	return result
}

func TestAddTokenReturnsPlaintextExactlyOnceAndPersistsOnlyHashAndPrefix(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	body := map[string]any{
		"name":            "development",
		"expired_time":    -1,
		"unlimited_quota": true,
	}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", body, 17)

	AddToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("create failed: %s", response.Message)
	}
	var created tokenCreateResponse
	if err := common.Unmarshal(response.Data, &created); err != nil {
		t.Fatalf("decode create data: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("create response omitted id")
	}
	if !strings.HasPrefix(created.Key, "sk-zt-") {
		t.Fatalf("create response key = %q", created.Key)
	}
	if created.KeyPrefix != created.Key[:12] {
		t.Fatalf("create response prefix = %q, want %q", created.KeyPrefix, created.Key[:12])
	}
	if strings.Count(recorder.Body.String(), created.Key) != 1 {
		t.Fatalf("plaintext must appear exactly once in create response: %s", recorder.Body.String())
	}

	var stored model.Token
	if err := db.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("load stored token: %v", err)
	}
	if stored.KeyHash != common.HashZTAPIKey(created.Key) {
		t.Fatalf("stored hash = %q", stored.KeyHash)
	}
	if stored.KeyPrefix != created.KeyPrefix {
		t.Fatalf("stored prefix = %q, want %q", stored.KeyPrefix, created.KeyPrefix)
	}
	columns := tokenTableColumns(t, db)
	if columns["key"] {
		t.Fatal("token table still contains plaintext key column")
	}

	detailCtx, detailRecorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/"+strconv.Itoa(created.ID), nil, 17)
	detailCtx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(created.ID)}}
	GetToken(detailCtx)
	assertNoSecretTokenFields(t, detailRecorder.Body.Bytes(), created.Key, stored.KeyHash)
}

func TestTokenReadAndUpdateResponsesExposePrefixWithoutSecretMaterial(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	token, plaintext := seedToken(t, db, 23, "visible-token")

	tests := []struct {
		name string
		run  func() *httptest.ResponseRecorder
	}{
		{
			name: "list",
			run: func() *httptest.ResponseRecorder {
				ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/?p=1&size=10", nil, 23)
				GetAllTokens(ctx)
				return recorder
			},
		},
		{
			name: "search",
			run: func() *httptest.ResponseRecorder {
				ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/search?keyword=visible-token&p=1&size=10", nil, 23)
				SearchTokens(ctx)
				return recorder
			},
		},
		{
			name: "detail",
			run: func() *httptest.ResponseRecorder {
				ctx, recorder := newAuthenticatedContext(t, http.MethodGet, "/api/token/"+strconv.Itoa(token.Id), nil, 23)
				ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(token.Id)}}
				GetToken(ctx)
				return recorder
			},
		},
		{
			name: "update",
			run: func() *httptest.ResponseRecorder {
				body := map[string]any{
					"id":                   token.Id,
					"name":                 "updated-token",
					"status":               common.TokenStatusEnabled,
					"expired_time":         -1,
					"remain_quota":         100,
					"unlimited_quota":      true,
					"model_limits_enabled": false,
					"model_limits":         "",
					"group":                "default",
					"cross_group_retry":    false,
				}
				ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/", body, 23)
				UpdateToken(ctx)
				return recorder
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := test.run()
			response := decodeAPIResponse(t, recorder)
			if !response.Success {
				t.Fatalf("%s failed: %s", test.name, response.Message)
			}
			assertNoSecretTokenFields(t, recorder.Body.Bytes(), plaintext, token.KeyHash)
			if !strings.Contains(recorder.Body.String(), token.KeyPrefix) {
				t.Fatalf("%s response omitted key prefix: %s", test.name, recorder.Body.String())
			}
		})
	}
}

func seedEnabledPublicModel(t *testing.T, db *gorm.DB, modelName string) {
	t.Helper()

	publicName := modelName
	entries, err := model.ZTAPIQuotationEntries()
	if err != nil {
		t.Fatal(err)
	}
	var quoted model.ZTAPIQuotationEntry
	for _, entry := range entries {
		if entry.Status == "mapped" && entry.PublicName == publicName {
			quoted = entry
			break
		}
	}
	if quoted.SourceModel == "" {
		t.Fatalf("fixture requires an explicitly quoted identity: %q", publicName)
	}
	sourceModel := quoted.SourceModel
	config := model.ZTAPIModelConfig{
		SourceModel: sourceModel, PublicName: &publicName,
		Family: model.ZTAPIModelFamilyOpenAI, Protocol: quoted.Protocol,
		ProviderFamily: quoted.ProviderFamily, EnabledGroups: `["default"]`,
		Published: true, Version: 2,
	}
	if err := db.Create(&config).Error; err != nil {
		t.Fatalf("create public model config %q: %v", modelName, err)
	}
	priceSource := model.ZTAPIModelPriceSource{
		ModelConfigID: config.ID, SourceModel: sourceModel,
		ResourceType: "enterprise", SpendTier: "test",
		BillingDimensions: `["input_tokens","output_tokens"]`, Currency: "USD",
		InputPerMillion: "1.0000000000", OutputPerMillion: "2.0000000000",
		CacheReadPerMillion: "0", CacheWritePerMillion: "0",
		CacheWrite5mPerMillion: "0", CacheWrite1hPerMillion: "0",
		ImageUnitCost: "0", AudioUnitCost: "0", RequestUnitCost: "0", CNYPerUSD: "0",
		QuotationEffectiveAt: 1_787_000_000, SourceDocumentChecksum: model.ZTAPIQuotationSHA256,
		OperatorID: 1, Version: 1, CreatedAt: 1_787_000_001,
	}
	if err := db.Create(&priceSource).Error; err != nil {
		t.Fatalf("create public model price source %q: %v", modelName, err)
	}
	snapshot := model.ZTAPIModelPublicationSnapshot{
		ModelConfigID: config.ID, ModelVersion: config.Version,
		SourceModel: sourceModel, PublicName: publicName,
		Protocol: quoted.Protocol, ProviderFamily: quoted.ProviderFamily,
		EnabledGroups: `["default"]`, AllowedChannelIDs: `[1]`, PriceSourceID: priceSource.ID,
		InputPricePerMillion: 1.6666666667, OutputPricePerMillion: 3.3333333333,
		CacheReadRatio: 0.1, CacheCreationRatio: 1.25,
		CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
		ImageRatio: 1, AudioRatio: 1, AudioCompletionRatio: 1,
		VerificationIDs: `[]`, IdentityUpdatedAt: 1, CreatedAt: 2,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatalf("create public model snapshot %q: %v", modelName, err)
	}
	if err := db.Model(&config).Update("publication_snapshot_id", snapshot.ID).Error; err != nil {
		t.Fatalf("attach public model snapshot %q: %v", modelName, err)
	}
	model.InvalidateZTAPIAliasCache()
}

func TestAddTokenModelAllowlistAcceptsPublicAliasAndRejectsSourceName(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	seedEnabledPublicModel(t, db, "zt-gpt-5.5")

	acceptedBody := map[string]any{
		"name": "public-alias", "expired_time": -1, "unlimited_quota": true,
		"model_limits_enabled": true, "model_limits": "zt-gpt-5.5",
	}
	acceptedContext, acceptedRecorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", acceptedBody, 40)
	AddToken(acceptedContext)
	if response := decodeAPIResponse(t, acceptedRecorder); !response.Success {
		t.Fatalf("public alias was rejected: %s", acceptedRecorder.Body.String())
	}

	rejectedBody := map[string]any{
		"name": "private-source", "expired_time": -1, "unlimited_quota": true,
		"model_limits_enabled": true, "model_limits": "gpt-5.5",
	}
	rejectedContext, rejectedRecorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", rejectedBody, 40)
	AddToken(rejectedContext)
	if response := decodeAPIResponse(t, rejectedRecorder); response.Success {
		t.Fatalf("private source model was accepted: %s", rejectedRecorder.Body.String())
	}
}

func TestAddTokenRejectsInvalidRestrictionInputs(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "blank enabled model allowlist",
			body: map[string]any{
				"name":                 "blank-model",
				"expired_time":         -1,
				"unlimited_quota":      true,
				"model_limits_enabled": true,
				"model_limits":         "",
			},
		},
		{
			name: "blank model entry",
			body: map[string]any{
				"name":                 "blank-entry",
				"expired_time":         -1,
				"unlimited_quota":      true,
				"model_limits_enabled": true,
				"model_limits":         "zt-gpt-5.5,,zt-gpt-5.5",
			},
		},
		{
			name: "unknown model",
			body: map[string]any{
				"name":                 "unknown-model",
				"expired_time":         -1,
				"unlimited_quota":      true,
				"model_limits_enabled": true,
				"model_limits":         "gpt-unknown",
			},
		},
		{
			name: "unquoted wildcard",
			body: map[string]any{
				"name":                 "unquoted-wildcard",
				"expired_time":         -1,
				"unlimited_quota":      true,
				"model_limits_enabled": true,
				"model_limits":         "gpt-4-gizmo-*",
			},
		},
		{
			name: "malformed source CIDR",
			body: map[string]any{
				"name":            "bad-cidr",
				"expired_time":    -1,
				"unlimited_quota": true,
				"allow_ips":       "192.0.2.0/99",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupTokenControllerTestDB(t)
			seedEnabledPublicModel(t, db, "zt-gpt-5.5")
			ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", test.body, 41)

			AddToken(ctx)

			response := decodeAPIResponse(t, recorder)
			if response.Success {
				t.Fatalf("invalid restrictions were accepted: %s", recorder.Body.String())
			}
			var count int64
			if err := db.Model(&model.Token{}).Count(&count).Error; err != nil {
				t.Fatalf("count tokens: %v", err)
			}
			if count != 0 {
				t.Fatalf("invalid create persisted %d token(s)", count)
			}
		})
	}
}

func TestAddTokenNormalizesRestrictionPersistence(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	seedEnabledPublicModel(t, db, "zt-gpt-5.5")
	seedEnabledPublicModel(t, db, "zt-gpt-5.4")
	body := map[string]any{
		"name":                 "normalized-create",
		"status":               common.TokenStatusDisabled,
		"expired_time":         int64(1_900_000_000),
		"unlimited_quota":      true,
		"model_limits_enabled": true,
		"model_limits":         " zt-gpt-5.5 ,zt-gpt-5.4, zt-gpt-5.5 ",
		"allow_ips":            "2001:db8::1,\n192.0.2.10, 192.0.2.10/32",
	}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPost, "/api/token/", body, 42)

	AddToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("normalized create failed: %s", recorder.Body.String())
	}
	var created tokenCreateResponse
	if err := common.Unmarshal(response.Data, &created); err != nil {
		t.Fatalf("decode create data: %v", err)
	}
	var stored model.Token
	if err := db.First(&stored, created.ID).Error; err != nil {
		t.Fatalf("load normalized token: %v", err)
	}
	if stored.ModelLimits != "zt-gpt-5.4,zt-gpt-5.5" {
		t.Fatalf("model limits=%q, want normalized deduplicated value", stored.ModelLimits)
	}
	if stored.AllowIps == nil || *stored.AllowIps != "192.0.2.10/32\n2001:db8::1/128" {
		t.Fatalf("allow IPs=%v, want deterministic normalized CIDRs", stored.AllowIps)
	}
	if stored.Status != common.TokenStatusDisabled {
		t.Fatalf("status=%d, want disabled", stored.Status)
	}
	if stored.ExpiredTime != 1_900_000_000 {
		t.Fatalf("expiry=%d, want preserved value", stored.ExpiredTime)
	}
}

func TestUpdateTokenRejectsInvalidRestrictionsWithoutPersistence(t *testing.T) {
	tests := []struct {
		name        string
		modelLimits string
		allowIPs    string
	}{
		{
			name:        "unknown model",
			modelLimits: "gpt-unknown",
			allowIPs:    "192.0.2.10/32",
		},
		{
			name:        "unquoted wildcard",
			modelLimits: "gpt-4-gizmo-*",
			allowIPs:    "192.0.2.10/32",
		},
		{
			name:        "malformed CIDR",
			modelLimits: "zt-gpt-5.5",
			allowIPs:    "not-an-ip",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupTokenControllerTestDB(t)
			seedEnabledPublicModel(t, db, "zt-gpt-5.5")
			token, _ := seedToken(t, db, 43, "before-update")
			originalExpiry := token.ExpiredTime
			body := map[string]any{
				"id":                   token.Id,
				"name":                 "must-not-persist",
				"status":               common.TokenStatusEnabled,
				"expired_time":         int64(1_900_000_000),
				"unlimited_quota":      true,
				"model_limits_enabled": true,
				"model_limits":         test.modelLimits,
				"allow_ips":            test.allowIPs,
			}
			ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/", body, 43)

			UpdateToken(ctx)

			response := decodeAPIResponse(t, recorder)
			if response.Success {
				t.Fatalf("invalid update was accepted: %s", recorder.Body.String())
			}
			var stored model.Token
			if err := db.First(&stored, token.Id).Error; err != nil {
				t.Fatalf("reload token: %v", err)
			}
			if stored.Name != "before-update" || stored.ExpiredTime != originalExpiry {
				t.Fatalf("invalid update changed persisted token: name=%q expiry=%d", stored.Name, stored.ExpiredTime)
			}
			if stored.ModelLimitsEnabled != token.ModelLimitsEnabled || stored.ModelLimits != token.ModelLimits {
				t.Fatal("invalid update changed persisted model or IP restrictions")
			}
			if (stored.AllowIps == nil) != (token.AllowIps == nil) || (stored.AllowIps != nil && *stored.AllowIps != *token.AllowIps) {
				t.Fatal("invalid update changed persisted IP restrictions")
			}
		})
	}
}

func TestUpdateTokenNormalizesRestrictionPersistence(t *testing.T) {
	db := setupTokenControllerTestDB(t)
	seedEnabledPublicModel(t, db, "zt-gpt-5.5")
	token, _ := seedToken(t, db, 44, "before-normalized-update")
	token.Status = common.TokenStatusDisabled
	if err := db.Model(token).Update("status", token.Status).Error; err != nil {
		t.Fatalf("disable token fixture: %v", err)
	}
	body := map[string]any{
		"id":                   token.Id,
		"name":                 "normalized-update",
		"status":               common.TokenStatusDisabled,
		"expired_time":         int64(1_910_000_000),
		"unlimited_quota":      true,
		"model_limits_enabled": true,
		"model_limits":         "zt-gpt-5.5,zt-gpt-5.5",
		"allow_ips":            "2001:db8::2/128, 198.51.100.7",
	}
	ctx, recorder := newAuthenticatedContext(t, http.MethodPut, "/api/token/", body, 44)

	UpdateToken(ctx)

	response := decodeAPIResponse(t, recorder)
	if !response.Success {
		t.Fatalf("normalized update failed: %s", recorder.Body.String())
	}
	var stored model.Token
	if err := db.First(&stored, token.Id).Error; err != nil {
		t.Fatalf("reload normalized token: %v", err)
	}
	if stored.ModelLimits != "zt-gpt-5.5" {
		t.Fatalf("model limits=%q, want normalized value", stored.ModelLimits)
	}
	if stored.AllowIps == nil || *stored.AllowIps != "198.51.100.7/32\n2001:db8::2/128" {
		t.Fatalf("allow IPs=%v, want normalized deterministic CIDRs", stored.AllowIps)
	}
	if stored.Status != common.TokenStatusDisabled || stored.ExpiredTime != 1_910_000_000 {
		t.Fatalf("status/expiry not preserved: status=%d expiry=%d", stored.Status, stored.ExpiredTime)
	}
}
