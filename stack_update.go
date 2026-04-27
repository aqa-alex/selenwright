package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	ctr "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

const (
	composeProjectLabel    = "com.docker.compose.project"
	composeServiceLabel    = "com.docker.compose.service"
	composeConfigLabel     = "com.docker.compose.project.config_files"
	composeWorkingDirLabel = "com.docker.compose.project.working_dir"

	stackPullTimeout     = 2 * time.Minute
	stackInspectTimeout  = 10 * time.Second
	stackRecreateTimeout = 30 * time.Second

	updaterImage         = "docker:27-cli"
	updaterContainerName = "selenwright-stack-updater"
)

// --- response types ---

type stackServiceStatus struct {
	Service      string `json:"service"`
	Image        string `json:"image"`
	ImageID      string `json:"imageId"`
	ImageIDShort string `json:"imageIdShort"`
	ContainerID  string `json:"containerId"`
	Status       string `json:"status"`
	Created      string `json:"created"`
}

type stackStatusResponse struct {
	Available   bool                 `json:"available"`
	Reason      string               `json:"reason,omitempty"`
	Services    []stackServiceStatus `json:"services"`
	ProjectName string               `json:"projectName,omitempty"`
}

type stackPullServiceResult struct {
	Service    string `json:"service"`
	Image      string `json:"image"`
	PreviousID string `json:"previousId"`
	CurrentID  string `json:"currentId"`
	Updated    bool   `json:"updated"`
	Error      string `json:"error,omitempty"`
}

type stackPullResponse struct {
	Results   []stackPullServiceResult `json:"results"`
	HasUpdate bool                     `json:"hasUpdate"`
}

type stackRecreateResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message"`
}

// --- availability check ---

func stackUpdateAvailable() (bool, string) {
	if app.disableDocker {
		return false, "Docker is disabled."
	}
	if app.cli == nil {
		return false, "Docker client is not initialised."
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return false, "The server does not appear to be running inside Docker."
	}
	return true, ""
}

// --- compose metadata ---

type composeMetadata struct {
	Project    string
	ConfigFile string
	WorkingDir string
}

func getComposeMetadata(ctx context.Context, cli *client.Client) (*composeMetadata, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, stackInspectTimeout)
	defer cancel()

	info, err := cli.ContainerInspect(inspectCtx, app.hostname)
	if err != nil {
		return nil, fmt.Errorf("inspect own container %q: %w", app.hostname, err)
	}

	project := info.Config.Labels[composeProjectLabel]
	if project == "" {
		return nil, fmt.Errorf("container %q has no %s label — it was not started via Docker Compose", app.hostname, composeProjectLabel)
	}

	configFile := info.Config.Labels[composeConfigLabel]
	workingDir := info.Config.Labels[composeWorkingDirLabel]
	if configFile == "" || workingDir == "" {
		return nil, fmt.Errorf("container %q is missing compose config/working-dir labels", app.hostname)
	}

	return &composeMetadata{
		Project:    project,
		ConfigFile: configFile,
		WorkingDir: workingDir,
	}, nil
}

func getProjectContainers(ctx context.Context, cli *client.Client, project string) ([]types.Container, error) {
	listCtx, cancel := context.WithTimeout(ctx, stackInspectTimeout)
	defer cancel()

	return cli.ContainerList(listCtx, ctr.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", composeProjectLabel+"="+project),
		),
	})
}

// --- GET /stack/status ---

func stackStatusHandler(w http.ResponseWriter, _ *http.Request) {
	ok, reason := stackUpdateAvailable()
	if !ok {
		writeJSONResponse(w, http.StatusOK, stackStatusResponse{
			Available: false,
			Reason:    reason,
			Services:  []stackServiceStatus{},
		})
		return
	}

	ctx := context.Background()
	meta, err := getComposeMetadata(ctx, app.cli)
	if err != nil {
		writeJSONResponse(w, http.StatusOK, stackStatusResponse{
			Available: false,
			Reason:    err.Error(),
			Services:  []stackServiceStatus{},
		})
		return
	}

	containers, err := getProjectContainers(ctx, app.cli, meta.Project)
	if err != nil {
		writeJSONResponse(w, http.StatusOK, stackStatusResponse{
			Available: false,
			Reason:    fmt.Sprintf("Failed to list project containers: %v", err),
			Services:  []stackServiceStatus{},
		})
		return
	}

	services := make([]stackServiceStatus, 0, len(containers))
	for _, c := range containers {
		svc := c.Labels[composeServiceLabel]
		if svc == "" {
			svc = strings.TrimPrefix(firstOrEmpty(c.Names), "/")
		}
		imgRef := c.Image
		if strings.HasPrefix(imgRef, "sha256:") {
			if orig := resolveContainerImageRef(ctx, app.cli, c.ID); orig != "" {
				imgRef = orig
			}
		}
		services = append(services, stackServiceStatus{
			Service:      svc,
			Image:        imgRef,
			ImageID:      c.ImageID,
			ImageIDShort: shortID(c.ImageID),
			ContainerID:  c.ID[:12],
			Status:       c.State,
			Created:      time.Unix(c.Created, 0).UTC().Format(time.RFC3339),
		})
	}

	sort.Slice(services, func(i, j int) bool {
		return services[i].Service < services[j].Service
	})

	writeJSONResponse(w, http.StatusOK, stackStatusResponse{
		Available:   true,
		Services:    services,
		ProjectName: meta.Project,
	})
}

// --- POST /stack/pull ---

func stackPullHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ok, reason := stackUpdateAvailable()
	if !ok {
		writeJSONResponse(w, http.StatusConflict, stackPullResponse{
			Results: []stackPullServiceResult{},
		})
		_ = json.NewEncoder(w) // already wrote
		log.Printf("[-] [STACK_PULL] [unavailable: %s]", reason)
		return
	}

	ctx := context.Background()
	meta, err := getComposeMetadata(ctx, app.cli)
	if err != nil {
		writeJSONResponse(w, http.StatusConflict, map[string]string{
			"error":   "compose_metadata",
			"message": err.Error(),
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

	type pullTarget struct {
		service          string
		image            string
		containerImageID string
	}
	seen := map[string]bool{}
	var targets []pullTarget
	for _, c := range containers {
		imgRef := c.Image
		if strings.HasPrefix(imgRef, "sha256:") {
			if orig := resolveContainerImageRef(ctx, app.cli, c.ID); orig != "" {
				imgRef = orig
			}
		}
		if seen[imgRef] {
			continue
		}
		seen[imgRef] = true
		svc := c.Labels[composeServiceLabel]
		if svc == "" {
			svc = strings.TrimPrefix(firstOrEmpty(c.Names), "/")
		}
		targets = append(targets, pullTarget{service: svc, image: imgRef, containerImageID: c.ImageID})
	}

	results := make([]stackPullServiceResult, 0, len(targets))
	hasUpdate := false

	for _, t := range targets {
		result := stackPullServiceResult{
			Service: t.service,
			Image:   t.image,
		}
		result.PreviousID = shortID(t.containerImageID)

		pullCtx, pullCancel := context.WithTimeout(ctx, stackPullTimeout)
		reader, pullErr := app.cli.ImagePull(pullCtx, t.image, image.PullOptions{})
		if pullErr != nil {
			log.Printf("[-] [STACK_PULL] [%s] [PULL_SKIPPED] [%v]", t.image, pullErr)
		} else {
			_, _ = io.Copy(io.Discard, reader)
			_ = reader.Close()
		}
		pullCancel()

		localID, _ := resolveImageID(ctx, app.cli, t.image)
		result.CurrentID = shortID(localID)
		result.Updated = t.containerImageID != "" && localID != "" && t.containerImageID != localID
		if result.Updated {
			hasUpdate = true
		}

		results = append(results, result)
		log.Printf("[-] [STACK_PULL] [%s] [container=%s] [local=%s] [updated=%v]", t.image, result.PreviousID, result.CurrentID, result.Updated)
	}

	writeJSONResponse(w, http.StatusOK, stackPullResponse{
		Results:   results,
		HasUpdate: hasUpdate,
	})
}

// --- POST /stack/recreate ---

func stackRecreateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ok, reason := stackUpdateAvailable()
	if !ok {
		writeJSONResponse(w, http.StatusConflict, stackRecreateResponse{
			Message: reason,
		})
		return
	}

	ctx := context.Background()
	meta, err := getComposeMetadata(ctx, app.cli)
	if err != nil {
		writeJSONResponse(w, http.StatusConflict, stackRecreateResponse{
			Message: err.Error(),
		})
		return
	}

	if err := ensureUpdaterImage(ctx, app.cli); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to prepare updater image %s: %v", updaterImage, err),
		})
		return
	}

	removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
	_ = app.cli.ContainerRemove(removeCtx, updaterContainerName, ctr.RemoveOptions{Force: true})
	removeCancel()

	createCtx, createCancel := context.WithTimeout(ctx, stackRecreateTimeout)
	defer createCancel()

	resp, err := app.cli.ContainerCreate(createCtx,
		&ctr.Config{
			Image: updaterImage,
			Cmd: []string{
				"docker", "compose",
				"-f", meta.ConfigFile,
				"--project-directory", meta.WorkingDir,
				"up", "-d",
			},
			Labels: map[string]string{
				"io.selenwright.role": "stack-updater",
			},
		},
		&ctr.HostConfig{
			AutoRemove: true,
			Binds: []string{
				"/var/run/docker.sock:/var/run/docker.sock",
				meta.WorkingDir + ":" + meta.WorkingDir + ":ro",
			},
		},
		nil, nil, updaterContainerName,
	)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to create updater container: %v", err),
		})
		return
	}

	if err := app.cli.ContainerStart(createCtx, resp.ID, ctr.StartOptions{}); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to start updater container: %v", err),
		})
		return
	}

	log.Printf("[-] [STACK_RECREATE] [updater=%s] [compose=%s]", resp.ID[:12], meta.ConfigFile)

	writeJSONResponse(w, http.StatusAccepted, stackRecreateResponse{
		Accepted: true,
		Message:  "Stack is being recreated. The UI will reconnect when services are back.",
	})
}

// --- POST /stack/update ---

type stackUpdateRequest struct {
	Services map[string]string `json:"services"`
}

type stackUpdatePlanEntry struct {
	Service  string
	Image    string // "<ns>/<repo>:<canonicalTag>"
	Tag      string // canonical (e.g. "v1.1.0")
	Original string // tag string the operator selected (preserved when valid)
}

func stackUpdateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ok, reason := stackUpdateAvailable()
	if !ok {
		writeJSONResponse(w, http.StatusConflict, stackRecreateResponse{Message: reason})
		return
	}

	var req stackUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Services) == 0 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error":   "bad_request",
			"message": "services map required",
		})
		return
	}

	ctx := r.Context()
	meta, err := getComposeMetadata(ctx, app.cli)
	if err != nil {
		writeJSONResponse(w, http.StatusConflict, stackRecreateResponse{Message: err.Error()})
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

	// Map: service-name -> parsed imageRef of the running container.
	serviceRefs := map[string]imageRef{}
	for _, c := range containers {
		svc := c.Labels[composeServiceLabel]
		if svc == "" {
			svc = strings.TrimPrefix(firstOrEmpty(c.Names), "/")
		}
		imgRef := c.Image
		if strings.HasPrefix(imgRef, "sha256:") {
			if orig := resolveContainerImageRef(ctx, app.cli, c.ID); orig != "" {
				imgRef = orig
			}
		}
		ref, perr := parseImageRef(imgRef)
		if perr != nil {
			continue
		}
		serviceRefs[svc] = ref
	}

	plan := make([]stackUpdatePlanEntry, 0, len(req.Services))
	for svc, requested := range req.Services {
		ref, ok := serviceRefs[svc]
		if !ok {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error":   "unknown_service",
				"message": fmt.Sprintf("service %q not in stack", svc),
			})
			return
		}
		if !strings.EqualFold(ref.Namespace, selenwrightNS) {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error":   "unsupported_namespace",
				"message": fmt.Sprintf("service %q image namespace %q is not version-aware", svc, ref.Namespace),
			})
			return
		}
		canonical := normalizeTag(requested)
		if canonical == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_tag",
				"message": fmt.Sprintf("tag %q for service %q is not a valid semver", requested, svc),
			})
			return
		}
		// Defense-in-depth: tag must be in the current candidate list from Hub.
		tags, herr := fetchHubTags(ctx, ref.Namespace, ref.Repo)
		if herr != nil {
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{
				"error":   "hub_unavailable",
				"message": fmt.Sprintf("unable to query Docker Hub for %s/%s: %v", ref.Namespace, ref.Repo, herr),
			})
			return
		}
		names := make([]string, 0, len(tags))
		for _, t := range tags {
			names = append(names, t.Name)
		}
		// Wide candidate window for validation (50) so an operator can select
		// any tag the UI showed even if N=5 in the dropdown.
		cand := pickCandidateVersions(ref.Tag, names, 50)
		published, ok := findCandidateByCanonical(cand, canonical)
		if !ok {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_tag",
				"message": fmt.Sprintf("tag %q is not an available newer version for %s/%s", requested, ref.Namespace, ref.Repo),
			})
			return
		}
		// Use the tag string Hub actually publishes (e.g. "v.1.0.0") for the
		// pull/override — `docker pull <ns>/<repo>:<canonical>` would 404 if
		// the publisher uses a non-canonical tag form.
		plan = append(plan, stackUpdatePlanEntry{
			Service:  svc,
			Image:    fmt.Sprintf("%s/%s:%s", ref.Namespace, ref.Repo, published),
			Tag:      published,
			Original: strings.TrimSpace(requested),
		})
	}

	// Services that the operator actually asked to upgrade. We pre-pull and
	// force-recreate exactly these. The plan is then extended below with
	// already-running version-aware services pinned at their current tag, so
	// a single-service click never silently drops pins set by a prior click.
	changedServices := make([]string, 0, len(plan))
	for _, p := range plan {
		changedServices = append(changedServices, p.Service)
	}

	plan = extendPlanWithCurrentPins(plan, serviceRefs, req.Services)

	// Pre-pull each NEW target image; surface failures before touching compose.
	// Pins for unchanged services are already local — re-pulling them is wasteful
	// and could fail spuriously if the Hub tag was rotated in the meantime.
	changedSet := make(map[string]bool, len(changedServices))
	for _, s := range changedServices {
		changedSet[s] = true
	}
	for _, p := range plan {
		if !changedSet[p.Service] {
			continue
		}
		pullCtx, cancel := context.WithTimeout(ctx, stackPullTimeout)
		reader, perr := app.cli.ImagePull(pullCtx, p.Image, image.PullOptions{})
		if perr != nil {
			cancel()
			writeJSONResponse(w, http.StatusBadGateway, map[string]string{
				"error":   "pull_failed",
				"message": fmt.Sprintf("failed to pull %s: %v", p.Image, perr),
			})
			return
		}
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
		cancel()
	}

	if err := ensureUpdaterImage(ctx, app.cli); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to prepare updater image %s: %v", updaterImage, err),
		})
		return
	}

	overrideYAML := renderOverrideYAML(plan)
	shellSnippet := buildUpdaterShellSnippet(overrideYAML, changedServices)

	removeCtx, removeCancel := context.WithTimeout(ctx, 5*time.Second)
	_ = app.cli.ContainerRemove(removeCtx, updaterContainerName, ctr.RemoveOptions{Force: true})
	removeCancel()

	createCtx, createCancel := context.WithTimeout(ctx, stackRecreateTimeout)
	defer createCancel()

	resp, err := app.cli.ContainerCreate(createCtx,
		&ctr.Config{
			Image:      updaterImage,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{shellSnippet},
			Env: []string{
				"WORKING_DIR=" + meta.WorkingDir,
				"CONFIG_FILE=" + meta.ConfigFile,
			},
			Labels: map[string]string{
				"io.selenwright.role": "stack-updater",
			},
		},
		&ctr.HostConfig{
			AutoRemove: true,
			Binds: []string{
				"/var/run/docker.sock:/var/run/docker.sock",
				meta.WorkingDir + ":" + meta.WorkingDir + ":rw",
			},
		},
		nil, nil, updaterContainerName,
	)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to create updater container: %v", err),
		})
		return
	}
	if err := app.cli.ContainerStart(createCtx, resp.ID, ctr.StartOptions{}); err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, stackRecreateResponse{
			Message: fmt.Sprintf("Failed to start updater container: %v", err),
		})
		return
	}

	planSummary := make([]string, 0, len(plan))
	for _, p := range plan {
		planSummary = append(planSummary, p.Service+"="+p.Tag)
	}
	log.Printf("[-] [STACK_UPDATE] [updater=%s] [compose=%s] [plan=%s]",
		resp.ID[:12], meta.ConfigFile, strings.Join(planSummary, ","))

	writeJSONResponse(w, http.StatusAccepted, stackRecreateResponse{
		Accepted: true,
		Message:  "Stack update started. The UI will reconnect when services are back.",
	})
}

// renderOverrideYAML emits a minimal docker-compose override that pins each
// service's image. The structure is fixed and small — we hand-format rather
// than pull in a YAML library.
func renderOverrideYAML(plan []stackUpdatePlanEntry) string {
	// Stable ordering for reproducible output (helpful for tests + diffs).
	ordered := make([]stackUpdatePlanEntry, len(plan))
	copy(ordered, plan)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Service < ordered[j].Service })

	var b strings.Builder
	b.WriteString("# Generated by selenwright stack update — do not edit by hand.\n")
	b.WriteString("services:\n")
	for _, p := range ordered {
		fmt.Fprintf(&b, "  %s:\n    image: %s\n", p.Service, p.Image)
	}
	return b.String()
}

// buildUpdaterShellSnippet writes the override YAML into the working dir
// (via heredoc, no shell substitution inside the body) and then runs
// `docker compose up -d` with both compose files. Paths come from env vars
// set on the updater container (WORKING_DIR, CONFIG_FILE).
//
// changedServices, if non-empty, are appended after `up -d --force-recreate`
// so compose unconditionally recreates them even when the resolved local
// image_id is unchanged (e.g. when two tags published to Hub point at the
// same content via manifest aliasing).
func buildUpdaterShellSnippet(overrideYAML string, changedServices []string) string {
	const eof = "__SELENWRIGHT_OVERRIDE_EOF__"
	body := overrideYAML
	// Defense: ensure the heredoc delimiter doesn't appear in the body.
	if strings.Contains(body, eof) {
		body = strings.ReplaceAll(body, eof, "__SELENWRIGHT_OVERRIDE_EOF_SAFE__")
	}
	composeCmd := `docker compose -f "$CONFIG_FILE" -f "$OVERRIDE_PATH" --project-directory "$WORKING_DIR" up -d`
	if len(changedServices) > 0 {
		sorted := append([]string(nil), changedServices...)
		sort.Strings(sorted)
		composeCmd += " --force-recreate"
		for _, s := range sorted {
			composeCmd += " " + shellSingleQuote(s)
		}
	}
	return strings.Join([]string{
		`set -e`,
		`OVERRIDE_PATH="$WORKING_DIR/docker-compose.override.yml"`,
		`cat > "$OVERRIDE_PATH" <<'` + eof + `'`,
		strings.TrimRight(body, "\n"),
		eof,
		composeCmd,
	}, "\n")
}

// shellSingleQuote wraps s in single quotes, escaping any embedded single
// quotes by closing/escaping/reopening the literal. Suitable for safe sh
// argument injection. Compose service names are normally restricted to
// [a-zA-Z0-9._-], but defensive quoting costs nothing.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// extendPlanWithCurrentPins appends entries for every running version-aware
// service that wasn't in requestedServices, pinning them at their currently
// running tag. This way the override file always represents the full pinning
// state of the version-aware stack — clicking Update on one service can't
// silently drop a pin set by an earlier click on a different service.
func extendPlanWithCurrentPins(plan []stackUpdatePlanEntry, serviceRefs map[string]imageRef, requestedServices map[string]string) []stackUpdatePlanEntry {
	for svc, ref := range serviceRefs {
		if _, requested := requestedServices[svc]; requested {
			continue
		}
		if !strings.EqualFold(ref.Namespace, selenwrightNS) {
			continue
		}
		if ref.Tag == "" {
			continue
		}
		plan = append(plan, stackUpdatePlanEntry{
			Service:  svc,
			Image:    fmt.Sprintf("%s/%s:%s", ref.Namespace, ref.Repo, ref.Tag),
			Tag:      ref.Tag,
			Original: ref.Tag,
		})
	}
	return plan
}

// findCandidateByCanonical returns the original tag in cands whose normalized
// form equals canon, plus whether such an entry was found. The caller uses
// the original — `docker pull` must use the exact tag string the registry
// publishes, even when it deviates from semver canonical form.
func findCandidateByCanonical(cands []string, canon string) (string, bool) {
	for _, c := range cands {
		if c == canon {
			return c, true
		}
		if normalizeTag(c) == canon {
			return c, true
		}
	}
	return "", false
}

// --- helpers ---

func ensureUpdaterImage(ctx context.Context, cli *client.Client) error {
	if _, _, err := cli.ImageInspectWithRaw(ctx, updaterImage); err == nil {
		return nil
	}
	log.Printf("[-] [STACK_RECREATE] [pulling updater image %s]", updaterImage)
	pullCtx, cancel := context.WithTimeout(ctx, stackPullTimeout)
	defer cancel()
	reader, err := cli.ImagePull(pullCtx, updaterImage, image.PullOptions{})
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, reader)
	_ = reader.Close()
	return nil
}

// resolveContainerImageRef returns the original image reference (tag) from
// ContainerInspect.Config.Image. ContainerList.Image loses the tag once the
// tag is reassigned to a newer build; Config.Image always keeps it.
func resolveContainerImageRef(ctx context.Context, cli *client.Client, containerID string) string {
	inspectCtx, cancel := context.WithTimeout(ctx, stackInspectTimeout)
	defer cancel()
	info, err := cli.ContainerInspect(inspectCtx, containerID)
	if err != nil {
		return ""
	}
	return info.Config.Image
}

func resolveImageID(ctx context.Context, cli *client.Client, ref string) (string, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, stackInspectTimeout)
	defer cancel()
	inspect, _, err := cli.ImageInspectWithRaw(inspectCtx, ref)
	if err != nil {
		return "", err
	}
	return inspect.ID, nil
}

func shortID(id string) string {
	clean := strings.TrimPrefix(id, "sha256:")
	if len(clean) > 12 {
		return clean[:12]
	}
	return clean
}

func firstOrEmpty(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}
