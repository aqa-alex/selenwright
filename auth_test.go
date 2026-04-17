package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/session"
	assert "github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func writeTestHtpasswd(t *testing.T, user, password string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "htpasswd")
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	assert.NoError(t, err)
	assert.NoError(t, os.WriteFile(path, []byte(user+":"+string(hash)+"\n"), 0o600))
	return path
}

func withAuthenticator(t *testing.T, a protect.Authenticator) {
	t.Helper()
	prev := app.authenticator
	app.authenticator = a
	t.Cleanup(func() { app.authenticator = prev })
}

func TestEmbeddedAuth_RejectsMissingCredentials(t *testing.T) {
	a, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), nil)
	assert.NoError(t, err)
	withAuthenticator(t, a)

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("WWW-Authenticate"), "selenwright")
}

func TestEmbeddedAuth_AcceptsValidBasicAuth(t *testing.T) {
	a, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), nil)
	assert.NoError(t, err)
	withAuthenticator(t, a)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.SetBasicAuth("alice", "pw")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestEmbeddedAuth_RejectsBadPassword(t *testing.T) {
	a, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), nil)
	assert.NoError(t, err)
	withAuthenticator(t, a)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.SetBasicAuth("alice", "wrong")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestTrustedProxyAuth_RequiresUserHeader(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("WWW-Authenticate"),
		"trusted-proxy mode must not advertise BasicAuth realm — clients should auth at the proxy")
}

func TestTrustedProxyAuth_AcceptsHeader(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestOpenPathsBypassAuth(t *testing.T) {
	a, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), nil)
	assert.NoError(t, err)
	withAuthenticator(t, a)

	for _, p := range []string{paths.Ping, paths.Status, paths.Welcome} {
		t.Run(p, func(t *testing.T) {
			resp, err := http.Get(srv.URL + p)
			assert.NoError(t, err)
			defer resp.Body.Close()
			assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
				"open path %s must not require authentication", p)
		})
	}
}

func TestNoneAuth_AnyRequestPasses(t *testing.T) {
	withAuthenticator(t, protect.NoneAuthenticator{})

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIsLoopbackListen(t *testing.T) {
	cases := []struct {
		addr   string
		expect bool
	}{
		{"127.0.0.1:4444", true},
		{"localhost:4444", true},
		{"[::1]:4444", true},
		{"0.0.0.0:4444", false},
		{":4444", false},
		{"192.168.1.1:4444", false},
		{"example.com:4444", false},
		{"not-a-host", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			assert.Equal(t, tc.expect, isLoopbackListen(tc.addr),
				"isLoopbackListen(%q)", tc.addr)
		})
	}
}

func TestBuildAuthenticator_EmbeddedRequiresHtpasswd(t *testing.T) {
	_, err := buildAuthenticator(authBuildOptions{
		mode:        "embedded",
		userHeader:  "X-Forwarded-User",
		adminHeader: "X-Admin",
		listenAddr:  ":4444",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-htpasswd")
}

func TestBuildAuthenticator_NoneOnLoopback(t *testing.T) {
	r, err := buildAuthenticator(authBuildOptions{mode: "none", listenAddr: "127.0.0.1:4444"})
	assert.NoError(t, err)
	assert.NotNil(t, r.authenticator)
}

func TestBuildAuthenticator_NoneOnPublicListen(t *testing.T) {
	r, err := buildAuthenticator(authBuildOptions{mode: "none", listenAddr: "0.0.0.0:4444"})
	assert.NoError(t, err)
	assert.NotNil(t, r.authenticator)
}

func TestBuildAuthenticator_UnknownMode(t *testing.T) {
	_, err := buildAuthenticator(authBuildOptions{mode: "nonsense", listenAddr: ":4444"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected embedded|trusted-proxy|none")
}

func withSourceTrust(t *testing.T, st *protect.SourceTrust) {
	t.Helper()
	prev := app.sourceTrust
	app.sourceTrust = st
	t.Cleanup(func() { app.sourceTrust = prev })
}

func TestSourceTrust_RejectsRequestWithoutSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_AcceptsRequestWithCorrectSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set(protect.HeaderRouterSecret, "topsecret")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_RejectsWrongSecret(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.WdHub+"/", nil)
	req.Header.Set(protect.HeaderRouterSecret, "wrong")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSourceTrust_DisabledByDefault(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{}))

	resp, err := http.Get(srv.URL + paths.WdHub + "/")
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"empty source-trust config must not block any request")
}

func TestSourceTrust_OpenPathsAreNotGated(t *testing.T) {
	withSourceTrust(t, protect.NewSourceTrust(protect.SourceTrustConfig{Secret: "topsecret"}))

	resp, err := http.Get(srv.URL + paths.Ping)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusUnauthorized, resp.StatusCode,
		"open paths still bypass when source-trust is enabled — health-check style endpoints should not require the secret")
}

func TestBuildSourceTrustConfig_StripsRouterHeaders(t *testing.T) {
	cfg, err := buildSourceTrustConfig("trusted-proxy", "secret", "10.0.0.0/8", "", "X-Forwarded-User", "X-Admin", "X-Groups")
	assert.NoError(t, err)
	assert.Contains(t, cfg.StripHeaders, protect.HeaderRouterSecret)
	assert.Contains(t, cfg.StripHeaders, "X-Forwarded-User")
	assert.Contains(t, cfg.StripHeaders, "X-Admin")
	assert.Contains(t, cfg.StripHeaders, "X-Groups")
}

func TestBuildSourceTrustConfig_RejectsInvalidCIDR(t *testing.T) {
	_, err := buildSourceTrustConfig("trusted-proxy", "", "not-a-cidr", "", "", "", "")
	assert.Error(t, err)
}

func TestBuildSourceTrustConfig_LoadsCAPool(t *testing.T) {
	dir := t.TempDir()
	caPath := dir + "/ca.pem"
	assert.NoError(t, os.WriteFile(caPath, generateTestCAPEM(t), 0o600))

	cfg, err := buildSourceTrustConfig("trusted-proxy", "", "", caPath, "", "", "")
	assert.NoError(t, err)
	assert.True(t, cfg.RequireMTLS)
	assert.NotNil(t, cfg.AllowedRootCAs)
}

func generateTestCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assert.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"selenwright-test-ca"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	assert.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBuildSourceTrustConfig_MissingCAFile(t *testing.T) {
	_, err := buildSourceTrustConfig("trusted-proxy", "", "", "/no/such/file", "", "", "")
	assert.Error(t, err)
}

func TestSessionACL_OwnerCanAccessOwnSession(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	app.sessions.Put("alice-vnc-owner", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("alice-vnc-owner") })

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

	app.sessions.Put("alice-download", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{Fileserver: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("alice-download") })

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

	app.sessions.Put("alice-vnc-admin", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("alice-vnc-admin") })

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

	app.sessions.Put("alice-vnc", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{VNC: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("alice-vnc") })

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

	app.sessions.Put("alice-dt", &session.Session{
		Quota:    "alice",
		HostPort: session.HostPort{Devtools: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("alice-dt") })

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

func TestSessionACL_GroupMateCanAccess(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", GroupsHeader: "X-Groups"})

	app.sessions.Put("bot-download", &session.Session{
		Quota:       "jenkins-bot",
		OwnerGroups: []string{"qa"},
		HostPort:    session.HostPort{Fileserver: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("bot-download") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.Download+"bot-download/file.txt", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Groups", "qa,growth")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"group-mate of the owner must bypass the ACL gate")
}

func TestSessionACL_DisjointGroupsRejected(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", GroupsHeader: "X-Groups"})

	app.sessions.Put("bot-download2", &session.Session{
		Quota:       "jenkins-bot",
		OwnerGroups: []string{"qa"},
		HostPort:    session.HostPort{Fileserver: "127.0.0.1:0"},
	})
	t.Cleanup(func() { app.sessions.Remove("bot-download2") })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+paths.Download+"bot-download2/file.txt", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	req.Header.Set("X-Groups", "ops")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"caller with no overlapping groups must be rejected")
}

func TestPlaywrightDelete_Owner(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	cancelled := false
	app.sessions.Put("pw-owner", &session.Session{
		Quota:    "alice",
		Protocol: session.ProtocolPlaywright,
		Cancel:   func() { cancelled = true },
	})
	t.Cleanup(func() { app.sessions.Remove("pw-owner") })

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/playwright/session/pw-owner", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.True(t, cancelled)
}

func TestPlaywrightDelete_GroupMate(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", GroupsHeader: "X-Groups"})

	cancelled := false
	app.sessions.Put("pw-group", &session.Session{
		Quota:       "jenkins-bot",
		OwnerGroups: []string{"qa"},
		Protocol:    session.ProtocolPlaywright,
		Cancel:      func() { cancelled = true },
	})
	t.Cleanup(func() { app.sessions.Remove("pw-group") })

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/playwright/session/pw-group", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Groups", "qa")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.True(t, cancelled, "group-mate must be allowed to terminate the Playwright session")
}

func TestPlaywrightDelete_Foreign(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"})

	cancelled := false
	app.sessions.Put("pw-foreign", &session.Session{
		Quota:    "alice",
		Protocol: session.ProtocolPlaywright,
		Cancel:   func() { cancelled = true },
	})
	t.Cleanup(func() { app.sessions.Remove("pw-foreign") })

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/playwright/session/pw-foreign", nil)
	req.Header.Set("X-Forwarded-User", "mallory")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"non-owner must not terminate a Playwright session — regression guard for the earlier auth gap")
	assert.False(t, cancelled, "cancel must not fire when the ACL gate denies the request")
}

func TestPlaywrightDelete_AdminBypass(t *testing.T) {
	withAuthenticator(t, &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", AdminHeader: "X-Admin"})

	cancelled := false
	app.sessions.Put("pw-admin", &session.Session{
		Quota:    "alice",
		Protocol: session.ProtocolPlaywright,
		Cancel:   func() { cancelled = true },
	})
	t.Cleanup(func() { app.sessions.Remove("pw-admin") })

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/playwright/session/pw-admin", nil)
	req.Header.Set("X-Forwarded-User", "root")
	req.Header.Set("X-Admin", "true")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.True(t, cancelled)
}

func TestTrustedProxyAuth_ReadsGroupsHeader(t *testing.T) {
	a := &protect.TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", GroupsHeader: "X-Groups"}
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-User", "alice")
	r.Header.Set("X-Groups", "qa , ops, , qa")

	id, err := a.Authenticate(r)
	assert.NoError(t, err)
	assert.Equal(t, "alice", id.User)
	assert.Equal(t, []string{"qa", "ops"}, id.Groups)
}

func TestHtpasswdAuth_PopulatesGroupsFromProvider(t *testing.T) {
	a, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "alice", "pw"), nil)
	assert.NoError(t, err)

	dir := t.TempDir()
	groupsPath := dir + "/groups.json"
	assert.NoError(t, os.WriteFile(groupsPath, []byte(`{"qa":["alice"]}`), 0o600))
	gp, err := protect.NewFileGroupsProvider(groupsPath)
	assert.NoError(t, err)
	a.SetGroups(gp)

	id, err := a.ValidateCredentials("alice", "pw")
	assert.NoError(t, err)
	assert.Equal(t, []string{"qa"}, id.Groups)

	// Unknown-to-groups user still authenticates, with no groups attached.
	a2, err := protect.NewHtpasswdAuthenticator(writeTestHtpasswd(t, "bob", "pw"), nil)
	assert.NoError(t, err)
	a2.SetGroups(gp)
	id2, err := a2.ValidateCredentials("bob", "pw")
	assert.NoError(t, err)
	assert.Nil(t, id2.Groups)
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
