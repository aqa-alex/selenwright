package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRegistry implements the subset of OCI Registry API v2 that Client.List
// uses. Configurable per-test via the maps below. The handler is exposed so
// tests can wrap it for fault injection.
type fakeRegistry struct {
	mu       sync.Mutex
	repos    []string
	tagsByRepo map[string][]string
	pageSize int

	// failTagsFor causes /v2/<repo>/tags/list to return 500 for the named repo.
	failTagsFor string
	// notFoundCatalog returns 404 from /v2/_catalog.
	notFoundCatalog bool
	// requireAuthCatalog returns 401 from /v2/_catalog.
	requireAuthCatalog bool

	tagCalls atomic.Int32
}

func (f *fakeRegistry) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/_catalog":
			f.serveCatalog(w, r)
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.HasSuffix(r.URL.Path, "/tags/list"):
			f.serveTags(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (f *fakeRegistry) serveCatalog(w http.ResponseWriter, r *http.Request) {
	if f.notFoundCatalog {
		http.NotFound(w, r)
		return
	}
	if f.requireAuthCatalog {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	pageSize := f.pageSize
	if pageSize <= 0 {
		pageSize = catalogPageSize
	}
	last := r.URL.Query().Get("last")

	f.mu.Lock()
	all := slices.Clone(f.repos)
	f.mu.Unlock()

	start := 0
	if last != "" {
		for i, repo := range all {
			if repo == last {
				start = i + 1
				break
			}
		}
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	if end < len(all) {
		nextURL := fmt.Sprintf("/v2/_catalog?last=%s&n=%d", url.QueryEscape(page[len(page)-1]), pageSize)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, nextURL))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(catalogPage{Repositories: page})
}

func (f *fakeRegistry) serveTags(w http.ResponseWriter, r *http.Request) {
	f.tagCalls.Add(1)
	repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")

	if repo == f.failTagsFor {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}

	f.mu.Lock()
	tags, ok := f.tagsByRepo[repo]
	f.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tagListResponse{
		Name: repo,
		Tags: slices.Clone(tags),
	})
}

func newClientForServer(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := NewClient()
	// Override the http client timeout to be generous for slow CI but still
	// finite (we deliberately keep the default for production code).
	c.http.Timeout = 5 * time.Second
	return c
}

func hostFromServer(srv *httptest.Server) string {
	// httptest serves over http; the Client must accept "http://..." input.
	return srv.URL
}

func TestList_Success(t *testing.T) {
	t.Parallel()

	fake := &fakeRegistry{
		repos: []string{"chrome", "firefox"},
		tagsByRepo: map[string][]string{
			"chrome":  {"131.0", "130.0"},
			"firefox": {"latest"},
		},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientForServer(t, srv)
	got, err := c.List(context.Background(), hostFromServer(srv))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d (%+v)", len(got.Repos), got.Repos)
	}
	if got.Repos[0].Repo != "chrome" || got.Repos[1].Repo != "firefox" {
		t.Fatalf("repos not sorted: %+v", got.Repos)
	}
	if !slices.Equal(got.Repos[0].Tags, []string{"130.0", "131.0"}) {
		t.Fatalf("chrome tags: %+v", got.Repos[0].Tags)
	}
	if len(got.Errors) != 0 {
		t.Fatalf("unexpected per-repo errors: %+v", got.Errors)
	}
}

func TestList_Pagination(t *testing.T) {
	t.Parallel()

	repos := make([]string, 0, 250)
	tags := map[string][]string{}
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("repo-%03d", i)
		repos = append(repos, name)
		tags[name] = []string{"v1"}
	}
	fake := &fakeRegistry{
		repos:      repos,
		tagsByRepo: tags,
		pageSize:   100,
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientForServer(t, srv)
	got, err := c.List(context.Background(), hostFromServer(srv))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Repos) != 250 {
		t.Fatalf("want 250 repos, got %d", len(got.Repos))
	}
	// Ensure no duplicates and stable sort.
	for i := 1; i < len(got.Repos); i++ {
		if got.Repos[i].Repo == got.Repos[i-1].Repo {
			t.Fatalf("duplicate repo at index %d: %s", i, got.Repos[i].Repo)
		}
	}
}

func TestList_PerRepoFailureRecorded(t *testing.T) {
	t.Parallel()

	fake := &fakeRegistry{
		repos: []string{"chrome", "firefox"},
		tagsByRepo: map[string][]string{
			"chrome":  {"131.0"},
			"firefox": {"latest"},
		},
		failTagsFor: "firefox",
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientForServer(t, srv)
	got, err := c.List(context.Background(), hostFromServer(srv))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got.Errors) != 1 || got.Errors[0].Repo != "firefox" {
		t.Fatalf("expected one firefox error, got %+v", got.Errors)
	}
	// Other repo still listed.
	chromeFound := false
	for _, r := range got.Repos {
		if r.Repo == "chrome" && slices.Equal(r.Tags, []string{"131.0"}) {
			chromeFound = true
		}
		if r.Repo == "firefox" && len(r.Tags) != 0 {
			t.Fatalf("firefox tags should be empty on failure: %+v", r.Tags)
		}
	}
	if !chromeFound {
		t.Fatalf("chrome listing missing")
	}
}

func TestList_CatalogNotFound(t *testing.T) {
	t.Parallel()

	fake := &fakeRegistry{notFoundCatalog: true}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientForServer(t, srv)
	_, err := c.List(context.Background(), hostFromServer(srv))
	if err == nil || !strings.Contains(err.Error(), "_catalog") {
		t.Fatalf("expected catalog 404 error, got %v", err)
	}
}

func TestList_CatalogRequiresAuth(t *testing.T) {
	t.Parallel()

	fake := &fakeRegistry{requireAuthCatalog: true}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := newClientForServer(t, srv)
	_, err := c.List(context.Background(), hostFromServer(srv))
	if err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("expected auth-required error, got %v", err)
	}
}

func TestList_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Server that hangs on /v2/_catalog until the context is cancelled.
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold
	}))
	t.Cleanup(func() {
		close(hold)
		srv.Close()
	})

	c := newClientForServer(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.List(ctx, hostFromServer(srv))
	if err == nil {
		t.Fatalf("expected error from cancelled context")
	}
}

func TestList_EmptyHostRejected(t *testing.T) {
	t.Parallel()

	c := NewClient()
	_, err := c.List(context.Background(), "")
	if err == nil {
		t.Fatalf("expected error for empty host")
	}
}

func TestList_InvalidHostRejected(t *testing.T) {
	t.Parallel()

	c := NewClient()
	for _, bad := range []string{"reg /sub", "reg with space", "host:abc"} {
		if _, err := c.List(context.Background(), bad); err == nil {
			t.Errorf("expected error for invalid host %q", bad)
		}
	}
}

func TestPullRef_Roundtrip(t *testing.T) {
	t.Parallel()

	cases := []struct {
		host, repo, tag, want string
		wantErr                bool
	}{
		{"registry.example.com", "chrome", "131.0", "registry.example.com/chrome:131.0", false},
		{"localhost:5000", "ns/sub/img", "latest", "localhost:5000/ns/sub/img:latest", false},
		{"http://localhost:5000", "img", "v1", "localhost:5000/img:v1", false},
		{"", "img", "v1", "", true},
		{"host", "", "v1", "", true},
		{"host", "img", "", "", true},
		{"bad host", "img", "v1", "", true},
	}
	for _, tc := range cases {
		got, err := PullRef(tc.host, tc.repo, tc.tag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("PullRef(%q,%q,%q) want error, got %q", tc.host, tc.repo, tc.tag, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("PullRef(%q,%q,%q) error: %v", tc.host, tc.repo, tc.tag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("PullRef(%q,%q,%q) = %q, want %q", tc.host, tc.repo, tc.tag, got, tc.want)
		}
	}
}

func TestNormalizeBaseURL_Schemes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, baseURL, host string
		wantErr            bool
	}{
		{"registry.example.com", "https://registry.example.com", "registry.example.com", false},
		{"http://localhost:5000", "http://localhost:5000", "localhost:5000", false},
		{"https://hub.example/", "https://hub.example", "hub.example", false},
		{"", "", "", true},
		{"reg with space", "", "", true},
	}
	for _, tc := range cases {
		base, host, err := normalizeBaseURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("normalizeBaseURL(%q) want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeBaseURL(%q): %v", tc.in, err)
			continue
		}
		if base != tc.baseURL || host != tc.host {
			t.Errorf("normalizeBaseURL(%q) = (%q,%q), want (%q,%q)", tc.in, base, host, tc.baseURL, tc.host)
		}
	}
}

func TestClassifyInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in        string
		wantSrc   Source
		wantValue string
		wantErr   bool
	}{
		// OCI host inputs
		{"registry.example.com", SourceOCI, "registry.example.com", false},
		{"localhost:5000", SourceOCI, "localhost:5000", false},
		{"http://localhost:5000", SourceOCI, "http://localhost:5000", false},
		{"https://reg.example.com/", SourceOCI, "https://reg.example.com", false},
		{"reg.example.com:443", SourceOCI, "reg.example.com:443", false},
		// Hub namespaces
		{"selenwright", SourceHub, "selenwright", false},
		{"library", SourceHub, "library", false},
		{"my-org_2", SourceHub, "my-org_2", false},
		{"SelenWright", SourceHub, "selenwright", false}, // case-folded
		// Hub aliases
		{"docker.io/selenwright", SourceHub, "selenwright", false},
		{"hub.docker.com/selenwright", SourceHub, "selenwright", false},
		{"docker.io/selenwright/chrome", SourceHub, "selenwright", false}, // path beyond ns ignored
		{"https://hub.docker.com/r/selenwright/", SourceHub, "selenwright", false},
		// Bare Hub host w/o namespace -> error
		{"docker.io", "", "", true},
		{"hub.docker.com", "", "", true},
		// Empty / malformed
		{"", "", "", true},
		{"   ", "", "", true},
		{"reg with space", "", "", true},
		{"https://", "", "", true},
		// Slashes inside a plain word -> rejected as Hub namespace
		{"a/b/c", "", "", true},
	}

	for _, tc := range cases {
		got, err := ClassifyInput(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ClassifyInput(%q) want error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ClassifyInput(%q): %v", tc.in, err)
			continue
		}
		if got.Source != tc.wantSrc || got.Value != tc.wantValue {
			t.Errorf("ClassifyInput(%q) = (%s,%q), want (%s,%q)", tc.in, got.Source, got.Value, tc.wantSrc, tc.wantValue)
		}
	}
}

func TestHubPullRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ns, repo, tag, want string
		wantErr             bool
	}{
		{"selenwright", "chrome", "v1.71.0", "selenwright/chrome:v1.71.0", false},
		{"library", "alpine", "3.20", "library/alpine:3.20", false},
		{"", "chrome", "v1", "", true},
		{"selenwright", "", "v1", "", true},
		{"selenwright", "chrome", "", "", true},
		{"BadNS!", "chrome", "v1", "", true},
		{"selenwright", "chr/ome", "v1", "", true},
	}
	for _, tc := range cases {
		got, err := HubPullRef(tc.ns, tc.repo, tc.tag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("HubPullRef(%q,%q,%q) want error, got %q", tc.ns, tc.repo, tc.tag, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("HubPullRef(%q,%q,%q): %v", tc.ns, tc.repo, tc.tag, err)
			continue
		}
		if got != tc.want {
			t.Errorf("HubPullRef(%q,%q,%q) = %q, want %q", tc.ns, tc.repo, tc.tag, got, tc.want)
		}
	}
}

// fakeHub serves the subset of Docker Hub Web API that ListDockerHub uses.
type fakeHub struct {
	repos     []string
	tagsByRepo map[string][]string
}

func (f *fakeHub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /v2/repositories/{ns}/?page_size=...
		// /v2/repositories/{ns}/{repo}/tags/?page_size=...
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// /v2/repositories/{ns}            -> 3 segments, namespace listing
		// /v2/repositories/{ns}/{repo}/tags -> 5 segments, tag listing
		if len(segs) >= 3 && segs[0] == "v2" && segs[1] == "repositories" {
			tail := segs[3:]
			if len(tail) == 0 {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": resultsFromNames(f.repos),
				})
				return
			}
			if len(tail) >= 2 && tail[len(tail)-1] == "tags" {
				repo := tail[0]
				tags, ok := f.tagsByRepo[repo]
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"results": resultsFromNames(tags),
				})
				return
			}
		}
		http.NotFound(w, r)
	})
}

func resultsFromNames(names []string) []map[string]string {
	out := make([]map[string]string, len(names))
	for i, n := range names {
		out[i] = map[string]string{"name": n}
	}
	return out
}

func TestListDockerHub_AgainstFake(t *testing.T) {
	t.Parallel()

	fake := &fakeHub{
		repos: []string{"chrome", "firefox"},
		tagsByRepo: map[string][]string{
			"chrome":  {"v1.0", "v1.1"},
			"firefox": {"latest"},
		},
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	c := NewClient()
	c.http.Timeout = 5 * time.Second

	// Override the hub base URL by hitting a private helper that lets tests
	// substitute a transport. The simplest way without exposing internals is
	// to swap the http.Transport via DialContext. Instead we use a thin
	// helper: rewrite hub.docker.com requests to the fake server via a
	// custom RoundTripper.
	c.http.Transport = &rewritingTransport{base: srv.URL}

	got, err := c.ListDockerHub(context.Background(), "selenwright")
	if err != nil {
		t.Fatalf("ListDockerHub: %v", err)
	}
	if got.Source != SourceHub || got.Namespace != "selenwright" {
		t.Fatalf("source/ns = (%s,%q), want (hub,selenwright)", got.Source, got.Namespace)
	}
	if len(got.Repos) != 2 {
		t.Fatalf("want 2 repos, got %d", len(got.Repos))
	}
	for _, r := range got.Repos {
		if r.Repo == "chrome" && !slices.Equal(r.Tags, []string{"v1.0", "v1.1"}) {
			t.Errorf("chrome tags: %+v", r.Tags)
		}
	}
}

func TestListDockerHub_NamespaceNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := NewClient()
	c.http.Timeout = 5 * time.Second
	c.http.Transport = &rewritingTransport{base: srv.URL}

	_, err := c.ListDockerHub(context.Background(), "nonexistent-ns-xxx")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// rewritingTransport rewrites every outgoing URL host+scheme to the fake
// server's base, preserving the path/query. This lets the test exercise the
// real Hub client code path with a stand-in upstream.
type rewritingTransport struct {
	base string
}

func (rt *rewritingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base, err := url.Parse(rt.base)
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = base.Scheme
	cloned.URL.Host = base.Host
	cloned.Host = base.Host
	return http.DefaultTransport.RoundTrip(cloned)
}

func TestResolveLinkNext(t *testing.T) {
	t.Parallel()

	cases := []struct {
		baseURL, header, want string
	}{
		{"https://reg.example", `</v2/_catalog?last=z&n=100>; rel="next"`, "https://reg.example/v2/_catalog?last=z&n=100"},
		{"https://reg.example", `<https://other/v2/_catalog?last=z>; rel="next"`, "https://other/v2/_catalog?last=z"},
		{"https://reg.example", `</foo>; rel="prev", </bar>; rel="next"`, "https://reg.example/bar"},
		{"https://reg.example", "", ""},
		{"https://reg.example", `</foo>; rel="prev"`, ""},
	}
	for _, tc := range cases {
		got := resolveLinkNext(tc.baseURL, tc.header)
		if got != tc.want {
			t.Errorf("resolveLinkNext(%q, %q) = %q, want %q", tc.baseURL, tc.header, got, tc.want)
		}
	}
}
