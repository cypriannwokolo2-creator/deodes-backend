package config

import (
	"bufio"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// arcAddressRegexp mirrors pkg/arc.ValidateAddress (0.7 validator style).
// Duplicated here so the config package stays dependency-free.
var arcAddressRegexp = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

type Config struct {
	Server       ServerConfig
	Database     DatabaseConfig
	Redis        RedisConfig
	RabbitMQ     RabbitMQConfig
	Auth         AuthConfig
	Security     SecurityConfig
	Brevo        BrevoConfig
	Indexer      IndexerConfig
	Notification NotificationConfig
	CORS         CORSConfig
	RateLimit    RateLimitConfig `mapstructure:"rate_limit"`
	Logging      LoggingConfig
	Environment  string
	Tracing      TracingConfig
	Swap         SwapConfig        `mapstructure:"swap"`
	Transfer     TransferConfig    `mapstructure:"transfer"`
	MobileMoney  MobileMoneyConfig `mapstructure:"mobile_money"`
	Governance   GovernanceConfig  `mapstructure:"governance"`
	Arc          ArcConfig         `mapstructure:"arc"`
	Keeper       KeeperConfig      `mapstructure:"keeper"`
	Google       GoogleConfig      `mapstructure:"google"`

	// Hot holds the live values of the keys that can be reloaded without a
	// restart (see HotReloader).
	Hot *HotReloader `mapstructure:"-"`
}

type ServerConfig struct {
	Port             int           `mapstructure:"port"`
	Host             string        `mapstructure:"host"`
	ReadTimeout      time.Duration `mapstructure:"read_timeout"`
	WriteTimeout     time.Duration `mapstructure:"write_timeout"`
	MaxHeaderBytes   int           `mapstructure:"max_header_bytes"`
	TLSEnabled       bool          `mapstructure:"tls_enabled"`
	TLSCertPath      string        `mapstructure:"tls_cert_path"`
	TLSKeyPath       string        `mapstructure:"tls_key_path"`
	HTTPRedirectPort int           `mapstructure:"http_redirect_port"`
	// ShutdownDelay is how long the server keeps serving after readiness has
	// flipped to not-ready, so load balancers stop routing to this replica
	// before the listener closes. Match it to your platform's endpoint
	// propagation time (a few seconds on Kubernetes).
	ShutdownDelay time.Duration `mapstructure:"shutdown_delay"`
	// ShutdownTimeout bounds how long in-flight requests may take to finish
	// after SIGTERM before their connections are closed forcibly.
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// MaxBodyBytes caps request bodies on every write route (#445). Zero falls
	// back to middleware.DefaultMaxBodyBytes (4 MB).
	MaxBodyBytes int64 `mapstructure:"max_body_bytes"`
	// MaxBodyBytesRoutes raises or lowers the cap for individual routes, keyed
	// by the Gin route pattern (e.g. "/v1/wallets/withdraw"). Use it for
	// endpoints that legitimately accept larger payloads than the default.
	MaxBodyBytesRoutes map[string]int64 `mapstructure:"max_body_bytes_routes"`
	// TrustedProxies is the list of CIDRs / IPs whose X-Forwarded-For we
	// trust when deriving c.ClientIP() for rate limiting. Must include the
	// LB/CDN in front of deodes.com, otherwise every client collapses to
	// the proxy IP and shares one OTP bucket (always-429). Empty = trust
	// loopback only (secure default for direct-connect dev).
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

type DatabaseConfig struct {
	URL             string        `mapstructure:"url"`
	ReplicaURL      string        `mapstructure:"replica_url"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	MigrationPath   string        `mapstructure:"migration_path"`
}

type RedisConfig struct {
	URL      string `mapstructure:"url"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

type RabbitMQConfig struct {
	URL      string `mapstructure:"url"`
	Exchange string `mapstructure:"exchange"`
	Queues   struct {
		Notifications string `mapstructure:"notifications"`
		Webhooks      string `mapstructure:"webhooks"`
	} `mapstructure:"queues"`
}

// MobileMoneyConfig holds credentials for the mobile-money bridge (#190,
// product-spec.md §7): M-Pesa, MTN Mobile Money, and Airtel Money adapters
// for non-NGN African markets. Each provider is only registered at startup
// when its required credentials are non-empty, so an unconfigured provider
// simply isn't available rather than failing startup.
type MobileMoneyConfig struct {
	CallbackBaseURL      string `mapstructure:"callback_base_url"`
	ReconcileIntervalMin int    `mapstructure:"reconcile_interval_minutes"`

	MPesaConsumerKey        string `mapstructure:"mpesa_consumer_key"`
	MPesaConsumerSecret     string `mapstructure:"mpesa_consumer_secret"`
	MPesaShortcode          string `mapstructure:"mpesa_shortcode"`
	MPesaPasskey            string `mapstructure:"mpesa_passkey"`
	MPesaSecurityCredential string `mapstructure:"mpesa_security_credential"`
	MPesaInitiatorName      string `mapstructure:"mpesa_initiator_name"`
	MPesaSandbox            bool   `mapstructure:"mpesa_sandbox"`

	MTNSubscriptionKey string `mapstructure:"mtn_subscription_key"`
	MTNAPIUser         string `mapstructure:"mtn_api_user"`
	MTNAPIKey          string `mapstructure:"mtn_api_key"`
	MTNTargetCurrency  string `mapstructure:"mtn_target_currency"`
	MTNSandbox         bool   `mapstructure:"mtn_sandbox"`

	AirtelClientID       string `mapstructure:"airtel_client_id"`
	AirtelClientSecret   string `mapstructure:"airtel_client_secret"`
	AirtelCountry        string `mapstructure:"airtel_country"`
	AirtelTargetCurrency string `mapstructure:"airtel_target_currency"`
	AirtelSandbox        bool   `mapstructure:"airtel_sandbox"`
}

// ArcConfig holds the Arc (EVM) chain settings for hosted wallets and the
// contribution keeper. USDC (18 decimals) is the native gas token. The USDC
// contract address has no safe default — look it up at
// docs.arc.io/arc/references/contract-addresses.md for the target network.
// Empty USDCContract fails fast at transfer time, never at boot — except in
// mainnet mode (environment production/mainnet),
// where the startup guard (checkMainnetIssues) rejects an empty rpc_url, a
// testnet rpc_url, an empty usdc_contract, and any chain_id other than 5042.
type ArcConfig struct {
	RPCURL       string `mapstructure:"rpc_url"`
	ChainID      uint64 `mapstructure:"chain_id"`
	USDCContract string `mapstructure:"usdc_contract"`
	// CircleAddress is the DeodesCircle deployment for the target network.
	// Source of truth: deodes-contracts/deployments.{testnet,mainnet}.json
	// (Phase 0.5 freeze). Empty fails fast at call time, never at boot —
	// except in mainnet mode, where the startup guard rejects it.
	CircleAddress string `mapstructure:"circle_address"`
	// TreasuryAddress is the protocol-fee vault (DeodesCircle treasury).
	// MUST differ from keeper.escrow_address (user-debit collector):
	// commingling them was frozen out in Phase 0.6 scope.
	TreasuryAddress string `mapstructure:"treasury_address"`
}

// KeeperConfig drives the auto-debit subscription loop. Empty EscrowAddress
// disables debiting (the loop still runs its tick but debits nothing —
// fail-closed until an operator collection address is configured).
// Mainnet guard: an empty escrow is REJECTED when the keeper is enabled
// (interval_minutes != 0). Set keeper.interval_minutes to 0 to disable the
// keeper in mainnet mode instead of configuring an escrow address.
type KeeperConfig struct {
	EscrowAddress string `mapstructure:"escrow_address"`
	IntervalMin   int    `mapstructure:"interval_minutes"`
}

type AuthConfig struct {
	JWTPrivateKeyPath         string        `mapstructure:"jwt_private_key_path"`
	JWTPublicKeyPath          string        `mapstructure:"jwt_public_key_path"`
	JWTPrivateKeyPEM          string        `mapstructure:"jwt_private_key_pem"`
	JWTPublicKeyPEM           string        `mapstructure:"jwt_public_key_pem"`
	JWTCurrentKID             string        `mapstructure:"jwt_current_kid"`
	JWTPreviousPrivateKeyPath string        `mapstructure:"jwt_previous_private_key_path"`
	JWTPreviousPublicKeyPath  string        `mapstructure:"jwt_previous_public_key_path"`
	JWTPreviousPrivateKeyPEM  string        `mapstructure:"jwt_previous_private_key_pem"`
	JWTPreviousPublicKeyPEM   string        `mapstructure:"jwt_previous_public_key_pem"`
	JWTPreviousKID            string        `mapstructure:"jwt_previous_kid"`
	AccessTokenTTL            time.Duration `mapstructure:"access_token_ttl"`
	RefreshTokenTTL           time.Duration `mapstructure:"refresh_token_ttl"`
	NonceTTL                  time.Duration `mapstructure:"nonce_ttl"`
	AdminAPIKey               string        `mapstructure:"admin_api_key"`
	AdminAPIKeySecondary      string        `mapstructure:"admin_api_key_secondary"`
	// CleanupInterval is how often the scheduled session cleanup job runs
	// (#374). It is the only thing that reclaims expired session state; no
	// request path sweeps.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`
	// CleanupJitter is the upper bound of the random delay applied before each
	// sweep attempt, so replicas do not contend for the cleanup lock in
	// lockstep.
	CleanupJitter time.Duration `mapstructure:"cleanup_jitter"`
}

type SecurityConfig struct {
	// WalletPepper is the single server-side secret used for deterministic
	// wallet seed derivation. The former, unused PasskeyPepper field and its
	// DEODES_PASSKEY_PEPPER env binding were removed in #163 so there is
	// exactly one pepper source of truth.
	WalletPepper  string `mapstructure:"wallet_pepper"`
	EncryptionKey string `mapstructure:"encryption_key"`
	Argon2Time    int    `mapstructure:"argon2_time"`
	Argon2Memory  int    `mapstructure:"argon2_memory"`
	Argon2Threads int    `mapstructure:"argon2_threads"`
}

// GoogleConfig holds the OAuth client ID used as the audience check for
// Google ID tokens on POST /v1/auth/google. Empty disables the endpoint
// (fail-closed) without blocking boot.
type GoogleConfig struct {
	ClientID string `mapstructure:"client_id"`
}

type BrevoConfig struct {
	APIKey    string `mapstructure:"api_key"`
	FromEmail string `mapstructure:"from_email"`
	FromName  string `mapstructure:"from_name"`
	// TempSenderNotice appends the temporary-sender footer to code emails.
	// Flip back to false when the canonical domain is restored.
	TempSenderNotice bool `mapstructure:"temp_sender_notice"`
}

type IndexerConfig struct {
	PollInterval time.Duration `mapstructure:"poll_interval"`
	BatchSize    int           `mapstructure:"batch_size"`
	StartLedger  int64         `mapstructure:"start_ledger"`
	// StartBlock is the Arc backfill origin (2.6): the first block polled
	// when no arc_cursor row exists. Set to the circle/factory deployment
	// block to skip empty history; 0 means genesis.
	StartBlock uint64 `mapstructure:"start_block"`
	// MaxCursorLag is how long the cursor's last_processed_at may trail the
	// current time before the health server reports the indexer as unhealthy.
	MaxCursorLag time.Duration `mapstructure:"max_cursor_lag"`
	// ReorgWindow is how many ledgers behind the cursor the reorg check
	// inspects, and therefore how deep a reorganization can be detected and
	// rolled back. Values <= 0 fall back to a 10 ledger default; a reorg
	// reaching past the window is reported rather than partially repaired
	// (#346).
	ReorgWindow int `mapstructure:"reorg_window"`

	StallThreshold time.Duration `mapstructure:"stall_threshold"`
}

type NotificationConfig struct {
	Email struct {
		Provider    string `mapstructure:"provider"`
		APIKey      string `mapstructure:"api_key"`
		FromAddress string `mapstructure:"from_address"`
	} `mapstructure:"email"`
	SMS struct {
		Provider   string `mapstructure:"provider"`
		AccountSID string `mapstructure:"account_sid"`
		AuthToken  string `mapstructure:"auth_token"`
		FromNumber string `mapstructure:"from_number"`
	} `mapstructure:"sms"`
	Push struct {
		FCMServerKey string `mapstructure:"fcm_server_key"`
	} `mapstructure:"push"`
}

type CORSConfig struct {
	AllowedOrigins   []string      `mapstructure:"allowed_origins"`
	AllowedMethods   []string      `mapstructure:"allowed_methods"`
	AllowedHeaders   []string      `mapstructure:"allowed_headers"`
	AllowCredentials bool          `mapstructure:"allow_credentials"`
	MaxAge           time.Duration `mapstructure:"max_age"`
}

type SwapConfig struct {
	// SweepInterval is how often the swap sweep worker runs. The sweep
	// releases escrow on-chain for created swap offers past their expiry and
	// marks them expired (#243).
	SweepInterval time.Duration `mapstructure:"sweep_interval"`
}

// TransferConfig bounds member-to-member and withdrawal sends (C2).
type TransferConfig struct {
	// DailyLimitUSD caps completed sends per rolling 24h across withdraw +
	// transfer (both settle via SendPayment). Zero/negative disables.
	DailyLimitUSD float64 `mapstructure:"daily_limit_usd"`
}

// GovernanceConfig holds the execution timelock settings (#414).
type GovernanceConfig struct {
	// ExecutionTimelock is how long a proposal that has passed its vote waits
	// before it can be executed. It gives members a window to review the
	// proposal and cancel it, instead of a passed proposal executing the
	// instant anyone calls the execute endpoint. Set to "0" to disable the
	// timelock and restore immediate execution.
	ExecutionTimelock time.Duration `mapstructure:"execution_timelock"`
	// CancelThresholdPct is the share of votes cast (for + against) that must
	// vote to cancel a proposal while it is inside its timelock. Out-of-range
	// values fall back to the domain default.
	CancelThresholdPct int `mapstructure:"cancel_threshold_pct"`
}

type RateLimitConfig struct {
	Global        int `mapstructure:"global"`
	Authenticated int `mapstructure:"authenticated"`
	Auth          int `mapstructure:"auth"`
	// FailClosed decides what happens when Redis is unreachable during a rate
	// limit check. True (the default, and the single policy documented in
	// docs/rate-limiting.md) refuses the request with 503 — the same posture
	// the legacy JS middleware/rateLimiter.js always had. False falls back to
	// the in-memory limiter (fails open). Individual routes may override this
	// per-route via middleware.RateLimitMiddleware options.
	FailClosed bool `mapstructure:"fail_closed"`

	// Per-resource limits (#197). middleware.PerResourceRateLimitMiddleware
	// existed but was never applied to any route — these give the
	// high-value/sensitive resources their own configurable, tighter buckets
	// instead of sharing the coarse Global/Authenticated/Auth limits above.
	OTPLimit         int `mapstructure:"otp_limit"`
	OTPWindowSeconds int `mapstructure:"otp_window_seconds"`
	// Split OTP buckets (fix for always-429 on verify): send and verify used
	// to share one "otp" bucket (5/15m for 6 endpoints), so 1 register + 1
	// typo + 1 resend = locked. Zero falls back to OTPLimit/OTPWindowSeconds.
	OTPSendLimit           int `mapstructure:"otp_send_limit"`
	OTPSendWindowSeconds   int `mapstructure:"otp_send_window_seconds"`
	OTPVerifyLimit         int `mapstructure:"otp_verify_limit"`
	OTPVerifyWindowSeconds int `mapstructure:"otp_verify_window_seconds"`
	// Login bucket (login + google never sent OTP but shared the OTP bucket).
	// Zero falls back to OTPLimit/OTPWindowSeconds.
	LoginLimit         int `mapstructure:"login_limit"`
	LoginWindowSeconds int `mapstructure:"login_window_seconds"`
	// Public invite preview (landing page): high-entropy codes need no
	// strict bucket, but unauthenticated enumeration must stay bounded.
	InvitePreviewLimit         int `mapstructure:"invite_preview_limit"`
	InvitePreviewWindowSeconds int `mapstructure:"invite_preview_window_seconds"`
	// Anonymous-write buckets (claim-name allocates names, consent writes
	// records — both unauthenticated and previously unlimited).
	ClaimLimit                  int `mapstructure:"claim_limit"`
	ClaimWindowSeconds          int `mapstructure:"claim_window_seconds"`
	ConsentLimit                int `mapstructure:"consent_limit"`
	ConsentWindowSeconds        int `mapstructure:"consent_window_seconds"`
	SwapLimit                   int `mapstructure:"swap_limit"`
	SwapWindowSeconds           int `mapstructure:"swap_window_seconds"`
	ContributeLimit             int `mapstructure:"contribute_limit"`
	ContributeWindowSeconds     int `mapstructure:"contribute_window_seconds"`
	WalletTransferLimit         int `mapstructure:"wallet_transfer_limit"`
	WalletTransferWindowSeconds int `mapstructure:"wallet_transfer_window_seconds"`
	ReferralLimit               int `mapstructure:"referral_limit"`
	ReferralWindowSeconds       int `mapstructure:"referral_window_seconds"`
	PasswordResetIPLimit        int `mapstructure:"password_reset_ip_limit"`
	PasswordResetAccountLimit   int `mapstructure:"password_reset_account_limit"`
	PasswordResetWindowSeconds  int `mapstructure:"password_reset_window_seconds"`
	AdminLimit                  int `mapstructure:"admin_limit"`
	AdminWindowSeconds          int `mapstructure:"admin_window_seconds"`
}

// resolveWithFallback returns limit/window, falling back to the legacy OTP
// bucket when the split bucket is unset (zero). Keeps old configs and tests
// that only set otp_limit working.
func (r RateLimitConfig) resolveWithFallback(limit, window int) (int, int) {
	if limit <= 0 {
		limit = r.OTPLimit
	}
	if window <= 0 {
		window = r.OTPWindowSeconds
	}
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = 900
	}
	return limit, window
}

// OTPSend returns the send-code bucket (register, password/forgot).
func (r RateLimitConfig) OTPSend() (int, int) {
	return r.resolveWithFallback(r.OTPSendLimit, r.OTPSendWindowSeconds)
}

// OTPVerify returns the verify-code bucket (register/verify, password/reset).
// Deliberately roomier than send: typos must not lock the user for 15m.
func (r RateLimitConfig) OTPVerify() (int, int) {
	return r.resolveWithFallback(r.OTPVerifyLimit, r.OTPVerifyWindowSeconds)
}

// Login returns the login/google bucket (never shared with OTP).
func (r RateLimitConfig) Login() (int, int) {
	return r.resolveWithFallback(r.LoginLimit, r.LoginWindowSeconds)
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
}

type TracingConfig struct {
	Enabled           bool    `mapstructure:"enabled"`
	CollectorEndpoint string  `mapstructure:"collector_endpoint"`
	ServiceName       string  `mapstructure:"service_name"`
	SampleRate        float64 `mapstructure:"sample_rate"`
}

func newViper() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./config")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/deodes/")
	v.SetEnvPrefix("DEODES")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	return v
}

func Load(path string) (*Config, error) {
	v := newViper()

	setDefault(v, "server.port", 1100)
	setDefault(v, "server.host", "0.0.0.0")
	setDefault(v, "server.read_timeout", "10s")
	setDefault(v, "server.write_timeout", "30s")
	setDefault(v, "server.max_header_bytes", 1048576)
	setDefault(v, "server.tls_enabled", false)
	setDefault(v, "server.http_redirect_port", 80)
	setDefault(v, "server.shutdown_delay", "0s")
	setDefault(v, "server.shutdown_timeout", "30s")
	setDefault(v, "database.max_open_conns", 50)
	setDefault(v, "database.max_idle_conns", 10)
	setDefault(v, "database.conn_max_lifetime", "30m")
	setDefault(v, "redis.url", "redis://localhost:6379")
	setDefault(v, "redis.pool_size", 20)
	setDefault(v, "rabbitmq.url", "amqp://guest:guest@localhost:5672/")
	setDefault(v, "rabbitmq.exchange", "deodes.events")
	setDefault(v, "rabbitmq.queues.notifications", "deodes.notifications")
	setDefault(v, "rabbitmq.queues.webhooks", "deodes.webhooks")
	setDefault(v, "arc.rpc_url", "")
	setDefault(v, "arc.chain_id", uint64(5042))
	setDefault(v, "arc.usdc_contract", "")
	setDefault(v, "arc.circle_address", "")
	setDefault(v, "arc.treasury_address", "")
	setDefault(v, "keeper.escrow_address", "")
	setDefault(v, "keeper.interval_minutes", 15)
	setDefault(v, "auth.access_token_ttl", "15m")
	setDefault(v, "auth.refresh_token_ttl", "168h")
	setDefault(v, "auth.nonce_ttl", "5m")
	setDefault(v, "auth.cleanup_interval", "5m")
	setDefault(v, "auth.cleanup_jitter", "30s")
	setDefault(v, "auth.jwt_private_key_path", "./config/keys/jwt-private.pem")
	setDefault(v, "auth.jwt_public_key_path", "./config/keys/jwt-public.pem")
	setDefault(v, "security.argon2_time", 1)
	setDefault(v, "security.argon2_memory", 64*1024)
	setDefault(v, "security.argon2_threads", 4)
	setDefault(v, "brevo.api_key", "")
	setDefault(v, "google.client_id", "")
	setDefault(v, "brevo.from_email", "noreply@deodes.com")
	setDefault(v, "brevo.from_name", "Deodes")
	setDefault(v, "indexer.poll_interval", "3s")
	setDefault(v, "indexer.batch_size", 50)
	setDefault(v, "indexer.start_block", uint64(0))
	setDefault(v, "indexer.max_cursor_lag", "2m")
	setDefault(v, "indexer.reorg_window", 10)
	setDefault(v, "indexer.stall_threshold", "5m")
	// cors.allowed_origins deliberately has no default: a localhost default here
	// would be indistinguishable from an operator's explicit choice and would
	// make ResolveCORSAllowedOrigins return it in every environment (#348).
	setDefault(v, "cors.allowed_methods", []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"})
	setDefault(v, "cors.allowed_headers", DefaultCORSAllowedHeaders())
	setDefault(v, "cors.allow_credentials", true)
	setDefault(v, "cors.max_age", "24h")
	setDefault(v, "rate_limit.global", 100)
	setDefault(v, "rate_limit.authenticated", 300)
	setDefault(v, "rate_limit.auth", 10)
	setDefault(v, "rate_limit.fail_closed", true)
	setDefault(v, "rate_limit.otp_limit", 5)
	setDefault(v, "rate_limit.otp_window_seconds", 900)
	setDefault(v, "rate_limit.otp_send_limit", 5)
	setDefault(v, "rate_limit.otp_send_window_seconds", 900)
	setDefault(v, "rate_limit.otp_verify_limit", 15)
	setDefault(v, "rate_limit.otp_verify_window_seconds", 900)
	setDefault(v, "rate_limit.login_limit", 20)
	setDefault(v, "rate_limit.login_window_seconds", 900)
	setDefault(v, "rate_limit.invite_preview_limit", 30)
	setDefault(v, "rate_limit.invite_preview_window_seconds", 60)
	setDefault(v, "rate_limit.claim_limit", 20)
	setDefault(v, "rate_limit.claim_window_seconds", 3600)
	setDefault(v, "rate_limit.consent_limit", 30)
	setDefault(v, "rate_limit.consent_window_seconds", 3600)
	setDefault(v, "rate_limit.swap_limit", 10)
	setDefault(v, "rate_limit.swap_window_seconds", 60)
	setDefault(v, "rate_limit.contribute_limit", 10)
	setDefault(v, "rate_limit.contribute_window_seconds", 60)
	setDefault(v, "rate_limit.wallet_transfer_limit", 5)
	setDefault(v, "rate_limit.wallet_transfer_window_seconds", 60)
	setDefault(v, "rate_limit.referral_limit", 10)
	setDefault(v, "rate_limit.referral_window_seconds", 3600)
	setDefault(v, "rate_limit.admin_limit", 50)
	setDefault(v, "rate_limit.admin_window_seconds", 60)
	setDefault(v, "logging.level", "debug")
	setDefault(v, "logging.format", "json")
	setDefault(v, "logging.output", "stdout")
	setDefault(v, "environment", "development")
	setDefault(v, "notification.email.provider", "")
	setDefault(v, "notification.email.api_key", "")
	setDefault(v, "notification.email.from_address", "")
	setDefault(v, "notification.sms.provider", "")
	setDefault(v, "notification.sms.account_sid", "")
	setDefault(v, "notification.sms.auth_token", "")
	setDefault(v, "notification.sms.from_number", "")
	setDefault(v, "notification.push.fcm_server_key", "")
	setDefault(v, "swap.sweep_interval", "1m")
	setDefault(v, "transfer.daily_limit_usd", 1000)
	// #414: a passed proposal waits 48h before it can be executed, giving
	// members time to review it and cancel it. Set to "0" to execute immediately.
	setDefault(v, "governance.execution_timelock", "48h")
	setDefault(v, "governance.cancel_threshold_pct", "33")
	setDefault(v, "security.wallet_pepper", "")
	setDefault(v, "security.passkey_pepper", "")
	setDefault(v, "security.encryption_key", "")

	mustBindEnv(v, "environment", "DEODES_ENVIRONMENT", "NODE_ENV")
	mustBindEnv(v, "database.url", "DEODES_DATABASE_URL", "DATABASE_URL")
	mustBindEnv(v, "database.replica_url", "DEODES_DATABASE_REPLICA_URL", "DATABASE_REPLICA_URL")
	mustBindEnv(v, "arc.rpc_url", "DEODES_ARC_RPC_URL", "ARC_RPC_URL")
	mustBindEnv(v, "arc.chain_id", "DEODES_ARC_CHAIN_ID", "ARC_CHAIN_ID")
	mustBindEnv(v, "arc.usdc_contract", "DEODES_ARC_USDC_CONTRACT", "ARC_USDC_CONTRACT")
	mustBindEnv(v, "arc.circle_address", "DEODES_CIRCLE_ADDRESS", "ARC_CIRCLE_ADDRESS")
	mustBindEnv(v, "arc.treasury_address", "DEODES_TREASURY_ADDRESS", "ARC_TREASURY_ADDRESS")
	mustBindEnv(v, "keeper.escrow_address", "DEODES_KEEPER_ESCROW_ADDRESS", "KEEPER_ESCROW_ADDRESS")
	mustBindEnv(v, "keeper.interval_minutes", "DEODES_KEEPER_INTERVAL_MINUTES", "KEEPER_INTERVAL_MINUTES")
	mustBindEnv(v, "transfer.daily_limit_usd", "DEODES_TRANSFER_DAILY_LIMIT_USD")
	mustBindEnv(v, "security.wallet_pepper", "DEODES_SECURITY_WALLET_PEPPER", "DEODES_WALLET_PEPPER")
	mustBindEnv(v, "security.encryption_key", "DEODES_SECURITY_ENCRYPTION_KEY", "ENCRYPTION_KEY")
	mustBindEnv(v, "auth.jwt_private_key_pem", "DEODES_AUTH_JWT_PRIVATE_KEY_PEM", "JWT_PRIVATE_KEY")
	mustBindEnv(v, "auth.jwt_public_key_pem", "DEODES_AUTH_JWT_PUBLIC_KEY_PEM", "JWT_PUBLIC_KEY")
	mustBindEnv(v, "auth.jwt_current_kid", "DEODES_AUTH_JWT_CURRENT_KID", "JWT_CURRENT_KID")
	mustBindEnv(v, "auth.jwt_previous_private_key_pem", "DEODES_AUTH_JWT_PREVIOUS_PRIVATE_KEY_PEM", "JWT_PREVIOUS_PRIVATE_KEY", "JWT_PREVIOUS_PRIVATE_KEY_PEM")
	mustBindEnv(v, "auth.jwt_previous_public_key_pem", "DEODES_AUTH_JWT_PREVIOUS_PUBLIC_KEY_PEM", "JWT_PREVIOUS_PUBLIC_KEY", "JWT_PREVIOUS_PUBLIC_KEY_PEM")
	mustBindEnv(v, "auth.jwt_previous_kid", "DEODES_AUTH_JWT_PREVIOUS_KID", "JWT_PREVIOUS_KID")
	mustBindEnv(v, "brevo.api_key", "DEODES_BREVO_API_KEY", "DEODES_NOTIFICATION_EMAIL_APIKEY", "DEODES_EMAIL_API_KEY")
	mustBindEnv(v, "google.client_id", "DEODES_GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_ID")
	mustBindEnv(v, "brevo.from_email", "DEODES_BREVO_FROM_EMAIL", "DEODES_NOTIFICATION_EMAIL_FROM_ADDRESS")
	mustBindEnv(v, "brevo.from_name", "DEODES_BREVO_FROM_NAME", "DEODES_NOTIFICATION_EMAIL_FROM_NAME")
	mustBindEnv(v, "brevo.temp_sender_notice", "DEODES_BREVO_TEMP_SENDER_NOTICE")
	mustBindEnv(v, "cors.allowed_origins", "DEODES_CORS_ALLOWED_ORIGINS", "ALLOWED_ORIGINS")
	mustBindEnv(v, "redis.url", "DEODES_REDIS_URL", "REDIS_URL")
	mustBindEnv(v, "redis.password", "DEODES_REDIS_PASSWORD", "REDIS_PASSWORD")
	mustBindEnv(v, "auth.admin_api_key", "DEODES_AUTH_ADMIN_API_KEY", "ADMIN_API_KEY")
	mustBindEnv(v, "auth.admin_api_key_secondary", "DEODES_AUTH_ADMIN_API_KEY_SECONDARY", "ADMIN_API_KEY_SECONDARY")
	mustBindEnv(v, "server.trusted_proxies", "DEODES_TRUSTED_PROXIES", "TRUSTED_PROXIES")
	mustBindEnv(v, "rate_limit.otp_send_limit", "DEODES_RATE_LIMIT_OTP_SEND_LIMIT")
	mustBindEnv(v, "rate_limit.otp_send_window_seconds", "DEODES_RATE_LIMIT_OTP_SEND_WINDOW")
	mustBindEnv(v, "rate_limit.otp_verify_limit", "DEODES_RATE_LIMIT_OTP_VERIFY_LIMIT")
	mustBindEnv(v, "rate_limit.otp_verify_window_seconds", "DEODES_RATE_LIMIT_OTP_VERIFY_WINDOW")
	mustBindEnv(v, "rate_limit.login_limit", "DEODES_RATE_LIMIT_LOGIN_LIMIT")
	mustBindEnv(v, "rate_limit.login_window_seconds", "DEODES_RATE_LIMIT_LOGIN_WINDOW")
	mustBindEnv(v, "rate_limit.invite_preview_limit", "DEODES_RATE_LIMIT_INVITE_PREVIEW_LIMIT")
	mustBindEnv(v, "rate_limit.invite_preview_window_seconds", "DEODES_RATE_LIMIT_INVITE_PREVIEW_WINDOW")
	mustBindEnv(v, "rate_limit.claim_limit", "DEODES_RATE_LIMIT_CLAIM_LIMIT")
	mustBindEnv(v, "rate_limit.claim_window_seconds", "DEODES_RATE_LIMIT_CLAIM_WINDOW")
	mustBindEnv(v, "rate_limit.consent_limit", "DEODES_RATE_LIMIT_CONSENT_LIMIT")
	mustBindEnv(v, "rate_limit.consent_window_seconds", "DEODES_RATE_LIMIT_CONSENT_WINDOW")
	v.SetDefault("auth.admin_api_key", "")
	v.SetDefault("auth.admin_api_key_secondary", "")
	v.SetDefault("server.port", 1100)
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.read_timeout", "10s")
	v.SetDefault("server.write_timeout", "30s")
	v.SetDefault("server.max_header_bytes", 1048576)
	v.SetDefault("server.tls_enabled", false)
	v.SetDefault("server.http_redirect_port", 80)
	v.SetDefault("server.shutdown_delay", "0s")
	v.SetDefault("server.shutdown_timeout", "30s")
	v.SetDefault("server.max_body_bytes", 4*1024*1024)
	v.SetDefault("server.max_body_bytes_routes", map[string]int64{})
	// No default DATABASE_URL: the env var DATABASE_URL (mapped to DEODES_DATABASE_URL)
	// must be set explicitly. An empty URL will cause a clear startup failure rather than
	// silently connecting with plaintext credentials and SSL disabled.
	v.SetDefault("database.max_open_conns", 50)
	v.SetDefault("database.max_idle_conns", 10)
	v.SetDefault("database.conn_max_lifetime", "30m")
	v.SetDefault("redis.url", "redis://localhost:6379")
	v.SetDefault("redis.pool_size", 20)
	v.SetDefault("rabbitmq.url", "amqp://guest:guest@localhost:5672/")
	mustBindEnv(v, "rabbitmq.url", "DEODES_RABBITMQ_URL", "RABBITMQ_URL")
	v.SetDefault("rabbitmq.exchange", "deodes.events")
	v.SetDefault("rabbitmq.queues.notifications", "deodes.notifications")
	v.SetDefault("rabbitmq.queues.webhooks", "deodes.webhooks")
	v.SetDefault("auth.access_token_ttl", "15m")
	v.SetDefault("auth.refresh_token_ttl", "168h")
	v.SetDefault("auth.nonce_ttl", "5m")
	v.SetDefault("auth.cleanup_interval", "5m")
	v.SetDefault("auth.cleanup_jitter", "30s")
	v.SetDefault("brevo.api_key", "")
	v.SetDefault("brevo.from_email", "noreply@deodes.com")
	v.SetDefault("brevo.from_name", "Deodes")
	v.SetDefault("brevo.temp_sender_notice", false)
	v.SetDefault("indexer.poll_interval", "3s")
	v.SetDefault("indexer.batch_size", 50)
	v.SetDefault("indexer.max_cursor_lag", "2m")
	v.SetDefault("indexer.reorg_window", 10)
	v.SetDefault("indexer.stall_threshold", "5m")
	// See the note above: no default for cors.allowed_origins (#348).
	v.SetDefault("cors.allowed_methods", []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"})
	v.SetDefault("cors.allowed_headers", DefaultCORSAllowedHeaders())
	v.SetDefault("cors.allow_credentials", true)
	v.SetDefault("cors.max_age", "24h")
	v.SetDefault("rate_limit.global", 100)
	v.SetDefault("rate_limit.authenticated", 300)
	v.SetDefault("rate_limit.auth", 10)
	v.SetDefault("rate_limit.fail_closed", true)
	v.SetDefault("rate_limit.otp_limit", 5)
	v.SetDefault("rate_limit.otp_window_seconds", 900)
	v.SetDefault("rate_limit.otp_send_limit", 5)
	v.SetDefault("rate_limit.otp_send_window_seconds", 900)
	v.SetDefault("rate_limit.otp_verify_limit", 15)
	v.SetDefault("rate_limit.otp_verify_window_seconds", 900)
	v.SetDefault("rate_limit.login_limit", 20)
	v.SetDefault("rate_limit.login_window_seconds", 900)
	v.SetDefault("rate_limit.invite_preview_limit", 30)
	v.SetDefault("rate_limit.invite_preview_window_seconds", 60)
	v.SetDefault("rate_limit.claim_limit", 20)
	v.SetDefault("rate_limit.claim_window_seconds", 3600)
	v.SetDefault("rate_limit.consent_limit", 30)
	v.SetDefault("rate_limit.consent_window_seconds", 3600)
	v.SetDefault("rate_limit.swap_limit", 10)
	v.SetDefault("rate_limit.swap_window_seconds", 60)
	v.SetDefault("rate_limit.contribute_limit", 10)
	v.SetDefault("rate_limit.contribute_window_seconds", 60)
	v.SetDefault("rate_limit.wallet_transfer_limit", 5)
	v.SetDefault("rate_limit.wallet_transfer_window_seconds", 60)
	v.SetDefault("rate_limit.referral_limit", 10)
	v.SetDefault("rate_limit.referral_window_seconds", 3600)
	v.SetDefault("logging.level", "debug")
	v.SetDefault("logging.format", "json")
	v.SetDefault("logging.output", "stdout")
	v.SetDefault("tracing.enabled", false)
	v.SetDefault("tracing.collector_endpoint", "localhost:4317")
	v.SetDefault("tracing.service_name", "deodes-api")
	v.SetDefault("tracing.sample_rate", 0.1)
	v.SetDefault("environment", "development")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("config: reading config file: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config: unmarshaling config: %w", err)
	}

	cfg.Environment = strings.TrimSpace(v.GetString("environment"))

	var errs []string

	cfg.Database.URL = collectRequiredString(&errs, "database.url", cfg.Database.URL, "set DEODES_DATABASE_URL or DATABASE_URL")
	cfg.Security.WalletPepper = collectRequiredString(&errs, "security.wallet_pepper", cfg.Security.WalletPepper, "set DEODES_WALLET_PEPPER")
	cfg.Security.EncryptionKey = collectRequiredString(&errs, "security.encryption_key", cfg.Security.EncryptionKey, "set ENCRYPTION_KEY")

	cfg.Auth.JWTPrivateKeyPEM = collectRequiredText(&errs, cfg.Auth.JWTPrivateKeyPEM, cfg.Auth.JWTPrivateKeyPath, "auth.jwt_private_key_pem", "auth.jwt_private_key_path")
	cfg.Auth.JWTPublicKeyPEM = collectRequiredText(&errs, cfg.Auth.JWTPublicKeyPEM, cfg.Auth.JWTPublicKeyPath, "auth.jwt_public_key_pem", "auth.jwt_public_key_path")
	if cfg.Auth.JWTPreviousPrivateKeyPEM == "" && cfg.Auth.JWTPreviousPrivateKeyPath != "" {
		if content, err := os.ReadFile(cfg.Auth.JWTPreviousPrivateKeyPath); err == nil {
			cfg.Auth.JWTPreviousPrivateKeyPEM = string(content)
		}
	}
	if cfg.Auth.JWTPreviousPublicKeyPEM == "" && cfg.Auth.JWTPreviousPublicKeyPath != "" {
		if content, err := os.ReadFile(cfg.Auth.JWTPreviousPublicKeyPath); err == nil {
			cfg.Auth.JWTPreviousPublicKeyPEM = string(content)
		}
	}

	if cfg.Security.EncryptionKey != "" {
		raw, err := hex.DecodeString(strings.TrimSpace(cfg.Security.EncryptionKey))
		if err != nil || len(raw) != 32 {
			errs = append(errs, "config: security.encryption_key must be a 32-byte hex string")
		}
	}

	// Phase 2.5: every configured EVM address must be 0x + 40 hex or the
	// process fails boot here instead of signing/verifying on a typo'd
	// address. Empty means unset (chain features stay dormant in dev); the
	// shape check matches pkg/arc.ValidateAddress, duplicated (not imported)
	// so config stays dependency-free.
	for _, addr := range []struct{ name, value string }{
		{"arc.usdc_contract", cfg.Arc.USDCContract},
		{"arc.circle_address", cfg.Arc.CircleAddress},
		{"arc.treasury_address", cfg.Arc.TreasuryAddress},
		{"keeper.escrow_address", cfg.Keeper.EscrowAddress},
	} {
		if strings.TrimSpace(addr.value) != "" && !arcAddressRegexp.MatchString(strings.TrimSpace(addr.value)) {
			errs = append(errs, fmt.Sprintf("config: %s must be 0x + 40 hex chars, got %q (set DEODES_%s)", addr.name, addr.value, strings.ToUpper(strings.ReplaceAll(addr.name, ".", "_"))))
		}
	}

	if cfg.Security.Argon2Time <= 0 {
		errs = append(errs, "config: security.argon2_time must be greater than zero")
	}
	if cfg.Security.Argon2Memory <= 0 {
		errs = append(errs, "config: security.argon2_memory must be greater than zero")
	}
	if cfg.Security.Argon2Threads <= 0 {
		errs = append(errs, "config: security.argon2_threads must be greater than zero")
	}

	if cfg.Environment != "development" && strings.Contains(cfg.Database.URL, "sslmode=disable") {
		errs = append(errs, "database.url must not use sslmode=disable outside development; use sslmode=require or stronger")
	}

	if mainnetIssues := checkMainnetIssues(&cfg); len(mainnetIssues) > 0 {
		errs = append(errs, fmt.Sprintf("mainnet cutover guard: mainnet mode requires all fields to differ from testnet defaults: %v", mainnetIssues))
	}

	// CORS policy is environment specific (#348): resolve the allowed origins
	// and refuse combinations that would silently break the browser handshake.
	// Resolved before the error return so a CORS misconfiguration is reported in
	// the same single-pass list as every other invalid setting.
	cfg.CORS.AllowedOrigins = ResolveCORSAllowedOrigins(cfg.CORS.AllowedOrigins, cfg.Environment)
	for _, corsErr := range validateCORS(cfg.Environment, cfg.CORS) {
		errs = append(errs, corsErr.Error())
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config errors:\n - %s", strings.Join(errs, "\n - "))
	}

	cfg.Hot = NewHotReloader(&cfg)

	return &cfg, nil
}

func loadDotEnv() {
	for _, candidate := range []string{".env", ".env.local", "config/.env"} {
		if err := loadEnvFile(candidate); err != nil {
			panic(fmt.Errorf("config: loading %s: %w", candidate, err))
		}
	}
}

func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) || (strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func mustBindEnv(v *viper.Viper, key string, envNames ...string) {
	args := append([]string{key}, envNames...)
	if err := v.BindEnv(args...); err != nil {
		panic(fmt.Errorf("config: binding env for %s: %w", key, err))
	}
}

func setDefault(v *viper.Viper, key string, value any) {
	v.SetDefault(key, value)
}

func collectRequiredString(errs *[]string, field, value, hint string) string {
	val := strings.TrimSpace(value)
	if val == "" {
		*errs = append(*errs, fmt.Sprintf("config: %s is required (%s)", field, hint))
	}
	return val
}

func collectRequiredText(errs *[]string, current, path, field, pathField string) string {
	if strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current)
	}
	if strings.TrimSpace(path) == "" {
		*errs = append(*errs, fmt.Sprintf("config: %s is required (set it directly or configure %s)", field, pathField))
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("config: reading %s from %q: %v", field, path, err))
		return ""
	}
	return strings.TrimSpace(string(data))
}

func requireString(field, value, hint string) string {
	if strings.TrimSpace(value) == "" {
		panic(fmt.Errorf("config: %s is required (%s)", field, hint))
	}
	return strings.TrimSpace(value)
}

func loadRequiredText(current, path, field, pathField string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	if strings.TrimSpace(path) == "" {
		panic(fmt.Errorf("config: %s is required (set it directly or configure %s)", field, pathField))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Errorf("config: reading %s from %q: %w", field, path, err))
	}
	return strings.TrimSpace(string(data))
}

func validateHexKey(value string) {
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(raw) != 32 {
		panic(fmt.Errorf("config: security.encryption_key must be a 32-byte hex string"))
	}
}

func validateDuration(name string, ok bool) {
	if !ok {
		panic(fmt.Errorf("config: %s must be greater than zero", name))
	}
}

// isMainnetEnvironment reports whether the operator intends mainnet mode via
// the environment name. Matching is
// case-insensitive and trims surrounding whitespace.
func isMainnetEnvironment(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "production", "mainnet":
		return true
	default:
		return false
	}
}

func checkMainnetIssues(cfg *Config) []string {
	const (
		// Arc mainnet chain ID (docs.arc.io/arc/references/connect-to-arc).
		// The config default chain_id is 5042 (mainnet, no more testnet).
		// Kept as a literal (not pkg/arc.ChainIDMainnet) so config stays
		// dependency-free.
		arcMainnetChainID = uint64(5042)
	)

	envMainnet := isMainnetEnvironment(cfg.Environment)
	if !envMainnet {
		return nil
	}

	issues := []string{}

	// Arc is the primary chain: a mainnet process must point at Arc mainnet
	// (chain 5042) with an explicit RPC endpoint and USDC contract. The
	// defaults are mainnet-shaped (chain 5042, empty rpc/usdc), so an
	// unconfigured mainnet deploy fails here instead of signing on the
	// wrong chain.
	if cfg.Arc.ChainID != arcMainnetChainID {
		issues = append(issues, fmt.Sprintf("arc.chain_id must be %d for mainnet (got %d; set DEODES_ARC_CHAIN_ID)", arcMainnetChainID, cfg.Arc.ChainID))
	}
	if strings.TrimSpace(cfg.Arc.RPCURL) == "" {
		issues = append(issues, "arc.rpc_url must be set for mainnet mode (set DEODES_ARC_RPC_URL)")
	} else if strings.Contains(strings.ToLower(cfg.Arc.RPCURL), "testnet") {
		issues = append(issues, fmt.Sprintf("arc.rpc_url still points to testnet (%q)", cfg.Arc.RPCURL))
	}
	if strings.TrimSpace(cfg.Arc.USDCContract) == "" {
		issues = append(issues, "arc.usdc_contract must be set for mainnet mode (set DEODES_ARC_USDC_CONTRACT; see docs.arc.io/arc/references/contract-addresses.md)")
	}

	// Keeper: an empty escrow disables debiting, which is safe in development
	// but a silent misconfiguration in mainnet while the loop is enabled.
	// Reject ONLY when the keeper is enabled (interval_minutes != 0); an
	// operator that wants no auto-debits sets keeper.interval_minutes to 0.
	if strings.TrimSpace(cfg.Keeper.EscrowAddress) == "" && cfg.Keeper.IntervalMin != 0 {
		issues = append(issues, "keeper.escrow_address must be set when keeper.interval_minutes is non-zero in mainnet mode (set DEODES_KEEPER_ESCROW_ADDRESS, or set interval to 0 to disable the keeper)")
	}

	// Phase 0.6 freeze: the protocol-fee vault (arc.treasury_address) and the
	// user-debit collector (keeper.escrow_address) must be separate keys.
	// The V0 mainnet deployment used one EOA for both; commingling them means
	// one compromised key drains fees and scheduled contributions together.
	if escrow, treasury := strings.TrimSpace(cfg.Keeper.EscrowAddress), strings.TrimSpace(cfg.Arc.TreasuryAddress); escrow != "" && treasury != "" &&
		strings.EqualFold(escrow, treasury) {
		issues = append(issues, "keeper.escrow_address must differ from arc.treasury_address in mainnet mode (fee vault and debit collector must be separate keys; set DEODES_KEEPER_ESCROW_ADDRESS and DEODES_TREASURY_ADDRESS distinctly)")
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

func validateMainnetConfig(cfg *Config) {
	if issues := checkMainnetIssues(cfg); len(issues) > 0 {
		panic(fmt.Errorf("mainnet cutover guard: mainnet mode requires all fields to differ from testnet defaults: %v", issues))
	}
}

// ValidateSecrets performs pre-deploy validation that all required secrets are present.
// Call this before any sensitive operations to fail fast with clear error messages.
func (c *Config) ValidateSecrets() error {
	required := []struct {
		name  string
		value string
	}{
		{"Auth.JWTPrivateKeyPEM", c.Auth.JWTPrivateKeyPEM},
		{"Auth.JWTPublicKeyPEM", c.Auth.JWTPublicKeyPEM},
		{"Auth.AdminAPIKey", c.Auth.AdminAPIKey},
		{"Security.WalletPepper", c.Security.WalletPepper},
		{"Security.EncryptionKey", c.Security.EncryptionKey},
		{"Redis.Password", c.Redis.Password},
		{"Database.URL", c.Database.URL}, // credentials live in the DSN; there is no separate password field
	}

	var missing []string
	for _, req := range required {
		if strings.TrimSpace(req.value) == "" {
			missing = append(missing, req.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf(
			"[SECURITY CRITICAL] Missing required secrets — deploy blocked: %v. See docs/ops/SECRETS-RUNBOOK.md",
			missing,
		)
	}

	// Validate format of hex keys
	if err := validateHexKeyFormat("Security.EncryptionKey", c.Security.EncryptionKey); err != nil {
		return err
	}
	if err := validateHexKeyFormat("Security.WalletPepper", c.Security.WalletPepper); err != nil {
		return err
	}
	if err := validateHexKeyFormat("Auth.AdminAPIKey", c.Auth.AdminAPIKey); err != nil {
		return err
	}
	if c.Auth.AdminAPIKeySecondary != "" {
		if err := validateHexKeyFormat("Auth.AdminAPIKeySecondary", c.Auth.AdminAPIKeySecondary); err != nil {
			return err
		}
	}

	// Validate JWT keys are PEM format
	if !strings.HasPrefix(strings.TrimSpace(c.Auth.JWTPrivateKeyPEM), "-----BEGIN") {
		return fmt.Errorf("[SECURITY CRITICAL] Auth.JWTPrivateKeyPEM must be PEM format (-----BEGIN...)")
	}
	if !strings.HasPrefix(strings.TrimSpace(c.Auth.JWTPublicKeyPEM), "-----BEGIN") {
		return fmt.Errorf("[SECURITY CRITICAL] Auth.JWTPublicKeyPEM must be PEM format (-----BEGIN...)")
	}

	return nil
}

// ValidateOffline performs offline preflight validation:
// 1. Checks all required secrets presence and format via ValidateSecrets
// 2. Verifies that JWT private and public key PEMs can be decoded and parsed
// 3. Verifies previous JWT keys if configured
// Does NOT open any network connections.
func (c *Config) ValidateOffline() error {
	var errs []string

	if err := c.ValidateSecrets(); err != nil {
		errs = append(errs, err.Error())
	}

	if err := validatePEMKey("Auth.JWTPrivateKeyPEM", c.Auth.JWTPrivateKeyPEM); err != nil {
		errs = append(errs, err.Error())
	}
	if err := validatePEMKey("Auth.JWTPublicKeyPEM", c.Auth.JWTPublicKeyPEM); err != nil {
		errs = append(errs, err.Error())
	}
	if c.Auth.JWTPreviousPrivateKeyPEM != "" {
		if err := validatePEMKey("Auth.JWTPreviousPrivateKeyPEM", c.Auth.JWTPreviousPrivateKeyPEM); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.Auth.JWTPreviousPublicKeyPEM != "" {
		if err := validatePEMKey("Auth.JWTPreviousPublicKeyPEM", c.Auth.JWTPreviousPublicKeyPEM); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("offline validation errors:\n - %s", strings.Join(errs, "\n - "))
	}
	return nil
}

func validatePEMKey(name, pemStr string) error {
	trimmed := strings.TrimSpace(pemStr)
	if trimmed == "" {
		return fmt.Errorf("%s is empty", name)
	}
	block, _ := pem.Decode([]byte(trimmed))
	if block == nil {
		return fmt.Errorf("%s: failed to decode PEM block", name)
	}
	if len(block.Bytes) == 0 {
		return fmt.Errorf("%s: PEM block bytes are empty", name)
	}
	return nil
}

// validateHexKeyFormat ensures a secret is 32-byte hex (64 hex chars).
func validateHexKeyFormat(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "-----BEGIN") && len(trimmed) > 0 {
		// Only validate hex keys (not PEM keys like JWT keys)
		if !strings.ContainsAny(strings.ToLower(trimmed), "ghijklmnopqrstuvwxyz") {
			raw, err := hex.DecodeString(trimmed)
			if err != nil || len(raw) != 32 {
				return fmt.Errorf("[SECURITY CRITICAL] %s must be 32-byte hex (got %d chars)", name, len(trimmed))
			}
		}
	}
	return nil
}
