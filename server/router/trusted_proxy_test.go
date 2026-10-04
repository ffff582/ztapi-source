package router

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
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestParseTrustedProxyCIDRsAcceptsOnlyPrivateNetworks(t *testing.T) {
	got, err := ParseTrustedProxyCIDRs(
		"127.0.0.1/32, 10.0.0.0/8,172.16.0.0/12,192.168.7.0/24,::1/128,fc00::/7,fd20::/16",
		true,
	)
	if err != nil {
		t.Fatalf("parse private trusted proxies: %v", err)
	}
	want := []string{
		"127.0.0.1/32",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.7.0/24",
		"::1/128",
		"fc00::/7",
		"fd20::/16",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("trusted proxies=%v, want %v", got, want)
	}
}

func TestParseTrustedProxyCIDRsRejectsUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed", raw: "not-a-cidr"},
		{name: "bare address", raw: "10.0.0.1"},
		{name: "public IPv4", raw: "203.0.113.0/24"},
		{name: "public IPv6", raw: "2001:db8::/32"},
		{name: "IPv4 wildcard", raw: "0.0.0.0/0"},
		{name: "IPv6 wildcard", raw: "::/0"},
		{name: "network escapes RFC1918", raw: "10.0.0.0/7"},
		{name: "network escapes unique local", raw: "fc00::/6"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseTrustedProxyCIDRs(test.raw, true); err == nil {
				t.Fatalf("unsafe trusted proxy configuration %q was accepted", test.raw)
			}
		})
	}
}

func TestParseTrustedProxyCIDRsRejectsEmptyProductionConfiguration(t *testing.T) {
	if _, err := ParseTrustedProxyCIDRs("", true); err == nil {
		t.Fatal("empty production trusted proxy configuration was accepted")
	} else if !strings.Contains(err.Error(), "ZTAPI_TRUSTED_PROXY_CIDRS") {
		t.Fatalf("production configuration error = %q, want ZTAPI variable", err)
	}
	got, err := ParseTrustedProxyCIDRs("", false)
	if err != nil {
		t.Fatalf("empty development configuration returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty development trusted proxies=%v, want none", got)
	}
}

func setupTrustedProxyTokenTest(t *testing.T, allowCIDR string) (*gorm.DB, string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	if err := i18n.Init(); err != nil {
		t.Fatalf("initialize i18n: %v", err)
	}

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	if err := db.AutoMigrate(&model.User{}, &model.Token{}); err != nil {
		t.Fatalf("migrate proxy token tables: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	user := &model.User{
		Username: "proxy-user-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Password: "not-used",
		Status:   common.UserStatusEnabled,
		Quota:    100,
		Group:    "default",
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create proxy user: %v", err)
	}
	plaintext, keyHash, keyPrefix, err := common.GenerateZTAPIKey()
	if err != nil {
		t.Fatalf("generate proxy test key: %v", err)
	}
	token := &model.Token{
		UserId:         user.Id,
		KeyHash:        keyHash,
		KeyPrefix:      keyPrefix,
		Status:         common.TokenStatusEnabled,
		Name:           "proxy-ip-restriction",
		ExpiredTime:    -1,
		RemainQuota:    100,
		UnlimitedQuota: true,
		AllowIps:       &allowCIDR,
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create proxy token: %v", err)
	}
	return db, plaintext
}

func performTrustedProxyTokenRequest(
	t *testing.T,
	trustedCIDRs string,
	plaintext string,
	remoteAddr string,
	forwardedFor string,
	realIP string,
) (*httptest.ResponseRecorder, bool) {
	t.Helper()

	var called atomic.Bool
	engine := gin.New()
	if err := ConfigureTrustedProxies(engine, trustedCIDRs, true); err != nil {
		t.Fatalf("configure trusted proxies: %v", err)
	}
	engine.POST("/v1/chat/completions", middleware.TokenAuth(), func(c *gin.Context) {
		called.Store(true)
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer "+plaintext)
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	if realIP != "" {
		request.Header.Set("X-Real-IP", realIP)
	}
	request.RemoteAddr = remoteAddr
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder, called.Load()
}

func TestUntrustedDirectClientCannotSpoofForwardedIPIntoTokenAllowlist(t *testing.T) {
	_, plaintext := setupTrustedProxyTokenTest(t, "198.51.100.25/32")

	recorder, called := performTrustedProxyTokenRequest(
		t,
		"10.20.0.0/16",
		plaintext,
		"203.0.113.10:43123",
		"198.51.100.25",
		"",
	)

	if called {
		t.Fatalf("spoofed direct request reached terminal handler: status=%d", recorder.Code)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("spoofed direct status=%d, want %d: %s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestTrustedPrivateProxyUsesForwardedClientIP(t *testing.T) {
	_, plaintext := setupTrustedProxyTokenTest(t, "198.51.100.25/32")

	recorder, called := performTrustedProxyTokenRequest(
		t,
		"10.20.0.0/16",
		plaintext,
		"10.20.0.8:43123",
		"",
		"198.51.100.25",
	)

	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("trusted proxy request rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func initializeRouterRelayTestColumnNames(t *testing.T) {
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
		t.Fatalf("initialize router model column names: %v", err)
	}
	if model.DB != nil {
		sqlDB, err := model.DB.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	}
}

func TestZTAPIRelayRouteUsesGeneratedCredentialAndRealMiddlewareChain(t *testing.T) {
	initializeRouterRelayTestColumnNames(t)
	db, plaintext := setupTrustedProxyTokenTest(t, "203.0.113.10/32")
	if err := db.AutoMigrate(
		&model.Channel{},
		&model.Ability{},
		&model.ZTAPIModelConfig{},
		&model.ZTAPIModelPriceSource{},
		&model.ZTAPIModelPublicationSnapshot{},
	); err != nil {
		t.Fatalf("migrate relay route tables: %v", err)
	}
	ratio_setting.InitRatioSettings()

	var token model.Token
	if err := db.Where("key_hash = ?", common.HashZTAPIKey(plaintext)).First(&token).Error; err != nil {
		t.Fatalf("load generated token: %v", err)
	}
	token.ModelLimitsEnabled = true
	token.ModelLimits = "zt-gpt-5.5"
	if err := db.Model(&token).Updates(map[string]any{
		"model_limits_enabled": true,
		"model_limits":         token.ModelLimits,
	}).Error; err != nil {
		t.Fatalf("set route token model limit: %v", err)
	}

	priority := int64(0)
	weight := uint(100)
	channel := &model.Channel{
		Type:        constant.ChannelTypeOpenAI,
		Key:         "deterministic-router-terminal-key",
		Status:      common.ChannelStatusEnabled,
		Name:        "deterministic-router-terminal-channel",
		Weight:      &weight,
		CreatedTime: common.GetTimestamp(),
		Models:      "gpt-5.5",
		Group:       "default",
		Priority:    &priority,
	}
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("create route channel: %v", err)
	}
	if err := db.Create(&model.Ability{
		Group:     "default",
		Model:     "gpt-5.5",
		ChannelId: channel.Id,
		Enabled:   true,
		Priority:  &priority,
		Weight:    weight,
	}).Error; err != nil {
		t.Fatalf("create route ability: %v", err)
	}
	seedRouterPublicModel(t, db, channel.Id, "gpt-5.5", "zt-gpt-5.5")

	var called atomic.Bool
	engine := gin.New()
	if err := ConfigureTrustedProxies(engine, "10.20.0.0/16", true); err != nil {
		t.Fatalf("configure relay route proxies: %v", err)
	}
	relayV1 := engine.Group("/v1")
	relayV1.Use(middleware.TokenAuth(), middleware.Distribute())
	relayV1.POST("/chat/completions", func(c *gin.Context) {
		called.Store(true)
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		bytes.NewBufferString(`{"model":"zt-gpt-5.5","messages":[{"role":"user","content":"test"}]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+plaintext)
	request.RemoteAddr = "203.0.113.10:43123"
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if !called.Load() || recorder.Code != http.StatusNoContent {
		t.Fatalf("real relay chain rejected generated key: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
