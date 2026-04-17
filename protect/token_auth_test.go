package protect

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBearerToken_Parse(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"", "", false},
		{"Bearer ", "", false},
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"Bearer   abc  ", "abc", true},
		{"Basic dXNlcjpwYXNz", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			got, ok := BearerToken(r)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTokenAwareAuthenticator_AcceptsBearer(t *testing.T) {
	store, _ := newTestTokenStore(t)
	_, plaintext, err := store.Create("alice", "t1", []string{"qa"})
	require.NoError(t, err)

	auth := &TokenAwareAuthenticator{
		Store:  store,
		Admins: map[string]struct{}{"alice": {}},
	}
	r, _ := http.NewRequest(http.MethodGet, "/wd/hub/session", nil)
	r.Header.Set("Authorization", "Bearer "+plaintext)

	id, err := auth.Authenticate(r)
	require.NoError(t, err)
	require.Equal(t, "alice", id.User)
	require.True(t, id.IsAdmin)
	require.Equal(t, []string{"qa"}, id.Groups)
}

func TestTokenAwareAuthenticator_RejectsInvalidBearerHardly(t *testing.T) {
	store, _ := newTestTokenStore(t)
	fallback := &stubAuth{}
	auth := &TokenAwareAuthenticator{Store: store, Fallback: fallback}

	r, _ := http.NewRequest(http.MethodGet, "/wd/hub/session", nil)
	r.Header.Set("Authorization", "Bearer swr_live_doesnotexist")

	_, err := auth.Authenticate(r)
	require.ErrorIs(t, err, ErrAuthFailed)
	require.False(t, fallback.called, "fallback must not be invoked when Bearer present but invalid")
}

func TestTokenAwareAuthenticator_FallsThroughWhenNoBearer(t *testing.T) {
	store, _ := newTestTokenStore(t)
	fallback := &stubAuth{id: Identity{User: "fb"}}
	auth := &TokenAwareAuthenticator{Store: store, Fallback: fallback}

	r, _ := http.NewRequest(http.MethodGet, "/wd/hub/session", nil)
	id, err := auth.Authenticate(r)
	require.NoError(t, err)
	require.Equal(t, "fb", id.User)
	require.True(t, fallback.called)
}

func TestTokenAwareAuthenticator_AcceptsQueryParamOnAllowedPath(t *testing.T) {
	store, _ := newTestTokenStore(t)
	_, plaintext, err := store.Create("alice", "t1", nil)
	require.NoError(t, err)

	auth := &TokenAwareAuthenticator{
		Store:            store,
		QueryAllowedPath: func(p string) bool { return strings.HasPrefix(p, "/playwright/") },
	}
	r, _ := http.NewRequest(http.MethodGet, "/playwright/chromium/1.0.0?token="+plaintext, nil)
	id, err := auth.Authenticate(r)
	require.NoError(t, err)
	require.Equal(t, "alice", id.User)
}

func TestTokenAwareAuthenticator_IgnoresQueryParamOnDisallowedPath(t *testing.T) {
	store, _ := newTestTokenStore(t)
	_, plaintext, err := store.Create("alice", "t1", nil)
	require.NoError(t, err)

	fallback := &stubAuth{id: Identity{User: "fb"}}
	auth := &TokenAwareAuthenticator{
		Store:            store,
		Fallback:         fallback,
		QueryAllowedPath: func(p string) bool { return strings.HasPrefix(p, "/playwright/") },
	}
	r, _ := http.NewRequest(http.MethodGet, "/status?token="+plaintext, nil)
	id, err := auth.Authenticate(r)
	require.NoError(t, err)
	require.Equal(t, "fb", id.User)
	require.True(t, fallback.called)
}

func TestAuthMiddleware_StripsTokenQueryParam(t *testing.T) {
	store, _ := newTestTokenStore(t)
	_, plaintext, err := store.Create("alice", "t1", nil)
	require.NoError(t, err)

	auth := &TokenAwareAuthenticator{
		Store:            store,
		QueryAllowedPath: func(string) bool { return true },
	}
	mw := AuthMiddleware(func() Authenticator { return auth }, AuthMiddlewareOptions{})

	var seenRawQuery string
	var seenToken string
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenRawQuery = r.URL.RawQuery
		seenToken = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/playwright/chromium/1.0?token=" + plaintext + "&other=keep")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, seenToken, "token must be stripped before reaching downstream")
	require.Contains(t, seenRawQuery, "other=keep", "other params preserved")
	require.NotContains(t, seenRawQuery, "token=", "token param stripped from RawQuery")
}

func TestAuthMiddleware_BearerAuthorization(t *testing.T) {
	store, _ := newTestTokenStore(t)
	_, plaintext, err := store.Create("alice", "t1", nil)
	require.NoError(t, err)

	auth := &TokenAwareAuthenticator{Store: store}
	mw := AuthMiddleware(func() Authenticator { return auth }, AuthMiddlewareOptions{})

	var seenUser string
	srv := httptest.NewServer(mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := IdentityFromContext(r.Context()); ok {
			seenUser = id.User
		}
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/anything", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "alice", seenUser)
}

func TestHtpasswdAuthenticator_Users(t *testing.T) {
	path := writeHtpasswd(t,
		"alice:"+bcryptHash(t, "pw"),
		"bob:"+bcryptHash(t, "pw"),
	)
	a, err := NewHtpasswdAuthenticator(path, nil)
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"alice", "bob"}, a.Users())
}

// stubAuth is a minimal Authenticator used to verify fallback behavior.
type stubAuth struct {
	called bool
	id     Identity
	err    error
}

func (s *stubAuth) Authenticate(*http.Request) (Identity, error) {
	s.called = true
	return s.id, s.err
}
func (s *stubAuth) Realm() string { return "" }
