package router

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const workbenchPilotConfigEnv = "ZTAPI_WORKBENCH_PILOT_CONFIG"

type workbenchPilotConfig struct {
	WorkbenchAcceptanceConfig
	DrainOnly       bool                    `json:"drain_only"`
	ReadOnly        bool                    `json:"read_only"`
	DispatchEnabled bool                    `json:"dispatch_enabled"`
	FableCap        *workbenchFablePilotCap `json:"fable_cap,omitempty"`
}

type workbenchFablePilotCap struct {
	BatchID     string `json:"batch_id"`
	ExpiresAt   int64  `json:"expires_at"`
	BudgetBasis string `json:"budget_basis"`
	MaxRequests int    `json:"max_requests"`
	BudgetNanoU int64  `json:"budget_nano_u"`
}

func (config workbenchPilotConfig) fablePolicy() (model.ZTAPIWorkbenchPilotPolicy, error) {
	cap := config.FableCap
	rejected := errors.New("workbench Fable pilot wallet cap unavailable")
	var empty model.ZTAPIWorkbenchPilotPolicy
	if !config.DispatchEnabled {
		if cap != nil {
			return empty, rejected
		}
		return empty, nil
	}
	hasFable := false
	for _, pin := range config.Models {
		hasFable = hasFable || pin.Model == "zt-claude-fable-5"
	}
	if !hasFable && cap == nil {
		return empty, nil
	}
	if !hasFable || cap == nil || len(config.UserIDs) != 1 || len(config.Models) != 1 {
		return empty, rejected
	}
	quota, ok := model.ZTAPIWorkbenchPilotWalletQuota(cap.BudgetNanoU)
	if !ok || cap.BudgetBasis != "ztapi_wallet" {
		return empty, rejected
	}
	pin := config.Models[0]
	digestInput, err := common.Marshal(struct {
		Cap         workbenchFablePilotCap
		Pin         WorkbenchAcceptanceModel
		UID         int
		WalletQuota int64
	}{*cap, pin, config.UserIDs[0], quota})
	if err != nil {
		return empty, rejected
	}
	p := model.ZTAPIWorkbenchPilotPolicy{BatchID: cap.BatchID, PolicySHA256: common.HashZTAPIKey(string(digestInput)),
		ExpiresAt: cap.ExpiresAt, MaxRequests: cap.MaxRequests, BudgetQuota: quota,
		UserID: config.UserIDs[0], ChannelID: pin.ChannelID}
	if !p.ValidAt(time.Now()) {
		return empty, rejected
	}
	return p, nil
}

func (config workbenchPilotConfig) validMode() bool {
	switch {
	case config.Locked || config.LoginOnly:
		return false
	case config.ReadOnly:
		return !config.DrainOnly && !config.DispatchEnabled
	case config.DrainOnly:
		return !config.DispatchEnabled
	default:
		return config.DispatchEnabled
	}
}

func validWorkbenchDispatchBinding(pin WorkbenchAcceptanceModel) bool {
	digest := pin.CredentialSHA256
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return false
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return false
	}
	u, err := url.Parse(pin.UpstreamOrigin)
	if err != nil || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(pin.UpstreamOrigin, "?#") || strings.HasSuffix(pin.UpstreamOrigin, "/") {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "https" || u.Scheme == "http" && ip != nil && ip.IsLoopback()
}

func mountConfiguredWorkbenchPilot(r gin.IRouter) {
	path := strings.TrimSpace(os.Getenv(workbenchPilotConfigEnv))
	if path == "" {
		return
	}
	if err := mountWorkbenchPilotFile(r, path); err != nil {
		common.SysLog("Workbench pilot configuration unavailable; routes remain disabled")
	}
}

func workbenchPilotSchemaReady(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	m := db.Migrator()
	for _, table := range []struct {
		model  any
		fields []string
	}{
		{&model.Token{}, []string{"WorkbenchOnly"}},
		{&model.ZTAPIWorkbenchTask{}, []string{"ID", "UserID", "Model", "LimitQuota", "PilotBatchID"}},
		{&model.ZTAPIRequestSettlement{}, []string{"WorkbenchTaskID", "WorkbenchBatchID", "WorkbenchStage",
			"WorkbenchBodySHA256", "WorkbenchOutboundMax", "WorkbenchOutputMax"}},
		{&model.ZTAPIRequestAttempt{}, nil},
		{&model.ZTAPISettlementFinalizationIntent{}, nil},
		{&model.BalanceLedger{}, nil},
	} {
		if !m.HasTable(table.model) {
			return false
		}
		for _, field := range table.fields {
			if !m.HasColumn(table.model, field) {
				return false
			}
		}
	}
	return true
}

func mountWorkbenchPilotFile(r gin.IRouter, path string) error {
	const maxConfig = 32 << 10
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > maxConfig {
		return errors.New("invalid workbench pilot file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxConfig+1))
	if err != nil || len(raw) > maxConfig {
		return errors.New("invalid workbench pilot file size")
	}
	var config workbenchPilotConfig
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil ||
		common.DecodeJsonStrict(bytes.NewReader(raw), &config) != nil || !config.validMode() {
		return errors.New("invalid workbench pilot settings")
	}
	p, err := newWorkbenchAcceptancePolicy(config.WorkbenchAcceptanceConfig)
	if err != nil {
		return err
	}
	if config.DispatchEnabled {
		for _, pin := range config.Models {
			if !validWorkbenchDispatchBinding(pin) {
				return errors.New("workbench pilot dispatch binding unavailable")
			}
		}
	}
	if !workbenchPilotSchemaReady(model.DB) {
		return errors.New("workbench pilot schema unavailable")
	}
	fable, err := config.fablePolicy()
	if err != nil {
		return err
	}
	if fable.BatchID != "" {
		m := model.DB.Migrator()
		spend := &model.ZTAPIWorkbenchPilotSpend{}
		if !m.HasTable(spend) || !m.HasColumn(spend, "PolicySHA256") || !m.HasColumn(spend, "ClaimsJSON") {
			return errors.New("workbench pilot wallet cap schema unavailable")
		}
	}
	mountWorkbenchFirstPartyPilot(r, config, p, fable)
	return nil
}

func mountWorkbenchFirstPartyPilot(r gin.IRouter, config workbenchPilotConfig, p workbenchAcceptancePolicy, fable model.ZTAPIWorkbenchPilotPolicy) {
	r.GET("/.well-known/ztapi-workbench", workbenchPilotNoStore, func(c *gin.Context) {
		manifest := gin.H{"protocol": "ztapi-workbench-first-party-v1", "budget": "wire-bound-v1",
			"dispatch": "enabled", "account_routes": "workbench-v1"}
		if config.ReadOnly || config.DrainOnly || fable.BatchID != "" && !fable.ValidAt(time.Now()) {
			manifest["dispatch"] = "locked"
		}
		if config.ReadOnly {
			manifest["auth"] = "enabled"
		}
		c.JSON(http.StatusOK, manifest)
	})
	account := r.Group("/api/workbench", workbenchPilotNoStore, middleware.RouteTag("api"),
		middleware.ZTAPIAdminHostGate(), middleware.GlobalAPIRateLimit(), middleware.ZTAPIUserAuth(), p.accountGate)
	account.GET("/self", p.accountSnapshot)
	account.GET("/models", p.accountCatalog(config.DrainOnly, !config.ReadOnly))
	budget := r.Group("", workbenchPilotNoStore, middleware.RouteTag("relay"), middleware.GlobalAPIRateLimit(), middleware.BodyStorageCleanup())
	if config.ReadOnly {
		account.POST("/keys", workbenchPilotUnavailable)
		for _, path := range []string{"/reserve", "/responses", "/release"} {
			budget.POST("/__workbench_budget"+path, workbenchPilotUnavailable)
		}
		return
	}
	account.POST("/keys", middleware.CriticalRateLimit(), func(c *gin.Context) {
		if config.DrainOnly || fable.BatchID != "" && !fable.ValidAt(time.Now()) {
			workbenchPilotUnavailable(c)
			return
		}
		p.createTaskKey(c)
	})
	keys := account.Group("/keys/:id", p.taskKeyGate)
	keys.GET("", controller.GetToken)
	keys.DELETE("", controller.DeleteToken)
	gate := func(c *gin.Context) {
		if config.DrainOnly && c.FullPath() != "/__workbench_budget/release" {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		if fable.BatchID != "" && c.FullPath() != "/__workbench_budget/release" && !fable.ValidAt(time.Now()) {
			workbenchPilotUnavailable(c)
			return
		}
		if fable.BatchID != "" {
			service.BindZTAPIWorkbenchPilotPolicy(c, fable)
		}
		p.dispatchGate(c)
	}
	mountWorkbenchBudgetRoutesWithSelection(budget, config.TaskLimitQuota, []gin.HandlerFunc{gate}, p.selectedChannelGate)
}

func workbenchPilotUnavailable(c *gin.Context) {
	c.AbortWithStatus(http.StatusServiceUnavailable)
}

func workbenchPilotNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}
