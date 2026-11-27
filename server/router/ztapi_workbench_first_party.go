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
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
	OpusBeta        bool                    `json:"opus_beta,omitempty"`
	AllActiveUsers  bool                    `json:"all_active_users,omitempty"`
	FableCap        *workbenchFablePilotCap `json:"fable_cap,omitempty"`
	WalletCap       *workbenchFablePilotCap `json:"wallet_cap,omitempty"`
}

type workbenchFablePilotCap struct {
	BatchID          string `json:"batch_id"`
	ExpiresAt        int64  `json:"expires_at"`
	BudgetBasis      string `json:"budget_basis"`
	MaxRequests      int    `json:"max_requests"`
	BudgetNanoU      int64  `json:"budget_nano_u"`
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
	UpstreamModel    string `json:"upstream_model,omitempty"`
}

func (config workbenchPilotConfig) fablePolicy() (model.ZTAPIWorkbenchPilotPolicy, error) {
	cap := config.FableCap
	rejected := errors.New("workbench pilot wallet cap unavailable")
	var empty model.ZTAPIWorkbenchPilotPolicy
	if config.WalletCap != nil {
		if cap != nil {
			return empty, rejected
		}
		cap = config.WalletCap
	}
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
	if cap == nil || len(config.UserIDs) != 1 || len(config.Models) != 1 {
		return empty, rejected
	}
	pin := config.Models[0]
	if config.FableCap != nil && !hasFable || config.WalletCap != nil &&
		(!strings.HasPrefix(pin.Model, "zt-claude-") || len(pin.Model) == len("zt-claude-")) {
		return empty, rejected
	}
	publicModel := ""
	upstreamPath := ""
	wirePath := ""
	if config.WalletCap != nil {
		publicModel = pin.Model
		switch cap.UpstreamProtocol {
		case "":
		case "chat-completions":
			upstreamPath = model.ZTAPIWorkbenchChatPath
			u, err := url.Parse(pin.UpstreamOrigin)
			if err != nil || u.RawPath != "" {
				return empty, rejected
			}
			switch u.Path {
			case "":
			case "/hub":
				wirePath = model.ZTAPIWorkbenchHubChatPath
			default:
				return empty, rejected
			}
		default:
			return empty, rejected
		}
	} else if cap.UpstreamProtocol != "" || cap.UpstreamModel != "" {
		return empty, rejected
	}
	quota, ok := model.ZTAPIWorkbenchPilotWalletQuota(cap.BudgetNanoU)
	if !ok || cap.BudgetBasis != "ztapi_wallet" {
		return empty, rejected
	}
	digestInput, err := common.Marshal(struct {
		Cap         workbenchFablePilotCap
		Pin         WorkbenchAcceptanceModel
		UID         int
		WalletQuota int64
	}{*cap, pin, config.UserIDs[0], quota})
	if err != nil {
		return empty, rejected
	}
	digest := string(digestInput)
	if publicModel != "" {
		digest = "messages-v1/" + digest
	}
	p := model.ZTAPIWorkbenchPilotPolicy{BatchID: cap.BatchID, PolicySHA256: common.HashZTAPIKey(digest),
		ExpiresAt: cap.ExpiresAt, MaxRequests: cap.MaxRequests, BudgetQuota: quota,
		UserID: config.UserIDs[0], ChannelID: pin.ChannelID, PublicModel: publicModel, UpstreamPath: upstreamPath,
		WirePath: wirePath, UpstreamModel: cap.UpstreamModel}
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

func readWorkbenchPilotFile(path string) (workbenchPilotConfig, error) {
	const maxConfig = 32 << 10
	var config workbenchPilotConfig
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > maxConfig {
		return config, errors.New("invalid workbench pilot file")
	}
	file, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxConfig+1))
	if err != nil || len(raw) > maxConfig {
		return config, errors.New("invalid workbench pilot file size")
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(raw)) != nil ||
		common.DecodeJsonStrict(bytes.NewReader(raw), &config) != nil || !config.validMode() {
		return config, errors.New("invalid workbench pilot settings")
	}
	return config, nil
}

func mountWorkbenchPilotFile(r gin.IRouter, path string) error {
	config, err := readWorkbenchPilotFile(path)
	if err != nil {
		return err
	}
	if config.OpusBeta {
		return errors.New("stable workbench beta requires the dedicated service")
	}
	return mountWorkbenchPilotConfig(r, config)
}

// MountZTAPISonnetPilot preserves the original dedicated Sonnet policy.
func MountZTAPISonnetPilot(r gin.IRouter, path string, expectedUserID int) error {
	return MountZTAPIWorkbenchDedicatedPilot(r, path, expectedUserID, "")
}

// MountZTAPIWorkbenchDedicatedPilot registers only the named, single-model test service.
func MountZTAPIWorkbenchDedicatedPilot(r gin.IRouter, path string, expectedUserID int, expectedModel string) error {
	const sonnet = "zt-claude-sonnet-5"
	const mappedOpus = "zt-claude-opus-5.5"
	switch expectedModel {
	case "", sonnet:
		expectedModel = sonnet
	case mappedOpus:
	default:
		return errors.New("unapproved dedicated workbench test model")
	}
	if expectedUserID <= 0 || !filepath.IsAbs(path) {
		return errors.New("explicit workbench test account and config required")
	}
	config, err := readWorkbenchPilotFile(path)
	if err != nil {
		return err
	}
	if config.OpusBeta || config.AllActiveUsers || !config.DispatchEnabled || config.WalletCap == nil || config.FableCap != nil ||
		len(config.UserIDs) != 1 || config.UserIDs[0] != expectedUserID ||
		len(config.Models) != 1 || config.Models[0].Model != expectedModel ||
		config.Models[0].CredentialSHA256 == strings.Repeat("0", 64) {
		return errors.New("unapproved workbench test account, model or wallet scope")
	}
	policy, err := config.fablePolicy()
	if err != nil || policy.BudgetQuota < config.TaskLimitQuota {
		return errors.New("invalid workbench test wallet scope")
	}
	if expectedModel == sonnet && policy.WirePath != "" ||
		expectedModel == mappedOpus && (policy.ExpectedWirePath() != model.ZTAPIWorkbenchHubChatPath ||
			policy.ChannelID != 2 || policy.UpstreamModel != "claude-opus-5-5-hc") {
		return errors.New("unapproved dedicated workbench wire binding")
	}
	return mountWorkbenchPilotConfig(r, config)
}

// MountZTAPIWorkbenchOpusBeta uses only eligible users' normal wallet
// reservations; a beta config cannot reuse any timed test budget.
func MountZTAPIWorkbenchOpusBeta(r gin.IRouter, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("an absolute workbench beta config is required")
	}
	config, err := readWorkbenchPilotFile(path)
	if err != nil {
		return err
	}
	if !config.OpusBeta || config.WalletCap != nil || config.FableCap != nil || len(config.Models) != 1 {
		return errors.New("an explicit single-model workbench beta scope is required")
	}
	pin := config.Models[0]
	if pin.Model != model.ZTAPIWorkbenchOpusBetaModel || pin.ChannelID != model.ZTAPIWorkbenchOpusBetaChannel ||
		pin.UpstreamOrigin != model.ZTAPIWorkbenchOpusBetaOrigin || !validWorkbenchDispatchBinding(pin) ||
		pin.CredentialSHA256 == strings.Repeat("0", 64) {
		return errors.New("unverified workbench beta channel")
	}
	return mountWorkbenchPilotConfig(r, config)
}

func mountWorkbenchPilotConfig(r gin.IRouter, config workbenchPilotConfig) error {
	if config.AllActiveUsers && (!config.OpusBeta || config.WalletCap != nil || config.FableCap != nil) {
		return errors.New("platform account scope requires dedicated workbench beta without a test budget")
	}
	p, err := newWorkbenchAccountPolicy(config.WorkbenchAcceptanceConfig, config.AllActiveUsers)
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
	if fable.PublicModel != "" {
		p.messagesChannelType = constant.ChannelTypeAnthropic
		if fable.ExpectedUpstreamPath() == model.ZTAPIWorkbenchChatPath {
			p.messagesChannelType = constant.ChannelTypeOpenAI
		}
	}
	if config.OpusBeta {
		p.messagesChannelType = constant.ChannelTypeOpenAI
	}
	r.GET("/.well-known/ztapi-workbench", workbenchPilotNoStore, func(c *gin.Context) {
		manifest := gin.H{"protocol": "ztapi-workbench-first-party-v1", "budget": "wire-bound-v1",
			"dispatch": "enabled", "account_routes": "workbench-v1"}
		if config.ReadOnly || config.DrainOnly || fable.BatchID != "" && !fable.ValidAt(time.Now()) {
			manifest["dispatch"] = "locked"
		}
		if config.ReadOnly {
			manifest["auth"] = "enabled"
		}
		if p.messagesChannelType != 0 {
			manifest["messages"] = "wire-bound-v1"
		}
		c.JSON(http.StatusOK, manifest)
	})
	account := r.Group("/api/workbench", workbenchPilotNoStore, middleware.RouteTag("api"),
		middleware.ZTAPIAdminHostGate(), middleware.GlobalAPIRateLimit(), middleware.ZTAPIUserAuth(), p.accountGate)
	account.GET("/self", p.accountSnapshot)
	account.GET("/models", p.accountCatalog(config.DrainOnly, !config.ReadOnly, config.AllActiveUsers))
	budget := r.Group("", workbenchPilotNoStore, middleware.RouteTag("relay"), middleware.GlobalAPIRateLimit(), middleware.BodyStorageCleanup())
	if config.ReadOnly {
		account.POST("/keys", workbenchPilotUnavailable)
		for _, path := range []string{"/reserve", "/responses", "/release"} {
			budget.POST("/__workbench_budget"+path, workbenchPilotUnavailable)
		}
		if config.OpusBeta {
			budget.POST("/__workbench_budget/messages/reserve", workbenchPilotUnavailable)
			budget.POST("/__workbench_budget/messages", workbenchPilotUnavailable)
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
		if config.OpusBeta {
			service.BindZTAPIWorkbenchOpusBeta(c)
		}
		p.dispatchGate(c)
	}
	if p.messagesChannelType != 0 {
		mountWorkbenchMessagesRoutes(budget, config.TaskLimitQuota, gate, p.selectedChannelGate)
	} else {
		mountWorkbenchBudgetRoutesWithSelection(budget, config.TaskLimitQuota, []gin.HandlerFunc{gate}, p.selectedChannelGate)
	}
}

func workbenchPilotUnavailable(c *gin.Context) {
	c.AbortWithStatus(http.StatusServiceUnavailable)
}

func workbenchPilotNoStore(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Next()
}
