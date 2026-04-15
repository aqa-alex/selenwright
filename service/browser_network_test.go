package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
	assert "github.com/stretchr/testify/require"
)

func TestBrowserNetworkBecomesPrimaryAttachment(t *testing.T) {
	var createPayload map[string]any
	var secondaryConnects []string
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/v1.29/containers/create", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		_ = json.Unmarshal(body, &createPayload)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"abc","Warnings":[]}`))
	})
	mux.HandleFunc("/v1.29/containers/abc/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.29/containers/abc", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1.29/containers/abc/json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"test aborts inspect"}`))
	})
	mux.HandleFunc("/v1.29/networks/operator-net/connect", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		secondaryConnects = append(secondaryConnects, "operator-net")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1.29/networks/selenwright-browsers/connect", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		secondaryConnects = append(secondaryConnects, "selenwright-browsers")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1.29/networks/selenwright-browsers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Id":"netabc","Name":"selenwright-browsers"}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	assert.NoError(t, err)
	t.Setenv("DOCKER_HOST", "tcp://"+u.Host)
	t.Setenv("DOCKER_API_VERSION", "1.29")
	cl, err := client.NewClientWithOpts(client.FromEnv)
	assert.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })

	cfg := config.NewConfig()
	cfg.Browsers["firefox"] = config.Versions{
		Default: "33.0",
		Versions: map[string]*config.Browser{
			"33.0": {
				Image: "selenwright/firefox:33.0",
				Port:  "4444",
			},
		},
	}
	env := &Environment{
		Network:        "operator-net",
		BrowserNetwork: "selenwright-browsers",
	}
	mgr := DefaultManager{Environment: env, Client: cl, Config: cfg}
	caps := session.Caps{Name: "firefox", Version: "33.0"}
	starter, ok := mgr.Find(caps, 42)
	assert.True(t, ok)
	_, _ = starter.StartWithCancel()

	mu.Lock()
	defer mu.Unlock()
	hostCfg, _ := createPayload["HostConfig"].(map[string]any)
	assert.Equal(t, "selenwright-browsers", hostCfg["NetworkMode"],
		"BrowserNetwork must take precedence as the primary NetworkMode")
	assert.Contains(t, secondaryConnects, "operator-net",
		"-container-network value must be attached as a secondary network")
	assert.NotContains(t, secondaryConnects, "selenwright-browsers",
		"primary network must not be attached a second time")
}
