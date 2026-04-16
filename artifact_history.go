package main

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
)

const (
	defaultArtifactHistoryRetentionDays = 7
	maxArtifactHistoryRetentionDays     = 365
	minArtifactHistoryRetentionDays     = 1
	artifactHistoryJanitorInterval      = 3 * time.Hour
	managedDownloadsPath                = "/home/selenium/Downloads"
)

type artifactHistorySettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

type artifactHistorySettingsResponse struct {
	Enabled       bool   `json:"enabled"`
	RetentionDays int    `json:"retentionDays"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason"`
}

type artifactHistoryFileRef struct {
	Filename string `json:"filename"`
	Path     string `json:"path"`
}

type artifactHistoryDownload struct {
	Filename     string `json:"filename"`
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	SizeBytes    int64  `json:"sizeBytes"`
}

type artifactHistoryManifest struct {
	SessionID      string    `json:"sessionId"`
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
	Browser        string    `json:"browser"`
	BrowserVersion string    `json:"browserVersion"`
	Protocol       string    `json:"protocol"`
	Quota          string    `json:"quota"`
	Artifacts      struct {
		Log       *artifactHistoryFileRef   `json:"log,omitempty"`
		Video     *artifactHistoryFileRef   `json:"video,omitempty"`
		Downloads []artifactHistoryDownload `json:"downloads,omitempty"`
	} `json:"artifacts"`
	CaptureStatus struct {
		Downloads string `json:"downloads"`
	} `json:"captureStatus"`
}

type artifactHistoryDownloadListItem struct {
	Filename       string `json:"filename"`
	SessionID      string `json:"sessionId"`
	Browser        string `json:"browser"`
	BrowserVersion string `json:"browserVersion"`
	Protocol       string `json:"protocol"`
	CreatedAt      string `json:"createdAt"`
	RelativePath   string `json:"relativePath"`
	SizeBytes      int64  `json:"sizeBytes"`
	DownloadURL    string `json:"downloadUrl"`
}

// artifactHistoryFileListItem is the enriched shape the `?json` handlers for
// /logs and /video emit. Fields beyond filename/size come from the matching
// artifact-history manifest; when no manifest exists (e.g. files left over
// from before retention was enabled) we fall back to a stat-derived timestamp
// and a filename-stripped session id.
type artifactHistoryFileListItem struct {
	Filename       string `json:"filename"`
	SessionID      string `json:"sessionId"`
	Browser        string `json:"browser,omitempty"`
	BrowserVersion string `json:"browserVersion,omitempty"`
	Protocol       string `json:"protocol,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	Size           int64  `json:"size"`
}

// pendingDownloadCapture caches the result of a pre-cancel docker cp so the
// async SessionStopped listener can include it in the manifest without needing
// the container to still exist.
type pendingDownloadCapture struct {
	downloads []artifactHistoryDownload
	status    string
	err       error
}

type artifactHistoryManager struct {
	settingsPath string
	rootDir      string
	logDir       string
	videoDir     string
	dockerClient *client.Client
	dockerMode   bool

	nowFn  func() time.Time
	copyFn func(*session.Session, string) ([]artifactHistoryDownload, string, error)

	mu       sync.RWMutex
	settings artifactHistorySettings

	pendingMu       sync.Mutex
	pendingCaptures map[string]pendingDownloadCapture

	janitorOnce sync.Once
}

type artifactHistoryListener struct{}

var (
	artifactHistoryMu sync.Mutex
	artifactHistory   *artifactHistoryManager
)

func init() {
	event.AddSessionStoppedListener(artifactHistoryListener{})
}

func (artifactHistoryListener) OnSessionStopped(stoppedSession event.StoppedSession) {
	manager := ensureArtifactHistoryManager()
	if manager == nil {
		return
	}
	manager.OnSessionStopped(stoppedSession)
}

func ensureArtifactHistoryManager() *artifactHistoryManager {
	artifactHistoryMu.Lock()
	defer artifactHistoryMu.Unlock()

	expectedSettingsPath := app.artifactHistorySettingsPath
	expectedRootDir := app.artifactHistoryDir
	dockerMode := !app.disableDocker
	if artifactHistory != nil &&
		artifactHistory.settingsPath == expectedSettingsPath &&
		artifactHistory.rootDir == expectedRootDir &&
		artifactHistory.logDir == app.logOutputDir &&
		artifactHistory.videoDir == app.videoOutputDir &&
		artifactHistory.dockerClient == app.cli &&
		artifactHistory.dockerMode == dockerMode {
		return artifactHistory
	}

	manager := &artifactHistoryManager{
		settingsPath:    expectedSettingsPath,
		rootDir:         expectedRootDir,
		logDir:          app.logOutputDir,
		videoDir:        app.videoOutputDir,
		dockerClient:    app.cli,
		dockerMode:      dockerMode,
		nowFn:           time.Now,
		pendingCaptures: make(map[string]pendingDownloadCapture),
	}
	manager.copyFn = manager.copyDownloadsFromContainer
	if err := manager.loadSettings(); err != nil {
		log.Printf("[-] [INIT] [Artifact history settings: %v]", err)
	}
	artifactHistory = manager
	return artifactHistory
}

func (m *artifactHistoryManager) StartJanitor() {
	if m == nil {
		return
	}
	m.janitorOnce.Do(func() {
		go func() {
			m.RunCleanupOnce()
			ticker := time.NewTicker(artifactHistoryJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				m.RunCleanupOnce()
			}
		}()
	})
}

func (m *artifactHistoryManager) SettingsResponse() artifactHistorySettingsResponse {
	available, reason := m.Availability()
	settings := m.Settings()
	response := artifactHistorySettingsResponse{
		Enabled:       settings.Enabled,
		RetentionDays: settings.RetentionDays,
		Available:     available,
	}
	if reason != "" {
		response.Reason = reason
	}
	return response
}

func (m *artifactHistoryManager) Settings() artifactHistorySettings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

func (m *artifactHistoryManager) IsEnabledForNewSessions() bool {
	if m == nil {
		return false
	}
	if available, _ := m.Availability(); !available {
		return false
	}
	return m.Settings().Enabled
}

func (m *artifactHistoryManager) ShouldPersistLogsForSession(sess *session.Session) bool {
	if m == nil || sess == nil || !sess.ArtifactHistoryEnabled {
		return false
	}
	available, _ := m.Availability()
	return available && m.logDir != ""
}

func (m *artifactHistoryManager) Availability() (bool, string) {
	if m == nil {
		return false, "artifact history is not initialized"
	}
	if !m.dockerMode || m.dockerClient == nil {
		return false, "artifact history requires Docker-backed sessions"
	}
	if m.logDir == "" {
		return false, "artifact history requires -log-output-dir"
	}
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0o755); err != nil {
		return false, fmt.Sprintf("failed to prepare settings directory: %v", err)
	}
	if err := os.MkdirAll(m.rootDir, 0o755); err != nil {
		return false, fmt.Sprintf("failed to prepare artifacts directory: %v", err)
	}
	if err := os.MkdirAll(m.manifestsDir(), 0o755); err != nil {
		return false, fmt.Sprintf("failed to prepare manifests directory: %v", err)
	}
	if err := os.MkdirAll(m.downloadsDir(), 0o755); err != nil {
		return false, fmt.Sprintf("failed to prepare downloads directory: %v", err)
	}
	return true, ""
}

func (m *artifactHistoryManager) loadSettings() error {
	defaults := artifactHistorySettings{
		Enabled:       false,
		RetentionDays: defaultArtifactHistoryRetentionDays,
	}

	m.mu.Lock()
	m.settings = defaults
	m.mu.Unlock()

	if m.settingsPath == "" {
		return fmt.Errorf("settings path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0o755); err != nil {
		return err
	}
	payload, err := os.ReadFile(m.settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m.persistSettings(defaults)
		}
		return err
	}
	var loaded artifactHistorySettings
	if err := json.Unmarshal(payload, &loaded); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	if err := validateArtifactHistorySettings(loaded); err != nil {
		return fmt.Errorf("validate settings: %w", err)
	}
	m.mu.Lock()
	m.settings = loaded
	m.mu.Unlock()
	return nil
}

func (m *artifactHistoryManager) UpdateSettings(next artifactHistorySettings) error {
	if err := validateArtifactHistorySettings(next); err != nil {
		return err
	}
	if available, reason := m.Availability(); !available {
		return artifactHistoryUnavailableError{reason: reason}
	}
	if err := m.persistSettings(next); err != nil {
		return err
	}
	m.mu.Lock()
	m.settings = next
	m.mu.Unlock()
	return nil
}

func (m *artifactHistoryManager) persistSettings(settings artifactHistorySettings) error {
	if err := os.MkdirAll(filepath.Dir(m.settingsPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(m.settingsPath), "artifact-history-*.json")
	if err != nil {
		return err
	}
	tempName := tempFile.Name()
	defer os.Remove(tempName)
	if _, err := tempFile.Write(data); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, m.settingsPath)
}

// CaptureDownloadsForSession runs while the browser container is still alive.
// Selenwright's session-delete path removes the container before firing
// SessionStopped, so the async listener can't docker-cp anymore — call this
// synchronously before cancel() to stash results for the listener.
func (m *artifactHistoryManager) CaptureDownloadsForSession(sess *session.Session, sessionID string) {
	if m == nil || sess == nil || !sess.ArtifactHistoryEnabled {
		return
	}
	if available, _ := m.Availability(); !available {
		return
	}
	targetDir := filepath.Join(m.downloadsDir(), sessionID)
	downloads, status, err := m.copyFn(sess, targetDir)
	m.pendingMu.Lock()
	m.pendingCaptures[sessionID] = pendingDownloadCapture{
		downloads: downloads,
		status:    status,
		err:       err,
	}
	m.pendingMu.Unlock()
}

func (m *artifactHistoryManager) takePendingCapture(sessionID string) (pendingDownloadCapture, bool) {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	capture, ok := m.pendingCaptures[sessionID]
	if ok {
		delete(m.pendingCaptures, sessionID)
	}
	return capture, ok
}

func (m *artifactHistoryManager) OnSessionStopped(stoppedSession event.StoppedSession) {
	if m == nil || stoppedSession.Session == nil || !stoppedSession.Session.ArtifactHistoryEnabled {
		return
	}
	if available, reason := m.Availability(); !available {
		log.Printf("[%d] [ARTIFACT_HISTORY_SKIPPED] [%s] [%s]", stoppedSession.RequestId, stoppedSession.SessionId, reason)
		return
	}

	manifest := artifactHistoryManifest{
		SessionID:      stoppedSession.SessionId,
		StartedAt:      stoppedSession.Session.Started,
		FinishedAt:     m.nowFn(),
		Browser:        strings.ToLower(stoppedSession.Session.Caps.BrowserName()),
		BrowserVersion: stoppedSession.Session.Caps.Version,
		Protocol:       string(stoppedSession.Session.Protocol),
		Quota:          stoppedSession.Session.Quota,
	}
	if manifest.Protocol == "" {
		manifest.Protocol = string(session.ProtocolWebDriver)
	}

	if m.logDir != "" && stoppedSession.Session.Caps.LogName != "" {
		manifest.Artifacts.Log = &artifactHistoryFileRef{
			Filename: filepath.Base(stoppedSession.Session.Caps.LogName),
			Path:     filepath.Join(m.logDir, stoppedSession.Session.Caps.LogName),
		}
	}
	if m.videoDir != "" && stoppedSession.Session.Caps.Video && stoppedSession.Session.Caps.VideoName != "" {
		manifest.Artifacts.Video = &artifactHistoryFileRef{
			Filename: filepath.Base(stoppedSession.Session.Caps.VideoName),
			Path:     filepath.Join(m.videoDir, stoppedSession.Session.Caps.VideoName),
		}
	}

	var downloads []artifactHistoryDownload
	var status string
	var captureErr error
	if capture, ok := m.takePendingCapture(stoppedSession.SessionId); ok {
		downloads, status, captureErr = capture.downloads, capture.status, capture.err
	} else {
		downloads, status, captureErr = m.copyFn(stoppedSession.Session, filepath.Join(m.downloadsDir(), stoppedSession.SessionId))
	}
	if captureErr != nil {
		status = "error"
		log.Printf("[%d] [ARTIFACT_HISTORY_DOWNLOADS_ERROR] [%s] [%v]", stoppedSession.RequestId, stoppedSession.SessionId, captureErr)
	}
	if status == "" {
		status = "unsupported"
	}
	manifest.CaptureStatus.Downloads = status
	manifest.Artifacts.Downloads = downloads

	if err := m.writeManifest(manifest); err != nil {
		log.Printf("[%d] [ARTIFACT_HISTORY_ERROR] [%s] [Failed to write manifest: %v]", stoppedSession.RequestId, stoppedSession.SessionId, err)
	}
}

func (m *artifactHistoryManager) writeManifest(manifest artifactHistoryManifest) error {
	if err := os.MkdirAll(m.manifestsDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.manifestPath(manifest.SessionID), data, 0o644)
}

func (m *artifactHistoryManager) RunCleanupOnce() {
	if m == nil {
		return
	}
	if available, _ := m.Availability(); !available {
		return
	}
	settings := m.Settings()
	expiry := time.Duration(settings.RetentionDays) * 24 * time.Hour
	manifests, err := m.loadAllManifests()
	if err != nil {
		log.Printf("[-] [ARTIFACT_HISTORY_CLEANUP_ERROR] [%v]", err)
		return
	}
	now := m.nowFn()
	for _, manifest := range manifests {
		if manifest.FinishedAt.IsZero() {
			continue
		}
		if now.Sub(manifest.FinishedAt) < expiry {
			continue
		}
		if err := m.removeManifestArtifacts(manifest); err != nil {
			log.Printf("[-] [ARTIFACT_HISTORY_CLEANUP_ERROR] [%s] [%v]", manifest.SessionID, err)
		}
	}
}

func (m *artifactHistoryManager) removeManifestArtifacts(manifest artifactHistoryManifest) error {
	var errs []string
	if manifest.Artifacts.Log != nil && manifest.Artifacts.Log.Path != "" {
		if err := os.Remove(manifest.Artifacts.Log.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	if manifest.Artifacts.Video != nil && manifest.Artifacts.Video.Path != "" {
		if err := os.Remove(manifest.Artifacts.Video.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err.Error())
		}
	}
	if err := os.RemoveAll(filepath.Join(m.downloadsDir(), manifest.SessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err.Error())
	}
	if err := os.Remove(m.manifestPath(manifest.SessionID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *artifactHistoryManager) loadAllManifests() ([]artifactHistoryManifest, error) {
	entries, err := os.ReadDir(m.manifestsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	manifests := make([]artifactHistoryManifest, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(m.manifestsDir(), entry.Name()))
		if err != nil {
			log.Printf("[-] [ARTIFACT_HISTORY_MANIFEST_SKIP] [%s] [read: %v]", entry.Name(), err)
			continue
		}
		var manifest artifactHistoryManifest
		if err := json.Unmarshal(payload, &manifest); err != nil {
			log.Printf("[-] [ARTIFACT_HISTORY_MANIFEST_SKIP] [%s] [parse: %v]", entry.Name(), err)
			continue
		}
		manifests = append(manifests, manifest)
	}
	return manifests, nil
}

func (m *artifactHistoryManager) ListDownloads() ([]artifactHistoryDownloadListItem, error) {
	manifests, err := m.loadAllManifests()
	if err != nil {
		return nil, err
	}
	items := make([]artifactHistoryDownloadListItem, 0)
	for _, manifest := range manifests {
		for _, download := range manifest.Artifacts.Downloads {
			items = append(items, artifactHistoryDownloadListItem{
				Filename:       download.Filename,
				SessionID:      manifest.SessionID,
				Browser:        manifest.Browser,
				BrowserVersion: manifest.BrowserVersion,
				Protocol:       manifest.Protocol,
				CreatedAt:      manifest.FinishedAt.Format(time.RFC3339),
				RelativePath:   download.RelativePath,
				SizeBytes:      download.SizeBytes,
				DownloadURL:    path.Join(paths.Downloads, manifest.SessionID, download.RelativePath),
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})
	return items, nil
}

func (m *artifactHistoryManager) ListLogs() ([]artifactHistoryFileListItem, error) {
	if m == nil || m.logDir == "" {
		return []artifactHistoryFileListItem{}, nil
	}
	return m.listArtifactFiles(m.logDir, logFileExtension, func(mf artifactHistoryManifest) *artifactHistoryFileRef {
		return mf.Artifacts.Log
	})
}

func (m *artifactHistoryManager) ListVideos() ([]artifactHistoryFileListItem, error) {
	if m == nil || m.videoDir == "" {
		return []artifactHistoryFileListItem{}, nil
	}
	return m.listArtifactFiles(m.videoDir, videoFileExtension, func(mf artifactHistoryManifest) *artifactHistoryFileRef {
		return mf.Artifacts.Video
	})
}

func (m *artifactHistoryManager) listArtifactFiles(dir, extension string, pick func(artifactHistoryManifest) *artifactHistoryFileRef) ([]artifactHistoryFileListItem, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []artifactHistoryFileListItem{}, nil
		}
		return nil, err
	}

	manifests, _ := m.loadAllManifests()
	byName := make(map[string]artifactHistoryManifest, len(manifests))
	for _, mf := range manifests {
		if ref := pick(mf); ref != nil && ref.Filename != "" {
			byName[ref.Filename] = mf
		}
	}

	items := make([]artifactHistoryFileListItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), extension) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		item := artifactHistoryFileListItem{
			Filename: entry.Name(),
			Size:     info.Size(),
		}
		if mf, ok := byName[entry.Name()]; ok {
			item.SessionID = mf.SessionID
			item.Browser = mf.Browser
			item.BrowserVersion = mf.BrowserVersion
			item.Protocol = mf.Protocol
			item.CreatedAt = mf.FinishedAt.UTC().Format(time.RFC3339)
		} else {
			item.SessionID = strings.TrimSuffix(entry.Name(), extension)
			item.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})
	return items, nil
}

func (m *artifactHistoryManager) copyDownloadsFromContainer(sess *session.Session, targetDir string) ([]artifactHistoryDownload, string, error) {
	if sess == nil || sess.Container == nil || sess.Container.ID == "" || m.dockerClient == nil {
		return nil, "unsupported", nil
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, "", err
	}
	reader, _, err := m.dockerClient.CopyFromContainer(context.Background(), sess.Container.ID, managedDownloadsPath)
	if err != nil {
		if isUnsupportedDownloadsError(err) {
			_ = os.RemoveAll(targetDir)
			return nil, "unsupported", nil
		}
		_ = os.RemoveAll(targetDir)
		return nil, "", err
	}
	defer reader.Close()

	downloads, err := extractCopiedDownloads(reader, targetDir)
	if err != nil {
		_ = os.RemoveAll(targetDir)
		return nil, "", err
	}
	if len(downloads) == 0 {
		_ = os.RemoveAll(targetDir)
		return nil, "empty", nil
	}
	return downloads, "captured", nil
}

func extractCopiedDownloads(reader io.Reader, targetDir string) ([]artifactHistoryDownload, error) {
	tr := tar.NewReader(reader)
	downloads := make([]artifactHistoryDownload, 0)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(path.Clean(strings.ReplaceAll(header.Name, "\\", "/")), "./")
		if name == "." || name == "" {
			continue
		}
		segments := strings.Split(name, "/")
		if len(segments) <= 1 {
			if header.FileInfo().IsDir() {
				continue
			}
			return nil, fmt.Errorf("unexpected archive entry: %s", header.Name)
		}
		relativePath := path.Clean(path.Join(segments[1:]...))
		if relativePath == "." || strings.HasPrefix(relativePath, "../") {
			return nil, fmt.Errorf("unexpected archive entry: %s", header.Name)
		}
		targetPath := filepath.Join(targetDir, filepath.FromSlash(relativePath))
		if !strings.HasPrefix(targetPath, targetDir) {
			return nil, fmt.Errorf("unexpected archive target: %s", targetPath)
		}
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return nil, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(file, tr); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		downloads = append(downloads, artifactHistoryDownload{
			Filename:     filepath.Base(targetPath),
			Path:         targetPath,
			RelativePath: filepath.ToSlash(relativePath),
			SizeBytes:    header.Size,
		})
	}
	sort.Slice(downloads, func(i, j int) bool {
		return downloads[i].RelativePath < downloads[j].RelativePath
	})
	return downloads, nil
}

func validateArtifactHistorySettings(settings artifactHistorySettings) error {
	if settings.RetentionDays < minArtifactHistoryRetentionDays || settings.RetentionDays > maxArtifactHistoryRetentionDays {
		return fmt.Errorf("retentionDays must be between %d and %d", minArtifactHistoryRetentionDays, maxArtifactHistoryRetentionDays)
	}
	return nil
}

func isUnsupportedDownloadsError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such file") ||
		strings.Contains(message, "could not find the file") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "no such container")
}

func (m *artifactHistoryManager) manifestsDir() string {
	return filepath.Join(m.rootDir, "sessions")
}

func (m *artifactHistoryManager) downloadsDir() string {
	return filepath.Join(m.rootDir, "downloads")
}

func (m *artifactHistoryManager) manifestPath(sessionID string) string {
	return filepath.Join(m.manifestsDir(), sessionID+".json")
}

type artifactHistoryUnavailableError struct {
	reason string
}

func (e artifactHistoryUnavailableError) Error() string {
	return e.reason
}

func writeJSONResponse(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func historySettingsHandler(w http.ResponseWriter, r *http.Request) {
	manager := ensureArtifactHistoryManager()
	if manager == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, artifactHistorySettingsResponse{
			Enabled:       false,
			RetentionDays: defaultArtifactHistoryRetentionDays,
			Available:     false,
			Reason:        "artifact history is not initialized",
		})
		return
	}
	if r.URL.Path != paths.HistorySettings && r.URL.Path != paths.HistorySettings+slash {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{"message": "unknown history settings path"})
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSONResponse(w, http.StatusOK, manager.SettingsResponse())
	case http.MethodPut:
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON body"})
			return
		}
		retentionDays, err := parseRetentionDays(payload["retentionDays"])
		if err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		enabled, ok := payload["enabled"].(bool)
		if !ok {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"message": "enabled must be a boolean"})
			return
		}
		next := artifactHistorySettings{Enabled: enabled, RetentionDays: retentionDays}
		if err := manager.UpdateSettings(next); err != nil {
			var unavailable artifactHistoryUnavailableError
			if errors.As(err, &unavailable) {
				writeJSONResponse(w, http.StatusConflict, artifactHistorySettingsResponse{
					Enabled:       manager.Settings().Enabled,
					RetentionDays: manager.Settings().RetentionDays,
					Available:     false,
					Reason:        unavailable.reason,
				})
				return
			}
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, manager.SettingsResponse())
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSONResponse(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
	}
}

func parseRetentionDays(raw interface{}) (int, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int(value)) {
			return 0, fmt.Errorf("retentionDays must be an integer")
		}
		return int(value), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, fmt.Errorf("retentionDays must be an integer")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("retentionDays must be an integer")
	}
}

func persistedDownloadsHandler(w http.ResponseWriter, r *http.Request) {
	manager := ensureArtifactHistoryManager()
	if manager == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"message": "artifact history is not initialized"})
		return
	}
	if _, ok := r.URL.Query()[jsonParam]; ok {
		items, err := manager.ListDownloads()
		if err != nil {
			writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
			return
		}
		writeJSONResponse(w, http.StatusOK, items)
		return
	}
	fileServer := http.StripPrefix(paths.Downloads, http.FileServer(http.Dir(manager.downloadsDir())))
	fileServer.ServeHTTP(w, r)
}
