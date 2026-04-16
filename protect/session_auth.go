package protect

import "net/http"

const DefaultSessionCookieName = "selenwright_session"

// SessionAwareAuthenticator wraps an existing Authenticator, checking for a
// valid session cookie before falling back to the wrapped authenticator.
// This allows cookie-based UI login to coexist with Basic Auth API clients.
type SessionAwareAuthenticator struct {
	Sessions   *SessionStore
	CookieName string
	Fallback   Authenticator
}

func (a *SessionAwareAuthenticator) Authenticate(r *http.Request) (Identity, error) {
	if cookie, err := r.Cookie(a.CookieName); err == nil && cookie.Value != "" {
		if id, ok := a.Sessions.Validate(cookie.Value); ok {
			return id, nil
		}
	}
	return a.Fallback.Authenticate(r)
}

func (a *SessionAwareAuthenticator) Realm() string {
	return a.Fallback.Realm()
}
