package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/aqa-alex/selenwright/config"
)

// configItem is one labeled entry in a configuration card on the UI.
// The UI's formatConfigurationValue accepts any JSON primitive.
type configItem struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Value interface{} `json:"value"`
}

// configRaw feeds the UI's "Raw configuration" card.
type configRaw struct {
	BrowserCatalog []config.BrowserCatalogEntry `json:"browserCatalog"`
	Flags          map[string]interface{}       `json:"flags"`
	ReloadStatus   map[string]interface{}       `json:"reloadStatus"`
}

// configPayload is the full /config response shape expected by the UI.
type configPayload struct {
	Limits              []configItem `json:"limits"`
	Paths               []configItem `json:"paths"`
	Logging             []configItem `json:"logging"`
	FeatureAvailability []configItem `json:"featureAvailability"`
	Raw                 configRaw    `json:"raw"`
}

func configInfo(w http.ResponseWriter, _ *http.Request) {
	payload := configPayload{
		Limits: []configItem{
			{Key: "limit", Label: "Simultaneous sessions", Value: app.limit},
			{Key: "retry-count", Label: "New session retry count", Value: app.retryCount},
			{Key: "timeout", Label: "Session idle timeout", Value: app.timeout.String()},
			{Key: "max-timeout", Label: "Max session idle timeout", Value: app.maxTimeout.String()},
			{Key: "session-attempt-timeout", Label: "New session attempt timeout", Value: app.newSessionAttemptTimeout.String()},
			{Key: "session-delete-timeout", Label: "Session delete timeout", Value: app.sessionDeleteTimeout.String()},
			{Key: "service-startup-timeout", Label: "Service startup timeout", Value: app.serviceStartupTimeout.String()},
			{Key: "graceful-period", Label: "Graceful shutdown period", Value: app.gracefulPeriod.String()},
			{Key: "max-create-body-bytes", Label: "Max /session body bytes", Value: app.maxCreateBodyBytes},
			{Key: "max-upload-body-bytes", Label: "Max /file body bytes", Value: app.maxUploadBodyBytes},
			{Key: "max-upload-extracted-bytes", Label: "Max /file extracted bytes", Value: app.maxUploadExtractedBytes},
			{Key: "max-ws-message-bytes", Label: "Max WS message bytes", Value: app.maxWSMessageBytes},
			{Key: "event-workers", Label: "Event dispatcher workers", Value: app.eventWorkers},
		},
		Paths: []configItem{
			{Key: "conf", Label: "Browsers config", Value: app.confPath},
			{Key: "log-conf", Label: "Container logging config", Value: stringOrNone(app.logConfPath)},
			{Key: "video-output-dir", Label: "Video output dir", Value: app.videoOutputDir},
			{Key: "log-output-dir", Label: "Session log output dir", Value: stringOrNone(app.logOutputDir)},
			{Key: "metrics-path", Label: "Metrics path", Value: metricsPathValue()},
		},
		Logging: []configItem{
			{Key: "log-json", Label: "JSON logs", Value: app.logJSON},
			{Key: "save-all-logs", Label: "Save all session logs", Value: app.saveAllLogs},
			{Key: "capture-driver-logs", Label: "Capture driver logs", Value: app.captureDriverLogs},
		},
		FeatureAvailability: []configItem{
			{Key: "file-upload", Label: "File upload", Value: app.enableFileUpload},
			{Key: "metrics", Label: "Prometheus metrics", Value: app.enableMetrics},
			{Key: "queue", Label: "Wait queue", Value: !app.disableQueue},
			{Key: "docker", Label: "Docker backend", Value: !app.disableDocker},
			{Key: "privileged", Label: "Privileged containers", Value: app.privilegedContainers},
			{Key: "cap-add-sys-admin", Label: "SYS_ADMIN capability", Value: app.capAddSysAdmin},
			{Key: "browser-network-isolation", Label: "Isolated browser network", Value: app.browserNetwork != ""},
		},
		Raw: configRaw{
			BrowserCatalog: app.conf.BrowserCatalog(),
			Flags: map[string]interface{}{
				"listen":               app.listen,
				"auth-mode":            app.authModeFlag,
				"caps-policy":          app.capsPolicyFlag,
				"container-network":    app.containerNetwork,
				"browser-network":      app.browserNetwork,
				"video-recorder-image": app.videoRecorderImage,
				"htpasswd":             stringOrNone(app.htpasswdPath),
				"user-header":          app.userHeaderFlag,
				"admin-header":         app.adminHeaderFlag,
				"allow-insecure-none":  app.allowInsecureNone,
				"allowed-origins":      stringOrNone(app.allowedOriginsRaw),
			},
			ReloadStatus: map[string]interface{}{
				"lastReloadTime": app.conf.LastReloadTime.Format("2006-01-02T15:04:05Z07:00"),
				"confPath":       app.confPath,
				"gitRevision":    gitRevision,
				"buildStamp":     buildStamp,
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func stringOrNone(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func metricsPathValue() string {
	if !app.enableMetrics {
		return "(disabled)"
	}
	return app.metricsPath
}

// historySettingsPayload matches the ArtifactHistorySettings shape the UI
// normalizes in selenwright-ui/src/app/api/settings.ts. Artifact retention is
// not implemented in Selenwright yet; available:false makes the UI render the
// "unavailable" note instead of a red protocol error.
type historySettingsPayload struct {
	Available     bool   `json:"available"`
	Enabled       bool   `json:"enabled"`
	Reason        string `json:"reason,omitempty"`
	RetentionDays int    `json:"retentionDays"`
}

const historySettingsUnavailableReason = "Artifact retention is not implemented in this Selenwright build."

func historySettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(historySettingsPayload{
			Available:     false,
			Enabled:       false,
			Reason:        historySettingsUnavailableReason,
			RetentionDays: 7,
		})
	case http.MethodPut, http.MethodPost:
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "not_implemented",
			"message": historySettingsUnavailableReason,
		})
	default:
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "method_not_allowed",
			"message": fmt.Sprintf("method %s not allowed on /history/settings", r.Method),
		})
	}
}
