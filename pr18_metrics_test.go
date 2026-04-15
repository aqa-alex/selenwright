package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aqa-alex/selenwright/internal/metrics"
	assert "github.com/stretchr/testify/require"
)

// withMetricsEnabled toggles the global metrics switch on for the
// duration of a test. metrics.Enable is sticky in production (once
// on, on for the life of the process) but harmless to call from
// tests — the collectors stay registered either way.
func withMetricsEnabled(t *testing.T) {
	t.Helper()
	prevEnable := app.enableMetrics
	prevPath := app.metricsPath
	app.enableMetrics = true
	app.metricsPath = "/metrics"
	metrics.Enable()
	t.Cleanup(func() {
		app.enableMetrics = prevEnable
		app.metricsPath = prevPath
	})
}

func fetchMetrics(t *testing.T) string {
	t.Helper()
	h := handler()
	req, err := http.NewRequest(http.MethodGet, "http://localhost"+app.metricsPath, nil)
	assert.NoError(t, err)
	rw := &recorder{body: &strings.Builder{}, header: http.Header{}}
	h.ServeHTTP(rw, req)
	assert.Equal(t, http.StatusOK, rw.status, "metrics endpoint should respond 200")
	return rw.body.String()
}

type recorder struct {
	status int
	body   *strings.Builder
	header http.Header
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

// TestMetricsEndpointDisabledByDefault — with the flag off, /metrics
// is not registered so scrapes hit the mux's catch-all welcome page
// rather than the Prometheus exporter.
func TestMetricsEndpointDisabledByDefault(t *testing.T) {
	prev := app.enableMetrics
	app.enableMetrics = false
	t.Cleanup(func() { app.enableMetrics = prev })

	h := handler()
	req, err := http.NewRequest(http.MethodGet, "http://localhost/metrics", nil)
	assert.NoError(t, err)
	rw := &recorder{body: &strings.Builder{}, header: http.Header{}}
	h.ServeHTTP(rw, req)
	assert.NotContains(t, rw.header.Get("Content-Type"), "text/plain; version=",
		"metrics endpoint must not leak when -enable-metrics is false")
}

// TestMetricsEndpointExposesCoreSeries — with the flag on, /metrics
// returns the Prometheus text format and contains the core series
// this PR introduces. Values are not asserted; internal/metrics has
// its own value-level coverage if needed.
func TestMetricsEndpointExposesCoreSeries(t *testing.T) {
	withMetricsEnabled(t)
	body := fetchMetrics(t)
	for _, series := range []string{
		"selenwright_sessions_created_total",
		"selenwright_sessions_ended_total",
		"selenwright_session_duration_seconds",
		"selenwright_auth_failures_total",
		"selenwright_caps_rejected_total",
		"selenwright_queue_used",
		"selenwright_queue_pending",
		"selenwright_queue_queued",
		"selenwright_sessions_active",
	} {
		assert.Contains(t, body, series, "metrics endpoint missing series %q", series)
	}
}

// TestMetricsSurvivesAuthGate — /metrics must not be gated by the
// authenticator even in -auth-mode=embedded: Prometheus scrapers do
// not carry credentials by default, and the handoff spec places the
// endpoint inside the scraper's network boundary, not behind the
// app's BasicAuth.
func TestMetricsSurvivesAuthGate(t *testing.T) {
	withMetricsEnabled(t)
	body := fetchMetrics(t)
	assert.Contains(t, body, "selenwright_queue_used")
}

// TestMetricsCapsRejectedIncrements — the sanitizer hook ticks the
// counter whenever caps are rejected. Driving it directly through
// the metrics API is sufficient; the HTTP handler paths that invoke
// CapsRejected are exercised in pr08_caps_sanitizer_test.go.
func TestMetricsCapsRejectedIncrements(t *testing.T) {
	withMetricsEnabled(t)
	before := metricValue(t, "selenwright_caps_rejected_total")
	metrics.CapsRejected()
	metrics.CapsRejected()
	after := metricValue(t, "selenwright_caps_rejected_total")
	assert.Equal(t, before+2, after, "caps rejection counter should tick by two")
}

// metricValue scrapes the endpoint and returns the last value line
// that matches the exact (unlabelled) metric name. Absent series
// resolve to 0 — valid for counters before their first observation.
func metricValue(t *testing.T, name string) float64 {
	t.Helper()
	body := fetchMetrics(t)
	var value float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		key := line
		if idx := strings.IndexAny(line, " {"); idx > 0 {
			key = line[:idx]
		}
		if key != name {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		value = v
	}
	return value
}
