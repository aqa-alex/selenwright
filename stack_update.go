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

func stackPullHandler(w http.ResponseWriter, _ *http.Request) {
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

func stackRecreateHandler(w http.ResponseWriter, _ *http.Request) {
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
