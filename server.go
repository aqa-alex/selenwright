package main

import (
	"time"

	ggr "github.com/aerokube/ggr/config"
	"github.com/aqa-alex/selenwright/config"
	"github.com/aqa-alex/selenwright/protect"
	"github.com/aqa-alex/selenwright/service"
	"github.com/aqa-alex/selenwright/session"
	"github.com/docker/docker/client"
)

// Server bundles runtime state and flag-derived configuration so
// handlers can be written as methods with a single receiver instead of
// reading a zoo of package-level globals. One instance is constructed
// in main.init and accessed via the package-level app pointer; tests
// reassign its fields through the same pointer.
type Server struct {
	// Flag-derived config (bound during main.init; read-only after that
	// in production, mutable from tests to exercise specific paths).
	hostname                 string
	disableDocker            bool
	disableQueue             bool
	enableFileUpload         bool
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
	htpasswdPath             string
	userHeaderFlag           string
	adminHeaderFlag          string
	adminUsersRaw            string
	allowInsecureNone        bool
	trustedProxySecretRaw    string
	trustedProxyCIDRsRaw     string
	trustedProxyMTLSCAPath   string
	capsPolicyFlag           string
	eventWorkers             int
	enableMetrics            bool
	metricsPath              string
	logJSON                  bool
	browserNetwork           string

	// Runtime state (populated in main.init after flag parsing and
	// after conf/client/manager construction; reloadable via SIGHUP).
	sessions      *session.Map
	queue         *protect.Queue
	manager       service.Manager
	cli           *client.Client
	conf          *config.Config
	originChecker *protect.OriginChecker
	authenticator protect.Authenticator
	htpasswdAuth  *protect.HtpasswdAuthenticator
	sourceTrust   *protect.SourceTrust
	ggrHost       *ggr.Host

	startTime time.Time
}

// app is the single Server instance used throughout the package. It is
// populated during main.init() and becomes the receiver of every
// handler method. Tests reassign its fields to install fakes.
var app = &Server{
	sessions:  session.NewMap(),
	startTime: time.Now(),
}
