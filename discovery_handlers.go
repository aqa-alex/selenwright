package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/aqa-alex/selenwright/discovery"
	"github.com/aqa-alex/selenwright/protect"
)

// requireAdmin checks admin access and writes a 403 if denied.
// When auth-mode=none the server has no ACL — all callers are trusted.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if app.authModeFlag == string(protect.ModeNone) {
		return true
	}
	identity, _ := protect.IdentityFromContext(r.Context())
	if identity.IsAdmin {
		return true
	}
	protect.WriteForbidden(w)
	return false
}

func discoveredBrowsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if app.disableDocker || app.adoptedStore == nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
		return
	}

	discovered, err := discovery.ScanImages(context.Background(), app.cli)
	if err != nil {
		log.Printf("[-] [DISCOVERY] [scan error: %v]", err)
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	unadopted := discovery.FilterUnadopted(discovered, app.adoptedStore)
	if unadopted == nil {
		unadopted = []discovery.DiscoveredImage{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(unadopted)
}

type digestRequest struct {
	Digest string `json:"digest"`
}

func adoptBrowser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if app.disableDocker || app.adoptedStore == nil {
		http.Error(w, "discovery not available", http.StatusServiceUnavailable)
		return
	}

	var req digestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Digest == "" {
		http.Error(w, "missing digest", http.StatusBadRequest)
		return
	}

	discovered, err := discovery.ScanImages(context.Background(), app.cli)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var target *discovery.DiscoveredImage
	for i := range discovered {
		if discovered[i].Digest == req.Digest {
			target = &discovered[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	repoTag := req.Digest
	if len(target.RepoTags) > 0 {
		repoTag = target.RepoTags[0]
	}
	if err := app.adoptedStore.Adopt(req.Digest, target.Name, target.Version, repoTag); err != nil {
		log.Printf("[-] [DISCOVERY] [adopt error: %v]", err)
		http.Error(w, "adopt failed", http.StatusInternalServerError)
		return
	}
	log.Printf("[-] [DISCOVERY] [Adopted %s %s (%s)]", target.Name, target.Version, repoTag)

	app.rescanMu.Lock()
	defer app.rescanMu.Unlock()
	if err := discovery.AssembleCatalog(context.Background(), app.cli, app.adoptedStore, app.conf, app.confPath, app.logConfPath); err != nil {
		log.Printf("[-] [DISCOVERY] [catalog rebuild after adopt: %v]", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "adopted"})
}

func dismissBrowser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if app.disableDocker || app.adoptedStore == nil {
		http.Error(w, "discovery not available", http.StatusServiceUnavailable)
		return
	}

	var req digestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Digest == "" {
		http.Error(w, "missing digest", http.StatusBadRequest)
		return
	}

	discovered, err := discovery.ScanImages(context.Background(), app.cli)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	var target *discovery.DiscoveredImage
	for i := range discovered {
		if discovered[i].Digest == req.Digest {
			target = &discovered[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	repoTag := req.Digest
	if len(target.RepoTags) > 0 {
		repoTag = target.RepoTags[0]
	}
	if err := app.adoptedStore.Dismiss(req.Digest, target.Name, target.Version, repoTag); err != nil {
		log.Printf("[-] [DISCOVERY] [dismiss error: %v]", err)
		http.Error(w, "dismiss failed", http.StatusInternalServerError)
		return
	}
	log.Printf("[-] [DISCOVERY] [Dismissed %s %s (%s)]", target.Name, target.Version, repoTag)

	app.rescanMu.Lock()
	defer app.rescanMu.Unlock()
	if err := discovery.AssembleCatalog(context.Background(), app.cli, app.adoptedStore, app.conf, app.confPath, app.logConfPath); err != nil {
		log.Printf("[-] [DISCOVERY] [catalog rebuild after dismiss: %v]", err)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "dismissed"})
}

func rescanBrowsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if app.disableDocker || app.adoptedStore == nil {
		http.Error(w, "discovery not available", http.StatusServiceUnavailable)
		return
	}

	app.rescanMu.Lock()
	if err := discovery.AssembleCatalog(context.Background(), app.cli, app.adoptedStore, app.conf, app.confPath, app.logConfPath); err != nil {
		app.rescanMu.Unlock()
		log.Printf("[-] [DISCOVERY] [rescan error: %v]", err)
		http.Error(w, "rescan failed", http.StatusInternalServerError)
		return
	}
	app.rescanMu.Unlock()
	log.Printf("[-] [DISCOVERY] [Manual rescan completed]")

	discovered, err := discovery.ScanImages(context.Background(), app.cli)
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}
	unadopted := discovery.FilterUnadopted(discovered, app.adoptedStore)
	if unadopted == nil {
		unadopted = []discovery.DiscoveredImage{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(unadopted)
}
