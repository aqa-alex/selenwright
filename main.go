// Modified by [Aleksander R], 2026: added Playwright protocol support

package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/internal/metrics"
	"github.com/aqa-alex/selenwright/internal/safepath"
	"github.com/aqa-alex/selenwright/internal/slogx"
	"github.com/docker/docker/api"

	ggr "github.com/aerokube/ggr/config"
	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/event"
	"github.com/aqa-alex/selenwright/jsonerror"
	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	"github.com/aqa-alex/selenwright/upload"
	"github.com/docker/docker/client"
)

var (
	version     bool
	gitRevision = "HEAD"
	buildStamp  = "unknown"
)

// HTTP server hardening defaults.
//
// ReadHeaderTimeout caps how long a client can take to send the request line
// and headers — the primary defense against Slowloris-style attacks. ReadTimeout
// bounds the entire request body read for non-streaming endpoints. IdleTimeout
// closes idle keep-alive connections to free file descriptors. WriteTimeout is
// deliberately omitted on the server (left at 0) because long-lived WebSocket
// tunnels (Playwright, DevTools, VNC, log streams) and the WebDriver reverse
// proxy must outlive any per-request write deadline; per-handler timeouts are
// applied where appropriate.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 60 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10 // 64 KiB
)

func init() {
	var mem service.MemLimit
	var cpu service.CpuLimit
	flag.BoolVar(&app.disableDocker, "disable-docker", false, "Disable docker support")
	flag.BoolVar(&app.disableQueue, "disable-queue", false, "Disable wait queue")
	flag.BoolVar(&app.enableFileUpload, "enable-file-upload", false, "File upload support")
	flag.StringVar(&app.listen, "listen", ":4444", "Network address to accept connections")
	flag.StringVar(&app.confPath, "conf", "config/browsers.json", "Browsers configuration file")
	flag.StringVar(&app.logConfPath, "log-conf", "", "Container logging configuration file")
	flag.IntVar(&app.limit, "limit", 5, "Simultaneous container runs")
	flag.IntVar(&app.retryCount, "retry-count", 1, "New session attempts retry count")
	flag.DurationVar(&app.timeout, "timeout", 60*time.Second, "Session idle timeout in time.Duration format")
	flag.DurationVar(&app.maxTimeout, "max-timeout", 1*time.Hour, "Maximum valid session idle timeout in time.Duration format")
	flag.DurationVar(&app.newSessionAttemptTimeout, "session-attempt-timeout", 30*time.Second, "New session attempt timeout in time.Duration format")
	flag.DurationVar(&app.sessionDeleteTimeout, "session-delete-timeout", 30*time.Second, "Session delete timeout in time.Duration format")
	flag.DurationVar(&app.serviceStartupTimeout, "service-startup-timeout", 30*time.Second, "Service startup timeout in time.Duration format")
	flag.BoolVar(&version, "version", false, "Show version and exit")
	flag.Var(&mem, "mem", "Containers memory limit e.g. 128m or 1g")
	flag.Var(&cpu, "cpu", "Containers cpu limit as float e.g. 0.2 or 1.0")
	flag.StringVar(&app.containerNetwork, "container-network", service.DefaultContainerNetwork, "Network to be used for containers")
	flag.BoolVar(&app.captureDriverLogs, "capture-driver-logs", false, "Whether to add driver process logs to Selenwright output")
	flag.BoolVar(&app.privilegedContainers, "privileged", false, "Run browser containers in privileged mode. Default false — opposite of legacy upstream Selenoid which defaulted to true. Enable only when the browser needs host-level capabilities and the deployment isolates tenants some other way")
	flag.BoolVar(&app.capAddSysAdmin, "cap-add-sys-admin", false, "Add the SYS_ADMIN Linux capability to browser containers (without full -privileged). Chrome's user-namespace sandbox requires it; most headless workloads do not")
	flag.StringVar(&app.videoOutputDir, "video-output-dir", "video", "Directory to save recorded video to")
	flag.StringVar(&app.videoRecorderImage, "video-recorder-image", "selenwright/video-recorder:latest-release", "Image to use as video recorder")
	flag.StringVar(&app.logOutputDir, "log-output-dir", "", "Directory to save session log to")
	flag.BoolVar(&app.saveAllLogs, "save-all-logs", false, "Whether to save all logs without considering capabilities")
	flag.DurationVar(&app.gracefulPeriod, "graceful-period", 300*time.Second, "graceful shutdown period in time.Duration format, e.g. 300s or 500ms")
	flag.Int64Var(&app.maxCreateBodyBytes, "max-create-body-bytes", 4<<20, "Maximum POST body size for /session create requests in bytes (default 4 MiB)")
	flag.Int64Var(&app.maxUploadBodyBytes, "max-upload-body-bytes", 256<<20, "Maximum POST body size for /file upload requests in bytes (default 256 MiB)")
	flag.Int64Var(&app.maxUploadExtractedBytes, "max-upload-extracted-bytes", 1<<30, "Maximum total extracted size for /file uploaded zip archives in bytes (default 1 GiB)")
	flag.Int64Var(&app.maxWSMessageBytes, "max-ws-message-bytes", 64<<20, "Maximum single WebSocket message size in bytes for Playwright, DevTools, VNC and log streams. gorilla/websocket materializes each frame in memory before returning it to the handler, so without this limit a single multi-gigabyte frame can OOM the process. 0 disables the limit (legacy behavior). Default 64 MiB is ample for CDP screenshots and Playwright traces while capping the blast radius of a hostile peer.")
	flag.StringVar(&app.allowedOriginsRaw, "allowed-origins", "", "Comma-separated list of allowed Origin values for WebSocket upgrades (devtools, playwright, vnc, logs). Empty (default) keeps the legacy permissive behavior; '*' is explicit allow-all. Recommended: configure to your CI/QA hosts to defend against Cross-Site WebSocket Hijacking")
	flag.StringVar(&app.authModeFlag, "auth-mode", string(protect.ModeEmbedded), "Authentication mode: 'embedded' (built-in BasicAuth + htpasswd), 'trusted-proxy' (read pre-validated user from -user-header), 'none' (no auth — only allowed when -listen is bound to loopback unless -allow-insecure-none is set)")
	flag.StringVar(&app.htpasswdPath, "htpasswd", "", "Path to bcrypt-format htpasswd file used by -auth-mode=embedded. Generate with `htpasswd -B users.htpasswd alice` (apache2-utils) or `docker run --rm httpd:alpine htpasswd -nbB alice pass`")
	flag.StringVar(&app.userHeaderFlag, "user-header", "X-Forwarded-User", "Header to read for authenticated user identity in -auth-mode=trusted-proxy")
	flag.StringVar(&app.adminHeaderFlag, "admin-header", "X-Admin", "Header in -auth-mode=trusted-proxy whose value 'true' marks the request as administrative")
	flag.StringVar(&app.adminUsersRaw, "admin-users", "", "Comma-separated list of usernames treated as admin in -auth-mode=embedded")
	flag.BoolVar(&app.allowInsecureNone, "allow-insecure-none", false, "Permit -auth-mode=none on a non-loopback listen address. Required acknowledgement that the service is reachable without authentication")
	flag.StringVar(&app.trustedProxySecretRaw, "trusted-proxy-secret", "", "Shared secret expected in X-Router-Secret header. When set, every request must present this value or it is rejected with 401 — defends -auth-mode=trusted-proxy from clients that bypass the router")
	flag.StringVar(&app.trustedProxyCIDRsRaw, "trusted-proxy-cidr", "", "Comma-separated CIDR allow-list for the source IP. When set, request must originate from one of the listed networks regardless of headers")
	flag.StringVar(&app.trustedProxyMTLSCAPath, "trusted-proxy-mtls-ca", "", "Path to PEM bundle of CAs that issued the trusted client certificate. When set, the request must present a verified mTLS client certificate")
	flag.StringVar(&app.capsPolicyFlag, "caps-policy", string(session.PolicyStrict), "Capability policy: 'strict' rejects dangerous caps (env, dnsServers, hostsEntries, additionalNetworks, applicationContainers) for non-admin callers; 'permissive' preserves the legacy upstream-Selenoid behavior")
	flag.IntVar(&app.eventWorkers, "event-workers", 16, "Number of worker goroutines that dispatch session-lifecycle events (FileCreated, SessionStopped) to registered listeners. Bounds fan-out so a single slow listener (e.g. a hung S3 upload) cannot leak goroutines")
	flag.BoolVar(&app.enableMetrics, "enable-metrics", false, "Expose a Prometheus-compatible /metrics endpoint (queue depth, session counts, session duration histogram, auth/caps rejection counters). Path is controlled by -metrics-path")
	flag.StringVar(&app.metricsPath, "metrics-path", "/metrics", "Path the Prometheus metrics endpoint is served on when -enable-metrics is set. Access is not gated by the configured authenticator; the endpoint is expected to live behind the same network boundary as Prometheus itself")
	flag.BoolVar(&app.logJSON, "log-json", false, "Emit logs as one JSON object per line (event, request_id, fields, level, time). Default is the legacy bracketed text format. Parses existing log lines structurally so no call-site changes are required")
	flag.StringVar(&app.browserNetwork, "browser-network", "selenwright-browsers", "Dedicated Docker network that browser containers attach to as their primary network. Created on startup with Internal=true (no external gateway) to limit the blast radius of a sandbox escape. Set empty to disable isolation and revert to -container-network as the primary attachment")
	flag.Parse()

	slogx.Install(slogx.Config{JSON: app.logJSON})

	if version {
		showVersion()
		os.Exit(0)
	}

	var err error
	app.originChecker, err = protect.NewOriginChecker(splitCSV(app.allowedOriginsRaw))
	if err != nil {
		log.Fatalf("[-] [INIT] [Invalid -allowed-origins: %v]", err)
	}
	if app.originChecker.AllowsAll() {
		log.Printf("[-] [INIT] [WARN] [WebSocket Origin check is permissive — set -allowed-origins to defend against Cross-Site WebSocket Hijacking]")
	}
	app.authenticator, app.htpasswdAuth, err = buildAuthenticator(app.authModeFlag, app.htpasswdPath, splitCSV(app.adminUsersRaw), app.userHeaderFlag, app.adminHeaderFlag, app.listen, app.allowInsecureNone)
	if err != nil {
		if testing.Testing() {
			app.authenticator = protect.NoneAuthenticator{}
		} else {
			log.Fatalf("[-] [INIT] [%v]", err)
		}
	}
	stCfg, err := buildSourceTrustConfig(app.authModeFlag, app.trustedProxySecretRaw, app.trustedProxyCIDRsRaw, app.trustedProxyMTLSCAPath, app.userHeaderFlag, app.adminHeaderFlag)
	if err != nil {
		if testing.Testing() {
			stCfg = protect.SourceTrustConfig{}
		} else {
			log.Fatalf("[-] [INIT] [%v]", err)
		}
	}
	app.sourceTrust = protect.NewSourceTrust(stCfg)
	app.hostname, err = os.Hostname()
	if err != nil {
		log.Fatalf("[-] [INIT] [%s: %v]", os.Args[0], err)
	}
	if ggrHostEnv := os.Getenv("GGR_HOST"); ggrHostEnv != "" {
		app.ggrHost = parseGgrHost(ggrHostEnv)
	}
	app.queue = protect.New(app.limit, app.disableQueue)
	app.conf = config.NewConfig()
	err = app.conf.Load(app.confPath, app.logConfPath)
	if err != nil {
		log.Fatalf("[-] [INIT] [%s: %v]", os.Args[0], err)
	}
	onSIGHUP(func() {
		err := app.conf.Load(app.confPath, app.logConfPath)
		if err != nil {
			log.Printf("[-] [INIT] [%s: %v]", os.Args[0], err)
		}
		if app.htpasswdAuth != nil {
			if err := app.htpasswdAuth.Reload(); err != nil {
				log.Printf("[-] [INIT] [htpasswd reload failed: %v]", err)
			} else {
				log.Printf("[-] [INIT] [htpasswd reloaded]")
			}
		}
		if app.sourceTrust != nil {
			cfg, err := buildSourceTrustConfig(app.authModeFlag, app.trustedProxySecretRaw, app.trustedProxyCIDRsRaw, app.trustedProxyMTLSCAPath, app.userHeaderFlag, app.adminHeaderFlag)
			if err != nil {
				log.Printf("[-] [INIT] [source-trust reload failed: %v]", err)
			} else {
				app.sourceTrust.Update(cfg)
				log.Printf("[-] [INIT] [source-trust reloaded]")
			}
		}
	})
	inDocker := false
	_, err = os.Stat("/.dockerenv")
	if err == nil {
		inDocker = true
	}

	if !app.disableDocker {
		app.videoOutputDir, err = filepath.Abs(app.videoOutputDir)
		if err != nil {
			log.Fatalf("[-] [INIT] [Invalid video output dir %s: %v]", app.videoOutputDir, err)
		}
		// 0o750 — the previous 0o644 omitted the execute bit and made the
		// directory unenterable for the owning process, which only worked
		// in production because it ran as root.
		err = os.MkdirAll(app.videoOutputDir, 0o750)
		if err != nil {
			log.Fatalf("[-] [INIT] [Failed to create video output dir %s: %v]", app.videoOutputDir, err)
		}
		log.Printf("[-] [INIT] [Video Dir: %s]", app.videoOutputDir)
	}
	if app.logOutputDir != "" {
		app.logOutputDir, err = filepath.Abs(app.logOutputDir)
		if err != nil {
			log.Fatalf("[-] [INIT] [Invalid log output dir %s: %v]", app.logOutputDir, err)
		}
		err = os.MkdirAll(app.logOutputDir, 0o750)
		if err != nil {
			log.Fatalf("[-] [INIT] [Failed to create log output dir %s: %v]", app.logOutputDir, err)
		}
		log.Printf("[-] [INIT] [Logs Dir: %s]", app.logOutputDir)
		if app.saveAllLogs {
			log.Printf("[-] [INIT] [Saving all logs]")
		}
	}

	upload.Init()
	event.StartPool(app.eventWorkers, 0)
	metrics.BindQueueGauges(app.queue.Used, app.queue.Pending, app.queue.Queued)
	metrics.BindSessionsGauge(app.sessions.Len)
	if app.enableMetrics {
		metrics.Enable()
		log.Printf("[-] [INIT] [Metrics enabled at %s]", app.metricsPath)
	}

	environment := service.Environment{
		InDocker:             inDocker,
		CPU:                  int64(cpu),
		Memory:               int64(mem),
		Network:              app.containerNetwork,
		BrowserNetwork:       app.browserNetwork,
		StartupTimeout:       app.serviceStartupTimeout,
		SessionDeleteTimeout: app.sessionDeleteTimeout,
		CaptureDriverLogs:    app.captureDriverLogs,
		VideoOutputDir:       app.videoOutputDir,
		VideoContainerImage:  app.videoRecorderImage,
		LogOutputDir:         app.logOutputDir,
		SaveAllLogs:          app.saveAllLogs,
		Privileged:           app.privilegedContainers,
		CapAddSysAdmin:       app.capAddSysAdmin,
	}
	if app.disableDocker {
		app.manager = &service.DefaultManager{Environment: &environment, Config: app.conf}
		if app.logOutputDir != "" && app.captureDriverLogs {
			log.Fatalf("[-] [INIT] [In drivers mode only one of -capture-driver-logs and -log-output-dir flags is allowed]")
		}
		return
	}
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = client.DefaultDockerHost
	}
	u, err := client.ParseHostURL(dockerHost)
	if err != nil {
		log.Fatalf("[-] [INIT] [%v]", err)
	}
	ip, _, _ := net.SplitHostPort(u.Host)
	environment.IP = ip
	app.cli, err = createCompatibleDockerClient(
		func(specifiedApiVersion string) {
			log.Printf("[-] [INIT] [Using Docker API version: %s]", specifiedApiVersion)
		},
		func(determinedApiVersion string) {
			log.Printf("[-] [INIT] [Your Docker API version is %s]", determinedApiVersion)
		},
		func(defaultApiVersion string) {
			log.Printf("[-] [INIT] [Did not manage to determine your Docker API version - using default version: %s]", defaultApiVersion)
		},
	)
	if err != nil {
		log.Fatalf("[-] [INIT] [New docker client: %v]", err)
	}
	if app.browserNetwork != "" {
		if err := service.EnsureBrowserNetwork(context.Background(), app.cli, app.browserNetwork); err != nil {
			if testing.Testing() {
				log.Printf("[-] [INIT] [Browser network %s unavailable in test: %v]", app.browserNetwork, err)
			} else {
				log.Fatalf("[-] [INIT] [Browser network %s: %v]", app.browserNetwork, err)
			}
		} else {
			log.Printf("[-] [INIT] [Browser network: %s (internal, no external gateway)]", app.browserNetwork)
		}
	}
	app.manager = &service.DefaultManager{Environment: &environment, Client: app.cli, Config: app.conf}
}

func createCompatibleDockerClient(onVersionSpecified, onVersionDetermined, onUsingDefaultVersion func(string)) (*client.Client, error) {
	const dockerApiVersion = "DOCKER_API_VERSION"
	dockerApiVersionEnv := os.Getenv(dockerApiVersion)
	if dockerApiVersionEnv != "" {
		onVersionSpecified(dockerApiVersionEnv)
	} else {
		maxMajorVersion, maxMinorVersion := parseVersion(api.DefaultVersion)
		minMajorVersion, minMinorVersion := parseVersion("1.24")
		for majorVersion := maxMajorVersion; majorVersion >= minMajorVersion; majorVersion-- {
			for minorVersion := maxMinorVersion; minorVersion >= minMinorVersion; minorVersion-- {
				apiVersion := fmt.Sprintf("%d.%d", majorVersion, minorVersion)
				_ = os.Setenv(dockerApiVersion, apiVersion)
				docker, err := client.NewClientWithOpts(client.FromEnv)
				if err != nil {
					return nil, err
				}
				if isDockerAPIVersionCorrect(docker) {
					onVersionDetermined(apiVersion)
					return docker, nil
				}
				_ = docker.Close()
			}
		}
		onUsingDefaultVersion(api.DefaultVersion)
	}
	return client.NewClientWithOpts(client.FromEnv)
}

func parseVersion(ver string) (int, int) {
	const point = "."
	pieces := strings.Split(ver, point)
	major, err := strconv.Atoi(pieces[0])
	if err != nil {
		return 0, 0
	}
	minor, err := strconv.Atoi(pieces[1])
	if err != nil {
		return 0, 0
	}
	return major, minor
}

func isDockerAPIVersionCorrect(docker *client.Client) bool {
	ctx := context.Background()
	apiInfo, err := docker.ServerVersion(ctx)
	if err != nil {
		return false
	}
	return apiInfo.APIVersion == docker.ClientVersion()
}

func parseGgrHost(s string) *ggr.Host {
	h, p, err := net.SplitHostPort(s)
	if err != nil {
		log.Fatalf("[-] [INIT] [Invalid Ggr host: %v]", err)
	}
	ggrPort, err := strconv.Atoi(p)
	if err != nil {
		log.Fatalf("[-] [INIT] [Invalid Ggr host: %v]", err)
	}
	host := &ggr.Host{
		Name: h,
		Port: ggrPort,
	}
	log.Printf("[-] [INIT] [Will prefix all session IDs with a hash-sum: %s]", host.Sum())
	return host
}

// splitCSV parses a comma-separated flag value into a list of trimmed,
// non-empty entries. Used for multi-value string flags (-allowed-origins).
// Returns nil when the result is empty so callers can treat "no entries"
// uniformly regardless of whether the input was "" or ",,,".
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// stripTrustHeaders removes router-trust headers (X-Router-Secret,
// X-Forwarded-User, X-Admin) before the request crosses the trust
// boundary into a browser container. The set is kept in sync with
// SourceTrust.StripHeaders configured in main.init via -user-header
// and -admin-header. Defends against credential / identity leakage
// to upstream containers (PR #6).
func stripTrustHeaders(r *http.Request) {
	if app.sourceTrust != nil {
		app.sourceTrust.StripFromRequest(r)
	}
}

// gateSessionOwner extracts the session ID from the URL path at the given
// fragment index, looks up the session, and forbids access when the
// authenticated identity is neither the session owner nor an admin.
// Unknown sessions pass through so the next handler can render its
// standard 404 (or proceed when the path doesn't address one).
func gateSessionOwner(idIndex int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := protect.ExtractSessionID(r.URL.Path, idIndex)
		if sid == "" {
			next.ServeHTTP(w, r)
			return
		}
		sess, ok := app.sessions.Get(sid)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		identity, _ := protect.IdentityFromContext(r.Context())
		if !protect.SessionOwnership(identity, sess.Quota) {
			protect.WriteForbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func buildAuthenticator(mode, htpasswd string, admins []string, userHeader, adminHeader, listenAddr string, allowInsecure bool) (protect.Authenticator, *protect.HtpasswdAuthenticator, error) {
	switch protect.AuthMode(mode) {
	case protect.ModeEmbedded:
		if htpasswd == "" {
			return nil, nil, fmt.Errorf("-auth-mode=embedded requires -htpasswd <path>")
		}
		auth, err := protect.NewHtpasswdAuthenticator(htpasswd, admins)
		if err != nil {
			return nil, nil, fmt.Errorf("loading htpasswd: %w", err)
		}
		log.Printf("[-] [INIT] [Auth: embedded BasicAuth from %s, %d admin(s)]", htpasswd, len(admins))
		return auth, auth, nil
	case protect.ModeTrustedProxy:
		if userHeader == "" {
			return nil, nil, fmt.Errorf("-auth-mode=trusted-proxy requires non-empty -user-header")
		}
		log.Printf("[-] [INIT] [Auth: trusted-proxy reading user from %q, admin from %q]", userHeader, adminHeader)
		return &protect.TrustedProxyAuthenticator{UserHeader: userHeader, AdminHeader: adminHeader}, nil, nil
	case protect.ModeNone:
		if !isLoopbackListen(listenAddr) && !allowInsecure {
			return nil, nil, fmt.Errorf("-auth-mode=none on non-loopback listen %q is refused; bind to 127.0.0.1 or set -allow-insecure-none to opt in", listenAddr)
		}
		if !isLoopbackListen(listenAddr) {
			log.Printf("[-] [INIT] [WARN] [Auth: NONE on %s — service is reachable without authentication]", listenAddr)
		} else {
			log.Printf("[-] [INIT] [Auth: none (loopback only)]")
		}
		return protect.NoneAuthenticator{}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown -auth-mode %q (expected embedded|trusted-proxy|none)", mode)
	}
}

func buildSourceTrustConfig(mode, secret, cidrCSV, caPath, userHeader, adminHeader string) (protect.SourceTrustConfig, error) {
	cfg := protect.SourceTrustConfig{
		Secret: strings.TrimSpace(secret),
	}
	cidrs, err := protect.ParseCIDRs(splitCSV(cidrCSV))
	if err != nil {
		return cfg, err
	}
	cfg.TrustedCIDRs = cidrs
	if caPath != "" {
		pool, err := loadCAPool(caPath)
		if err != nil {
			return cfg, fmt.Errorf("loading -trusted-proxy-mtls-ca: %w", err)
		}
		cfg.AllowedRootCAs = pool
		cfg.RequireMTLS = true
	}

	stripped := []string{protect.HeaderRouterSecret}
	if userHeader != "" {
		stripped = append(stripped, userHeader)
	}
	if adminHeader != "" {
		stripped = append(stripped, adminHeader)
	}
	cfg.StripHeaders = stripped

	if mode == string(protect.ModeTrustedProxy) && cfg.Secret == "" && len(cfg.TrustedCIDRs) == 0 && !cfg.RequireMTLS {
		log.Printf("[-] [INIT] [WARN] [auth-mode=trusted-proxy without -trusted-proxy-secret/-cidr/-mtls-ca: any client that can reach the listen address can spoof X-Forwarded-User]")
	}

	return cfg, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// onSIGHUP installs a reload handler invoked on every SIGHUP. The
// handler runs inside a recover so a panicking fn (e.g. htpasswd
// reload choking on a malformed file) takes down the reload cycle,
// not the whole process.
func onSIGHUP(fn func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGHUP)
	go func() {
		for range sig {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[-] [SIGHUP] [reload panicked: %v]", r)
					}
				}()
				fn()
			}()
		}
	}()
}

var seleniumPaths = struct {
	CreateSession, ProxySession string
}{
	CreateSession: "/session",
	ProxySession:  "/session/",
}

func selenium() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(seleniumPaths.CreateSession, post(app.queue.Try(app.queue.Check(app.queue.Protect(create)))))
	mux.Handle(seleniumPaths.ProxySession, gateSessionOwner(2, http.HandlerFunc(proxy)))
	mux.HandleFunc(paths.Status, status)
	mux.HandleFunc(paths.Welcome, welcome)
	return mux
}

func post(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func get(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Add("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Uptime         string `json:"uptime"`
		LastReloadTime string `json:"lastReloadTime"`
		NumRequests    uint64 `json:"numRequests"`
		Version        string `json:"version"`
	}{time.Since(app.startTime).String(), app.conf.LastReloadTime.Format(time.RFC3339), getSerial(), gitRevision})
}

func video(w http.ResponseWriter, r *http.Request) {
	requestId := serial()
	if r.Method == http.MethodDelete {
		deleteFileIfExists(requestId, w, r, app.videoOutputDir, paths.Video, "DELETED_VIDEO_FILE")
		return
	}
	user, remote := info.RequestInfo(r)
	if _, ok := r.URL.Query()[jsonParam]; ok {
		listFilesAsJson(requestId, w, app.videoOutputDir, "VIDEO_ERROR")
		return
	}
	log.Printf("[%d] [VIDEO_LISTING] [%s] [%s]", requestId, user, remote)
	fileServer := http.StripPrefix(paths.Video, http.FileServer(http.Dir(app.videoOutputDir)))
	fileServer.ServeHTTP(w, r)
}

func deleteFileIfExists(requestId uint64, w http.ResponseWriter, r *http.Request, dir string, prefix string, status string) {
	user, remote := info.RequestInfo(r)
	fileName := strings.TrimPrefix(r.URL.Path, prefix)
	// Resolve the URL-supplied filename against the output dir while
	// rejecting traversal attempts (e.g. DELETE /video/../../etc/passwd
	// would otherwise let any caller wipe arbitrary files in the
	// process's reach). Without auth in front of these endpoints — see
	// PR #5 — this gate is the only thing standing between the wire and
	// `os.Remove` on attacker-chosen paths.
	filePath, err := safepath.Join(dir, fileName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid file name %s", fileName), http.StatusBadRequest)
		log.Printf("[%d] [%s] [%s] [%s] [REJECTED_TRAVERSAL] [%s]", requestId, status, user, remote, fileName)
		return
	}
	_, err = os.Stat(filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Unknown file %s", filePath), http.StatusNotFound)
		return
	}
	err = os.Remove(filePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete file %s: %v", filePath, err), http.StatusInternalServerError)
		return
	}
	log.Printf("[%d] [%s] [%s] [%s] [%s]", requestId, status, user, remote, fileName)
}

var paths = struct {
	Video, VNC, Logs, Devtools, Playwright, Download, Clipboard, File, Ping, Status, Error, WdHub, Welcome string
}{
	Video:      "/video/",
	VNC:        "/vnc/",
	Logs:       "/logs/",
	Devtools:   "/devtools/",
	Playwright: "/playwright/",
	Download:   "/download/",
	Clipboard:  "/clipboard/",
	Status:     "/status",
	File:       "/file",
	Ping:       "/ping",
	Error:      "/error",
	WdHub:      "/wd/hub",
	Welcome:    "/",
}

var openPaths = []string{paths.Ping, paths.Status, paths.Error, paths.Welcome}

func handler() http.Handler {
	root := http.NewServeMux()
	root.HandleFunc(paths.WdHub+"/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		r.URL.Scheme = "http"
		r.URL.Host = (&request{r}).localaddr()
		r.URL.Path = strings.TrimPrefix(r.URL.Path, paths.WdHub)
		selenium().ServeHTTP(w, r)
	})
	root.HandleFunc(paths.Error, func(w http.ResponseWriter, r *http.Request) {
		jsonerror.InvalidSessionID(errors.New("session timed out or not found")).Encode(w)
	})
	root.HandleFunc(paths.Status, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app.conf.State(app.sessions, app.limit, app.queue.Queued(), app.queue.Pending()))
	})
	root.HandleFunc(paths.Ping, ping)
	root.Handle(paths.VNC, gateSessionOwner(2, http.HandlerFunc(vnc)))
	root.Handle(paths.Logs, gateSessionOwner(2, http.HandlerFunc(logs)))
	root.HandleFunc(paths.Video, video)
	root.Handle(paths.Download, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Fileserver }, "DOWNLOADING_FILE"))))
	root.Handle(paths.Clipboard, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Clipboard }, "CLIPBOARD"))))
	root.Handle(paths.Devtools, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Devtools }, "DEVTOOLS"))))
	root.HandleFunc(paths.Playwright, get(app.queue.Try(app.queue.Check(app.queue.Protect(playwright)))))
	if app.enableFileUpload {
		root.HandleFunc(paths.File, fileUpload)
	}
	root.HandleFunc(paths.Welcome, welcome)
	metricsOpen := openPaths
	if app.enableMetrics {
		root.Handle(app.metricsPath, metrics.Handler())
		metricsOpen = append([]string{app.metricsPath}, openPaths...)
	}
	authMw := protect.AuthMiddleware(
		func() protect.Authenticator { return app.authenticator },
		protect.AuthMiddlewareOptions{
			OpenPaths: metricsOpen,
			OnFailure: func() { metrics.AuthFailure(app.authModeFlag) },
		},
	)
	sourceTrustMw := protect.SourceTrustMiddlewareWithHooks(
		func() *protect.SourceTrust { return app.sourceTrust },
		metricsOpen,
		func() { metrics.AuthFailure("source-trust") },
	)
	return sourceTrustMw(authMw(root))
}

func showVersion() {
	fmt.Printf("Git Revision: %s\n", gitRevision)
	fmt.Printf("UTC Build Time: %s\n", buildStamp)
}

func main() {
	log.Printf("[-] [INIT] [Timezone: %s]", time.Local)
	log.Printf("[-] [INIT] [Listening on %s]", app.listen)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	server := &http.Server{
		Addr:              app.listen,
		Handler:           handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		// WriteTimeout intentionally left zero: long-lived WebSocket and
		// log-stream connections must outlive any per-request write deadline.
	}
	e := make(chan error)
	go func() {
		e <- server.ListenAndServe()
	}()
	select {
	case err := <-e:
		log.Fatalf("[-] [INIT] [Failed to start: %v]", err)
	case <-stop:
	}

	log.Printf("[-] [SHUTTING_DOWN] [%s]", app.gracefulPeriod)
	ctx, cancel := context.WithTimeout(context.Background(), app.gracefulPeriod)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("[-] [SHUTTING_DOWN] [Failed to shut down: %v]", err)
	}

	type activeSession struct {
		id      string
		session *session.Session
	}
	activeSessions := make([]activeSession, 0, app.sessions.Len())
	app.sessions.Each(func(k string, s *session.Session) {
		activeSessions = append(activeSessions, activeSession{id: k, session: s})
	})

	for _, activeSession := range activeSessions {
		if app.enableFileUpload {
			_ = os.RemoveAll(path.Join(os.TempDir(), activeSession.id))
		}
		if activeSession.session != nil && activeSession.session.Cancel != nil {
			activeSession.session.Cancel()
		}
	}

	if err := event.Shutdown(ctx); err != nil {
		log.Printf("[-] [SHUTTING_DOWN] [Event pool drain %v]", err)
	}

	if !app.disableDocker {
		err := app.cli.Close()
		if err != nil {
			log.Fatalf("[-] [SHUTTING_DOWN] [Error closing Docker client: %v]", err)
		}
	}
}
