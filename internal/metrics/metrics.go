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

func Enable() { enabled.Store(true) }

func Enabled() bool { return enabled.Load() }

func BindQueueGauges(used, pending, queued func() int) {
	setAtomicFn(&queueUsedFn, used)
	setAtomicFn(&queuePendingFn, pending)
	setAtomicFn(&queueQueuedFn, queued)
}

func BindSessionsGauge(length func() int) {
	setAtomicFn(&sessionsFn, length)
}

func Handler() http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

func SessionCreated(protocol string) {
	if !enabled.Load() {
		return
	}
	sessionCreated.WithLabelValues(protocol).Inc()
}

func SessionEnded(protocol, reason string, durationSeconds float64) {
	if !enabled.Load() {
		return
	}
	sessionEnded.WithLabelValues(protocol, reason).Inc()
	sessionDuration.WithLabelValues(protocol).Observe(durationSeconds)
}

func AuthFailure(mode string) {
	if !enabled.Load() {
		return
	}
	authFailures.WithLabelValues(mode).Inc()
}

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
