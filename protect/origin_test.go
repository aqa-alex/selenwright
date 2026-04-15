// Modified by [Aleksander R], 2026: PR #3 — WebSocket CheckOrigin allow-list

package protect

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewOriginChecker_EmptyMeansAllowAll(t *testing.T) {
	c, err := NewOriginChecker(nil)
	require.NoError(t, err)
	require.True(t, c.AllowsAll())
	require.True(t, c.Check(reqWithOrigin("https://evil.example.com")))
}

func TestNewOriginChecker_StarAllowsAll(t *testing.T) {
	c, err := NewOriginChecker([]string{"*"})
	require.NoError(t, err)
	require.True(t, c.AllowsAll())
	require.True(t, c.Check(reqWithOrigin("https://anything")))
}

func TestNewOriginChecker_AllowsExactMatch(t *testing.T) {
	c, err := NewOriginChecker([]string{"https://ci.example.com"})
	require.NoError(t, err)
	require.False(t, c.AllowsAll())
	require.True(t, c.Check(reqWithOrigin("https://ci.example.com")))
	require.False(t, c.Check(reqWithOrigin("https://evil.example.com")))
}

func TestNewOriginChecker_NormalizesCaseAndDefaultPort(t *testing.T) {
	c, err := NewOriginChecker([]string{"https://Ci.Example.Com:443"})
	require.NoError(t, err)

	cases := []string{
		"https://ci.example.com",
		"https://ci.example.com:443",
		"https://CI.example.com",
		"HTTPS://CI.EXAMPLE.COM",
	}
	for _, origin := range cases {
		t.Run(origin, func(t *testing.T) {
			require.True(t, c.Check(reqWithOrigin(origin)))
		})
	}
}

func TestNewOriginChecker_NonDefaultPortRequiresMatch(t *testing.T) {
	c, err := NewOriginChecker([]string{"https://ci.example.com:8443"})
	require.NoError(t, err)
	require.True(t, c.Check(reqWithOrigin("https://ci.example.com:8443")))
	require.False(t, c.Check(reqWithOrigin("https://ci.example.com")))
}

func TestNewOriginChecker_RejectsInvalidEntry(t *testing.T) {
	cases := []string{"not-a-url", "/no/scheme", "://missing-scheme"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := NewOriginChecker([]string{in})
			require.Error(t, err)
		})
	}
}

// TestCheck_AbsentOriginAllowed documents the deliberate decision to allow
// requests without an Origin header — they cannot be CSWSH victims because
// no browser is involved (browsers always set Origin on WS upgrade).
func TestCheck_AbsentOriginAllowed(t *testing.T) {
	c, err := NewOriginChecker([]string{"https://ci.example.com"})
	require.NoError(t, err)
	req, _ := http.NewRequest(http.MethodGet, "/playwright/chromium/1.0.0", nil)
	require.True(t, c.Check(req), "native non-browser clients omit Origin and must continue to work")
}

func TestCheck_NilReceiverIsSafe(t *testing.T) {
	var c *OriginChecker
	require.False(t, c.Check(reqWithOrigin("https://anywhere")))
}

func reqWithOrigin(origin string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Origin", origin)
	return r
}
