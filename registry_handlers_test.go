package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aqa-alex/selenwright/discovery/registry"
	"github.com/aqa-alex/selenwright/protect"
)

// withAuthBypassed sets app.authModeFlag = "none" for the duration of the test.
// Without it, requireAdmin would fall through to the configured authenticator
// which has no identity attached to a bare httptest request and rejects.
func withAuthBypassed(t *testing.T) {
	t.Helper()
	prev := app.authModeFlag
	app.authModeFlag = string(protect.ModeNone)
	t.Cleanup(func() { app.authModeFlag = prev })
}

func withRegistryClient(t *testing.T, c *registry.Client) {
	t.Helper()
	prev := app.registryClient
	app.registryClient = c
	t.Cleanup(func() { app.registryClient = prev })
}

func withDockerDisabled(t *testing.T) {
	t.Helper()
	prevDisable := app.disableDocker
	prevCli := app.cli
	app.disableDocker = true
	app.cli = nil
	t.Cleanup(func() {
		app.disableDocker = prevDisable
		app.cli = prevCli
	})
}

func TestRegistryListHandler_MethodNotAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/registry/list", nil)
	registryListHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestRegistryListHandler_ForbiddenWithoutAdmin(t *testing.T) {
	// Default authModeFlag is "embedded"; bare httptest request has no admin
	// identity attached, so requireAdmin should refuse with 403.
	prev := app.authModeFlag
	app.authModeFlag = string(protect.ModeEmbedded)
	t.Cleanup(func() { app.authModeFlag = prev })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/registry/list?host=registry.example.com", nil)
	registryListHandler(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestRegistryListHandler_MissingHost(t *testing.T) {
	withAuthBypassed(t)
	withRegistryClient(t, registry.NewClient())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/registry/list", nil)
	registryListHandler(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "host") {
		t.Fatalf("body should mention host: %s", rr.Body.String())
	}
}

func TestRegistryListHandler_NoClient(t *testing.T) {
	withAuthBypassed(t)
	prev := app.registryClient
	app.registryClient = nil
	t.Cleanup(func() { app.registryClient = prev })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/registry/list?host=registry.example.com", nil)
	registryListHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestRegistryListHandler_HappyPath(t *testing.T) {
	withAuthBypassed(t)

	// Spin up a fake registry that the client will hit.
	fake := &fakeRegistryServer{
		repos: []string{"chrome", "firefox"},
		tags: map[string][]string{
			"chrome":  {"131.0"},
			"firefox": {"latest"},
		},
	}
	regSrv := httptest.NewServer(fake.handler())
	t.Cleanup(regSrv.Close)

	withRegistryClient(t, registry.NewClient())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/registry/list?host="+regSrv.URL, nil)
	registryListHandler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var got registry.Listing
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(got.Repos))
	}
}

func TestRegistryPullHandler_MethodNotAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/registry/pull", nil)
	registryPullHandler(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestRegistryPullHandler_ForbiddenWithoutAdmin(t *testing.T) {
	prev := app.authModeFlag
	app.authModeFlag = string(protect.ModeEmbedded)
	t.Cleanup(func() { app.authModeFlag = prev })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/registry/pull",
		bytes.NewBufferString(`{"host":"x","refs":[{"repo":"y","tag":"z"}]}`))
	registryPullHandler(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestRegistryPullHandler_DockerDisabled(t *testing.T) {
	withAuthBypassed(t)
	withDockerDisabled(t)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/registry/pull",
		bytes.NewBufferString(`{"host":"x","refs":[{"repo":"y","tag":"z"}]}`))
	registryPullHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestRegistryPullHandler_BadJSON(t *testing.T) {
	withAuthBypassed(t)
	// Pretend docker is enabled so we don't short-circuit on disable check.
	prevDisable := app.disableDocker
	app.disableDocker = false
	t.Cleanup(func() { app.disableDocker = prevDisable })
	prevCli := app.cli
	// app.cli is *client.Client; nil is fine because we bail before using it.
	app.cli = nil
	t.Cleanup(func() { app.cli = prevCli })

	// disableDocker=false but cli=nil also triggers 503 (the safety guard
	// covers the test-build where no Docker daemon is wired up).
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/registry/pull",
		bytes.NewBufferString(`not-json`))
	registryPullHandler(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil cli should yield 503, got %d", rr.Code)
	}
}

// --- fakeRegistryServer: a thin OCI v2 fake reused for handler tests. ---

type fakeRegistryServer struct {
	repos []string
	tags  map[string][]string
}

func (f *fakeRegistryServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string][]string{
				"repositories": f.repos,
			})
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.HasSuffix(r.URL.Path, "/tags/list"):
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
			tags, ok := f.tags[repo]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": repo,
				"tags": tags,
			})
		default:
			fmt.Fprintln(w, "unknown")
		}
	})
}
