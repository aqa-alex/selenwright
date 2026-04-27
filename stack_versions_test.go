package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseImageRef(t *testing.T) {
	cases := []struct {
		name    string
		ref     string
		want    imageRef
		wantErr bool
	}{
		{"selenwright tagged", "selenwright/hub:v1.0.0",
			imageRef{Registry: "docker.io", Namespace: "selenwright", Repo: "hub", Tag: "v1.0.0"}, false},
		{"library implicit", "nginx:1.27-alpine",
			imageRef{Registry: "docker.io", Namespace: "library", Repo: "nginx", Tag: "1.27-alpine"}, false},
		{"library explicit no tag", "library/nginx",
			imageRef{Registry: "docker.io", Namespace: "library", Repo: "nginx", Tag: "latest"}, false},
		{"docker.io explicit", "docker.io/selenwright/hub:v1.2.3",
			imageRef{Registry: "docker.io", Namespace: "selenwright", Repo: "hub", Tag: "v1.2.3"}, false},
		{"ghcr.io with tag", "ghcr.io/x/y:z",
			imageRef{Registry: "ghcr.io", Namespace: "x", Repo: "y", Tag: "z"}, false},
		{"localhost registry", "localhost:5000/team/app:dev",
			imageRef{Registry: "localhost:5000", Namespace: "team", Repo: "app", Tag: "dev"}, false},
		{"digest stripped", "selenwright/hub:v1.0.0@sha256:abc",
			imageRef{Registry: "docker.io", Namespace: "selenwright", Repo: "hub", Tag: "v1.0.0"}, false},
		{"empty ref", "", imageRef{}, true},
		{"only colon", ":bad", imageRef{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseImageRef(tc.ref)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeTag(t *testing.T) {
	cases := map[string]string{
		"v1.0.0":      "v1.0.0",
		"v.1.0.0":     "v1.0.0", // the screenshot quirk
		"v..1.2.3":    "v1.2.3",
		"1.0.0":       "v1.0.0",
		"V1.2.3":      "", // semver pkg requires lowercase v
		"v1.0.0-rc1":  "v1.0.0-rc1",
		"v1.0.0+meta": "v1.0.0", // build metadata is dropped by semver canonicalisation
		"latest":      "",
		"":            "",
		"   v1.0.0  ": "v1.0.0",
		"notasemver":  "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, normalizeTag(in))
		})
	}
}

func TestPickCandidateVersions(t *testing.T) {
	t.Run("newer than current, no prereleases", func(t *testing.T) {
		// "v.1.2.0" is the screenshot-style quirky tag.
		avail := []string{"v0.9.0", "v1.0.0", "v1.0.1", "v1.1.0", "v.1.2.0", "v2.0.0-rc1", "latest", "floating"}
		got := pickCandidateVersions("v1.0.0", avail, 5)
		// Order: descending semver. The original tag string is preserved so
		// `docker pull` can use it verbatim — Hub's "v.1.2.0" must round-trip.
		assert.Equal(t, []string{"v.1.2.0", "v1.1.0", "v1.0.1"}, got)
	})

	t.Run("respects max", func(t *testing.T) {
		avail := []string{"v1.1.0", "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0"}
		got := pickCandidateVersions("v1.0.0", avail, 2)
		assert.Equal(t, []string{"v1.5.0", "v1.4.0"}, got)
	})

	t.Run("dedupe by canonical, first occurrence wins", func(t *testing.T) {
		avail := []string{"v1.1.0", "v.1.1.0", "1.1.0"}
		got := pickCandidateVersions("v1.0.0", avail, 5)
		require.Len(t, got, 1)
		assert.Equal(t, "v1.1.0", got[0])
	})

	t.Run("prerelease current allows prerelease candidates", func(t *testing.T) {
		avail := []string{"v2.0.0-rc1", "v2.0.0-rc2", "v2.0.0", "v1.9.0"}
		got := pickCandidateVersions("v2.0.0-rc1", avail, 5)
		assert.Contains(t, got, "v2.0.0-rc2")
		assert.Contains(t, got, "v2.0.0")
		assert.NotContains(t, got, "v1.9.0")
	})

	t.Run("empty current shows everything valid", func(t *testing.T) {
		avail := []string{"v1.0.0", "garbage", "v0.5.0"}
		got := pickCandidateVersions("", avail, 5)
		assert.Equal(t, []string{"v1.0.0", "v0.5.0"}, got)
	})
}

func TestFetchHubTags_Pagination(t *testing.T) {
	var srv2URL atomic.Pointer[string]
	var page1Calls, page2Calls atomic.Int32

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"next":null,"results":[{"name":"v1.1.0","last_updated":"2025-02-01T00:00:00Z"}]}`))
	}))
	defer srv2.Close()
	u := srv2.URL + "/v2/repositories/selenwright/hub/tags?page_size=100&page=2"
	srv2URL.Store(&u)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page1Calls.Add(1)
		require.Contains(t, r.URL.Path, "/selenwright/hub/tags")
		w.Header().Set("Content-Type", "application/json")
		next := *srv2URL.Load()
		_, _ = w.Write([]byte(`{"next":"` + next + `","results":[{"name":"v1.0.0","last_updated":"2025-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	cache := newHubTagCache()
	cache.baseURL = srv.URL
	got, err := cache.fetchHubTags(context.Background(), "selenwright", "hub")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "v1.0.0", got[0].Name)
	assert.Equal(t, "v1.1.0", got[1].Name)
	assert.Equal(t, int32(1), page1Calls.Load())
	assert.Equal(t, int32(1), page2Calls.Load())

	// Second call hits the cache — no new HTTP requests.
	got2, err := cache.fetchHubTags(context.Background(), "selenwright", "hub")
	require.NoError(t, err)
	assert.Equal(t, got, got2)
	assert.Equal(t, int32(1), page1Calls.Load(), "page1 should not be re-fetched on cache hit")
	assert.Equal(t, int32(1), page2Calls.Load(), "page2 should not be re-fetched on cache hit")
}

func TestFetchHubTags_NotFound(t *testing.T) {
	calls := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cache := newHubTagCache()
	cache.baseURL = srv.URL
	_, err := cache.fetchHubTags(context.Background(), "selenwright", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Negative-cache hit: same error returned without re-hitting the server.
	_, err2 := cache.fetchHubTags(context.Background(), "selenwright", "missing")
	require.Error(t, err2)
	assert.Equal(t, err.Error(), err2.Error())
	assert.Equal(t, int32(1), calls.Load(), "second call should be served from negative cache")
}

func TestFetchHubTags_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"next":null,"results":[]}`))
	}))
	defer srv.Close()

	cache := newHubTagCache()
	cache.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := cache.fetchHubTags(ctx, "selenwright", "slow")
	require.Error(t, err)
}

func TestRenderOverrideYAML(t *testing.T) {
	plan := []stackUpdatePlanEntry{
		{Service: "selenwright-ui", Image: "selenwright/selenwright-ui:v1.1.0", Tag: "v1.1.0"},
		{Service: "selenwright", Image: "selenwright/hub:v1.1.0", Tag: "v1.1.0"},
	}
	got := renderOverrideYAML(plan)
	require.Contains(t, got, "services:")
	require.Contains(t, got, "  selenwright:\n    image: selenwright/hub:v1.1.0")
	require.Contains(t, got, "  selenwright-ui:\n    image: selenwright/selenwright-ui:v1.1.0")
	// Stable ordering: selenwright (alpha-first) precedes selenwright-ui.
	require.Less(t, strings.Index(got, "  selenwright:"), strings.Index(got, "  selenwright-ui:"))
}

func TestBuildUpdaterShellSnippet(t *testing.T) {
	yaml := "services:\n  selenwright:\n    image: selenwright/hub:v1.1.0\n"
	snippet := buildUpdaterShellSnippet(yaml, []string{"selenwright"})

	assert.Contains(t, snippet, "set -e")
	assert.Contains(t, snippet, `cat > "$OVERRIDE_PATH" <<'__SELENWRIGHT_OVERRIDE_EOF__'`)
	assert.Contains(t, snippet, `services:`)
	assert.Contains(t, snippet, `image: selenwright/hub:v1.1.0`)
	assert.Contains(t, snippet,
		`docker compose -f "$CONFIG_FILE" -f "$OVERRIDE_PATH" --project-directory "$WORKING_DIR" up -d --force-recreate 'selenwright'`)
	// Single-quoted heredoc is intentional — body must NOT be shell-expanded.
	assert.Contains(t, snippet, `<<'__SELENWRIGHT_OVERRIDE_EOF__'`)
}

func TestBuildUpdaterShellSnippet_RewritesCollidingDelimiter(t *testing.T) {
	// Adversarial: an operator-controlled string contains the delimiter.
	yaml := "services:\n  evil:\n    image: __SELENWRIGHT_OVERRIDE_EOF__\n"
	snippet := buildUpdaterShellSnippet(yaml, nil)
	assert.Contains(t, snippet, "__SELENWRIGHT_OVERRIDE_EOF_SAFE__")
	count := strings.Count(snippet, "__SELENWRIGHT_OVERRIDE_EOF__")
	assert.Equal(t, 2, count, "delimiter appears exactly twice (open + close)")
}

func TestBuildUpdaterShellSnippet_ForceRecreate(t *testing.T) {
	t.Run("multiple services sorted alphabetically", func(t *testing.T) {
		snippet := buildUpdaterShellSnippet("services:\n", []string{"selenwright-ui", "selenwright"})
		assert.Contains(t, snippet, `up -d --force-recreate 'selenwright' 'selenwright-ui'`)
	})
	t.Run("empty changedServices omits flag", func(t *testing.T) {
		snippet := buildUpdaterShellSnippet("services:\n", nil)
		assert.NotContains(t, snippet, "--force-recreate")
		assert.True(t, strings.HasSuffix(snippet, `up -d`),
			"snippet should end with bare `up -d` when no services changed; got: %q", snippet)
	})
	t.Run("service name with embedded single quote is escaped", func(t *testing.T) {
		snippet := buildUpdaterShellSnippet("services:\n", []string{`weird'name`})
		// Single-quote close, escaped quote, single-quote open: 'weird'\''name'
		assert.Contains(t, snippet, `--force-recreate 'weird'\''name'`)
	})
}

func TestExtendPlanWithCurrentPins(t *testing.T) {
	t.Run("preserves running version-aware services not in request", func(t *testing.T) {
		serviceRefs := map[string]imageRef{
			"selenwright":    {Namespace: "selenwright", Repo: "hub", Tag: "v1.0.2"},
			"selenwright-ui": {Namespace: "selenwright", Repo: "selenwright-ui", Tag: "v.1.0.0"},
			"proxy":          {Namespace: "library", Repo: "nginx", Tag: "1.27-alpine"},
		}
		requested := map[string]string{"selenwright-ui": "v1.0.3"}
		plan := []stackUpdatePlanEntry{
			{Service: "selenwright-ui", Image: "selenwright/selenwright-ui:v1.0.3", Tag: "v1.0.3"},
		}

		out := extendPlanWithCurrentPins(plan, serviceRefs, requested)

		// hub is preserved at its CURRENT tag — operator only asked to upgrade ui.
		var hub *stackUpdatePlanEntry
		for i := range out {
			if out[i].Service == "selenwright" {
				hub = &out[i]
			}
		}
		require.NotNil(t, hub, "hub must be in extended plan")
		assert.Equal(t, "selenwright/hub:v1.0.2", hub.Image)
		assert.Equal(t, "v1.0.2", hub.Tag)

		// proxy (non-selenwright namespace) is NOT pinned — base compose owns it.
		for _, p := range out {
			assert.NotEqual(t, "proxy", p.Service)
		}

		// ui entry from caller is preserved as-is.
		var ui *stackUpdatePlanEntry
		for i := range out {
			if out[i].Service == "selenwright-ui" {
				ui = &out[i]
			}
		}
		require.NotNil(t, ui)
		assert.Equal(t, "selenwright/selenwright-ui:v1.0.3", ui.Image)
	})

	t.Run("skips services with empty tag", func(t *testing.T) {
		serviceRefs := map[string]imageRef{
			"selenwright": {Namespace: "selenwright", Repo: "hub", Tag: ""},
		}
		out := extendPlanWithCurrentPins(nil, serviceRefs, nil)
		assert.Empty(t, out)
	})

	t.Run("handles dotted-v Hub quirk by preserving original tag", func(t *testing.T) {
		// "v.1.0.0" must be written to the override file VERBATIM — that's
		// the only string `docker pull` will resolve. Canonicalisation is
		// internal, never on-disk.
		serviceRefs := map[string]imageRef{
			"selenwright-ui": {Namespace: "selenwright", Repo: "selenwright-ui", Tag: "v.1.0.0"},
		}
		out := extendPlanWithCurrentPins(nil, serviceRefs, nil)
		require.Len(t, out, 1)
		assert.Equal(t, "selenwright/selenwright-ui:v.1.0.0", out[0].Image)
		assert.Equal(t, "v.1.0.0", out[0].Tag)
	})
}

func TestFindCandidateByCanonical(t *testing.T) {
	t.Run("direct match", func(t *testing.T) {
		got, ok := findCandidateByCanonical([]string{"v1.1.0", "v1.0.5"}, "v1.1.0")
		assert.True(t, ok)
		assert.Equal(t, "v1.1.0", got)
	})
	t.Run("normalised match returns original", func(t *testing.T) {
		got, ok := findCandidateByCanonical([]string{"v.1.1.0"}, "v1.1.0")
		assert.True(t, ok)
		assert.Equal(t, "v.1.1.0", got, "must return the published tag form, not canonical")
	})
	t.Run("no match", func(t *testing.T) {
		_, ok := findCandidateByCanonical([]string{"v1.1.0"}, "v0.9.0")
		assert.False(t, ok)
	})
}
