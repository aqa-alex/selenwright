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

	artifactHistoryDir          string
	artifactHistorySettingsPath string

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

var app = &Server{
	sessions:  session.NewMap(),
	startTime: time.Now(),
}

var testHooksEnabled bool
