package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "deodes_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPRequestsTotal = HTTPRequests

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "deodes_http_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	HTTPLatencySeconds = HTTPDuration

	HTTPErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "deodes_http_errors_total",
		Help: "Total HTTP error responses (status >= 400)",
	}, []string{"method", "path", "status"})

	EmailsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "deodes_emails_total",
		Help: "Total transactional email attempts by kind (otp/recovery/backup_codes/notification) and result (sent/rejected/failed/cancelled/skipped_dev_mode)",
	}, []string{"kind", "result"})

	WSActiveConnections = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "deodes_websocket_active_connections",
		Help: "Current number of active WebSocket connections",
	})

	AdminKeyRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "deodes_admin_key_requests_total",
		Help: "Total admin API key authenticated requests by key identity (primary/secondary)",
	}, []string{"identity"})

	DBPoolUtilization = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "deodes_db_pool_utilization",
		Help: "Database connection pool stats and utilization",
	}, []string{"type"})

	DBPoolWaitTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_db_pool_wait_total",
		Help: "Total number of times a caller had to wait for a free database connection",
	})

	DBPoolWaitSecondsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_db_pool_wait_seconds_total",
		Help: "Total time callers spent waiting to acquire a database connection",
	})

	DBIdleInTransaction = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "deodes_db_idle_in_transaction_connections",
		Help: "Connections idle in transaction longer than the leak threshold",
	})

	DBPoolAlertsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "deodes_db_pool_alerts_total",
		Help: "Database pool alerts raised (pool_saturated, idle_in_transaction)",
	}, []string{"kind"})

	RPCLatencySeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "deodes_rpc_latency_seconds",
		Help:    "RPC call latency in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "method"})

	CirclesCreated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_circles_created_total",
		Help: "Total circles created",
	})

	ActiveUsers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "deodes_active_users",
		Help: "Number of active users",
	})

	WSStaleConnectionsClosedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_websocket_stale_connections_closed_total",
		Help: "Total stale WebSocket connections closed due to missed heartbeat pongs",
	})

	WSDroppedMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_websocket_dropped_messages_total",
		Help: "Total WebSocket messages dropped due to slow client backpressure",
	})

	WSSlowClientsDisconnectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "deodes_websocket_slow_clients_disconnected_total",
		Help: "Total slow WebSocket clients disconnected due to backpressure overflow",
	})
)
