package observe

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// RequestID is the outermost per-request middleware. It mints/keeps an
// X-Request-ID (echoed on the response), stores it in c.Locals("request_id"),
// and includes it in access logs and error logging so one request is traceable
// end to end.
func RequestID(c *fiber.Ctx) error {
	id := c.Get("X-Request-ID")
	if id == "" {
		id = uuid.NewString()
	}
	c.Locals("request_id", id)
	c.Set("X-Request-ID", id)
	return c.Next()
}

// AccessLog emits one structured JSON line per request (log/slog) with the
// fields Phase 7 requires: request id, resolved tenant id, latency. It runs
// outside the auth middlewares, so by the time innner handlers return, tenant_id
// and role are already in c.Locals.
func AccessLog(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	slog.Info("http_request",
		"request_id", c.Locals("request_id"),
		"method", c.Method(),
		"path", c.Path(),
		"status", c.Response().StatusCode(),
		"latency_ms", time.Since(start).Milliseconds(),
		"tenant_id", c.Locals("tenant_id"),
		"role", c.Locals("role"),
	)
	return err
}

// Metrics is the Phase 7 observability surface: Prometheus counters plus a
// Fiber middleware that instruments each request and a Handler serving the
// text exposition over /metrics.
type Metrics struct {
	requests *prometheus.CounterVec   // method, route, status
	latency  *prometheus.HistogramVec // method, route
}

// NewMetrics registers the HTTP instrumentation and the DB pool gauges (the
// pool's statistics are captured per scrape via StatSnapshot so we own no
// goroutine). Named opencode-side: shopkeet_http_requests_total,
// shopkeet_http_request_duration_seconds, shopkeet_db_pool_{max,acquired,idle,total}.
func NewMetrics(pool *pgxpool.Pool) *Metrics {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "shopkeet",
			Name:      "http_requests_total",
			Help:      "HTTP requests handled by route, method and status.",
		}, []string{"method", "route", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "shopkeet",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency by route and method.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	prometheus.MustRegister(m.requests, m.latency)

	// DB pool gauges sampled at scrape time (no ticker to keep alive).
	poolGauges := map[string]struct {
		help string
		val  func() float64
	}{
		"shopkeet_db_pool_max": {"Maximum connections the pool can open",
			func() float64 { return float64(pool.Stat().MaxConns()) }},
		"shopkeet_db_pool_acquired": {"Connections currently checked out",
			func() float64 { return float64(pool.Stat().AcquiredConns()) }},
		"shopkeet_db_pool_idle": {"Idle connections waiting in the pool",
			func() float64 { return float64(pool.Stat().IdleConns()) }},
		"shopkeet_db_pool_total": {"Total constructed connections",
			func() float64 { return float64(pool.Stat().TotalConns()) }},
	}
	for fullName, g := range poolGauges {
		name := fullName[len("shopkeet_"):]
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: "shopkeet", Name: name, Help: g.help}, g.val,
		))
	}
	return m
}

// Middleware records count + latency for a request after it completes. The
// route is the matched Fiber pattern (e.g. /api/v1/products/:id), not the raw
// path, so cardinality stays bounded; unmatched routes fall back to their path.
func (m *Metrics) Middleware(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	status := c.Response().StatusCode()
	route := ""
	if c.Route() != nil {
		route = c.Route().Path
	}
	if route == "" {
		route = c.Path()
	}
	m.requests.WithLabelValues(c.Method(), route, itoa(status)).Inc()
	m.latency.WithLabelValues(c.Method(), route).Observe(time.Since(start).Seconds())
	return err
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

// Handler serves the Prometheus text exposition (Content-Type
// text/plain; version=0.0.4). main.go gates it behind a token so /metrics is
// not publicly exposed (docs/api-reference.md §Platform).
func (m *Metrics) Handler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"error": "metrics gather failed"})
		}
		c.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		enc := expfmt.NewEncoder(c, expfmt.NewFormat(expfmt.TypeTextPlain))
		for _, f := range mfs {
			if err := enc.Encode(f); err != nil {
				return err
			}
		}
		return nil
	}
}
