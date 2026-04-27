package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
	"golang.org/x/sync/errgroup"
)

const (
	hubBaseURL      = "https://hub.docker.com/v2/repositories"
	hubFetchTimeout = 5 * time.Second
	hubCacheTTL     = 5 * time.Minute
	hubNegCacheTTL  = 30 * time.Second
	hubMaxPages     = 5
	hubPageSize     = 100
	selenwrightNS   = "selenwright"
	candidateCount  = 5
	hubCheckJobs    = 4
)

// imageRef is a normalized view of a Docker image reference.
// Defaults: Registry="docker.io", Namespace="library" for short refs like "nginx".
type imageRef struct {
	Registry, Namespace, Repo, Tag string
}

// parseImageRef accepts forms used by the compose stack:
//
//	"selenwright/hub:v1.0.0"          -> docker.io/selenwright/hub:v1.0.0
//	"nginx:1.27-alpine"               -> docker.io/library/nginx:1.27-alpine
//	"library/nginx"                   -> docker.io/library/nginx:latest
//	"docker.io/selenwright/hub:v1.2"  -> docker.io/selenwright/hub:v1.2
//	"ghcr.io/x/y:z"                   -> ghcr.io/x/y:z
//
// Returns an error for empty/malformed refs.
func parseImageRef(ref string) (imageRef, error) {
	r := strings.TrimSpace(ref)
	if r == "" {
		return imageRef{}, fmt.Errorf("empty image reference")
	}
	// Drop digest suffix (e.g. "@sha256:...") — we operate on tags only.
	if at := strings.Index(r, "@"); at >= 0 {
		r = r[:at]
	}

	out := imageRef{Registry: "docker.io", Tag: "latest"}

	// Split off the registry portion if the first "/" segment looks like a host
	// (contains "." or ":" or equals "localhost").
	first := r
	rest := ""
	if i := strings.Index(r, "/"); i >= 0 {
		first = r[:i]
		rest = r[i+1:]
	}
	if rest != "" && (strings.ContainsAny(first, ".:") || first == "localhost") {
		out.Registry = first
		r = rest
	}

	// Pull off ":tag" — but only the LAST colon, since registry-with-port was
	// already stripped above.
	if i := strings.LastIndex(r, ":"); i >= 0 {
		// Defend against a path that contains "/" after the ":" (would mean
		// the colon was part of something else — shouldn't happen post-strip).
		if !strings.Contains(r[i+1:], "/") {
			out.Tag = r[i+1:]
			r = r[:i]
		}
	}

	// What remains is "repo" or "namespace/repo" (or "ns/sub/repo" for some
	// registries — keep everything after the first segment as the repo path).
	if r == "" {
		return imageRef{}, fmt.Errorf("invalid image reference: %q", ref)
	}
	if i := strings.Index(r, "/"); i >= 0 {
		out.Namespace = r[:i]
		out.Repo = r[i+1:]
	} else {
		out.Namespace = "library"
		out.Repo = r
	}

	if out.Repo == "" || out.Namespace == "" {
		return imageRef{}, fmt.Errorf("invalid image reference: %q", ref)
	}
	return out, nil
}

// hubTag is a single page entry from the Docker Hub tags API.
type hubTag struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
}

type hubTagsPage struct {
	Next    *string  `json:"next"`
	Results []hubTag `json:"results"`
}

type hubCacheEntry struct {
	tags    []hubTag
	fetched time.Time
	err     error // negative-cache value (short TTL)
}

type hubTagCache struct {
	mu      sync.Mutex
	entries map[string]hubCacheEntry
	// httpClient is overridable in tests; nil falls back to http.DefaultClient
	// with a per-call timeout via context.
	baseURL string
}

func newHubTagCache() *hubTagCache {
	return &hubTagCache{
		entries: map[string]hubCacheEntry{},
		baseURL: hubBaseURL,
	}
}

var hubTags = newHubTagCache()

// fetchHubTags returns all tags for ns/repo, paginated up to hubMaxPages.
// Cached per (ns,repo); positive TTL hubCacheTTL, negative TTL hubNegCacheTTL.
// Network requests honour ctx and a per-call timeout.
func (c *hubTagCache) fetchHubTags(ctx context.Context, ns, repo string) ([]hubTag, error) {
	key := ns + "/" + repo
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		ttl := hubCacheTTL
		if e.err != nil {
			ttl = hubNegCacheTTL
		}
		if now.Sub(e.fetched) < ttl {
			c.mu.Unlock()
			return e.tags, e.err
		}
	}
	c.mu.Unlock()

	tags, err := c.doFetch(ctx, ns, repo)

	c.mu.Lock()
	c.entries[key] = hubCacheEntry{tags: tags, fetched: time.Now(), err: err}
	c.mu.Unlock()
	return tags, err
}

func (c *hubTagCache) doFetch(ctx context.Context, ns, repo string) ([]hubTag, error) {
	httpClient := &http.Client{Timeout: hubFetchTimeout}

	pageURL := fmt.Sprintf("%s/%s/%s/tags?page_size=%d",
		c.baseURL, url.PathEscape(ns), url.PathEscape(repo), hubPageSize)

	all := make([]hubTag, 0, hubPageSize)
	for page := 0; page < hubMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, fmt.Errorf("hub request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("hub fetch %s/%s: %w", ns, repo, err)
		}
		body := resp.Body
		if resp.StatusCode == http.StatusNotFound {
			_ = body.Close()
			return nil, fmt.Errorf("repository %s/%s not found on Docker Hub", ns, repo)
		}
		if resp.StatusCode >= 400 {
			_ = body.Close()
			return nil, fmt.Errorf("hub %s/%s: status %d", ns, repo, resp.StatusCode)
		}

		var pg hubTagsPage
		dec := json.NewDecoder(body)
		if err := dec.Decode(&pg); err != nil {
			_ = body.Close()
			return nil, fmt.Errorf("hub %s/%s decode: %w", ns, repo, err)
		}
		_ = body.Close()
		all = append(all, pg.Results...)
		if pg.Next == nil || *pg.Next == "" {
			break
		}
		pageURL = *pg.Next
	}
	return all, nil
}

// fetchHubTags is the package-level shortcut over the default cache.
func fetchHubTags(ctx context.Context, ns, repo string) ([]hubTag, error) {
	return hubTags.fetchHubTags(ctx, ns, repo)
}

var leadingDotsRE = regexp.MustCompile(`^v\.+`)

// normalizeTag coerces a tag into x/mod/semver-compatible canonical form.
// Returns "" if the tag cannot be parsed as semver.
//
//	"v1.0.0"     -> "v1.0.0"
//	"v.1.0.0"    -> "v1.0.0"   (the 'v.X.Y.Z' quirk seen on selenwright Hub)
//	"1.0.0"      -> "v1.0.0"
//	"latest"     -> ""
//	"v1.0.0-rc1" -> "v1.0.0-rc1"
func normalizeTag(tag string) string {
	t := strings.TrimSpace(tag)
	if t == "" {
		return ""
	}
	t = leadingDotsRE.ReplaceAllString(t, "v")
	if !strings.HasPrefix(t, "v") {
		t = "v" + t
	}
	if !semver.IsValid(t) {
		return ""
	}
	return semver.Canonical(t)
}

// pickCandidateVersions returns up to max canonical tags strictly newer than
// currentTag, sorted descending. Tags that don't normalize to valid semver
// are skipped. Pre-releases are excluded unless currentTag itself is a
// pre-release.
func pickCandidateVersions(currentTag string, available []string, max int) []string {
	cur := normalizeTag(currentTag)
	allowPre := cur != "" && semver.Prerelease(cur) != ""

	type cand struct{ orig, canon string }
	cands := make([]cand, 0, len(available))
	seen := make(map[string]bool, len(available))
	for _, t := range available {
		c := normalizeTag(t)
		if c == "" {
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		if !allowPre && semver.Prerelease(c) != "" {
			continue
		}
		if cur != "" && semver.Compare(c, cur) <= 0 {
			continue
		}
		// Always preserve the original published tag — that's what `docker pull`
		// must use. Hub's quirky tags (e.g. "v.1.0.0") are stored verbatim and
		// only normalised internally for sorting/comparison.
		cands = append(cands, cand{orig: strings.TrimSpace(t), canon: c})
	}
	sort.Slice(cands, func(i, j int) bool {
		return semver.Compare(cands[i].canon, cands[j].canon) > 0
	})
	if len(cands) > max {
		cands = cands[:max]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.orig
	}
	return out
}

// --- response types ---

type stackVersionInfo struct {
	Service         string   `json:"service"`
	Image           string   `json:"image,omitempty"`
	CurrentTag      string   `json:"currentTag,omitempty"`
	LatestTag       string   `json:"latestTag,omitempty"`
	AvailableTags   []string `json:"availableTags,omitempty"`
	UpdateAvailable bool     `json:"updateAvailable"`
	VersionCheck    string   `json:"versionCheck"` // "ok" | "unsupported" | "error"
	VersionMessage  string   `json:"versionMessage,omitempty"`
}

type stackCheckUpdatesResponse struct {
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Services  []stackVersionInfo `json:"services"`
	CheckedAt string             `json:"checkedAt"`
}

// --- POST /stack/check-updates ---

func stackCheckUpdatesHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ok, reason := stackUpdateAvailable()
	if !ok {
		writeJSONResponse(w, http.StatusOK, stackCheckUpdatesResponse{
			Available: false,
			Reason:    reason,
			Services:  []stackVersionInfo{},
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	ctx := r.Context()
	meta, err := getComposeMetadata(ctx, app.cli)
	if err != nil {
		writeJSONResponse(w, http.StatusOK, stackCheckUpdatesResponse{
			Available: false,
			Reason:    err.Error(),
			Services:  []stackVersionInfo{},
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	containers, err := getProjectContainers(ctx, app.cli, meta.Project)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{
			"error":   "container_list",
			"message": err.Error(),
		})
		return
	}

	type job struct {
		service string
		image   string
		ref     imageRef
		parsed  bool
		parseErr error
	}
	jobs := make([]job, 0, len(containers))
	seen := map[string]bool{}
	for _, c := range containers {
		svc := c.Labels[composeServiceLabel]
		if svc == "" {
			svc = strings.TrimPrefix(firstOrEmpty(c.Names), "/")
		}
		if seen[svc] {
			continue
		}
		seen[svc] = true
		imgRef := c.Image
		if strings.HasPrefix(imgRef, "sha256:") {
			if orig := resolveContainerImageRef(ctx, app.cli, c.ID); orig != "" {
				imgRef = orig
			}
		}
		ref, perr := parseImageRef(imgRef)
		jobs = append(jobs, job{service: svc, image: imgRef, ref: ref, parsed: perr == nil, parseErr: perr})
	}

	results := make([]stackVersionInfo, len(jobs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(hubCheckJobs)
	for i, j := range jobs {
		i, j := i, j
		results[i] = stackVersionInfo{Service: j.service, Image: j.image}
		if !j.parsed {
			results[i].VersionCheck = "error"
			results[i].VersionMessage = j.parseErr.Error()
			continue
		}
		results[i].CurrentTag = j.ref.Tag
		if !strings.EqualFold(j.ref.Namespace, selenwrightNS) {
			results[i].VersionCheck = "unsupported"
			continue
		}
		g.Go(func() error {
			tags, ferr := fetchHubTags(gctx, j.ref.Namespace, j.ref.Repo)
			if ferr != nil {
				results[i].VersionCheck = "error"
				results[i].VersionMessage = ferr.Error()
				return nil // don't fail the whole batch on one repo
			}
			names := make([]string, 0, len(tags))
			for _, t := range tags {
				names = append(names, t.Name)
			}
			cand := pickCandidateVersions(j.ref.Tag, names, candidateCount)
			results[i].AvailableTags = cand
			if len(cand) > 0 {
				results[i].LatestTag = cand[0]
				results[i].UpdateAvailable = true
			}
			results[i].VersionCheck = "ok"
			return nil
		})
	}
	_ = g.Wait()

	sort.Slice(results, func(i, j int) bool { return results[i].Service < results[j].Service })

	log.Printf("[-] [STACK_CHECK] [project=%s] [services=%d]", meta.Project, len(results))

	writeJSONResponse(w, http.StatusOK, stackCheckUpdatesResponse{
		Available: true,
		Services:  results,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

