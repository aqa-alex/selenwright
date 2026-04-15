package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aqa-alex/selenwright/protect"
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
	prev := authenticator
	authenticator = a
	t.Cleanup(func() { authenticator = prev })
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
	_, _, err := buildAuthenticator("embedded", "", nil, "X-Forwarded-User", "X-Admin", ":4444", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-htpasswd")
}

func TestBuildAuthenticator_NoneRefusesPublicListen(t *testing.T) {
	_, _, err := buildAuthenticator("none", "", nil, "", "", "0.0.0.0:4444", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "-allow-insecure-none")
}

func TestBuildAuthenticator_NoneAllowsLoopback(t *testing.T) {
	a, _, err := buildAuthenticator("none", "", nil, "", "", "127.0.0.1:4444", false)
	assert.NoError(t, err)
	assert.NotNil(t, a)
}

func TestBuildAuthenticator_NoneAllowsPublicWithFlag(t *testing.T) {
	a, _, err := buildAuthenticator("none", "", nil, "", "", "0.0.0.0:4444", true)
	assert.NoError(t, err)
	assert.NotNil(t, a)
}

func TestBuildAuthenticator_UnknownMode(t *testing.T) {
	_, _, err := buildAuthenticator("nonsense", "", nil, "", "", ":4444", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected embedded|trusted-proxy|none")
}
