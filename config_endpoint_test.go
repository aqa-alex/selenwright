package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/config"
	"github.com/docker/docker/client"
	assert "github.com/stretchr/testify/require"
)

type configEndpointTestServerOptions struct {
	logConfigJSON    string
	logOutputEnabled bool
	enableFileUpload bool
	disableQueue     bool
}

func TestConfigEndpointContract(t *testing.T) {
	srv := newConfigEndpointTestServer(t, configEndpointTestServerOptions{
		logConfigJSON:    `{"Type":"json-file","Config":{"max-size":"10m"}}`,
		logOutputEnabled: true,
		enableFileUpload: true,
	})
	defer srv.Close()

	resp, err := http.Get(With(srv.URL).Path(paths.Config))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var topLevel map[string]json.RawMessage
	assert.NoError(t, json.Unmarshal(body, &topLevel))
	assert.Equal(t, []string{"featureAvailability", "limits", "logging", "paths", "raw"}, sortedKeys(topLevel))

	var payload configResponse
	assert.NoError(t, json.Unmarshal(body, &payload))
	assert.Equal(t, []string{"maxSessions", "retryCount", "sessionTimeout", "maxTimeout", "sessionAttemptTimeout", "sessionDeleteTimeout", "serviceStartupTimeout", "gracefulPeriod"}, entryKeys(payload.Limits))
	assert.Equal(t, []string{"Max sessions", "Retry count", "Session timeout", "Max timeout", "Session attempt timeout", "Session delete timeout", "Service startup timeout", "Graceful period"}, entryLabels(payload.Limits))
	assert.Equal(t, []string{"browsersConfig", "logConfig", "videoOutputDir", "logOutputDir", "artifactHistoryDir", "artifactHistorySettings", "downloadsDir"}, entryKeys(payload.Paths))
	assert.Equal(t, []string{"containerLogDriver", "containerLogOptions", "captureDriverLogs", "saveAllLogs"}, entryKeys(payload.Logging))
	assert.Equal(t, []string{"dockerBackedSessions", "waitQueue", "fileUpload", "vncProxy", "videoRecording", "logCapture", "clipboardApi", "devtoolsProxy", "playwright", "artifactHistory", "persistedDownloads"}, entryKeys(payload.FeatureAvailability))

	assert.Len(t, payload.Raw.BrowserCatalog, 3)
	assert.Equal(t, []string{"chrome", "chromium", "firefox"}, browserNames(payload.Raw.BrowserCatalog))
	assert.Equal(t, "webdriver", payload.Raw.BrowserCatalog[0].Versions[0].Protocol)
	assert.Equal(t, "playwright", payload.Raw.BrowserCatalog[1].Versions[0].Protocol)

	var raw map[string]json.RawMessage
	assert.NoError(t, json.Unmarshal(topLevel["raw"], &raw))
	assert.Equal(t, []string{"browserCatalog", "flags", "reloadStatus"}, sortedKeys(raw))

	var flags map[string]interface{}
	assert.NoError(t, json.Unmarshal(raw["flags"], &flags))
	assert.Equal(t, []string{
		"artifactHistoryDir",
		"artifactHistorySettings",
		"captureDriverLogs",
		"conf",
		"containerNetwork",
		"disableDocker",
		"disablePrivileged",
		"disableQueue",
		"enableFileUpload",
		"gracefulPeriod",
		"limit",
		"listen",
		"logConf",
		"logOutputDir",
		"maxTimeout",
		"retryCount",
		"saveAllLogs",
		"serviceStartupTimeout",
		"sessionAttemptTimeout",
		"sessionDeleteTimeout",
		"timeout",
		"videoOutputDir",
		"videoRecorderImage",
	}, sortedKeys(flags))
	assert.Equal(t, float64(12), flags["limit"])
	assert.Equal(t, "127.0.0.1:5555", flags["listen"])

	var reloadStatus map[string]interface{}
	assert.NoError(t, json.Unmarshal(raw["reloadStatus"], &reloadStatus))
	assert.Equal(t, []string{"lastReloadAttemptTime", "lastReloadError", "lastReloadSuccessful", "lastReloadTime"}, sortedKeys(reloadStatus))
	assert.Equal(t, true, reloadStatus["lastReloadSuccessful"])
}

func TestConfigEndpointHandlesPartialData(t *testing.T) {
	srv := newConfigEndpointTestServer(t, configEndpointTestServerOptions{})
	defer srv.Close()

	resp, err := http.Get(With(srv.URL).Path(paths.Config))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var payload configResponse
	assert.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	assert.Equal(t, "", payload.Paths[1].Value)
	assert.Equal(t, "", payload.Paths[3].Value)
	assert.Equal(t, "", payload.Logging[0].Value)
	assert.Equal(t, "", payload.Logging[1].Value)
	assert.Equal(t, "Disabled", payload.FeatureAvailability[2].Value)
	assert.Equal(t, "Enabled", payload.FeatureAvailability[8].Value)
	assert.Contains(t, payload.FeatureAvailability[9].Value, "Unavailable:")
	assert.Contains(t, payload.FeatureAvailability[10].Value, "Unavailable:")
	assert.NotNil(t, payload.Raw.BrowserCatalog)
	assert.True(t, payload.Raw.ReloadStatus.LastReloadSuccessful)
	assert.Equal(t, "", payload.Raw.Flags.LogConf)
	assert.Equal(t, "", payload.Raw.Flags.LogOutputDir)
}

func newConfigEndpointTestServer(t *testing.T, opts configEndpointTestServerOptions) *httptest.Server {
	t.Helper()

	tempDir := t.TempDir()
	browsersPath := filepath.Join(tempDir, "browsers.json")
	logConfigPath := ""
	logsDir := ""
	if opts.logOutputEnabled {
		logsDir = filepath.Join(tempDir, "logs")
		assert.NoError(t, os.MkdirAll(logsDir, 0o755))
	}
	videoDir := filepath.Join(tempDir, "video")
	assert.NoError(t, os.MkdirAll(videoDir, 0o755))
	artifactDir := filepath.Join(tempDir, "artifacts")
	artifactSettingsPath := filepath.Join(tempDir, "state", "artifact-history.json")

	browsersJSON := `{
		"firefox": {
			"default": "latest",
			"versions": {
				"latest": {
					"image": "selenwright/firefox",
					"port": "4444",
					"path": "/wd/hub"
				}
			}
		},
		"chromium": {
			"default": "1.56.1",
			"versions": {
				"1.56.1": {
					"image": "selenwright/playwright-chromium:1.56.1",
					"port": "3000",
					"path": "/",
					"protocol": "playwright"
				}
			}
		},
		"chrome": {
			"default": "latest",
			"versions": {
				"latest": {
					"image": "selenwright/chrome",
					"port": "4444"
				}
			}
		}
	}`
	assert.NoError(t, os.WriteFile(browsersPath, []byte(browsersJSON), 0o644))
	if opts.logConfigJSON != "" {
		logConfigPath = filepath.Join(tempDir, "container-logs.json")
		assert.NoError(t, os.WriteFile(logConfigPath, []byte(opts.logConfigJSON), 0o644))
	}

	cfg := config.NewConfig()
	assert.NoError(t, cfg.Load(browsersPath, logConfigPath))

	previousConf := app.conf
	previousConfPath := app.confPath
	previousLogConfPath := app.logConfPath
	previousListen := app.listen
	previousLimit := app.limit
	previousRetryCount := app.retryCount
	previousTimeout := app.timeout
	previousMaxTimeout := app.maxTimeout
	previousSessionAttemptTimeout := app.newSessionAttemptTimeout
	previousSessionDeleteTimeout := app.sessionDeleteTimeout
	previousServiceStartupTimeout := app.serviceStartupTimeout
	previousGracefulPeriod := app.gracefulPeriod
	previousContainerNetwork := app.containerNetwork
	previousDisableDocker := app.disableDocker
	previousDisableQueue := app.disableQueue
	previousEnableFileUpload := app.enableFileUpload
	previousCaptureDriverLogs := app.captureDriverLogs
	previousPrivilegedContainers := app.privilegedContainers
	previousVideoOutputDir := app.videoOutputDir
	previousVideoRecorderImage := app.videoRecorderImage
	previousLogOutputDir := app.logOutputDir
	previousSaveAllLogs := app.saveAllLogs
	previousArtifactHistoryDir := app.artifactHistoryDir
	previousArtifactHistorySettingsPath := app.artifactHistorySettingsPath
	previousArtifactHistory := artifactHistory
	previousDockerClient := app.cli

	app.conf = cfg
	app.confPath = browsersPath
	app.logConfPath = logConfigPath
	app.listen = "127.0.0.1:5555"
	app.limit = 12
	app.retryCount = 3
	app.timeout = 15 * time.Minute
	app.maxTimeout = 90 * time.Minute
	app.newSessionAttemptTimeout = 45 * time.Second
	app.sessionDeleteTimeout = 35 * time.Second
	app.serviceStartupTimeout = 40 * time.Second
	app.gracefulPeriod = 10 * time.Minute
	app.containerNetwork = "config-test-network"
	app.disableDocker = false
	app.disableQueue = opts.disableQueue
	app.enableFileUpload = opts.enableFileUpload
	app.captureDriverLogs = false
	app.privilegedContainers = false // maps to disablePrivileged=true
	app.videoOutputDir = videoDir
	app.videoRecorderImage = "custom/video-recorder:latest"
	app.logOutputDir = logsDir
	app.saveAllLogs = true
	app.artifactHistoryDir = artifactDir
	app.artifactHistorySettingsPath = artifactSettingsPath
	artifactHistory = nil
	app.cli = &client.Client{}

	server := httptest.NewServer(handler())
	t.Cleanup(func() {
		server.Close()
		app.conf = previousConf
		app.confPath = previousConfPath
		app.logConfPath = previousLogConfPath
		app.listen = previousListen
		app.limit = previousLimit
		app.retryCount = previousRetryCount
		app.timeout = previousTimeout
		app.maxTimeout = previousMaxTimeout
		app.newSessionAttemptTimeout = previousSessionAttemptTimeout
		app.sessionDeleteTimeout = previousSessionDeleteTimeout
		app.serviceStartupTimeout = previousServiceStartupTimeout
		app.gracefulPeriod = previousGracefulPeriod
		app.containerNetwork = previousContainerNetwork
		app.disableDocker = previousDisableDocker
		app.disableQueue = previousDisableQueue
		app.enableFileUpload = previousEnableFileUpload
		app.captureDriverLogs = previousCaptureDriverLogs
		app.privilegedContainers = previousPrivilegedContainers
		app.videoOutputDir = previousVideoOutputDir
		app.videoRecorderImage = previousVideoRecorderImage
		app.logOutputDir = previousLogOutputDir
		app.saveAllLogs = previousSaveAllLogs
		app.artifactHistoryDir = previousArtifactHistoryDir
		app.artifactHistorySettingsPath = previousArtifactHistorySettingsPath
		artifactHistory = previousArtifactHistory
		app.cli = previousDockerClient
	})

	return server
}

func entryKeys(entries []configEntry) []string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key)
	}
	return keys
}

func entryLabels(entries []configEntry) []string {
	labels := make([]string, 0, len(entries))
	for _, entry := range entries {
		labels = append(labels, entry.Label)
	}
	return labels
}

func browserNames(entries []browserCatalogItem) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func sortedKeys[M ~map[string]V, V any](values M) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
