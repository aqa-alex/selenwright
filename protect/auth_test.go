package protect

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestParseHtpasswd_AcceptsBcryptVariants(t *testing.T) {
	in := []byte(`# comment
alice:$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
bob:$2b$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
carol:$2y$12$3euPcmQFCiblsZeEu5s7p.9OVHgeHWFDk9nhMqZ.q.0rzGcOmnXFu
`)
	out, err := parseHtpasswd(in)
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.Contains(t, out, "alice")
	require.Contains(t, out, "bob")
	require.Contains(t, out, "carol")
}

func TestParseHtpasswd_RejectsNonBcrypt(t *testing.T) {
	cases := []string{
		"alice:plaintext",
		"alice:$apr1$abc$abc",
		"alice:{SHA}abc",
		"alice:$1$salt$abc",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			_, err := parseHtpasswd([]byte(line + "\n"))
			require.Error(t, err)
		})
	}
}

func TestParseHtpasswd_RejectsMalformed(t *testing.T) {
	cases := []string{
		"justuser",
		"alice:",
		":hash",
		"alice:$2a$10$too\nshort",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			_, err := parseHtpasswd([]byte(line + "\n"))
			require.Error(t, err)
		})
	}
}

func TestParseHtpasswd_RejectsDuplicates(t *testing.T) {
	in := []byte(`alice:$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
alice:$2b$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy
`)
	_, err := parseHtpasswd(in)
	require.Error(t, err)
}

func TestParseHtpasswd_EmptyFileRejected(t *testing.T) {
	_, err := parseHtpasswd([]byte("# only comments\n\n"))
	require.Error(t, err)
}

func writeHtpasswd(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "htpasswd")
	require.NoError(t, os.WriteFile(path, []byte(joinLines(lines)), 0o600))
	return path
}

func bcryptHash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(h)
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestHtpasswdAuthenticator_AcceptsValidCredentials(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "secret")
	id, err := a.Authenticate(req)
	require.NoError(t, err)
	require.Equal(t, "alice", id.User)
	require.False(t, id.IsAdmin)
}

func TestHtpasswdAuthenticator_AdminFlag(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, []string{"alice"})
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "secret")
	id, err := a.Authenticate(req)
	require.NoError(t, err)
	require.True(t, id.IsAdmin)
}

func TestHtpasswdAuthenticator_RejectsBadPassword(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "wrong")
	_, err = a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestHtpasswdAuthenticator_RejectsUnknownUser(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("bob", "secret")
	_, err = a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestHtpasswdAuthenticator_NoBasicAuthHeader(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	_, err = a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthRequired)
}

func TestHtpasswdAuthenticator_Reload(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "secret"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte("bob:"+bcryptHash(t, "newpass")+"\n"), 0o600))
	require.NoError(t, a.Reload())

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "secret")
	_, err = a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthFailed)

	req2, _ := http.NewRequest(http.MethodGet, "/", nil)
	req2.SetBasicAuth("bob", "newpass")
	_, err = a.Authenticate(req2)
	require.NoError(t, err)
}

func TestTrustedProxyAuthenticator_ReadsHeader(t *testing.T) {
	a := &TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", AdminHeader: "X-Admin"}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	id, err := a.Authenticate(req)
	require.NoError(t, err)
	require.Equal(t, "alice", id.User)
	require.False(t, id.IsAdmin)
}

func TestTrustedProxyAuthenticator_AdminTrue(t *testing.T) {
	a := &TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User", AdminHeader: "X-Admin"}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "root")
	req.Header.Set("X-Admin", "true")
	id, err := a.Authenticate(req)
	require.NoError(t, err)
	require.True(t, id.IsAdmin)
}

func TestTrustedProxyAuthenticator_MissingHeader(t *testing.T) {
	a := &TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	_, err := a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthRequired)
}

func TestTrustedProxyAuthenticator_RejectsControlChars(t *testing.T) {
	a := &TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-User", "alice\tinjection")
	_, err := a.Authenticate(req)
	require.ErrorIs(t, err, ErrAuthFailed)
}

func TestAuthMiddleware_OpenPathBypassesAuth(t *testing.T) {
	mw := AuthMiddleware(func() Authenticator { return NoneAuthenticator{} }, AuthMiddlewareOptions{OpenPaths: []string{"/ping"}})
	hit := false
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ping")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, hit)
}

func TestAuthMiddleware_NoneAuthenticatorAlwaysSucceeds(t *testing.T) {
	mw := AuthMiddleware(func() Authenticator { return NoneAuthenticator{} }, AuthMiddlewareOptions{})
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		require.True(t, ok)
		require.Equal(t, "unknown", id.User)
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anything")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuthMiddleware_HtpasswdReturns401WithRealm(t *testing.T) {
	path := writeHtpasswd(t, "alice:"+bcryptHash(t, "pw"))
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	mw := AuthMiddleware(func() Authenticator { return a }, AuthMiddlewareOptions{})
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, resp.Header.Get("WWW-Authenticate"), `realm="`+HtpasswdRealm+`"`)
}

func TestAuthMiddleware_TrustedProxyNoWWWAuthenticate(t *testing.T) {
	a := &TrustedProxyAuthenticator{UserHeader: "X-Forwarded-User"}
	mw := AuthMiddleware(func() Authenticator { return a }, AuthMiddlewareOptions{})
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Empty(t, resp.Header.Get("WWW-Authenticate"))
}

func TestConstantTimeStringEqual(t *testing.T) {
	require.True(t, ConstantTimeStringEqual("secret", "secret"))
	require.False(t, ConstantTimeStringEqual("secret", "secre"))
	require.False(t, ConstantTimeStringEqual("secret", "secrets"))
	require.False(t, ConstantTimeStringEqual("", "x"))
	require.True(t, ConstantTimeStringEqual("", ""))
}
