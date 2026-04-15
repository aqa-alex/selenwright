package main

import (
	"net/http"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/session"
	assert "github.com/stretchr/testify/require"
)

func TestSessionACL_OwnerCanAccessOwnSession(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	sessions.Put("alice-vnc-owner", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { sessions.Remove("alice-vnc-owner") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.VNC+"alice-vnc-owner", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("Origin", "http://localhost")
	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"owner must pass the ACL gate; downstream upgrade may still fail because the test backend is fake")
}

func TestSessionACL_NonOwnerForbidden(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	sessions.Put("alice-download", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{Fileserver: "127.0.0.1:0"},
	})
	t.Cleanup(func() { sessions.Remove("alice-download") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.Download+"alice-download/file.txt", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"bob must not see alice's session")
}

func TestSessionACL_AdminBypass(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", AdminHeader: "X-Admin"})

	sessions.Put("alice-vnc-admin", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { sessions.Remove("alice-vnc-admin") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.VNC+"alice-vnc-admin", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("X-Forwarded-User", "root")
	req.Header.Set("X-Admin", "true")
	req.Header.Set("Origin", "http://localhost")
	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"admin must bypass the per-session ACL")
}

func TestSessionACL_VNCRejectsNonOwner(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	sessions.Put("alice-vnc", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { sessions.Remove("alice-vnc") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.VNC+"alice-vnc", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("X-Forwarded-User", "bob")
	req.Header.Set("Origin", "http://localhost")

	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"VNC upgrade for foreign session must be rejected before handshake")
}

func TestSessionACL_DevtoolsRejectsNonOwner(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	sessions.Put("alice-dt", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{Devtools: "127.0.0.1:0"},
	})
	t.Cleanup(func() { sessions.Remove("alice-dt") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.Devtools+"alice-dt/page", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestSessionACL_UnknownSessionPassesThrough(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/wd/hub/session/never-existed", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"unknown session must not surface as 403; downstream renders 404 or its own error")
}

func TestSessionACL_UnknownSessionWithVNC(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.VNC+"never-existed", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("Origin", "http://localhost")
	resp, err := http.DefaultTransport.RoundTrip(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode)
}

