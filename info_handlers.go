package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

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
// normalizes in selenwright-ui/src/app/api/settings.ts. Retention enforcement
// (periodic sweep of old video/logs/downloads) is not implemented yet — the
// settings round-trip through an in-memory store so the UI stays interactive
// and the backend can accept PUTs without 501ing.
type historySettingsPayload struct {
	Available     bool `json:"available"`
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

type historySettingsUpdate struct {
	Enabled       *bool `json:"enabled"`
	RetentionDays *int  `json:"retentionDays"`
}

const (
	historyRetentionMinDays = 1
	historyRetentionMaxDays = 365
)

var historyState = struct {
	mu            sync.Mutex
	enabled       bool
	retentionDays int
}{retentionDays: 7}

func snapshotHistorySettings() historySettingsPayload {
	historyState.mu.Lock()
	defer historyState.mu.Unlock()
	return historySettingsPayload{
		Available:     true,
		Enabled:       historyState.enabled,
		RetentionDays: historyState.retentionDays,
	}
}

func historySettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(snapshotHistorySettings())
	case http.MethodPut, http.MethodPost:
		var update historySettingsUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			writeHistoryError(w, http.StatusBadRequest, "invalid_body",
				fmt.Sprintf("could not parse body: %v", err))
			return
		}
		if update.RetentionDays != nil {
			days := *update.RetentionDays
			if days < historyRetentionMinDays || days > historyRetentionMaxDays {
				writeHistoryError(w, http.StatusBadRequest, "invalid_retention",
					fmt.Sprintf("retentionDays must be between %d and %d", historyRetentionMinDays, historyRetentionMaxDays))
				return
			}
		}
		historyState.mu.Lock()
		if update.Enabled != nil {
			historyState.enabled = *update.Enabled
		}
		if update.RetentionDays != nil {
			historyState.retentionDays = *update.RetentionDays
		}
		resp := historySettingsPayload{
			Available:     true,
			Enabled:       historyState.enabled,
			RetentionDays: historyState.retentionDays,
		}
		historyState.mu.Unlock()
		log.Printf("[-] [HISTORY_SETTINGS_UPDATED] [enabled=%t] [retentionDays=%d]", resp.Enabled, resp.RetentionDays)
		_ = json.NewEncoder(w).Encode(resp)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeHistoryError(w, http.StatusMethodNotAllowed, "method_not_allowed",
			fmt.Sprintf("method %s not allowed on /history/settings", r.Method))
	}
}

func writeHistoryError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   code,
		"message": message,
	})
}
