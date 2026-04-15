package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/docker/docker/client"
	assert "github.com/stretchr/testify/require"
)

func TestEnsureBrowserNetworkEmptyIsNoOp(t *testing.T) {
	err := EnsureBrowserNetwork(context.Background(), nil, "")
	assert.NoError(t, err)
}

func TestEnsureBrowserNetworkIdempotent(t *testing.T) {
	var createCalls, inspectCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.29/networks/selenwright-browsers", func(w http.ResponseWriter, r *http.Request) {
		inspectCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Id":"abc","Name":"selenwright-browsers"}`))
	})
	mux.HandleFunc("/v1.29/networks/create", func(w http.ResponseWriter, r *http.Request) {
		createCalls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"abc","Warning":""}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cl := dockerClient(t, srv)
	assert.NoError(t, EnsureBrowserNetwork(context.Background(), cl, "selenwright-browsers"))
	assert.Equal(t, 1, inspectCalls)
	assert.Equal(t, 0, createCalls)
}

func TestEnsureBrowserNetworkCreatesWhenMissing(t *testing.T) {
	var createBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.29/networks/selenwright-browsers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"network selenwright-browsers not found"}`))
	})
	mux.HandleFunc("/v1.29/networks/create", func(w http.ResponseWriter, r *http.Request) {
		createBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"Id":"abc","Warning":""}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cl := dockerClient(t, srv)
	assert.NoError(t, EnsureBrowserNetwork(context.Background(), cl, "selenwright-browsers"))

	var payload map[string]any
	assert.NoError(t, json.Unmarshal(createBody, &payload))
	assert.Equal(t, "selenwright-browsers", payload["Name"])
	assert.Equal(t, "bridge", payload["Driver"])
	assert.Equal(t, true, payload["Internal"],
		"browser network must be Internal=true so browsers have no default gateway")
	labels, _ := payload["Labels"].(map[string]any)
	assert.Equal(t, "true", labels["io.selenwright.managed"])
	assert.Equal(t, "browser", labels["io.selenwright.isolation"])
}

func TestEnsureBrowserNetworkPropagatesInspectErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.29/networks/selenwright-browsers", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"daemon broken"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cl := dockerClient(t, srv)
	err := EnsureBrowserNetwork(context.Background(), cl, "selenwright-browsers")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "inspect network")
}

func dockerClient(t *testing.T, srv *httptest.Server) *client.Client {
	t.Helper()
	u, err := url.Parse(srv.URL)
	assert.NoError(t, err)
	t.Setenv("DOCKER_HOST", "tcp://"+u.Host)
	t.Setenv("DOCKER_API_VERSION", "1.29")
	cl, err := client.NewClientWithOpts(client.FromEnv)
	assert.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}
