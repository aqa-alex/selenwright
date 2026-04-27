package main

import (
	"sync"
	"sync/atomic"
	"time"

	ggr "github.com/aerokube/ggr/config"
	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/discovery"
	"github.com/aqa-alex/selenwright/discovery/registry"
	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
)

type Server struct {
	hostname                 string
	disableDocker            bool
	disableQueue             bool
	enableFileUpload         bool
	defaultEnableVNC         bool
	listen                   string
	timeout                  time.Duration
	maxTimeout               time.Duration
	newSessionAttemptTimeout time.Duration
	sessionDeleteTimeout     time.Duration
	serviceStartupTimeout    time.Duration
	gracefulPeriod           time.Duration
	limit                    int
	retryCount               int
	containerNetwork         string
	confPath                 string
	logConfPath              string
	captureDriverLogs        bool
	privilegedContainers     bool
	capAddSysAdmin           bool
	videoOutputDir           string
	videoRecorderImage       string
	logOutputDir             string
	saveAllLogs              bool
	maxCreateBodyBytes       int64
	maxUploadBodyBytes       int64
	maxUploadExtractedBytes  int64
	maxWSMessageBytes        int64
	allowedOriginsRaw        string
	authModeFlag             string
	noAuthFlag               bool
	htpasswdPath             string
	userHeaderFlag           string
	adminHeaderFlag          string
	adminUsersRaw            string
	groupsFilePath           string
	groupsHeaderFlag         string
	trustedProxySecretRaw    string
	trustedProxyCIDRsRaw     string
	trustedProxyMTLSCAPath   string
	capsPolicyFlag           string
	eventWorkers             int
	enableMetrics            bool
	metricsPath              string
	logJSON                  bool
	browserNetwork           string
	stateDir                 string

	artifactHistoryDir          string
	artifactHistorySettingsPath string

	sessions       *session.Map
	queue          *protect.Queue
	manager        service.Manager
	cli            *client.Client
	conf           *config.Config
	adoptedStore   *discovery.AdoptedStore
	rescanMu       sync.Mutex
	originChecker  *protect.OriginChecker
	registryClient *registry.Client
	// authenticatorPtr is swapped at startup and in tests; request handlers
	// read it concurrently, so access is synchronised via atomic.Pointer.
	// Use currentAuthenticator() / setAuthenticator() — do not touch directly.
	authenticatorPtr atomic.Pointer[protect.Authenticator]
	htpasswdAuth   *protect.HtpasswdAuthenticator
	groupsProvider *protect.FileGroupsProvider
	sessionStore   *protect.SessionStore
	tokenStore     *protect.TokenStore
	sourceTrust    *protect.SourceTrust
	sessionTTLRaw string
	ggrHost       *ggr.Host

	startTime time.Time
}

var app = &Server{
	sessions:       session.NewMap(),
	startTime:      time.Now(),
	registryClient: registry.NewClient(),
}

func (s *Server) currentAuthenticator() protect.Authenticator {
	if p := s.authenticatorPtr.Load(); p != nil {
		return *p
	}
	return nil
}

func (s *Server) setAuthenticator(a protect.Authenticator) {
	s.authenticatorPtr.Store(&a)
}

var testHooksEnabled bool
