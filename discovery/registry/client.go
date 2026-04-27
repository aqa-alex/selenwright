// Package registry implements an anonymous OCI Distribution Spec (Registry API v2)
// client used by the operator UI to enumerate browser images on an arbitrary
// registry host before they are pulled into the local Docker daemon.
//
// The local-only auto-discovery path (see ../../discovery) only sees images that
// already exist locally. This package adds the missing "pull from somewhere
// else" half: list /v2/_catalog, list /v2/<repo>/tags/list, hand the result to
// the UI, then let the existing app.cli.ImagePull pipeline bring chosen tags
// into the daemon.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	catalogPageSize = 100
	maxCatalogPages = 10 // safety cap: 1000 repos per host
	tagListPageSize = 200
	maxTagPages     = 5 // 1000 tags per repo

	defaultListTimeout  = 15 * time.Second
	tagFetchConcurrency = 4

	// Docker Hub Web API (not OCI). Anonymous, no auth required for public namespaces.
	hubReposBaseURL = "https://hub.docker.com/v2/repositories"
	hubPageSize     = 100
	hubMaxPages     = 10
)

// Source identifies which API was used to produce a Listing.
type Source string

const (
	SourceOCI Source = "oci"
	SourceHub Source = "hub"
)

// Client is an HTTP client for OCI Registry API v2. Anonymous reads only.
type Client struct {
	http *http.Client
}

// NewClient returns a Client with sensible defaults for read-only operations.
func NewClient() *Client {
	return &Client{
		http: &http.Client{Timeout: defaultListTimeout},
	}
}

// Listing is the response shape returned to the UI for one registry host or
// Hub namespace: a flat list of repositories with their tag sets.
type Listing struct {
	Host      string         `json:"host"`
	BaseURL   string         `json:"baseUrl"`
	Source    Source         `json:"source"`
	Namespace string         `json:"namespace,omitempty"` // populated only for Source=="hub"
	Repos     []RepoListing  `json:"repos"`
	Errors    []ListingError `json:"errors,omitempty"`
}

// RepoListing is one repository on the host.
type RepoListing struct {
	Repo string   `json:"repo"`
	Tags []string `json:"tags"`
}

// ListingError records a per-repository fetch failure that did not abort the
// overall listing.
type ListingError struct {
	Repo  string `json:"repo"`
	Error string `json:"error"`
}

// linkRel matches a single relation in an RFC 5988 Link header value.
// Spec: <next-url>; rel="next"
var linkRel = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="([^"]+)"`)

// validHostPattern accepts host[:port]; rejects schemes, paths, query strings,
// and obviously malformed input. We keep it conservative because the value
// goes into URL construction.
var validHostPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(?::[0-9]+)?$`)

// hubNamespacePattern matches the constraints Docker Hub places on
// namespace identifiers: lowercase alphanumeric plus _ and -.
// Reference: docs.docker.com/reference/cli/docker/image/tag (repository names).
var hubNamespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,254}$`)

// ClassifiedInput is the result of parsing whatever the user typed in the
// "registry" field. Source is either OCI (a registry host) or Hub (a Docker
// Hub namespace); Value holds the canonical form.
type ClassifiedInput struct {
	Source    Source
	Value     string // OCI: host[:port]; Hub: namespace
}

// ClassifyInput parses a free-form input and decides whether it points at an
// OCI v2 registry host or a Docker Hub namespace. Empty / malformed input
// returns an error suitable for surfacing in the UI.
//
// Heuristic:
//   - explicit http(s):// scheme  -> OCI host
//   - input starts with "docker.io" or "hub.docker.com" -> Hub (namespace
//     extracted from the path; bare host without a namespace is rejected)
//   - input contains "." or ":"   -> OCI host
//   - otherwise                   -> Hub namespace
func ClassifyInput(raw string) (ClassifiedInput, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ClassifiedInput{}, fmt.Errorf("empty input")
	}

	hasScheme := false
	scheme := ""
	if rest, ok := strings.CutPrefix(s, "http://"); ok {
		s = rest
		hasScheme = true
		scheme = "http://"
	} else if rest, ok := strings.CutPrefix(s, "https://"); ok {
		s = rest
		hasScheme = true
		scheme = "https://"
	}
	s = strings.Trim(s, "/")
	if s == "" {
		return ClassifiedInput{}, fmt.Errorf("empty input after stripping scheme")
	}

	// Explicit "docker.io" / "hub.docker.com" in the host part means Hub even
	// though the string contains a "."; treat the rest of the path as the
	// namespace and accept docker.io/<namespace> as a friendly form.
	for _, alias := range []string{"hub.docker.com", "docker.io"} {
		if s == alias {
			return ClassifiedInput{}, fmt.Errorf("Docker Hub requires a namespace, e.g. %q", "selenwright")
		}
		prefix := alias + "/"
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			rest = strings.Trim(rest, "/")
			// Hub's website uses /r/<ns>/<repo>, /u/<ns> and /_/<repo>
			// (library-namespace shortcut). Strip those so the next segment
			// is the actual namespace.
			for _, webPrefix := range []string{"r/", "u/", "_/"} {
				if trimmed, ok2 := strings.CutPrefix(rest, webPrefix); ok2 {
					rest = trimmed
					break
				}
			}
			return classifyHubNamespace(rest)
		}
	}

	// Explicit scheme always means OCI.
	if hasScheme {
		if !validHostPattern.MatchString(s) {
			return ClassifiedInput{}, fmt.Errorf("invalid host %q", s)
		}
		return ClassifiedInput{Source: SourceOCI, Value: scheme + s}, nil
	}

	// Looks like a host (has "." or port separator).
	if strings.ContainsAny(s, ".:") {
		if !validHostPattern.MatchString(s) {
			return ClassifiedInput{}, fmt.Errorf("invalid host %q", s)
		}
		return ClassifiedInput{Source: SourceOCI, Value: s}, nil
	}

	// Plain word — must be a clean Hub namespace. Reject inputs like "a/b/c"
	// here so the user gets a clear error instead of a silent first-segment
	// guess; the docker.io/<ns>/<repo> path still trims slashes leniently.
	ns := strings.ToLower(s)
	if !hubNamespacePattern.MatchString(ns) {
		return ClassifiedInput{}, fmt.Errorf("invalid Docker Hub namespace %q", s)
	}
	return ClassifiedInput{Source: SourceHub, Value: ns}, nil
}

// classifyHubNamespace validates rest as a Docker Hub namespace, lower-casing
// it and trimming any trailing path segments. Used by ClassifyInput.
func classifyHubNamespace(rest string) (ClassifiedInput, error) {
	rest = strings.Trim(rest, "/")
	if i := strings.Index(rest, "/"); i >= 0 {
		// docker.io/<ns>/<repo>... — only the namespace is meaningful here.
		rest = rest[:i]
	}
	ns := strings.ToLower(strings.TrimSpace(rest))
	if ns == "" {
		return ClassifiedInput{}, fmt.Errorf("Docker Hub requires a namespace, e.g. %q", "selenwright")
	}
	if !hubNamespacePattern.MatchString(ns) {
		return ClassifiedInput{}, fmt.Errorf("invalid Docker Hub namespace %q", ns)
	}
	return ClassifiedInput{Source: SourceHub, Value: ns}, nil
}

// List walks /v2/_catalog (paginated via Link header) and then /v2/<repo>/tags/list
// for each repository concurrently. Per-repository errors are recorded in the
// returned Listing.Errors slice but never abort the run; only a hard failure
// against /v2/_catalog returns an error.
//
// host may be "registry.example.com", "registry.example.com:5000", or
// "http://localhost:5000" / "https://...". When no scheme is present, https
// is assumed.
func (c *Client) List(ctx context.Context, host string) (Listing, error) {
	baseURL, normHost, err := normalizeBaseURL(host)
	if err != nil {
		return Listing{}, err
	}

	listing := Listing{Host: normHost, BaseURL: baseURL, Source: SourceOCI}

	repos, err := c.listCatalog(ctx, baseURL)
	if err != nil {
		return listing, fmt.Errorf("catalog: %w", err)
	}
	if len(repos) == 0 {
		listing.Repos = []RepoListing{}
		return listing, nil
	}

	results := make([]RepoListing, len(repos))
	errsMu := sync.Mutex{}
	var perRepoErrs []ListingError

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(tagFetchConcurrency)
	for i, repo := range repos {
		i, repo := i, repo
		g.Go(func() error {
			tags, terr := c.listTags(gctx, baseURL, repo)
			if terr != nil {
				log.Printf("[-] [REGISTRY] [%s tags %s: %v]", normHost, repo, terr)
				errsMu.Lock()
				perRepoErrs = append(perRepoErrs, ListingError{Repo: repo, Error: terr.Error()})
				errsMu.Unlock()
				results[i] = RepoListing{Repo: repo, Tags: []string{}}
				return nil
			}
			results[i] = RepoListing{Repo: repo, Tags: tags}
			return nil
		})
	}
	// errgroup with SetLimit + nil returns from goroutines never produces an
	// error here; we still check to satisfy the contract.
	_ = g.Wait()

	listing.Repos = results
	if len(perRepoErrs) > 0 {
		sort.Slice(perRepoErrs, func(i, j int) bool { return perRepoErrs[i].Repo < perRepoErrs[j].Repo })
		listing.Errors = perRepoErrs
	}
	return listing, nil
}

// ListDockerHub walks Docker Hub's /v2/repositories/{namespace}/ endpoint to
// enumerate the namespace's repositories, then fetches /tags for each one in
// parallel. Anonymous reads only — works for any public namespace.
//
// Hub's anonymous rate limit (100 requests / 6 hours / IP at last check) means
// listing a 200-repo namespace twice in a session can exhaust it. The OCI
// path has the same risk against /v2/_catalog; both are bounded by SetLimit.
func (c *Client) ListDockerHub(ctx context.Context, namespace string) (Listing, error) {
	ns := strings.ToLower(strings.TrimSpace(namespace))
	if !hubNamespacePattern.MatchString(ns) {
		return Listing{}, fmt.Errorf("invalid Docker Hub namespace %q", namespace)
	}
	listing := Listing{
		Host:      "docker.io",
		BaseURL:   hubReposBaseURL + "/" + ns,
		Source:    SourceHub,
		Namespace: ns,
	}

	repos, err := c.fetchHubRepos(ctx, ns)
	if err != nil {
		return listing, fmt.Errorf("hub repositories: %w", err)
	}
	if len(repos) == 0 {
		listing.Repos = []RepoListing{}
		return listing, nil
	}

	results := make([]RepoListing, len(repos))
	errsMu := sync.Mutex{}
	var perRepoErrs []ListingError

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(tagFetchConcurrency)
	for i, repo := range repos {
		i, repo := i, repo
		g.Go(func() error {
			tags, terr := c.fetchHubTags(gctx, ns, repo)
			if terr != nil {
				log.Printf("[-] [REGISTRY] [hub %s/%s tags: %v]", ns, repo, terr)
				errsMu.Lock()
				perRepoErrs = append(perRepoErrs, ListingError{Repo: repo, Error: terr.Error()})
				errsMu.Unlock()
				results[i] = RepoListing{Repo: repo, Tags: []string{}}
				return nil
			}
			results[i] = RepoListing{Repo: repo, Tags: tags}
			return nil
		})
	}
	_ = g.Wait()

	listing.Repos = results
	if len(perRepoErrs) > 0 {
		sort.Slice(perRepoErrs, func(i, j int) bool { return perRepoErrs[i].Repo < perRepoErrs[j].Repo })
		listing.Errors = perRepoErrs
	}
	return listing, nil
}

// PullRef formats an "<host>/<repo>:<tag>" reference suitable for
// docker.Client.ImagePull. Returns an error when host/repo/tag are malformed.
func PullRef(host, repo, tag string) (string, error) {
	host = strings.TrimSpace(host)
	repo = strings.TrimSpace(repo)
	tag = strings.TrimSpace(tag)
	host = stripScheme(host)
	if host == "" || repo == "" || tag == "" {
		return "", fmt.Errorf("missing host/repo/tag")
	}
	if !validHostPattern.MatchString(host) {
		return "", fmt.Errorf("invalid host %q", host)
	}
	if strings.ContainsAny(repo, " \t\n\r") || strings.ContainsAny(tag, " \t\n\r") {
		return "", fmt.Errorf("repo/tag contains whitespace")
	}
	return host + "/" + repo + ":" + tag, nil
}

// --- internal helpers ---

func normalizeBaseURL(host string) (baseURL, normHost string, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", fmt.Errorf("empty host")
	}

	scheme := "https"
	if rest, ok := strings.CutPrefix(host, "http://"); ok {
		scheme = "http"
		host = rest
	} else if rest, ok := strings.CutPrefix(host, "https://"); ok {
		host = rest
	}

	host = strings.Trim(host, "/")
	if !validHostPattern.MatchString(host) {
		return "", "", fmt.Errorf("invalid host %q", host)
	}
	return scheme + "://" + host, host, nil
}

func stripScheme(host string) string {
	if rest, ok := strings.CutPrefix(host, "http://"); ok {
		return strings.Trim(rest, "/")
	}
	if rest, ok := strings.CutPrefix(host, "https://"); ok {
		return strings.Trim(rest, "/")
	}
	return strings.Trim(host, "/")
}

type catalogPage struct {
	Repositories []string `json:"repositories"`
}

func (c *Client) listCatalog(ctx context.Context, baseURL string) ([]string, error) {
	repos := make([]string, 0, catalogPageSize)
	seen := map[string]struct{}{}

	next := fmt.Sprintf("%s/v2/_catalog?n=%d", baseURL, catalogPageSize)
	for page := 0; page < maxCatalogPages && next != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("registry %s does not expose /v2/_catalog (HTTP 404)", baseURL)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("registry %s requires authentication (HTTP %d) — anonymous catalog is not supported in phase 1", baseURL, resp.StatusCode)
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("catalog HTTP %d", resp.StatusCode)
		}

		var pg catalogPage
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, fmt.Errorf("decode catalog page: %w", err)
		}
		for _, r := range pg.Repositories {
			if r == "" {
				continue
			}
			if _, dup := seen[r]; dup {
				continue
			}
			seen[r] = struct{}{}
			repos = append(repos, r)
		}

		next = resolveLinkNext(baseURL, resp.Header.Get("Link"))
	}

	sort.Strings(repos)
	return repos, nil
}

type tagListResponse struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

func (c *Client) listTags(ctx context.Context, baseURL, repo string) ([]string, error) {
	tags := make([]string, 0, tagListPageSize)
	seen := map[string]struct{}{}

	encoded := encodeRepoPath(repo)
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=%d", baseURL, encoded, tagListPageSize)
	for page := 0; page < maxTagPages && next != ""; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("repo not found")
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("tags HTTP %d", resp.StatusCode)
		}

		var pg tagListResponse
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, fmt.Errorf("decode tags: %w", err)
		}
		for _, t := range pg.Tags {
			if t == "" {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			tags = append(tags, t)
		}

		next = resolveLinkNext(baseURL, resp.Header.Get("Link"))
	}

	sort.Strings(tags)
	return tags, nil
}

// encodeRepoPath path-escapes each "/" segment of repo while keeping the
// slashes unescaped, matching how OCI registries compose URLs.
func encodeRepoPath(repo string) string {
	parts := strings.Split(repo, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// HubPullRef formats a "<namespace>/<repo>:<tag>" reference for an image
// hosted on Docker Hub. The "docker.io/" prefix is intentionally omitted —
// docker.Client.ImagePull resolves bare namespace refs to Hub by default,
// and the bare form is what `docker pull` users type.
func HubPullRef(namespace, repo, tag string) (string, error) {
	namespace = strings.ToLower(strings.TrimSpace(namespace))
	repo = strings.TrimSpace(repo)
	tag = strings.TrimSpace(tag)
	if namespace == "" || repo == "" || tag == "" {
		return "", fmt.Errorf("missing namespace/repo/tag")
	}
	if !hubNamespacePattern.MatchString(namespace) {
		return "", fmt.Errorf("invalid namespace %q", namespace)
	}
	if strings.ContainsAny(repo, " \t\n\r/") || strings.ContainsAny(tag, " \t\n\r") {
		return "", fmt.Errorf("repo/tag contains invalid characters")
	}
	return namespace + "/" + repo + ":" + tag, nil
}

// hubRepoPage is one page of /v2/repositories/{namespace} response.
type hubRepoPage struct {
	Next    string         `json:"next"`
	Results []hubRepoEntry `json:"results"`
}

type hubRepoEntry struct {
	Name string `json:"name"`
}

func (c *Client) fetchHubRepos(ctx context.Context, namespace string) ([]string, error) {
	repos := make([]string, 0, hubPageSize)
	seen := map[string]struct{}{}

	next := fmt.Sprintf("%s/%s/?page_size=%d", hubReposBaseURL, namespace, hubPageSize)
	for page := 0; page < hubMaxPages && next != ""; page++ {
		body, status, err := c.getJSON(ctx, next)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			return nil, fmt.Errorf("Docker Hub namespace %q not found", namespace)
		}
		if status/100 != 2 {
			return nil, fmt.Errorf("hub repositories HTTP %d", status)
		}
		var pg hubRepoPage
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, fmt.Errorf("decode hub repositories: %w", err)
		}
		for _, r := range pg.Results {
			if r.Name == "" {
				continue
			}
			if _, dup := seen[r.Name]; dup {
				continue
			}
			seen[r.Name] = struct{}{}
			repos = append(repos, r.Name)
		}
		next = strings.TrimSpace(pg.Next)
	}

	sort.Strings(repos)
	return repos, nil
}

// hubTagPage is one page of /v2/repositories/{namespace}/{repo}/tags response.
type hubTagPage struct {
	Next    string        `json:"next"`
	Results []hubTagEntry `json:"results"`
}

type hubTagEntry struct {
	Name string `json:"name"`
}

func (c *Client) fetchHubTags(ctx context.Context, namespace, repo string) ([]string, error) {
	tags := make([]string, 0, hubPageSize)
	seen := map[string]struct{}{}

	next := fmt.Sprintf("%s/%s/%s/tags/?page_size=%d", hubReposBaseURL, namespace, repo, hubPageSize)
	for page := 0; page < hubMaxPages && next != ""; page++ {
		body, status, err := c.getJSON(ctx, next)
		if err != nil {
			return nil, err
		}
		if status == http.StatusNotFound {
			return nil, fmt.Errorf("repo not found")
		}
		if status/100 != 2 {
			return nil, fmt.Errorf("hub tags HTTP %d", status)
		}
		var pg hubTagPage
		if err := json.Unmarshal(body, &pg); err != nil {
			return nil, fmt.Errorf("decode hub tags: %w", err)
		}
		for _, t := range pg.Results {
			if t.Name == "" {
				continue
			}
			if _, dup := seen[t.Name]; dup {
				continue
			}
			seen[t.Name] = struct{}{}
			tags = append(tags, t.Name)
		}
		next = strings.TrimSpace(pg.Next)
	}

	sort.Strings(tags)
	return tags, nil
}

// getJSON issues a GET against url and returns the response body and HTTP
// status code. Body is read with a 4 MiB cap to bound memory.
func (c *Client) getJSON(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// resolveLinkNext extracts the rel="next" target from an RFC 5988 Link header
// and resolves it against baseURL. Returns "" when no rel="next" is present.
func resolveLinkNext(baseURL, header string) string {
	if header == "" {
		return ""
	}
	for _, m := range linkRel.FindAllStringSubmatch(header, -1) {
		if len(m) != 3 {
			continue
		}
		target, rel := m[1], m[2]
		if !strings.EqualFold(rel, "next") {
			continue
		}
		if u, err := url.Parse(target); err == nil {
			if u.IsAbs() {
				return u.String()
			}
			if base, berr := url.Parse(baseURL); berr == nil {
				return base.ResolveReference(u).String()
			}
		}
	}
	return ""
}
