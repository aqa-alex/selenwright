// Modified by [Aleksander R], 2026: added Playwright protocol support; added BrowserCatalog snapshot for /config endpoint; added ReloadStatus/Snapshot for artifact history; added Replace() for label-based discovery; added OwnerGroups in session state for group-based ACL

package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/api/types/container"
)

// Session - session id and vnc flag
type Session struct {
	ID            string             `json:"id"`
	Container     string             `json:"container,omitempty"`
	ContainerInfo *session.Container `json:"containerInfo,omitempty"`
	VNC           bool               `json:"vnc"`
	Screen        string             `json:"screen"`
	Caps          session.Caps       `json:"caps"`
	Started       time.Time          `json:"started"`
	OwnerGroups   []string           `json:"ownerGroups,omitempty"`
}

// Sessions - used count and individual sessions for quota user
type Sessions struct {
	Count    int       `json:"count"`
	Sessions []Session `json:"sessions"`
}

// Quota - list of sessions for quota user
type Quota map[string]*Sessions

// Version - browser version for quota
type Version map[string]Quota

// Browsers - browser names for versions
type Browsers map[string]Version

// State - current state
type State struct {
	Total    int      `json:"total"`
	Used     int      `json:"used"`
	Queued   int      `json:"queued"`
	Pending  int      `json:"pending"`
	Browsers Browsers `json:"browsers"`
}

// Browser configuration
type Browser struct {
	Image           interface{}       `json:"image"`
	Port            string            `json:"port"`
	Path            string            `json:"path"`
	Protocol        string            `json:"protocol,omitempty"`
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

// Versions configuration
type Versions struct {
	Default  string              `json:"default"`
	Versions map[string]*Browser `json:"versions"`
}

// Config current configuration
type Config struct {
	lock                  sync.RWMutex
	LastReloadTime        time.Time
	LastReloadAttemptTime time.Time
	LastReloadSuccessful  bool
	LastReloadError       string
	Browsers              map[string]Versions
	ContainerLogs         *container.LogConfig
}

// ReloadStatus stores the latest configuration reload outcome.
type ReloadStatus struct {
	LastReloadTime        time.Time
	LastReloadAttemptTime time.Time
	LastReloadSuccessful  bool
	LastReloadError       string
}

// Snapshot is a read-only copy of the loaded configuration state.
type Snapshot struct {
	Browsers      map[string]Versions
	ContainerLogs *container.LogConfig
	ReloadStatus  ReloadStatus
}

// NewConfig creates new config
func NewConfig() *Config {
	return &Config{Browsers: make(map[string]Versions), ContainerLogs: new(container.LogConfig), LastReloadTime: time.Now()}
}

func loadJSON(filename string, v interface{}) error {
	buf, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read error: %v", err)
	}
	if err := json.Unmarshal(buf, v); err != nil {
		return fmt.Errorf("parse error: %v", err)
	}
	return nil
}

// Load loads config from file
func (config *Config) Load(browsers, containerLogs string) error {
	log.Println("[-] [INIT] [Loading configuration files...]")
	attemptTime := time.Now()
	config.markReloadAttempt(attemptTime)
	br := make(map[string]Versions)
	err := loadJSON(browsers, &br)
	if err != nil {
		err = fmt.Errorf("browsers config: %v", err)
		config.markReloadFailure(err)
		return err
	}
	log.Printf("[-] [INIT] [Loaded configuration from %s]", browsers)
	cl := &container.LogConfig{}
	if containerLogs != "" {
		err = loadJSON(containerLogs, cl)
		if err != nil {
			err = fmt.Errorf("log config: %v", err)
			config.markReloadFailure(err)
			return err
		}
		log.Printf("[-] [INIT] [Loaded log configuration from %s]", containerLogs)
	}
	config.lock.Lock()
	defer config.lock.Unlock()
	config.Browsers, config.ContainerLogs = br, cl
	config.LastReloadTime = attemptTime
	config.LastReloadSuccessful = true
	config.LastReloadError = ""
	return nil
}

func (config *Config) markReloadAttempt(attemptTime time.Time) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.LastReloadAttemptTime = attemptTime
}

func (config *Config) markReloadFailure(err error) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.LastReloadSuccessful = false
	if err != nil {
		config.LastReloadError = err.Error()
		return
	}
	config.LastReloadError = ""
}

// ReloadStatus returns the current configuration reload status.
func (config *Config) ReloadStatus() ReloadStatus {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return ReloadStatus{
		LastReloadTime:        config.LastReloadTime,
		LastReloadAttemptTime: config.LastReloadAttemptTime,
		LastReloadSuccessful:  config.LastReloadSuccessful,
		LastReloadError:       config.LastReloadError,
	}
}

// Snapshot returns a deep copy of the loaded configuration state.
func (config *Config) Snapshot() Snapshot {
	config.lock.RLock()
	defer config.lock.RUnlock()
	return Snapshot{
		Browsers:      config.Browsers,
		ContainerLogs: config.ContainerLogs,
		ReloadStatus: ReloadStatus{
			LastReloadTime:        config.LastReloadTime,
			LastReloadAttemptTime: config.LastReloadAttemptTime,
			LastReloadSuccessful:  config.LastReloadSuccessful,
			LastReloadError:       config.LastReloadError,
		},
	}
}

// Replace atomically swaps the browser catalog, e.g. when label-based
// discovery rebuilds the catalog from Docker image labels.
func (config *Config) Replace(browsers map[string]Versions) {
	config.lock.Lock()
	defer config.lock.Unlock()
	config.Browsers = browsers
	config.LastReloadTime = time.Now()
}

// Find - find concrete browser
func (config *Config) Find(name string, version string) (*Browser, string, bool) {
	config.lock.RLock()
	defer config.lock.RUnlock()
	browser, ok := config.Browsers[name]
	if !ok {
		return nil, "", false
	}
	if version == "" {
		log.Printf("[-] [DEFAULT_VERSION] [Using default version: %s]", browser.Default)
		version = browser.Default
		if version == "" {
			return nil, "", false
		}
	}

	if b, ok := browser.Versions[version]; ok {
		return b, version, true
	}

	var prefixMatch *Browser
	var prefixVersion string
	for v, b := range browser.Versions {
		if !isPlaywrightBrowser(b) && strings.HasPrefix(v, version) {
			prefixMatch = b
			prefixVersion = v
			break
		}
	}
	if prefixMatch != nil {
		return prefixMatch, prefixVersion, true
	}

	playwrightMatches := findPlaywrightVersionMatches(browser.Versions, version)
	switch len(playwrightMatches) {
	case 0:
		return nil, version, false
	case 1:
		matchedVersion := playwrightMatches[0]
		return browser.Versions[matchedVersion], matchedVersion, true
	default:
		sort.Strings(playwrightMatches)
		log.Printf(
			"[-] [PLAYWRIGHT_VERSION_AMBIGUOUS] [%s] [%s] [%s]",
			name,
			version,
			strings.Join(playwrightMatches, ", "),
		)
		return nil, version, false
	}
}

func isPlaywrightBrowser(browser *Browser) bool {
	if browser == nil {
		return false
	}
	return strings.EqualFold(browser.Protocol, "playwright")
}

func findPlaywrightVersionMatches(versions map[string]*Browser, requestedVersion string) []string {
	requestedMajorMinor, ok := playwrightMajorMinor(requestedVersion)
	if !ok {
		return nil
	}

	matches := make([]string, 0, len(versions))
	for configuredVersion, browser := range versions {
		if !isPlaywrightBrowser(browser) {
			continue
		}

		configuredMajorMinor, ok := playwrightMajorMinor(configuredVersion)
		if !ok || configuredMajorMinor != requestedMajorMinor {
			continue
		}

		matches = append(matches, configuredVersion)
	}

	return matches
}

func playwrightMajorMinor(version string) (string, bool) {
	segments := strings.Split(version, ".")
	if len(segments) < 2 {
		return "", false
	}

	major, err := strconv.Atoi(segments[0])
	if err != nil {
		return "", false
	}
	minor, err := strconv.Atoi(segments[1])
	if err != nil {
		return "", false
	}

	return fmt.Sprintf("%d.%d", major, minor), true
}

// State - get current state
func (config *Config) State(sessions *session.Map, limit, queued, pending int) *State {
	config.lock.RLock()
	defer config.lock.RUnlock()
	state := &State{limit, 0, queued, pending, make(Browsers)}
	for n, b := range config.Browsers {
		state.Browsers[n] = make(Version)
		for v := range b.Versions {
			state.Browsers[n][v] = make(Quota)
		}
	}
	sessions.Each(func(id string, session *session.Session) {
		state.Used++
		browserName := session.Caps.BrowserName()
		version := session.Caps.Version
		_, ok := state.Browsers[browserName]
		if !ok {
			state.Browsers[browserName] = make(Version)
		}
		_, ok = state.Browsers[browserName][version]
		if !ok {
			state.Browsers[browserName][version] = make(Quota)
		}
		v, ok := state.Browsers[browserName][version][session.Quota]
		if !ok {
			v = &Sessions{0, []Session{}}
			state.Browsers[browserName][version][session.Quota] = v
		}
		v.Count++
		vnc := false
		if session.HostPort.VNC != "" {
			vnc = true
		}
		ctr := session.Container
		sess := Session{
			ID:            id,
			ContainerInfo: ctr,
			VNC:           vnc,
			Screen:        session.Caps.ScreenResolution,
			Caps:          session.Caps,
			Started:       session.Started,
			OwnerGroups:   session.OwnerGroups,
		}
		if ctr != nil {
			sess.Container = ctr.ID
		}
		v.Sessions = append(v.Sessions, sess)
	})
	return state
}

// BrowserCatalogVersion is one configured version of a browser, flattened for
// the /config endpoint consumers.
type BrowserCatalogVersion struct {
	Version string `json:"version"`
	Image   string `json:"image"`
}

// BrowserCatalogEntry is a single browser with all its configured versions,
// sorted deterministically for stable output.
type BrowserCatalogEntry struct {
	Name     string                  `json:"name"`
	Default  string                  `json:"default,omitempty"`
	Versions []BrowserCatalogVersion `json:"versions"`
}

// BrowserCatalog returns a deterministic snapshot of the configured browser
// catalog suitable for the /config endpoint. Image values that are not plain
// strings (legacy driver configs) are rendered via fmt.Sprint.
func (config *Config) BrowserCatalog() []BrowserCatalogEntry {
	config.lock.RLock()
	defer config.lock.RUnlock()
	catalog := make([]BrowserCatalogEntry, 0, len(config.Browsers))
	for name, versions := range config.Browsers {
		entry := BrowserCatalogEntry{Name: name, Default: versions.Default}
		for version, browser := range versions.Versions {
			image := ""
			if browser != nil && browser.Image != nil {
				if s, ok := browser.Image.(string); ok {
					image = s
				} else {
					image = fmt.Sprint(browser.Image)
				}
			}
			entry.Versions = append(entry.Versions, BrowserCatalogVersion{
				Version: version,
				Image:   image,
			})
		}
		sort.Slice(entry.Versions, func(i, j int) bool {
			return entry.Versions[i].Version < entry.Versions[j].Version
		})
		catalog = append(catalog, entry)
	}
	sort.Slice(catalog, func(i, j int) bool {
		return catalog[i].Name < catalog[j].Name
	})
	return catalog
}
