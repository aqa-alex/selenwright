package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
	assert "github.com/stretchr/testify/require"
)

func TestHistorySettingsGetDefault(t *testing.T) {
	srv, _ := newArtifactHistoryTestServer(t, true)
	defer srv.Close()

	resp, err := http.Get(With(srv.URL).Path(paths.HistorySettings))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	var body artifactHistorySettingsResponse
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.False(t, body.Enabled)
	assert.Equal(t, defaultArtifactHistoryRetentionDays, body.RetentionDays)
	assert.True(t, body.Available)
	assert.Empty(t, body.Reason)

	resp, err = http.Get(With(srv.URL).Path(paths.HistorySettings))
	assert.NoError(t, err)
	defer resp.Body.Close()
	var raw map[string]interface{}
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
	reason, ok := raw["reason"]
	assert.True(t, ok)
	assert.Equal(t, "", reason)
}

func TestHistorySettingsGetWithTrailingSlash(t *testing.T) {
	srv, _ := newArtifactHistoryTestServer(t, true)
	defer srv.Close()

	resp, err := http.Get(With(srv.URL).Path(paths.HistorySettings + "/"))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	var body artifactHistorySettingsResponse
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.True(t, body.Available)
}

func TestHistorySettingsPutPersistsAndSurvivesReload(t *testing.T) {
	srv, manager := newArtifactHistoryTestServer(t, true)
	defer srv.Close()

	payload := []byte(`{"enabled":true,"retentionDays":14}`)
	req, _ := http.NewRequest(http.MethodPut, With(srv.URL).Path(paths.HistorySettings), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	reloaded := &artifactHistoryManager{
		settingsPath:    manager.settingsPath,
		rootDir:         manager.rootDir,
		logDir:          manager.logDir,
		videoDir:        manager.videoDir,
		dockerClient:    manager.dockerClient,
		dockerMode:      true,
		nowFn:           time.Now,
		pendingCaptures: make(map[string]pendingDownloadCapture),
	}
	reloaded.copyFn = reloaded.copyDownloadsFromContainer
	assert.NoError(t, reloaded.loadSettings())

	settings := reloaded.Settings()
	assert.True(t, settings.Enabled)
	assert.Equal(t, 14, settings.RetentionDays)
}

func TestHistorySettingsPutRejectsInvalidRetentionDays(t *testing.T) {
	srv, _ := newArtifactHistoryTestServer(t, true)
	defer srv.Close()

	payload := []byte(`{"enabled":true,"retentionDays":0}`)
	req, _ := http.NewRequest(http.MethodPut, With(srv.URL).Path(paths.HistorySettings), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))
}

func TestHistorySettingsUnavailableBlocksPut(t *testing.T) {
	srv, _ := newArtifactHistoryTestServer(t, false)
	defer srv.Close()

	resp, err := http.Get(With(srv.URL).Path(paths.HistorySettings))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var settings artifactHistorySettingsResponse
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&settings))
	assert.False(t, settings.Available)
	assert.NotEmpty(t, settings.Reason)

	payload := []byte(`{"enabled":true,"retentionDays":7}`)
	req, _ := http.NewRequest(http.MethodPut, With(srv.URL).Path(paths.HistorySettings), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))
}

func TestHistorySettingsUnsupportedMethodReturnsJSON(t *testing.T) {
	srv, _ := newArtifactHistoryTestServer(t, true)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, With(srv.URL).Path(paths.HistorySettings), bytes.NewReader([]byte(`{}`)))
	resp, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Equal(t, "application/json; charset=utf-8", resp.Header.Get("Content-Type"))

	var body map[string]string
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "method not allowed", body["message"])
}

func TestArtifactHistorySessionStopWritesManifestAndListsDownloads(t *testing.T) {
	_, manager := newArtifactHistoryTestServer(t, true)

	logFile := filepath.Join(manager.logDir, "sess-1.log")
	videoFile := filepath.Join(manager.videoDir, "sess-1.mp4")
	assert.NoError(t, os.WriteFile(logFile, []byte("saved log"), 0o644))
	assert.NoError(t, os.WriteFile(videoFile, []byte("saved video"), 0o644))

	manager.copyFn = func(_ *session.Session, targetDir string) ([]artifactHistoryDownload, string, error) {
		assert.NoError(t, os.MkdirAll(targetDir, 0o755))
		downloadPath := filepath.Join(targetDir, "report.txt")
		assert.NoError(t, os.WriteFile(downloadPath, []byte("report-data"), 0o644))
		return []artifactHistoryDownload{{
			Filename:     "report.txt",
			Path:         downloadPath,
			RelativePath: "report.txt",
			SizeBytes:    int64(len("report-data")),
		}}, "captured", nil
	}

	sess := &session.Session{
		Quota:                  "team-a",
		ArtifactHistoryEnabled: true,
		Protocol:               session.ProtocolWebDriver,
		Started:                time.Date(2026, time.April, 8, 9, 0, 0, 0, time.UTC),
		DownloadsDir:           "/home/pwuser/Downloads",
		Caps: session.Caps{
			Name:      "chrome",
			Version:   "123.0",
			LogName:   "sess-1.log",
			Video:     true,
			VideoName: "sess-1.mp4",
		},
	}

	manager.OnSessionStopped(event.StoppedSession{
		Event: event.Event{
			RequestId: 1,
			SessionId: "sess-1",
			Session:   sess,
		},
	})

	manifestData, err := os.ReadFile(manager.manifestPath("sess-1"))
	assert.NoError(t, err)

	var manifest artifactHistoryManifest
	assert.NoError(t, json.Unmarshal(manifestData, &manifest))
	assert.Equal(t, "captured", manifest.CaptureStatus.Downloads)
	assert.NotNil(t, manifest.Artifacts.Log)
	assert.NotNil(t, manifest.Artifacts.Video)
	assert.Len(t, manifest.Artifacts.Downloads, 1)
	assert.Equal(t, logFile, manifest.Artifacts.Log.Path)
	assert.Equal(t, videoFile, manifest.Artifacts.Video.Path)

	items, err := manager.ListDownloads()
	assert.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, "sess-1", items[0].SessionID)
	assert.Equal(t, path.Join(paths.Downloads, "sess-1", "report.txt"), items[0].DownloadURL)
}

func TestArtifactHistoryPreCancelCaptureStashesDownloads(t *testing.T) {
	_, manager := newArtifactHistoryTestServer(t, true)

	copyCalls := 0
	manager.copyFn = func(_ *session.Session, targetDir string) ([]artifactHistoryDownload, string, error) {
		copyCalls++
		assert.NoError(t, os.MkdirAll(targetDir, 0o755))
		return []artifactHistoryDownload{{
			Filename:     "report.txt",
			Path:         filepath.Join(targetDir, "report.txt"),
			RelativePath: "report.txt",
			SizeBytes:    11,
		}}, "captured", nil
	}

	sess := &session.Session{
		ArtifactHistoryEnabled: true,
		Protocol:               session.ProtocolWebDriver,
		Started:                time.Date(2026, time.April, 8, 9, 0, 0, 0, time.UTC),
		Caps:                   session.Caps{Name: "chrome", Version: "123.0"},
	}
	manager.CaptureDownloadsForSession(sess, "sess-pre")
	assert.Equal(t, 1, copyCalls)

	// After pre-capture, OnSessionStopped must reuse the stash and not re-run copyFn.
	manager.OnSessionStopped(event.StoppedSession{
		Event: event.Event{RequestId: 2, SessionId: "sess-pre", Session: sess},
	})
	assert.Equal(t, 1, copyCalls)

	var manifest artifactHistoryManifest
	data, err := os.ReadFile(manager.manifestPath("sess-pre"))
	assert.NoError(t, err)
	assert.NoError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, "captured", manifest.CaptureStatus.Downloads)
	assert.Len(t, manifest.Artifacts.Downloads, 1)
}

func TestArtifactHistoryCleanupRemovesExpiredArtifacts(t *testing.T) {
	_, manager := newArtifactHistoryTestServer(t, true)
	manager.nowFn = func() time.Time {
		return time.Date(2026, time.April, 10, 12, 0, 0, 0, time.UTC)
	}
	assert.NoError(t, manager.UpdateSettings(artifactHistorySettings{
		Enabled:       true,
		RetentionDays: 1,
	}))

	logFile := filepath.Join(manager.logDir, "expired.log")
	videoFile := filepath.Join(manager.videoDir, "expired.mp4")
	downloadDir := filepath.Join(manager.downloadsDir(), "expired-session")
	downloadFile := filepath.Join(downloadDir, "artifact.txt")
	assert.NoError(t, os.WriteFile(logFile, []byte("saved log"), 0o644))
	assert.NoError(t, os.WriteFile(videoFile, []byte("saved video"), 0o644))
	assert.NoError(t, os.MkdirAll(downloadDir, 0o755))
	assert.NoError(t, os.WriteFile(downloadFile, []byte("download"), 0o644))

	manifest := artifactHistoryManifest{
		SessionID:      "expired-session",
		StartedAt:      time.Date(2026, time.April, 8, 8, 0, 0, 0, time.UTC),
		FinishedAt:     time.Date(2026, time.April, 8, 9, 0, 0, 0, time.UTC),
		Browser:        "chrome",
		BrowserVersion: "123.0",
		Protocol:       "webdriver",
		Quota:          "team-a",
	}
	manifest.Artifacts.Log = &artifactHistoryFileRef{Filename: "expired.log", Path: logFile}
	manifest.Artifacts.Video = &artifactHistoryFileRef{Filename: "expired.mp4", Path: videoFile}
	manifest.Artifacts.Downloads = []artifactHistoryDownload{{
		Filename:     "artifact.txt",
		Path:         downloadFile,
		RelativePath: "artifact.txt",
		SizeBytes:    int64(len("download")),
	}}
	manifest.CaptureStatus.Downloads = "captured"
	assert.NoError(t, manager.writeManifest(manifest))

	manager.RunCleanupOnce()

	_, err := os.Stat(logFile)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(videoFile)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(downloadDir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(manager.manifestPath("expired-session"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func newArtifactHistoryTestServer(t *testing.T, available bool) (*httptest.Server, *artifactHistoryManager) {
	t.Helper()

	rootDir := t.TempDir()
	settingsPath := filepath.Join(rootDir, "state", "artifact-history.json")
	artifactsDir := filepath.Join(rootDir, "artifacts")
	logsDir := filepath.Join(rootDir, "logs")
	videosDir := filepath.Join(rootDir, "video")
	assert.NoError(t, os.MkdirAll(logsDir, 0o755))
	assert.NoError(t, os.MkdirAll(videosDir, 0o755))

	previousHistory := artifactHistory
	previousHistoryDir := app.artifactHistoryDir
	previousHistorySettingsPath := app.artifactHistorySettingsPath
	previousLogDir := app.logOutputDir
	previousVideoDir := app.videoOutputDir
	previousDockerDisabled := app.disableDocker
	previousDockerClient := app.cli

	app.artifactHistoryDir = artifactsDir
	app.artifactHistorySettingsPath = settingsPath
	if available {
		app.logOutputDir = logsDir
	} else {
		app.logOutputDir = ""
	}
	app.videoOutputDir = videosDir
	app.disableDocker = false
	app.cli = &client.Client{}
	artifactHistory = nil

	manager := ensureArtifactHistoryManager()
	manager.copyFn = func(_ *session.Session, targetDir string) ([]artifactHistoryDownload, string, error) {
		assert.NoError(t, os.MkdirAll(targetDir, 0o755))
		return nil, "empty", nil
	}

	server := httptest.NewServer(handler())
	t.Cleanup(func() {
		server.Close()
		artifactHistory = previousHistory
		app.artifactHistoryDir = previousHistoryDir
		app.artifactHistorySettingsPath = previousHistorySettingsPath
		app.logOutputDir = previousLogDir
		app.videoOutputDir = previousVideoDir
		app.disableDocker = previousDockerDisabled
		app.cli = previousDockerClient
	})

	return server, manager
}
