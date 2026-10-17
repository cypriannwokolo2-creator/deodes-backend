package api

import (
	"strings"
	"time"

	"github.com/deodes/backend/config"
	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/api/middleware"
	"github.com/deodes/backend/webhook"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// perResource is a small helper so route registration below doesn't have to
// repeat the redisClient/resource/limit/window/fail-closed boilerplate for
// every sensitive route (#197 — PerResourceRateLimitMiddleware existed but
// was never applied to any route).
func perResource(redisClient *redis.Client, resource string, limit, windowSeconds int) gin.HandlerFunc {
	return middleware.PerResourceRateLimitMiddleware(
		redisClient,
		resource,
		limit,
		time.Duration(windowSeconds)*time.Second,
		middleware.WithFailClosed(),
	)
}

// perResourceLive resolves OTP/login buckets on every request so hot-reloaded
// config applies without a restart. resolve reads cfg.Hot when present,
// otherwise the startup snapshot.
func perResourceLive(redisClient *redis.Client, cfg *config.Config, resource string, staticLimit, staticWindowSeconds int, resolve func(config.RateLimitConfig) (int, int)) gin.HandlerFunc {
	return middleware.PerResourceRateLimitMiddlewareLive(
		redisClient,
		resource,
		staticLimit,
		time.Duration(staticWindowSeconds)*time.Second,
		func() (int, time.Duration) {
			rl := cfg.RateLimit
			if cfg.Hot != nil {
				rl = cfg.Hot.RateLimit()
			}
			limit, windowSecs := resolve(rl)
			if limit <= 0 {
				limit = staticLimit
			}
			if windowSecs <= 0 {
				windowSecs = staticWindowSeconds
			}
			return limit, time.Duration(windowSecs) * time.Second
		},
		middleware.WithFailClosed(),
	)
}

// consentLimit bounds unauthenticated consent writes per IP (30/hour
// default). Zero config falls back to the default.
func consentLimit(redisClient *redis.Client, cfg *config.Config) gin.HandlerFunc {
	limit, window := cfg.RateLimit.ConsentLimit, cfg.RateLimit.ConsentWindowSeconds
	if cfg.Hot != nil {
		rl := cfg.Hot.RateLimit()
		limit, window = rl.ConsentLimit, rl.ConsentWindowSeconds
	}
	if limit <= 0 {
		limit = 30
	}
	if window <= 0 {
		window = 3600
	}
	return perResource(redisClient, "consent", limit, window)
}

// otpSendLimit / otpVerifyLimit / loginLimit resolve the split buckets with
// fallback to the legacy shared OTP bucket for old configs.
func otpSendLimit(rl config.RateLimitConfig) (int, int)   { return rl.OTPSend() }
func otpVerifyLimit(rl config.RateLimitConfig) (int, int) { return rl.OTPVerify() }
func loginLimit(rl config.RateLimitConfig) (int, int)     { return rl.Login() }

// claimLimit bounds anonymous name allocation per IP (20/hour default).
// Zero config falls back to the default so bare test configs stay open.
func claimLimit(redisClient *redis.Client, cfg *config.Config) gin.HandlerFunc {
	limit, window := cfg.RateLimit.ClaimLimit, cfg.RateLimit.ClaimWindowSeconds
	if cfg.Hot != nil {
		rl := cfg.Hot.RateLimit()
		limit, window = rl.ClaimLimit, rl.ClaimWindowSeconds
	}
	if limit <= 0 {
		limit = 20
	}
	if window <= 0 {
		window = 3600
	}
	return perResource(redisClient, "claim-name", limit, window)
}

// passwordResetLimit wires the dual per-IP + per-account reset limiter with
// live config and startup fallbacks (middleware defaults 5/3/15m when unset).
func passwordResetLimit(redisClient *redis.Client, cfg *config.Config) gin.HandlerFunc {
	rl := cfg.RateLimit
	if cfg.Hot != nil {
		rl = cfg.Hot.RateLimit()
	}
	return middleware.PasswordResetRateLimitMiddleware(
		redisClient, rl.PasswordResetIPLimit, rl.PasswordResetAccountLimit, rl.PasswordResetWindowSeconds)
}

// liveLimitOptions wires the hot-reloadable rate limits into a middleware when
// the config carries a reloader.
func liveLimitOptions(cfg *config.Config) []middleware.RateLimitOption {
	if cfg.Hot == nil {
		return nil
	}
	return []middleware.RateLimitOption{middleware.WithLiveLimits(cfg.Hot.RateLimit)}
}

func NewRouter(
	cfg *config.Config,
	redisClient *redis.Client,
	authHandler *handler.AuthHandler,
	userHandler *handler.UserHandler,
	circleHandler *handler.CircleHandler,
	contributionHandler *handler.ContributionHandler,
	payoutHandler *handler.PayoutHandler,
	inviteHandler *handler.InviteHandler,
	notificationHandler *handler.NotificationHandler,
	adminHandler *handler.AdminHandler,
	webhookHandler *handler.WebhookHandler,
	healthHandler *handler.HealthHandler,
	passkeyCredentialHandler *handler.PasskeyCredentialHandler,
	walletHandler *handler.WalletHandler,
	mobileMoneyHandler *handler.MobileMoneyHandler,
	chatHandler *handler.ChatHandler,
	communityHandler *handler.CommunityHandler,
	wsHandler *handler.WebSocketHandler,
	savingsGoalHandler *handler.SavingsGoalHandler,
	bankAccountHandler *handler.BankAccountHandler,
	swapHandler *handler.SwapHandler,
	governanceHandler *handler.GovernanceHandler,
	reputationHandler *handler.ReputationHandler,
	referralHandler *handler.ReferralHandler,
	consentHandler *handler.ConsentHandler,
	subscriptionHandler *handler.SubscriptionHandler,
	adminJobQueueHandler *handler.AdminJobQueueHandler,
	supportHandler *handler.SupportHandler,
	webhookRepo webhook.WebhookRepository,
	jwtPublicKey []byte,
) *gin.Engine {
	r := gin.New()

	// Trusted proxies for c.ClientIP(): without this every client behind the
	// LB/CDN collapses to the proxy IP and shares one OTP bucket (always-429
	// in prod). Configure server.trusted_proxies / DEODES_TRUSTED_PROXIES with
	// the LB/CDN CIDRs. Empty = gin default (loopback only, safe for dev).
	if len(cfg.Server.TrustedProxies) > 0 {
		if err := r.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
			panic("invalid server.trusted_proxies: " + err.Error())
		}
	}

	// CSRF boundary (#399). CSRF tokens are bound to the bearer session, so they
	// are enforced wherever a request is authenticated:
	//   - wsRoute, authenticated (and admin, nested in it): always enforced.
	//   - optional: enforced when an Authorization header is present
	//     (CSRFTokenValidatorIfAuthenticated); anonymous callers have no session.
	// Exempt by design (no user session to protect / not browser-initiated):
	//   - /webhooks/incoming/:id: server-to-server, verified by the handler.
	//   - /v1/auth/{register,register/verify,refresh,nonce,verify}: they create
	//     or exchange the session, so no CSRF token can exist yet.
	//   - /v1/claim-name: anonymous, not user-bound (allocates the next name).
	// TestRouter_CSRFCoverage enforces this: any new state-changing route must be
	// CSRF-enforced or explicitly added to its exemption list.

	r.Use(middleware.RecoveryMiddleware())
	r.Use(middleware.TracingMiddleware(cfg.Tracing.ServiceName))
	r.Use(middleware.LoggingMiddleware())
	r.Use(middleware.CORSMiddleware(cfg.CORS))
	r.Use(middleware.PrometheusMiddleware())

	// Request body cap (#445). Mounted after CORS so a 413 still carries the
	// CORS headers browsers need, and after Prometheus so rejected requests
	// are still counted. It runs ahead of auth and rate limiting, so an
	// oversized payload is discarded before any expensive work happens.
	r.Use(middleware.BodyLimit(cfg.Server.MaxBodyBytes, cfg.Server.MaxBodyBytesRoutes))

	// Prometheus metrics endpoint — protected by admin API key (primary + secondary for zero-downtime rotation), un-rate-limited
	r.GET("/metrics", middleware.AdminAPIKeyMiddleware(cfg.Auth.AdminAPIKey, cfg.Auth.AdminAPIKeySecondary), gin.WrapH(promhttp.Handler()))

	r.Use(middleware.RateLimitMiddleware(redisClient, cfg.RateLimit, liveLimitOptions(cfg)...))

	r.GET("/health", healthHandler.Health)
	r.GET("/health/ready", healthHandler.Readiness)
	r.GET("/health/live", healthHandler.Liveness)
	r.GET("/internal/indexer/lag", healthHandler.IndexerLag)

	swaggerH := handler.NewSwaggerHandler()
	r.GET("/api-docs", swaggerH.ServeUI)
	r.GET("/api-docs/openapi.json", swaggerH.ServeJSON)

	// Public webhooks (idempotency-keyed internally)
	r.POST("/webhooks/incoming/:id", handler.NewIncomingWebhookHandler(webhookRepo).ReceiveWebhook)

	// WebSocket — real-time events
	wsRoute := r.Group("")
	wsRoute.Use(middleware.AuthMiddleware(jwtPublicKey))
	wsRoute.Use(middleware.TokenBlocklistMiddleware(redisClient))
	wsRoute.Use(middleware.CSRFTokenValidator(redisClient))
	{
		wsRoute.GET("/ws", wsHandler.HandleWebSocket)
	}

	api := r.Group("/v1")
	{
		auth := api.Group("/auth")
		auth.Use(middleware.AuthRateLimitMiddleware(redisClient, cfg.RateLimit, liveLimitOptions(cfg)...))
		{
			// Split OTP buckets: send and verify no longer share one 5/15m
			// bucket. Send stays strict (spam), verify is roomier (typos),
			// login has its own bucket (never sent OTP but shared it).
			sendLimit, sendWindow := cfg.RateLimit.OTPSend()
			verifyLimit, verifyWindow := cfg.RateLimit.OTPVerify()
			loginLimitVal, loginWindow := cfg.RateLimit.Login()
			otpSend := perResourceLive(redisClient, cfg, "otp-send", sendLimit, sendWindow, otpSendLimit)
			otpVerify := perResourceLive(redisClient, cfg, "otp-verify", verifyLimit, verifyWindow, otpVerifyLimit)
			loginRL := perResourceLive(redisClient, cfg, "login", loginLimitVal, loginWindow, loginLimit)
			auth.POST("/register", otpSend, authHandler.Register)
			auth.POST("/register/verify", otpVerify, authHandler.RegisterVerify)
			auth.POST("/login", loginRL, authHandler.Login)
			auth.POST("/refresh", middleware.RefreshTokenBlocklistMiddleware(redisClient), authHandler.Refresh)
			// Wallet challenge/response creates sessions like login: same
			// long-window bucket on top of the coarse group limiter.
			auth.POST("/nonce", loginRL, authHandler.Nonce)
			auth.POST("/verify", loginRL, authHandler.Verify)
			auth.POST("/google", loginRL, authHandler.Google)
			auth.POST("/password/forgot", otpSend, passwordResetLimit(redisClient, cfg), authHandler.ForgotPassword)
			auth.POST("/password/reset", otpVerify, passwordResetLimit(redisClient, cfg), authHandler.ResetPassword)
		}

		// User & Profile routes (authenticated)
		// Public — claim a unique anonymous name (before auth). Registered on the
		// bare api group (no auth, so no CSRF session); see the CSRF boundary
		// note above. Kept out of the authenticated block below so it can't be
		// mistaken for an authenticated route.
		api.POST("/claim-name", claimLimit(redisClient, cfg), userHandler.ClaimName)

		// Public invite preview for the /invite/:code landing page: signed-out
		// recipients must review before registering. Rate-limited per IP since
		// it is unauthenticated.
		invitePreviewLimit := cfg.RateLimit.InvitePreviewLimit
		invitePreviewWindow := cfg.RateLimit.InvitePreviewWindowSeconds
		if invitePreviewLimit <= 0 {
			invitePreviewLimit = 30
		}
		if invitePreviewWindow <= 0 {
			invitePreviewWindow = 60
		}
		api.GET("/circles/invites/:code", perResource(redisClient, "invite-preview", invitePreviewLimit, invitePreviewWindow), inviteHandler.Preview)

		// Public client configuration (Phase 2.3) — pre-login feature flags
		// (google_enabled, arc addresses, fiat provider). GET is CSRF-safe by
		// method; the payload carries no secrets (see ConfigHandler).
		configHandler := handler.NewConfigHandler(handler.PublicConfig{
			GoogleClientID:     cfg.Google.ClientID,
			ArcChainID:         cfg.Arc.ChainID,
			ArcCircleAddress:   cfg.Arc.CircleAddress,
			ArcTreasuryAddress: cfg.Arc.TreasuryAddress,
			ArcUSDCContract:    cfg.Arc.USDCContract,
			EmailConfigured:    strings.TrimSpace(cfg.Brevo.APIKey) != "",
		})
		api.GET("/config", configHandler.PublicConfig)

		authenticated := api.Group("")
		authenticated.Use(middleware.AuthMiddleware(jwtPublicKey))
		authenticated.Use(middleware.TokenBlocklistMiddleware(redisClient))
		authenticated.Use(middleware.CSRFTokenValidator(redisClient))
		// Idempotency must run after AuthMiddleware so keys are scoped per
		// user (#198) — a global, pre-auth middleware let idempotency keys
		// collide across different users' requests.
		authenticated.Use(middleware.IdempotencyMiddleware(redisClient))
		// Idempotency audit (#393). Every POST/PUT/PATCH/DELETE registered on
		// `authenticated` (and its `admin` sub-group) runs IdempotencyMiddleware
		// above. That middleware is opt-in per request, so routes that move
		// funds or cast a binding vote/bid additionally mount requireIdem,
		// making the Idempotency-Key header mandatory there:
		//   wallets/withdraw,
		//   wallet/mobile-money/{onramp,offramp}, circles/:id/{contribute,
		//   payout,vote,auction-bid}, circles/:id/invites,
		//   governance/proposals/:id/{vote,execute}, token/{stake,unstake},
		//   swap/{offer,accept,cancel}.
		// Intentionally NOT idempotency-wrapped: /v1/auth/* (pre-auth, keys
		// would be unscoped — see #198), public webhooks (idempotent by
		// provider event ID internally), POST /v1/claim-name (public,
		// pre-auth) and optional-auth POST /v1/consent.
		requireIdem := middleware.RequireIdempotencyKey()
		{
			authenticated.GET("/me", authHandler.Me)
			authenticated.PATCH("/me", userHandler.UpdateProfile)
			authenticated.PATCH("/users/me", userHandler.UpdateProfile)
			authenticated.DELETE("/users/me", userHandler.DeleteMe)
			authenticated.GET("/users/lookup", userHandler.LookupByEmail)
			authenticated.GET("/users/me/subscriptions", subscriptionHandler.ListMine)
			authenticated.POST("/auth/logout", authHandler.Logout)
			authenticated.GET("/sessions", authHandler.ListSessions)
			authenticated.POST("/auth/password/change", authHandler.ChangePassword)
			authenticated.DELETE("/sessions/:id", authHandler.RevokeSessionByID)
			emailChangeLimit := perResource(redisClient, "email-change", cfg.RateLimit.OTPLimit, cfg.RateLimit.OTPWindowSeconds)
			authenticated.POST("/auth/email/change", emailChangeLimit, authHandler.RequestEmailChange)
			authenticated.POST("/auth/email/change/verify-current", emailChangeLimit, authHandler.VerifyCurrentEmail)
			authenticated.POST("/auth/email/change/verify-new", emailChangeLimit, authHandler.VerifyNewEmail)

			authenticated.POST("/users/username/claim", userHandler.ClaimName)

			// Passkey credential store/retrieval
			authenticated.POST("/credential", passkeyCredentialHandler.StoreCredential)
			authenticated.GET("/credential", passkeyCredentialHandler.GetCredential)

			// Wallet routes
			authenticated.POST("/wallets", walletHandler.CreateWallet)
			authenticated.POST("/wallets/hosted-arc/ensure", walletHandler.EnsureHostedArcWallet)
			authenticated.POST("/wallets/hosted-arc", requireIdem, walletHandler.CreateHostedArcWallet)
			authenticated.POST("/wallets/link", perResource(redisClient, "wallet-link", cfg.RateLimit.OTPLimit, cfg.RateLimit.OTPWindowSeconds), walletHandler.LinkWallet)
			authenticated.GET("/wallets", walletHandler.ListWallets)
			authenticated.GET("/wallets/balance", walletHandler.GetBalance)
			authenticated.POST("/wallets/withdraw", requireIdem, perResource(redisClient, "wallet-transfer", cfg.RateLimit.WalletTransferLimit, cfg.RateLimit.WalletTransferWindowSeconds), walletHandler.Withdraw)
			authenticated.DELETE("/wallets/:id", walletHandler.DeleteWallet)

			// Saved fiat payout rails (payment settings screen).
			authenticated.GET("/bank-accounts", bankAccountHandler.List)
			authenticated.POST("/bank-accounts", bankAccountHandler.Create)
			authenticated.POST("/bank-accounts/:id/default", bankAccountHandler.SetDefault)
			authenticated.DELETE("/bank-accounts/:id", bankAccountHandler.Delete)

			// Member-to-member USDC sends (transfer screen: internal email /
			// address, external address). Idempotency-keyed like withdrawals.
			authenticated.GET("/wallets/allowance", walletHandler.Allowance)
			authenticated.POST("/wallets/transfer", requireIdem, perResource(redisClient, "wallet-transfer", cfg.RateLimit.WalletTransferLimit, cfg.RateLimit.WalletTransferWindowSeconds), walletHandler.Transfer)
		// On-chain outcome reported by the sender's client once the receipt
		// settles; fans out transfer.confirmed/failed/received in-app
		// notifications (idempotent per tx hash).
		authenticated.POST("/wallets/transfers/:txHash/outcome", perResource(redisClient, "transfer-outcome", cfg.RateLimit.OTPLimit, cfg.RateLimit.OTPWindowSeconds), walletHandler.ReportTransferOutcome)

			// Authenticator (TOTP) enrollment. Code checks share the OTP
			// limiter so six-digit codes cannot be brute-forced.
			totpLimit := perResource(redisClient, "totp", cfg.RateLimit.OTPLimit, cfg.RateLimit.OTPWindowSeconds)
			authenticated.POST("/auth/totp/enroll", authHandler.EnrollTOTP)
			authenticated.POST("/auth/totp/enable", totpLimit, authHandler.EnableTOTP)
			authenticated.POST("/auth/totp/verify", totpLimit, authHandler.VerifyTOTP)
			authenticated.POST("/auth/totp/disable", totpLimit, authHandler.DisableTOTP)
			authenticated.POST("/auth/totp/recovery-codes", totpLimit, authHandler.RegenerateTOTPRecoveryCodes)

			// Fiat ramp routes removed — local-currency deposits are coming
			// soon. Mobile-money stubs below stay dormant until configured.
			authenticated.GET("/wallet/mobile-money/providers", mobileMoneyHandler.ListProviders)
			authenticated.POST("/wallet/mobile-money/onramp", requireIdem, perResource(redisClient, "wallet-transfer", cfg.RateLimit.WalletTransferLimit, cfg.RateLimit.WalletTransferWindowSeconds), mobileMoneyHandler.InitiateOnramp)
			authenticated.POST("/wallet/mobile-money/offramp", requireIdem, perResource(redisClient, "wallet-transfer", cfg.RateLimit.WalletTransferLimit, cfg.RateLimit.WalletTransferWindowSeconds), mobileMoneyHandler.InitiateOfframp)
			authenticated.GET("/wallet/mobile-money/:id", mobileMoneyHandler.GetTransaction)

			authenticated.POST("/chat/keys", chatHandler.PublishKeys)
			authenticated.GET("/chat/keys/:userId", chatHandler.GetBundle)
			authenticated.POST("/chat/conversations", chatHandler.CreateConversation)
			authenticated.GET("/chat/conversations", chatHandler.ListConversations)
			authenticated.POST("/chat/conversations/:id/messages", chatHandler.SendMessage)
			authenticated.GET("/chat/conversations/:id/messages", chatHandler.ListMessages)

			// Circles
			authenticated.POST("/circles", circleHandler.CreateCircle)
			authenticated.GET("/circles/:id", circleHandler.GetCircle)
			authenticated.GET("/circles/:id/export", circleHandler.ExportCircle)
			authenticated.PATCH("/circles/:id", circleHandler.UpdateCircle)
			authenticated.POST("/circles/:id/start", circleHandler.StartCircle)
			authenticated.POST("/circles/:id/payout", requireIdem, circleHandler.TriggerPayout)
			authenticated.POST("/circles/:id/payout/preview", circleHandler.PreviewPayout)
			authenticated.POST("/circles/:id/close", circleHandler.CloseCircle)
			authenticated.DELETE("/circles/:id", circleHandler.CancelCircle)
			authenticated.POST("/circles/:id/join", circleHandler.JoinCircle)
			authenticated.POST("/circles/:id/contribute", requireIdem, perResource(redisClient, "contribute", cfg.RateLimit.ContributeLimit, cfg.RateLimit.ContributeWindowSeconds), circleHandler.Contribute)
			authenticated.POST("/circles/:id/exit", circleHandler.ExitCircle)
			authenticated.GET("/circles/:id/members", circleHandler.GetMembers)
			authenticated.GET("/circles/:id/rounds", circleHandler.GetRounds)
			authenticated.GET("/circles/:id/rounds/:round/config", circleHandler.GetRoundConfig)
			authenticated.GET("/circles/:id/payouts", circleHandler.GetPayouts)
			authenticated.POST("/circles/:id/dispute", circleHandler.Dispute)
			authenticated.POST("/circles/:id/vote", requireIdem, circleHandler.Vote)
			authenticated.POST("/circles/:id/auction-bid", requireIdem, circleHandler.AuctionBid)
			authenticated.POST("/circles/:id/members/:address/remove", circleHandler.RemoveMember)

			// Dashboard snapshot endpoints (replaces multi-call pattern — see #440)
			authenticated.GET("/circles/snapshots", circleHandler.GetBulkSnapshots)
			authenticated.GET("/circles/:id/snapshot", circleHandler.GetSnapshot)

			authenticated.GET("/circles/:id/invites", inviteHandler.ListInvites)
			authenticated.POST("/circles/:id/invites", requireIdem, inviteHandler.CreateInvite)
			authenticated.DELETE("/invites/:code", inviteHandler.RevokeInvite)

			// Auto-debit subscriptions (Auto circles only; Flexible stays manual).
			authenticated.POST("/circles/:id/subscription", requireIdem, subscriptionHandler.Subscribe)
			authenticated.DELETE("/circles/:id/subscription", subscriptionHandler.Cancel)
			authenticated.POST("/circles/:id/subscription/pause", subscriptionHandler.Pause)
			authenticated.POST("/circles/:id/subscription/resume", subscriptionHandler.Resume)

			authenticated.GET("/contributions", contributionHandler.ListContributions)
			authenticated.GET("/contributions/:id", contributionHandler.GetContribution)

			authenticated.GET("/payouts", payoutHandler.ListPayouts)
			authenticated.GET("/payouts/:id", payoutHandler.GetPayout)

			// Governance
			authenticated.POST("/governance/proposals", governanceHandler.CreateProposal)
			authenticated.GET("/governance/proposals", governanceHandler.ListProposals)
			authenticated.GET("/governance/proposals/:id", governanceHandler.GetProposal)
			authenticated.POST("/governance/proposals/:id/vote", requireIdem, governanceHandler.VoteProposal)
			authenticated.POST("/governance/proposals/:id/execute", requireIdem, governanceHandler.ExecuteProposal)
			// Cancellation inside the execution timelock (#414).
			authenticated.POST("/governance/proposals/:id/cancel", requireIdem, governanceHandler.CancelProposal)

			// Reputation tiers
			authenticated.GET("/reputation/tiers", reputationHandler.GetTiers)
			authenticated.GET("/reputation/tier/:address", reputationHandler.GetTierByAddress)
			authenticated.GET("/reputation/leaderboard", reputationHandler.Leaderboard)

			// Referral system
			authenticated.POST("/referral/code", perResource(redisClient, "referral", cfg.RateLimit.ReferralLimit, cfg.RateLimit.ReferralWindowSeconds), referralHandler.GenerateCode)
			authenticated.POST("/referral/apply", referralHandler.ApplyCode)
			authenticated.GET("/referral/stats", referralHandler.GetStats)
			authenticated.GET("/referral/history", referralHandler.GetHistory)

			// Communities
			authenticated.POST("/communities", communityHandler.Create)
			authenticated.GET("/communities", communityHandler.List)
			authenticated.GET("/communities/:id", communityHandler.Get)
			authenticated.GET("/communities/slug/:slug", communityHandler.GetBySlug)
			authenticated.PATCH("/communities/:id", communityHandler.Update)
			authenticated.DELETE("/communities/:id", communityHandler.Delete)
			authenticated.POST("/communities/:id/join", communityHandler.Join)
			authenticated.POST("/communities/:id/leave", communityHandler.Leave)
			authenticated.GET("/communities/:id/members", communityHandler.GetMembers)
			authenticated.GET("/communities/:id/membership", communityHandler.IsMember)
			authenticated.POST("/communities/:id/announcements", communityHandler.CreateAnnouncement)
			authenticated.GET("/communities/:id/announcements", communityHandler.GetAnnouncements)
			authenticated.DELETE("/communities/:id/announcements/:announcementId", communityHandler.DeleteAnnouncement)
			authenticated.POST("/communities/:id/announcements/:announcementId/like", communityHandler.LikeAnnouncement)
			authenticated.PATCH("/communities/:id/announcements/:announcementId/pin", communityHandler.PinAnnouncement)
			authenticated.DELETE("/communities/:id/members/:memberId", communityHandler.RemoveMember)
			authenticated.POST("/communities/:id/transfer-ownership", communityHandler.TransferOwnership)
			authenticated.GET("/communities/:id/activity", communityHandler.GetActivity)
			authenticated.GET("/users/me/communities", communityHandler.GetMyCommunities)

			authenticated.GET("/notifications", notificationHandler.ListNotifications)
			authenticated.GET("/notifications/search", notificationHandler.SearchNotifications)
			authenticated.PATCH("/notifications/:id/read", notificationHandler.MarkRead)
			authenticated.PATCH("/notifications/read-all", notificationHandler.MarkAllRead)
			authenticated.POST("/notifications/bulk-archive", notificationHandler.BulkArchive)
			authenticated.POST("/notifications/bulk-unarchive", notificationHandler.BulkUnarchive)
			authenticated.PUT("/notifications/preferences", notificationHandler.UpdatePreferences)

			// Savings goals
			authenticated.POST("/savings/goals", savingsGoalHandler.Create)
			authenticated.GET("/savings/goals", savingsGoalHandler.List)
			authenticated.GET("/savings/goals/active", savingsGoalHandler.ListActive)
			authenticated.GET("/savings/goals/summary", savingsGoalHandler.Summary)
			authenticated.GET("/savings/goals/obligations", savingsGoalHandler.UpcomingObligations)
			authenticated.GET("/savings/goals/:id", savingsGoalHandler.Get)
			authenticated.PATCH("/savings/goals/:id", savingsGoalHandler.Update)
			authenticated.DELETE("/savings/goals/:id", savingsGoalHandler.Delete)
			authenticated.POST("/savings/goals/:id/complete", savingsGoalHandler.Complete)

			// Swap endpoints
			authenticated.POST("/swap/offer", requireIdem, perResource(redisClient, "swap", cfg.RateLimit.SwapLimit, cfg.RateLimit.SwapWindowSeconds), swapHandler.CreateSwapOffer)
			authenticated.POST("/swap/accept", requireIdem, perResource(redisClient, "swap", cfg.RateLimit.SwapLimit, cfg.RateLimit.SwapWindowSeconds), swapHandler.AcceptSwapOffer)
			authenticated.POST("/swap/cancel", requireIdem, perResource(redisClient, "swap", cfg.RateLimit.SwapLimit, cfg.RateLimit.SwapWindowSeconds), swapHandler.CancelSwapOffer)
			authenticated.GET("/swap/history", swapHandler.GetSwapHistory)

			authenticated.POST("/webhooks", webhookHandler.RegisterWebhook)
			authenticated.GET("/webhooks", webhookHandler.ListWebhooks)
			authenticated.GET("/webhooks/deliveries", webhookHandler.ListDeliveries)
			authenticated.DELETE("/webhooks/:id", webhookHandler.DeleteWebhook)
		}

		admin := authenticated.Group("/admin")
		admin.Use(middleware.AdminMiddleware())
		admin.Use(perResource(redisClient, "admin", cfg.RateLimit.AdminLimit, cfg.RateLimit.AdminWindowSeconds))
		{
			admin.GET("/users", adminHandler.ListUsers)
			// Registered before any /users/:id route so the static "deleted" segment
			// is not captured by a wildcard parameter.
			admin.GET("/users/deleted", adminHandler.ListDeletedUsers)
			admin.POST("/users/:id/restore", adminHandler.RestoreUser)
			admin.GET("/users/:id", adminHandler.GetUserDetail)
			admin.POST("/users/:id/freeze", adminHandler.FreezeUserFunds)
			admin.POST("/users/:id/unfreeze", adminHandler.UnfreezeUserFunds)
			admin.GET("/frozen-wallets", adminHandler.ListFrozenWallets)
			admin.GET("/circles", adminHandler.ListCircles)
			admin.GET("/circles/:id/inspect", adminHandler.InspectCircleState)
			admin.POST("/circles/:id/pause", adminHandler.PauseCircle)
			admin.POST("/circles/:id/resume", adminHandler.ResumeCircle)
			admin.POST("/circles/:id/ban", adminHandler.BanCircle)
			admin.GET("/communities", adminHandler.ListCommunities)
			admin.POST("/communities/:id/suspend", adminHandler.SuspendCommunity)
			admin.POST("/communities/:id/unsuspend", adminHandler.UnsuspendCommunity)
			admin.POST("/communities/:id/ban", adminHandler.BanCommunity)
			admin.GET("/support-tickets", supportHandler.ListTickets)
			admin.PATCH("/support-tickets/:id", supportHandler.UpdateTicket)
			admin.GET("/audit-log", adminHandler.GetAuditLog)
			admin.GET("/metrics", adminHandler.GetMetrics)
			admin.GET("/feature-flags", adminHandler.ListFeatureFlags)
			admin.GET("/feature-flags/:flag", adminHandler.GetFeatureFlag)
			admin.POST("/feature-flags", adminHandler.UpdateFeatureFlag)
			admin.DELETE("/feature-flags/:flag", adminHandler.DeleteFeatureFlag)
			admin.GET("/jobs/dead-letter", adminJobQueueHandler.GetDeadLetterJobs)
			admin.POST("/jobs/dead-letter/:id/retry", adminJobQueueHandler.RetryDeadLetterJob)
		}

		optional := api.Group("")
		optional.Use(middleware.OptionalAuthMiddleware(jwtPublicKey))
		// Authenticated callers on optional-auth routes (e.g. POST /consent
		// writing a signed-in user's consent) must pass CSRF too (#399).
		optional.Use(middleware.CSRFTokenValidatorIfAuthenticated(redisClient))
		{
			optional.GET("/circles", circleHandler.ListCircles)

			// GDPR cookie consent — works for both authenticated and anonymous users
			optional.GET("/consent", consentHandler.GetConsent)
			optional.POST("/consent", consentLimit(redisClient, cfg), consentHandler.SaveConsent)

			// Contact-form inbox — anyone can open a ticket; throttled against spam.
			optional.POST("/support", perResource(redisClient, "support", cfg.RateLimit.OTPLimit, cfg.RateLimit.OTPWindowSeconds), supportHandler.CreateTicket)
		}
	}

	return r
}
