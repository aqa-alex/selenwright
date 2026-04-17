package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/aqa-alex/selenwright/protect"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	User    string   `json:"user"`
	IsAdmin bool     `json:"isAdmin"`
	Groups  []string `json:"groups"`
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if app.htpasswdAuth == nil {
		http.NotFound(w, r)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username and password required"})
		return
	}

	identity, err := app.htpasswdAuth.ValidateCredentials(req.Username, req.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		return
	}

	token, err := app.sessionStore.Create(identity)
	if err != nil {
		log.Printf("[-] [LOGIN] [session create error: %v]", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     protect.DefaultSessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(app.sessionStore.TTL().Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecureRequest(r),
	})

	log.Printf("[-] [LOGIN] [%s] [admin=%v] [groups=%d]", identity.User, identity.IsAdmin, len(identity.Groups))
	groups := identity.Groups
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, loginResponse{
		User:    identity.User,
		IsAdmin: identity.IsAdmin,
		Groups:  groups,
	})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(protect.DefaultSessionCookieName); err == nil {
		app.sessionStore.Delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     protect.DefaultSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
