package discovery

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aqa-alex/selenwright/config"
)

const labelPrefix = "io.selenwright."

const (
	LabelBrowser  = labelPrefix + "browser"
	LabelVersion  = labelPrefix + "version"
	LabelPort     = labelPrefix + "port"
	LabelProtocol = labelPrefix + "protocol"
	LabelPath     = labelPrefix + "path"
	LabelDefault  = labelPrefix + "default"
	LabelTmpfs    = labelPrefix + "tmpfs"
	LabelEnv      = labelPrefix + "env"
	LabelVolumes  = labelPrefix + "volumes"
	LabelHosts    = labelPrefix + "hosts"
	LabelShmSize  = labelPrefix + "shmSize"
	LabelMem      = labelPrefix + "mem"
	LabelCPU      = labelPrefix + "cpu"
	LabelLabels   = labelPrefix + "labels"
	LabelSysctl   = labelPrefix + "sysctl"
	LabelConfig   = labelPrefix + "config"
)

// ParseLabels converts Docker image labels into a config.Browser along with
// the browser name, version, and default flag. If the escape-hatch label
// io.selenwright.config is present, it is unmarshalled directly into a Browser
// and per-field labels are ignored.
func ParseLabels(labels map[string]string) (browser *config.Browser, name, version string, isDefault bool, err error) {
	name = labels[LabelBrowser]
	if name == "" {
		return nil, "", "", false, fmt.Errorf("missing required label %s", LabelBrowser)
	}
	version = labels[LabelVersion]
	if version == "" {
		return nil, "", "", false, fmt.Errorf("missing required label %s", LabelVersion)
	}
	isDefault = strings.EqualFold(labels[LabelDefault], "true")

	if raw, ok := labels[LabelConfig]; ok && raw != "" {
		browser = &config.Browser{}
		if err := json.Unmarshal([]byte(raw), browser); err != nil {
			return nil, "", "", false, fmt.Errorf("unmarshal %s: %w", LabelConfig, err)
		}
		return browser, name, version, isDefault, nil
	}

	port := labels[LabelPort]
	if port == "" {
		return nil, "", "", false, fmt.Errorf("missing required label %s", LabelPort)
	}

	browser = &config.Browser{
		Port:     port,
		Path:     labels[LabelPath],
		Protocol: labels[LabelProtocol],
		Mem:      labels[LabelMem],
		Cpu:      labels[LabelCPU],
	}

	if v := labels[LabelShmSize]; v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return nil, "", "", false, fmt.Errorf("parse %s %q: %w", LabelShmSize, v, err)
		}
		browser.ShmSize = n
	}

	if err := unmarshalJSONLabel(labels, LabelTmpfs, &browser.Tmpfs); err != nil {
		return nil, "", "", false, err
	}
	if err := unmarshalJSONLabel(labels, LabelEnv, &browser.Env); err != nil {
		return nil, "", "", false, err
	}
	if err := unmarshalJSONLabel(labels, LabelVolumes, &browser.Volumes); err != nil {
		return nil, "", "", false, err
	}
	if err := unmarshalJSONLabel(labels, LabelHosts, &browser.Hosts); err != nil {
		return nil, "", "", false, err
	}
	if err := unmarshalJSONLabel(labels, LabelLabels, &browser.Labels); err != nil {
		return nil, "", "", false, err
	}
	if err := unmarshalJSONLabel(labels, LabelSysctl, &browser.Sysctl); err != nil {
		return nil, "", "", false, err
	}

	return browser, name, version, isDefault, nil
}

func unmarshalJSONLabel(labels map[string]string, key string, dst interface{}) error {
	raw, ok := labels[key]
	if !ok || raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return nil
}
