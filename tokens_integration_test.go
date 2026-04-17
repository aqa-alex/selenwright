package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	assert "github.com/stretchr/testify/require"
)

// newTestTokenStore builds a TokenStore backed by a temp file. Cleanup stops
// the flush goroutine and is registered with t.
func newTestTokenStore(t *testing.T) *protect.TokenStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens.json")
	ts, err := protect.NewTokenStore(path)
	assert.NoError(t, err)
	t.Cleanup(ts.Stop)
	return ts
}

// withTokenStore swaps the global app.tokenStore and restores the previous
// value at test end.
func withTokenStore(t *testing.T, ts *protect.TokenStore) {
	t.Helper()
	prev := app.tokenStore
	app.tokenStore = ts
	t.Cleanup(func() { app.tokenStore = prev })
}

// withAuthModeFlag overrides app.authModeFlag for a single test. requireAdmin
// short-circuits in ModeNone, so admin-gated tests have to flip this off.
func withAuthModeFlag(t *testing.T, mode string) {
	t.Helper()
	prev := app.authModeFlag
	app.authModeFlag = mode
	t.Cleanup(func() { app.authModeFlag = prev })
}

func TestTokenAuth_BearerAccepts(t *testing.T) {
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	_, plaintext, err := ts.Create("alice", "laptop", nil)
	assert.NoError(t, err)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		QueryAllowedPath: isTokenQueryAllowedPath,
		// No fallback: token is the only credential path.
	})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"valid Bearer token must clear the auth middleware; downstream handler may respond with any other status")
}

func TestTokenAuth_BearerRejectsInvalid(t *testing.T) {
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set("Authorization", "Bearer "+protect.TokenPlaintextPrefix+"deadbeefdeadbeefdeadbeefdeadbeef")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"malformed Bearer token must fail hard — no fallthrough to other authenticators")
}

func TestTokenAuth_QueryParamOnWSPath(t *testing.T) {
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	_, plaintext, err := ts.Create("alice", "laptop", nil)
	assert.NoError(t, err)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	// /playwright/ is in the WS-upgrade whitelist — token may be passed via query.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.Playwright+"chromium/latest?token="+plaintext, nil)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"query token on WS-upgrade path must clear auth")
}

func TestTokenAuth_QueryParamRejectedOffWhitelist(t *testing.T) {
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	_, plaintext, err := ts.Create("alice", "laptop", nil)
	assert.NoError(t, err)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	// /api/admin/tokens is NOT a WS-upgrade path — query fallback must not apply.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+tokensAPIPath+"?token="+plaintext, nil)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"query-param auth must be confined to the WS-upgrade whitelist; REST endpoints require a Bearer header")
}

func TestTokenAuth_RESTEndpoint_NonAdmin403(t *testing.T) {
	withAuthModeFlag(t, string(protect.ModeEmbedded))
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	_, plaintext, err := ts.Create("bob", "ci", nil)
	assert.NoError(t, err)
	// bob is NOT in the admins set.
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		Admins:           map[string]struct{}{"alice": {}},
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	body := bytes.NewBufferString(`{"owner":"whoever","name":"x"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+tokensAPIPath, body)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"non-admin token holder must not be able to mint new tokens")
}

func TestTokenAuth_RESTEndpoint_AdminCanCreateAndRevoke(t *testing.T) {
	withAuthModeFlag(t, string(protect.ModeEmbedded))
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)

	// Seed an htpasswd-less environment: no known-users list means owner
	// validation in createTokenHandler is skipped, letting the admin mint
	// tokens for whatever name it passes (matches the dev-fallback flow).
	prevHtpasswd := app.htpasswdAuth
	app.htpasswdAuth = nil
	t.Cleanup(func() { app.htpasswdAuth = prevHtpasswd })

	_, adminToken, err := ts.Create("root", "admin-laptop", nil)
	assert.NoError(t, err)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		Admins:           map[string]struct{}{"root": {}},
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	// Create token for alice.
	body := bytes.NewBufferString(`{"owner":"alice","name":"ci-laptop"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+tokensAPIPath, body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var created createTokenResponse
	raw, _ := io.ReadAll(resp.Body)
	assert.NoError(t, json.Unmarshal(raw, &created))
	assert.NotEmpty(t, created.ID)
	assert.NotEmpty(t, created.Token)

	// Revoke it.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+tokensAPIPrefix+created.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+adminToken)
	delResp, err := http.DefaultClient.Do(delReq)
	assert.NoError(t, err)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusOK, delResp.StatusCode)

	// The revoked token must no longer authenticate.
	if _, _, _, ok := ts.Lookup(created.Token); ok {
		t.Fatalf("revoked token still resolves in store")
	}
}

func TestTokenAuth_CreateHandler_RejectsUnknownOwner(t *testing.T) {
	withAuthModeFlag(t, string(protect.ModeEmbedded))
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)

	// Install an htpasswd authenticator with only alice.
	htpasswd, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), []string{"alice"})
	assert.NoError(t, err)
	prevHtpasswd := app.htpasswdAuth
	app.htpasswdAuth = htpasswd
	t.Cleanup(func() { app.htpasswdAuth = prevHtpasswd })

	_, adminToken, err := ts.Create("alice", "admin-laptop", nil)
	assert.NoError(t, err)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		Admins:           map[string]struct{}{"alice": {}},
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	body := bytes.NewBufferString(`{"owner":"mallory","name":"x"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+tokensAPIPath, body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"token creation for an owner not in the htpasswd file must be rejected")
}

func TestTokenAuth_BulkRevokeByOwner(t *testing.T) {
	withAuthModeFlag(t, string(protect.ModeEmbedded))
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	prevHtpasswd := app.htpasswdAuth
	app.htpasswdAuth = nil
	t.Cleanup(func() { app.htpasswdAuth = prevHtpasswd })

	_, adminToken, err := ts.Create("root", "admin", nil)
	assert.NoError(t, err)
	_, _, err = ts.Create("alice", "laptop-1", nil)
	assert.NoError(t, err)
	_, _, err = ts.Create("alice", "laptop-2", nil)
	assert.NoError(t, err)
	_, _, err = ts.Create("bob", "laptop", nil)
	assert.NoError(t, err)

	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		Admins:           map[string]struct{}{"root": {}},
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+tokensAPIPath+"?owner=alice", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	remaining := ts.List("")
	assert.Equal(t, 2, len(remaining), "only root + bob should remain")
	for _, m := range remaining {
		assert.NotEqual(t, "alice", m.Owner)
	}
}

func TestTokenAuth_DevFallbackOpenPathsDoNotLeak(t *testing.T) {
	// Regression: /ping remains reachable without a token even when the
	// middleware is wrapping every other path with TokenAwareAuthenticator.
	ts := newTestTokenStore(t)
	withTokenStore(t, ts)
	withAuthenticator(t, &protect.TokenAwareAuthenticator{
		Store:            ts,
		QueryAllowedPath: isTokenQueryAllowedPath,
	})

	resp, err := http.Get(srv.URL + paths.Ping)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
