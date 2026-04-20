// Modified by [Aleksander R], 2026: added Playwright protocol support; added /config and /history/settings endpoints for the operator UI; added label-based browser discovery; -auth-mode=none is permitted on any listen address (warning only; operator owns network-level protection); added -groups-file / -groups-header for team-based session ACL; wrapped /playwright/session/ DELETE in gateSessionOwner to close ownership-check gap; added bearer-token auth (Authorization: Bearer / ?token= on WS paths) with admin-managed /api/admin/tokens store under <state-dir>/auth/tokens.json, --no-auth alias for -auth-mode=none, and SELENWRIGHT_AUTH_TOKEN env-seed / dev-fallback initial token

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
	"time"

	"github.com/aqa-alex/selenwright/info"
	"github.com/aqa-alex/selenwright/internal/metrics"
	"github.com/aqa-alex/selenwright/internal/safepath"
	"github.com/aqa-alex/selenwright/internal/slogx"
	"github.com/docker/docker/api"

	ggr "github.com/aerokube/ggr/config"
	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/discovery"
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
	flag.BoolVar(&app.defaultEnableVNC, "default-enable-vnc", false, "Default value for the enableVNC capability when a client does not pass ?enableVNC=. Clients can still opt out per session with ?enableVNC=false")
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
	flag.StringVar(&app.videoRecorderImage, "video-recorder-image", "selenwright-video-recorder:latest", "Image to use as video recorder")
	flag.StringVar(&app.logOutputDir, "log-output-dir", "", "Directory to save session log to")
	flag.BoolVar(&app.saveAllLogs, "save-all-logs", false, "Whether to save all logs without considering capabilities")
	flag.DurationVar(&app.gracefulPeriod, "graceful-period", 300*time.Second, "graceful shutdown period in time.Duration format, e.g. 300s or 500ms")
	flag.Int64Var(&app.maxCreateBodyBytes, "max-create-body-bytes", 4<<20, "Maximum POST body size for /session create requests in bytes (default 4 MiB)")
	flag.Int64Var(&app.maxUploadBodyBytes, "max-upload-body-bytes", 256<<20, "Maximum POST body size for /file upload requests in bytes (default 256 MiB)")
	flag.Int64Var(&app.maxUploadExtractedBytes, "max-upload-extracted-bytes", 1<<30, "Maximum total extracted size for /file uploaded zip archives in bytes (default 1 GiB)")
	flag.Int64Var(&app.maxWSMessageBytes, "max-ws-message-bytes", 64<<20, "Maximum single WebSocket message size in bytes for Playwright, DevTools, VNC and log streams. gorilla/websocket materializes each frame in memory before returning it to the handler, so without this limit a single multi-gigabyte frame can OOM the process. 0 disables the limit (legacy behavior). Default 64 MiB is ample for CDP screenshots and Playwright traces while capping the blast radius of a hostile peer.")
	flag.StringVar(&app.allowedOriginsRaw, "allowed-origins", "", "Comma-separated list of allowed Origin values for WebSocket upgrades (devtools, playwright, vnc, logs). Empty (default) keeps the legacy permissive behavior; '*' is explicit allow-all. Recommended: configure to your CI/QA hosts to defend against Cross-Site WebSocket Hijacking")
	flag.StringVar(&app.authModeFlag, "auth-mode", string(protect.ModeEmbedded), "Authentication mode: 'embedded' (built-in BasicAuth + htpasswd), 'trusted-proxy' (read pre-validated user from -user-header), 'none' (no auth; allowed on any listen address — operator owns network-level protection)")
	flag.BoolVar(&app.noAuthFlag, "no-auth", false, "Alias for -auth-mode=none. Disables authentication on every request, including WebSocket upgrades. Allowed on any listen address (operator owns network-level protection)")
	flag.StringVar(&app.htpasswdPath, "htpasswd", "", "Path to bcrypt-format htpasswd file used by -auth-mode=embedded. Generate with `htpasswd -B users.htpasswd alice` (apache2-utils) or `docker run --rm httpd:alpine htpasswd -nbB alice pass`. When omitted and no htpasswd is supplied, selenwright boots in dev mode and prints a single admin bearer token to stdout (see SELENWRIGHT_AUTH_TOKEN env var to seed instead of generating)")
	flag.StringVar(&app.userHeaderFlag, "user-header", "X-Forwarded-User", "Header to read for authenticated user identity in -auth-mode=trusted-proxy")
	flag.StringVar(&app.adminHeaderFlag, "admin-header", "X-Admin", "Header in -auth-mode=trusted-proxy whose value 'true' marks the request as administrative")
	flag.StringVar(&app.adminUsersRaw, "admin-users", "", "Comma-separated list of usernames treated as admin in -auth-mode=embedded")
	flag.StringVar(&app.groupsFilePath, "groups-file", "", "Path to JSON file mapping team/group name to list of member usernames, e.g. {\"qa-payments\":[\"alice\",\"jenkins-bot\"]}. Members of the same group can manage each other's sessions (useful for CI service accounts). Hot-reloaded on SIGHUP. Used with -auth-mode=embedded")
	flag.StringVar(&app.groupsHeaderFlag, "groups-header", "X-Groups", "Header to read CSV group names from in -auth-mode=trusted-proxy (e.g. \"qa-payments,qa-growth\"). Empty disables reading groups from headers")
	flag.StringVar(&app.trustedProxySecretRaw, "trusted-proxy-secret", "", "Shared secret expected in X-Router-Secret header. When set, every request must present this value or it is rejected with 401 — defends -auth-mode=trusted-proxy from clients that bypass the router")
	flag.StringVar(&app.trustedProxyCIDRsRaw, "trusted-proxy-cidr", "", "Comma-separated CIDR allow-list for the source IP. When set, request must originate from one of the listed networks regardless of headers")
	flag.StringVar(&app.trustedProxyMTLSCAPath, "trusted-proxy-mtls-ca", "", "Path to PEM bundle of CAs that issued the trusted client certificate. When set, the request must present a verified mTLS client certificate")
	flag.StringVar(&app.sessionTTLRaw, "session-ttl", "24h", "Lifetime of UI login sessions. Parsed as Go duration (e.g. 1h, 24h, 7d). Sessions are in-memory and lost on restart")
	flag.StringVar(&app.capsPolicyFlag, "caps-policy", string(session.PolicyStrict), "Capability policy: 'strict' rejects dangerous caps (env, dnsServers, hostsEntries, additionalNetworks, applicationContainers) for non-admin callers; 'permissive' preserves the legacy upstream-Selenoid behavior")
	flag.IntVar(&app.eventWorkers, "event-workers", 16, "Number of worker goroutines that dispatch session-lifecycle events (FileCreated, SessionStopped) to registered listeners. Bounds fan-out so a single slow listener (e.g. a hung S3 upload) cannot leak goroutines")
	flag.BoolVar(&app.enableMetrics, "enable-metrics", false, "Expose a Prometheus-compatible /metrics endpoint (queue depth, session counts, session duration histogram, auth/caps rejection counters). Path is controlled by -metrics-path")
	flag.StringVar(&app.metricsPath, "metrics-path", "/metrics", "Path the Prometheus metrics endpoint is served on when -enable-metrics is set. Access is not gated by the configured authenticator; the endpoint is expected to live behind the same network boundary as Prometheus itself")
	flag.BoolVar(&app.logJSON, "log-json", false, "Emit logs as one JSON object per line (event, request_id, fields, level, time). Default is the legacy bracketed text format. Parses existing log lines structurally so no call-site changes are required")
	flag.StringVar(&app.artifactHistoryDir, "artifact-history-dir", "artifacts", "Directory to store artifact history manifests and persisted downloads. Must be writable and ideally on a volume mount so data survives container recreation")
	flag.StringVar(&app.artifactHistorySettingsPath, "artifact-history-settings", filepath.Join("state", "artifact-history.json"), "JSON file storing artifact history settings (enabled, retentionDays). Survives restarts when placed on a volume mount")
	flag.StringVar(&app.stateDir, "state-dir", "state", "Directory for persistent state (adopted browser set). Created on startup if missing")
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
	if app.noAuthFlag {
		if app.authModeFlag != "" && app.authModeFlag != string(protect.ModeEmbedded) && app.authModeFlag != string(protect.ModeNone) {
			log.Printf("[-] [INIT] [WARN] [--no-auth overrides -auth-mode=%s]", app.authModeFlag)
		}
		app.authModeFlag = string(protect.ModeNone)
	}
	// Dev-fallback: -auth-mode=embedded (the default) without -htpasswd would
	// normally fail the build step below. In that case we boot in token-only
	// mode — the TokenAwareAuthenticator that wraps this downstream carves out
	// the bearer path, so Playwright/Selenium clients can still authenticate
	// while nothing else can. The initial admin token is printed to stdout in
	// the token-store init block a few lines down.
	devTokenOnly := !testHooksEnabled &&
		app.authModeFlag == string(protect.ModeEmbedded) &&
		app.htpasswdPath == ""
	if devTokenOnly {
		log.Printf("[-] [INIT] [Auth: embedded without htpasswd — token-only bearer auth]")
		app.setAuthenticator(protect.TokenOnlyAuthenticator{})
	}
	if !devTokenOnly {
		authBuilt, err := buildAuthenticator(authBuildOptions{
			mode:         app.authModeFlag,
			htpasswd:     app.htpasswdPath,
			admins:       splitCSV(app.adminUsersRaw),
			userHeader:   app.userHeaderFlag,
			adminHeader:  app.adminHeaderFlag,
			groupsFile:   app.groupsFilePath,
			groupsHeader: app.groupsHeaderFlag,
			listenAddr:   app.listen,
		})
		if err != nil {
			if testHooksEnabled {
				app.setAuthenticator(protect.NoneAuthenticator{})
			} else {
				log.Fatalf("[-] [INIT] [%v]", err)
			}
		} else {
			app.setAuthenticator(authBuilt.authenticator)
			app.htpasswdAuth = authBuilt.htpasswdAuth
			app.groupsProvider = authBuilt.groups
		}
	}
	if app.htpasswdAuth != nil {
		sessionTTL, parseErr := time.ParseDuration(app.sessionTTLRaw)
		if parseErr != nil {
			log.Fatalf("[-] [INIT] [Invalid -session-ttl %q: %v]", app.sessionTTLRaw, parseErr)
		}
		app.sessionStore = protect.NewSessionStore(sessionTTL)
		app.setAuthenticator(&protect.SessionAwareAuthenticator{
			Sessions:   app.sessionStore,
			CookieName: protect.DefaultSessionCookieName,
			Fallback:   app.currentAuthenticator(),
		})
		log.Printf("[-] [INIT] [Session auth: cookie %q, TTL %s]", protect.DefaultSessionCookieName, sessionTTL)
	}
	if app.authModeFlag != string(protect.ModeNone) && !testHooksEnabled {
		tokenPath := filepath.Join(app.stateDir, "auth", "tokens.json")
		ts, terr := protect.NewTokenStore(tokenPath)
		if terr != nil {
			if testHooksEnabled {
				log.Printf("[-] [INIT] [Token store %s unavailable in test: %v]", tokenPath, terr)
			} else {
				log.Fatalf("[-] [INIT] [Token store %s: %v]", tokenPath, terr)
			}
		}
		if ts != nil {
			app.tokenStore = ts
			if envTok := strings.TrimSpace(os.Getenv("SELENWRIGHT_AUTH_TOKEN")); envTok != "" {
				if ts.Len() == 0 {
					id, _, serr := ts.SeedFromPlaintext("admin", "env-seed", envTok, nil)
					if serr != nil {
						log.Fatalf("[-] [INIT] [seed SELENWRIGHT_AUTH_TOKEN: %v]", serr)
					}
					log.Printf("[-] [INIT] [Seeded admin token %s from SELENWRIGHT_AUTH_TOKEN]", id)
				} else if _, _, _, ok := ts.Lookup(envTok); !ok {
					log.Printf("[-] [INIT] [WARN] [SELENWRIGHT_AUTH_TOKEN differs from stored tokens — ignoring env var; revoke and restart with empty state-dir to re-seed]")
				}
			} else if app.htpasswdPath == "" && ts.Len() == 0 {
				id, plaintext, cerr := ts.Create("admin", "initial", nil)
				if cerr != nil {
					log.Fatalf("[-] [INIT] [initial token: %v]", cerr)
				}
				logInitialTokenBox(id, plaintext, tokenPath)
			}
			admins := map[string]struct{}{}
			for _, a := range splitCSV(app.adminUsersRaw) {
				admins[a] = struct{}{}
			}
			// In dev-fallback (no htpasswd) the auto-generated token owner is
			// "admin"; make sure that identity is recognised as admin even when
			// the operator did not pass -admin-users.
			if app.htpasswdPath == "" {
				admins["admin"] = struct{}{}
			}
			app.setAuthenticator(&protect.TokenAwareAuthenticator{
				Store:            app.tokenStore,
				Admins:           admins,
				QueryAllowedPath: isTokenQueryAllowedPath,
				Fallback:         app.currentAuthenticator(),
			})
			log.Printf("[-] [INIT] [Token auth: store %s, %d token(s)]", tokenPath, ts.Len())
		}
	}
	stCfg, err := buildSourceTrustConfig(app.authModeFlag, app.trustedProxySecretRaw, app.trustedProxyCIDRsRaw, app.trustedProxyMTLSCAPath, app.userHeaderFlag, app.adminHeaderFlag, app.groupsHeaderFlag)
	if err != nil {
		if testHooksEnabled {
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
	// Browser catalog is loaded later: via label discovery when Docker is enabled,
	// or via JSON when Docker is disabled. See setupDocker() and the disableDocker branch.
	onSIGHUP(func() {
		app.rescanMu.Lock()
		defer app.rescanMu.Unlock()
		if !app.disableDocker && app.adoptedStore != nil {
			if err := discovery.AssembleCatalog(context.Background(), app.cli, app.adoptedStore, app.conf, app.confPath, app.logConfPath); err != nil {
				log.Printf("[-] [SIGHUP] [discovery rescan: %v]", err)
			} else {
				log.Printf("[-] [SIGHUP] [browser catalog reloaded via discovery]")
			}
		} else {
			if err := app.conf.Load(app.confPath, app.logConfPath); err != nil {
				log.Printf("[-] [INIT] [%s: %v]", os.Args[0], err)
			}
		}
		if app.htpasswdAuth != nil {
			if err := app.htpasswdAuth.Reload(); err != nil {
				log.Printf("[-] [INIT] [htpasswd reload failed: %v]", err)
			} else {
				log.Printf("[-] [INIT] [htpasswd reloaded]")
			}
		}
		if app.groupsProvider != nil {
			if err := app.groupsProvider.Reload(); err != nil {
				log.Printf("[-] [INIT] [groups reload failed: %v]", err)
			} else {
				log.Printf("[-] [INIT] [groups reloaded]")
			}
		}
		if app.sourceTrust != nil {
			cfg, err := buildSourceTrustConfig(app.authModeFlag, app.trustedProxySecretRaw, app.trustedProxyCIDRsRaw, app.trustedProxyMTLSCAPath, app.userHeaderFlag, app.adminHeaderFlag, app.groupsHeaderFlag)
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

	if app.artifactHistoryDir != "" {
		app.artifactHistoryDir, err = filepath.Abs(app.artifactHistoryDir)
		if err != nil {
			log.Fatalf("[-] [INIT] [Invalid artifact history dir %s: %v]", app.artifactHistoryDir, err)
		}
		log.Printf("[-] [INIT] [Artifact History Dir: %s]", app.artifactHistoryDir)
	}
	if app.artifactHistorySettingsPath != "" {
		app.artifactHistorySettingsPath, err = filepath.Abs(app.artifactHistorySettingsPath)
		if err != nil {
			log.Fatalf("[-] [INIT] [Invalid artifact history settings path %s: %v]", app.artifactHistorySettingsPath, err)
		}
		log.Printf("[-] [INIT] [Artifact History Settings: %s]", app.artifactHistorySettingsPath)
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
		if err := app.conf.Load(app.confPath, app.logConfPath); err != nil {
			log.Fatalf("[-] [INIT] [%s: %v]", os.Args[0], err)
		}
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
			if testHooksEnabled {
				log.Printf("[-] [INIT] [Browser network %s unavailable in test: %v]", app.browserNetwork, err)
			} else {
				log.Fatalf("[-] [INIT] [Browser network %s: %v]", app.browserNetwork, err)
			}
		} else {
			log.Printf("[-] [INIT] [Browser network: %s (internal, no external gateway)]", app.browserNetwork)
		}
	}
	app.adoptedStore, err = discovery.NewAdoptedStore(app.stateDir)
	if err != nil {
		log.Fatalf("[-] [INIT] [adopted store: %v]", err)
	}
	if err := discovery.AssembleCatalog(context.Background(), app.cli, app.adoptedStore, app.conf, app.confPath, app.logConfPath); err != nil {
		log.Fatalf("[-] [INIT] [browser catalog: %v]", err)
	}
	app.manager = &service.DefaultManager{Environment: &environment, Client: app.cli, Config: app.conf}
}

func createCompatibleDockerClient(onVersionSpecified, onVersionDetermined, onUsingDefaultVersion func(string)) (*client.Client, error) {
	// If the operator pinned DOCKER_API_VERSION explicitly, honor it verbatim —
	// don't negotiate, don't override.
	if pinned := os.Getenv("DOCKER_API_VERSION"); pinned != "" {
		onVersionSpecified(pinned)
		return client.NewClientWithOpts(client.FromEnv)
	}

	// Otherwise use the SDK's built-in negotiation: the client asks the daemon
	// for its API version on the first call and downgrades itself to match.
	// This is what every modern Docker Go tool does and it handles the full
	// range of daemons without a hand-rolled loop.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if info, pingErr := cli.Ping(ctx); pingErr == nil && info.APIVersion != "" {
		onVersionDetermined(info.APIVersion)
	} else {
		onUsingDefaultVersion(api.DefaultVersion)
	}
	return cli, nil
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

func stripTrustHeaders(r *http.Request) {
	if app.sourceTrust != nil {
		app.sourceTrust.StripFromRequest(r)
	}
}

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
		if !protect.SessionAccess(identity, sess.Quota, sess.OwnerGroups) {
			protect.WriteForbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type authBuildOptions struct {
	mode         string
	htpasswd     string
	admins       []string
	userHeader   string
	adminHeader  string
	groupsFile   string
	groupsHeader string
	listenAddr   string
}

type authBuildResult struct {
	authenticator protect.Authenticator
	htpasswdAuth  *protect.HtpasswdAuthenticator
	groups        *protect.FileGroupsProvider
}

func buildAuthenticator(opts authBuildOptions) (authBuildResult, error) {
	switch protect.AuthMode(opts.mode) {
	case protect.ModeEmbedded:
		if opts.htpasswd == "" {
			return authBuildResult{}, fmt.Errorf("-auth-mode=embedded requires -htpasswd <path>")
		}
		auth, err := protect.NewHtpasswdAuthenticator(opts.htpasswd, opts.admins)
		if err != nil {
			return authBuildResult{}, fmt.Errorf("loading htpasswd: %w", err)
		}
		var groups *protect.FileGroupsProvider
		if opts.groupsFile != "" {
			groups, err = protect.NewFileGroupsProvider(opts.groupsFile)
			if err != nil {
				return authBuildResult{}, fmt.Errorf("loading groups file: %w", err)
			}
			auth.SetGroups(groups)
			log.Printf("[-] [INIT] [Auth: embedded BasicAuth from %s, %d admin(s), groups from %s]", opts.htpasswd, len(opts.admins), opts.groupsFile)
		} else {
			log.Printf("[-] [INIT] [Auth: embedded BasicAuth from %s, %d admin(s)]", opts.htpasswd, len(opts.admins))
		}
		return authBuildResult{authenticator: auth, htpasswdAuth: auth, groups: groups}, nil
	case protect.ModeTrustedProxy:
		if opts.userHeader == "" {
			return authBuildResult{}, fmt.Errorf("-auth-mode=trusted-proxy requires non-empty -user-header")
		}
		if opts.groupsHeader != "" {
			log.Printf("[-] [INIT] [Auth: trusted-proxy reading user from %q, admin from %q, groups from %q]", opts.userHeader, opts.adminHeader, opts.groupsHeader)
		} else {
			log.Printf("[-] [INIT] [Auth: trusted-proxy reading user from %q, admin from %q]", opts.userHeader, opts.adminHeader)
		}
		return authBuildResult{
			authenticator: &protect.TrustedProxyAuthenticator{
				UserHeader:   opts.userHeader,
				AdminHeader:  opts.adminHeader,
				GroupsHeader: opts.groupsHeader,
			},
		}, nil
	case protect.ModeNone:
		if isLoopbackListen(opts.listenAddr) {
			log.Printf("[-] [INIT] [Auth: none on %s]", opts.listenAddr)
		} else {
			log.Printf("[-] [INIT] [WARN] [Auth: NONE on %s — service is reachable without authentication]", opts.listenAddr)
		}
		return authBuildResult{authenticator: protect.NoneAuthenticator{}}, nil
	default:
		return authBuildResult{}, fmt.Errorf("unknown -auth-mode %q (expected embedded|trusted-proxy|none)", opts.mode)
	}
}

func buildSourceTrustConfig(mode, secret, cidrCSV, caPath, userHeader, adminHeader, groupsHeader string) (protect.SourceTrustConfig, error) {
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
	if groupsHeader != "" {
		stripped = append(stripped, groupsHeader)
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

// isTokenQueryAllowedPath reports whether the given request path accepts a
// ?token=<plaintext> query parameter as a bearer fallback. Restricted to
// WebSocket-upgrade endpoints where header control is hard (e.g. wscat,
// <img src>, browser-devtools clients).
func isTokenQueryAllowedPath(p string) bool {
	switch {
	case strings.HasPrefix(p, paths.Playwright),
		strings.HasPrefix(p, paths.WdHub+"/"),
		strings.HasPrefix(p, paths.Devtools),
		strings.HasPrefix(p, paths.VNC),
		strings.HasPrefix(p, paths.Logs):
		return true
	}
	return false
}

// logInitialTokenBox prints a highly visible banner advertising the
// freshly-generated dev-fallback admin token. Intended for stdout inspection
// by the operator; the value is never written to file beyond the hashed
// record in tokens.json.
func logInitialTokenBox(id, plaintext, storePath string) {
	bar := strings.Repeat("─", 60)
	log.Printf("[-] [INIT] [No htpasswd configured — generated initial admin token]")
	log.Printf("[-] [INIT] ┌%s┐", bar)
	log.Printf("[-] [INIT] │  %s", plaintext)
	log.Printf("[-] [INIT] │  id=%s owner=admin name=initial", id)
	log.Printf("[-] [INIT] │  Save it — won't be shown again.")
	log.Printf("[-] [INIT] │  Hash stored at %s", storePath)
	log.Printf("[-] [INIT] └%s┘", bar)
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

func httpDelete(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
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
		items, err := ensureArtifactHistoryManager().ListVideos()
		if err != nil {
			log.Printf("[%d] [VIDEO_ERROR] [Failed to list directory %s: %v]", requestId, app.videoOutputDir, err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSONResponse(w, http.StatusOK, items)
		return
	}
	log.Printf("[%d] [VIDEO_LISTING] [%s] [%s]", requestId, user, remote)
	fileServer := http.StripPrefix(paths.Video, http.FileServer(http.Dir(app.videoOutputDir)))
	fileServer.ServeHTTP(w, r)
}

func deleteFileIfExists(requestId uint64, w http.ResponseWriter, r *http.Request, dir string, prefix string, status string) {
	user, remote := info.RequestInfo(r)
	fileName := strings.TrimPrefix(r.URL.Path, prefix)
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
	Video, VNC, Logs, Devtools, Playwright, Download, Downloads, Clipboard, File, Ping, Status, Error, WdHub, Welcome, Config, HistorySettings string
	DiscoveredBrowsers, AdoptBrowser, DismissBrowser, RescanBrowsers                                                                         string
	StackStatus, StackPull, StackRecreate                                                                                                    string
	Whoami, Login, Logout                                                                                                                    string
}{
	Video:              "/video/",
	VNC:                "/vnc/",
	Logs:               "/logs/",
	Devtools:           "/devtools/",
	Playwright:         "/playwright/",
	Download:           "/download/",
	Downloads:          "/downloads/",
	Clipboard:          "/clipboard/",
	Status:             "/status",
	File:               "/file",
	Ping:               "/ping",
	Error:              "/error",
	WdHub:              "/wd/hub",
	Welcome:            "/",
	Config:             "/config",
	HistorySettings:    "/history/settings",
	DiscoveredBrowsers: "/browsers/discovered",
	AdoptBrowser:       "/browsers/adopt",
	DismissBrowser:     "/browsers/dismiss",
	RescanBrowsers:     "/browsers/rescan",
	StackStatus:        "/stack/status",
	StackPull:          "/stack/pull",
	StackRecreate:      "/stack/recreate",
	Whoami:             "/whoami",
	Login:              "/login",
	Logout:             "/logout",
}

// openPaths bypass AuthMiddleware. Keep this list minimal: every entry here
// is reachable anonymously from any client that can connect to the listen
// address. Admitted endpoints are limited to liveness (/ping), the welcome
// banner (/), the generic Selenium error JSON (/error) and the auth-bootstrap
// triad required by the UI (/whoami, /login, /logout).
//
// Anything that exposes session IDs, container IPs, owner quotas, the browser
// catalog, retention settings or downloads listings is deliberately left OUT —
// an unauthenticated scrape of /status was an information-disclosure gap that
// also let the UI render a populated dashboard for logged-out users.
//
// Prometheus is unaffected: /metrics is gated by a separate `metricsOpen` list
// only when `-enable-metrics` is set.
var openPaths = []string{paths.Ping, paths.Error, paths.Welcome, paths.Whoami, paths.Login, paths.Logout}

func handler() http.Handler {
	ensureArtifactHistoryManager()
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
	root.HandleFunc(paths.Config, get(configHandler))
	root.HandleFunc(paths.HistorySettings, historySettingsHandler)
	root.HandleFunc(paths.HistorySettings+"/", historySettingsHandler)
	root.HandleFunc(paths.Downloads, persistedDownloadsHandler)
	root.Handle(paths.VNC, gateSessionOwner(2, http.HandlerFunc(vnc)))
	root.Handle(paths.Logs, gateSessionOwner(2, http.HandlerFunc(logs)))
	root.HandleFunc(paths.Video, video)
	root.Handle(paths.Download, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Fileserver }, "DOWNLOADING_FILE"))))
	root.Handle(paths.Clipboard, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Clipboard }, "CLIPBOARD"))))
	root.Handle(paths.Devtools, gateSessionOwner(2, http.HandlerFunc(reverseProxy(func(sess *session.Session) string { return sess.HostPort.Devtools }, "DEVTOOLS"))))
	root.Handle("/playwright/session/", gateSessionOwner(3, httpDelete(deletePlaywrightSession)))
	root.HandleFunc(paths.Playwright, get(app.queue.Try(app.queue.Check(app.queue.Protect(playwright)))))
	if app.enableFileUpload {
		root.HandleFunc(paths.File, fileUpload)
	}
	root.HandleFunc(paths.DiscoveredBrowsers, discoveredBrowsers)
	root.HandleFunc(paths.AdoptBrowser, adoptBrowser)
	root.HandleFunc(paths.DismissBrowser, dismissBrowser)
	root.HandleFunc(paths.RescanBrowsers, rescanBrowsers)
	root.HandleFunc(paths.StackStatus, get(stackStatusHandler))
	root.HandleFunc(paths.StackPull, post(stackPullHandler))
	root.HandleFunc(paths.StackRecreate, post(stackRecreateHandler))
	root.HandleFunc(paths.Whoami, get(whoamiHandler))
	root.HandleFunc(paths.Login, loginHandler)
	root.HandleFunc(paths.Logout, logoutHandler)
	root.HandleFunc(tokensAPIPath, tokensHandler)
	root.HandleFunc(tokensAPIPrefix, tokenByIDHandler)
	root.HandleFunc(usersAPIPath, tokenUsersHandler)
	root.HandleFunc(paths.Welcome, welcome)
	metricsOpen := openPaths
	if app.enableMetrics {
		root.Handle(app.metricsPath, metrics.Handler())
		metricsOpen = append([]string{app.metricsPath}, openPaths...)
	}
	authMw := protect.AuthMiddleware(
		app.currentAuthenticator,
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

	ensureArtifactHistoryManager().StartJanitor()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	server := &http.Server{
		Addr:              app.listen,
		Handler:           handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
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

	if app.tokenStore != nil {
		app.tokenStore.Stop()
	}

	if !app.disableDocker {
		err := app.cli.Close()
		if err != nil {
			log.Fatalf("[-] [SHUTTING_DOWN] [Error closing Docker client: %v]", err)
		}
	}
}
