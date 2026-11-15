package router

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const workbenchAcceptanceKeyLimit = 500000

var workbenchAcceptanceKeyName = regexp.MustCompile(`^workbench-r37-[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)
var workbenchAcceptanceModelName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type WorkbenchAcceptanceConfig struct {
	TaskLimitQuota int64                      `json:"task_limit_quota"`
	UserIDs        []int                      `json:"user_ids"`
	Models         []WorkbenchAcceptanceModel `json:"models"`
	Locked         bool                       `json:"locked"`
	LoginOnly      bool                       `json:"login_only"`
}

type WorkbenchAcceptanceModel struct {
	Model            string `json:"model"`
	Version          uint64 `json:"version"`
	SnapshotID       int64  `json:"snapshot_id"`
	ChannelID        int    `json:"channel_id"`
	UpstreamOrigin   string `json:"upstream_origin,omitempty"`
	CredentialSHA256 string `json:"credential_sha256,omitempty"`
}

type workbenchAcceptancePolicy struct {
	users  map[int]bool
	models map[string]WorkbenchAcceptanceModel
}

func newWorkbenchAcceptancePolicy(config WorkbenchAcceptanceConfig) (workbenchAcceptancePolicy, error) {
	p := workbenchAcceptancePolicy{users: make(map[int]bool), models: make(map[string]WorkbenchAcceptanceModel)}
	if config.TaskLimitQuota < 1 || config.TaskLimitQuota > workbenchAcceptanceKeyLimit ||
		len(config.UserIDs) < 1 || len(config.UserIDs) > 20 || (!config.LoginOnly && len(config.Models) < 1) ||
		len(config.Models) > 20 || (config.LoginOnly && (!config.Locked || len(config.Models) != 0)) {
		return p, errors.New("invalid workbench acceptance bounds")
	}
	for _, id := range config.UserIDs {
		if id < 1 || p.users[id] {
			return p, errors.New("invalid workbench acceptance account")
		}
		p.users[id] = true
	}
	for _, item := range config.Models {
		if !workbenchAcceptanceModelName.MatchString(item.Model) || item.Version < 1 || item.SnapshotID < 1 ||
			item.ChannelID < 1 || p.models[item.Model].Model != "" {
			return p, errors.New("invalid workbench acceptance publication")
		}
		p.models[item.Model] = item
	}
	return p, nil
}

func (p workbenchAcceptancePolicy) allowedAccount(id int) (*model.User, bool) {
	if !p.users[id] {
		return nil, false
	}
	user, err := model.GetUserById(id, false)
	return user, err == nil && user.Status == common.UserStatusEnabled && user.Role == common.RoleCommonUser
}

func (p workbenchAcceptancePolicy) usableModel(c *gin.Context, id, group string) bool {
	pin, ok := p.models[id]
	if !ok || group == "" || group == "auto" {
		return false
	}
	pub, err := model.GetZTAPIRuntimePublication(id)
	if err != nil || pub.PublicName != id || pub.Modality != model.ZTAPIModalityText || pub.Version != pin.Version ||
		pub.SnapshotID != pin.SnapshotID || !slices.Contains(pub.AllowedChannelIDs, pin.ChannelID) ||
		!model.IsChannelEnabledForGroupModel(group, pub.SourceModel, pin.ChannelID) {
		return false
	}
	channel, err := model.GetChannelById(pin.ChannelID, false)
	if err != nil || channel.Status != common.ChannelStatusEnabled {
		return false
	}
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	middleware.PinZTAPIWorkbenchPublication(c, id, pin.Version, pin.SnapshotID, pin.ChannelID)
	snapshot, err := middleware.LoadZTAPIWorkbenchPublication(c, id)
	if err != nil || snapshot.Version != pin.Version || !slices.Equal(snapshot.AllowedChannelIDs, []int{pin.ChannelID}) {
		return false
	}
	_, err = service.ZTAPIWorkbenchQuote(snapshot, 1024, 2048)
	return err == nil
}

func (p workbenchAcceptancePolicy) accountGate(c *gin.Context) {
	if _, ok := p.allowedAccount(c.GetInt("id")); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Next()
}

func (p workbenchAcceptancePolicy) ownedTaskKey(c *gin.Context, keyID int) (*model.Token, bool) {
	token, err := model.GetTokenByIds(keyID, c.GetInt("id"))
	return token, err == nil && token.WorkbenchOnly
}

func (p workbenchAcceptancePolicy) dispatchGate(c *gin.Context) {
	user, ok := p.allowedAccount(c.GetInt("id"))
	if !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	key, ok := p.ownedTaskKey(c, c.GetInt("token_id"))
	if !ok || key.UnlimitedQuota || !key.ModelLimitsEnabled || key.CrossGroupRetry || key.Group != "" ||
		key.RemainQuota < 0 || key.RemainQuota > workbenchAcceptanceKeyLimit ||
		key.ExpiredTime <= time.Now().Unix() || key.ExpiredTime > time.Now().Add(30*time.Minute).Unix() {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if c.Request.URL.Path != "/__workbench_budget/release" && !p.usableModel(c, key.ModelLimits, user.Group) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Next()
}

func (p workbenchAcceptancePolicy) selectedChannelGate(c *gin.Context) {
	snapshot := relaycommon.GetZTAPIPublicationSnapshot(c)
	if snapshot == nil {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	pin := p.models[snapshot.PublicName]
	if pin.ChannelID < 1 || snapshot.Version != pin.Version ||
		!slices.Equal(snapshot.AllowedChannelIDs, []int{pin.ChannelID}) ||
		common.GetContextKeyInt(c, constant.ContextKeyChannelId) != pin.ChannelID {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	if pin.UpstreamOrigin != "" {
		setting, ok := common.GetContextKeyType[dto.ChannelSettings](c, constant.ContextKeyChannelSetting)
		origin := strings.TrimSuffix(common.GetContextKeyString(c, constant.ContextKeyChannelBaseUrl), "/")
		key := common.GetContextKeyString(c, constant.ContextKeyChannelKey)
		if !ok || setting.Proxy != "" || common.GetContextKeyBool(c, constant.ContextKeyChannelIsMultiKey) ||
			origin != pin.UpstreamOrigin || key == "" || subtle.ConstantTimeCompare([]byte(common.HashZTAPIKey(key)), []byte(pin.CredentialSHA256)) != 1 {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
	}
	c.Next()
}

func (p workbenchAcceptancePolicy) createTaskKey(c *gin.Context) {
	var policy struct {
		Name               string `json:"name"`
		Status             int    `json:"status"`
		ExpiredTime        int64  `json:"expired_time"`
		RemainQuota        int    `json:"remain_quota"`
		UnlimitedQuota     bool   `json:"unlimited_quota"`
		ModelLimitsEnabled bool   `json:"model_limits_enabled"`
		ModelLimits        string `json:"model_limits"`
		Group              string `json:"group"`
		CrossGroupRetry    bool   `json:"cross_group_retry"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}
	if common.RejectDuplicateJsonObjectMembers(bytes.NewReader(data)) != nil ||
		common.DecodeJsonStrict(bytes.NewReader(data), &policy) != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	user, ok := p.allowedAccount(c.GetInt("id"))
	now := time.Now().Unix()
	if !ok || !p.usableModel(c, policy.ModelLimits, user.Group) || !workbenchAcceptanceKeyName.MatchString(policy.Name) ||
		policy.Status != common.TokenStatusEnabled || policy.ExpiredTime <= now || policy.ExpiredTime > now+1800 ||
		policy.RemainQuota < 1 || policy.RemainQuota > min(user.Quota, workbenchAcceptanceKeyLimit) ||
		policy.UnlimitedQuota || !policy.ModelLimitsEnabled || policy.Group != "" || policy.CrossGroupRetry {
		c.Status(http.StatusForbidden)
		return
	}
	body, err := common.Marshal(policy)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Request.ContentLength = int64(len(body))
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	controller.AddWorkbenchToken(c)
}

// MountZTAPIWorkbenchAcceptance exposes only the desktop contract, not the general API.
func MountZTAPIWorkbenchAcceptance(r gin.IRouter, config WorkbenchAcceptanceConfig) error {
	p, err := newWorkbenchAcceptancePolicy(config)
	if err != nil {
		return err
	}
	r.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	r.GET("/.well-known/ztapi-workbench", func(c *gin.Context) {
		dispatch := "locked"
		if !config.Locked {
			for _, id := range config.UserIDs {
				if user, ok := p.allowedAccount(id); ok {
					for name := range p.models {
						if p.usableModel(c, name, user.Group) {
							dispatch = "enabled"
						}
					}
				}
			}
		}
		manifest := gin.H{"protocol": "ztapi-workbench-first-party-v1", "budget": "wire-bound-v1",
			"mode": "acceptance", "dispatch": dispatch}
		if config.LoginOnly {
			manifest["auth"] = "enabled"
		}
		c.JSON(http.StatusOK, manifest)
	})
	if config.Locked && !config.LoginOnly {
		return nil
	}
	api := r.Group("/api", middleware.GlobalAPIRateLimit(), middleware.BodyStorageCleanup())
	api.POST("/auth/login", middleware.CriticalRateLimit(), middleware.AnonymousRequestBodyLimit(), controller.ZTAPILogin)
	api.POST("/auth/refresh", middleware.ZTAPIRefreshRateLimit(), middleware.AnonymousRequestBodyLimit(), controller.ZTAPIRefresh)
	api.POST("/auth/logout", controller.ZTAPILogout)
	self := api.Group("", middleware.ZTAPIUserAuth(), p.accountGate)
	self.GET("/auth/session", controller.ZTAPISession)
	self.GET("/user/self", p.accountSnapshot)
	self.GET("/user/models", p.accountCatalog(config.LoginOnly, true))
	if config.LoginOnly {
		return nil
	}
	self.POST("/token/", p.createTaskKey)
	tokenRoute := self.Group("/token/:id", p.taskKeyGate)
	tokenRoute.GET("", controller.GetToken)
	tokenRoute.DELETE("", controller.DeleteToken)
	budget := r.Group("", middleware.BodyStorageCleanup())
	mountWorkbenchBudgetRoutesWithSelection(budget, config.TaskLimitQuota, []gin.HandlerFunc{p.dispatchGate}, p.selectedChannelGate)
	return nil
}

func (p workbenchAcceptancePolicy) accountSnapshot(c *gin.Context) {
	user, ok := p.allowedAccount(c.GetInt("id"))
	if !ok {
		c.Status(http.StatusForbidden)
		return
	}
	common.ApiSuccess(c, gin.H{"id": user.Id, "quota": user.Quota})
}

func (p workbenchAcceptancePolicy) accountCatalog(empty, projectTools bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := p.allowedAccount(c.GetInt("id"))
		if !ok {
			c.Status(http.StatusForbidden)
			return
		}
		if empty {
			c.JSON(http.StatusOK, gin.H{"success": true, "data": []string{}, "catalog": []gin.H{}})
			return
		}
		catalog, err := model.ListZTAPIPublicCatalog()
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		rows, ids := make([]gin.H, 0), make([]string, 0)
		for _, item := range model.FilterZTAPIPublicCatalogByGroups(catalog, []string{user.Group}) {
			if !p.usableModel(c, item.ModelName, user.Group) {
				continue
			}
			ids = append(ids, item.ModelName)
			rows = append(rows, gin.H{"model_name": item.ModelName, "modality": item.Modality,
				"supported_endpoint_types": item.SupportedEndpointTypes, "workbench": gin.H{
					"contract": "ztapi-workbench-responses-v1", "project_tools": projectTools,
				}})
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": ids, "catalog": rows})
	}
}

func (p workbenchAcceptancePolicy) taskKeyGate(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 || strings.Contains(c.Param("id"), "+") {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if _, ok := p.ownedTaskKey(c, id); !ok {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Next()
}
