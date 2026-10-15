// Package metrics owns the service's Prometheus instrumentation: HTTP request
// counters and latencies, financial-operation counters and latencies, and
// database pool gauges. Each Metrics has its own registry, so handlers built
// side by side (as in tests) never share or double-register collectors.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics records and serves the service's metrics.
type Metrics struct {
	registry *prometheus.Registry

	requests      *prometheus.CounterVec
	requestTime   *prometheus.HistogramVec
	operations    *prometheus.CounterVec
	operationTime *prometheus.HistogramVec
}

// New returns Metrics whose pool gauges read from pool.
func New(pool *pgxpool.Pool) *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests handled, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		requestTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency, by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wallet_financial_operations_total",
			Help: "Deposits, Withdrawals and Transfers, by type, outcome (success, rejected, error) and Rejection code.",
		}, []string{"type", "outcome", "reason"}),
		operationTime: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wallet_financial_operation_duration_seconds",
			Help:    "Latency of Deposits, Withdrawals and Transfers, by type.",
			Buckets: prometheus.DefBuckets,
		}, []string{"type"}),
	}
	m.registry.MustRegister(m.requests, m.requestTime, m.operations, m.operationTime)
	registerPool(m.registry, pool)
	return m
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveRequest records one handled HTTP request. route must be a bounded
// route pattern, never a raw path.
func (m *Metrics) ObserveRequest(method, route string, status int, d time.Duration) {
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.requestTime.WithLabelValues(method, route).Observe(d.Seconds())
}

// ObserveOperation records one finished Deposit, Withdrawal or Transfer.
func (m *Metrics) ObserveOperation(typ, outcome, reason string, d time.Duration) {
	m.operations.WithLabelValues(typ, outcome, reason).Inc()
	m.operationTime.WithLabelValues(typ).Observe(d.Seconds())
}

// registerPool exposes the pgx pool's live statistics. They are read at
// scrape time, so no goroutine polls the pool.
func registerPool(r *prometheus.Registry, pool *pgxpool.Pool) {
	r.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_pool_acquired_connections",
			Help: "Connections currently checked out of the pool.",
		}, func() float64 { return float64(pool.Stat().AcquiredConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_pool_idle_connections",
			Help: "Idle connections in the pool.",
		}, func() float64 { return float64(pool.Stat().IdleConns()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "db_pool_total_connections",
			Help: "Connections in the pool, in any state.",
		}, func() float64 { return float64(pool.Stat().TotalConns()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "db_pool_acquire_wait_seconds_total",
			Help: "Cumulative time requests spent waiting for a connection because the pool had none free.",
		}, func() float64 { return pool.Stat().EmptyAcquireWaitTime().Seconds() }),
	)
}
