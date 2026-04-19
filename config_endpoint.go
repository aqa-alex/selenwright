package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aqa-alex/selenwright/config"
	"github.com/docker/docker/api/types/container"
)

const (
	featureEnabled  = "Enabled"
	featureDisabled = "Disabled"
)

type configEntry struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type configResponse struct {
	Limits              []configEntry     `json:"limits"`
	Paths               []configEntry     `json:"paths"`
	Logging             []configEntry     `json:"logging"`
	FeatureAvailability []configEntry     `json:"featureAvailability"`
	Raw                 configResponseRaw `json:"raw"`
}

type configResponseRaw struct {
	BrowserCatalog []browserCatalogItem       `json:"browserCatalog"`
	Flags          configResponseFlags        `json:"flags"`
	ReloadStatus   configResponseReloadStatus `json:"reloadStatus"`
}

type configResponseFlags struct {
	Listen                  string `json:"listen"`
	Conf                    string `json:"conf"`
	LogConf                 string `json:"logConf"`
	Limit                   int    `json:"limit"`
	RetryCount              int    `json:"retryCount"`
	Timeout                 string `json:"timeout"`
	MaxTimeout              string `json:"maxTimeout"`
	SessionAttemptTimeout   string `json:"sessionAttemptTimeout"`
	SessionDeleteTimeout    string `json:"sessionDeleteTimeout"`
	ServiceStartupTimeout   string `json:"serviceStartupTimeout"`
	GracefulPeriod          string `json:"gracefulPeriod"`
	ContainerNetwork        string `json:"containerNetwork"`
	DisableDocker           bool   `json:"disableDocker"`
	DisableQueue            bool   `json:"disableQueue"`
	EnableFileUpload        bool   `json:"enableFileUpload"`
	CaptureDriverLogs       bool   `json:"captureDriverLogs"`
	DisablePrivileged       bool   `json:"disablePrivileged"`
	VideoOutputDir          string `json:"videoOutputDir"`
	VideoRecorderImage      string `json:"videoRecorderImage"`
	LogOutputDir            string `json:"logOutputDir"`
	SaveAllLogs             bool   `json:"saveAllLogs"`
	ArtifactHistoryDir      string `json:"artifactHistoryDir"`
	ArtifactHistorySettings string `json:"artifactHistorySettings"`
}

type configResponseReloadStatus struct {
	LastReloadTime        string `json:"lastReloadTime"`
	LastReloadAttemptTime string `json:"lastReloadAttemptTime"`
	LastReloadSuccessful  bool   `json:"lastReloadSuccessful"`
	LastReloadError       string `json:"lastReloadError"`
}

type browserCatalogItem struct {
	Name           string                  `json:"name"`
	DefaultVersion string                  `json:"defaultVersion"`
	Versions       []browserCatalogVersion `json:"versions"`
}

type browserCatalogVersion struct {
	Version         string            `json:"version"`
	Image           interface{}       `json:"image"`
	Port            string            `json:"port"`
	Path            string            `json:"path"`
	Protocol        string            `json:"protocol"`
	Tmpfs           map[string]string `json:"tmpfs,omitempty"`
	Volumes         []string          `json:"volumes,omitempty"`
	Env             []string          `json:"env,omitempty"`
	Hosts           []string          `json:"hosts,omitempty"`
	ShmSize         int64             `json:"shmSize,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	Sysctl          map[string]string `json:"sysctl,omitempty"`
	Mem             string            `json:"mem,omitempty"`
	Cpu             string            `json:"cpu,omitempty"`
	PublishAllPorts bool              `json:"publishAllPorts,omitempty"`
}

func configHandler(w http.ResponseWriter, _ *http.Request) {
	response := buildConfigResponse()
	payload, err := json.Marshal(response)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to build config response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func buildConfigResponse() configResponse {
	response := configResponse{
		Limits:              make([]configEntry, 0, 8),
		Paths:               make([]configEntry, 0, 7),
		Logging:             make([]configEntry, 0, 4),
		FeatureAvailability: make([]configEntry, 0, 11),
		Raw: configResponseRaw{
			BrowserCatalog: make([]browserCatalogItem, 0),
			Flags:          buildConfigFlags(),
			ReloadStatus:   configResponseReloadStatus{},
		},
	}

	configSnapshot := config.Snapshot{
		Browsers:      map[string]config.Versions{},
		ContainerLogs: nil,
	}
	if app.conf != nil {
		configSnapshot = app.conf.Snapshot()
	}

	historyManager := ensureArtifactHistoryManager()
	downloadsDir := ""
	historyAvailable := false
	historyReason := ""
	historyEnabledForNewSessions := false
	if historyManager != nil {
		downloadsDir = historyManager.downloadsDir()
		historyAvailable, historyReason = historyManager.Availability()
		historyEnabledForNewSessions = historyManager.IsEnabledForNewSessions()
	}

	response.Limits = buildConfigLimits()
	response.Paths = buildConfigPaths(downloadsDir)
	response.Logging = buildConfigLogging(configSnapshot.ContainerLogs, historyEnabledForNewSessions)
	response.FeatureAvailability = buildFeatureAvailability(configSnapshot.Browsers, historyAvailable, historyReason)
	response.Raw.BrowserCatalog = buildBrowserCatalog(configSnapshot.Browsers)
	response.Raw.ReloadStatus = buildReloadStatus(configSnapshot.ReloadStatus)

	return response
}

func buildConfigLimits() []configEntry {
	return []configEntry{
		newConfigEntry("maxSessions", "Max sessions", fmt.Sprintf("%d", app.limit)),
		newConfigEntry("retryCount", "Retry count", fmt.Sprintf("%d", app.retryCount)),
		newConfigEntry("sessionTimeout", "Session timeout", app.timeout.String()),
		newConfigEntry("maxTimeout", "Max timeout", app.maxTimeout.String()),
		newConfigEntry("sessionAttemptTimeout", "Session attempt timeout", app.newSessionAttemptTimeout.String()),
		newConfigEntry("sessionDeleteTimeout", "Session delete timeout", app.sessionDeleteTimeout.String()),
		newConfigEntry("serviceStartupTimeout", "Service startup timeout", app.serviceStartupTimeout.String()),
		newConfigEntry("gracefulPeriod", "Graceful period", app.gracefulPeriod.String()),
	}
}

func buildConfigPaths(downloadsDir string) []configEntry {
	return []configEntry{
		newConfigEntry("browsersConfig", "Browsers config", app.confPath),
		newConfigEntry("logConfig", "Log config", app.logConfPath),
		newConfigEntry("videoOutputDir", "Video output dir", app.videoOutputDir),
		newConfigEntry("logOutputDir", "Log output dir", app.logOutputDir),
		newConfigEntry("artifactHistoryDir", "Artifact history dir", app.artifactHistoryDir),
		newConfigEntry("artifactHistorySettings", "Artifact history settings", app.artifactHistorySettingsPath),
		newConfigEntry("downloadsDir", "Downloads dir", downloadsDir),
	}
}

func buildConfigLogging(logConfig *container.LogConfig, historyEnabledForNewSessions bool) []configEntry {
	return []configEntry{
		newConfigEntry("containerLogDriver", "Container log driver", logConfigType(logConfig)),
		newConfigEntry("containerLogOptions", "Container log options", stringifyStringMap(logConfigConfig(logConfig))),
		newConfigEntry("captureDriverLogs", "Capture driver logs", boolLabel(app.captureDriverLogs)),
		newConfigEntry("logCaptureEffective", "Effective log capture", effectiveLogCaptureLabel(app.logOutputDir, app.saveAllLogs, historyEnabledForNewSessions)),
	}
}

func effectiveLogCaptureLabel(logOutputDir string, saveAllLogs, historyEnabledForNewSessions bool) string {
	if logOutputDir == "" {
		return featureDisabled
	}
	if saveAllLogs {
		return "Enabled (via -save-all-logs)"
	}
	if historyEnabledForNewSessions {
		return "Enabled (via artifact history)"
	}
	return "Opt-in (enableLog capability)"
}

func buildFeatureAvailability(browsers map[string]config.Versions, historyAvailable bool, historyReason string) []configEntry {
	return []configEntry{
		newConfigEntry("dockerBackedSessions", "Docker-backed sessions", boolLabel(!app.disableDocker)),
		newConfigEntry("waitQueue", "Wait queue", boolLabel(!app.disableQueue)),
		newConfigEntry("fileUpload", "File upload", boolLabel(app.enableFileUpload)),
		newConfigEntry("vncProxy", "VNC proxy", featureEnabled),
		newConfigEntry("videoRecording", "Video recording", boolLabel(!app.disableDocker && app.videoOutputDir != "")),
		newConfigEntry("logCapture", "Log capture", boolLabel(!app.disableDocker || app.logOutputDir != "" || app.captureDriverLogs)),
		newConfigEntry("clipboardApi", "Clipboard API", boolLabel(!app.disableDocker)),
		newConfigEntry("devtoolsProxy", "DevTools proxy", boolLabel(!app.disableDocker)),
		newConfigEntry("playwright", "Playwright", boolLabel(hasPlaywrightBrowsers(browsers))),
		newConfigEntry("artifactHistory", "Artifact history", availabilityLabel(historyAvailable, historyReason)),
		newConfigEntry("persistedDownloads", "Persisted downloads", availabilityLabel(historyAvailable, historyReason)),
	}
}

func buildBrowserCatalog(browsers map[string]config.Versions) []browserCatalogItem {
	if len(browsers) == 0 {
		return []browserCatalogItem{}
	}

	browserNames := make([]string, 0, len(browsers))
	for browserName := range browsers {
		browserNames = append(browserNames, browserName)
	}
	sort.Strings(browserNames)

	catalog := make([]browserCatalogItem, 0, len(browserNames))
	for _, browserName := range browserNames {
		versions := browsers[browserName]
		versionKeys := make([]string, 0, len(versions.Versions))
		for version := range versions.Versions {
			versionKeys = append(versionKeys, version)
		}
		sort.Strings(versionKeys)

		versionItems := make([]browserCatalogVersion, 0, len(versionKeys))
		for _, versionKey := range versionKeys {
			browser := versions.Versions[versionKey]
			if browser == nil {
				versionItems = append(versionItems, browserCatalogVersion{
					Version:  versionKey,
					Protocol: normalizedProtocol(""),
				})
				continue
			}

			versionItems = append(versionItems, browserCatalogVersion{
				Version:         versionKey,
				Image:           browser.Image,
				Port:            browser.Port,
				Path:            browser.Path,
				Protocol:        normalizedProtocol(browser.Protocol),
				Tmpfs:           browser.Tmpfs,
				Volumes:         browser.Volumes,
				Env:             browser.Env,
				Hosts:           browser.Hosts,
				ShmSize:         browser.ShmSize,
				Labels:          browser.Labels,
				Sysctl:          browser.Sysctl,
				Mem:             browser.Mem,
				Cpu:             browser.Cpu,
				PublishAllPorts: browser.PublishAllPorts,
			})
		}

		catalog = append(catalog, browserCatalogItem{
			Name:           browserName,
			DefaultVersion: versions.Default,
			Versions:       versionItems,
		})
	}

	return catalog
}

func buildConfigFlags() configResponseFlags {
	return configResponseFlags{
		Listen:                  app.listen,
		Conf:                    app.confPath,
		LogConf:                 app.logConfPath,
		Limit:                   app.limit,
		RetryCount:              app.retryCount,
		Timeout:                 app.timeout.String(),
		MaxTimeout:              app.maxTimeout.String(),
		SessionAttemptTimeout:   app.newSessionAttemptTimeout.String(),
		SessionDeleteTimeout:    app.sessionDeleteTimeout.String(),
		ServiceStartupTimeout:   app.serviceStartupTimeout.String(),
		GracefulPeriod:          app.gracefulPeriod.String(),
		ContainerNetwork:        app.containerNetwork,
		DisableDocker:           app.disableDocker,
		DisableQueue:            app.disableQueue,
		EnableFileUpload:        app.enableFileUpload,
		CaptureDriverLogs:       app.captureDriverLogs,
		DisablePrivileged:       !app.privilegedContainers,
		VideoOutputDir:          app.videoOutputDir,
		VideoRecorderImage:      app.videoRecorderImage,
		LogOutputDir:            app.logOutputDir,
		SaveAllLogs:             app.saveAllLogs,
		ArtifactHistoryDir:      app.artifactHistoryDir,
		ArtifactHistorySettings: app.artifactHistorySettingsPath,
	}
}

func buildReloadStatus(status config.ReloadStatus) configResponseReloadStatus {
	return configResponseReloadStatus{
		LastReloadTime:        formatTimeOrEmpty(status.LastReloadTime),
		LastReloadAttemptTime: formatTimeOrEmpty(status.LastReloadAttemptTime),
		LastReloadSuccessful:  status.LastReloadSuccessful,
		LastReloadError:       status.LastReloadError,
	}
}

func newConfigEntry(key, label, value string) configEntry {
	return configEntry{
		Key:   key,
		Label: label,
		Value: value,
	}
}

func boolLabel(enabled bool) string {
	if enabled {
		return featureEnabled
	}
	return featureDisabled
}

func availabilityLabel(available bool, reason string) string {
	if available {
		return featureEnabled
	}
	if reason != "" {
		return fmt.Sprintf("Unavailable: %s", reason)
	}
	return featureDisabled
}

func hasPlaywrightBrowsers(browsers map[string]config.Versions) bool {
	for _, versions := range browsers {
		for _, browser := range versions.Versions {
			if browser != nil && normalizedProtocol(browser.Protocol) == "playwright" {
				return true
			}
		}
	}
	return false
}

func normalizedProtocol(protocol string) string {
	if strings.EqualFold(protocol, "playwright") {
		return "playwright"
	}
	return "webdriver"
}

func stringifyStringMap(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}

	payload, err := json.Marshal(values)
	if err != nil {
		return ""
	}
	return string(payload)
}

func logConfigType(logConfig *container.LogConfig) string {
	if logConfig == nil {
		return ""
	}
	return logConfig.Type
}

func logConfigConfig(logConfig *container.LogConfig) map[string]string {
	if logConfig == nil {
		return nil
	}
	return logConfig.Config
}

func formatTimeOrEmpty(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
