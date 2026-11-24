package main

import (
	"errors"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/router"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

type pilotSettings struct {
	listen   netip.AddrPort
	policy   string
	userID   int
	proxyIPs string
}

func settingsFromEnv() (pilotSettings, error) {
	var config pilotSettings
	if os.Getenv("NODE_TYPE") != "slave" {
		return config, errors.New("workbench pilot must not run database migrations")
	}
	listen, err := netip.ParseAddrPort(os.Getenv("ZTAPI_WORKBENCH_PILOT_LISTEN"))
	if err != nil || !listen.Addr().Is4() || (!listen.Addr().IsLoopback() && !listen.Addr().IsPrivate()) || listen.Port() < 1024 {
		return config, errors.New("a private IPv4 pilot listener is required")
	}
	policy := os.Getenv("ZTAPI_WORKBENCH_PILOT_CONFIG")
	userID, err := strconv.Atoi(os.Getenv("ZTAPI_WORKBENCH_PILOT_USER_ID"))
	proxies := os.Getenv("ZTAPI_TRUSTED_PROXY_CIDRS")
	if err != nil || userID < 1 || !filepath.IsAbs(policy) || proxies == "" {
		return config, errors.New("explicit pilot account, policy file and trusted proxy are required")
	}
	if _, err := router.ParseTrustedProxyCIDRs(proxies, true); err != nil {
		return config, errors.New("invalid workbench pilot trusted proxy")
	}
	return pilotSettings{listen: listen, policy: policy, userID: userID, proxyIPs: proxies}, nil
}

func run() error {
	config, err := settingsFromEnv()
	if err != nil {
		return err
	}
	if err := service.ConfigureZTAPISessionSigningKeyFromEnv(); err != nil {
		return errors.New("workbench session verification is not configured")
	}
	// Do not run InitResources or any normal-server workers here, even in slave mode.
	common.InitEnv()
	if common.IsMasterNode || common.DebugEnabled {
		return errors.New("master or debug mode is not allowed in the workbench pilot")
	}
	if os.Getenv("SQL_DSN") == "" {
		info, err := os.Lstat(common.SQLitePath)
		if err != nil || !filepath.IsAbs(common.SQLitePath) || !info.Mode().IsRegular() {
			return errors.New("an existing absolute SQLite database or a managed database is required")
		}
	}
	logger.SetupLogger()
	ratio_setting.InitRatioSettings()
	service.InitHttpClient()
	service.InitTokenEncoders()
	if err := model.InitDB(); err != nil {
		return errors.New("workbench pilot database unavailable")
	}
	defer model.CloseDB()
	if err := model.InitLogDB(); err != nil {
		return errors.New("workbench pilot log database unavailable")
	}
	if err := common.InitRedisClient(); err != nil {
		return errors.New("workbench pilot rate limit store unavailable")
	}
	common.MemoryCacheEnabled = false
	model.InitOptionMap()
	if err := i18n.Init(); err != nil {
		return errors.New("workbench pilot localization unavailable")
	}
	i18n.SetUserLangLoader(model.GetUserLanguage)

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	if err := router.ConfigureTrustedProxies(engine, config.proxyIPs, true); err != nil {
		return errors.New("workbench pilot trusted proxy configuration rejected")
	}
	engine.Use(gin.Recovery(), middleware.RequestId(), middleware.I18n())
	middleware.SetUpLogger(engine)
	if err := router.MountZTAPISonnetPilot(engine, config.policy, config.userID); err != nil {
		return errors.New("workbench pilot policy or database schema rejected")
	}
	server := &http.Server{Addr: config.listen.String(), Handler: engine, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	return server.ListenAndServe()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
