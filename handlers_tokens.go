package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aqa-alex/selenwright/protect"
)

const (
	tokensAPIPath   = "/api/admin/tokens"
	tokensAPIPrefix = "/api/admin/tokens/"
	usersAPIPath    = "/api/admin/users"
)

type createTokenRequest struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type createTokenResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type tokenItem struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

// tokensHandler serves the collection endpoint /api/admin/tokens.
//
// Methods:
//   POST   → create a token for {owner, name}; plaintext returned once
//   GET    → list token metadata (optionally filtered by ?owner=)
//   DELETE → bulk revoke every token owned by ?owner=<user>
func tokensHandler(w http.ResponseWriter, r *http.Request) {
	if app.tokenStore == nil {
		http.NotFound(w, r)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodPost:
		createTokenHandler(w, r)
	case http.MethodGet:
		listTokensHandler(w, r)
	case http.MethodDelete:
		bulkRevokeTokensHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// tokenByIDHandler serves /api/admin/tokens/{id} (DELETE only) to revoke one
// token.
func tokenByIDHandler(w http.ResponseWriter, r *http.Request) {
	if app.tokenStore == nil {
		http.NotFound(w, r)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, tokensAPIPrefix)
	id = strings.TrimSuffix(id, "/")
	if id == "" || strings.ContainsAny(id, "/?#") {
		http.Error(w, "missing token id", http.StatusBadRequest)
		return
	}
	err := app.tokenStore.Revoke(id)
	if errors.Is(err, protect.ErrTokenNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("[-] [TOKENS] [revoke %s failed: %v]", id, err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	identity, _ := protect.IdentityFromContext(r.Context())
	log.Printf("[-] [TOKENS] [REVOKED] [id=%s] [by=%s]", id, identity.User)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "revoked"})
}

func createTokenHandler(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	owner := strings.TrimSpace(req.Owner)
	name := strings.TrimSpace(req.Name)
	if owner == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner and name required"})
		return
	}
	if app.htpasswdAuth != nil {
		known := false
		for _, u := range app.htpasswdAuth.Users() {
			if u == owner {
				known = true
				break
			}
		}
		if !known {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unknown owner %q", owner)})
			return
		}
	}
	var groups []string
	if app.groupsProvider != nil {
		groups = app.groupsProvider.GroupsFor(owner)
	}
	id, plaintext, err := app.tokenStore.Create(owner, name, groups)
	if err != nil {
		log.Printf("[-] [TOKENS] [create owner=%s name=%q failed: %v]", owner, name, err)
		http.Error(w, "create failed", http.StatusInternalServerError)
		return
	}
	identity, _ := protect.IdentityFromContext(r.Context())
	log.Printf("[-] [TOKENS] [CREATED] [id=%s] [owner=%s] [name=%q] [by=%s]", id, owner, name, identity.User)
	writeJSON(w, http.StatusCreated, createTokenResponse{ID: id, Token: plaintext})
}

func listTokensHandler(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	metas := app.tokenStore.List(owner)
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Owner != metas[j].Owner {
			return metas[i].Owner < metas[j].Owner
		}
		return metas[i].CreatedAt.Before(metas[j].CreatedAt)
	})
	out := make([]tokenItem, 0, len(metas))
	for _, m := range metas {
		item := tokenItem{
			ID:        m.ID,
			Owner:     m.Owner,
			Name:      m.Name,
			CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339),
		}
		if m.LastUsedAt != nil {
			item.LastUsedAt = m.LastUsedAt.UTC().Format(time.RFC3339)
		}
		if m.ExpiresAt != nil {
			item.ExpiresAt = m.ExpiresAt.UTC().Format(time.RFC3339)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func bulkRevokeTokensHandler(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("owner"))
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner query required for bulk revoke"})
		return
	}
	n, err := app.tokenStore.RevokeByOwner(owner)
	if err != nil {
		log.Printf("[-] [TOKENS] [bulk revoke owner=%s failed: %v]", owner, err)
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	identity, _ := protect.IdentityFromContext(r.Context())
	log.Printf("[-] [TOKENS] [BULK_REVOKED] [owner=%s] [count=%d] [by=%s]", owner, n, identity.User)
	writeJSON(w, http.StatusOK, map[string]any{"owner": owner, "revoked": n})
}

// tokenUsersHandler serves GET /api/admin/users. Returns usernames known to
// the htpasswd authenticator (plus any distinct token-owner names) so UIs can
// populate an owner dropdown for token creation. Admin-only.
func tokenUsersHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	seen := map[string]struct{}{}
	if app.htpasswdAuth != nil {
		for _, u := range app.htpasswdAuth.Users() {
			seen[u] = struct{}{}
		}
	}
	if app.tokenStore != nil {
		for _, o := range app.tokenStore.Owners() {
			seen[o] = struct{}{}
		}
	}
	users := make([]string, 0, len(seen))
	for u := range seen {
		users = append(users, u)
	}
	sort.Strings(users)
	writeJSON(w, http.StatusOK, users)
}
