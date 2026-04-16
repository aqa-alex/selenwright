package main

import (
	"encoding/json"
	"net/http"

	"github.com/aqa-alex/selenwright/protect"
)

type whoamiResponse struct {
	User          string `json:"user"`
	IsAdmin       bool   `json:"isAdmin"`
	AuthMode      string `json:"authMode"`
	Authenticated bool   `json:"authenticated"`
}

// whoamiHandler is registered as an open path so the UI can call it before
// the user has logged in. It attempts authentication manually: first from
// context (set by middleware for non-open paths — won't be set here), then
// by calling the authenticator directly.
func whoamiHandler(w http.ResponseWriter, r *http.Request) {
	identity, authenticated := tryAuthenticate(r)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(whoamiResponse{
		User:          identity.User,
		IsAdmin:       identity.IsAdmin,
		AuthMode:      app.authModeFlag,
		Authenticated: authenticated,
	})
}

func tryAuthenticate(r *http.Request) (protect.Identity, bool) {
	if id, ok := protect.IdentityFromContext(r.Context()); ok {
		return id, true
	}
	if id, err := app.authenticator.Authenticate(r); err == nil {
		return id, true
	}
	return protect.AnonymousIdentity, false
}
