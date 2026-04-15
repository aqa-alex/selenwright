// Package metrics exposes a small Prometheus-compatible surface for
// operational observability. It is intentionally minimal: no traces,
// no push gateways, no OTEL exporters — just the counters, gauges
// and histograms operators typically want to alert on.
//
// The package is silent until Enable() is called from main.init after
// the -enable-metrics flag is parsed; until then every Register and
// observation path is a no-op, so importing this package does not
// pull a Prometheus registry into processes that do not want it.
package metrics

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	enabled atomic.Bool
	reg     = prometheus.NewRegistry()

	sessionCreated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "selenwright_sessions_created_total",
		Help: "Number of sessions successfully created, by protocol.",
	}, []string{"protocol"})

	sessionEnded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "selenwright_sessions_ended_total",
		Help: "Number of sessions ended, by protocol and reason (client_close, upstream_close, timeout, shutdown).",
	}, []string{"protocol", "reason"})

	sessionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "selenwright_session_duration_seconds",
		Help:    "Session lifetime from first byte to close, by protocol.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 14), // 1s .. 4h
	}, []string{"protocol"})

	authFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "selenwright_auth_failures_total",
		Help: "Authentication failures, by mode (embedded, trusted-proxy, source-trust).",
	}, []string{"mode"})

	capsRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "selenwright_caps_rejected_total",
		Help: "Capabilities requests rejected by the caps sanitizer.",
	})

	queueUsedFn    atomic.Pointer[func() float64]
	queuePendingFn atomic.Pointer[func() float64]
	queueQueuedFn  atomic.Pointer[func() float64]
	sessionsFn     atomic.Pointer[func() float64]
)

func init() {
	reg.MustRegister(sessionCreated)
	reg.MustRegister(sessionEnded)
	reg.MustRegister(sessionDuration)
	reg.MustRegister(authFailures)
	reg.MustRegister(capsRejected)

	// Pre-initialize the common label combinations so operators see
	// an explicit zero in /metrics before the first observation. This
	// lets Prometheus alerting rules count from zero without waiting
	// for the first event to create the series.
	for _, proto := range []string{"selenium", "playwright"} {
		sessionCreated.WithLabelValues(proto)
		sessionDuration.WithLabelValues(proto)
		for _, reason := range []string{"cancel", "client", "upstream", "timeout", "shutdown", "close"} {
			sessionEnded.WithLabelValues(proto, reason)
		}
	}
	for _, mode := range []string{"embedded", "trusted-proxy", "none", "source-trust"} {
		authFailures.WithLabelValues(mode)
	}

	reg.MustRegister(gaugeFuncFromAtomic("selenwright_queue_used",
		"Number of active container slots currently held by live sessions.",
		&queueUsedFn))
	reg.MustRegister(gaugeFuncFromAtomic("selenwright_queue_pending",
		"Number of requests waiting for a slot.",
		&queuePendingFn))
	reg.MustRegister(gaugeFuncFromAtomic("selenwright_queue_queued",
		"Number of requests that have been admitted past the -limit gate.",
		&queueQueuedFn))
	reg.MustRegister(gaugeFuncFromAtomic("selenwright_sessions_active",
		"Number of sessions currently in the in-memory session map.",
		&sessionsFn))
}

// Enable activates metric observation. Until this is called (or after
// Disable), every Observe/Inc is a no-op and /metrics scrapes still
// return a valid but empty snapshot. Enable is idempotent.
func Enable() { enabled.Store(true) }

// Enabled reports whether metrics collection is currently active.
func Enabled() bool { return enabled.Load() }

// BindQueueGauges wires the Queue's Used/Pending/Queued values into
// the three queue gauges. The metrics package stays decoupled from
// the protect package by accepting plain functions.
func BindQueueGauges(used, pending, queued func() int) {
	setAtomicFn(&queueUsedFn, used)
	setAtomicFn(&queuePendingFn, pending)
	setAtomicFn(&queueQueuedFn, queued)
}

// BindSessionsGauge wires the session map's Len into the sessions
// gauge. Same decoupling rationale as BindQueueGauges.
func BindSessionsGauge(length func() int) {
	setAtomicFn(&sessionsFn, length)
}

// Handler returns the HTTP handler that serves the exposition
// format. Always safe to register on the router; when Enabled()
// returns false it produces an empty (but valid) response.
func Handler() http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// SessionCreated increments the created counter for the given
// protocol ("selenium" or "playwright"). No-op when disabled.
func SessionCreated(protocol string) {
	if !enabled.Load() {
		return
	}
	sessionCreated.WithLabelValues(protocol).Inc()
}

// SessionEnded increments the ended counter and records duration.
// reason is a short tag such as "client_close", "upstream_close",
// "timeout", "shutdown". durationSeconds is the session lifetime.
func SessionEnded(protocol, reason string, durationSeconds float64) {
	if !enabled.Load() {
		return
	}
	sessionEnded.WithLabelValues(protocol, reason).Inc()
	sessionDuration.WithLabelValues(protocol).Observe(durationSeconds)
}

// AuthFailure increments the auth-failure counter for the given
// mode (embedded, trusted-proxy, source-trust).
func AuthFailure(mode string) {
	if !enabled.Load() {
		return
	}
	authFailures.WithLabelValues(mode).Inc()
}

// CapsRejected increments the caps-rejection counter.
func CapsRejected() {
	if !enabled.Load() {
		return
	}
	capsRejected.Inc()
}

func setAtomicFn(slot *atomic.Pointer[func() float64], fn func() int) {
	if fn == nil {
		slot.Store(nil)
		return
	}
	wrap := func() float64 { return float64(fn()) }
	slot.Store(&wrap)
}

func gaugeFuncFromAtomic(name, help string, slot *atomic.Pointer[func() float64]) prometheus.Collector {
	return prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: name,
		Help: help,
	}, func() float64 {
		if p := slot.Load(); p != nil {
			return (*p)()
		}
		return 0
	})
}
