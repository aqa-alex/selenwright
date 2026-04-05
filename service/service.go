// Modified by [Aleksander R], 2026: added Playwright protocol support

package service

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
)

// Environment - all settings that influence browser startup
type Environment struct {
	IP                   string
	InDocker             bool
	CPU                  int64
	Memory               int64
	Network              string
	Hostname             string
	StartupTimeout       time.Duration
	SessionDeleteTimeout time.Duration
	CaptureDriverLogs    bool
	VideoOutputDir       string
	VideoContainerImage  string
	LogOutputDir         string
	SaveAllLogs          bool
	Privileged           bool
}

const (
	DefaultContainerNetwork = "default"
	readinessProbeInterval  = 50 * time.Millisecond
	readinessProbeTimeout   = time.Second
	protocolWebDriver       = "webdriver"
	protocolPlaywright      = "playwright"
)

// ServiceBase - stores fields required by all services
type ServiceBase struct {
	RequestId uint64
	Service   *config.Browser
}

// StartedService - all started service properties
type StartedService struct {
	Url           *url.URL
	PlaywrightURL *url.URL
	Container     *session.Container
	HostPort      session.HostPort
	Origin        string
	Cancel        func()
}

// Starter - interface to create session with cancellation ability
type Starter interface {
	StartWithCancel() (*StartedService, error)
}

// Manager - interface to choose appropriate starter
type Manager interface {
	Find(caps session.Caps, requestId uint64) (Starter, bool)
}

// DefaultManager - struct for default implementation
type DefaultManager struct {
	Environment *Environment
	Client      *client.Client
	Config      *config.Config
}

// Find - default implementation Manager interface
func (m *DefaultManager) Find(caps session.Caps, requestId uint64) (Starter, bool) {
	browserName := caps.BrowserName()
	version := caps.Version
	log.Printf("[%d] [LOCATING_SERVICE] [%s] [%s]", requestId, browserName, version)
	service, version, ok := m.Config.Find(browserName, version)
	serviceBase := ServiceBase{RequestId: requestId, Service: service}
	if !ok {
		return nil, false
	}
	switch service.Image.(type) {
	case string:
		if m.Client == nil {
			return nil, false
		}
		log.Printf("[%d] [USING_DOCKER] [%s] [%s]", requestId, browserName, version)
		return &Docker{
			ServiceBase: serviceBase,
			Environment: *m.Environment,
			Caps:        caps,
			Client:      m.Client,
			LogConfig:   m.Config.ContainerLogs}, true
	case []interface{}:
		log.Printf("[%d] [USING_DRIVER] [%s] [%s]", requestId, browserName, version)
		return &Driver{ServiceBase: serviceBase, Environment: *m.Environment, Caps: caps}, true
	}
	return nil, false
}

func serviceProtocol(service *config.Browser) string {
	if service == nil {
		return protocolWebDriver
	}
	switch strings.ToLower(service.Protocol) {
	case protocolPlaywright:
		return protocolPlaywright
	default:
		return protocolWebDriver
	}
}

func servicePath(service *config.Browser) string {
	if serviceProtocol(service) == protocolPlaywright && service.Path == "" {
		return "/"
	}
	if service == nil {
		return ""
	}
	return service.Path
}

func wait(u string, t time.Duration) error {
	return waitHTTP(u, t)
}

func waitHTTP(u string, t time.Duration) error {
	return waitFor(
		t,
		func(timeout time.Duration) error {
			req, _ := http.NewRequest(http.MethodHead, u, nil)
			req.Close = true
			client := &http.Client{Timeout: timeout}
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			return err
		},
		func() error {
			return fmt.Errorf("%s does not respond in %v", u, t)
		},
	)
}

func waitTCP(address string, t time.Duration) error {
	return waitFor(
		t,
		func(timeout time.Duration) error {
			conn, err := net.DialTimeout("tcp", address, timeout)
			if err != nil {
				return err
			}
			return conn.Close()
		},
		func() error {
			return fmt.Errorf("%s does not respond in %v", address, t)
		},
	)
}

func waitFor(timeout time.Duration, probe func(time.Duration) error, timeoutErr func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return timeoutErr()
		}
		if err := probe(probeTimeout(remaining)); err == nil {
			return nil
		}
		sleep := readinessProbeInterval
		if remaining < sleep {
			sleep = remaining
		}
		time.Sleep(sleep)
	}
}

func probeTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout < readinessProbeTimeout {
		return timeout
	}
	return readinessProbeTimeout
}
