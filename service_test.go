// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/gorilla/websocket"
	assert "github.com/stretchr/testify/require"
)

var (
	mockServer *httptest.Server
	lock       sync.Mutex
)

func init() {
	updateMux(testMux())
	app.timeout = 2 * time.Second
	app.serviceStartupTimeout = 1 * time.Second
	app.newSessionAttemptTimeout = 1 * time.Second
	app.sessionDeleteTimeout = 1 * time.Second
}

func updateMux(mux http.Handler) {
	lock.Lock()
	defer lock.Unlock()
	mockServer = httptest.NewServer(mux)
	_ = os.Setenv("DOCKER_HOST", "tcp://"+hostPort(mockServer.URL))
	_ = os.Setenv("DOCKER_API_VERSION", "1.29")
	app.cli, _ = client.NewClientWithOpts(client.FromEnv)
}

func testMux() http.Handler {
	mux := http.NewServeMux()

	//Selenium Hub mock
	mux.HandleFunc("/wd/hub", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))

	//Docker API mock
	mux.HandleFunc("/v1.29/containers/create", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			output := `{"id": "e90e34656806", "warnings": []}`
			_, _ = w.Write([]byte(output))
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/start", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/kill", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/logs", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Content-Type", "text/plain; charset=utf-8")
			w.Header().Add("Transfer-Encoding", "chunked")
			w.WriteHeader(http.StatusOK)
			const streamTypeStderr = 2
			header := []byte{streamTypeStderr, 0, 0, 0, 0, 0, 0, 9}
			_, _ = w.Write(header)
			data := []byte("test-data")
			_, _ = w.Write(data)
		},
	))
	mux.HandleFunc("/v%s/containers/e90e34656806", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/json", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			p := port(mockServer.URL)
			output := fmt.Sprintf(`			
			{
			  "Id": "e90e34656806",
			  "Created": "2015-01-06T15:47:31.485331387Z",
			  "Driver": "aufs",
			  "HostConfig": {},
			  "NetworkSettings": {
			    "Ports": {
					"4444/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "%s"
						}
					],
					"7070/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "%s"
						}
					],
					"8080/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "%s"
						}
					],
					"9090/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "%s"
						}
					],
					"5900/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "5900"
						}
					],
					"%s/tcp": [
						{
						"HostIp": "0.0.0.0",
						"HostPort": "%s"
						}
					]
			    },
				"Networks": {
					"bridge": {
						"IPAMConfig": null,
						"Links": null,
						"Aliases": null,
						"NetworkID": "0152391a00ed79360bcf69401f7e2659acfab9553615726dbbcfc08b4f367b25",
						"EndpointID": "6a36b6f58b37490666329fd0fd74b21aa4eba939dd1ce466bdb6e0f826d56f98",
						"Gateway": "127.0.0.1",
						"IPAddress": "127.0.0.1",
						"IPPrefixLen": 16,
						"IPv6Gateway": "",
						"GlobalIPv6Address": "",
						"GlobalIPv6PrefixLen": 0,
						"MacAddress": "02:42:ac:11:00:02"
					}
				}			
			  },
			  "State": {},
			  "Mounts": []
			}
			`, p, p, p, p, p, p)
			_, _ = w.Write([]byte(output))
		},
	))
	mux.HandleFunc("/v1.29/networks/net-1/connect", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))
	return mux
}

type dockerInspectOptions struct {
	servicePort     string
	serviceHostPort string
	healthStatuses  []string
	deleteRequests  *int
}

func dockerReadinessMux(opts dockerInspectOptions) http.Handler {
	mux := http.NewServeMux()
	inspectCalls := 0

	mux.HandleFunc("/wd/hub", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))
	mux.HandleFunc("/v1.29/containers/create", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			output := `{"id": "e90e34656806", "warnings": []}`
			_, _ = w.Write([]byte(output))
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/start", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/kill", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/logs", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Content-Type", "text/plain; charset=utf-8")
			w.Header().Add("Transfer-Encoding", "chunked")
			w.WriteHeader(http.StatusOK)
			const streamTypeStderr = 2
			header := []byte{streamTypeStderr, 0, 0, 0, 0, 0, 0, 9}
			_, _ = w.Write(header)
			_, _ = w.Write([]byte("test-data"))
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if opts.deleteRequests != nil {
				(*opts.deleteRequests)++
			}
			w.WriteHeader(http.StatusNoContent)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/json", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			status := ""
			if len(opts.healthStatuses) > 0 {
				index := inspectCalls
				if index >= len(opts.healthStatuses) {
					index = len(opts.healthStatuses) - 1
				}
				status = opts.healthStatuses[index]
			}
			inspectCalls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(dockerInspectResponse(opts.servicePort, opts.serviceHostPort, status)))
		},
	))
	mux.HandleFunc("/v1.29/networks/net-1/connect", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	))

	return mux
}

func dockerInspectResponse(servicePort string, serviceHostPort string, healthStatus string) string {
	state := `"State": {}`
	if healthStatus != "" {
		state = fmt.Sprintf(`"State": {"Health": {"Status": %q}}`, healthStatus)
	}

	return fmt.Sprintf(`{
		"Id": "e90e34656806",
		"Created": "2015-01-06T15:47:31.485331387Z",
		"Driver": "aufs",
		"Config": {
			"Hostname": "browser-container"
		},
		"HostConfig": {},
		"NetworkSettings": {
			"Ports": {
				"7070/tcp": [
					{
						"HostIp": "0.0.0.0",
						"HostPort": "7070"
					}
				],
				"8080/tcp": [
					{
						"HostIp": "0.0.0.0",
						"HostPort": "8080"
					}
				],
				"9090/tcp": [
					{
						"HostIp": "0.0.0.0",
						"HostPort": "9090"
					}
				],
				"5900/tcp": [
					{
						"HostIp": "0.0.0.0",
						"HostPort": "5900"
					}
				],
				"%s/tcp": [
					{
						"HostIp": "0.0.0.0",
						"HostPort": %q
					}
				]
			},
			"Networks": {
				"bridge": {
					"IPAMConfig": null,
					"Links": null,
					"Aliases": null,
					"NetworkID": "0152391a00ed79360bcf69401f7e2659acfab9553615726dbbcfc08b4f367b25",
					"EndpointID": "6a36b6f58b37490666329fd0fd74b21aa4eba939dd1ce466bdb6e0f826d56f98",
					"Gateway": "127.0.0.1",
					"IPAddress": "127.0.0.1",
					"IPPrefixLen": 16,
					"IPv6Gateway": "",
					"GlobalIPv6Address": "",
					"GlobalIPv6PrefixLen": 0,
					"MacAddress": "02:42:ac:11:00:02"
				}
			}
		},
		%s,
		"Mounts": []
	}`, servicePort, serviceHostPort, state)
}

func parseUrl(input string) *url.URL {
	u, err := url.Parse(input)
	if err != nil {
		panic(err)
	}
	return u
}

func hostPort(input string) string {
	return parseUrl(input).Host
}

func port(input string) string {
	return parseUrl(input).Port()
}

func testConfig(env *service.Environment) *config.Config {
	cfg := config.NewConfig()
	p := "4444"
	if env.InDocker {
		p = port(mockServer.URL)
	}
	cfg.Browsers["firefox"] = config.Versions{
		Default: "33.0",
		Versions: map[string]*config.Browser{
			"33.0": {
				Image:   "selenwright/firefox:33.0",
				Tmpfs:   map[string]string{"/tmp": "size=128m"},
				Port:    p,
				Volumes: []string{"/test:/test"},
				Labels:  map[string]string{"key": "value"},
				Sysctl:  map[string]string{"sysctl net.ipv4.tcp_timestamps": "2"},
				Mem:     "512m",
				Cpu:     "1.0",
			},
		},
	}
	cfg.Browsers["internet explorer"] = config.Versions{
		Default: "11",
		Versions: map[string]*config.Browser{
			"11": {
				Image: []interface{}{
					"/usr/bin/test-command", "-arg",
				},
			},
		},
	}
	return cfg
}

func testEnvironment() *service.Environment {
	app.logOutputDir, _ = os.MkdirTemp("", "selenwright-test")
	return &service.Environment{
		CPU:                 int64(0),
		Memory:              int64(0),
		Network:             app.containerNetwork,
		StartupTimeout:      app.serviceStartupTimeout,
		CaptureDriverLogs:   app.captureDriverLogs,
		VideoContainerImage: "selenwright-video-recorder:latest",
		VideoOutputDir:      "/some/dir",
		LogOutputDir:        app.logOutputDir,
		Privileged:          false,
	}
}

func TestFindOutsideOfDocker(t *testing.T) {
	env := testEnvironment()
	env.InDocker = false
	testDocker(t, env, testConfig(env))
}

func TestFindInsideOfDocker(t *testing.T) {
	env := testEnvironment()
	env.InDocker = true
	cfg := testConfig(env)
	logConfig := make(map[string]string)
	cfg.ContainerLogs = &container.LogConfig{
		Type:   "rsyslog",
		Config: logConfig,
	}
	testDocker(t, env, cfg)
}

func TestFindDockerIPSpecified(t *testing.T) {
	env := testEnvironment()
	env.IP = "127.0.0.1"
	testDocker(t, env, testConfig(env))
}

func testDocker(t *testing.T, env *service.Environment, cfg *config.Config) {
	starter := createDockerStarter(t, env, cfg)
	startedService, err := starter.StartWithCancel()
	assert.NoError(t, err)
	assert.NotNil(t, startedService.Url)
	assert.NotNil(t, startedService.Container)
	assert.Equal(t, startedService.Container.ID, "e90e34656806")
	assert.Equal(t, startedService.HostPort.VNC, "127.0.0.1:5900")
	assert.NotNil(t, startedService.Cancel)
	startedService.Cancel()
}

func createDockerStarter(t *testing.T, env *service.Environment, cfg *config.Config) service.Starter {
	dockerCli, err := client.NewClientWithOpts(client.FromEnv)
	assert.NoError(t, err)
	mgr := service.DefaultManager{Environment: env, Client: dockerCli, Config: cfg}
	caps := session.Caps{
		DeviceName:            "firefox",
		Version:               "33.0",
		ScreenResolution:      "1024x768",
		Skin:                  "WXGA800",
		VNC:                   true,
		Video:                 true,
		VideoScreenSize:       "1024x768",
		VideoFrameRate:        25,
		VideoCodec:            "libx264",
		Log:                   true,
		LogName:               "testfile",
		Env:                   []string{"LANG=ru_RU.UTF-8", "LANGUAGE=ru:en"},
		HostsEntries:          []string{"example.com:192.168.0.1", "test.com:192.168.0.2"},
		DNSServers:            []string{"192.168.0.1", "192.168.0.2"},
		Labels:                map[string]string{"label1": "some-value", "label2": ""},
		ApplicationContainers: []string{"one", "two"},
		AdditionalNetworks:    []string{"net-1"},
		TimeZone:              "Europe/Moscow",
		ContainerHostname:     "some-hostname",
		TestName:              "my-cool-test",
	}
	starter, success := mgr.Find(caps, 42)
	assert.True(t, success)
	assert.NotNil(t, starter)
	return starter
}

func failingMux(numDeleteRequests *int) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.29/containers/create", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			output := `{"id": "e90e34656806", "warnings": []}`
			_, _ = w.Write([]byte(output))
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806/start", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	))
	mux.HandleFunc("/v1.29/containers/e90e34656806", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			*numDeleteRequests++
			w.WriteHeader(http.StatusNoContent)
		},
	))
	return mux
}

func TestDeleteContainerOnStartupError(t *testing.T) {
	numDeleteRequests := 0
	updateMux(failingMux(&numDeleteRequests))
	defer updateMux(testMux())
	env := testEnvironment()
	starter := createDockerStarter(t, env, testConfig(env))
	_, err := starter.StartWithCancel()
	assert.Error(t, err)
	assert.Equal(t, numDeleteRequests, 1)
}

func TestDockerHealthcheckSuccess(t *testing.T) {
	updateMux(dockerReadinessMux(dockerInspectOptions{
		servicePort:     "4444",
		serviceHostPort: "4444",
		healthStatuses:  []string{"healthy"},
	}))
	defer updateMux(testMux())

	env := testEnvironment()
	starter := createDockerStarter(t, env, testConfig(env))
	startedService, err := starter.StartWithCancel()
	assert.NoError(t, err)
	assert.NotNil(t, startedService)
	assert.NotNil(t, startedService.Url)
	assert.NotNil(t, startedService.Cancel)

	startedService.Cancel()
}

func TestDockerHealthcheckUnhealthy(t *testing.T) {
	numDeleteRequests := 0
	updateMux(dockerReadinessMux(dockerInspectOptions{
		servicePort:     "4444",
		serviceHostPort: "4444",
		healthStatuses:  []string{"unhealthy"},
		deleteRequests:  &numDeleteRequests,
	}))
	defer updateMux(testMux())

	env := testEnvironment()
	starter := createDockerStarter(t, env, testConfig(env))
	_, err := starter.StartWithCancel()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "became unhealthy")
	assert.GreaterOrEqual(t, numDeleteRequests, 1)
}

func TestPlaywrightUsesTCPReadinessFallbackWithoutHealthcheck(t *testing.T) {
	listener := testTCPServer("playwright-ready")
	defer listener.Close()

	servicePort := "3200"
	updateMux(dockerReadinessMux(dockerInspectOptions{
		servicePort:     servicePort,
		serviceHostPort: portFromListener(t, listener),
	}))
	defer updateMux(testMux())

	env := testEnvironment()
	cfg := testConfig(env)
	browser := cfg.Browsers["firefox"].Versions["33.0"]
	browser.Protocol = "playwright"
	browser.Port = servicePort
	browser.Path = ""

	starter := createDockerStarter(t, env, cfg)
	startedService, err := starter.StartWithCancel()
	assert.NoError(t, err)
	assert.NotNil(t, startedService)
	assert.Nil(t, startedService.Url)
	assert.NotNil(t, startedService.PlaywrightURL)
	assert.Equal(t, "ws", startedService.PlaywrightURL.Scheme)
	assert.Equal(t, net.JoinHostPort("127.0.0.1", portFromListener(t, listener)), startedService.PlaywrightURL.Host)
	assert.Equal(t, "/", startedService.PlaywrightURL.Path)

	startedService.Cancel()
}

func TestFindDriver(t *testing.T) {
	env := testEnvironment()
	mgr := service.DefaultManager{Environment: env, Config: testConfig(env)}
	caps := session.Caps{
		Name:             "internet explorer", //Using default version
		ScreenResolution: "1024x768",
		VNC:              true,
	}
	starter, success := mgr.Find(caps, 42)
	assert.True(t, success)
	assert.NotNil(t, starter)
}

func TestGetVNC(t *testing.T) {

	srv := httptest.NewServer(handler())
	defer srv.Close()

	testTcpServer := testTCPServer("test-data")
	app.sessions.Put("test-session", &session.Session{
		Quota: "unknown",
		HostPort: session.HostPort{
			VNC: testTcpServer.Addr().String(),
		},
	})
	defer app.sessions.Remove("test-session")

	u := fmt.Sprintf("ws://%s/vnc/test-session", hostPort(srv.URL))
	assert.Equal(t, readDataFromWebSocket(t, u), "test-data")
}

func testTCPServer(data string) net.Listener {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				continue
			}
			_, _ = io.WriteString(conn, data)
			_ = conn.Close()
			return
		}
	}()
	return l
}

func portFromListener(t *testing.T, listener net.Listener) string {
	t.Helper()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	assert.NoError(t, err)
	return port
}

func readDataFromWebSocket(t *testing.T, wsURL string) string {
	ws, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	assert.NoError(t, err)
	defer ws.Close()

	_, msg, _ := ws.ReadMessage()
	msg = bytes.Trim(msg, "\x00")
	return string(msg)
}

func TestGetLogs(t *testing.T) {

	srv := httptest.NewServer(handler())
	defer srv.Close()

	app.sessions.Put("test-session", &session.Session{
		Quota: "unknown",
		Container: &session.Container{
			ID:        "e90e34656806",
			IPAddress: "127.0.0.1",
		},
	})
	defer app.sessions.Remove("test-session")

	u := fmt.Sprintf("ws://%s/logs/test-session", hostPort(srv.URL))
	assert.Equal(t, readDataFromWebSocket(t, u), "test-data")
}
