// PR #3 regression tests for Cross-Site WebSocket Hijacking defenses.
// Each WebSocket-bearing path is exercised with three Origin states:
//
//   - allowed origin → upgrade succeeds (101 / status acceptable)
//   - disallowed origin → 403 via the upgrader's CheckOrigin callback
//   - missing origin → upgrade succeeds, mirroring native CLI clients

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

// withStrictOrigin temporarily replaces originChecker with one that only
// accepts the supplied origins. Restores the prior checker on cleanup.
func withStrictOrigin(t *testing.T, allowed ...string) {
	t.Helper()
	prev := app.originChecker
	c, err := protect.NewOriginChecker(allowed)
	assert.NoError(t, err)
	app.originChecker = c
	t.Cleanup(func() { app.originChecker = prev })
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

// TestVNCRejectsForeignOrigin — Origin rejected in the upgrader's CheckOrigin.
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

// TestVNCAllowsAbsentOrigin — native CLI clients (no Origin header) must
// keep working after PR #14's migration to the gorilla upgrader. The
// originChecker treats a missing Origin as legitimate; the upgrader's
// CheckOrigin honors that same contract.
func TestVNCAllowsAbsentOrigin(t *testing.T) {
	withStrictOrigin(t, "https://ci.example.com")
	resp := upgradeRequest(t, srv.URL+paths.VNC+"unknown-session", "")
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"absent Origin must pass the gate so native clients keep working")
}

// TestPermissiveModeStillAccepts — empty allow-list keeps backward compat
// for users who haven't configured -allowed-origins yet. Documented as
// "warned but not blocked" in main.init.
func TestPermissiveModeStillAccepts(t *testing.T) {
	withStrictOrigin(t /* no entries -> permissive */)
	assert.True(t, app.originChecker.AllowsAll())
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
