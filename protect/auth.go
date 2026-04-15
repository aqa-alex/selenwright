package protect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type AuthMode string

const (
	ModeEmbedded     AuthMode = "embedded"
	ModeTrustedProxy AuthMode = "trusted-proxy"
	ModeNone         AuthMode = "none"
)

type Identity struct {
	User    string
	IsAdmin bool
}

// AnonymousIdentity is the placeholder returned by NoneAuthenticator and
// used as the fallback when no Authenticator was set. The user string
// matches selenwright's legacy info.RequestInfo convention so log output
// and quota maps remain stable across the auth-mode rollout.
var AnonymousIdentity = Identity{User: "unknown", IsAdmin: false}

var (
	ErrAuthRequired = errors.New("authentication required")
	ErrAuthFailed   = errors.New("authentication failed")
)

type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
	Realm() string
}

type identityCtxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

type NoneAuthenticator struct{}

func (NoneAuthenticator) Authenticate(_ *http.Request) (Identity, error) {
	return AnonymousIdentity, nil
}

func (NoneAuthenticator) Realm() string { return "" }

type TrustedProxyAuthenticator struct {
	UserHeader  string
	AdminHeader string
}

func (a *TrustedProxyAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	user := r.Header.Get(a.UserHeader)
	if user == "" {
		return Identity{}, ErrAuthRequired
	}
	if strings.ContainsAny(user, "\r\n\t") {
		return Identity{}, fmt.Errorf("%w: header contains control characters", ErrAuthFailed)
	}
	id := Identity{User: user}
	if a.AdminHeader != "" && strings.EqualFold(r.Header.Get(a.AdminHeader), "true") {
		id.IsAdmin = true
	}
	return id, nil
}

func (a *TrustedProxyAuthenticator) Realm() string { return "" }

type AuthMiddlewareOptions struct {
	OpenPaths []string
}

// AuthMiddleware returns a middleware that authenticates incoming requests
// using the Authenticator returned by authFn. The function is invoked per
// request so callers can swap the active Authenticator atomically (SIGHUP
// reload, runtime reconfiguration, tests).
func AuthMiddleware(authFn func() Authenticator, opts AuthMiddlewareOptions) func(http.Handler) http.Handler {
	openExact := make(map[string]struct{}, len(opts.OpenPaths))
	for _, p := range opts.OpenPaths {
		openExact[p] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := openExact[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			auth := authFn()
			id, err := auth.Authenticate(r)
			if err != nil {
				if realm := auth.Realm(); realm != "" {
					w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
				}
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}
