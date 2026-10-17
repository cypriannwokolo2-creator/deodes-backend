// @title Deodes API
// @version 1.0.0
// @description Decentralized savings circles on Arc. REST API for circles, contributions, payouts, reputation, and governance.
// @termsOfService https://deodes.com/terms
// @contact.name Deodes Support
// @contact.email support@deodes.com
// @contact.url https://deodes.com/support
// @license.name MIT
// @license.url https://opensource.org/licenses/MIT
// @host deodes.com
// @BasePath /v1
// @schemes https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description JWT token obtained from /auth/verify or /auth/register. Format: "Bearer <token>"
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deodes/backend/config"
	"github.com/deodes/backend/internal/api"
	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/admin"
	"github.com/deodes/backend/internal/domain/audit"
	"github.com/deodes/backend/internal/domain/auth"
	"github.com/deodes/backend/internal/domain/auth/session"
	"github.com/deodes/backend/internal/domain/bankaccount"
	"github.com/deodes/backend/internal/domain/chat"
	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/community"
	"github.com/deodes/backend/internal/domain/contribution"
	"github.com/deodes/backend/internal/domain/email"
	"github.com/deodes/backend/internal/domain/featureflag"
	"github.com/deodes/backend/internal/domain/governance"
	"github.com/deodes/backend/internal/domain/incentives"
	"github.com/deodes/backend/internal/domain/invite"
	"github.com/deodes/backend/internal/domain/mobilemoney"
	"github.com/deodes/backend/internal/domain/notification"
	"github.com/deodes/backend/internal/domain/payout"
	"github.com/deodes/backend/internal/domain/push"
	"github.com/deodes/backend/internal/domain/reputation"
	"github.com/deodes/backend/internal/domain/savings"
	"github.com/deodes/backend/internal/domain/sms"
	"github.com/deodes/backend/internal/domain/subscription"
	"github.com/deodes/backend/internal/domain/support"
	"github.com/deodes/backend/internal/domain/swap"
	"github.com/deodes/backend/internal/domain/totp"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/verification"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/internal/indexer"
	ws "github.com/deodes/backend/internal/websocket"
	"github.com/deodes/backend/pkg/arc"
	"github.com/deodes/backend/pkg/jobqueue"
	"github.com/deodes/backend/pkg/logger"
	"github.com/deodes/backend/pkg/postgres"
	"github.com/deodes/backend/pkg/rabbitmq"
	"github.com/deodes/backend/pkg/redis"
	"github.com/deodes/backend/pkg/tracing"
	"github.com/deodes/backend/pkg/validator"
	"github.com/deodes/backend/webhook"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type moiAdapter struct {
	repo user.Repository
}

func (a *moiAdapter) FindByID(ctx context.Context, id uuid.UUID) (*circle.UserMOIData, error) {
	u, err := a.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return &circle.UserMOIData{MoiScore: u.MoiScore}, nil
}

// walletBalanceAdapter feeds the circle funds gate from the wallet service
// through the narrow BalanceChecker interface (no domain import cycle).
type walletBalanceAdapter struct {
	svc wallet.Service
}

func (a *walletBalanceAdapter) GetBalance(ctx context.Context, userID string) (float64, error) {
	bal, err := a.svc.GetBalance(ctx, userID)
	if err != nil {
		return 0, err
	}
	return bal.USDC, nil
}

// payoutWalletAdapter satisfies payout.WalletLookup by resolving a user's
// on-chain wallet address from the user repository.
type payoutWalletAdapter struct {
	repo user.Repository
}

func (a *payoutWalletAdapter) WalletAddressForUser(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := a.repo.FindByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.WalletAddress, nil
}

type communityAdapter struct {
	repo community.Repository
}

func (a *communityAdapter) IsMember(ctx context.Context, communityID, userID uuid.UUID) (bool, error) {
	return a.repo.IsMember(ctx, communityID, userID)
}

// streakAdapter feeds DeoScore streak input from the incentives service. A
// user with no streak row yet reads as zero, not an error.
type streakAdapter struct {
	svc incentives.Service
}

func (a *streakAdapter) CurrentStreak(ctx context.Context, userID string) (int, error) {
	streak, err := a.svc.GetStreak(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if streak == nil {
		return 0, nil
	}
	return streak.CurrentStreak, nil
}

// completionsAdapter feeds DeoScore completion input by counting the
// user's completed circles.
type completionsAdapter struct {
	svc circle.Service
}

func (a *completionsAdapter) CompletedCircles(ctx context.Context, userID string) (int, error) {
	circles, err := a.svc.GetUserCircles(ctx, userID)
	if err != nil {
		return 0, err
	}
	completed := 0
	for i := range circles {
		if circles[i].Status == circle.CircleStatusCompleted {
			completed++
		}
	}
	return completed, nil
}

// userLookupAdapter resolves a notification.Recipient from user.Repository —
// #191's delivery channels need a user's contact details and preferences,
// without the notification package importing the full user domain.
type userLookupAdapter struct {
	repo user.Repository
}

func (a *userLookupAdapter) FindRecipient(ctx context.Context, userID string) (notification.Recipient, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return notification.Recipient{}, err
	}
	u, err := a.repo.FindByID(ctx, id)
	if err != nil {
		return notification.Recipient{}, err
	}
	return notification.Recipient{
		Email:             u.Email,
		Phone:             u.Phone,
		PushToken:         u.PushToken,
		PreferredChannels: []string(u.NotificationChannels),
		Muted:             u.NotificationsMuted,
	}, nil
}

// digestPreferenceAdapter resolves a notification.DigestPreferences from
// user.Repository — #415's digest cadence, without the notification package
// importing the user domain. Same adapter pattern as userLookupAdapter above.
type digestPreferenceAdapter struct {
	repo user.Repository
}

func (a *digestPreferenceAdapter) DigestPreferences(ctx context.Context, userID string) (notification.DigestPreferences, error) {
	id, err := uuid.Parse(userID)
	if err != nil {
		return notification.DigestPreferences{}, err
	}
	u, err := a.repo.FindByID(ctx, id)
	if err != nil {
		return notification.DigestPreferences{}, err
	}
	return notification.DigestPreferences{
		Enabled:  u.DigestEnabled,
		Interval: time.Duration(u.DigestIntervalMinutes) * time.Minute,
	}, nil
}

// governanceWeightResolver computes a voter's governance weight for the
// creation-time snapshot (#418): reputation tier factor over a baseline of 1.
// (Token-balance scaling returns with the Arc token in Phase 2.11; until
// then every voter counts at their reputation tier, never zero.)
//
// It lives here rather than in the governance package so governance does not
// import the reputation and user domains — the same adapter pattern used
// by userLookupAdapter and digestPreferenceAdapter above.
type governanceWeightResolver struct {
	users      user.Repository
	reputation reputation.Repository
}

func (r *governanceWeightResolver) VotingWeight(ctx context.Context, userID string) (int64, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return 0, err
	}
	if _, err := r.users.FindByID(ctx, uid); err != nil {
		return 0, err
	}

	// A missing reputation snapshot is not an error: the user simply counts at
	// the default tier factor rather than being disenfranchised.
	tier := ""
	if snapshot, err := r.reputation.GetByUser(ctx, uid); err == nil && snapshot != nil {
		tier = snapshot.Level
	}

	return governance.ApplyTierFactor(1, tier), nil
}

// EligibleVoters returns every active user's id, so the snapshot covers anyone
// who might later vote. It pages through the user list rather than loading
// everyone at once, since a snapshot covering the whole electorate is the
// point and a single unbounded query is not.
func (r *governanceWeightResolver) EligibleVoters(ctx context.Context) ([]string, error) {
	const pageSize = 200
	const maxPages = 50 // cap at 10k voters per proposal

	ids := make([]string, 0, pageSize)
	for page := 1; page <= maxPages; page++ {
		users, err := r.users.List(ctx, user.UserFilter{Page: page, Limit: pageSize})
		if err != nil {
			return nil, err
		}
		for i := range users {
			ids = append(ids, users[i].ID.String())
		}
		if len(users) < pageSize {
			return ids, nil
		}
	}
	return ids, nil
}

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "config-validate" || os.Args[1] == "--config-validate" || (os.Args[1] == "config" && len(os.Args) > 2 && os.Args[2] == "validate")) {
		cfg, err := config.Load("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Configuration load failed:\n%v\n", err)
			os.Exit(1)
		}
		if err := cfg.ValidateOffline(); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Configuration offline validation failed:\n%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Configuration is valid (environment=%s, offline preflight passed)\n", cfg.Environment)
		os.Exit(0)
	}

	cfg, err := config.Load("")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load configuration")
	}

	logger.Init(cfg.Logging.Level, cfg.Logging.Format)
	validator.Init()

	// Apply whitelisted, non-critical config changes (log level, rate limits)
	// without a restart; an invalid edit is rejected and the old values stay.
	if err := cfg.Hot.Watch(
		func(h config.HotConfig) {
			logger.SetLevel(h.LogLevel)
			log.Info().Str("log_level", h.LogLevel).Msg("config reloaded")
		},
		func(err error) {
			log.Warn().Err(err).Msg("config reload rejected; keeping previous values")
		},
	); err != nil {
		log.Warn().Err(err).Msg("config hot-reload disabled")
	}

	// Initialize OpenTelemetry tracing
	if err := tracing.Init(cfg.Tracing); err != nil {
		log.Fatal().Err(err).Msg("failed to initialize tracing")
	}

	log.Info().Msg("starting Deodes API server")

	db, err := postgres.New(cfg.Database)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}

	// Optional read replica for analytics queries; nil falls back to the primary.
	replicaDB := postgres.NewReplica(cfg.Database)

	redisClient, err := redis.New(cfg.Redis)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to redis")
	}

	// Detect connection leaks and pool exhaustion; alerts are logged and counted
	// in deodes_db_pool_alerts_total.
	poolMonitorCtx, stopPoolMonitor := context.WithCancel(context.Background())
	poolMonitor := postgres.NewPoolMonitor(db, postgres.PoolMonitorOptions{}, func(a postgres.PoolAlert) {
		log.Warn().Str("kind", a.Kind).Str("detail", a.Detail).Msg("db pool alert")
	})
	go poolMonitor.Run(poolMonitorCtx)

	userRepo := user.NewRepository(db)
	circleRepo := circle.NewRepository(db)
	contribRepo := contribution.NewRepository(db)
	payoutRepo := payout.NewRepository(db)
	reputationRepo := reputation.NewRepository(db)
	notificationRepo := notification.NewRepository(db)
	notificationDeliveryRepo := notification.NewDeliveryAuditRepository(db)
	inviteRepo := invite.NewRepository(db)
	auditRepo := audit.NewRepository(db)

	communityRepo := community.NewRepository(db)

	wsHub := ws.NewHub()
	wsBroadcaster := ws.NewBroadcaster(wsHub, redisClient)
	wsBridge := ws.NewRedisBridge(wsHub, redisClient)

	userSvc := user.NewService(userRepo, circleRepo)
	// Wallet service is built before the circle service so circles can gate
	// creation/joining on funded wallets (no import cycle: the gate is a
	// narrow float64 interface). Declared up here; the auth handler still
	// consumes walletSvc below as before.
	walletCfg := wallet.Config{
		// Deterministic seed derivation for email-based wallets (#166).
		WalletPepper:  cfg.Security.WalletPepper,
		Argon2Time:    cfg.Security.Argon2Time,
		Argon2Memory:  cfg.Security.Argon2Memory,
		Argon2Threads: cfg.Security.Argon2Threads,
		// Custodial hosted Arc wallets encrypt secrets with this key.
		EncryptionKey: cfg.Security.EncryptionKey,
		// Arc chain settings for hosted 0x balance reads and sends.
		ArcRPCURL:       cfg.Arc.RPCURL,
		ArcChainID:      cfg.Arc.ChainID,
		ArcUSDCContract: cfg.Arc.USDCContract,
		// Daily value cap on custodial sends (C2).
		DailySendLimitUSD: cfg.Transfer.DailyLimitUSD,
	}
	walletRepo := wallet.NewRepository(db)
	walletSvc, err := wallet.NewService(walletRepo, walletCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize wallet service")
	}
	circleSvc := circle.NewService(circleRepo, &moiAdapter{repo: userRepo}, circle.Dependencies{
		CommunityChecker: &communityAdapter{repo: communityRepo},
		Broadcaster:      wsBroadcaster,
		Transactor:       circle.NewTransactor(db),
		BalanceChecker:   &walletBalanceAdapter{svc: walletSvc},
	})
	contribSvc := contribution.NewService(contribRepo, wsBroadcaster, contribution.NewTransactor(db), circleSvc)
	payoutSvc := payout.NewService(payoutRepo, &payoutWalletAdapter{repo: userRepo}, circleSvc)
	// Phase 2.5: Contribute/TriggerPayout resolve against the configured
	// circle contract. Wiring fails boot on a malformed address (the
	// config-load shape check already rejects typos; this is the
	// defense-in-depth service-side pin).
	if cwc, ok := contribSvc.(contribution.ServiceWithCircle); ok {
		if err := cwc.SetArcCircleAddress(cfg.Arc.CircleAddress); err != nil {
			log.Fatal().Err(err).Msg("invalid arc.circle_address for contribution service")
		}
	}
	if pwc, ok := payoutSvc.(payout.ServiceWithCircle); ok {
		if err := pwc.SetArcCircleAddress(cfg.Arc.CircleAddress); err != nil {
			log.Fatal().Err(err).Msg("invalid arc.circle_address for payout service")
		}
	}

	// Phase 2.1: record-time receipt verification for contributions/payouts.
	// Wired whenever an RPC URL is configured (independent of the 2.6 indexer
	// below): confirmed receipts settle immediately, reverted/wrong-contract
	// receipts are rejected, RPC flakes stay pending for the indexer.
	if strings.TrimSpace(cfg.Arc.RPCURL) != "" {
		verifyClient := arc.NewClient(cfg.Arc.RPCURL, cfg.Arc.ChainID)
		if cwc, ok := contribSvc.(contribution.ServiceWithCircle); ok {
			cwc.SetTxVerifier(verifyClient)
		}
		if pwc, ok := payoutSvc.(payout.ServiceWithCircle); ok {
			pwc.SetTxVerifier(verifyClient)
		}
		log.Info().Msg("record-time tx verification enabled (contribution/payout)")
	} else {
		log.Info().Msg("record-time tx verification disabled (arc.rpc_url unset; pending semantics)")
	}

	// Phase 2.6: Arc event indexer. Tails DeodesCircle logs (backfill +
	// live), settles on-chain verification, and fans out WS chain events.
	// Disabled when no RPC URL or no circle is configured (dev default):
	// the API serves everything else normally.
	if strings.TrimSpace(cfg.Arc.RPCURL) != "" && strings.TrimSpace(cfg.Arc.CircleAddress) != "" {
		arcClient := arc.NewClient(cfg.Arc.RPCURL, cfg.Arc.ChainID)
		batchBlocks := uint64(cfg.Indexer.BatchSize)
		if batchBlocks == 0 {
			batchBlocks = 50
		}
		reorgWindow := uint64(cfg.Indexer.ReorgWindow)
		if cfg.Indexer.ReorgWindow < 0 {
			reorgWindow = 10
		}
		arcPoller := indexer.NewPoller(
			arcClient,
			indexer.NewRepository(db),
			contribRepo,
			payoutRepo,
			circleRepo,
			wsBroadcaster,
			indexer.Config{
				ChainID:           cfg.Arc.ChainID,
				Addresses:         []string{cfg.Arc.CircleAddress},
				StartBlock:        cfg.Indexer.StartBlock,
				BatchBlocks:       batchBlocks,
				MaxBatchesPerTick: 20,
				ReorgWindow:       reorgWindow,
				PollInterval:      cfg.Indexer.PollInterval,
			},
		)
		go arcPoller.Run(context.Background())
		log.Info().
			Str("circle", cfg.Arc.CircleAddress).
			Uint64("chain_id", cfg.Arc.ChainID).
			Msg("arc indexer started")
	} else {
		log.Info().Msg("arc indexer disabled (arc.rpc_url or arc.circle_address unset)")
	}
	reputationSvc := reputation.NewService(reputationRepo)
	authSvc, err := auth.NewServiceWithKeyConfig(redisClient, cfg.Auth.NonceTTL, cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL, auth.KeyConfig{
		CurrentPrivateKeyPEM:  cfg.Auth.JWTPrivateKeyPEM,
		CurrentPublicKeyPEM:   cfg.Auth.JWTPublicKeyPEM,
		CurrentKID:            cfg.Auth.JWTCurrentKID,
		PreviousPrivateKeyPEM: cfg.Auth.JWTPreviousPrivateKeyPEM,
		PreviousPublicKeyPEM:  cfg.Auth.JWTPreviousPublicKeyPEM,
		PreviousKID:           cfg.Auth.JWTPreviousKID,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize auth service")
	}

	// Expired-session cleanup (#374). This job is the only thing that reclaims
	// session state by age — request paths delete sessions by explicit token
	// hash on logout or revocation and never sweep. Every replica schedules the
	// job with jitter and contends for a Redis lock, so a tick performs exactly
	// one sweep across the fleet.
	sessionCleaner := session.NewCleaner(redisClient, session.NewSessionStore(db), cfg.Auth.CleanupInterval, cfg.Auth.CleanupJitter)
	sessionCleaner.Start(context.Background())

	totpSvc := totp.NewService()
	verificationSvc := verification.NewService(redisClient).WithPepper(cfg.Security.WalletPepper)
	emailSvc := email.NewService(email.Config{
		APIKey:           cfg.Brevo.APIKey,
		FromAddress:      cfg.Brevo.FromEmail,
		FromName:         cfg.Brevo.FromName,
		TempSenderNotice: cfg.Brevo.TempSenderNotice,
	})
	// Warn-only sender check: a From address that Brevo does not know fails
	// every send with a 4xx. Brevo downtime must never block API boot, so a
	// failed check logs an ERROR with the fix and continues.
	if strings.TrimSpace(cfg.Brevo.APIKey) != "" {
		vctx, vcancel := context.WithTimeout(context.Background(), 10*time.Second)
		if verr := emailSvc.VerifySender(vctx); verr != nil {
			log.Error().Err(verr).Msg("brevo sender check failed — emails will likely 4xx until the sender domain is verified")
		} else {
			log.Info().Str("from", cfg.Brevo.FromEmail).Msg("brevo sender verified")
		}
		vcancel()
	} else {
		log.Warn().Msg("brevo API key unset — OTP emails are skipped in dev mode (users will never receive codes)")
	}

	// Notification delivery channels (#191): email reuses the same Brevo
	// client already used for OTP/backup-code/recovery emails; SMS/push are
	// new clients reading the notification.sms.*/notification.push.* config
	// that already existed (see config.NotificationConfig) but had nothing
	// wired to it.
	smsSvc := sms.NewService(sms.Config{
		AccountSID: cfg.Notification.SMS.AccountSID,
		AuthToken:  cfg.Notification.SMS.AuthToken,
		FromNumber: cfg.Notification.SMS.FromNumber,
	})
	pushSvc := push.NewService(push.Config{
		ServerKey: cfg.Notification.Push.FCMServerKey,
	})
	notificationSvc := notification.NewService(notificationRepo, nil, wsBroadcaster,
		notification.WithDeliveryChannels(
			&userLookupAdapter{repo: userRepo},
			notificationDeliveryRepo,
			&notification.EmailChannel{Sender: emailSvc},
			&notification.SMSChannel{Sender: smsSvc},
			&notification.PushChannel{Sender: pushSvc},
		),
		// Digest batching (#415): non-urgent circle events collapse into a
		// periodic per-user summary; urgent classes still go out immediately.
		notification.WithDigestBatching(
			notification.NewDigestBuffer(),
			&digestPreferenceAdapter{repo: userRepo},
		),
	)

	inviteSvc := invite.NewService(inviteRepo)
	_ = auditRepo

	// Wallet service is initialized above (before the circle service) so the
	// funds gate can use it.

	jwtPublicKey := []byte(cfg.Auth.JWTPublicKeyPEM)

	wsH := handler.NewWebSocketHandler(wsHub, cfg.CORS.AllowedOrigins)
	go wsHub.StartMembershipAuditor(context.Background(), 30*time.Second)

	authH := handler.NewAuthHandler(authSvc, userSvc, walletSvc, totpSvc, verificationSvc, emailSvc, redisClient, userRepo).
		WithGoogle(authSvc, userSvc, userRepo, walletSvc, cfg.Google.ClientID)
	userH := handler.NewUserHandler(userSvc)
	circleH := handler.NewCircleHandler(circleSvc, inviteSvc, contribSvc, payoutSvc)
	contribH := handler.NewContributionHandler(contribSvc, contribRepo)
	payoutH := handler.NewPayoutHandler(payoutSvc, payoutRepo)
	inviteH := handler.NewInviteHandler(inviteSvc)
	notifH := handler.NewNotificationHandler(notificationSvc, userSvc)
	adminSvc, err := admin.NewService(admin.NewRepositoryWithReader(postgres.NewReader(db, replicaDB)), 0)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize admin metrics service")
	}
	featureFlagRepo := featureflag.NewRepository(db)
	featureFlagSvc := featureflag.NewService(featureFlagRepo)
	featureFlagCache := featureflag.NewCache(featureFlagSvc, featureflag.DefaultReloadInterval)
	if err := featureFlagCache.Start(context.Background()); err != nil {
		log.Warn().Err(err).Msg("failed to load feature flags on startup — cache starts empty until the next reload")
	}
	communitySvc := community.NewService(communityRepo, wsBroadcaster)
	communityH := handler.NewCommunityHandler(communitySvc)
	adminH := handler.NewAdminHandler(userSvc, userRepo, circleSvc, auditRepo, adminSvc, featureFlagSvc, featureFlagCache).
		WithCommunityService(communitySvc).
		WithWalletRepository(walletRepo)
	webhookRepo := webhook.NewPostgresRepository(db.DB)
	webhookH := handler.NewWebhookHandler(webhookRepo)
	healthH := handler.NewHealthHandler(db.DB, redisClient)
	passkeyCredH := handler.NewPasskeyCredentialHandler(db)
	walletH := handler.NewWalletHandler(walletSvc, authSvc).WithUserRepository(userRepo).WithWalletRepository(walletRepo).WithNotificationService(notificationSvc)

	// Fiat ramp (Yellow Card) removed — local-currency deposits are coming
	// soon. Re-add the ramp wiring here when a provider is chosen; the
	// deposits/withdrawals tables stay dormant in the meantime.

	// Mobile-money bridge (#190) — on/off-ramp for non-NGN markets. Each
	// provider is only registered when its credentials are configured, so
	// deployments that haven't onboarded a given provider yet just don't
	// advertise support for its currency rather than failing to start.
	mmRegistry := mobilemoney.NewRegistry()
	if cfg.MobileMoney.MPesaConsumerKey != "" {
		mmRegistry.Register(mobilemoney.NewMPesaProvider(mobilemoney.MPesaConfig{
			ConsumerKey:        cfg.MobileMoney.MPesaConsumerKey,
			ConsumerSecret:     cfg.MobileMoney.MPesaConsumerSecret,
			Shortcode:          cfg.MobileMoney.MPesaShortcode,
			Passkey:            cfg.MobileMoney.MPesaPasskey,
			SecurityCredential: cfg.MobileMoney.MPesaSecurityCredential,
			InitiatorName:      cfg.MobileMoney.MPesaInitiatorName,
			CallbackBaseURL:    cfg.MobileMoney.CallbackBaseURL,
			Sandbox:            cfg.MobileMoney.MPesaSandbox,
		}))
	}
	if cfg.MobileMoney.MTNSubscriptionKey != "" {
		mmRegistry.Register(mobilemoney.NewMTNProvider(mobilemoney.MTNConfig{
			SubscriptionKey: cfg.MobileMoney.MTNSubscriptionKey,
			APIUser:         cfg.MobileMoney.MTNAPIUser,
			APIKey:          cfg.MobileMoney.MTNAPIKey,
			TargetCurrency:  cfg.MobileMoney.MTNTargetCurrency,
			CallbackBaseURL: cfg.MobileMoney.CallbackBaseURL,
			Sandbox:         cfg.MobileMoney.MTNSandbox,
		}))
	}
	if cfg.MobileMoney.AirtelClientID != "" {
		mmRegistry.Register(mobilemoney.NewAirtelProvider(mobilemoney.AirtelConfig{
			ClientID:        cfg.MobileMoney.AirtelClientID,
			ClientSecret:    cfg.MobileMoney.AirtelClientSecret,
			Country:         cfg.MobileMoney.AirtelCountry,
			TargetCurrency:  cfg.MobileMoney.AirtelTargetCurrency,
			CallbackBaseURL: cfg.MobileMoney.CallbackBaseURL,
			Sandbox:         cfg.MobileMoney.AirtelSandbox,
		}))
	}
	mmRepo := mobilemoney.NewRepository(db)
	mmSvc := mobilemoney.NewService(mmRepo, mmRegistry)
	mobileMoneyH := handler.NewMobileMoneyHandler(mmSvc, walletSvc)

	// Startup provider-presence probe and logging (#412)
	activeProviders := mmRegistry.ActiveProviderNames()
	if len(activeProviders) > 0 {
		log.Info().
			Strs("active_providers", activeProviders).
			Strs("supported_currencies", mmRegistry.SupportedCurrencies()).
			Msg("mobile money providers initialized")
	} else {
		isDev := cfg.Environment == "development" || cfg.Environment == "dev" || cfg.Environment == "test"
		if !isDev {
			log.Error().Msg("zero mobile money providers configured in non-development environment")
		} else {
			log.Warn().Msg("no mobile money providers configured")
		}
	}

	// E2EE chat (#188): X3DH key bundles + encrypted message store on top
	// of the crypto primitives in internal/domain/chat/x3dh.go.
	chatKeyRepo := chat.NewKeyRepository(db)
	chatMsgRepo := chat.NewRepository(db)
	chatSvc := chat.NewService(chatKeyRepo, chatMsgRepo, wsBroadcaster)
	chatH := handler.NewChatHandler(chatSvc)

	reconcileInterval := time.Duration(cfg.MobileMoney.ReconcileIntervalMin) * time.Minute
	if reconcileInterval <= 0 {
		reconcileInterval = 5 * time.Minute
	}
	// Reconciler with Redis single-flight lock across replicas (#413)
	mmReconciler := mobilemoney.NewReconciler(redisClient, mmSvc, reconcileInterval, cfg.Auth.CleanupJitter)
	mmReconciler.Start(context.Background())

	// Digest flusher (#415): drains each user's batched circle events into a
	// single summary once their cadence elapses. The tick is a heartbeat only
	// — the per-user cadence in the buffer decides what is actually due.
	digestFlusher := notification.NewDigestFlusher(notificationSvc, notification.DefaultDigestFlushInterval)
	digestFlusher.Start(context.Background())

	// Savings goals
	savingsRepo := savings.NewRepository(db)
	savingsSvc := savings.NewService(savingsRepo)
	savingsH := handler.NewSavingsGoalHandler(savingsSvc)

	// Auto-save keeper: tops up due goal allocations on a tick (15m). Uses
	// the same wallet balance adapter as the circle funds gate; atomic
	// last_auto_at claims make concurrent replicas safe.
	savingsKeeper := savings.NewKeeper(savingsRepo, &walletBalanceAdapter{svc: walletSvc}, redisClient, 15*time.Minute)
	savingsKeeper.Start(context.Background())

	// Saved fiat payout rails (payment settings screen).
	bankAccountRepo := bankaccount.NewRepository(db)
	bankAccountSvc := bankaccount.NewService(bankAccountRepo)
	bankAccountH := handler.NewBankAccountHandler(bankAccountSvc)

	// Swap escrow is on-chain (purged): no escrow client exists, so
	// the swap service gets an untyped nil and fails fast per call
	// (requireEscrow) instead of failing at boot. Arc escrow arrives with
	// the 2.x swap rework; off-chain offer records work meanwhile.
	var escrowSwapClient swap.EscrowClient

	// Swap service and handler
	swapRepo := swap.NewPostgresRepository(db)
	swapSvc := swap.NewService(swapRepo, circleSvc, userSvc, escrowSwapClient)
	swapH := handler.NewSwapHandler(swapSvc)

	// Swap sweep worker with Redis single-flight lock across replicas (#416).
	// The lock avoids duplicated work; the per-offer atomic claim in
	// SweepExpiredOffers is what guarantees escrow is never released twice.
	swapSweeper := swap.NewSweeper(swapSvc, redisClient, cfg.Swap.SweepInterval)
	swapSweeper.Start(context.Background())

	// Auto-debit subscription keeper: pulls scheduled contributions for
	// members who opted into Auto circles. Nil keeper (no escrow address)
	// is a no-op, so deployments configure escrow only when ready to debit.
	// Sprint 2 wires OnEvent into the notification service (T-24h reminders
	// today come from circle contribution deadlines, not the keeper).
	var keeperLoop *subscription.Loop
	keeperInterval := time.Duration(cfg.Keeper.IntervalMin) * time.Minute
	keeperNotifier := subscription.NewNotifier(notificationSvc)
	keeper, keeperErr := subscription.NewKeeper(subscription.KeeperConfig{
		Repo:          subscription.NewRepository(db),
		Wallets:       walletSvc,
		Contrib:       contribSvc,
		Circles:       circleSvc,
		EscrowAddress: cfg.Keeper.EscrowAddress,
		// Phase 0.6 freeze: protocol-fee vault and debit collector must be
		// separate keys. Equal values fail here (fail-closed) instead of
		// commingling fees with user debits.
		TreasuryAddress: cfg.Arc.TreasuryAddress,
		Notifier:        keeperNotifier,
		OnEvent: func(ctx context.Context, e subscription.DebitEvent) {
			log.Info().
				Str("subscription", e.SubscriptionID).
				Str("circle", e.CircleID).
				Int("round", e.Round).
				Str("outcome", string(e.Outcome)).
				Str("reason", e.Reason).
				Str("tx", e.TxHash).
				Msg("subscription keeper decision")
			// Member-facing outcome notifications ride the same notifier
			// as reminders; failures are best-effort (the event log above
			// is the source of truth for reconciliation).
			switch e.Outcome {
			case subscription.OutcomeDebited:
				_ = keeperNotifier.NotifyDebited(ctx, e.UserID, e.CircleID, e.Amount, e.Round, e.TxHash)
			case subscription.OutcomeFailed:
				_ = keeperNotifier.NotifyDebitFailed(ctx, e.UserID, e.CircleID, e.Amount, e.Round, e.Reason)
			case subscription.OutcomeSkippedLowBalance:
				_ = keeperNotifier.NotifyContributionDue(ctx, e.UserID, e.CircleID, e.Amount, 0)
			}
		},
	})
	if keeperErr != nil {
		// Missing escrow or wiring: log once, keep serving everything else.
		log.Info().Err(keeperErr).Msg("subscription keeper disabled")
	} else {
		keeperLoop = subscription.NewLoop(subscription.LoopConfig{
			Keeper:   keeper,
			Redis:    redisClient,
			Interval: keeperInterval,
			Jitter:   cfg.Auth.CleanupJitter,
		})
		keeperLoop.Start(context.Background())
	}

	governanceRepo := governance.NewRepository(db)
	// Both #418 and #414 configure the same service: weight is snapshotted at
	// proposal creation so tokens moved mid-vote cannot change an outcome, and a
	// passed proposal waits out an execution timelock during which it can still
	// be cancelled by threshold vote. A zero timelock delay restores the
	// pre-#414 immediate execution.
	governanceSvc := governance.NewService(governanceRepo,
		governance.WithWeightResolver(
			&governanceWeightResolver{users: userRepo, reputation: reputationRepo},
		),
		governance.WithTimelock(governance.TimelockConfig{
			Delay:              cfg.Governance.ExecutionTimelock,
			CancelThresholdPct: cfg.Governance.CancelThresholdPct,
		}),
	)
	governanceH := handler.NewGovernanceHandler(governanceSvc)

	incentivesRepo := incentives.NewRepository(db)
	incentivesSvc := incentives.NewService(incentivesRepo)

	// DeoScore pipeline: confirmed contributions refresh the contributor's
	// score (snapshot + users.moi_score) best-effort, and the admin
	// recalculate endpoint backfills everyone. Before this wiring nothing
	// ever wrote to either store, so every score sat at its signup zero.
	scoreRefresher := reputation.NewRefresher(
		contribRepo,
		&streakAdapter{svc: incentivesSvc},
		&completionsAdapter{svc: circleSvc},
		reputationSvc,
		userRepo,
	)
	if cwc, ok := contribSvc.(contribution.ServiceWithCircle); ok {
		cwc.SetScoreUpdater(scoreRefresher)
	}
	adminH.WithScoreRefresher(scoreRefresher)
	reputationH := handler.NewReputationHandler(reputationSvc)
	referralH := handler.NewReferralHandler(incentivesSvc)

	// GDPR cookie consent handler
	consentH := handler.NewConsentHandler(db.DB)

	// Contact-form inbox: public writes, admin triage.
	supportH := handler.NewSupportHandler(support.NewService(support.NewRepository(db)), auditRepo)

	// Auto-debit subscriptions share the keeper's repository.
	subscriptionH := handler.NewSubscriptionHandler(subscription.NewRepository(db), circleSvc)

	// RabbitMQ connection for health checks and event publishing
	rmqClient, rmqErr := rabbitmq.New(cfg.RabbitMQ)
	if rmqErr != nil {
		log.Warn().Err(rmqErr).Msg("RabbitMQ unavailable — health checks will report degraded")
	}

	// Wire RabbitMQ and MobileMoney into health handler for /health and /health/ready probes
	if rmqClient != nil {
		healthH.WithRabbitMQ(rmqClient)
	}
	healthH.WithMobileMoney(mmRegistry)

	// Job queue for background tasks
	jobQueue := jobqueue.NewJobQueue(db)
	adminJobQueueH := handler.NewAdminJobQueueHandler(jobQueue)

	router := api.NewRouter(cfg, redisClient, authH, userH, circleH, contribH, payoutH, inviteH, notifH, adminH, webhookH, healthH, passkeyCredH, walletH, mobileMoneyH, chatH, communityH, wsH, savingsH, bankAccountH, swapH, governanceH, reputationH, referralH, consentH, subscriptionH, adminJobQueueH, supportH, webhookRepo, jwtPublicKey)

	// Shutdown order: fail readiness first so the load balancer stops sending
	// traffic, drain in-flight HTTP requests (bounded by
	// server.shutdown_timeout), close long-lived WebSocket connections and
	// background loops, and only then close the pools they were using.
	hooks := api.ShutdownHooks{
		PreDrain: []func(){healthH.BeginShutdown},
		Drain: []func(context.Context){
			func(ctx context.Context) {
				if err := wsHub.Shutdown(ctx); err != nil {
					log.Warn().Err(err).Msg("websocket drain incomplete")
				}
			},
			func(context.Context) { wsBridge.Close() },
			func(context.Context) { stopPoolMonitor() },
			func(context.Context) {
				featureFlagCache.Stop()
				mmReconciler.Stop()
			},
			func(context.Context) { digestFlusher.Stop() },
			func(context.Context) { swapSweeper.Stop() },
			func(context.Context) { savingsKeeper.Stop() },
			func(context.Context) {
				if keeperLoop != nil {
					keeperLoop.Stop()
				}
			},
			func(context.Context) { sessionCleaner.Stop() },
		},
		CloseLast: []func(){
			func() {
				if rmqClient != nil {
					rmqClient.Close()
				}
			},
			func() {
				if err := redisClient.Close(); err != nil {
					log.Warn().Err(err).Msg("closing redis")
				}
			},
			func() {
				if replicaDB != nil {
					if err := replicaDB.Close(); err != nil {
						log.Warn().Err(err).Msg("closing postgres replica")
					}
				}
				if err := db.Close(); err != nil {
					log.Warn().Err(err).Msg("closing postgres")
				}
			},
		},
	}

	if err := api.RunServerWithHooks(router, cfg.Server, hooks); err != nil {
		log.Fatal().Err(err).Msg("server error")
	}
}
