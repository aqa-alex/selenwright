package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/aqa-alex/selenwright/discovery/registry"
	"github.com/docker/docker/api/types/image"
)

// registryListHandler handles GET /api/registry/list?host=<host>.
//
// Anonymous OCI v2 catalog walk. Admin-only — same gate as the existing
// browser-discovery endpoints in discovery_handlers.go.
func registryListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if app.registryClient == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"message": "registry client not initialised",
		})
		return
	}

	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"message": "missing host query parameter",
		})
		return
	}

	classified, err := registry.ClassifyInput(host)
	if err != nil {
		// 400 — bad input from the user, not an upstream failure.
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"message": err.Error(),
		})
		return
	}

	var listing registry.Listing
	switch classified.Source {
	case registry.SourceHub:
		listing, err = app.registryClient.ListDockerHub(r.Context(), classified.Value)
	default:
		listing, err = app.registryClient.List(r.Context(), classified.Value)
	}
	if err != nil {
		log.Printf("[-] [REGISTRY] [list %q (%s): %v]", host, classified.Source, err)
		// 502 reflects "we tried, the upstream refused"; 400 was reserved for
		// malformed local input above.
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"message": err.Error(),
		})
		return
	}
	log.Printf("[-] [REGISTRY] [list %q (%s): %d repo(s), %d error(s)]", host, classified.Source, len(listing.Repos), len(listing.Errors))
	writeJSONResponse(w, http.StatusOK, listing)
}

// registryRef is one selected (repo, tag) pair to pull.
type registryRef struct {
	Repo string `json:"repo"`
	Tag  string `json:"tag"`
}

// registryPullRequest is the body of POST /api/registry/pull.
//
// Source/Namespace come from the listing response and tell the handler how to
// build pull refs: "oci" -> "<host>/<repo>:<tag>"; "hub" -> "<namespace>/<repo>:<tag>".
type registryPullRequest struct {
	Host      string        `json:"host"`
	Source    string        `json:"source,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	Refs      []registryRef `json:"refs"`
}

// registryPullItem is the per-ref outcome of a pull.
type registryPullItem struct {
	Ref   string `json:"ref"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// registryPullResponse wraps the per-ref results.
type registryPullResponse struct {
	Results []registryPullItem `json:"results"`
}

// registryPullHandler handles POST /api/registry/pull.
//
// Pulls each requested ref via the existing app.cli.ImagePull. Per-ref errors
// are recorded and reported, but never abort the batch. Once images land in
// the local daemon, the existing label-discovery path (FilterUnadopted) will
// surface them in /api/browsers/discovered on the next refetch — the UI
// invalidates that query on success.
func registryPullHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAdmin(w, r) {
		return
	}
	if app.disableDocker || app.cli == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{
			"message": "Docker is disabled",
		})
		return
	}

	var req registryPullRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"message": "invalid JSON body",
		})
		return
	}
	if strings.TrimSpace(req.Host) == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"message": "missing host",
		})
		return
	}
	if len(req.Refs) == 0 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"message": "refs must be a non-empty array",
		})
		return
	}

	source := strings.ToLower(strings.TrimSpace(req.Source))
	results := make([]registryPullItem, 0, len(req.Refs))
	for _, ref := range req.Refs {
		item := registryPullItem{}
		var built string
		var err error
		switch source {
		case string(registry.SourceHub):
			built, err = registry.HubPullRef(req.Namespace, ref.Repo, ref.Tag)
		default:
			built, err = registry.PullRef(req.Host, ref.Repo, ref.Tag)
		}
		if err != nil {
			item.Ref = strings.TrimSpace(ref.Repo + ":" + ref.Tag)
			item.OK = false
			item.Error = err.Error()
			results = append(results, item)
			continue
		}
		item.Ref = built

		pullCtx, cancel := context.WithTimeout(r.Context(), stackPullTimeout)
		rc, perr := app.cli.ImagePull(pullCtx, built, image.PullOptions{})
		if perr != nil {
			cancel()
			item.OK = false
			item.Error = perr.Error()
			log.Printf("[-] [REGISTRY] [pull %s: %v]", built, perr)
			results = append(results, item)
			continue
		}
		// docker SDK requires the body to be drained before the pull is
		// considered complete.
		_, copyErr := io.Copy(io.Discard, rc)
		_ = rc.Close()
		cancel()

		switch {
		case copyErr == nil, errors.Is(copyErr, io.EOF):
			item.OK = true
			log.Printf("[-] [REGISTRY] [pull %s: ok]", built)
		default:
			item.OK = false
			item.Error = copyErr.Error()
			log.Printf("[-] [REGISTRY] [pull %s: %v]", built, copyErr)
		}
		results = append(results, item)
	}

	writeJSONResponse(w, http.StatusOK, registryPullResponse{Results: results})
}
