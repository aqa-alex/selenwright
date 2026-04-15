// Modified by [Aleksander R], 2026: PR #3 — WebSocket CheckOrigin allow-list
//
// Regression tests for Cross-Site WebSocket Hijacking defenses. We exercise
// each WebSocket-bearing path with three Origin states:
//
//   - allowed origin → upgrade succeeds (101 / status acceptable)
//   - disallowed origin → 403 (gorilla returns it via CheckOrigin failure;
//     the legacy x/net/websocket path returns it via the HandlerWrap
//     middleware)
//   - missing origin → upgrade succeeds, mirroring native CLI clients

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

// withStrictOrigin temporarily replaces originChecker with one that only
// accepts the supplied origins. Restores the prior checker on cleanup.
func withStrictOrigin(t *testing.T, allowed ...string) {
	t.Helper()
	prev := originChecker
	c, err := protect.NewOriginChecker(allowed)
	assert.NoError(t, err)
	originChecker = c
	t.Cleanup(func() { originChecker = prev })
}

// upgradeRequest constructs a WebSocket Upgrade request without going
// through a real client, so we can manipulate Origin freely. Returns the
// raw response so tests can assert status codes from the upgrade attempt
// itself, before any post-upgrade WS framing.
func upgradeRequest(t *testing.T, target, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	assert.NoError(t, err)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	return resp
}

// TestVNCRejectsForeignOrigin — x/net/websocket path defended via HandlerWrap.
func TestVNCRejectsForeignOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"some-session-id", "https://evil.example.com")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"foreign Origin must be rejected before VNC handshake")
}

// TestVNCAllowsConfiguredOrigin — positive case for the same endpoint.
// We don't have a real VNC backend, so success is "we got past the Origin
// gate"; the actual websocket handler then runs and produces its own
// response (typically a session-not-found message). The key invariant:
// status is NOT 403.
func TestVNCAllowsConfiguredOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"unknown-session", "https://ci.example.com")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"allowed Origin must pass the gate")
}

// TestLogsRejectsForeignOrigin — same wrap as VNC.
func TestLogsRejectsForeignOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.Logs+"some-session-id", "https://evil.example.com")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// Note: end-to-end coverage of the gorilla CheckOrigin path (used by
// /playwright/ and /devtools/ via wsproxy.proxyWebSocket) is provided by
// protect/origin_test.go which exercises OriginChecker.Check directly.
// Wiring an httptest harness here would require standing up a fake
// Playwright upstream and a session map fixture — out of proportion for
// what is a one-line CheckOrigin: originChecker.Check substitution.

// TestOriginGateAllowsAbsentOrigin verifies our gateOrigin middleware
// itself does not reject a missing Origin (since browsers always send
// Origin on WS upgrade, an absent value identifies a native CLI client
// which cannot host CSWSH).
//
// We exercise the gate against a one-off endpoint registered for the
// test instead of /vnc/ or /logs/, because both legacy x/net/websocket
// endpoints additionally enforce Origin presence inside the library
// itself (handshake.go returns 403 when Origin is absent). PR #14 will
// migrate them to gorilla/websocket where the behavior matches our gate.
func TestOriginGateAllowsAbsentOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	gated := gateOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	mux := http.NewServeMux()
	mux.Handle("/probe", gated)
	probeSrv := httptest.NewServer(mux)
	t.Cleanup(probeSrv.Close)

	resp := upgradeRequest(t, probeSrv.URL+"/probe", "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusTeapot, resp.StatusCode,
		"absent Origin must pass the gate so native clients keep working")
}

// TestPermissiveModeStillAccepts — empty allow-list keeps backward compat
// for users who haven't configured -allowed-origins yet. Documented as
// "warned but not blocked" in main.init.
func TestPermissiveModeStillAccepts(t *testing.T) {
	withStrictOrigin(t /* no entries -> permissive */)
	assert.True(t, originChecker.AllowsAll())
	resp := upgradeRequest(t, srv.URL+paths.VNC+"x", "https://anywhere.example.com")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}

// TestSplitCSV — small unit check on the helper used to parse the flag.
func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , ,b ", []string{"a", "b"}},
		{",,,", nil},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.in, ",", "_"), func(t *testing.T) {
			assert.Equal(t, tc.want, splitCSV(tc.in))
		})
	}
}
