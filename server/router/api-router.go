package router

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	// Import oauth package to register providers via init()
	_ "github.com/QuantumNous/new-api/oauth"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

const legacyPaymentEnabledEnv = "ZTAPI_LEGACY_PAYMENT_ENABLED"

func SetApiRouter(router *gin.Engine) {
	router.GET("/.well-known/source", controller.GetZTAPISource)

	apiRouter := router.Group("/api")
	apiRouter.Use(middleware.RouteTag("api"))
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.BodyStorageCleanup()) // 清理请求体存储
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	apiRouter.Use(middleware.ZTAPIAdminHostGate())
	anonymousRequestBodyLimit := middleware.AnonymousRequestBodyLimit()
	legacyPaymentEnabled := legacyPaymentRoutesEnabled()
	{
		apiRouter.GET("/setup", controller.GetSetup)
		apiRouter.POST("/setup", anonymousRequestBodyLimit, controller.PostSetup)
		apiRouter.GET("/status", controller.GetStatus)
		apiRouter.GET("/uptime/status", controller.GetUptimeKumaStatus)
		apiRouter.GET("/models", middleware.UserAuth(), controller.DashboardListModels)
		apiRouter.GET("/status/test", middleware.AdminAuth(), controller.TestStatus)
		apiRouter.GET("/notice", controller.GetNotice)
		apiRouter.GET("/user-agreement", controller.GetUserAgreement)
		apiRouter.GET("/privacy-policy", controller.GetPrivacyPolicy)
		apiRouter.GET("/about", controller.GetAbout)
		//apiRouter.GET("/midjourney", controller.GetMidjourney)
		apiRouter.GET("/home_page_content", controller.GetHomePageContent)
		apiRouter.GET("/pricing", middleware.HeaderNavModuleAuth("pricing"), controller.GetPricing)
		perfMetricsRoute := apiRouter.Group("/perf-metrics")
		perfMetricsRoute.Use(middleware.HeaderNavModulePublicOrUserAuth("pricing"))
		{
			perfMetricsRoute.GET("/summary", controller.GetPerfMetricsSummary)
			perfMetricsRoute.GET("", controller.GetPerfMetrics)
		}
		apiRouter.GET("/rankings", middleware.HeaderNavModuleAuth("rankings"), controller.GetRankings)
		apiRouter.GET("/verification", middleware.EmailVerificationRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification)
		apiRouter.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail)
		apiRouter.POST("/user/reset", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.ResetPassword)
		// OAuth routes - specific routes must come before :provider wildcard
		apiRouter.GET("/oauth/state", middleware.CriticalRateLimit(), controller.GenerateOAuthCode)
		apiRouter.POST("/oauth/email/bind", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.EmailBind)
		// Non-standard OAuth (WeChat, Telegram) - keep original routes
		apiRouter.GET("/oauth/wechat", middleware.CriticalRateLimit(), controller.WeChatAuth)
		apiRouter.POST("/oauth/wechat/bind", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.WeChatBind)
		apiRouter.GET("/oauth/telegram/login", middleware.CriticalRateLimit(), controller.TelegramLogin)
		apiRouter.GET("/oauth/telegram/bind", middleware.CriticalRateLimit(), controller.TelegramBind)
		// Standard OAuth providers (GitHub, Discord, OIDC, LinuxDO) - unified route
		apiRouter.GET("/oauth/:provider", middleware.CriticalRateLimit(), controller.HandleOAuth)
		apiRouter.GET("/ratio_config", middleware.CriticalRateLimit(), controller.GetRatioConfig)

		if legacyPaymentEnabled {
			apiRouter.POST("/stripe/webhook", anonymousRequestBodyLimit, controller.StripeWebhook)
			apiRouter.POST("/creem/webhook", anonymousRequestBodyLimit, controller.CreemWebhook)
			apiRouter.POST("/waffo/webhook", anonymousRequestBodyLimit, controller.WaffoWebhook)
			// :env separates test vs prod URLs so the operator can register each
			// in Pancake's matching webhook slot; handler enforces env match.
			apiRouter.POST("/waffo-pancake/webhook/:env", anonymousRequestBodyLimit, controller.WaffoPancakeWebhook)
		}

		authRoute := apiRouter.Group("/auth")
		{
			authRoute.POST("/register", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.ZTAPIRegister)
			authRoute.POST("/login", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.ZTAPILogin)
			authRoute.POST("/refresh", middleware.ZTAPIRefreshRateLimit(), anonymousRequestBodyLimit, controller.ZTAPIRefresh)
			authRoute.POST("/logout", controller.ZTAPILogout)
			authRoute.GET("/session", middleware.ZTAPIUserAuth(), controller.ZTAPISession)
		}

		// Universal secure verification routes
		apiRouter.POST("/verify", middleware.UserAuth(), middleware.CriticalRateLimit(), controller.UniversalVerify)

		userRoute := apiRouter.Group("/user")
		{
			usdtTopUpRoute := userRoute.Group("/topup/usdt-trc20")
			usdtTopUpRoute.Use(middleware.UserAuth())
			{
				usdtTopUpRoute.POST("/orders", middleware.CriticalRateLimit(), controller.CreateUSDTTopUpOrder)
				usdtTopUpRoute.GET("/orders/:trade_no", controller.GetUSDTTopUpOrder)
				usdtTopUpRoute.POST("/orders/:trade_no/cancel", controller.CancelUSDTTopUpOrder)
			}

			userRoute.POST("/login/2fa", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.Verify2FALogin)
			userRoute.POST("/passkey/login/begin", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.PasskeyLoginBegin)
			userRoute.POST("/passkey/login/finish", middleware.CriticalRateLimit(), anonymousRequestBodyLimit, controller.PasskeyLoginFinish)
			//userRoute.POST("/tokenlog", middleware.CriticalRateLimit(), controller.TokenLog)
			if legacyPaymentEnabled {
				userRoute.POST("/epay/notify", anonymousRequestBodyLimit, controller.EpayNotify)
				userRoute.GET("/epay/notify", controller.EpayNotify)
			}
			userRoute.GET("/groups", controller.GetUserGroups)

			selfRoute := userRoute.Group("/")
			selfRoute.Use(middleware.UserAuth())
			{
				selfRoute.GET("/self/groups", controller.GetUserGroups)
				selfRoute.GET("/self", controller.GetSelf)
				selfRoute.GET("/pending-settlements", controller.GetSelfZTAPIRequestSettlements)
				selfRoute.GET("/models", controller.GetUserModels)
				selfRoute.PUT("/self", controller.UpdateSelf)
				selfRoute.DELETE("/self", controller.DeleteSelf)
				selfRoute.GET("/token", controller.GenerateAccessToken)
				selfRoute.GET("/passkey", controller.PasskeyStatus)
				selfRoute.POST("/passkey/register/begin", controller.PasskeyRegisterBegin)
				selfRoute.POST("/passkey/register/finish", controller.PasskeyRegisterFinish)
				selfRoute.POST("/passkey/verify/begin", controller.PasskeyVerifyBegin)
				selfRoute.POST("/passkey/verify/finish", controller.PasskeyVerifyFinish)
				selfRoute.DELETE("/passkey", controller.PasskeyDelete)
				selfRoute.GET("/aff", controller.GetAffCode)
				selfRoute.GET("/topup/info", controller.GetTopUpInfo)
				selfRoute.GET("/topup/self", controller.GetUserTopUps)
				if legacyPaymentEnabled {
					selfRoute.POST("/topup", middleware.CriticalRateLimit(), controller.TopUp)
					selfRoute.POST("/pay", middleware.CriticalRateLimit(), controller.RequestEpay)
					selfRoute.POST("/amount", controller.RequestAmount)
					selfRoute.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.RequestStripePay)
					selfRoute.POST("/stripe/amount", controller.RequestStripeAmount)
					selfRoute.POST("/creem/pay", middleware.CriticalRateLimit(), controller.RequestCreemPay)
					selfRoute.POST("/waffo/amount", controller.RequestWaffoAmount)
					selfRoute.POST("/waffo/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPay)
					selfRoute.POST("/waffo-pancake/amount", controller.RequestWaffoPancakeAmount)
					selfRoute.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.RequestWaffoPancakePay)
				}
				selfRoute.POST("/aff_transfer", controller.TransferAffQuota)
				selfRoute.PUT("/setting", controller.UpdateUserSetting)

				// 2FA routes
				selfRoute.GET("/2fa/status", controller.Get2FAStatus)
				selfRoute.POST("/2fa/setup", controller.Setup2FA)
				selfRoute.POST("/2fa/enable", controller.Enable2FA)
				selfRoute.POST("/2fa/disable", controller.Disable2FA)
				selfRoute.POST("/2fa/backup_codes", controller.RegenerateBackupCodes)

				// Check-in routes
				selfRoute.GET("/checkin", controller.GetCheckinStatus)
				selfRoute.POST("/checkin", middleware.TurnstileCheck(), controller.DoCheckin)

				// Custom OAuth bindings
				selfRoute.GET("/oauth/bindings", controller.GetUserOAuthBindings)
				selfRoute.DELETE("/oauth/bindings/:provider_id", controller.UnbindCustomOAuth)
			}

			adminRoute := userRoute.Group("/")
			{
				adminRoute.GET("/", middleware.AdminPermissionAuth(common.PermissionUserRead), controller.GetAllUsers)
				adminRoute.GET("/topup", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAllTopUps)
				adminRoute.POST("/topup/complete", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.AdminCompleteTopUp)
				adminRoute.GET("/search", middleware.AdminPermissionAuth(common.PermissionUserRead), controller.SearchUsers)
				adminRoute.GET("/:id/oauth/bindings", middleware.AdminPermissionAuth(common.PermissionUserRead), controller.GetUserOAuthBindingsByAdmin)
				adminRoute.DELETE("/:id/oauth/bindings/:provider_id", middleware.AdminAuth(), controller.UnbindCustomOAuthByAdmin)
				adminRoute.DELETE("/:id/bindings/:binding_type", middleware.AdminAuth(), controller.AdminClearUserBinding)
				adminRoute.POST("/", middleware.AdminAuth(), controller.CreateUser)
				adminRoute.POST("/manage", middleware.AdminAuth(), controller.ManageUser)
				adminRoute.PUT("/", middleware.AdminAuth(), controller.UpdateUser)
				adminRoute.DELETE("/:id", middleware.AdminAuth(), controller.DeleteUser)
				adminRoute.DELETE("/:id/reset_passkey", middleware.AdminAuth(), controller.AdminResetPasskey)

				// Admin 2FA routes
				adminRoute.GET("/2fa/stats", middleware.AdminAuth(), controller.Admin2FAStats)
				adminRoute.DELETE("/:id/2fa", middleware.AdminAuth(), controller.AdminDisable2FA)
			}
			userRoute.GET("/:id", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionUserRead), controller.GetUser)
		}

		adminBalanceRoute := apiRouter.Group("/admin")
		{
			adminBalanceRoute.GET("/overview", middleware.AdminPermissionAuth(common.PermissionOverviewRead), controller.GetAdminOverview)
			adminBalanceRoute.GET("/topups", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminTopUps)
			adminBalanceRoute.GET("/topups/export", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.ExportAdminTopUps)
			adminBalanceRoute.POST("/topups/:id/complete", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.CompleteAdminTopUp)
			adminBalanceRoute.POST("/topups/:id/reject", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.RejectAdminTopUp)
			adminBalanceRoute.GET("/request-logs", middleware.AdminPermissionAuth(common.PermissionLogRead), controller.GetAdminRequestLogs)
			adminBalanceRoute.GET("/request-logs/export", middleware.AdminPermissionAuth(common.PermissionLogRead), controller.ExportAdminRequestLogs)
			adminBalanceRoute.GET("/audit-logs", middleware.AdminPermissionAuth(common.PermissionAuditRead), controller.GetAdminAuditLogs)
			adminBalanceRoute.GET("/audit-logs/export", middleware.AdminPermissionAuth(common.PermissionAuditRead), controller.ExportAdminAuditLogs)
			adminBalanceRoute.GET("/settings", middleware.AdminPermissionAuth(common.PermissionSystemWrite), controller.GetAdminSettings)
			adminBalanceRoute.PATCH("/settings/:key", middleware.AdminPermissionAuth(common.PermissionSystemWrite), controller.UpdateAdminSetting)
			adminBalanceRoute.GET("/users", middleware.AdminPermissionAuth(common.PermissionUserRead), controller.GetZTAPIAdminUsers)
			adminBalanceRoute.GET("/users/:id", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionUserRead), controller.GetZTAPIAdminUser)
			adminBalanceRoute.PATCH("/users/:id/status", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionUserStatusWrite), controller.UpdateZTAPIAdminUserStatus)
			adminBalanceRoute.GET("/staff", middleware.AdminPermissionAuth(common.PermissionRoleWrite), controller.GetZTAPIAdminStaff)
			adminBalanceRoute.PATCH("/staff/:id/role", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionRoleWrite), controller.UpdateZTAPIAdminStaffRole)
			adminBalanceRoute.POST("/users/:id/balance-adjustments", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionBalanceWrite), controller.CreateBalanceAdjustment)
			adminBalanceRoute.GET("/balance-ledger", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminBalanceLedger)
			adminBalanceRoute.GET("/supplier-refunds", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetZTAPISupplierRefunds)
			adminBalanceRoute.POST("/supplier-refunds", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.SubmitZTAPISupplierRefund)
			adminBalanceRoute.POST("/supplier-refunds/:id/approve", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.ApproveZTAPISupplierRefund)
			adminBalanceRoute.GET("/request-settlements", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminZTAPIRequestSettlements)
			adminBalanceRoute.GET("/request-settlements/:id", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAdminZTAPIRequestSettlement)
			adminBalanceRoute.POST("/request-settlements/:id/confirm-no-charge", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.ResolveZTAPIPendingNoCharge)
			adminBalanceRoute.GET("/attempt-billing/reviews", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetZTAPIAttemptBillingReviews)
			adminBalanceRoute.GET("/attempt-billing/proofs", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetZTAPIAttemptBillingProofs)
			adminBalanceRoute.POST("/attempt-billing/proofs", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.SubmitZTAPIAttemptBilling)
			adminBalanceRoute.POST("/attempt-billing/proofs/:id/approve", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.ApproveZTAPIAttemptBilling)
			adminBalanceRoute.GET("/supplier-reconciliation/imports", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetZTAPISupplierReconciliationImport)
			adminBalanceRoute.POST("/supplier-reconciliation/imports", middleware.AdminPermissionAuth(common.PermissionFinanceWrite), controller.ImportZTAPISupplierReconciliation)
			adminBalanceRoute.GET("/balance-ledger/export", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.ExportAdminBalanceLedger)
			adminBalanceRoute.GET("/users/:id/balance-ledger", requireNumericUserID(), middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetUserBalanceLedger)
		}

		// Subscription billing (plans, purchase, admin management)
		subscriptionRoute := apiRouter.Group("/subscription")
		subscriptionRoute.Use(middleware.UserAuth())
		{
			subscriptionRoute.GET("/plans", controller.GetSubscriptionPlans)
			subscriptionRoute.GET("/self", controller.GetSubscriptionSelf)
			subscriptionRoute.PUT("/self/preference", controller.UpdateSubscriptionPreference)
			if legacyPaymentEnabled {
				subscriptionRoute.POST("/balance/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestBalancePay)
				subscriptionRoute.POST("/epay/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestEpay)
				subscriptionRoute.POST("/stripe/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestStripePay)
				subscriptionRoute.POST("/creem/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestCreemPay)
				subscriptionRoute.POST("/waffo-pancake/pay", middleware.CriticalRateLimit(), controller.SubscriptionRequestWaffoPancakePay)
			}
		}
		subscriptionAdminRoute := apiRouter.Group("/subscription/admin")
		subscriptionAdminRoute.Use(middleware.AdminAuth())
		{
			subscriptionAdminRoute.GET("/plans", controller.AdminListSubscriptionPlans)
			subscriptionAdminRoute.POST("/plans", controller.AdminCreateSubscriptionPlan)
			subscriptionAdminRoute.PUT("/plans/:id", controller.AdminUpdateSubscriptionPlan)
			subscriptionAdminRoute.PATCH("/plans/:id", controller.AdminUpdateSubscriptionPlanStatus)
			subscriptionAdminRoute.POST("/bind", controller.AdminBindSubscription)

			// User subscription management (admin)
			subscriptionAdminRoute.GET("/users/:id/subscriptions", controller.AdminListUserSubscriptions)
			subscriptionAdminRoute.POST("/users/:id/subscriptions", controller.AdminCreateUserSubscription)
			subscriptionAdminRoute.POST("/user_subscriptions/:id/invalidate", controller.AdminInvalidateUserSubscription)
			subscriptionAdminRoute.DELETE("/user_subscriptions/:id", controller.AdminDeleteUserSubscription)
		}

		// Subscription payment callbacks (no auth)
		if legacyPaymentEnabled {
			apiRouter.POST("/subscription/epay/notify", anonymousRequestBodyLimit, controller.SubscriptionEpayNotify)
			apiRouter.GET("/subscription/epay/notify", controller.SubscriptionEpayNotify)
			apiRouter.GET("/subscription/epay/return", controller.SubscriptionEpayReturn)
			apiRouter.POST("/subscription/epay/return", anonymousRequestBodyLimit, controller.SubscriptionEpayReturn)
		}
		optionRoute := apiRouter.Group("/option")
		optionRoute.Use(middleware.RootAuth())
		{
			optionRoute.GET("/", controller.GetOptions)
			optionRoute.PUT("/", controller.UpdateOption)
			optionRoute.POST("/payment_compliance", controller.ConfirmPaymentCompliance)
			optionRoute.GET("/channel_affinity_cache", controller.GetChannelAffinityCacheStats)
			optionRoute.DELETE("/channel_affinity_cache", controller.ClearChannelAffinityCache)
			optionRoute.POST("/rest_model_ratio", controller.ResetModelRatio)
			optionRoute.POST("/migrate_console_setting", controller.MigrateConsoleSetting) // 用于迁移检测的旧键，下个版本会删除
			optionRoute.POST("/waffo-pancake/catalog", controller.ListWaffoPancakeCatalog)
			optionRoute.POST("/waffo-pancake/pair", controller.CreateWaffoPancakePair)
			optionRoute.POST("/waffo-pancake/save", controller.SaveWaffoPancake)
			optionRoute.POST("/waffo-pancake/subscription-product", controller.CreateWaffoPancakeSubscriptionProduct)
			optionRoute.POST("/waffo-pancake/subscription-product-options", controller.ListWaffoPancakeSubscriptionProductOptions)
		}

		// Custom OAuth provider management (root only)
		customOAuthRoute := apiRouter.Group("/custom-oauth-provider")
		customOAuthRoute.Use(middleware.RootAuth())
		{
			customOAuthRoute.POST("/discovery", controller.FetchCustomOAuthDiscovery)
			customOAuthRoute.GET("/", controller.GetCustomOAuthProviders)
			customOAuthRoute.GET("/:id", controller.GetCustomOAuthProvider)
			customOAuthRoute.POST("/", controller.CreateCustomOAuthProvider)
			customOAuthRoute.PUT("/:id", controller.UpdateCustomOAuthProvider)
			customOAuthRoute.DELETE("/:id", controller.DeleteCustomOAuthProvider)
		}
		performanceRoute := apiRouter.Group("/performance")
		performanceRoute.Use(middleware.RootAuth())
		{
			performanceRoute.GET("/stats", controller.GetPerformanceStats)
			performanceRoute.DELETE("/disk_cache", controller.ClearDiskCache)
			performanceRoute.POST("/reset_stats", controller.ResetPerformanceStats)
			performanceRoute.POST("/gc", controller.ForceGC)
			performanceRoute.GET("/logs", controller.GetLogFiles)
			performanceRoute.DELETE("/logs", controller.CleanupLogFiles)
		}
		ratioSyncRoute := apiRouter.Group("/ratio_sync")
		ratioSyncRoute.Use(middleware.RootAuth())
		{
			ratioSyncRoute.GET("/channels", controller.GetSyncableChannels)
			ratioSyncRoute.POST("/fetch", controller.FetchUpstreamRatios)
		}
		channelRoute := apiRouter.Group("/channel")
		{
			channelRoute.GET("/ztapi/", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.GetZTAPIChannels)
			channelRoute.GET("/ztapi/search", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.SearchZTAPIChannels)
			channelRoute.GET("/ztapi/test/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.TestZTAPIChannel)
			channelRoute.GET("/ztapi/fetch_models/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.FetchZTAPIUpstreamModels)
			channelRoute.POST("/ztapi/", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.AddZTAPIChannel)
			channelRoute.PUT("/ztapi/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.UpdateZTAPIChannel)
			channelRoute.PATCH("/ztapi/:id/status", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.UpdateZTAPIChannelStatus)
			channelRoute.GET("/ztapi/:id", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.GetZTAPIChannel)
			channelRoute.GET("/", middleware.AdminAuth(), controller.GetAllChannels)
			channelRoute.GET("/search", middleware.AdminAuth(), controller.SearchChannels)
			channelRoute.GET("/models", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.ChannelListModels)
			channelRoute.GET("/models_enabled", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.EnabledListModels)
			channelRoute.GET("/:id", middleware.AdminAuth(), controller.GetChannel)
			channelRoute.POST("/:id/key", middleware.DisableCache(), middleware.RootAuth(), middleware.CriticalRateLimit(), middleware.SecureVerificationRequired(), controller.GetChannelKey)
			channelRoute.GET("/test", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.TestAllChannels)
			channelRoute.GET("/test/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.TestChannel)
			channelRoute.GET("/update_balance", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.UpdateAllChannelsBalance)
			channelRoute.GET("/update_balance/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.UpdateChannelBalance)
			channelRoute.POST("/", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.AddChannel)
			channelRoute.PUT("/", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.UpdateChannel)
			channelRoute.DELETE("/disabled", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DeleteDisabledChannel)
			channelRoute.POST("/tag/disabled", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DisableTagChannels)
			channelRoute.POST("/tag/enabled", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.EnableTagChannels)
			channelRoute.PUT("/tag", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.EditTagChannels)
			channelRoute.DELETE("/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DeleteChannel)
			channelRoute.POST("/batch", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DeleteChannelBatch)
			channelRoute.POST("/fix", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.FixChannelsAbilities)
			channelRoute.GET("/fetch_models/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.FetchUpstreamModels)
			channelRoute.POST("/fetch_models", middleware.RootAuth(), controller.FetchModels)
			channelRoute.POST("/:id/codex/refresh", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.RefreshCodexChannelCredential)
			channelRoute.GET("/:id/codex/usage", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.GetCodexChannelUsage)
			channelRoute.POST("/ollama/pull", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.OllamaPullModel)
			channelRoute.POST("/ollama/pull/stream", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.OllamaPullModelStream)
			channelRoute.DELETE("/ollama/delete", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.OllamaDeleteModel)
			channelRoute.GET("/ollama/version/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.OllamaVersion)
			channelRoute.POST("/batch/tag", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.BatchSetChannelTag)
			channelRoute.GET("/tag/models", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.GetTagModels)
			channelRoute.POST("/copy/:id", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.CopyChannel)
			channelRoute.POST("/multi_key/manage", middleware.AdminPermissionAuth(common.PermissionChannelRead), controller.ManageMultiKeys)
			channelRoute.POST("/upstream_updates/apply", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.ApplyChannelUpstreamModelUpdates)
			channelRoute.POST("/upstream_updates/apply_all", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.ApplyAllChannelUpstreamModelUpdates)
			channelRoute.POST("/upstream_updates/detect", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DetectChannelUpstreamModelUpdates)
			channelRoute.POST("/upstream_updates/detect_all", middleware.AdminPermissionAuth(common.PermissionChannelWrite), controller.DetectAllChannelUpstreamModelUpdates)
		}
		tokenRoute := apiRouter.Group("/token")
		tokenRoute.Use(middleware.ZTAPIUserAuth())
		{
			tokenRoute.GET("/", controller.GetAllTokens)
			tokenRoute.GET("/search", middleware.SearchRateLimit(), controller.SearchTokens)
			tokenRoute.GET("/:id", controller.GetToken)
			tokenRoute.POST("/", controller.AddToken)
			tokenRoute.PUT("/", controller.UpdateToken)
			tokenRoute.DELETE("/:id", controller.DeleteToken)
			tokenRoute.POST("/batch", controller.DeleteTokenBatch)
		}

		usageRoute := apiRouter.Group("/usage")
		usageRoute.Use(middleware.CORS(), middleware.CriticalRateLimit())
		{
			tokenUsageRoute := usageRoute.Group("/token")
			tokenUsageRoute.Use(middleware.TokenAuthReadOnly())
			{
				tokenUsageRoute.GET("/", controller.GetTokenUsage)
			}
		}

		redemptionRoute := apiRouter.Group("/redemption")
		redemptionRoute.Use(middleware.AdminAuth())
		{
			redemptionRoute.GET("/", controller.GetAllRedemptions)
			redemptionRoute.GET("/search", controller.SearchRedemptions)
			redemptionRoute.GET("/:id", controller.GetRedemption)
			redemptionRoute.POST("/", controller.AddRedemption)
			redemptionRoute.PUT("/", controller.UpdateRedemption)
			redemptionRoute.DELETE("/invalid", controller.DeleteInvalidRedemption)
			redemptionRoute.DELETE("/:id", controller.DeleteRedemption)
		}
		logRoute := apiRouter.Group("/log")
		logRoute.GET("/", middleware.AdminPermissionAuth(common.PermissionLogRead), controller.GetAllLogs)
		logRoute.DELETE("/", middleware.AdminAuth(), controller.DeleteHistoryLogs)
		logRoute.GET("/stat", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetLogsStat)
		logRoute.GET("/self/stat", middleware.ZTAPIUserAuth(), controller.GetLogsSelfStat)
		logRoute.GET("/channel_affinity_usage_cache", middleware.AdminAuth(), controller.GetChannelAffinityUsageCacheStats)
		logRoute.GET("/search", middleware.AdminPermissionAuth(common.PermissionLogRead), controller.SearchAllLogs)
		logRoute.GET("/self", middleware.ZTAPIUserAuth(), controller.GetZTAPIUserLogs)
		logRoute.GET("/self/search", middleware.ZTAPIUserAuth(), middleware.SearchRateLimit(), controller.SearchUserLogs)

		dataRoute := apiRouter.Group("/data")
		dataRoute.GET("/", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetAllQuotaDates)
		dataRoute.GET("/users", middleware.AdminPermissionAuth(common.PermissionFinanceRead), controller.GetQuotaDatesByUser)
		dataRoute.GET("/self", middleware.UserAuth(), controller.GetUserQuotaDates)

		logRoute.Use(middleware.CORS(), middleware.CriticalRateLimit())
		{
			logRoute.GET("/token", middleware.TokenAuthReadOnly(), controller.GetLogByKey)
		}
		groupRoute := apiRouter.Group("/group")
		groupRoute.Use(middleware.AdminAuth())
		{
			groupRoute.GET("/", controller.GetGroups)
		}

		prefillGroupRoute := apiRouter.Group("/prefill_group")
		prefillGroupRoute.Use(middleware.AdminAuth())
		{
			prefillGroupRoute.GET("/", controller.GetPrefillGroups)
			prefillGroupRoute.POST("/", controller.CreatePrefillGroup)
			prefillGroupRoute.PUT("/", controller.UpdatePrefillGroup)
			prefillGroupRoute.DELETE("/:id", controller.DeletePrefillGroup)
		}

		mjRoute := apiRouter.Group("/mj")
		mjRoute.GET("/self", middleware.UserAuth(), controller.GetUserMidjourney)
		mjRoute.GET("/", middleware.AdminAuth(), controller.GetAllMidjourney)

		taskRoute := apiRouter.Group("/task")
		{
			taskRoute.GET("/self", middleware.UserAuth(), controller.GetUserTask)
			taskRoute.GET("/", middleware.AdminAuth(), controller.GetAllTask)
		}

		vendorRoute := apiRouter.Group("/vendors")
		vendorRoute.Use(middleware.AdminAuth())
		{
			vendorRoute.GET("/", controller.GetAllVendors)
			vendorRoute.GET("/search", controller.SearchVendors)
			vendorRoute.GET("/:id", controller.GetVendorMeta)
			vendorRoute.POST("/", controller.CreateVendorMeta)
			vendorRoute.PUT("/", controller.UpdateVendorMeta)
			vendorRoute.DELETE("/:id", controller.DeleteVendorMeta)
		}

		modelsRoute := apiRouter.Group("/models")
		{
			modelsRoute.GET("/ztapi/audit-events", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIAuditEvents)
			modelsRoute.GET("/ztapi/pricing-preview", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIPricingPreview)
			modelsRoute.GET("/ztapi/health/status", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIHealthWorkerStatus)
			modelsRoute.POST("/ztapi/health/test-alert", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.CreateZTAPIHealthTestAlert)
			modelsRoute.GET("/ztapi/health/test-alert/:operation_id", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIHealthTestAlert)
			modelsRoute.GET("/ztapi/", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIModels)
			modelsRoute.GET("/ztapi/:id", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIModel)
			modelsRoute.GET("/ztapi/:id/health", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIModelHealth)
			modelsRoute.GET("/ztapi/:id/media-contract", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetZTAPIMediaContract)
			modelsRoute.POST("/ztapi/:id/health/recover", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.RecoverZTAPIModelHealth)
			modelsRoute.PUT("/ztapi/:id", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.UpdateZTAPIModel)
			modelsRoute.PUT("/ztapi/:id/identity", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.UpdateZTAPIModelIdentity)
			modelsRoute.POST("/ztapi/:id/price-preview", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.PreviewZTAPIModelPriceSource)
			modelsRoute.POST("/ztapi/:id/price-sources", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.ImportZTAPIModelPriceSource)
			modelsRoute.POST("/ztapi/:id/verify", middleware.AdminPermissionAuth(common.PermissionModelWrite), controller.VerifyZTAPIModel)
			modelsRoute.GET("/sync_upstream/preview", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.SyncUpstreamPreview)
			modelsRoute.POST("/sync_upstream", middleware.AdminAuth(), controller.SyncUpstreamModels)
			modelsRoute.GET("/missing", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetMissingModels)
			modelsRoute.GET("/", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetAllModelsMeta)
			modelsRoute.GET("/search", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.SearchModelsMeta)
			modelsRoute.GET("/:id", middleware.AdminPermissionAuth(common.PermissionModelRead), controller.GetModelMeta)
			modelsRoute.POST("/", middleware.AdminAuth(), controller.CreateModelMeta)
			modelsRoute.PUT("/", middleware.AdminAuth(), controller.UpdateModelMeta)
			modelsRoute.DELETE("/:id", middleware.AdminAuth(), controller.DeleteModelMeta)
		}

		// Deployments (model deployment management)
		deploymentsRoute := apiRouter.Group("/deployments")
		deploymentsRoute.Use(middleware.AdminAuth())
		{
			deploymentsRoute.GET("/settings", controller.GetModelDeploymentSettings)
			deploymentsRoute.POST("/settings/test-connection", controller.TestIoNetConnection)
			deploymentsRoute.GET("/", controller.GetAllDeployments)
			deploymentsRoute.GET("/search", controller.SearchDeployments)
			deploymentsRoute.POST("/test-connection", controller.TestIoNetConnection)
			deploymentsRoute.GET("/hardware-types", controller.GetHardwareTypes)
			deploymentsRoute.GET("/locations", controller.GetLocations)
			deploymentsRoute.GET("/available-replicas", controller.GetAvailableReplicas)
			deploymentsRoute.POST("/price-estimation", controller.GetPriceEstimation)
			deploymentsRoute.GET("/check-name", controller.CheckClusterNameAvailability)
			deploymentsRoute.POST("/", controller.CreateDeployment)

			deploymentsRoute.GET("/:id", controller.GetDeployment)
			deploymentsRoute.GET("/:id/logs", controller.GetDeploymentLogs)
			deploymentsRoute.GET("/:id/containers", controller.ListDeploymentContainers)
			deploymentsRoute.GET("/:id/containers/:container_id", controller.GetContainerDetails)
			deploymentsRoute.PUT("/:id", controller.UpdateDeployment)
			deploymentsRoute.PUT("/:id/name", controller.UpdateDeploymentName)
			deploymentsRoute.POST("/:id/extend", controller.ExtendDeployment)
			deploymentsRoute.DELETE("/:id", controller.DeleteDeployment)
		}
	}
}

func legacyPaymentRoutesEnabled() bool {
	return common.GetEnvOrDefaultBool(legacyPaymentEnabledEnv, false)
}

func requireNumericUserID() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, err := strconv.Atoi(c.Param("id")); err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}
